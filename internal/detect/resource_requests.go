package detect

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// ResourceRequests reports workloads whose real usage is far above what they
// request, including workloads that request nothing at all.
//
// This is the usual root cause behind "high node memory" alerts: the scheduler
// believes the workload needs little, keeps adding pods to the same nodes, and
// the node runs out. Pods without requests are also BestEffort, the first to be
// evicted when it does.
//
// Thresholds keep it quiet on the long tail: a sidecar using 40Mi without a
// request is not worth anyone's time. Only gaps that move a node are reported.
type ResourceRequests struct{}

func (ResourceRequests) ID() string { return "resource-requests" }
func (ResourceRequests) Description() string {
	return "Workloads using far more CPU/memory than they request (or requesting none), so nodes are packed blind"
}

const (
	// memSignificant: memory usage below this is not reported even without a request.
	memSignificant = giB
	// cpuSignificant: CPU usage (millicores) below this is not reported.
	cpuSignificant = 1000
	// underRequestFactor: usage at or above this multiple of the request is under-requested.
	underRequestFactor = 2
)

type rrIssue struct {
	key  string // none | under
	used int64  // worst pod's usage
	req  int64  // that pod's request
	cap  int64  // that pod's node allocatable (0 if unknown)
}

func (d ResourceRequests) Detect(ix *model.Index) []Finding {
	type grp struct {
		ns   string
		ref  model.OwnerRef
		pods []usagePod
	}
	groups := map[string]*grp{}
	var order []string
	for _, up := range runningWithUsage(ix) {
		w := up.pod.Workload()
		k := up.pod.Namespace + "|" + w.Kind + "|" + w.Name
		if groups[k] == nil {
			groups[k] = &grp{ns: up.pod.Namespace, ref: w}
			order = append(order, k)
		}
		groups[k].pods = append(groups[k].pods, up)
	}

	var out []Finding
	for _, k := range order {
		g := groups[k]
		mem := worstGap(g.pods, func(u usagePod) (int64, int64, int64) { return u.mem, u.reqMem, u.allocMem }, memSignificant)
		cpu := worstGap(g.pods, func(u usagePod) (int64, int64, int64) { return u.cpu, u.reqCPU, u.allocCPU }, cpuSignificant)
		if mem == nil && cpu == nil {
			continue
		}
		out = append(out, d.finding(ix, g.ns, g.ref, g.pods, mem, cpu))
	}
	return out
}

// worstGap finds the pod with the largest usage-over-request and classifies it.
func worstGap(pods []usagePod, get func(usagePod) (used, req, capacity int64), significant int64) *rrIssue {
	var best *rrIssue
	var bestGap int64 = -1
	for _, u := range pods {
		used, req, capacity := get(u)
		if used < significant {
			continue
		}
		var key string
		switch {
		case req == 0:
			key = "none"
		case used >= req*underRequestFactor && used-req >= significant:
			key = "under"
		default:
			continue
		}
		if gap := used - req; gap > bestGap {
			bestGap = gap
			best = &rrIssue{key: key, used: used, req: req, cap: capacity}
		}
	}
	return best
}

func (d ResourceRequests) finding(ix *model.Index, ns string, w model.OwnerRef, pods []usagePod, mem, cpu *rrIssue) Finding {
	qos := pods[0].pod.QOS
	var parts []string
	switch {
	case mem != nil && cpu != nil && mem.key == "none" && cpu.key == "none":
		parts = append(parts, fmt.Sprintf("uses %s memory and %s of CPU with no requests", fmtMem(mem.used), fmtCPU(cpu.used)))
	default:
		if mem != nil {
			if mem.key == "none" {
				parts = append(parts, fmt.Sprintf("uses %s memory with no memory request", fmtMem(mem.used)))
			} else {
				parts = append(parts, fmt.Sprintf("uses %s memory, requests %s", fmtMem(mem.used), fmtMem(mem.req)))
			}
		}
		if cpu != nil {
			if cpu.key == "none" {
				parts = append(parts, fmt.Sprintf("uses %s with no CPU request", fmtCPU(cpu.used)))
			} else {
				parts = append(parts, fmt.Sprintf("uses %s CPU, requests %s", fmtCPU(cpu.used), fmtCPU(cpu.req)))
			}
		}
	}
	title := fmt.Sprintf("%s %s/%s: %s", w.Kind, ns, w.Name, strings.Join(parts, "; "))
	if qos == "BestEffort" {
		title += " (BestEffort)"
	}

	var why []string
	if mem != nil {
		needs := "no memory at all"
		if mem.req > 0 {
			needs = fmtMem(mem.req) + " of memory"
		}
		why = append(why, fmt.Sprintf("The scheduler places pods by their requests, so it sees this workload as needing %s while it uses %s. The nodes it lands on get packed past their capacity, which is what node memory alerts report.", needs, fmtMem(mem.used)))
		if qos == "BestEffort" {
			why = append(why, "With no requests at all the pods are BestEffort: under node memory pressure the kubelet evicts them first.")
		} else {
			why = append(why, "Pods using more memory than they request are evicted before pods that stay within their request.")
		}
	}
	if cpu != nil {
		why = append(why, "CPU above the request is not reserved: it competes with neighbouring pods, is throttled when the node is busy, and an HPA cannot scale on utilization without a request.")
	}

	var ev []string
	p0 := pods[0]
	reqLine := fmt.Sprintf("requests per pod: memory %s, cpu %s; memory limit %s; QoS %s", fmtMem(p0.reqMem), fmtCPU(p0.reqCPU), fmtMem(p0.limMem), orDefault(qos, "unknown"))
	ev = append(ev, reqLine)
	noRequests := p0.reqMem == 0 && p0.reqCPU == 0
	if noRequests && qos != "" && qos != "BestEffort" {
		ev = append(ev, fmt.Sprintf("the app containers set no requests; QoS is %s only because an init container sets some", qos))
	}
	sorted := append([]usagePod(nil), pods...)
	sort.SliceStable(sorted, func(i, j int) bool {
		gi, gj := sorted[i].mem-sorted[i].reqMem, sorted[j].mem-sorted[j].reqMem
		if gi != gj {
			return gi > gj
		}
		return sorted[i].pod.Name < sorted[j].pod.Name
	})
	var affected []ObjectRef
	var contrib []string
	var maxMem, sumCPU int64
	for i, u := range sorted {
		affected = append(affected, podRef(u.pod))
		if u.mem > maxMem {
			maxMem = u.mem
		}
		sumCPU += u.cpu
		if mem != nil && u.mem-u.reqMem >= memSignificant {
			contrib = append(contrib, "memory:"+u.pod.Namespace+"/"+u.pod.Name)
		}
		if cpu != nil && u.cpu-u.reqCPU >= cpuSignificant {
			contrib = append(contrib, "cpu:"+u.pod.Namespace+"/"+u.pod.Name)
		}
		if i < 4 {
			node := u.pod.Node
			if u.nodeKnown && u.allocMem > 0 {
				node += fmt.Sprintf(", %d%% of its memory", pct(u.mem, u.allocMem))
			}
			ev = append(ev, fmt.Sprintf("%s [node %s]: memory %s (requested %s), cpu %s (requested %s)", u.pod.Name, node, fmtMem(u.mem), fmtMem(u.reqMem), fmtCPU(u.cpu), fmtCPU(u.reqCPU)))
		}
	}
	if len(sorted) > 4 {
		ev = append(ev, fmt.Sprintf("... %d more replicas", len(sorted)-4))
	}
	st, known := ix.Workload(ns, w)
	if line := ownerLine(st, known); line != "" {
		ev = append(ev, line)
	}
	ev = append(ev, "usage is one metrics-server sample (memory = working set, includes active page cache)")

	sz := sizing{}
	if mem != nil {
		sz.memRequest = suggestMem(maxMem)
	}
	if cpu != nil {
		sz.cpuRequest = suggestCPU(sumCPU / int64(len(sorted)))
	}
	issue := "cpu-" + cpuKey(cpu)
	if mem != nil {
		issue = "memory-" + mem.key
	}
	var measures []Measure
	if mem != nil {
		measures = append(measures, Measure{Resource: "memory", Used: mem.used, Requested: mem.req, Capacity: mem.cap})
	}
	if cpu != nil {
		measures = append(measures, Measure{Resource: "cpu", Used: cpu.used, Requested: cpu.req, Capacity: cpu.cap})
	}
	note := ""
	switch {
	case qos == "BestEffort":
		note = "BestEffort"
	case noRequests:
		note = "no requests on app containers"
	}
	return Finding{
		ID:          Fingerprint(d.ID(), ix.S.Cluster, ns, w.Kind, w.Name),
		Tier:        TierLatent,
		Title:       title,
		Summary:     strings.Join(why, " "),
		Subject:     ObjectRef{Kind: w.Kind, Namespace: ns, Name: w.Name},
		Affected:    affected,
		Evidence:    ev,
		Remediation: resizePlan(ix, ns, w, sz),
		PatternKey:  "resource-requests|" + issue + "|" + w.Kind + "|" + w.Name,
		Measures:    measures,
		FixIn:       fixTarget(st, known, w),
		Note:        note,
		Proposal:    sz.yaml(),
		contrib:     contrib,
		minor:       isMinor(mem, minorMemGap, 10) && isMinor(cpu, minorCPUGap, 25),
	}
}

const (
	// A gap is minor when it is below this AND below the given percent of the node.
	minorMemGap = 4 * giB
	minorCPUGap = 2000
)

// isMinor: no issue for this resource, or a gap that is small both in absolute
// terms and relative to the node the pod runs on.
func isMinor(i *rrIssue, absolute int64, nodePercent int64) bool {
	if i == nil {
		return true
	}
	gap := i.used - i.req
	if gap >= absolute {
		return false
	}
	return i.cap <= 0 || gap*100 < i.cap*nodePercent
}

func cpuKey(i *rrIssue) string {
	if i == nil {
		return ""
	}
	return i.key
}
