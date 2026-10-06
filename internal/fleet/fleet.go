// Package fleet sweeps many clusters in parallel and turns per-cluster
// findings into fleet-wide patterns: the same failure in many places is one
// problem with one fix, not N tickets.
//
// It depends only on a CollectFunc, so it is tested with fake collectors and
// knows nothing about client-go.
package fleet

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/detect"
	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// CollectFunc captures a snapshot of one cluster. It must honor ctx cancellation.
type CollectFunc func(ctx context.Context, cluster string) (*model.Snapshot, error)

type Options struct {
	Parallel  int           // clusters in flight at once (default 8)
	Timeout   time.Duration // per-cluster budget for collection + detection (default 2m)
	Detectors []detect.Detector
	// Progress, if set, is called as each cluster finishes (for stderr progress lines).
	Progress func(done, total int, r ClusterResult)
}

type ClusterResult struct {
	Cluster  string         `json:"cluster"`
	Error    string         `json:"error,omitempty"`
	Duration string         `json:"duration"`
	Report   *detect.Report `json:"report,omitempty"`
}

// Pattern is one failure seen in several clusters or namespaces.
type Pattern struct {
	Key         string      `json:"key"`
	Detector    string      `json:"detector"`
	Title       string      `json:"title"`
	Tier        detect.Tier `json:"tier"` // worst tier across occurrences
	Occurrences int         `json:"occurrences"`
	Clusters    []string    `json:"clusters"`
	Namespaces  []string    `json:"namespaces,omitempty"`
	Since       *time.Time  `json:"since,omitempty"` // earliest evidence anywhere
	FindingIDs  []string    `json:"finding_ids"`
}

type Result struct {
	SchemaVersion string              `json:"schema_version"`
	StartedAt     time.Time           `json:"started_at"`
	Duration      string              `json:"duration"`
	Clusters      []ClusterResult     `json:"clusters"`
	Failed        int                 `json:"failed"`
	Counts        map[detect.Tier]int `json:"counts"`
	Patterns      []Pattern           `json:"patterns"`
}

const SchemaVersion = "1"

// Run sweeps clusters with bounded parallelism. One slow or broken cluster
// never blocks or fails the sweep: it is reported with its error.
func Run(ctx context.Context, clusters []string, collect CollectFunc, opt Options) Result {
	if opt.Parallel <= 0 {
		opt.Parallel = 8
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 2 * time.Minute
	}
	if opt.Detectors == nil {
		opt.Detectors = detect.Registry()
	}
	start := time.Now()
	res := Result{SchemaVersion: SchemaVersion, StartedAt: start.UTC(), Counts: map[detect.Tier]int{}}
	res.Clusters = make([]ClusterResult, len(clusters))

	sem := make(chan struct{}, opt.Parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for i, c := range clusters {
		wg.Add(1)
		go func(i int, c string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				res.Clusters[i] = ClusterResult{Cluster: c, Error: "cancelled before start: " + ctx.Err().Error(), Duration: "0s"}
				return
			}
			defer func() { <-sem }()
			r := runOne(ctx, c, collect, opt)
			res.Clusters[i] = r
			if opt.Progress != nil {
				mu.Lock()
				done++
				opt.Progress(done, len(clusters), r)
				mu.Unlock()
			}
		}(i, c)
	}
	wg.Wait()

	for _, cr := range res.Clusters {
		if cr.Error != "" {
			res.Failed++
		}
		if cr.Report != nil {
			for t, n := range cr.Report.Counts {
				res.Counts[t] += n
			}
		}
	}
	res.Patterns = Patterns(res.Clusters)
	res.Duration = time.Since(start).Round(time.Second).String()
	return res
}

func runOne(ctx context.Context, cluster string, collect CollectFunc, opt Options) (out ClusterResult) {
	start := time.Now()
	out.Cluster = cluster
	defer func() {
		if p := recover(); p != nil {
			out.Error = fmt.Sprintf("panic: %v", p)
			out.Report = nil
		}
		out.Duration = time.Since(start).Round(100 * time.Millisecond).String()
	}()
	cctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()

	type got struct {
		s   *model.Snapshot
		err error
	}
	ch := make(chan got, 1)
	go func() {
		// recover here too: a panic in this goroutine is not caught by runOne's recover
		defer func() {
			if p := recover(); p != nil {
				ch <- got{nil, fmt.Errorf("collector panic: %v", p)}
			}
		}()
		s, err := collect(cctx, cluster)
		ch <- got{s, err}
	}()
	select {
	case g := <-ch:
		if g.err != nil {
			out.Error = g.err.Error()
			return
		}
		if g.s.Cluster == "" {
			g.s.Cluster = cluster
		}
		r := detect.Run(g.s, opt.Detectors)
		out.Report = &r
	case <-cctx.Done():
		out.Error = fmt.Sprintf("timed out after %s", opt.Timeout)
	}
	return
}

// Patterns groups findings by PatternKey across all clusters and keeps those
// that occur more than once. Sorted by worst tier, then breadth.
func Patterns(results []ClusterResult) []Pattern {
	byKey := map[string]*Pattern{}
	nsSeen := map[string]map[string]bool{}
	clSeen := map[string]map[string]bool{}
	for _, cr := range results {
		if cr.Report == nil {
			continue
		}
		for _, f := range cr.Report.Findings {
			if f.PatternKey == "" {
				continue
			}
			p, ok := byKey[f.PatternKey]
			if !ok {
				p = &Pattern{Key: f.PatternKey, Detector: f.Detector, Title: PatternTitle(f), Tier: f.Tier}
				byKey[f.PatternKey] = p
				nsSeen[f.PatternKey] = map[string]bool{}
				clSeen[f.PatternKey] = map[string]bool{}
			}
			p.Occurrences++
			p.FindingIDs = append(p.FindingIDs, f.ID)
			if f.Tier.Rank() < p.Tier.Rank() {
				p.Tier = f.Tier
			}
			if f.Since != nil && (p.Since == nil || f.Since.Before(*p.Since)) {
				t := *f.Since
				p.Since = &t
			}
			if !clSeen[f.PatternKey][cr.Cluster] {
				clSeen[f.PatternKey][cr.Cluster] = true
				p.Clusters = append(p.Clusters, cr.Cluster)
			}
			if ns := f.Subject.Namespace; ns != "" && !nsSeen[f.PatternKey][ns] {
				nsSeen[f.PatternKey][ns] = true
				p.Namespaces = append(p.Namespaces, ns)
			}
		}
	}
	var out []Pattern
	for _, p := range byKey {
		if p.Occurrences < 2 {
			continue
		}
		sort.Strings(p.Clusters)
		sort.Strings(p.Namespaces)
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Tier.Rank() != b.Tier.Rank() {
			return a.Tier.Rank() < b.Tier.Rank()
		}
		if len(a.Clusters) != len(b.Clusters) {
			return len(a.Clusters) > len(b.Clusters)
		}
		if a.Occurrences != b.Occurrences {
			return a.Occurrences > b.Occurrences
		}
		return a.Key < b.Key
	})
	return out
}

// PatternTitle describes a finding without its cluster/namespace specifics.
func PatternTitle(f detect.Finding) string {
	parts := strings.Split(f.PatternKey, "|")
	switch f.Detector {
	case "missing-reference":
		if len(parts) == 3 {
			return fmt.Sprintf("%s %q missing where pods reference it", parts[1], parts[2])
		}
	case "workload-unavailable":
		if len(parts) == 4 {
			return fmt.Sprintf("%s %s unavailable (%s)", parts[1], parts[2], parts[3])
		}
	case "node-unhealthy":
		if len(parts) == 2 {
			return "Nodes " + parts[1]
		}
	case "resource-requests":
		if len(parts) == 4 {
			issue := map[string]string{
				"memory-none": "no memory request", "memory-under": "memory far above its request",
				"cpu-none": "no CPU request", "cpu-under": "CPU far above its request",
			}[parts[1]]
			if issue == "" {
				issue = parts[1]
			}
			return fmt.Sprintf("%s %s: %s", parts[2], parts[3], issue)
		}
	case "node-saturation":
		if len(parts) == 2 {
			return fmt.Sprintf("Nodes with %s above 85%% of allocatable", parts[1])
		}
	case "cpu-hotspot":
		if len(parts) == 3 {
			return fmt.Sprintf("%s %s: one replica saturates its node's CPU", parts[1], parts[2])
		}
	}
	return f.Title
}
