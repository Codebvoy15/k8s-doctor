package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Codebvoy15/k8s-doctor/internal/upgrade"
)

func upgradeResults() []upgrade.Result {
	clean := upgrade.Result{Cluster: "arn:aws:eks:us-east-1:1:cluster/clean", Target: upgrade.DefaultTargets(36),
		ControlPlane: "v1.36.2-eks-x", NodesTotal: 3, NodesOnTarget: 3, Status: upgrade.StatusPass,
		Checks: []upgrade.Check{{ID: upgrade.CheckControlPlane, Title: "Control plane", Status: upgrade.StatusPass, Detail: "v1.36.2 (target 1.36)"}}}
	bad := upgrade.Result{Cluster: "arn:aws:eks:us-east-1:1:cluster/broken", Target: upgrade.DefaultTargets(36),
		ControlPlane: "v1.36.2-eks-x", NodesTotal: 4, NodesOnTarget: 2, Status: upgrade.StatusFail,
		Checks: []upgrade.Check{
			{ID: upgrade.CheckControlPlane, Title: "Control plane", Status: upgrade.StatusPass, Detail: "v1.36.2 (target 1.36)"},
			{ID: upgrade.CheckNodeVersions, Title: "Node versions", Status: upgrade.StatusFail, Detail: "2/4 on 1.36",
				Problems: []upgrade.Problem{{Status: upgrade.StatusFail, What: "2 node(s) in managed nodegroup ng-app on v1.34.6",
					Why: []string{"A control-plane upgrade never rolls managed nodegroups"}, Evidence: []string{"nodes: ip-3, ip-4"}, Fix: []string{"Update now"}}}},
		}}
	down := upgrade.Result{Cluster: "gone", Target: upgrade.DefaultTargets(36), Status: upgrade.StatusFail, Error: "API server unreachable: timeout"}
	return []upgrade.Result{clean, bad, down}
}

func TestUpgradeTerminal(t *testing.T) {
	var b bytes.Buffer
	Upgrade(&b, upgradeResults(), Options{Color: false})
	out := b.String()
	for _, want := range []string{"3 cluster(s)  target 1.36", "1 clean · 0 need attention · 2 failing",
		"CLUSTER", "broken", "PASS v1.36.2", "FAIL 2/4", "WHY", "Node versions — 2 node(s) in managed nodegroup ng-app",
		"why       A control-plane upgrade never rolls", "evidence  nodes: ip-3, ip-4", "fix       Update now",
		"API server unreachable: timeout", "add-on targets: kube-proxy v1.36.0-eksbuild.21"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("ANSI codes with Color=false")
	}
	if strings.Count(out, "k8s-doctor post-upgrade  clean") != 0 {
		t.Error("a clean cluster got a full section in fleet mode without -v")
	}
}

func TestUpgradeMarkdown(t *testing.T) {
	var b bytes.Buffer
	UpgradeMarkdown(&b, upgradeResults())
	out := b.String()
	for _, want := range []string{"# Post-upgrade check — target 1.36", "| broken | PASS v1.36.2 | 2/4 |", "| gone | unreachable |",
		"### FAIL Node versions: 2 node(s)", "**Why**", "- A control-plane upgrade never rolls managed nodegroups", "**Fix**"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "## clean") {
		t.Error("clean cluster should not get a section")
	}
}
