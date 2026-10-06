package detect

import (
	"sort"
	"strings"
	"testing"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// Fixtures memory-overcommit and cpu-hotspot are sanitized reconstructions of a
// real investigation (2026-10-06): the numbers are real, the names are not.

var resourceDetectors = []string{"resource-requests", "node-saturation", "cpu-hotspot"}

func resourceFindings(r Report) []Finding {
	var out []Finding
	for _, id := range resourceDetectors {
		out = append(out, byDetector(r, id)...)
	}
	return out
}

func stepsText(f Finding) string {
	var b strings.Builder
	if f.Remediation != nil {
		for _, s := range f.Remediation.Steps {
			b.WriteString(s.Description + " | " + s.Command + "\n")
		}
	}
	return b.String()
}

// Older snapshots have no requests, allocatable or usage. The resource
// detectors must stay silent rather than read "not recorded" as "none set".
func TestResourceDetectorsSilentOnOlderSnapshots(t *testing.T) {
	for _, name := range []string{"dynatrace-gap", "secrets-forbidden", "shop-stage", "node-notready", "real-run-apac"} {
		if fs := resourceFindings(run(t, name)); len(fs) != 0 {
			t.Errorf("%s: %d resource findings from a snapshot without resource data: %s", name, len(fs), fs[0].Title)
		}
	}
}

// metrics-server missing or forbidden: no usage means no claim about usage.
func TestNoMetricsNoResourceFindings(t *testing.T) {
	s := load(t, "memory-overcommit")
	s.Collected[model.KindPodMetrics] = false
	s.Collected[model.KindNodeMetrics] = false
	if fs := resourceFindings(Run(s, Registry())); len(fs) != 0 {
		t.Errorf("reported %d resource findings without metrics: %s", len(fs), fs[0].Title)
	}
	// node metrics alone are not enough to blame a workload
	s = load(t, "memory-overcommit")
	s.Collected[model.KindPodMetrics] = false
	r := Run(s, Registry())
	if n := len(byDetector(r, "resource-requests")); n != 0 {
		t.Errorf("resource-requests needs pod usage, got %d findings", n)
	}
	for _, f := range byDetector(r, "node-saturation") {
		if len(f.CausedBy) != 0 {
			t.Errorf("node finding linked to causes without pod usage: %v", f.CausedBy)
		}
	}
}

// The node at 100% memory with 41% requested is explained by exactly the
// unrequested CFK pods running on it, and nothing else is blamed.
func TestMemoryOvercommitAttribution(t *testing.T) {
	r := run(t, "memory-overcommit")
	nodes := byDetector(r, "node-saturation")
	if len(nodes) != 1 {
		t.Fatalf("want 1 node-saturation finding (only one node is >=85%%), got %d", len(nodes))
	}
	n := nodes[0]
	if n.Tier != TierLatent || !strings.Contains(n.Title, "memory at 100% of allocatable, 41% requested") {
		t.Errorf("node finding: [%s] %s", n.Tier, n.Title)
	}
	causes := map[string]bool{}
	for _, f := range r.Findings {
		for _, id := range n.CausedBy {
			if f.ID == id {
				causes[f.Subject.Name] = true
			}
		}
	}
	var got []string
	for c := range causes {
		got = append(got, c)
	}
	sort.Strings(got)
	want := []string{"dev-connect-1", "kraftcontroller", "ksqldb-cluster-1", "test-connect-1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("node caused_by %v, want %v (kafka and controlcenter are on other nodes)", got, want)
	}
	// the CDC pod uses 5.3Gi over its request on this node: listed as a
	// contributor, but it has a request and stays under 2x, so it is not a cause
	if !strings.Contains(strings.Join(n.Evidence, "\n"), "orders-cdc/cdc-orders") {
		t.Error("CDC pod should be listed among the node's contributors")
	}

	flagged := map[string]bool{}
	for _, f := range byDetector(r, "resource-requests") {
		flagged[f.Subject.Name] = true
		if f.Tier != TierLatent || !strings.HasSuffix(f.Title, "(BestEffort)") {
			t.Errorf("[%s] %s", f.Tier, f.Title)
		}
	}
	for _, name := range []string{"kafka", "controlcenter", "ksqldb-cluster-1", "dev-connect-1", "test-connect-1", "kraftcontroller"} {
		if !flagged[name] {
			t.Errorf("%s uses GiBs with no request and was not reported", name)
		}
	}
	for _, quiet := range []string{"kafka-exporter", "debug-shell", "cdc-orders", "workorder-api", "nightly-load-29850", "host-shield"} {
		if flagged[quiet] {
			t.Errorf("%s must not be reported (small, within its request, or a finished Job)", quiet)
		}
	}
}

// A fix that the operator or Helm reverts is not a fix.
func TestResizeGoesWhereItSticks(t *testing.T) {
	r := run(t, "memory-overcommit")
	kafka := findBySubject(r, "resource-requests", "StatefulSet", "kafka")
	if len(kafka) != 1 {
		t.Fatalf("want one kafka finding, got %d", len(kafka))
	}
	steps := stepsText(kafka[0])
	if !strings.Contains(steps, "spec.podTemplate.resources") || !strings.Contains(steps, "kubectl edit kafka kafka -n confluent") {
		t.Errorf("CFK-managed: fix must target the Kafka CR:\n%s", steps)
	}
	if strings.Contains(steps, "kubectl set resources") {
		t.Errorf("CFK-managed: patching the StatefulSet would be reverted:\n%s", steps)
	}

	// Helm-managed and under-requested: point at the release values
	s := load(t, "memory-overcommit")
	for i := range s.Pods {
		if strings.HasPrefix(s.Pods[i].Name, "cdc-orders-") {
			s.Pods[i].Containers[0].Resources.RequestMemBytes = 4 * giB
		}
	}
	cdc := findBySubject(Run(s, Registry()), "resource-requests", "Deployment", "cdc-orders")
	if len(cdc) != 1 {
		t.Fatalf("cdc-orders requesting 4Gi and using 13.3Gi should be under-requested")
	}
	if !strings.Contains(cdc[0].Title, "requests 4.0Gi") {
		t.Errorf("title: %s", cdc[0].Title)
	}
	if steps := stepsText(cdc[0]); !strings.Contains(steps, "helm get values cdc-orders -n orders-cdc") || strings.Contains(steps, "kubectl set resources") {
		t.Errorf("Helm-managed: fix must go through the release values:\n%s", steps)
	}

	// an unknown operator: find the field in its CRD instead of guessing
	s = load(t, "memory-overcommit")
	for i := range s.Workloads {
		if s.Workloads[i].Name == "controlcenter" {
			s.Workloads[i].Controller = &model.ControllerRef{APIVersion: "example.io/v1", Kind: "Dashboard", Name: "cc"}
		}
	}
	cc := findBySubject(Run(s, Registry()), "resource-requests", "StatefulSet", "controlcenter")
	if steps := stepsText(cc[0]); !strings.Contains(steps, "kubectl explain dashboard.spec --api-version=example.io/v1") {
		t.Errorf("unknown operator: expected a kubectl explain step:\n%s", steps)
	}
}

func TestCPUHotspotScalingContext(t *testing.T) {
	r := run(t, "cpu-hotspot")
	hs := findBySubject(r, "cpu-hotspot", "Deployment", "web-api")
	if len(hs) != 1 {
		t.Fatalf("want one hotspot for web-api, got %d", len(hs))
	}
	f := hs[0]
	if f.Tier != TierLatent || !strings.Contains(f.Title, "one replica uses 2.5 cores (64% of node") || !strings.HasSuffix(f.Title, "no HPA") {
		t.Errorf("[%s] %s", f.Tier, f.Title)
	}
	ev := strings.Join(f.Evidence, "\n")
	for _, want := range []string{"replica load is uneven", "(2.1x)", "replicas restarted 64 times"} {
		if !strings.Contains(ev, want) {
			t.Errorf("evidence missing %q:\n%s", want, ev)
		}
	}
	if !strings.Contains(stepsText(f), "kubectl autoscale deployment web-api -n storefront-prod --cpu-percent=70 --min=2 --max=6") {
		t.Errorf("expected an HPA step:\n%s", stepsText(f))
	}
	if n := len(findBySubject(r, "cpu-hotspot", "StatefulSet", "eureka")); n != 0 {
		t.Error("eureka uses 11m; not a hotspot")
	}
	// the 74% node is below the node threshold: the workload finding carries it
	if n := len(byDetector(r, "node-saturation")); n != 0 {
		t.Errorf("no node is >=85%%, got %d node findings", n)
	}

	// CPU request proposal: the mean across replicas, not the busiest one
	rr := findBySubject(r, "resource-requests", "Deployment", "web-api")
	if len(rr) != 1 || !strings.Contains(stepsText(rr[0]), "--requests=cpu=1900m,memory=2Gi --limits=memory=2Gi") {
		t.Errorf("want cpu=1900m (mean of 2516m and 1203m) and memory=2Gi:\n%s", stepsText(rr[0]))
	}

	withHPA := func(current, max int32) Finding {
		s := load(t, "cpu-hotspot")
		s.HPAs = []model.HPA{{Namespace: "storefront-prod", Name: "web-api", TargetKind: "Deployment", TargetName: "web-api", Min: 2, Max: max, Current: current}}
		return findBySubject(Run(s, Registry()), "cpu-hotspot", "Deployment", "web-api")[0]
	}
	if f := withHPA(4, 4); f.Tier != TierLatent || !strings.HasSuffix(f.Title, "HPA at max 4/4") {
		t.Errorf("HPA at max: [%s] %s", f.Tier, f.Title)
	}
	if f := withHPA(2, 6); f.Tier != TierInfo || !strings.HasSuffix(f.Title, "HPA 2-6") {
		t.Errorf("HPA with headroom: [%s] %s", f.Tier, f.Title)
	}

	// HPAs not collected: never claim "no HPA"
	s := load(t, "cpu-hotspot")
	s.Collected[model.KindHPA] = false
	f = findBySubject(Run(s, Registry()), "cpu-hotspot", "Deployment", "web-api")[0]
	if f.Tier != TierInfo || strings.Contains(f.Title, "no HPA") || strings.Contains(stepsText(f), "kubectl autoscale") {
		t.Errorf("HPA unknown: [%s] %s", f.Tier, f.Title)
	}
}

// A saturated CPU node is INFO (compressible) and linked to the hot workload.
func TestCPUSaturatedNodeIsInfoAndLinked(t *testing.T) {
	s := load(t, "cpu-hotspot")
	for i := range s.Usage.Nodes {
		if s.Usage.Nodes[i].Name == "ip-10-0-2-102.compute.internal" {
			s.Usage.Nodes[i].CPUMilli = 3600 // 91%
		}
	}
	r := Run(s, Registry())
	nodes := byDetector(r, "node-saturation")
	if len(nodes) != 1 || nodes[0].Tier != TierInfo {
		t.Fatalf("want one INFO cpu node finding, got %+v", nodes)
	}
	hot := findBySubject(r, "cpu-hotspot", "Deployment", "web-api")[0]
	linked := false
	for _, id := range nodes[0].CausedBy {
		linked = linked || id == hot.ID
	}
	if !linked {
		t.Errorf("cpu node finding should be caused by the web-api hotspot: %v", nodes[0].CausedBy)
	}
	// a memory-only cause must never be linked to a CPU node finding
	for _, f := range byDetector(r, "resource-requests") {
		for _, e := range f.Explains {
			if e == nodes[0].ID && !strings.Contains(f.Title, "CPU") {
				t.Errorf("memory-only finding linked to a CPU node: %s", f.Title)
			}
		}
	}
}

func TestSizingHelpers(t *testing.T) {
	cases := []struct{ got, want string }{
		{qtyMem(suggestMem(24394 * miB)), "29440Mi"}, // x1.2 = 29272.8Mi, rounded up to a 256Mi step
		{qtyMem(suggestMem(1676 * miB)), "2Gi"},      // x1.2 = 2011Mi -> 2048Mi
		{qtyCPU(suggestCPU(1859)), "1900m"},
		{fmtMem(0), "none"},
		{fmtCPU(851), "851m"},
		{fmtCPU(2516), "2.5 cores"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %s, want %s", c.got, c.want)
		}
	}
}
