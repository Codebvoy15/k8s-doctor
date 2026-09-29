package render

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Codebvoy15/k8s-doctor/internal/detect"
	"github.com/Codebvoy15/k8s-doctor/internal/fleet"
	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

func snap(t *testing.T, name string) *model.Snapshot {
	b, err := os.ReadFile("../detect/testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var s model.Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func TestReportNestsSymptomsAndCollapsesPatterns(t *testing.T) {
	var buf bytes.Buffer
	Report(&buf, detect.Run(snap(t, "shop-stage"), detect.Registry()), Options{Verbose: true})
	out := buf.String()
	if !strings.Contains(out, "└─ causes") || !strings.Contains(out, "StatefulSet shop-stage/elasticsearch1") {
		t.Errorf("ES should be nested under its missing pull secret:\n%s", out)
	}
	if strings.Count(out, "StatefulSet shop-stage/elasticsearch1: 0/1 ready") != 1 {
		t.Error("a symptom must appear once (nested), not also at top level")
	}
	if !strings.Contains(out, "✎") {
		t.Error("mutating steps must be marked in verbose output")
	}

	buf.Reset()
	Report(&buf, detect.Run(snap(t, "dynatrace-gap"), detect.Registry()), Options{})
	out = buf.String()
	if !strings.Contains(out, `Secret "dynatrace-bootstrapper-config" missing where pods reference it  (x3 namespaces)`) {
		t.Errorf("3 namespaces with the same missing secret should collapse into one line:\n%s", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("no ANSI codes when Color is off")
	}
}

func TestFleetTable(t *testing.T) {
	res := fleet.Run(context.Background(), []string{"eks-a", "eks-b"}, func(ctx context.Context, c string) (*model.Snapshot, error) {
		s := snap(t, "dynatrace-gap")
		s.Cluster = c
		return s, nil
	}, fleet.Options{})
	var buf bytes.Buffer
	Fleet(&buf, res, Options{})
	out := buf.String()
	for _, want := range []string{"CLUSTER", "eks-a", "eks-b", "FLEET PATTERNS", "6 occurrence(s) in 2 cluster(s), oldest"} {
		if !strings.Contains(out, want) {
			t.Errorf("fleet output missing %q:\n%s", want, out)
		}
	}
}

func TestShortCluster(t *testing.T) {
	cases := map[string]string{
		"arn:aws:eks:ap-southeast-1:123456789012:cluster/apac-nprod": "apac-nprod",
		"rancher-prod-1": "rancher-prod-1",
	}
	for in, want := range cases {
		if got := ShortCluster(in); got != want {
			t.Errorf("ShortCluster(%q) = %q, want %q", in, got, want)
		}
	}
}

// Regression from the first real fleet run: an IMPACTING finding in one cluster
// was invisible because the fleet view only printed repeated patterns.
func TestFleetShowsSingleClusterImpacting(t *testing.T) {
	arn := "arn:aws:eks:ap-southeast-1:123456789012:cluster/apac-nprod"
	res := fleet.Run(context.Background(), []string{arn, "eks-b"}, func(ctx context.Context, c string) (*model.Snapshot, error) {
		name := "dynatrace-gap"
		if c == arn {
			name = "real-run-apac"
		}
		s := snap(t, name)
		s.Cluster = c
		return s, nil
	}, fleet.Options{})
	var buf bytes.Buffer
	Fleet(&buf, res, Options{})
	out := buf.String()
	for _, want := range []string{"NEEDS ATTENTION", "apac-nprod", "Deployment jobs/worker: 0/2 ready (NoPods)", "chronic OOMKills (915 restarts)"} {
		if !strings.Contains(out, want) {
			t.Errorf("fleet output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "arn:aws:eks") {
		t.Errorf("terminal output should use short cluster names:\n%s", out)
	}
}
