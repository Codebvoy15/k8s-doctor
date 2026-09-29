package detect

import (
	"fmt"
	"strings"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// NodeUnhealthy reports NotReady nodes (IMPACTING when pods are scheduled on
// them), resource pressure (DEGRADED), and nodes cordoned for a long time (INFO).
type NodeUnhealthy struct{}

func (NodeUnhealthy) ID() string { return "node-unhealthy" }
func (NodeUnhealthy) Description() string {
	return "Nodes that are NotReady, under memory/disk/PID pressure, or cordoned"
}

func (d NodeUnhealthy) Detect(ix *model.Index) []Finding {
	now := ix.S.CapturedAt
	podsOn := map[string][]model.Pod{}
	for _, p := range ix.S.Pods {
		if p.Node != "" && p.Phase != "Succeeded" && p.Phase != "Failed" {
			podsOn[p.Node] = append(podsOn[p.Node], p)
		}
	}
	var out []Finding
	for _, n := range ix.S.Nodes {
		pods := podsOn[n.Name]
		ready := condition(n, "Ready")
		var pressures []model.Condition
		for _, c := range n.Conditions {
			if (c.Type == "MemoryPressure" || c.Type == "DiskPressure" || c.Type == "PIDPressure") && c.Status == "True" {
				pressures = append(pressures, c)
			}
		}

		switch {
		case !n.Ready:
			f := d.base(ix, n, "NotReady", pods)
			f.Tier = TierInfo
			if len(pods) > 0 {
				f.Tier = TierImpacting
			}
			f.blocks = f.Affected // every pod on a NotReady node is down because of it
			f.Title = fmt.Sprintf("Node %s NotReady (%d pods scheduled)", n.Name, len(pods))
			f.Summary = fmt.Sprintf("The node stopped reporting Ready: %s. Pods on it are not serving and will be evicted after the toleration timeout.", orDefault(ready.Message, ready.Reason))
			if !ready.Since.IsZero() {
				f.Since = &ready.Since
				f.Evidence = append(f.Evidence, "NotReady for "+humanDuration(now.Sub(ready.Since)))
			}
			f.Evidence = append(f.Evidence, fmt.Sprintf("Ready=%s reason=%s: %s", orDefault(ready.Status, "Unknown"), ready.Reason, truncate(ready.Message, 160)))
			f.Remediation = &Plan{Steps: []Step{
				{Description: "Deep node diagnosis (kubelet, EC2/Spot, Karpenter, cgroup)", Command: "./k8s-doctor node pressure -o json"},
				{Description: "Check the EC2 instance state and status checks in the AWS console (Spot interruption, impaired instance)"},
				{Description: "If the instance is gone or impaired, cordon and drain so pods reschedule", Command: fmt.Sprintf("kubectl drain %s --ignore-daemonsets --delete-emptydir-data", n.Name), Mutating: true},
			}, Verify: []string{"kubectl get node " + n.Name}}
			out = append(out, f)

		case len(pressures) > 0:
			c := pressures[0]
			f := d.base(ix, n, c.Type, pods)
			f.Tier = TierDegraded
			f.Title = fmt.Sprintf("Node %s under %s", n.Name, c.Type)
			f.Summary = fmt.Sprintf("The kubelet reports %s: %s. It will evict pods to recover.", c.Type, truncate(c.Message, 160))
			for _, pc := range pressures {
				f.Evidence = append(f.Evidence, fmt.Sprintf("%s=True: %s", pc.Type, truncate(pc.Message, 160)))
				if !pc.Since.IsZero() {
					f.Since = earliest(f.Since, pc.Since)
				}
			}
			f.Remediation = &Plan{Steps: []Step{
				{Description: "Find which pods and paths consume the resource", Command: "./k8s-doctor node pressure -o json"},
				{Description: "Current usage", Command: "kubectl top node " + n.Name},
			}, Verify: []string{"kubectl describe node " + n.Name + " | grep -A8 Conditions"}}
			out = append(out, f)

		case n.Unschedulable:
			// How long it has been cordoned is not in the API; the findings store (phase 2) tracks it.
			f := d.base(ix, n, "Cordoned", pods)
			f.Tier = TierInfo
			f.Title = fmt.Sprintf("Node %s is cordoned (%d pods still on it)", n.Name, len(pods))
			f.Summary = "The node is marked unschedulable. If a drain or maintenance was abandoned, capacity is being wasted."
			f.Evidence = append(f.Evidence, "spec.unschedulable=true")
			f.Remediation = &Plan{Steps: []Step{
				{Description: "Finish the drain or uncordon, depending on why it was cordoned", Command: "kubectl uncordon " + n.Name, Mutating: true},
			}}
			out = append(out, f)
		}
	}
	return out
}

func (d NodeUnhealthy) base(ix *model.Index, n model.Node, cond string, pods []model.Pod) Finding {
	var affected []ObjectRef
	for _, p := range pods {
		affected = append(affected, podRef(p))
	}
	var ev []string
	var meta []string
	for _, k := range []string{"node.kubernetes.io/instance-type", "karpenter.sh/capacity-type", "karpenter.sh/nodepool", "topology.kubernetes.io/zone"} {
		if v := n.Labels[k]; v != "" {
			meta = append(meta, k[strings.LastIndex(k, "/")+1:]+"="+v)
		}
	}
	if n.OSImage != "" {
		meta = append(meta, "os="+n.OSImage)
	}
	if len(meta) > 0 {
		ev = append(ev, strings.Join(meta, " "))
	}
	return Finding{
		ID:         Fingerprint(d.ID(), ix.S.Cluster, n.Name, cond),
		Subject:    ObjectRef{Kind: model.KindNode, Name: n.Name},
		Affected:   affected,
		Evidence:   ev,
		PatternKey: "node-unhealthy|" + cond,
	}
}

func condition(n model.Node, t string) model.Condition {
	for _, c := range n.Conditions {
		if c.Type == t {
			return c
		}
	}
	return model.Condition{Type: t}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
