package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

func fixture(t *testing.T, name, cluster string) *model.Snapshot {
	t.Helper()
	b, err := os.ReadFile("../detect/testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var s model.Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	s.Cluster = cluster
	return &s
}

// Two clusters with the Dynatrace gap, one broken cluster, one hung cluster.
func TestRunAggregatesAndIsolatesFailures(t *testing.T) {
	collect := func(ctx context.Context, c string) (*model.Snapshot, error) {
		switch c {
		case "broken":
			return nil, errors.New("Unauthorized")
		case "hung":
			<-ctx.Done() // honors cancellation like a real API call
			return nil, ctx.Err()
		}
		return fixture(t, "dynatrace-gap", c), nil
	}
	var progress int
	res := Run(context.Background(), []string{"eks-a", "broken", "hung", "eks-b"}, collect, Options{
		Parallel: 2, Timeout: 200 * time.Millisecond,
		Progress: func(done, total int, r ClusterResult) { progress++ },
	})

	if res.Failed != 2 {
		t.Errorf("failed = %d, want 2", res.Failed)
	}
	if progress != 4 {
		t.Errorf("progress callbacks = %d, want 4", progress)
	}
	if res.Clusters[0].Cluster != "eks-a" || res.Clusters[3].Cluster != "eks-b" {
		t.Error("results must keep input order")
	}
	if !strings.Contains(res.Clusters[2].Error, "timed out") {
		t.Errorf("hung cluster error = %q", res.Clusters[2].Error)
	}
	var dt *Pattern
	for i := range res.Patterns {
		if res.Patterns[i].Key == "missing-reference|Secret|dynatrace-bootstrapper-config" {
			dt = &res.Patterns[i]
		}
	}
	if dt == nil {
		t.Fatalf("dynatrace pattern not aggregated: %+v", res.Patterns)
	}
	if dt.Occurrences != 6 || len(dt.Clusters) != 2 || len(dt.Namespaces) != 3 {
		t.Errorf("pattern = %d occurrences / %d clusters / %d namespaces, want 6/2/3", dt.Occurrences, len(dt.Clusters), len(dt.Namespaces))
	}
	if dt.Since == nil || dt.Since.Format("2006-01-02") != "2026-09-15" {
		t.Errorf("pattern since = %v, want earliest evidence 2026-09-15", dt.Since)
	}
}

func TestPanickingCollectorIsContained(t *testing.T) {
	res := Run(context.Background(), []string{"x"}, func(ctx context.Context, c string) (*model.Snapshot, error) {
		panic("boom")
	}, Options{Timeout: time.Second})
	// the panic happens inside the collector goroutine; runOne must still return an error, not crash the sweep
	if res.Failed != 1 {
		t.Errorf("want the cluster reported as failed, got %+v", res.Clusters)
	}
}
