// Package render turns detection reports and fleet results into terminal text.
// Plain stdlib with optional ANSI color, so output is testable and works on
// any jump server terminal.
package render

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/detect"
	"github.com/Codebvoy15/k8s-doctor/internal/fleet"
)

type Options struct {
	MinTier  detect.Tier // hide findings below this tier (default LATENT: hide INFO)
	Color    bool
	Verbose  bool // show remediation plans and full affected lists
	MaxShown int  // findings/patterns to show (default 25)
}

func (o Options) withDefaults() Options {
	if o.MinTier == "" {
		o.MinTier = detect.TierLatent
	}
	if o.MaxShown <= 0 {
		o.MaxShown = 25
	}
	return o
}

const (
	red    = "\x1b[31m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
	grey   = "\x1b[90m"
	bold   = "\x1b[1m"
	reset  = "\x1b[0m"
)

type pen struct{ on bool }

func (p pen) c(code, s string) string {
	if !p.on {
		return s
	}
	return code + s + reset
}

func (p pen) tier(t detect.Tier) string {
	label := fmt.Sprintf("%-9s", string(t))
	switch t {
	case detect.TierImpacting:
		return p.c(red+bold, label)
	case detect.TierDegraded:
		return p.c(yellow, label)
	case detect.TierLatent:
		return p.c(cyan, label)
	}
	return p.c(grey, label)
}

func visible(t, min detect.Tier) bool { return t.Rank() <= min.Rank() }

// Report prints one cluster's findings: root causes first, the symptoms they
// explain nested beneath them, and same-pattern findings collapsed into one line.
func Report(w io.Writer, r detect.Report, o Options) {
	o = o.withDefaults()
	p := pen{o.Color}
	ns := r.Namespace
	if ns == "" {
		ns = "all"
	}
	fmt.Fprintf(w, "%s  cluster=%s  ns=%s  captured=%s\n", p.c(bold, "k8s-doctor scan"), r.Cluster, ns, r.CapturedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "%s\n", counts(p, r.Counts))
	for _, e := range r.Errors {
		fmt.Fprintf(w, "%s could not list %s: %s (findings needing it are skipped)\n", p.c(yellow, "coverage"), e.Kind, e.Message)
	}

	byID := map[string]detect.Finding{}
	for _, f := range r.Findings {
		byID[f.ID] = f
	}
	// collapse same-pattern findings (e.g. one operator secret missing in 12 namespaces)
	type group struct{ fs []detect.Finding }
	var order []string
	groups := map[string]*group{}
	for _, f := range r.Findings {
		if len(f.CausedBy) > 0 || !visible(f.Tier, o.MinTier) {
			continue // symptoms are printed under their cause
		}
		k := f.PatternKey
		if k == "" || f.Detector == "workload-unavailable" {
			k = f.ID
		}
		if groups[k] == nil {
			groups[k] = &group{}
			order = append(order, k)
		}
		groups[k].fs = append(groups[k].fs, f)
	}
	if len(order) == 0 {
		fmt.Fprintf(w, "\n  no findings at or above %s\n", o.MinTier)
		return
	}
	for i, k := range order {
		if i == o.MaxShown {
			fmt.Fprintf(w, "\n  ... %d more (use -o json, or raise --max)\n", len(order)-o.MaxShown)
			break
		}
		g := groups[k].fs
		f := g[0]
		fmt.Fprintln(w)
		if len(g) == 1 {
			fmt.Fprintf(w, "%s %s\n", p.tier(f.Tier), p.c(bold, f.Title))
		} else {
			var where []string
			for _, x := range g {
				where = append(where, x.Subject.Namespace)
			}
			sort.Strings(where)
			fmt.Fprintf(w, "%s %s\n", p.tier(worst(g)), p.c(bold, fmt.Sprintf("%s  (x%d namespaces)", fleet.PatternTitle(f), len(g))))
			fmt.Fprintf(w, "          in: %s\n", list(where, 12))
		}
		fmt.Fprintf(w, "          %s\n", f.Summary)
		for _, e := range f.Evidence {
			fmt.Fprintf(w, "          %s %s\n", p.c(grey, "·"), e)
		}
		for _, id := range explainsAll(g) {
			if s, ok := byID[id]; ok {
				fmt.Fprintf(w, "          %s %s %s\n", p.c(red, "└─ causes"), p.tier(s.Tier), s.Title)
				if o.Verbose {
					for _, e := range s.Evidence {
						fmt.Fprintf(w, "                    %s %s\n", p.c(grey, "·"), e)
					}
				}
			}
		}
		if o.Verbose && f.Remediation != nil {
			fmt.Fprintf(w, "          %s\n", p.c(cyan, "next:"))
			for _, s := range f.Remediation.Steps {
				tag := "  "
				if s.Mutating {
					tag = p.c(yellow, "✎ ") // human decision; k8s-doctor never runs it
				}
				fmt.Fprintf(w, "            %s%s\n", tag, s.Description)
				if s.Command != "" {
					fmt.Fprintf(w, "                %s\n", p.c(cyan, s.Command))
				}
			}
			for _, v := range f.Remediation.Verify {
				fmt.Fprintf(w, "            verify: %s\n", p.c(cyan, v))
			}
		}
	}
	if !o.Verbose {
		fmt.Fprintf(w, "\n%s\n", p.c(grey, "-v shows next steps; ✎ marks steps for a human to run (k8s-doctor never runs them)"))
	}
}

// Fleet prints the per-cluster table and the fleet-wide patterns.
func Fleet(w io.Writer, res fleet.Result, o Options) {
	o = o.withDefaults()
	p := pen{o.Color}
	fmt.Fprintf(w, "%s  clusters=%d  failed=%d  took=%s\n", p.c(bold, "k8s-doctor fleet"), len(res.Clusters), res.Failed, res.Duration)
	fmt.Fprintf(w, "%s\n\n", counts(p, res.Counts))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CLUSTER\tIMPACTING\tDEGRADED\tLATENT\tTOOK\tSTATUS")
	rows := append([]fleet.ClusterResult(nil), res.Clusters...)
	sort.SliceStable(rows, func(i, j int) bool { return score(rows[i]) > score(rows[j]) })
	for _, cr := range rows {
		status := "ok"
		var imp, deg, lat int
		if cr.Report != nil {
			imp, deg, lat = cr.Report.Counts[detect.TierImpacting], cr.Report.Counts[detect.TierDegraded], cr.Report.Counts[detect.TierLatent]
			if len(cr.Report.Errors) > 0 {
				status = fmt.Sprintf("partial (%d kinds not listed)", len(cr.Report.Errors))
			}
		}
		if cr.Error != "" {
			status = "ERROR " + truncate(cr.Error, 70)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", cr.Cluster, num(p, imp, red), num(p, deg, yellow), num(p, lat, cyan), cr.Duration, status)
	}
	tw.Flush()

	var shown []fleet.Pattern
	for _, pt := range res.Patterns {
		if visible(pt.Tier, o.MinTier) {
			shown = append(shown, pt)
		}
	}
	if len(shown) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s %s\n", p.c(bold, "FLEET PATTERNS"), p.c(grey, "(same failure in several places: one cause, one fix)"))
	for i, pt := range shown {
		if i == o.MaxShown {
			fmt.Fprintf(w, "  ... %d more (-o json)\n", len(shown)-o.MaxShown)
			break
		}
		age := ""
		if pt.Since != nil {
			age = fmt.Sprintf(", oldest %s ago", humanAgo(res.StartedAt.Sub(*pt.Since)))
		}
		fmt.Fprintf(w, "\n%s %s\n", p.tier(pt.Tier), p.c(bold, pt.Title))
		fmt.Fprintf(w, "          %d occurrence(s) in %d cluster(s)%s\n", pt.Occurrences, len(pt.Clusters), age)
		fmt.Fprintf(w, "          clusters: %s\n", list(pt.Clusters, 8))
		if len(pt.Namespaces) > 0 {
			fmt.Fprintf(w, "          namespaces: %s\n", list(pt.Namespaces, 10))
		}
	}
}

func counts(p pen, c map[detect.Tier]int) string {
	return fmt.Sprintf("%s=%d  %s=%d  %s=%d  %s=%d",
		p.c(red, "impacting"), c[detect.TierImpacting], p.c(yellow, "degraded"), c[detect.TierDegraded],
		p.c(cyan, "latent"), c[detect.TierLatent], p.c(grey, "info"), c[detect.TierInfo])
}

func num(p pen, n int, code string) string {
	if n == 0 {
		return "-"
	}
	return p.c(code, fmt.Sprint(n))
}

func score(cr fleet.ClusterResult) int {
	if cr.Error != "" {
		return 1 << 20
	}
	if cr.Report == nil {
		return 0
	}
	return cr.Report.Counts[detect.TierImpacting]*10000 + cr.Report.Counts[detect.TierDegraded]*100 + cr.Report.Counts[detect.TierLatent]
}

func worst(fs []detect.Finding) detect.Tier {
	t := fs[0].Tier
	for _, f := range fs {
		if f.Tier.Rank() < t.Rank() {
			t = f.Tier
		}
	}
	return t
}

func explainsAll(fs []detect.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Explains...)
	}
	return out
}

func list(xs []string, max int) string {
	if len(xs) <= max {
		return strings.Join(xs, ", ")
	}
	return strings.Join(xs[:max], ", ") + fmt.Sprintf(", +%d more", len(xs)-max)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
