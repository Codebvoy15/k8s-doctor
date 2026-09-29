package detect

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// Regenerate golden files after an intentional behavior change:
//
//	go test ./internal/detect -update
//
// then review the diff like any other code change.
var update = flag.Bool("update", false, "rewrite golden files")

func load(t *testing.T, name string) *model.Snapshot {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var s model.Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return &s
}

func run(t *testing.T, name string) Report {
	t.Helper()
	return Run(load(t, name), Registry())
}

func TestGolden(t *testing.T) {
	for _, name := range []string{"dynatrace-gap", "secrets-forbidden", "shop-stage", "node-notready", "real-run-apac"} {
		t.Run(name, func(t *testing.T) {
			got, err := json.MarshalIndent(run(t, name), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden file (run with -update): %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("report differs from %s; run with -update and review the diff", path)
			}
		})
	}
}

func byDetector(r Report, id string) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Detector == id {
			out = append(out, f)
		}
	}
	return out
}

// The 2026-09 Dynatrace incident: operator secret missing in 3 namespaces while
// pods kept running. Must be LATENT (not a false outage) and aggregate by pattern.
func TestDynatraceGapIsLatentAndSharesPattern(t *testing.T) {
	r := run(t, "dynatrace-gap")
	mr := byDetector(r, "missing-reference")
	patterns := map[string]int{}
	for _, f := range mr {
		if f.Tier != TierLatent {
			t.Errorf("%s: tier %s, want LATENT (pods are Ready)", f.Title, f.Tier)
		}
		patterns[f.PatternKey]++
	}
	if n := patterns["missing-reference|Secret|dynatrace-bootstrapper-config"]; n != 3 {
		t.Errorf("dynatrace-bootstrapper-config findings = %d, want 3 (one per namespace, same pattern)", n)
	}
	if patterns["missing-reference|Secret|k8s-informer-repo-cred"] != 1 {
		t.Errorf("pull secret inherited from the ServiceAccount was not detected: %v", patterns)
	}
	if len(byDetector(r, "workload-unavailable")) != 0 {
		t.Error("no workload is down; workload-unavailable must be silent")
	}
	for _, f := range mr {
		if strings.Contains(f.Subject.Name, "dynatrace") {
			if f.Since == nil {
				t.Error("since should come from the FailedMount event first_seen")
			}
			if !strings.Contains(strings.Join(f.Evidence, "\n"), "likely existed and was deleted or stopped syncing around 2026-09-15") {
				t.Errorf("pods predate the failures by 5 days; expected the deleted-or-unsynced inference: %v", f.Evidence)
			}
		}
	}
}

// Absence of an object proves nothing when the collector was not allowed to list that kind.
func TestNoFalsePositivesWhenSecretsNotCollected(t *testing.T) {
	r := run(t, "secrets-forbidden")
	for _, f := range byDetector(r, "missing-reference") {
		if f.Subject.Kind == model.KindSecret {
			t.Errorf("reported missing Secret %s although secrets were not collected", f.Subject)
		}
	}
	if len(r.Errors) == 0 {
		t.Error("collection error should be carried into the report")
	}
}

// ES is down because its pull secret is missing: the secret finding must be
// IMPACTING and explain the StatefulSet finding. web's 502 must NOT be linked
// to anything yet: that link needs the service graph, and we do not guess.
func TestShopStageCorrelation(t *testing.T) {
	r := run(t, "shop-stage")
	var secret, es, web *Finding
	for i := range r.Findings {
		f := &r.Findings[i]
		switch {
		case f.Detector == "missing-reference" && f.Subject.Name == "shop-registry-cred":
			secret = f
		case f.Subject.Kind == "StatefulSet" && f.Subject.Name == "elasticsearch1":
			es = f
		case f.Subject.Kind == "Deployment" && f.Subject.Name == "web":
			web = f
		}
	}
	if secret == nil || es == nil || web == nil {
		t.Fatalf("missing findings: secret=%v es=%v web=%v", secret != nil, es != nil, web != nil)
	}
	if secret.Tier != TierImpacting {
		t.Errorf("secret tier %s, want IMPACTING (it blocks ES)", secret.Tier)
	}
	if len(es.CausedBy) != 1 || es.CausedBy[0] != secret.ID {
		t.Errorf("ES caused_by = %v, want [%s]", es.CausedBy, secret.ID)
	}
	if len(web.CausedBy) != 0 {
		t.Errorf("web must not be linked without graph evidence, got %v", web.CausedBy)
	}
	if !strings.Contains(web.Title, "ReadinessProbe") {
		t.Errorf("web reason should be ReadinessProbe: %s", web.Title)
	}
	for _, e := range secret.Evidence {
		if strings.Contains(e, "likely existed") {
			t.Errorf("ES failed right at startup; must not infer the secret existed earlier: %q", e)
		}
	}
	if r.Findings[0].ID != secret.ID {
		t.Errorf("root cause should sort first, got %q", r.Findings[0].Title)
	}
}

func TestNodeNotReadyExplainsWorkload(t *testing.T) {
	r := run(t, "node-notready")
	nodes := byDetector(r, "node-unhealthy")
	wls := byDetector(r, "workload-unavailable")
	if len(nodes) != 1 || nodes[0].Tier != TierImpacting {
		t.Fatalf("want 1 IMPACTING node finding, got %+v", nodes)
	}
	if len(wls) != 1 || wls[0].Subject.Name != "api" || wls[0].Tier != TierImpacting {
		t.Fatalf("want Deployment api IMPACTING, got %+v", wls)
	}
	if len(wls[0].CausedBy) != 1 || wls[0].CausedBy[0] != nodes[0].ID {
		t.Errorf("api should be caused by the node: %v", wls[0].CausedBy)
	}
}

// Same snapshot, same IDs: the findings store (phase 2) depends on this.
func TestFingerprintsStableAndUnique(t *testing.T) {
	a, b := run(t, "dynatrace-gap"), run(t, "dynatrace-gap")
	seen := map[string]bool{}
	for i := range a.Findings {
		if a.Findings[i].ID != b.Findings[i].ID {
			t.Fatal("fingerprints differ between identical runs")
		}
		if seen[a.Findings[i].ID] {
			t.Errorf("duplicate fingerprint %s", a.Findings[i].ID)
		}
		seen[a.Findings[i].ID] = true
	}
}

func TestMutatingStepsAreMarked(t *testing.T) {
	for _, name := range []string{"dynatrace-gap", "shop-stage", "node-notready"} {
		for _, f := range run(t, name).Findings {
			if f.Remediation == nil {
				continue
			}
			for _, s := range f.Remediation.Steps {
				for _, verb := range []string{"kubectl delete", "kubectl drain", "kubectl uncordon", "kubectl apply", "kubectl patch", "kubectl scale", "rollout undo"} {
					if strings.Contains(s.Command, verb) && !s.Mutating {
						t.Errorf("%s: step %q runs %q but is not marked mutating", f.Title, s.Description, verb)
					}
				}
			}
		}
	}
}

func TestSelect(t *testing.T) {
	if _, err := Select([]string{"nope"}); err == nil {
		t.Error("unknown detector should error")
	}
	d, err := Select([]string{"node-unhealthy"})
	if err != nil || len(d) != 1 {
		t.Fatalf("select: %v %v", d, err)
	}
}

func TestCompletedInitContainerIsNotBlamed(t *testing.T) {
	s := load(t, "node-notready")
	// api pods: add a completed init container ahead of the app container
	for i := range s.Pods {
		if strings.HasPrefix(s.Pods[i].Name, "api-") {
			s.Pods[i].Containers = append([]model.Container{{Name: "init-db", Init: true, State: "terminated", Reason: "Completed", ExitCode: 0}}, s.Pods[i].Containers...)
		}
	}
	r := Run(s, Registry())
	for _, f := range byDetector(r, "workload-unavailable") {
		if strings.Contains(strings.Join(f.Evidence, " "), "init-db") {
			t.Errorf("completed init container blamed: %v", f.Evidence)
		}
	}
}

// Regression tests from the first real fleet run (2026-09-29).
func findBySubject(r Report, detector, kind, name string) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Detector == detector && f.Subject.Kind == kind && f.Subject.Name == name {
			out = append(out, f)
		}
	}
	return out
}

// False positive #1: an evicted pod of a Deployment scaled to 0/0 was reported
// as an IMPACTING outage. Nothing is down; the truth is a LATENT storage-limit problem.
func TestEvictedTombstoneOfScaledDownDeployment(t *testing.T) {
	r := run(t, "real-run-apac")
	if f := findBySubject(r, "workload-unavailable", "Deployment", "reconciler"); len(f) != 0 {
		t.Fatalf("scaled-to-0 deployment reported as unavailable: %s", f[0].Title)
	}
	f := findBySubject(r, "failed-pods", "Deployment", "reconciler")
	if len(f) != 1 {
		t.Fatalf("want one failed-pods finding for reconciler, got %d", len(f))
	}
	if f[0].Tier != TierLatent {
		t.Errorf("own emptyDir limit eviction should be LATENT (recurs), got %s", f[0].Tier)
	}
	for _, want := range []string{`emptyDir "reconciler-log" exceeded its 1G limit`} {
		if !strings.Contains(f[0].Title, want) {
			t.Errorf("title %q should contain %q", f[0].Title, want)
		}
	}
	if !strings.Contains(f[0].Summary, "scaled to 0") {
		t.Errorf("summary should say it is scaled to 0: %s", f[0].Summary)
	}
}

// Understated #2: 915 restarts was reported as "recent OOMKills, down since 2m".
func TestChronicOOMIsCalledChronic(t *testing.T) {
	f := findBySubject(run(t, "real-run-apac"), "workload-unavailable", "StatefulSet", "fluentd")
	if len(f) != 1 {
		t.Fatalf("want one fluentd finding, got %d", len(f))
	}
	if f[0].Tier != TierDegraded || !strings.Contains(f[0].Title, "chronic OOMKills (915 restarts)") {
		t.Errorf("got [%s] %s", f[0].Tier, f[0].Title)
	}
	for _, e := range f[0].Evidence {
		if strings.Contains(e, "down since") {
			t.Errorf("a Ready, restarting workload is not 'down': %q", e)
		}
	}
}

func TestControllerStatusDrivesAvailability(t *testing.T) {
	r := run(t, "real-run-apac")
	if f := findBySubject(r, "workload-unavailable", "Deployment", "api"); len(f) != 0 {
		t.Errorf("3/3 ready per controller; a rollout leftover must not alert: %s", f[0].Title)
	}
	cart := findBySubject(r, "workload-unavailable", "Deployment", "cart")
	if len(cart) != 1 || cart[0].Tier != TierDegraded || !strings.Contains(cart[0].Title, "1/2 ready (CrashLoopBackOff)") {
		t.Errorf("cart: %+v", cart)
	}
	worker := findBySubject(r, "workload-unavailable", "Deployment", "worker")
	if len(worker) != 1 || worker[0].Tier != TierImpacting || !strings.Contains(worker[0].Title, "0/2 ready (NoPods)") {
		t.Errorf("worker (wants 2, has none): %+v", worker)
	}
	evict := findBySubject(r, "failed-pods", "Deployment", "cart")
	if len(evict) != 1 || evict[0].Tier != TierInfo || !strings.Contains(evict[0].Title, "node was low on memory") {
		t.Errorf("node-pressure eviction should be INFO with the resource named: %+v", evict)
	}
}
