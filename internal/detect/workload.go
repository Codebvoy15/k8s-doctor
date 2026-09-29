package detect

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// WorkloadUnavailable groups not-Ready pods by workload (Deployment,
// StatefulSet, DaemonSet, ...) and explains why each pod is down.
// All replicas down = IMPACTING; some down, or recently OOMKilled = DEGRADED.
type WorkloadUnavailable struct{}

func (WorkloadUnavailable) ID() string { return "workload-unavailable" }
func (WorkloadUnavailable) Description() string {
	return "Workloads with pods that are not Ready, grouped by workload, with the reason per pod"
}

// startupGrace avoids flagging pods that are simply still starting.
const startupGrace = 3 * time.Minute

type podProblem struct {
	pod    model.Pod
	reason string // short machine-ish reason: CrashLoopBackOff, ReadinessProbe, Unschedulable, ...
	detail string
	since  time.Time
}

func (d WorkloadUnavailable) Detect(ix *model.Index) []Finding {
	now := ix.S.CapturedAt
	type wl struct {
		ns       string
		ref      model.OwnerRef
		total    int
		problems []podProblem
		oom      []podProblem
	}
	groups := map[string]*wl{}
	var order []string

	for _, p := range ix.S.Pods {
		if p.Phase == "Succeeded" || (p.Owner.Kind == "Job" && p.Phase == "Failed") {
			continue
		}
		w := p.Workload()
		k := p.Namespace + "|" + w.Kind + "|" + w.Name
		g, ok := groups[k]
		if !ok {
			g = &wl{ns: p.Namespace, ref: w}
			groups[k] = g
			order = append(order, k)
		}
		g.total++
		if p.DeletingSince != nil {
			continue // terminating pods are handled by rollout; stuck ones are a separate detector
		}
		if !p.Ready {
			if now.Sub(p.CreatedAt) < startupGrace {
				continue
			}
			g.problems = append(g.problems, diagnosePod(ix, p))
			continue
		}
		for _, c := range p.Containers {
			if c.LastTermReason == "OOMKilled" && c.LastTermAt != nil && now.Sub(*c.LastTermAt) < time.Hour {
				g.oom = append(g.oom, podProblem{pod: p, reason: "OOMKilled", since: *c.LastTermAt,
					detail: fmt.Sprintf("container %s OOMKilled %s ago (restarts=%d); Ready again now", c.Name, humanDuration(now.Sub(*c.LastTermAt)), c.Restarts)})
				break
			}
		}
	}

	var out []Finding
	for _, k := range order {
		g := groups[k]
		probs := g.problems
		tier := TierDegraded
		if len(probs) > 0 && len(probs) == g.total {
			tier = TierImpacting
		}
		if len(probs) == 0 {
			if len(g.oom) == 0 {
				continue
			}
			probs = g.oom
		}
		out = append(out, d.finding(ix, now, g.ns, g.ref, g.total, probs, tier))
	}
	return out
}

func diagnosePod(ix *model.Index, p model.Pod) podProblem {
	pp := podProblem{pod: p, since: p.CreatedAt}
	if p.Unschedulable != "" {
		pp.reason, pp.detail = "Unschedulable", truncate(p.Unschedulable, 200)
		return pp
	}
	for _, c := range p.Containers {
		// completed init containers are never Ready; they are not the problem
		if c.Ready || (c.Init && c.State == "terminated" && c.ExitCode == 0) {
			continue
		}
		switch c.Reason {
		case "CrashLoopBackOff":
			pp.reason = "CrashLoopBackOff"
			pp.detail = fmt.Sprintf("container %s crash-looping (restarts=%d, last exit %d %s)", c.Name, c.Restarts, c.LastTermExit, c.LastTermReason)
			if c.LastTermReason == "OOMKilled" {
				pp.reason = "OOMKilled"
			}
			if c.LastTermAt != nil {
				pp.since = *c.LastTermAt
			}
			return pp
		case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
			pp.reason = "ImagePull"
			pp.detail = fmt.Sprintf("container %s cannot pull %s: %s", c.Name, c.Image, truncate(c.Message, 160))
			return pp
		case "CreateContainerConfigError", "CreateContainerError":
			pp.reason = c.Reason
			pp.detail = fmt.Sprintf("container %s: %s", c.Name, truncate(c.Message, 180))
			return pp
		case "ContainerCreating", "PodInitializing":
			pp.reason = "Stuck" + c.Reason
			pp.detail = fmt.Sprintf("container %s stuck in %s", c.Name, c.Reason)
		}
	}
	// probe failures / mount failures show up as events
	var probe, mount *model.Event
	for _, e := range ix.PodEvents(p.Namespace, p.Name) {
		e := e
		if e.Type != "Warning" {
			continue
		}
		switch e.Reason {
		case "Unhealthy":
			if probe == nil || e.LastSeen.After(probe.LastSeen) {
				probe = &e
			}
		case "FailedMount", "FailedAttachVolume":
			if mount == nil || e.LastSeen.After(mount.LastSeen) {
				mount = &e
			}
		}
	}
	switch {
	case mount != nil && (pp.reason == "" || strings.HasPrefix(pp.reason, "Stuck")):
		pp.reason = "VolumeMount"
		pp.detail = fmt.Sprintf("%s x%d: %s", mount.Reason, mount.Count, truncate(mount.Message, 170))
		pp.since = mount.FirstSeen
	case probe != nil && pp.reason == "":
		pp.reason = "ProbeFailing"
		if strings.HasPrefix(probe.Message, "Readiness") {
			pp.reason = "ReadinessProbe"
		} else if strings.HasPrefix(probe.Message, "Liveness") {
			pp.reason = "LivenessProbe"
		}
		pp.detail = fmt.Sprintf("x%d: %s", probe.Count, truncate(probe.Message, 170))
		pp.since = probe.FirstSeen
	}
	if pp.reason == "" {
		pp.reason = "NotReady"
		pp.detail = fmt.Sprintf("phase=%s, no container or event explains it yet", p.Phase)
	}
	return pp
}

func (d WorkloadUnavailable) finding(ix *model.Index, now time.Time, ns string, w model.OwnerRef, total int, probs []podProblem, tier Tier) Finding {
	// dominant reason drives the title and the remediation
	counts := map[string]int{}
	var sinceT *time.Time
	var affected []ObjectRef
	for _, pp := range probs {
		counts[pp.reason]++
		sinceT = earliest(sinceT, pp.since)
		affected = append(affected, podRef(pp.pod))
	}
	reasons := make([]string, 0, len(counts))
	for r := range counts {
		reasons = append(reasons, r)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if counts[reasons[i]] != counts[reasons[j]] {
			return counts[reasons[i]] > counts[reasons[j]]
		}
		return reasons[i] < reasons[j]
	})
	dominant := reasons[0]

	unavailable := len(probs)
	var title, summary string
	if tier == TierDegraded && probs[0].reason == "OOMKilled" && probs[0].pod.Ready {
		title = fmt.Sprintf("%s %s/%s: recent OOMKills", w.Kind, ns, w.Name)
		summary = fmt.Sprintf("%d pod(s) were OOMKilled in the last hour and restarted. Memory limit is likely too low or usage spiked.", unavailable)
	} else {
		title = fmt.Sprintf("%s %s/%s: %d/%d pods unavailable (%s)", w.Kind, ns, w.Name, unavailable, total, dominant)
		if tier == TierImpacting {
			summary = fmt.Sprintf("Every pod of %s %s is down. Dominant reason: %s.", strings.ToLower(w.Kind), w.Name, reasonText(dominant))
		} else {
			summary = fmt.Sprintf("%d of %d pods of %s %s are down. Dominant reason: %s.", unavailable, total, strings.ToLower(w.Kind), w.Name, reasonText(dominant))
		}
	}

	var ev []string
	for i, pp := range probs {
		if i == 5 {
			ev = append(ev, fmt.Sprintf("... %d more pods", len(probs)-5))
			break
		}
		node := pp.pod.Node
		if node == "" {
			node = "unscheduled"
		}
		ev = append(ev, fmt.Sprintf("%s [%s, node %s]: %s", pp.pod.Name, pp.reason, node, pp.detail))
	}
	if sinceT != nil {
		ev = append(ev, "down since about "+since(now, sinceT)+" ago")
	}

	return Finding{
		ID:          Fingerprint(d.ID(), ix.S.Cluster, ns, w.Kind, w.Name),
		Tier:        tier,
		Title:       title,
		Summary:     summary,
		Subject:     ObjectRef{Kind: w.Kind, Namespace: ns, Name: w.Name},
		Affected:    affected,
		Evidence:    ev,
		Since:       sinceT,
		Remediation: workloadPlan(dominant, ns, probs[0].pod),
		PatternKey:  "workload-unavailable|" + w.Kind + "|" + w.Name + "|" + dominant,
	}
}

func reasonText(r string) string {
	switch r {
	case "CrashLoopBackOff":
		return "containers crash on start"
	case "OOMKilled":
		return "containers are killed for exceeding their memory limit"
	case "ImagePull":
		return "images cannot be pulled"
	case "Unschedulable":
		return "no node can fit or accept the pod"
	case "ReadinessProbe":
		return "readiness probe failing (the pod runs but receives no traffic)"
	case "LivenessProbe":
		return "liveness probe failing (the kubelet keeps restarting it)"
	case "VolumeMount":
		return "a volume cannot be mounted"
	case "CreateContainerConfigError":
		return "container config is invalid (usually a missing Secret/ConfigMap key)"
	}
	return strings.ToLower(r)
}

func workloadPlan(reason, ns string, p model.Pod) *Plan {
	pl := &Plan{}
	add := func(desc, cmd string) { pl.Steps = append(pl.Steps, Step{Description: desc, Command: cmd}) }
	switch reason {
	case "CrashLoopBackOff", "OOMKilled":
		add("Read the crash output from the previous container instance", fmt.Sprintf("kubectl logs %s -n %s --previous --tail=100", p.Name, ns))
		add("Check exit code, reason and limits", fmt.Sprintf("kubectl describe pod %s -n %s", p.Name, ns))
	case "ImagePull":
		add("Read the exact pull error (not found, unauthorized, or timeout)", fmt.Sprintf("kubectl describe pod %s -n %s", p.Name, ns))
		add("If unauthorized, check the pull secret findings for this namespace", "")
	case "Unschedulable":
		add("Read the scheduler message", fmt.Sprintf("kubectl describe pod %s -n %s", p.Name, ns))
		add("If capacity: check Karpenter NodeClaims and NodePool limits", "kubectl get nodeclaims -o wide")
	case "ReadinessProbe", "LivenessProbe", "ProbeFailing":
		add("See what the probe gets back (5xx usually means the app's own upstream is down)", fmt.Sprintf("kubectl logs %s -n %s --tail=100", p.Name, ns))
		add("Check backends in the namespace: empty endpoints mean a dependency is down", fmt.Sprintf("kubectl get endpoints -n %s", ns))
	case "VolumeMount":
		add("Read the mount error", fmt.Sprintf("kubectl describe pod %s -n %s", p.Name, ns))
		add("Check the PVC and whether the volume is attached elsewhere", fmt.Sprintf("kubectl get pvc -n %s", ns))
	default:
		add("Inspect the pod", fmt.Sprintf("kubectl describe pod %s -n %s", p.Name, ns))
	}
	pl.Verify = []string{fmt.Sprintf("kubectl get pods -n %s | grep %s", ns, p.Workload().Name)}
	return pl
}
