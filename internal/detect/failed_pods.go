package detect

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// FailedPods reports pods left in phase Failed (evicted, node shutdown, ...).
// They are tombstones: the controller has already replaced them, so they are
// never counted as the workload being down. What matters is *why* they failed:
//
//   - evicted for exceeding the workload's OWN limit (emptyDir sizeLimit,
//     container ephemeral-storage limit): LATENT. It recurs every time the
//     workload runs long enough, including after it is scaled back up.
//   - evicted because the node ran low on a resource: INFO (a node signal).
//   - anything else (NodeShutdown, UnexpectedAdmissionError, ...): INFO.
type FailedPods struct{}

func (FailedPods) ID() string { return "failed-pods" }
func (FailedPods) Description() string {
	return "Evicted/failed pods left behind, classified by cause (own storage limit vs node pressure)"
}

var (
	emptyDirLimitRE  = regexp.MustCompile(`EmptyDir volume "([^"]+)" exceeds the limit "([^"]+)"`)
	containerLimitRE = regexp.MustCompile(`[Cc]ontainer ([^ ]+) exceeded its local ephemeral storage limit "([^"]+)"`)
)

type failClass struct {
	key    string // stable class for grouping and pattern keys
	label  string // human label for titles
	tier   Tier
	ownCap bool // the workload's own limit (recurs)
}

func classifyFailed(p model.Pod) failClass {
	msg := p.Message
	switch {
	case p.Reason == "Evicted" && emptyDirLimitRE.MatchString(msg):
		m := emptyDirLimitRE.FindStringSubmatch(msg)
		return failClass{key: "emptydir-limit", label: fmt.Sprintf("emptyDir %q exceeded its %s limit", m[1], m[2]), tier: TierLatent, ownCap: true}
	case p.Reason == "Evicted" && (containerLimitRE.MatchString(msg) || strings.Contains(msg, "exceeds the total limit of containers")):
		return failClass{key: "ephemeral-limit", label: "ephemeral storage exceeded the container limit", tier: TierLatent, ownCap: true}
	case p.Reason == "Evicted" && strings.Contains(msg, "The node was low on resource"):
		res := "a resource"
		if i := strings.Index(msg, "low on resource: "); i >= 0 {
			res = strings.TrimRight(strings.Fields(msg[i+len("low on resource: "):])[0], ".")
		}
		return failClass{key: "node-pressure", label: "node was low on " + res, tier: TierInfo}
	case p.Reason != "":
		return failClass{key: strings.ToLower(p.Reason), label: p.Reason, tier: TierInfo}
	}
	return failClass{key: "failed", label: "failed", tier: TierInfo}
}

func (d FailedPods) Detect(ix *model.Index) []Finding {
	now := ix.S.CapturedAt
	type grp struct {
		ns   string
		ref  model.OwnerRef
		cls  failClass
		pods []model.Pod
	}
	groups := map[string]*grp{}
	var order []string
	for _, p := range ix.S.Pods {
		if p.Phase != "Failed" || p.Owner.Kind == "Job" {
			continue // Jobs keep failed pods by design (backoffLimit); a job detector is separate
		}
		w := p.Workload()
		c := classifyFailed(p)
		k := p.Namespace + "|" + w.Kind + "|" + w.Name + "|" + c.key
		g, ok := groups[k]
		if !ok {
			g = &grp{ns: p.Namespace, ref: w, cls: c}
			groups[k] = g
			order = append(order, k)
		}
		g.pods = append(g.pods, p)
	}

	var out []Finding
	for _, k := range order {
		g := groups[k]
		sort.Slice(g.pods, func(i, j int) bool { return g.pods[i].CreatedAt.Before(g.pods[j].CreatedAt) })
		w, ns := g.ref, g.ns
		n := len(g.pods)

		scale := ""
		if st, ok := ix.Workload(ns, w); ok {
			if st.Desired == 0 {
				scale = fmt.Sprintf("It is scaled to 0 now, so nothing is down, but it will be evicted again when scaled back up.")
			} else {
				scale = fmt.Sprintf("It runs %d/%d ready now; running replicas will be evicted again once they grow past the limit.", st.Ready, st.Desired)
			}
		}
		var summary string
		if g.cls.ownCap {
			summary = fmt.Sprintf("%d pod(s) were evicted because the workload outgrew its own storage limit (%s). This recurs: the app writes to local disk faster than it is cleaned up. %s", n, g.cls.label, scale)
		} else {
			summary = fmt.Sprintf("%d pod(s) left in Failed state (%s). The controller replaced them; these are leftovers.", n, g.cls.label)
		}

		var ev []string
		var affected []ObjectRef
		for i, p := range g.pods {
			affected = append(affected, podRef(p))
			if i < 3 {
				ev = append(ev, fmt.Sprintf("%s [%s, node %s, created %s ago]: %s", p.Name, orDefault(p.Reason, p.Phase), orDefault(p.Node, "?"), humanDuration(now.Sub(p.CreatedAt)), truncate(p.Message, 160)))
			}
		}
		if n > 3 {
			ev = append(ev, fmt.Sprintf("... %d more", n-3))
		}

		plan := &Plan{}
		if g.cls.ownCap {
			plan.Steps = append(plan.Steps,
				Step{Description: "Find what fills the volume (usually file logging): log to stdout, rotate, or ship logs off the pod"},
				Step{Description: "Or raise the emptyDir sizeLimit / ephemeral-storage limit if the usage is legitimate", Mutating: true},
			)
		} else if g.cls.key == "node-pressure" {
			plan.Steps = append(plan.Steps, Step{Description: "Check the node's pressure findings and which pods consumed the resource", Command: "./k8s-doctor node pressure -o json"})
		}
		plan.Steps = append(plan.Steps, Step{
			Description: "Clean up the leftover Failed pods (they hold no resources, only clutter)",
			Command:     fmt.Sprintf("kubectl delete pod -n %s --field-selector=status.phase=Failed", ns),
			Mutating:    true,
		})

		out = append(out, Finding{
			ID:          Fingerprint(d.ID(), ix.S.Cluster, ns, w.Kind, w.Name, g.cls.key),
			Tier:        g.cls.tier,
			Title:       fmt.Sprintf("%s %s/%s: %d evicted/failed pod(s) (%s)", w.Kind, ns, w.Name, n, g.cls.label),
			Summary:     strings.TrimSpace(summary),
			Subject:     ObjectRef{Kind: w.Kind, Namespace: ns, Name: w.Name},
			Affected:    affected,
			Evidence:    ev,
			Remediation: plan,
			PatternKey:  "failed-pods|" + g.cls.key + "|" + w.Name,
		})
	}
	return out
}
