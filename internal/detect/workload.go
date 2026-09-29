package detect

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// WorkloadUnavailable reports workloads that are below their desired
// availability, and workloads whose pods restart chronically.
//
// Availability comes from the controller's own status (desired vs ready) when
// it was collected: a Deployment scaled to 0 is not down, and a rolling update
// with one old pod not Ready is not an outage. Pods are only used to explain
// *why*. Older snapshots without controller status fall back to pod counts.
//
// All replicas down = IMPACTING; some down, or restarting/OOMKilled within the
// last hour = DEGRADED. Failed pods (evicted, node shutdown) are tombstones
// handled by the failed-pods detector, not counted as the workload being down.
type WorkloadUnavailable struct{}

func (WorkloadUnavailable) ID() string { return "workload-unavailable" }
func (WorkloadUnavailable) Description() string {
	return "Workloads below desired availability (from controller status), or restarting chronically, with the reason per pod"
}

// startupGrace avoids flagging pods that are simply still starting.
const startupGrace = 3 * time.Minute

// chronicRestarts: restart count at which a restart pattern is chronic, not a blip.
const chronicRestarts = 10

type podProblem struct {
	pod     model.Pod
	reason  string // short machine-ish reason: CrashLoopBackOff, ReadinessProbe, Unschedulable, ...
	detail  string
	since   time.Time
	chronic bool // restart problems only
}

type wlGroup struct {
	ns       string
	ref      model.OwnerRef
	active   int // non-terminal pods
	problems []podProblem
	restarts []podProblem
}

func (d WorkloadUnavailable) Detect(ix *model.Index) []Finding {
	now := ix.S.CapturedAt
	groups := map[string]*wlGroup{}
	var order []string
	group := func(ns string, ref model.OwnerRef) *wlGroup {
		k := ns + "|" + ref.Kind + "|" + ref.Name
		g, ok := groups[k]
		if !ok {
			g = &wlGroup{ns: ns, ref: ref}
			groups[k] = g
			order = append(order, k)
		}
		return g
	}

	for _, p := range ix.S.Pods {
		if p.Phase == "Succeeded" || p.Phase == "Failed" {
			continue // terminal pods: not part of current availability (see failed-pods)
		}
		g := group(p.Namespace, p.Workload())
		g.active++
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
		if rp, ok := restartProblem(now, p); ok {
			g.restarts = append(g.restarts, rp)
		}
	}
	// controllers that want pods but have none at all (quota, admission webhook, PSA, ...)
	for _, w := range ix.S.Workloads {
		if w.Desired > 0 && w.Ready < w.Desired {
			group(w.Namespace, model.OwnerRef{Kind: w.Kind, Name: w.Name})
		}
	}

	var out []Finding
	for _, k := range order {
		g := groups[k]
		st, known := ix.Workload(g.ns, g.ref)
		if known {
			if st.Desired == 0 {
				continue // scaled to zero on purpose: nothing is down
			}
			missing := st.Desired - st.Ready
			starting := len(g.problems) == 0 && g.active > 0 // not-ready pods are all inside the startup grace
			brandNew := g.active == 0 && now.Sub(st.CreatedAt) < startupGrace
			if missing > 0 && !starting && !brandNew {
				tier := TierDegraded
				if st.Ready == 0 {
					tier = TierImpacting
				}
				out = append(out, d.availability(ix, now, g, int(st.Ready), int(st.Desired), true, tier))
				continue
			}
			// fully available per the controller: stray not-Ready pods are rollout leftovers
		} else if len(g.problems) > 0 {
			tier := TierDegraded
			if len(g.problems) == g.active {
				tier = TierImpacting
			}
			out = append(out, d.availability(ix, now, g, g.active-len(g.problems), g.active, false, tier))
			continue
		}
		if len(g.restarts) > 0 {
			out = append(out, d.restartFinding(ix, now, g))
		}
	}
	return out
}

// restartProblem flags a Ready pod whose container restarted within the last
// hour after an OOMKill, or has restarted chronically.
func restartProblem(now time.Time, p model.Pod) (podProblem, bool) {
	for _, c := range p.Containers {
		if c.Init || c.LastTermAt == nil || now.Sub(*c.LastTermAt) >= time.Hour {
			continue
		}
		oom := c.LastTermReason == "OOMKilled"
		chronic := c.Restarts >= chronicRestarts
		if !oom && !chronic {
			continue
		}
		reason := "Restarting"
		if oom {
			reason = "OOMKilled"
		}
		last := c.LastTermReason
		if last == "" {
			last = fmt.Sprintf("exit %d", c.LastTermExit)
		}
		return podProblem{pod: p, reason: reason, since: *c.LastTermAt, chronic: chronic,
			detail: fmt.Sprintf("container %s restarted %s ago (%s), restarts=%d; Ready again now",
				c.Name, humanDuration(now.Sub(*c.LastTermAt)), last, c.Restarts)}, true
	}
	return podProblem{}, false
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

func dominantReason(probs []podProblem) string {
	counts := map[string]int{}
	for _, pp := range probs {
		counts[pp.reason]++
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
	return reasons[0]
}

func podEvidence(probs []podProblem) (ev []string, affected []ObjectRef, sinceT *time.Time) {
	for i, pp := range probs {
		affected = append(affected, podRef(pp.pod))
		sinceT = earliest(sinceT, pp.since)
		if i == 5 {
			ev = append(ev, fmt.Sprintf("... %d more pods", len(probs)-5))
			continue
		}
		if i > 5 {
			continue
		}
		node := pp.pod.Node
		if node == "" {
			node = "unscheduled"
		}
		ev = append(ev, fmt.Sprintf("%s [%s, node %s]: %s", pp.pod.Name, pp.reason, node, pp.detail))
	}
	return
}

// availability builds the finding for a workload below its desired replicas.
func (d WorkloadUnavailable) availability(ix *model.Index, now time.Time, g *wlGroup, ready, desired int, fromController bool, tier Tier) Finding {
	w, ns := g.ref, g.ns
	dominant := "NoPods"
	if len(g.problems) > 0 {
		dominant = dominantReason(g.problems)
	} else if g.active > 0 {
		dominant = "NotReady"
	}
	ev, affected, sinceT := podEvidence(g.problems)
	src := "pod count"
	if fromController {
		src = "controller status"
	}
	ev = append([]string{fmt.Sprintf("%d/%d ready (from %s)", ready, desired, src)}, ev...)

	var summary string
	switch {
	case dominant == "NoPods":
		summary = fmt.Sprintf("The %s wants %d pod(s) but none exist. Pod creation is being rejected: check the controller's events for quota, admission webhook or pod security errors.", strings.ToLower(w.Kind), desired)
		ev = append(ev, "no pods exist for this workload")
	case tier == TierImpacting:
		summary = fmt.Sprintf("No replica of %s %s is ready. Dominant reason: %s.", strings.ToLower(w.Kind), w.Name, reasonText(dominant))
	default:
		summary = fmt.Sprintf("%d of %d replicas of %s %s are ready. Dominant reason: %s.", ready, desired, strings.ToLower(w.Kind), w.Name, reasonText(dominant))
	}
	if sinceT != nil {
		ev = append(ev, "down since about "+since(now, sinceT)+" ago")
	}
	plan := noPodsPlan(w, ns)
	if len(g.problems) > 0 {
		plan = workloadPlan(dominant, ns, g.problems[0].pod)
	}
	return Finding{
		ID:          Fingerprint(d.ID(), ix.S.Cluster, ns, w.Kind, w.Name),
		Tier:        tier,
		Title:       fmt.Sprintf("%s %s/%s: %d/%d ready (%s)", w.Kind, ns, w.Name, ready, desired, dominant),
		Summary:     summary,
		Subject:     ObjectRef{Kind: w.Kind, Namespace: ns, Name: w.Name},
		Affected:    affected,
		Evidence:    ev,
		Since:       sinceT,
		Remediation: plan,
		PatternKey:  "workload-unavailable|" + w.Kind + "|" + w.Name + "|" + dominant,
	}
}

// restartFinding: the workload is available, but pods keep restarting.
func (d WorkloadUnavailable) restartFinding(ix *model.Index, now time.Time, g *wlGroup) Finding {
	w, ns := g.ref, g.ns
	dominant := dominantReason(g.restarts)
	ev, affected, _ := podEvidence(g.restarts)
	var maxRestarts int32
	chronic := false
	for _, pp := range g.restarts {
		chronic = chronic || pp.chronic
		for _, c := range pp.pod.Containers {
			if c.Restarts > maxRestarts {
				maxRestarts = c.Restarts
			}
		}
	}
	kind := "OOMKills"
	if dominant != "OOMKilled" {
		kind = "restarts"
	}
	var title, summary string
	if chronic {
		title = fmt.Sprintf("%s %s/%s: chronic %s (%d restarts)", w.Kind, ns, w.Name, kind, maxRestarts)
		summary = fmt.Sprintf("Pods keep restarting (up to %d times) and restarted again within the last hour. ", maxRestarts)
		if dominant == "OOMKilled" {
			summary += "Each OOMKill drops in-flight work (for log shippers: buffered logs). The memory limit is too low for the steady-state load, or there is a leak."
		} else {
			summary += "The workload looks available only between crashes."
		}
	} else {
		title = fmt.Sprintf("%s %s/%s: recent %s", w.Kind, ns, w.Name, kind)
		summary = fmt.Sprintf("%d pod(s) restarted in the last hour (%s) and are Ready again.", len(g.restarts), reasonText(dominant))
	}
	sinceT := earliest(nil, g.restarts[0].since)
	ev = append(ev, "last restart "+since(now, sinceT)+" ago")
	return Finding{
		ID:          Fingerprint(d.ID(), ix.S.Cluster, ns, w.Kind, w.Name),
		Tier:        TierDegraded,
		Title:       title,
		Summary:     summary,
		Subject:     ObjectRef{Kind: w.Kind, Namespace: ns, Name: w.Name},
		Affected:    affected,
		Evidence:    ev,
		Remediation: workloadPlan(dominant, ns, g.restarts[0].pod),
		PatternKey:  "workload-unavailable|" + w.Kind + "|" + w.Name + "|" + dominant,
	}
}

func noPodsPlan(w model.OwnerRef, ns string) *Plan {
	return &Plan{Steps: []Step{
		{Description: "Read the controller's conditions and events", Command: fmt.Sprintf("kubectl describe %s %s -n %s", strings.ToLower(w.Kind), w.Name, ns)},
		{Description: "Pod creation errors land on the ReplicaSet/StatefulSet (quota exceeded, webhook denied, pod security)", Command: fmt.Sprintf("kubectl get events -n %s --field-selector reason=FailedCreate", ns)},
		{Description: "Check namespace quota", Command: "kubectl describe resourcequota -n " + ns},
	}, Verify: []string{fmt.Sprintf("kubectl get %s %s -n %s", strings.ToLower(w.Kind), w.Name, ns)}}
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
	case "NotReady":
		return "pods are not Ready and nothing on the pod explains it (check the node they run on)"
	case "Restarting":
		return "containers exit and are restarted"
	case "NoPods":
		return "no pods could be created"
	}
	return strings.ToLower(r)
}

func workloadPlan(reason, ns string, p model.Pod) *Plan {
	pl := &Plan{}
	add := func(desc, cmd string) { pl.Steps = append(pl.Steps, Step{Description: desc, Command: cmd}) }
	switch reason {
	case "CrashLoopBackOff", "OOMKilled", "Restarting":
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
