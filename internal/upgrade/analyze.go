package upgrade

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Status string

const (
	StatusFail Status = "FAIL"
	StatusWarn Status = "WARN"
	StatusSkip Status = "SKIP"
	StatusPass Status = "PASS"
)

// Rank orders statuses worst-first: FAIL 0, WARN 1, SKIP 2, PASS 3.
func (s Status) Rank() int {
	switch s {
	case StatusFail:
		return 0
	case StatusWarn:
		return 1
	case StatusSkip:
		return 2
	}
	return 3
}

func worst(a, b Status) Status {
	if b.Rank() < a.Rank() {
		return b
	}
	return a
}

// Check IDs, in report order.
const (
	CheckControlPlane = "control-plane"
	CheckNodeVersions = "node-versions"
	CheckNodeHealth   = "node-health"
	CheckKubeProxy    = "kube-proxy"
	CheckCoreDNS      = "coredns"
	CheckVPCCNI       = "vpc-cni"
	CheckSystemPods   = "kube-system-pods"
)

// Check is one post-upgrade check and, when it is not a PASS, the reasoning.
type Check struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Status   Status    `json:"status"`
	Detail   string    `json:"detail"` // one line for the summary table
	Problems []Problem `json:"problems,omitempty"`
}

// Problem is one thing that is wrong, why it happened, what proves it, and the fix.
type Problem struct {
	Status   Status   `json:"status"`
	What     string   `json:"what"`
	Why      []string `json:"why,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
	Fix      []string `json:"fix,omitempty"`
}

// ResultSchemaVersion changes whenever the Result JSON shape changes incompatibly.
const ResultSchemaVersion = "1"

type Result struct {
	SchemaVersion string    `json:"schema_version"`
	Cluster       string    `json:"cluster"`
	CapturedAt    time.Time `json:"captured_at,omitempty"`
	Target        Targets   `json:"target"`
	ControlPlane  string    `json:"control_plane,omitempty"`
	NodesTotal    int       `json:"nodes_total"`
	NodesOnTarget int       `json:"nodes_on_target"`
	Status        Status    `json:"status"`
	Checks        []Check   `json:"checks"`
	Error         string    `json:"error,omitempty"` // cluster could not be read at all
}

// Check returns the check with the given ID, or a zero Check.
func (r Result) Check(id string) Check {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{}
}

// MaxSkew is how many minors a kubelet may trail kube-apiserver (k8s >= 1.28).
const MaxSkew = 3

// Analyze turns collected facts into checks. It is pure: no I/O, no clock.
func Analyze(f *Facts, t Targets) Result {
	r := Result{SchemaVersion: ResultSchemaVersion, Cluster: f.Cluster, CapturedAt: f.CapturedAt, Target: t, ControlPlane: f.ServerVersion}
	if !f.Collected[KindVersion] || f.ServerVersion == "" {
		r.Status = StatusFail
		r.Error = "API server unreachable: " + firstErr(f.Errors, KindVersion)
		return r
	}
	a := analyzer{f: f, t: t, cp: minorOf(f.ServerVersion)}
	a.ref = a.cp // nodes and kube-proxy must match the control plane they run against
	if a.ref < 0 {
		a.ref = t.Minor
	}
	r.Checks = []Check{
		a.controlPlane(),
		a.nodeVersions(&r),
		a.nodeHealth(),
		a.kubeProxy(),
		a.coreDNS(),
		a.vpcCNI(),
		a.systemPods(),
	}
	r.Status = StatusPass
	for _, c := range r.Checks {
		r.Status = worst(r.Status, c.Status)
	}
	return r
}

type analyzer struct {
	f   *Facts
	t   Targets
	cp  int // control plane minor
	ref int // minor nodes and kube-proxy must be on
}

func (a analyzer) controlPlane() Check {
	c := Check{ID: CheckControlPlane, Title: "Control plane", Detail: fmt.Sprintf("%s (target 1.%d)", shortVersion(a.f.ServerVersion), a.t.Minor)}
	switch {
	case a.cp == a.t.Minor:
		c.Status = StatusPass
	case a.cp < a.t.Minor:
		c.Status = StatusFail
		c.Problems = []Problem{{
			Status: StatusFail,
			What:   fmt.Sprintf("Control plane is on 1.%d, not 1.%d", a.cp, a.t.Minor),
			Why: []string{
				"The control-plane upgrade has not run (or failed) for this cluster.",
				fmt.Sprintf("Node and add-on checks below are judged against the running control plane (1.%d), not the target.", a.cp),
			},
			Evidence: []string{"kube-apiserver " + a.f.ServerVersion},
			Fix: []string{
				fmt.Sprintf("Upgrade the control plane one minor at a time (EKS console > cluster > Upgrade now) until it reaches 1.%d.", a.t.Minor),
			},
		}}
	default:
		c.Status = StatusWarn
		c.Problems = []Problem{{
			Status: StatusWarn,
			What:   fmt.Sprintf("Control plane is on 1.%d, newer than the target 1.%d", a.cp, a.t.Minor),
			Why:    []string{"--target is older than the cluster; add-on targets come from the target minor and may be too old."},
			Fix:    []string{fmt.Sprintf("Re-run with --target 1.%d.", a.cp)},
		}}
	}
	return c
}

// ---------------------------------------------------------------- nodes

type nodeGroup struct {
	owner, ownerName string
	minor            int
	nodes            []Node
}

func (g nodeGroup) names() []string {
	var out []string
	for _, n := range g.nodes {
		out = append(out, n.Name)
	}
	return out
}

func (g nodeGroup) label() string {
	switch g.owner {
	case OwnerMNG:
		return "managed nodegroup " + g.ownerName
	case OwnerKarpenter:
		return "Karpenter NodePool " + g.ownerName
	case OwnerFargate:
		return "Fargate"
	case OwnerAutoMode:
		return "EKS Auto Mode NodePool " + g.ownerName
	}
	return "self-managed nodes"
}

func (a analyzer) nodeVersions(r *Result) Check {
	c := Check{ID: CheckNodeVersions, Title: "Node versions"}
	if !a.f.Collected[KindNodes] {
		c.Status, c.Detail = StatusSkip, "nodes not readable: "+firstErr(a.f.Errors, KindNodes)
		return c
	}
	r.NodesTotal = len(a.f.Nodes)
	groups := map[string]*nodeGroup{}
	var keys []string
	patches := map[string]bool{}
	for _, n := range a.f.Nodes {
		m := minorOf(n.Kubelet)
		if m == a.ref {
			r.NodesOnTarget++
			patches[shortVersion(n.Kubelet)] = true
			continue
		}
		k := n.Owner + "|" + n.OwnerName + "|" + strconv.Itoa(m)
		if groups[k] == nil {
			groups[k] = &nodeGroup{owner: n.Owner, ownerName: n.OwnerName, minor: m}
			keys = append(keys, k)
		}
		groups[k].nodes = append(groups[k].nodes, n)
	}
	c.Detail = fmt.Sprintf("%d/%d on 1.%d", r.NodesOnTarget, r.NodesTotal, a.ref)
	if len(patches) > 1 {
		c.Detail += fmt.Sprintf(" (%d patch levels: cosmetic, different AMI releases)", len(patches))
	}
	if r.NodesTotal == 0 {
		c.Status, c.Detail = StatusWarn, "no nodes"
		return c
	}
	if len(groups) == 0 {
		c.Status = StatusPass
		return c
	}
	sort.Slice(keys, func(i, j int) bool {
		gi, gj := groups[keys[i]], groups[keys[j]]
		if gi.minor != gj.minor {
			return gi.minor < gj.minor // most behind first
		}
		return keys[i] < keys[j]
	})
	c.Status = StatusFail
	for _, k := range keys {
		c.Problems = append(c.Problems, a.explainGroup(*groups[k]))
	}
	return c
}

func (a analyzer) explainGroup(g nodeGroup) Problem {
	p := Problem{Status: StatusFail}
	ver := shortVersion(g.nodes[0].Kubelet)
	var skew string
	switch {
	case g.minor > a.ref:
		skew = "newer than the control plane: not allowed by the version skew policy"
	case a.ref-g.minor > MaxSkew:
		skew = fmt.Sprintf("%d minors behind the control plane: OUTSIDE the supported skew (max %d)", a.ref-g.minor, MaxSkew)
	default:
		skew = fmt.Sprintf("%d minor(s) behind: still inside the supported skew, but the upgrade is not complete", a.ref-g.minor)
	}
	p.What = fmt.Sprintf("%d node(s) in %s on %s (%s)", len(g.nodes), g.label(), ver, skew)
	p.Evidence = append(p.Evidence, "nodes: "+joinMax(g.names(), 8))

	cordoned := 0
	notReady := 0
	for _, n := range g.nodes {
		if n.Unschedulable {
			cordoned++
		}
		if !n.Ready {
			notReady++
		}
	}
	if cordoned > 0 {
		p.Why = append(p.Why, fmt.Sprintf("%d of them are cordoned: a replacement started but is stuck draining (see blockers below).", cordoned))
	}
	if notReady > 0 {
		p.Why = append(p.Why, fmt.Sprintf("%d of them are NotReady: a broken node can hang the drain of a rolling update.", notReady))
	}

	switch g.owner {
	case OwnerMNG:
		p.Why = append(p.Why, "A control-plane upgrade never rolls managed nodegroups: the nodegroup still runs its old AMI release until it is updated.")
		if img := g.nodes[0].Image; img != "" {
			p.Evidence = append(p.Evidence, "nodegroup image: "+img)
		}
		p.Fix = append(p.Fix,
			fmt.Sprintf("EKS console > %s > Compute > %s > Update now (rolling update) to the 1.%d AMI release.", ShortName(a.f.Cluster), g.ownerName, a.ref),
			"Custom launch template with a pinned AMI? Create a new launch template version with a 1."+strconv.Itoa(a.ref)+" AMI first, then update the nodegroup to it.",
		)
		if cordoned > 0 || len(a.f.PDBs) > 0 {
			p.Why = append(p.Why, "A nodegroup update fails with PodEvictionFailure when a PodDisruptionBudget blocks the drain for 15 minutes.")
			p.Evidence = append(p.Evidence, a.pdbEvidence()...)
		}
	case OwnerKarpenter:
		a.explainKarpenter(g, &p)
	case OwnerFargate:
		p.Why = append(p.Why, "Fargate nodes are per pod: a pod keeps the kubelet it started with until it is recreated.")
		p.Fix = append(p.Fix, "kubectl rollout restart the owning Deployment/StatefulSet so its pods come back on 1."+strconv.Itoa(a.ref)+".")
	case OwnerAutoMode:
		p.Why = append(p.Why, "EKS Auto Mode replaces nodes itself (max 21-day node lifetime); a NodePool disruption budget or a PDB can hold it back.")
		p.Evidence = append(p.Evidence, a.pdbEvidence()...)
		p.Fix = append(p.Fix, "Check the NodePool's disruption budgets and the PDBs listed; AWS replaces the nodes once disruption is allowed.")
	default:
		p.Why = append(p.Why, "No nodegroup or Karpenter label: these come from a self-managed Auto Scaling group, whose launch template still points at a 1."+strconv.Itoa(g.minor)+" AMI.")
		p.Fix = append(p.Fix, "Point the ASG launch template at a 1."+strconv.Itoa(a.ref)+" AMI, then run an ASG instance refresh (or terminate the nodes one at a time).")
	}
	return p
}

func (a analyzer) explainKarpenter(g nodeGroup, p *Problem) {
	k := a.f.Karpenter
	if k == nil {
		if a.f.Collected[KindKarpenter] {
			p.Why = append(p.Why, "Nodes carry Karpenter labels but no Karpenter CRDs are installed (left over from an uninstalled Karpenter?): nothing will ever replace them.")
			p.Fix = append(p.Fix, "Drain and terminate these instances; capacity comes back from whatever provisions nodes now.")
		} else {
			p.Why = append(p.Why, "Nodes carry Karpenter labels but the Karpenter CRDs could not be read: "+firstErr(a.f.Errors, KindKarpenter))
		}
		return
	}
	claims := map[string]NodeClaim{}
	for _, c := range k.NodeClaims {
		claims[c.NodeName] = c
	}
	var drifted, noClaim, dndNodes []string
	dndPods := map[string]bool{}
	classes := map[string]bool{}
	for _, n := range g.nodes {
		c, ok := claims[n.Name]
		switch {
		case !ok:
			noClaim = append(noClaim, n.Name)
		case c.Drifted == "True":
			drifted = append(drifted, n.Name)
		}
		if n.DoNotDisrupt {
			dndNodes = append(dndNodes, n.Name)
		}
		for _, pod := range n.DoNotDisruptPods {
			dndPods[pod] = true
		}
		if n.NodeClass != "" {
			classes[n.NodeClass] = true
		}
	}
	if k.ControllerImage != "" {
		p.Evidence = append(p.Evidence, "karpenter controller: "+imageTagOr(k.ControllerImage))
	}
	if len(noClaim) > 0 {
		p.Why = append(p.Why, fmt.Sprintf("%d node(s) have no NodeClaim: Karpenter does not own them, so it will never replace them.", len(noClaim)))
		p.Evidence = append(p.Evidence, "no NodeClaim: "+joinMax(noClaim, 6))
		p.Fix = append(p.Fix, "Find who launched them (EC2 tags / ASG); drain and terminate them so Karpenter provisions 1."+strconv.Itoa(a.ref)+" capacity.")
	}

	claimed := len(g.nodes) - len(noClaim)
	if claimed == 0 {
		return
	}
	if len(drifted) > 0 {
		p.Why = append(p.Why, fmt.Sprintf("Karpenter has marked %d/%d of them Drifted but has not replaced them, so something is blocking disruption:", len(drifted), claimed))
		blocked := false
		for _, b := range a.driftBudgetBlocks(g.ownerName) {
			p.Why = append(p.Why, "  - "+b)
			blocked = true
		}
		if len(dndNodes) > 0 {
			p.Why = append(p.Why, fmt.Sprintf("  - %d node(s) are annotated karpenter.sh/do-not-disrupt=true", len(dndNodes)))
			p.Evidence = append(p.Evidence, "do-not-disrupt nodes: "+joinMax(dndNodes, 6))
			blocked = true
		}
		if len(dndPods) > 0 {
			p.Why = append(p.Why, fmt.Sprintf("  - %d pod(s) on them are annotated karpenter.sh/do-not-disrupt=true", len(dndPods)))
			p.Evidence = append(p.Evidence, "do-not-disrupt pods: "+joinMax(sortedKeys(dndPods), 6))
			blocked = true
		}
		if len(a.f.PDBs) > 0 {
			p.Why = append(p.Why, "  - PodDisruptionBudgets currently allow 0 disruptions (Karpenter will not evict through them)")
			p.Evidence = append(p.Evidence, a.pdbEvidence()...)
			blocked = true
		}
		if !blocked {
			p.Why = append(p.Why, "  - no budget, do-not-disrupt or PDB blocker is visible: Karpenter may be busy with other disruptions or failing to launch replacements")
		}
		p.Fix = append(p.Fix, "kubectl logs -n <karpenter-ns> deploy/karpenter | grep -iE 'drift|disrupt|cannot'  (the controller says exactly what it is waiting on)")
		return
	}

	// Not drifted: the NodeClass still resolves to an AMI Karpenter considers current.
	for name := range classes {
		nc, ok := nodeClass(k, name)
		if !ok {
			p.Why = append(p.Why, fmt.Sprintf("EC2NodeClass %q referenced by the nodes does not exist.", name))
			continue
		}
		p.Evidence = append(p.Evidence, fmt.Sprintf("EC2NodeClass %s amiSelectorTerms=%s", name, nc.AMISelectorTerms))
		minors, names := amiMinors(nc.AMIs)
		if len(names) > 0 {
			p.Evidence = append(p.Evidence, "resolves to: "+joinMax(names, 4))
		}
		switch {
		case minors[a.ref]:
			p.Why = append(p.Why, fmt.Sprintf("EC2NodeClass %s already resolves 1.%d AMIs, yet Karpenter has not marked these nodes Drifted.", name, a.ref),
				"Drift is evaluated periodically; if this persists, the controller is not reconciling (check its logs) or its version does not support 1."+strconv.Itoa(a.ref)+".")
			p.Fix = append(p.Fix, "kubectl logs -n <karpenter-ns> deploy/karpenter | grep -i drift ; confirm the Karpenter version supports Kubernetes 1."+strconv.Itoa(a.ref)+".")
		case len(minors) > 0:
			p.Why = append(p.Why, fmt.Sprintf("EC2NodeClass %s only resolves to 1.%s AMIs, so Karpenter sees the nodes as up to date and never drifts them.", name, joinMinors(minors)))
			switch {
			case strings.Contains(nc.AMISelectorTerms, "@latest"):
				p.Why = append(p.Why, "The alias is @latest, which Karpenter resolves for the Kubernetes version it detects: a controller too old for 1."+strconv.Itoa(a.ref)+" keeps resolving the old AMIs.")
				p.Fix = append(p.Fix, "Upgrade Karpenter to a version that supports Kubernetes 1."+strconv.Itoa(a.ref)+"; drift then rolls the nodes.")
			case strings.Contains(nc.AMISelectorTerms, `"alias"`):
				p.Why = append(p.Why, "The AMI alias is pinned to a specific release version.")
				p.Fix = append(p.Fix, fmt.Sprintf("Set amiSelectorTerms to a 1.%d release (e.g. alias al2023@latest, or a pinned 1.%d version); drift then rolls the nodes within the NodePool budget.", a.ref, a.ref))
			default:
				p.Why = append(p.Why, "The selector picks AMIs by id/name/tags, which still match the old images.")
				p.Fix = append(p.Fix, fmt.Sprintf("Point amiSelectorTerms at 1.%d AMIs (new ids/tags, or alias al2023@latest); drift then rolls the nodes.", a.ref))
			}
		default:
			p.Why = append(p.Why, fmt.Sprintf("EC2NodeClass %s resolves to AMIs whose names carry no Kubernetes version (custom AMI?). Karpenter only drifts a node when the selector resolves to a different AMI.", name))
			p.Fix = append(p.Fix, fmt.Sprintf("Build/publish a 1.%d AMI and point amiSelectorTerms at it.", a.ref))
		}
	}
	if len(classes) == 0 {
		p.Why = append(p.Why, "Nodes have no karpenter.k8s.aws/ec2nodeclass label; cannot tell which AMI selector they use.")
	}
}

// driftBudgetBlocks returns NodePool budgets that stop drift replacements.
func (a analyzer) driftBudgetBlocks(pool string) []string {
	var out []string
	for _, np := range a.f.Karpenter.NodePools {
		if np.Name != pool {
			continue
		}
		for _, b := range np.Budgets {
			if strings.TrimSpace(b.Nodes) != "0" && strings.TrimSpace(b.Nodes) != "0%" {
				continue
			}
			if len(b.Reasons) > 0 && !contains(b.Reasons, "Drifted") {
				continue
			}
			when := "always"
			if b.Schedule != "" {
				when = fmt.Sprintf("while schedule %q (for %s) is active", b.Schedule, orDash(b.Duration))
			}
			out = append(out, fmt.Sprintf("NodePool %s has a disruption budget nodes=%q %s: Karpenter may replace 0 nodes", np.Name, b.Nodes, when))
		}
	}
	return out
}

func (a analyzer) pdbEvidence() []string {
	var out []string
	for i, p := range a.f.PDBs {
		if i == 6 {
			out = append(out, fmt.Sprintf("... and %d more PDBs at 0 allowed disruptions", len(a.f.PDBs)-6))
			break
		}
		out = append(out, fmt.Sprintf("PDB %s/%s allows 0 disruptions (healthy %d/%d)", p.Namespace, p.Name, p.CurrentHealthy, p.DesiredHealthy))
	}
	return out
}

func (a analyzer) nodeHealth() Check {
	c := Check{ID: CheckNodeHealth, Title: "Node health"}
	if !a.f.Collected[KindNodes] {
		c.Status, c.Detail = StatusSkip, "nodes not readable"
		return c
	}
	var notReady, cordoned []string
	for _, n := range a.f.Nodes {
		if !n.Ready {
			notReady = append(notReady, n.Name)
		}
		if n.Unschedulable {
			cordoned = append(cordoned, n.Name)
		}
	}
	c.Status = StatusPass
	c.Detail = fmt.Sprintf("%d Ready, %d NotReady, %d cordoned", len(a.f.Nodes)-len(notReady), len(notReady), len(cordoned))
	if len(notReady) > 0 {
		c.Status = StatusFail
		c.Problems = append(c.Problems, Problem{
			Status:   StatusFail,
			What:     fmt.Sprintf("%d node(s) NotReady", len(notReady)),
			Why:      []string{"Right after an upgrade this is usually a node on the new AMI whose kubelet or CNI did not come up (check aws-node on it), or an old node failing mid-drain."},
			Evidence: []string{"nodes: " + joinMax(notReady, 8)},
			Fix:      []string{"k8s-doctor node diagnose <node>  (or kubectl describe node <node>; check the aws-node and kube-proxy pods on it)"},
		})
	}
	if len(cordoned) > 0 {
		c.Status = worst(c.Status, StatusWarn)
		c.Problems = append(c.Problems, Problem{
			Status:   StatusWarn,
			What:     fmt.Sprintf("%d node(s) cordoned", len(cordoned)),
			Why:      []string{"Cordoned nodes left behind usually mean a drain that never finished (PDB, do-not-disrupt pod, or a manual cordon)."},
			Evidence: append([]string{"nodes: " + joinMax(cordoned, 8)}, a.pdbEvidence()...),
			Fix:      []string{"Finish the drain (fix the blocking PDB) or uncordon if the node should stay."},
		})
	}
	return c
}

// ---------------------------------------------------------------- add-ons

func (a analyzer) addon(name string) (Addon, bool) {
	for _, ad := range a.f.Addons {
		if ad.Name == name {
			return ad, true
		}
	}
	return Addon{}, false
}

func mainImage(ad Addon, container string) string {
	for _, c := range ad.Containers {
		if c.Name == container && !c.Init {
			return c.Image
		}
	}
	return ""
}

func initImage(ad Addon, container string) string {
	for _, c := range ad.Containers {
		if c.Name == container && c.Init {
			return c.Image
		}
	}
	return ""
}

// addonCheck runs the checks every add-on shares: present, version, rollout.
func (a analyzer) addonCheck(id, title, name, container, target, missingWhy string) (Check, Addon, string) {
	c := Check{ID: id, Title: title}
	if !a.f.Collected[KindAddons] {
		c.Status, c.Detail = StatusSkip, "kube-system not readable: "+firstErr(a.f.Errors, KindAddons)
		return c, Addon{}, ""
	}
	ad, _ := a.addon(name)
	if !ad.Found {
		c.Status, c.Detail = StatusWarn, ad.Object+" not found"
		c.Problems = []Problem{{Status: StatusWarn, What: ad.Object + " not found", Why: []string{missingWhy}}}
		return c, ad, ""
	}
	img := mainImage(ad, container)
	tag := imageTag(img)
	owner := "self-managed"
	if ad.ManagedByEKS {
		owner = "EKS add-on"
	}
	c.Detail = fmt.Sprintf("%s (target %s) [%s]", orDash(tag), orDash(target), owner)
	c.Status = StatusPass

	switch {
	case tag == "":
		c.Status = StatusWarn
		c.Problems = append(c.Problems, Problem{Status: StatusWarn, What: "Image has no tag (pinned by digest): version cannot be read", Evidence: []string{img}})
	case target == "":
		c.Status = StatusWarn
		c.Problems = append(c.Problems, Problem{
			Status: StatusWarn,
			What:   fmt.Sprintf("No built-in %s target for 1.%d; running %s", title, a.t.Minor, tag),
			Fix:    []string{fmt.Sprintf("Check the latest version in the EKS console (Add-ons > %s > Edit) and pass it with --%s.", name, name)},
		})
	case compareVersions(tag, target) < 0:
		c.Status = StatusFail
		c.Problems = append(c.Problems, Problem{
			Status:   StatusFail,
			What:     fmt.Sprintf("%s %s is behind the latest EKS add-on version for 1.%d (%s)", title, tag, a.t.Minor, target),
			Why:      []string{"Add-ons are not upgraded with the control plane; each is updated separately after the control-plane upgrade."},
			Evidence: []string{ad.Object + " image " + img},
			Fix:      addonFix(ad, name, target),
		})
	case compareVersions(tag, target) > 0:
		c.Detail += " (newer than the built-in target)"
	}

	// Rollout: a new version in the spec is not the same as running it.
	if ad.GenerationPending || ad.Updated < ad.Desired {
		c.Status = worst(c.Status, StatusWarn)
		c.Problems = append(c.Problems, Problem{
			Status:   StatusWarn,
			What:     fmt.Sprintf("Rollout not finished: %d/%d pods on the current template", ad.Updated, ad.Desired),
			Why:      []string{"Pods on the old template still run the previous version; a rollout that does not progress is usually blocked by pods that cannot start (image pull, scheduling) or by maxUnavailable."},
			Evidence: []string{fmt.Sprintf("%s desired=%d updated=%d ready=%d", ad.Object, ad.Desired, ad.Updated, ad.Ready)},
			Fix:      []string{fmt.Sprintf("kubectl -n kube-system rollout status %s/%s ; kubectl -n kube-system get pods -o wide | grep %s", strings.ToLower(ad.Kind), objName(ad.Object), objName(ad.Object))},
		})
	}
	if ad.Ready < ad.Desired {
		c.Status = worst(c.Status, StatusFail)
		c.Problems = append(c.Problems, Problem{
			Status:   StatusFail,
			What:     fmt.Sprintf("%d/%d pods ready", ad.Ready, ad.Desired),
			Why:      []string{"See the kube-system pods check below for the failing pods and their reasons."},
			Evidence: []string{fmt.Sprintf("%s desired=%d ready=%d unavailable=%d", ad.Object, ad.Desired, ad.Ready, ad.Unavailable)},
		})
	}
	return c, ad, tag
}

func addonFix(ad Addon, name, target string) []string {
	if ad.ManagedByEKS {
		return []string{fmt.Sprintf("EKS console > cluster > Add-ons > %s > Edit > version %s (conflict resolution Preserve keeps your config changes).", name, target)}
	}
	return []string{
		"This is a self-managed install (no 'eks' field manager), so the console will not offer an update for it.",
		fmt.Sprintf("Adopt it as an EKS add-on (console > Add-ons > Get more add-ons > %s, version %s; take a backup of the object first), or bump the image tag to %s.", name, target, target),
	}
}

func (a analyzer) kubeProxy() Check {
	c, ad, tag := a.addonCheck(CheckKubeProxy, "kube-proxy", AddonKubeProxy, "kube-proxy", a.t.KubeProxy,
		"No kube-proxy DaemonSet: expected only if a CNI replaces it (e.g. Cilium kube-proxy replacement).")
	if !ad.Found || tag == "" {
		return c
	}
	if m := minorOf(tag); m >= 0 && m != a.ref {
		c.Status = worst(c.Status, StatusFail)
		why := fmt.Sprintf("kube-proxy should run the control plane's minor after an upgrade; 1.%d kube-proxy on a 1.%d cluster was left behind.", m, a.ref)
		if m > a.ref {
			why = fmt.Sprintf("kube-proxy 1.%d is newer than the 1.%d control plane, which the skew policy does not allow.", m, a.ref)
		}
		// Same root cause as "behind the target": one problem, not two.
		for i := range c.Problems {
			if c.Problems[i].Status == StatusFail && strings.Contains(c.Problems[i].What, "is behind") {
				c.Problems[i].Why = append(c.Problems[i].Why, why)
				return c
			}
		}
		c.Problems = append(c.Problems, Problem{
			Status: StatusFail,
			What:   fmt.Sprintf("kube-proxy minor 1.%d does not match the control plane 1.%d", m, a.ref),
			Why:    []string{why},
		})
	}
	return c
}

func (a analyzer) coreDNS() Check {
	c, ad, _ := a.addonCheck(CheckCoreDNS, "CoreDNS", AddonCoreDNS, "coredns", a.t.CoreDNS,
		"No coredns Deployment in kube-system: cluster DNS is provided by something else, or it was deleted.")
	if ad.Found && c.Status == StatusFail {
		for i := range c.Problems {
			if c.Problems[i].Status == StatusFail && len(c.Problems[i].Fix) > 0 {
				c.Problems[i].Fix = append(c.Problems[i].Fix, "If the Corefile has custom zones/forwards, choose Preserve and diff the coredns ConfigMap before and after.")
				break
			}
		}
	}
	return c
}

func (a analyzer) vpcCNI() Check {
	c, ad, tag := a.addonCheck(CheckVPCCNI, "VPC CNI", AddonVPCCNI, "aws-node", a.t.VPCCNI,
		"No aws-node DaemonSet: the cluster uses another CNI.")
	if !ad.Found {
		return c
	}
	if na := imageTag(mainImage(ad, "aws-eks-nodeagent")); na != "" {
		c.Detail += " nodeagent " + na
	}
	initImg := initImage(ad, "aws-vpc-cni-init")
	if it := imageTag(initImg); it != "" && tag != "" && it != tag {
		c.Status = worst(c.Status, StatusFail)
		c.Problems = append(c.Problems, Problem{
			Status: StatusFail,
			What:   fmt.Sprintf("aws-vpc-cni-init (%s) does not match aws-node (%s)", it, tag),
			Why: []string{
				"The init container sets up host networking on every new node. It was left on the old version, typically because only the main containers' images were patched (kubectl set image / a script that skips initContainers).",
				"Existing nodes keep working; new nodes initialize networking with the old CNI.",
			},
			Evidence: []string{"aws-vpc-cni-init image " + initImg, "aws-node image " + mainImage(ad, "aws-node")},
			Fix: []string{
				"Update through the EKS add-on (console > Add-ons > vpc-cni > Edit), which rewrites the whole DaemonSet,",
				fmt.Sprintf("or set the init image too: kubectl -n kube-system set image ds/aws-node aws-vpc-cni-init=<same repo>:%s", tag),
			},
		})
	}
	return c
}

// ---------------------------------------------------------------- kube-system pods

// RestartWarn is the restart count at which a running kube-system pod is flagged.
const RestartWarn = 5

func (a analyzer) systemPods() Check {
	c := Check{ID: CheckSystemPods, Title: "kube-system pods"}
	if !a.f.Collected[KindPods] {
		c.Status, c.Detail = StatusSkip, "kube-system pods not readable: "+firstErr(a.f.Errors, KindPods)
		return c
	}
	var bad, flappy []string
	for _, p := range a.f.KubeSystemPods {
		if p.Phase == "Succeeded" {
			continue
		}
		desc := fmt.Sprintf("%s (%s", p.Name, orDash(firstNonEmpty(p.Reason, p.Phase)))
		if p.Restarts > 0 {
			desc += fmt.Sprintf(", %d restarts", p.Restarts)
		}
		if p.Node != "" {
			desc += ", on " + p.Node
		}
		desc += ")"
		switch {
		case p.Phase != "Running" || !p.Ready:
			bad = append(bad, desc)
		case p.Restarts >= RestartWarn:
			flappy = append(flappy, desc)
		}
	}
	c.Status = StatusPass
	c.Detail = fmt.Sprintf("%d pods, %d not ready, %d restarting", len(a.f.KubeSystemPods), len(bad), len(flappy))
	if len(bad) > 0 {
		c.Status = StatusFail
		c.Problems = append(c.Problems, Problem{
			Status:   StatusFail,
			What:     fmt.Sprintf("%d kube-system pod(s) not ready", len(bad)),
			Why:      []string{"After an upgrade, failing system pods are usually a new add-on version that cannot pull its image (private registry mirror), a node on the new AMI without working networking, or a config incompatibility."},
			Evidence: limit(bad, 10),
			Fix:      []string{"kubectl -n kube-system describe pod <pod> ; kubectl -n kube-system logs <pod> --previous"},
		})
	}
	if len(flappy) > 0 {
		c.Status = worst(c.Status, StatusWarn)
		c.Problems = append(c.Problems, Problem{
			Status:   StatusWarn,
			What:     fmt.Sprintf("%d kube-system pod(s) restarting (>= %d restarts)", len(flappy), RestartWarn),
			Evidence: limit(flappy, 10),
			Fix:      []string{"kubectl -n kube-system logs <pod> --previous"},
		})
	}
	return c
}

// ---------------------------------------------------------------- helpers

// amiNameMinorRE pulls the Kubernetes minor out of EKS-optimized AMI names:
// amazon-eks-node-al2023-x86_64-standard-1.34-v20250715, bottlerocket-aws-k8s-1.34-x86_64-v1.42.0, amazon-eks-node-1.34-v2025...
var amiNameMinorRE = regexp.MustCompile(`[-_]1\.(\d{2})[-_]`)

func amiMinors(amis []ResolvedAMI) (map[int]bool, []string) {
	minors := map[int]bool{}
	var names []string
	seen := map[string]bool{}
	for _, a := range amis {
		n := firstNonEmpty(a.Name, a.ID)
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
		if m := amiNameMinorRE.FindStringSubmatch(a.Name); m != nil {
			v, _ := strconv.Atoi(m[1])
			minors[v] = true
		}
	}
	return minors, names
}

func joinMinors(m map[int]bool) string {
	var out []int
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	var s []string
	for _, k := range out {
		s = append(s, strconv.Itoa(k))
	}
	return strings.Join(s, "/1.")
}

func nodeClass(k *Karpenter, name string) (NodeClass, bool) {
	for _, c := range k.NodeClasses {
		if c.Name == name {
			return c, true
		}
	}
	return NodeClass{}, false
}

func firstErr(errs []string, kind string) string {
	for _, e := range errs {
		if strings.HasPrefix(e, kind+":") {
			return strings.TrimSpace(strings.TrimPrefix(e, kind+":"))
		}
	}
	return "unknown error"
}

func joinMax(names []string, n int) string {
	if len(names) <= n {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:n], ", ") + fmt.Sprintf(" (+%d more)", len(names)-n)
}

func limit(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(append([]string{}, s[:n]...), fmt.Sprintf("... and %d more", len(s)-n))
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func imageTagOr(img string) string {
	if t := imageTag(img); t != "" {
		return t
	}
	return img
}

func objName(obj string) string {
	if i := strings.LastIndex(obj, "/"); i >= 0 {
		return obj[i+1:]
	}
	return obj
}
