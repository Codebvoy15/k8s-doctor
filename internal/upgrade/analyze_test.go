package upgrade

import (
	"strings"
	"testing"
)

// healthy builds a cluster that has fully finished upgrading to 1.36.
func healthy() *Facts {
	return &Facts{
		SchemaVersion: FactsSchemaVersion,
		Cluster:       "arn:aws:eks:us-east-1:111122223333:cluster/sbx",
		ServerVersion: "v1.36.2-eks-abc",
		Collected: map[string]bool{KindVersion: true, KindNodes: true, KindAddons: true,
			KindPods: true, KindPDBs: true, KindKarpenter: true},
		Nodes: []Node{
			{Name: "ip-1", Kubelet: "v1.36.1-eks-1", Ready: true, Owner: OwnerMNG, OwnerName: "ng-sys"},
			{Name: "ip-2", Kubelet: "v1.36.2-eks-1", Ready: true, Owner: OwnerKarpenter, OwnerName: "default", NodeClass: "default"},
		},
		Addons: []Addon{
			{Name: AddonKubeProxy, Kind: "DaemonSet", Object: "kube-system/kube-proxy", Found: true, ManagedByEKS: true,
				Containers: []AddonContainer{{Name: "kube-proxy", Image: "602401143452.dkr.ecr.us-east-1.amazonaws.com/eks/kube-proxy:v1.36.0-eksbuild.21"}},
				Desired:    2, Updated: 2, Ready: 2},
			{Name: AddonCoreDNS, Kind: "Deployment", Object: "kube-system/coredns", Found: true, ManagedByEKS: true,
				Containers: []AddonContainer{{Name: "coredns", Image: "registry:5000/eks/coredns:v1.14.3-eksbuild.3"}},
				Desired:    2, Updated: 2, Ready: 2},
			{Name: AddonVPCCNI, Kind: "DaemonSet", Object: "kube-system/aws-node", Found: true, ManagedByEKS: true,
				Containers: []AddonContainer{
					{Name: "aws-vpc-cni-init", Init: true, Image: "r/amazon-k8s-cni-init:v1.23.1-eksbuild.1"},
					{Name: "aws-node", Image: "r/amazon-k8s-cni:v1.23.1-eksbuild.1"},
					{Name: "aws-eks-nodeagent", Image: "r/aws-network-policy-agent:v1.3.0-eksbuild.1"},
				},
				Desired: 2, Updated: 2, Ready: 2},
		},
		KubeSystemPods: []Pod{{Name: "coredns-1", Phase: "Running", Ready: true}},
		Karpenter: &Karpenter{
			APIVersion: "v1",
			NodeClaims: []NodeClaim{{Name: "default-a", NodeName: "ip-2", Drifted: "False"}},
			NodeClasses: []NodeClass{{Name: "default", AMISelectorTerms: `[{"alias":"al2023@latest"}]`,
				AMIs: []ResolvedAMI{{ID: "ami-new", Name: "amazon-eks-node-al2023-x86_64-standard-1.36-v20260901"}}}},
			NodePools: []NodePool{{Name: "default"}},
		},
	}
}

func run(f *Facts) Result { return Analyze(f, DefaultTargets(36)) }

func problemText(c Check) string {
	var b strings.Builder
	for _, p := range c.Problems {
		b.WriteString(p.What + "\n" + strings.Join(p.Why, "\n") + "\n" + strings.Join(p.Evidence, "\n") + "\n" + strings.Join(p.Fix, "\n") + "\n")
	}
	return b.String()
}

func mustContain(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestHealthyClusterPasses(t *testing.T) {
	r := run(healthy())
	if r.Status != StatusPass {
		for _, c := range r.Checks {
			t.Logf("%s %s %s\n%s", c.ID, c.Status, c.Detail, problemText(c))
		}
		t.Fatalf("status %s, want PASS", r.Status)
	}
	if r.NodesOnTarget != 2 || r.NodesTotal != 2 {
		t.Errorf("nodes %d/%d", r.NodesOnTarget, r.NodesTotal)
	}
	mustContain(t, r.Check(CheckNodeVersions).Detail, "2/2 on 1.36", "2 patch levels")
}

func TestUnreachable(t *testing.T) {
	f := &Facts{Cluster: "x", Collected: map[string]bool{}, Errors: []string{"version: dial tcp: i/o timeout"}}
	r := run(f)
	if r.Status != StatusFail || !strings.Contains(r.Error, "i/o timeout") {
		t.Fatalf("got %+v", r)
	}
}

func TestControlPlaneBehind(t *testing.T) {
	f := healthy()
	f.ServerVersion = "v1.35.3-eks-x"
	f.Nodes = f.Nodes[:1]
	f.Nodes[0].Kubelet = "v1.35.3-eks-1"
	r := run(f)
	cp := r.Check(CheckControlPlane)
	if cp.Status != StatusFail {
		t.Fatalf("control plane %s", cp.Status)
	}
	// nodes are judged against the running control plane, not the target
	if s := r.Check(CheckNodeVersions).Status; s != StatusPass {
		t.Errorf("node versions %s, want PASS against a 1.35 control plane", s)
	}
}

func TestManagedNodegroupNotUpdated(t *testing.T) {
	f := healthy()
	f.Nodes = append(f.Nodes,
		Node{Name: "ip-3", Kubelet: "v1.34.6-eks-1", Ready: true, Owner: OwnerMNG, OwnerName: "ng-app", Image: "ami-0old"},
		Node{Name: "ip-4", Kubelet: "v1.34.6-eks-1", Ready: true, Unschedulable: true, Owner: OwnerMNG, OwnerName: "ng-app"},
	)
	f.PDBs = []PDB{{Namespace: "shop", Name: "web", Expected: 2, CurrentHealthy: 2, DesiredHealthy: 2}}
	r := run(f)
	c := r.Check(CheckNodeVersions)
	if c.Status != StatusFail || len(c.Problems) != 1 {
		t.Fatalf("got %s with %d problems", c.Status, len(c.Problems))
	}
	mustContain(t, problemText(c),
		"2 node(s) in managed nodegroup ng-app on v1.34.6",
		"inside the supported skew",
		"never rolls managed nodegroups",
		"1 of them are cordoned",
		"PodEvictionFailure",
		"PDB shop/web allows 0 disruptions",
		"EKS console > sbx > Compute > ng-app > Update now",
		"nodegroup image: ami-0old")
	if r.NodesOnTarget != 2 || r.NodesTotal != 4 {
		t.Errorf("nodes %d/%d", r.NodesOnTarget, r.NodesTotal)
	}
	if hc := r.Check(CheckNodeHealth); hc.Status != StatusWarn {
		t.Errorf("node health %s, want WARN for a cordoned node", hc.Status)
	}
}

func TestUnsupportedSkew(t *testing.T) {
	f := healthy()
	f.Nodes = append(f.Nodes, Node{Name: "old", Kubelet: "v1.32.9-eks-1", Ready: true, Owner: OwnerSelfManaged})
	c := run(f).Check(CheckNodeVersions)
	mustContain(t, problemText(c), "4 minors behind", "OUTSIDE the supported skew", "self-managed Auto Scaling group", "instance refresh")
}

func TestKarpenterPinnedAlias(t *testing.T) {
	f := healthy()
	f.Nodes = append(f.Nodes, Node{Name: "ip-5", Kubelet: "v1.34.4-eks-1", Ready: true, Owner: OwnerKarpenter, OwnerName: "gpu", NodeClass: "gpu"})
	f.Karpenter.NodeClaims = append(f.Karpenter.NodeClaims, NodeClaim{Name: "gpu-a", NodeName: "ip-5", Drifted: "False"})
	f.Karpenter.NodeClasses = append(f.Karpenter.NodeClasses, NodeClass{Name: "gpu", AMISelectorTerms: `[{"alias":"al2023@v20250715"}]`,
		AMIs: []ResolvedAMI{{ID: "ami-1", Name: "amazon-eks-node-al2023-x86_64-nvidia-1.34-v20250715"}}})
	mustContain(t, problemText(run(f).Check(CheckNodeVersions)),
		"Karpenter NodePool gpu",
		"only resolves to 1.34 AMIs",
		"never drifts them",
		"pinned to a specific release",
		"al2023@latest",
		"amazon-eks-node-al2023-x86_64-nvidia-1.34-v20250715")
}

func TestKarpenterLatestAliasButOldAMIs(t *testing.T) {
	f := healthy()
	f.Nodes[1].Kubelet = "v1.34.4-eks-1"
	f.Karpenter.NodeClasses[0].AMIs = []ResolvedAMI{{ID: "ami-1", Name: "amazon-eks-node-al2023-x86_64-standard-1.34-v20250715"}}
	f.Karpenter.ControllerImage = "public.ecr.aws/karpenter/controller:1.0.1@sha256:abc"
	mustContain(t, problemText(run(f).Check(CheckNodeVersions)),
		"alias is @latest", "controller too old", "Upgrade Karpenter", "karpenter controller: 1.0.1")
}

func TestKarpenterResolvedNewButNotDrifted(t *testing.T) {
	f := healthy()
	f.Nodes[1].Kubelet = "v1.35.1-eks-1"
	mustContain(t, problemText(run(f).Check(CheckNodeVersions)), "already resolves 1.36 AMIs", "not marked these nodes Drifted")
}

func TestKarpenterDriftBlocked(t *testing.T) {
	f := healthy()
	f.Nodes[1].Kubelet = "v1.35.1-eks-1"
	f.Nodes[1].DoNotDisruptPods = []string{"ml/train-0"}
	f.Karpenter.NodeClaims[0].Drifted = "True"
	f.Karpenter.NodePools[0].Budgets = []Budget{{Nodes: "10%"}, {Nodes: "0", Reasons: []string{"Drifted"}}}
	f.PDBs = []PDB{{Namespace: "app", Name: "web", Expected: 2, CurrentHealthy: 2, DesiredHealthy: 2}}
	mustContain(t, problemText(run(f).Check(CheckNodeVersions)),
		"marked 1/1 of them Drifted but has not replaced them",
		`disruption budget nodes="0" always`,
		"ml/train-0",
		"PDB app/web",
		"grep -iE 'drift|disrupt|cannot'")
}

func TestKarpenterBudgetForOtherReasonDoesNotBlockDrift(t *testing.T) {
	f := healthy()
	f.Nodes[1].Kubelet = "v1.35.1-eks-1"
	f.Karpenter.NodeClaims[0].Drifted = "True"
	f.Karpenter.NodePools[0].Budgets = []Budget{{Nodes: "0", Reasons: []string{"Underutilized"}}}
	txt := problemText(run(f).Check(CheckNodeVersions))
	if strings.Contains(txt, "disruption budget") {
		t.Errorf("an Underutilized-only budget was reported as blocking drift:\n%s", txt)
	}
	mustContain(t, txt, "no budget, do-not-disrupt or PDB blocker is visible")
}

func TestKarpenterNodeWithoutClaim(t *testing.T) {
	f := healthy()
	f.Nodes = append(f.Nodes, Node{Name: "orphan", Kubelet: "v1.35.0-eks-1", Ready: true, Owner: OwnerKarpenter, OwnerName: "default", NodeClass: "default"})
	mustContain(t, problemText(run(f).Check(CheckNodeVersions)), "have no NodeClaim", "never replace them")
}

func TestFargate(t *testing.T) {
	f := healthy()
	f.Nodes = append(f.Nodes, Node{Name: "fargate-ip-9", Kubelet: "v1.35.4-eks-1", Ready: true, Owner: OwnerFargate})
	mustContain(t, problemText(run(f).Check(CheckNodeVersions)), "Fargate nodes are per pod", "rollout restart")
}

func TestNotReadyNode(t *testing.T) {
	f := healthy()
	f.Nodes[0].Ready = false
	c := run(f).Check(CheckNodeHealth)
	if c.Status != StatusFail {
		t.Fatalf("node health %s", c.Status)
	}
	mustContain(t, problemText(c), "1 node(s) NotReady", "ip-1")
}

func TestKubeProxyBehindAndMinorMismatch(t *testing.T) {
	f := healthy()
	f.Addons[0].Containers[0].Image = "r/kube-proxy:v1.35.3-eksbuild.25"
	c := run(f).Check(CheckKubeProxy)
	if c.Status != StatusFail {
		t.Fatalf("kube-proxy %s", c.Status)
	}
	if len(c.Problems) != 1 {
		t.Errorf("want one merged problem, got %d", len(c.Problems))
	}
	mustContain(t, problemText(c), "behind the latest EKS add-on version", "v1.36.0-eksbuild.21", "1.35 kube-proxy on a 1.36 cluster was left behind", "Add-ons > kube-proxy > Edit")
}

func TestEksbuildNumbersCompareNumerically(t *testing.T) {
	f := healthy()
	f.Addons[0].Containers[0].Image = "r/kube-proxy:v1.36.0-eksbuild.3" // 3 < 21
	if s := run(f).Check(CheckKubeProxy).Status; s != StatusFail {
		t.Errorf("eksbuild.3 vs eksbuild.21: %s, want FAIL", s)
	}
	f.Addons[0].Containers[0].Image = "r/kube-proxy:v1.36.0-eksbuild.30"
	r := run(f)
	if s := r.Check(CheckKubeProxy).Status; s != StatusPass {
		t.Errorf("newer build: %s, want PASS", s)
	}
	mustContain(t, r.Check(CheckKubeProxy).Detail, "newer than the built-in target")
}

func TestSelfManagedAddonFix(t *testing.T) {
	f := healthy()
	f.Addons[1].ManagedByEKS = false
	f.Addons[1].Containers[0].Image = "artifactory.example.com/eks/coredns:v1.11.4-eksbuild.2"
	c := run(f).Check(CheckCoreDNS)
	mustContain(t, c.Detail, "[self-managed]")
	mustContain(t, problemText(c), "self-managed install", "Adopt it as an EKS add-on", "custom zones/forwards, choose Preserve")
}

func TestCoreDNSRolloutStuck(t *testing.T) {
	f := healthy()
	f.Addons[1].Updated, f.Addons[1].Ready = 1, 1
	c := run(f).Check(CheckCoreDNS)
	if c.Status != StatusFail {
		t.Fatalf("coredns %s", c.Status)
	}
	mustContain(t, problemText(c), "Rollout not finished: 1/2", "1/2 pods ready", "rollout status deployment/coredns")
}

func TestNoTargetKnown(t *testing.T) {
	r := Analyze(healthy(), DefaultTargets(35))
	c := r.Check(CheckCoreDNS)
	if c.Status != StatusWarn {
		t.Fatalf("coredns without target: %s", c.Status)
	}
	mustContain(t, problemText(c), "No built-in CoreDNS target for 1.35", "--coredns")
}

func TestVPCCNIInitDrift(t *testing.T) {
	f := healthy()
	f.Addons[2].Containers[0].Image = "r/amazon-k8s-cni-init:v1.20.0-eksbuild.1"
	c := run(f).Check(CheckVPCCNI)
	if c.Status != StatusFail {
		t.Fatalf("vpc-cni %s", c.Status)
	}
	mustContain(t, problemText(c), "aws-vpc-cni-init (v1.20.0-eksbuild.1) does not match aws-node (v1.23.1-eksbuild.1)", "set image ds/aws-node aws-vpc-cni-init=")
	mustContain(t, c.Detail, "nodeagent v1.3.0-eksbuild.1")
}

func TestAddonMissing(t *testing.T) {
	f := healthy()
	f.Addons[0] = Addon{Name: AddonKubeProxy, Kind: "DaemonSet", Object: "kube-system/kube-proxy"}
	c := run(f).Check(CheckKubeProxy)
	if c.Status != StatusWarn {
		t.Fatalf("missing kube-proxy: %s", c.Status)
	}
	mustContain(t, problemText(c), "Cilium")
}

func TestSystemPods(t *testing.T) {
	f := healthy()
	f.KubeSystemPods = append(f.KubeSystemPods,
		Pod{Name: "aws-node-x", Phase: "Running", Ready: false, Reason: "CrashLoopBackOff", Restarts: 12, Node: "ip-2"},
		Pod{Name: "ebs-csi-node-y", Phase: "Running", Ready: true, Restarts: 7},
		Pod{Name: "job-done", Phase: "Succeeded"},
	)
	c := run(f).Check(CheckSystemPods)
	if c.Status != StatusFail {
		t.Fatalf("system pods %s", c.Status)
	}
	mustContain(t, problemText(c), "aws-node-x (CrashLoopBackOff, 12 restarts, on ip-2)", "ebs-csi-node-y (Running, 7 restarts)")
	mustContain(t, c.Detail, "4 pods, 1 not ready, 1 restarting")
}

func TestUncollectedIsSkipNotPass(t *testing.T) {
	f := healthy()
	f.Collected[KindAddons] = false
	f.Errors = []string{"addons: kube-system/aws-node: forbidden"}
	r := run(f)
	for _, id := range []string{CheckKubeProxy, CheckCoreDNS, CheckVPCCNI} {
		if s := r.Check(id).Status; s != StatusSkip {
			t.Errorf("%s: %s, want SKIP", id, s)
		}
	}
	mustContain(t, r.Check(CheckVPCCNI).Detail, "forbidden")
	if r.Status != StatusSkip {
		t.Errorf("verdict %s, want SKIP", r.Status)
	}
}

func TestResolveContexts(t *testing.T) {
	ctxs := []string{
		"arn:aws:eks:us-east-1:330470878083:cluster/rd-bcg-sbx-01",
		"arn:aws:eks:us-east-1:330470878083:cluster/rd-bcg-sbx-011",
		"admin@atlas.us-east-1.eksctl.io",
		"kind-dev",
	}
	got, err := ResolveContexts([]string{"rd-bcg-sbx-01", " atlas ", "kind-dev", "rd-bcg-sbx-01"}, ctxs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ctxs[0], ctxs[2], ctxs[3]}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v want %v", got, want)
	}
	if _, err := ResolveContexts([]string{"nope"}, ctxs); err == nil || !strings.Contains(err.Error(), "no matching kube context") {
		t.Errorf("unknown name: %v", err)
	}
	if ShortName(ctxs[0]) != "rd-bcg-sbx-01" || ShortName("kind-dev") != "kind-dev" {
		t.Error("ShortName")
	}
}

func TestVersionHelpers(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.36.0-eksbuild.21", "v1.36.0-eksbuild.21", 0},
		{"v1.36.0-eksbuild.3", "v1.36.0-eksbuild.21", -1},
		{"v1.14.3-eksbuild.3", "v1.14.2-eksbuild.4", 1},
		{"v1.23.1-eksbuild.1", "v1.23.1", 1},
		{"v1.9.0", "v1.10.0", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%s,%s)=%d want %d", c.a, c.b, got, c.want)
		}
	}
	if imageTag("host:5000/a/b:v1@sha256:x") != "v1" || imageTag("host:5000/a/b") != "" || imageTag("b@sha256:x") != "" {
		t.Error("imageTag")
	}
	for in, want := range map[string]int{"1.36": 36, "36": 36, "v1.36.2-eks-x": 36} {
		if got, err := ParseMinor(in); err != nil || got != want {
			t.Errorf("ParseMinor(%s)=%d,%v", in, got, err)
		}
	}
	if _, err := ParseMinor("latest"); err == nil {
		t.Error("ParseMinor accepted garbage")
	}
}
