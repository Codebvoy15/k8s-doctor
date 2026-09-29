package detect

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// MissingReference finds pods that depend on a Secret, ConfigMap, PVC or
// ServiceAccount that does not exist: pod volumes, env, envFrom, pod
// imagePullSecrets, and imagePullSecrets inherited from the ServiceAccount.
//
// This is the "silent drift" class of incident: a rotated/unprovisioned pull
// secret, an operator that stopped creating its config secret. Running pods
// usually keep working (LATENT) until a restart or reschedule, when they fail
// to start. Pods already blocked by the missing object are IMPACTING.
type MissingReference struct{}

func (MissingReference) ID() string { return "missing-reference" }
func (MissingReference) Description() string {
	return "Pods depend on a Secret/ConfigMap/PVC/ServiceAccount that does not exist"
}

// existedMargin: a pod must have run this long before the first failure for us
// to infer the referenced object existed when it started.
const existedMargin = 15 * time.Minute

type missingGroup struct {
	ref      model.Ref
	ns       string
	vias     map[string]bool
	pods     []model.Pod
	blocked  []model.Pod
	since    *time.Time
	eventHit []string
}

func (d MissingReference) Detect(ix *model.Index) []Finding {
	now := ix.S.CapturedAt
	groups := map[string]*missingGroup{}
	var order []string

	for _, p := range ix.S.Pods {
		if p.Phase == "Succeeded" || p.Phase == "Failed" {
			continue
		}
		for _, r := range effectiveRefs(ix, p) {
			if r.Optional {
				continue
			}
			exists, known := ix.Exists(r.Kind, p.Namespace, r.Name)
			if !known || exists {
				continue // absence is only evidence when the kind was collected
			}
			k := p.Namespace + "|" + r.Kind + "|" + r.Name
			g, ok := groups[k]
			if !ok {
				g = &missingGroup{ref: r, ns: p.Namespace, vias: map[string]bool{}}
				groups[k] = g
				order = append(order, k)
			}
			g.vias[viaClass(r.Via)] = true
			newPod := len(g.pods) == 0 || g.pods[len(g.pods)-1].Name != p.Name
			if newPod {
				g.pods = append(g.pods, p)
			}
			// a pod can reference the same object several ways; it is blocked if any of them blocks it
			alreadyBlocked := len(g.blocked) > 0 && g.blocked[len(g.blocked)-1].Name == p.Name
			if !alreadyBlocked && blockedBy(p, r) {
				g.blocked = append(g.blocked, p)
			}
			if !newPod {
				continue // events for this pod were already scanned
			}
			for _, e := range ix.PodEvents(p.Namespace, p.Name) {
				if e.Type == "Warning" && strings.Contains(e.Message, r.Name) {
					g.since = earliest(g.since, e.FirstSeen)
					g.eventHit = append(g.eventHit, fmt.Sprintf("%s x%d on %s: %s", e.Reason, e.Count, p.Name, truncate(e.Message, 140)))
				}
			}
		}
	}

	var out []Finding
	for _, k := range order {
		g := groups[k]
		out = append(out, d.finding(ix, now, g))
	}
	return out
}

// effectiveRefs adds the refs a pod inherits from its ServiceAccount.
func effectiveRefs(ix *model.Index, p model.Pod) []model.Ref {
	refs := append([]model.Ref(nil), p.Refs...)
	if p.ServiceAccount != "" {
		if sa, ok := ix.ServiceAccount(p.Namespace, p.ServiceAccount); ok {
			for _, s := range sa.ImagePullSecrets {
				refs = append(refs, model.Ref{Kind: model.KindSecret, Name: s, Via: "serviceAccount:" + sa.Name + "/imagePullSecret"})
			}
		}
	}
	return refs
}

func viaClass(via string) string {
	switch {
	case strings.Contains(via, "imagePullSecret"):
		return "image pull secret"
	case strings.HasPrefix(via, "volume:"):
		return "volume " + strings.TrimPrefix(via, "volume:")
	case strings.HasPrefix(via, "envFrom"):
		return "envFrom"
	case strings.HasPrefix(via, "env:"):
		return "env var"
	case via == "serviceAccount":
		return "serviceAccountName"
	}
	return via
}

// blockedBy reports whether this pod is currently failing because of this ref.
func blockedBy(p model.Pod, r model.Ref) bool {
	if p.Ready {
		return false
	}
	for _, c := range p.Containers {
		switch {
		case strings.Contains(r.Via, "imagePullSecret"):
			if c.Reason == "ImagePullBackOff" || c.Reason == "ErrImagePull" {
				return true
			}
		case strings.HasPrefix(r.Via, "volume:") || r.Kind == model.KindPVC:
			if c.Reason == "ContainerCreating" || c.Reason == "PodInitializing" {
				return true
			}
		case strings.HasPrefix(r.Via, "env"):
			if c.Reason == "CreateContainerConfigError" {
				return true
			}
		}
	}
	// volumes/PVCs can also hold a pod in Pending before containers report state
	if (strings.HasPrefix(r.Via, "volume:") || r.Kind == model.KindPVC) && p.Phase == "Pending" && p.Unschedulable == "" {
		return true
	}
	return false
}

func (d MissingReference) finding(ix *model.Index, now time.Time, g *missingGroup) Finding {
	subject := ObjectRef{Kind: g.ref.Kind, Namespace: g.ns, Name: g.ref.Name}
	var vias []string
	for v := range g.vias {
		vias = append(vias, v)
	}
	sort.Strings(vias)

	tier := TierLatent
	if len(g.blocked) > 0 {
		tier = TierImpacting
	}
	var affected []ObjectRef
	for _, p := range g.pods {
		affected = append(affected, podRef(p))
	}

	title := fmt.Sprintf("Missing %s %s/%s referenced by %d pod(s)", g.ref.Kind, g.ns, g.ref.Name, len(g.pods))
	var summary string
	if tier == TierImpacting {
		summary = fmt.Sprintf("%d pod(s) cannot start because %s %q does not exist in %s (%s).",
			len(g.blocked), g.ref.Kind, g.ref.Name, g.ns, strings.Join(vias, ", "))
	} else {
		summary = fmt.Sprintf("%s %q does not exist in %s but %d running pod(s) reference it (%s). "+
			"They keep running now and will fail to start when recreated (rollout, reschedule, node replacement).",
			g.ref.Kind, g.ref.Name, g.ns, len(g.pods), strings.Join(vias, ", "))
	}
	if strings.Contains(strings.Join(vias, ","), "image pull secret") && tier == TierLatent {
		pulled := 0
		for _, p := range g.pods {
			if p.AllImagesPulled() {
				pulled++
			}
		}
		summary += fmt.Sprintf(" %d/%d have their images already pulled on their current node.", pulled, len(g.pods))
	}

	ev := []string{fmt.Sprintf("%s %s/%s: absent (the %s list was collected successfully)", g.ref.Kind, g.ns, g.ref.Name, strings.ToLower(g.ref.Kind))}
	ev = append(ev, fmt.Sprintf("referenced via %s by: %s", strings.Join(vias, ", "), podList(g.pods, 5)))
	if len(g.blocked) > 0 {
		ev = append(ev, "blocked now: "+podList(g.blocked, 5))
	}
	if len(g.eventHit) > 0 {
		sort.Strings(g.eventHit)
		n := len(g.eventHit)
		if n > 3 {
			g.eventHit = append(g.eventHit[:3], fmt.Sprintf("... %d more related events", n-3))
		}
		ev = append(ev, g.eventHit...)
	}
	if g.since != nil {
		ev = append(ev, "first seen "+since(now, g.since)+" ago")
		// Pods that started before the first failure mounted/pulled fine back then:
		// the object existed and was deleted or stopped being synced later.
		older := 0
		for _, p := range g.pods {
			// a margin, so failures that begin *at* startup do not count as "existed at start"
			if p.CreatedAt.Add(existedMargin).Before(*g.since) {
				older++
			}
		}
		if older == len(g.pods) {
			ev = append(ev, fmt.Sprintf("all referencing pods started before the first failure: the %s likely existed and was deleted or stopped syncing around %s",
				strings.ToLower(g.ref.Kind), g.since.UTC().Format("2006-01-02 15:04 UTC")))
		}
	}
	owner := knownOwner(g.ref.Name)
	if owner != "" {
		ev = append(ev, "likely managed by: "+owner)
	}

	plan := &Plan{Steps: []Step{
		{Description: "Confirm it is absent and see which pods need it",
			Command: fmt.Sprintf("kubectl get %s %s -n %s -o name", kubectlKind(g.ref.Kind), g.ref.Name, g.ns)},
	}}
	if owner != "" {
		plan.Steps = append(plan.Steps, Step{Description: "Check the component that should create it: " + owner})
	}
	plan.Steps = append(plan.Steps,
		Step{Description: fmt.Sprintf("Recreate %s %q through its normal provisioning path (operator, pipeline or secret sync), or remove the stale reference from the workload", g.ref.Kind, g.ref.Name), Mutating: true},
	)
	plan.Verify = []string{
		fmt.Sprintf("kubectl get %s %s -n %s -o name", kubectlKind(g.ref.Kind), g.ref.Name, g.ns),
		fmt.Sprintf("kubectl get events -n %s --field-selector type=Warning | grep %s   # should stop growing", g.ns, g.ref.Name),
	}

	var blocks []ObjectRef
	for _, p := range g.blocked {
		blocks = append(blocks, podRef(p))
	}
	return Finding{
		blocks:      blocks,
		ID:          Fingerprint(d.ID(), ix.S.Cluster, g.ref.Kind, g.ns, g.ref.Name),
		Tier:        tier,
		Title:       title,
		Summary:     summary,
		Subject:     subject,
		Affected:    affected,
		Evidence:    ev,
		Since:       g.since,
		Remediation: plan,
		PatternKey:  "missing-reference|" + g.ref.Kind + "|" + g.ref.Name,
	}
}

// knownOwner maps well-known object names to the component that owns them.
// Extend this from incident history.
func knownOwner(name string) string {
	switch {
	case strings.HasPrefix(name, "dynatrace-"):
		return "Dynatrace Operator (kubectl get dynakube -A; operator logs in the dynatrace namespace)"
	case strings.Contains(name, "ecr"):
		return "ECR credential refresher (ECR tokens expire every 12h)"
	case strings.Contains(name, "artifactory") || strings.Contains(name, "jfrog"):
		return "Artifactory credential provisioning"
	}
	return ""
}

func kubectlKind(kind string) string {
	switch kind {
	case model.KindPVC:
		return "pvc"
	case model.KindServiceAccount:
		return "serviceaccount"
	}
	return strings.ToLower(kind)
}

func podList(pods []model.Pod, max int) string {
	var names []string
	for i, p := range pods {
		if i == max {
			names = append(names, fmt.Sprintf("+%d more", len(pods)-max))
			break
		}
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}
