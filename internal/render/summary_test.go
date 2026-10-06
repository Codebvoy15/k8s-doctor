package render

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Codebvoy15/k8s-doctor/internal/detect"
)

// Regenerate after an intentional change: go test ./internal/render -update
var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
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
		t.Errorf("%s differs; run with -update and review the diff\n--- got:\n%s", path, got)
	}
}

func TestSummaryGolden(t *testing.T) {
	for _, name := range []string{"memory-overcommit", "cpu-hotspot", "shop-stage", "dynatrace-gap"} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			Summary(&buf, detect.Run(snap(t, name), detect.Registry()), Options{Context: "my-ctx"})
			golden(t, name+".summary.txt", buf.Bytes())
		})
	}
}

func TestMarkdownGolden(t *testing.T) {
	for _, name := range []string{"memory-overcommit", "cpu-hotspot"} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			Markdown(&buf, detect.Run(snap(t, name), detect.Registry()), Options{})
			golden(t, name+".md", buf.Bytes())
		})
	}
}

// The first real run printed one node's details four times and ran to
// hundreds of lines. The default view must fit on a screen and show each
// node once.
func TestSummaryIsOneScreen(t *testing.T) {
	var buf bytes.Buffer
	Summary(&buf, detect.Run(snap(t, "memory-overcommit"), detect.Registry()), Options{})
	out := buf.String()
	if n := strings.Count(out, "\n"); n > 40 {
		t.Errorf("summary is %d lines; it must fit on one screen:\n%s", n, out)
	}
	if c := strings.Count(out, "ip-10-0-1-11  "); c != 1 {
		t.Errorf("node ip-10-0-1-11 should have exactly one row, got %d:\n%s", c, out)
	}
	for _, want := range []string{
		"2 nodes ≥85% memory  ·  8 workloads to resize  ·  1 degraded",
		"Kafka CR kafka",
		"Helm release platform-prometheus",
		"drives ip-10-0-1-12",
		"5 lower-tier findings hidden: --min-tier info",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
	for _, minor := range []string{"schemaregistry", "kafkarestproxy", "ci-runner"} {
		if strings.Contains(out, minor) {
			t.Errorf("minor finding %s should be hidden by default:\n%s", minor, out)
		}
	}
	// a namespace is named once, on its first row
	if c := strings.Count(out, "  confluent  "); c != 1 {
		t.Errorf("namespace should label its group once, got %d", c)
	}
}

// Colors must not shift columns: the colored and plain outputs line up the same.
func TestSummaryColorDoesNotMisalign(t *testing.T) {
	rep := detect.Run(snap(t, "memory-overcommit"), detect.Registry())
	var plain, colored bytes.Buffer
	Summary(&plain, rep, Options{})
	Summary(&colored, rep, Options{Color: true})
	if !strings.Contains(colored.String(), "\x1b[") {
		t.Fatal("expected ANSI codes with Color on")
	}
	if got := ansi.ReplaceAllString(colored.String(), ""); got != plain.String() {
		t.Errorf("colored output misaligned:\n%s\n--- plain:\n%s", got, plain.String())
	}
}

func TestMarkdownForAppTeams(t *testing.T) {
	var buf bytes.Buffer
	Markdown(&buf, detect.Run(snap(t, "memory-overcommit"), detect.Registry()), Options{})
	out := buf.String()
	for _, want := range []string{
		"### `confluent`",
		"### `kube-system`",
		"| kafka | 23.8Gi | none | Kafka CR kafka | requests: {memory: 29440Mi}, limits: {memory: 29440Mi} |",
		"- **kafka**: Set the resources in the Confluent for Kubernetes Kafka CR \"kafka\" under spec.podTemplate.resources (CFK owns the statefulset and reverts direct edits): `kubectl edit kafka kafka -n confluent`",
		"confirm sizing against p95/peak history",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q:\n%s", want, out)
		}
	}
}

// --explain on a node: the node prints in full on its own, with its causes named.
func TestExplainNodeRendersOnItsOwn(t *testing.T) {
	rep, ok := detect.Explain(detect.Run(snap(t, "memory-overcommit"), detect.Registry()), "ip-10-0-1-12")
	if !ok {
		t.Fatal("node not found by its short name")
	}
	var buf bytes.Buffer
	Report(&buf, rep, Options{Verbose: true, MinTier: detect.TierInfo})
	out := buf.String()
	for _, want := range []string{"Node ip-10-0-1-12.ec2.internal memory at 85% of allocatable, 17% requested", "caused by: confluent/controlcenter, confluent/ksqldb-cluster-1, kube-system/platform-prometheus-server", "Allocated resources"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q:\n%s", want, out)
		}
	}
}

func TestTableHelpers(t *testing.T) {
	if got := joinClip([]string{"alpha", "beta", "gamma", "delta"}, 14); got != "alpha, beta, +2 more" {
		t.Errorf("joinClip = %q", got)
	}
	if got := clip("abcdefgh", 5); got != "abcd…" {
		t.Errorf("clip = %q", got)
	}
	if visibleLen("\x1b[31mred\x1b[0m") != 3 {
		t.Error("visibleLen must ignore ANSI codes")
	}
}
