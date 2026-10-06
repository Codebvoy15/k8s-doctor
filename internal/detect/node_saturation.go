package detect

import (
	"fmt"
	"sort"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// NodeSaturation explains a node's high CPU or memory: how much of the usage is
// covered by requests, and which pods use what nobody requested.
//
// Memory is LATENT: nothing is down yet, but the kubelet evicts pods (BestEffort
// first) or the kernel OOM-kills processes once it runs out. CPU is INFO: it is
// compressible, so a busy node throttles pods but evicts none. In both cases
// the node is the symptom; the workload findings it is linked to are the causes.
type NodeSaturation struct{}

func (NodeSaturation) ID() string { return "node-saturation" }
func (NodeSaturation) Description() string {
	return "Nodes near their CPU/memory allocatable, with the pods using capacity they never requested"
}

const (
	nodeMemHigh = 85 // percent of allocatable
	nodeCPUHigh = 85
	// a pod is listed as a contributor when it uses at least this much beyond its request
	contribMem = 256 * miB
	contribCPU = 250
	maxContrib = 5
)

func (d NodeSaturation) Detect(ix *model.Index) []Finding {
	podsOn := map[string][]model.Pod{}
	for _, p := range ix.S.Pods {
		if p.Node != "" && p.Phase != "Succeeded" && p.Phase != "Failed" {
			podsOn[p.Node] = append(podsOn[p.Node], p)
		}
	}
	var out []Finding
	for _, n := range ix.S.Nodes {
		u, ok := ix.NodeUsage(n.Name)
		if !ok || n.Allocatable == nil {
			continue
		}
		if p := pct(u.MemBytes, n.Allocatable.MemBytes); p >= nodeMemHigh {
			out = append(out, d.finding(ix, n, podsOn[n.Name], "memory", u.MemBytes, n.Allocatable.MemBytes))
		}
		if p := pct(u.CPUMilli, n.Allocatable.CPUMilli); p >= nodeCPUHigh {
			out = append(out, d.finding(ix, n, podsOn[n.Name], "cpu", u.CPUMilli, n.Allocatable.CPUMilli))
		}
	}
	return out
}

type contributor struct {
	pod             model.Pod
	used, requested int64
}

func (d NodeSaturation) finding(ix *model.Index, n model.Node, pods []model.Pod, res string, used, alloc int64) Finding {
	format, threshold := fmtMem, contribMem
	if res == "cpu" {
		format, threshold = fmtCPU, contribCPU
	}

	var requested int64
	requestsKnown := true
	bestEffort := 0
	var contribs []contributor
	for _, p := range pods {
		rc, rm, _, _, recorded := p.Requests()
		if !recorded {
			requestsKnown = false
			continue
		}
		req := rm
		if res == "cpu" {
			req = rc
		}
		requested += req
		if p.QOS == "BestEffort" {
			bestEffort++
		}
		cpu, mem, ok := ix.PodUsage(p.Namespace, p.Name)
		if !ok {
			continue
		}
		u := mem
		if res == "cpu" {
			u = cpu
		}
		if u-req >= threshold {
			contribs = append(contribs, contributor{pod: p, used: u, requested: req})
		}
	}
	sort.SliceStable(contribs, func(i, j int) bool {
		gi, gj := contribs[i].used-contribs[i].requested, contribs[j].used-contribs[j].requested
		if gi != gj {
			return gi > gj
		}
		return contribs[i].pod.Name < contribs[j].pod.Name
	})

	var ev []string
	if meta := nodeMeta(n); meta != "" {
		ev = append(ev, meta)
	}
	usage := fmt.Sprintf("%s: %s used of %s allocatable (%d%%)", res, format(used), format(alloc), pct(used, alloc))
	if requestsKnown {
		usage += fmt.Sprintf("; requested %s (%d%%)", format(requested), pct(requested, alloc))
	}
	ev = append(ev, usage)
	var affected []ObjectRef
	var contrib []string
	var unrequested int64
	for i, c := range contribs {
		gap := c.used - c.requested
		unrequested += gap
		affected = append(affected, podRef(c.pod))
		contrib = append(contrib, res+":"+c.pod.Namespace+"/"+c.pod.Name)
		if i < maxContrib {
			qos := ""
			if c.pod.QOS == "BestEffort" {
				qos = ", BestEffort"
			}
			ev = append(ev, fmt.Sprintf("%s/%s: uses %s, requested %s (%s unrequested%s)", c.pod.Namespace, c.pod.Name, format(c.used), format(c.requested), format(gap), qos))
		}
	}
	if len(contribs) > maxContrib {
		ev = append(ev, fmt.Sprintf("... %d more pods above their request", len(contribs)-maxContrib))
	}

	f := Finding{
		ID:         Fingerprint(d.ID(), ix.S.Cluster, n.Name, res),
		Subject:    ObjectRef{Kind: model.KindNode, Name: n.Name},
		Affected:   affected,
		Evidence:   ev,
		PatternKey: "node-saturation|" + res,
		contrib:    contrib,
	}
	f.Title = fmt.Sprintf("Node %s %s at %d%% of allocatable", n.Name, res, pct(used, alloc))
	if requestsKnown {
		f.Title += fmt.Sprintf(", %d%% requested", pct(requested, alloc))
	}
	gapText := ""
	if requestsKnown && used > requested {
		gapText = fmt.Sprintf("%s of the %s in use is not covered by any request, so the scheduler kept placing pods here. ", format(used-requested), res)
	} else if len(contribs) > 0 {
		gapText = fmt.Sprintf("%d pod(s) use %s more than they request. ", len(contribs), format(unrequested))
	}
	if res == "memory" {
		f.Tier = TierLatent
		f.Summary = gapText + "When free memory falls below the kubelet's eviction threshold it evicts pods, BestEffort first"
		if bestEffort > 0 {
			f.Summary += fmt.Sprintf(" (%d on this node)", bestEffort)
		}
		f.Summary += "; a faster spike gets processes OOM-killed instead."
	} else {
		f.Tier = TierInfo
		f.Summary = gapText + "CPU is compressible: a busy node throttles its pods but evicts none. The workload findings linked to this node say which pods to resize or scale."
	}
	f.Remediation = &Plan{Steps: []Step{
		{Description: "Resize the workloads named above (see the resource-requests / cpu-hotspot findings linked to this node)"},
		{Description: "Compare requests and limits with what the node can hold", Command: fmt.Sprintf("kubectl describe node %s | grep -A12 'Allocated resources'", n.Name)},
		{Description: "All pods on the node", Command: fmt.Sprintf("kubectl get pods -A -o wide --field-selector spec.nodeName=%s", n.Name)},
	}, Verify: []string{"kubectl top node " + n.Name}}
	return f
}
