// Package detect is k8s-doctor's detection engine.
//
// Detectors are pure functions over a model.Snapshot: they never call the
// Kubernetes API, never mutate anything, and are deterministic for a given
// snapshot (they use Snapshot.CapturedAt as "now"). That makes every detector
// unit-testable against golden snapshots captured from real incidents.
package detect

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// Tier is impact, not severity: is something hurting right now?
type Tier string

const (
	TierImpacting Tier = "IMPACTING" // down right now
	TierDegraded  Tier = "DEGRADED"  // partially down, or flapping
	TierLatent    Tier = "LATENT"    // not hurting yet; breaks on the next restart/reschedule/upgrade
	TierInfo      Tier = "INFO"      // worth knowing, no action implied
)

func (t Tier) Rank() int {
	switch t {
	case TierImpacting:
		return 0
	case TierDegraded:
		return 1
	case TierLatent:
		return 2
	}
	return 3
}

// ParseTier accepts impacting|degraded|latent|info (any case).
func ParseTier(s string) (Tier, error) {
	switch Tier(strings.ToUpper(s)) {
	case TierImpacting, TierDegraded, TierLatent, TierInfo:
		return Tier(strings.ToUpper(s)), nil
	}
	return "", fmt.Errorf("unknown tier %q (impacting|degraded|latent|info)", s)
}

type ObjectRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

func (o ObjectRef) String() string {
	if o.Namespace == "" {
		return o.Kind + "/" + o.Name
	}
	return o.Kind + " " + o.Namespace + "/" + o.Name
}

// Step is one remediation action. Mutating steps are for a human to run:
// k8s-doctor never executes them.
type Step struct {
	Description string `json:"description"`
	Command     string `json:"command,omitempty"`
	Mutating    bool   `json:"mutating,omitempty"`
}

type Plan struct {
	Steps  []Step   `json:"steps"`
	Verify []string `json:"verify,omitempty"`
}

type Finding struct {
	ID          string      `json:"id"` // stable fingerprint: same problem, same ID, run after run
	Detector    string      `json:"detector"`
	Cluster     string      `json:"cluster"`
	Tier        Tier        `json:"tier"`
	Title       string      `json:"title"`
	Summary     string      `json:"summary"`
	Subject     ObjectRef   `json:"subject"` // the object the finding is about (the cause, when known)
	Affected    []ObjectRef `json:"affected,omitempty"`
	Evidence    []string    `json:"evidence"`
	Since       *time.Time  `json:"since,omitempty"` // earliest evidence of the problem
	Remediation *Plan       `json:"remediation,omitempty"`
	// PatternKey is cluster- and namespace-agnostic, so the same failure in many
	// places (e.g. one operator secret missing in 40 namespaces across 12
	// clusters) aggregates into one fleet pattern.
	PatternKey string   `json:"pattern_key"`
	CausedBy   []string `json:"caused_by,omitempty"` // IDs of findings that explain this one
	Explains   []string `json:"explains,omitempty"`  // IDs of findings this one explains

	// blocks lists the pods this finding provably takes down. Only these are
	// used for cause->symptom linking, so correlation never guesses.
	blocks []ObjectRef

	// contrib lists "<resource>:<ns>/<pod>" keys for pods whose usage beyond
	// their request is measured. A node saturation finding is linked to the
	// workload findings that share these keys: proof by arithmetic, not guessing.
	contrib []string
}

// Fingerprint builds a stable, short ID from the parts that identify a problem.
func Fingerprint(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(h[:])[:12]
}

// Detector is implemented by every check. Detect must be pure and deterministic.
type Detector interface {
	ID() string
	Description() string
	Detect(ix *model.Index) []Finding
}

// Registry returns all built-in detectors in a stable order.
func Registry() []Detector {
	return []Detector{
		MissingReference{},
		WorkloadUnavailable{},
		NodeUnhealthy{},
		FailedPods{},
		ResourceRequests{},
		NodeSaturation{},
		CPUHotspot{},
	}
}

// Select filters the registry by detector ID. An empty list selects all.
func Select(ids []string) ([]Detector, error) {
	all := Registry()
	if len(ids) == 0 {
		return all, nil
	}
	byID := map[string]Detector{}
	for _, d := range all {
		byID[d.ID()] = d
	}
	var out []Detector
	for _, id := range ids {
		d, ok := byID[strings.TrimSpace(id)]
		if !ok {
			return nil, fmt.Errorf("unknown detector %q", id)
		}
		out = append(out, d)
	}
	return out, nil
}

// Report is the result of running detectors over one snapshot.
type Report struct {
	SchemaVersion string               `json:"schema_version"`
	Cluster       string               `json:"cluster"`
	Namespace     string               `json:"namespace,omitempty"`
	CapturedAt    time.Time            `json:"captured_at"`
	Detectors     []string             `json:"detectors"`
	Coverage      map[string]bool      `json:"coverage"`
	Errors        []model.CollectError `json:"errors,omitempty"`
	Counts        map[Tier]int         `json:"counts"`
	Findings      []Finding            `json:"findings"`
}

// ReportSchemaVersion changes when the Report JSON shape changes incompatibly.
const ReportSchemaVersion = "1"

// Run executes detectors, links causes to symptoms, and orders findings so the
// most impactful root causes come first.
func Run(s *model.Snapshot, dets []Detector) Report {
	ix := model.NewIndex(s)
	r := Report{
		SchemaVersion: ReportSchemaVersion,
		Cluster:       s.Cluster, Namespace: s.Namespace, CapturedAt: s.CapturedAt,
		Coverage: s.Collected, Errors: s.Errors,
		Counts: map[Tier]int{}, Findings: []Finding{},
	}
	for _, d := range dets {
		r.Detectors = append(r.Detectors, d.ID())
		for _, f := range d.Detect(ix) {
			f.Detector = d.ID()
			f.Cluster = s.Cluster
			if f.Evidence == nil {
				f.Evidence = []string{}
			}
			r.Findings = append(r.Findings, f)
		}
	}
	correlate(r.Findings)
	sortFindings(r.Findings)
	for _, f := range r.Findings {
		r.Counts[f.Tier]++
	}
	return r
}

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Tier.Rank() != b.Tier.Rank() {
			return a.Tier.Rank() < b.Tier.Rank()
		}
		// root causes before the symptoms they explain
		if (len(a.CausedBy) == 0) != (len(b.CausedBy) == 0) {
			return len(a.CausedBy) == 0
		}
		if len(a.Explains) != len(b.Explains) {
			return len(a.Explains) > len(b.Explains)
		}
		if len(a.Affected) != len(b.Affected) {
			return len(a.Affected) > len(b.Affected)
		}
		return a.ID < b.ID
	})
}

// --- small shared helpers ---

func podRef(p model.Pod) ObjectRef {
	return ObjectRef{Kind: model.KindPod, Namespace: p.Namespace, Name: p.Name}
}

func earliest(a *time.Time, b time.Time) *time.Time {
	if b.IsZero() {
		return a
	}
	if a == nil || b.Before(*a) {
		t := b
		return &t
	}
	return a
}

func since(now time.Time, t *time.Time) string {
	if t == nil {
		return ""
	}
	return humanDuration(now.Sub(*t))
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
