package render

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/detect"
	"github.com/Codebvoy15/k8s-doctor/internal/fleet"
)

// view sorts a report into the sections the summary and markdown outputs
// share: findings about availability/config, nodes under pressure, workloads
// to resize (grouped by namespace, i.e. by owning team), and CPU hotspots.
type view struct {
	r         detect.Report
	byID      map[string]detect.Finding
	other     []otherRow
	nodes     []detect.Finding
	workloads []nsGroup
	hotspots  []detect.Finding
	hidden    int
	nWork     int
}

type otherRow struct {
	f        detect.Finding
	title    string // collapsed pattern title, or the finding title
	tier     detect.Tier
	symptoms []detect.Finding
}

type nsGroup struct {
	ns    string
	items []detect.Finding
}

func newView(r detect.Report, min detect.Tier) view {
	v := view{r: r, byID: map[string]detect.Finding{}}
	for _, f := range r.Findings {
		v.byID[f.ID] = f
	}
	groups := map[string]*otherRow{}
	var order []string
	byNS := map[string][]detect.Finding{}
	for _, f := range r.Findings {
		if !visible(f.Tier, min) {
			v.hidden++
			continue
		}
		switch f.Detector {
		case "node-saturation":
			v.nodes = append(v.nodes, f)
		case "resource-requests":
			byNS[f.Subject.Namespace] = append(byNS[f.Subject.Namespace], f)
			v.nWork++
		case "cpu-hotspot":
			v.hotspots = append(v.hotspots, f)
		default:
			if len(f.CausedBy) > 0 {
				continue // printed under its cause
			}
			k := f.PatternKey
			if k == "" || f.Detector == "workload-unavailable" {
				k = f.ID
			}
			g := groups[k]
			if g == nil {
				g = &otherRow{f: f, title: f.Title, tier: f.Tier}
				groups[k] = g
				order = append(order, k)
			} else {
				g.title = "" // set below once the group size is known
				if f.Tier.Rank() < g.tier.Rank() {
					g.tier = f.Tier
				}
			}
			for _, id := range f.Explains {
				if s, ok := v.byID[id]; ok && s.Detector != "node-saturation" {
					g.symptoms = append(g.symptoms, s)
				}
			}
		}
	}
	for _, k := range order {
		g := groups[k]
		if g.title == "" {
			n := 0
			for _, f := range r.Findings {
				if f.PatternKey == g.f.PatternKey && len(f.CausedBy) == 0 && visible(f.Tier, min) {
					n++
				}
			}
			g.title = fmt.Sprintf("%s  (x%d namespaces)", fleet.PatternTitle(g.f), n)
		}
		v.other = append(v.other, *g)
	}

	// most impact first: workloads behind hot nodes, then by size of the gap
	score := func(f detect.Finding) (int, int64) {
		drives := 0
		for _, id := range f.Explains {
			if v.byID[id].Detector == "node-saturation" {
				drives++
			}
		}
		var gap int64
		for _, m := range f.Measures {
			if m.Resource == "memory" {
				gap += m.Used - m.Requested
			} else {
				gap += (m.Used - m.Requested) << 20 // a core of gap ranks like a GiB
			}
		}
		return drives, gap
	}
	less := func(a, b detect.Finding) bool {
		da, ga := score(a)
		db, gb := score(b)
		if da != db {
			return da > db
		}
		if ga != gb {
			return ga > gb
		}
		return a.Subject.Name < b.Subject.Name
	}
	for ns, fs := range byNS {
		sort.SliceStable(fs, func(i, j int) bool { return less(fs[i], fs[j]) })
		v.workloads = append(v.workloads, nsGroup{ns: ns, items: fs})
	}
	sort.SliceStable(v.workloads, func(i, j int) bool {
		a, b := v.workloads[i], v.workloads[j]
		if less(a.items[0], b.items[0]) != less(b.items[0], a.items[0]) {
			return less(a.items[0], b.items[0])
		}
		return a.ns < b.ns
	})
	sort.SliceStable(v.nodes, func(i, j int) bool {
		a, b := v.nodes[i].Measures, v.nodes[j].Measures
		if len(a) > 0 && len(b) > 0 && a[0].Capacity > 0 && b[0].Capacity > 0 {
			pa, pb := a[0].Used*1000/a[0].Capacity, b[0].Used*1000/b[0].Capacity
			if pa != pb {
				return pa > pb
			}
		}
		return v.nodes[i].Subject.Name < v.nodes[j].Subject.Name
	})
	return v
}

// verdict is the one line that says how bad it is.
func (v view) verdict() string {
	var parts []string
	nodes := map[string]int{}
	for _, f := range v.nodes {
		if len(f.Measures) > 0 {
			nodes[f.Measures[0].Resource]++
		}
	}
	for _, res := range []string{"memory", "cpu"} {
		if n := nodes[res]; n > 0 {
			label := res
			if res == "cpu" {
				label = "CPU"
			}
			parts = append(parts, fmt.Sprintf("%d %s ≥85%% %s", n, plural(n, "node", "nodes"), label))
		}
	}
	if v.nWork > 0 {
		parts = append(parts, fmt.Sprintf("%d %s to resize", v.nWork, plural(v.nWork, "workload", "workloads")))
	}
	if n := len(v.hotspots); n > 0 {
		parts = append(parts, fmt.Sprintf("%d CPU %s", n, plural(n, "hotspot", "hotspots")))
	}
	tiers := map[detect.Tier]int{}
	for _, o := range v.other {
		tiers[o.tier]++
	}
	for _, t := range []detect.Tier{detect.TierImpacting, detect.TierDegraded, detect.TierLatent, detect.TierInfo} {
		if n := tiers[t]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, strings.ToLower(string(t))))
		}
	}
	return strings.Join(parts, "  ·  ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// causedBy lists the workloads behind a node finding.
func (v view) causedBy(f detect.Finding) []string {
	var names []string
	seen := map[string]bool{}
	for _, id := range f.CausedBy {
		c, ok := v.byID[id]
		if !ok || seen[c.Subject.Name] {
			continue
		}
		seen[c.Subject.Name] = true
		names = append(names, c.Subject.Name)
	}
	return names
}

// drives lists the (short) nodes a workload finding is behind.
func (v view) drives(f detect.Finding) []string {
	var out []string
	for _, id := range f.Explains {
		if s, ok := v.byID[id]; ok && s.Detector == "node-saturation" {
			out = append(out, shortNode(s.Subject.Name))
		}
	}
	return out
}

func measureCells(f detect.Finding) (uses, requests string) {
	var u, r []string
	for _, m := range f.Measures {
		u = append(u, detect.FormatQuantity(m.Resource, m.Used))
		r = append(r, detect.FormatQuantity(m.Resource, m.Requested))
	}
	return strings.Join(u, " + "), strings.Join(r, " + ")
}

func withPct(resource string, v, total int64) string {
	q := detect.FormatQuantity(resource, v)
	if v < 0 || total <= 0 {
		return q
	}
	return fmt.Sprintf("%s (%d%%)", q, v*100/total)
}

// shortNode: ip-10-9-31-141.ec2.internal -> ip-10-9-31-141 (tables only).
func shortNode(name string) string {
	if i := strings.IndexByte(name, '.'); i > 0 && strings.HasPrefix(name, "ip-") {
		return name[:i]
	}
	return name
}

// Summary is the default scan output: one screen, answer first, grouped by who
// has to act, details on demand (--explain, -v, -o markdown).
func Summary(w io.Writer, r detect.Report, o Options) {
	o = o.withDefaults()
	p := pen{o.Color}
	v := newView(r, o.MinTier)
	ns := r.Namespace
	if ns == "" {
		ns = "all"
	}
	fmt.Fprintf(w, "%s  %s  ns=%s  %s\n", p.c(bold, "k8s-doctor scan"), p.c(bold, ShortCluster(r.Cluster)), ns, p.c(grey, r.CapturedAt.UTC().Format("2006-01-02 15:04 UTC")))
	for _, e := range r.Errors {
		fmt.Fprintf(w, "%s could not list %s (checks that need it were skipped)\n", p.c(yellow, "coverage:"), e.Kind)
	}
	if verdict := v.verdict(); verdict != "" {
		fmt.Fprintf(w, "%s\n", verdict)
	} else {
		fmt.Fprintf(w, "no findings at or above %s\n", o.MinTier)
	}

	if len(v.other) > 0 {
		fmt.Fprintf(w, "\n%s\n", p.c(bold, "FINDINGS"))
		t := table{indent: "  "}
		for i, row := range v.other {
			if i == o.MaxShown {
				t.add("", p.c(grey, fmt.Sprintf("... %d more (-o json)", len(v.other)-o.MaxShown)))
				break
			}
			t.add(p.tier(row.tier), clip(row.title, 120))
			for _, s := range row.symptoms {
				t.add("", p.c(grey, "└ causes ")+clip(s.Title, 110))
			}
		}
		t.write(w, p)
	}

	if len(v.nodes) > 0 {
		fmt.Fprintf(w, "\n%s\n", p.c(bold, "NODES UNDER PRESSURE"))
		t := table{indent: "  ", header: true}
		t.add("NODE", "RESOURCE", "USED", "REQUESTED", "CAUSED BY")
		for _, f := range v.nodes {
			m := f.Measures[0]
			caused := joinClip(v.causedBy(f), 70)
			if caused == "" {
				caused = p.c(grey, "no single workload (see --explain)")
			}
			t.add(shortNode(f.Subject.Name), m.Resource, withPct(m.Resource, m.Used, m.Capacity), withPct(m.Resource, m.Requested, m.Capacity), caused)
		}
		t.write(w, p)
	}

	if len(v.workloads) > 0 {
		fmt.Fprintf(w, "\n%s\n", p.c(bold, "WORKLOADS USING MORE THAN THEY REQUEST"))
		t := table{indent: "  ", header: true}
		t.add("NAMESPACE", "WORKLOAD", "USES", "REQUESTS", "FIX IN", "NOTE")
		shown := 0
		for _, g := range v.workloads {
			for i, f := range g.items {
				if shown == o.MaxShown {
					break
				}
				shown++
				nsCell := ""
				if i == 0 {
					nsCell = clip(g.ns, 32)
				}
				uses, reqs := measureCells(f)
				var notes []string
				if d := v.drives(f); len(d) > 0 {
					notes = append(notes, p.c(red, "drives "+strings.Join(d, ", ")))
				}
				if f.Tier == detect.TierInfo {
					notes = append(notes, "minor")
				}
				if f.Note != "" {
					notes = append(notes, f.Note)
				}
				t.add(nsCell, clip(f.Subject.Name, 40), uses, reqs, clip(f.FixIn, 40), strings.Join(notes, " · "))
			}
		}
		t.write(w, p)
		if v.nWork > shown {
			fmt.Fprintf(w, "  %s\n", p.c(grey, fmt.Sprintf("... %d more (raise --max, or -o markdown)", v.nWork-shown)))
		}
	}

	if len(v.hotspots) > 0 {
		fmt.Fprintf(w, "\n%s\n", p.c(bold, "CPU HOTSPOTS"))
		t := table{indent: "  ", header: true}
		t.add("NAMESPACE", "WORKLOAD", "BUSIEST REPLICA", "OF ITS NODE", "REQUEST", "SCALING")
		for _, f := range v.hotspots {
			m := f.Measures[0]
			share := "?"
			if m.Capacity > 0 {
				share = fmt.Sprintf("%d%%", m.Used*100/m.Capacity)
			}
			t.add(clip(f.Subject.Namespace, 32), clip(f.Subject.Name, 40), detect.FormatQuantity("cpu", m.Used), share, detect.FormatQuantity("cpu", m.Requested), f.Note)
		}
		t.write(w, p)
	}

	// next steps: details on demand
	fmt.Fprintln(w)
	ctx := ""
	if o.Context != "" {
		ctx = " --context " + o.Context
	}
	example := v.example()
	if v.hidden > 0 {
		fmt.Fprintf(w, "%s\n", p.c(grey, fmt.Sprintf("%d lower-tier %s hidden: --min-tier info", v.hidden, plural(v.hidden, "finding", "findings"))))
	}
	if example != "" {
		fmt.Fprintf(w, "%s  %s\n", p.c(grey, "details:"), p.c(cyan, fmt.Sprintf("k8s-doctor scan%s --explain %s", ctx, example)))
	}
	fmt.Fprintf(w, "%s  %s\n", p.c(grey, "report: "), p.c(cyan, fmt.Sprintf("k8s-doctor scan%s -o markdown > report.md", ctx)))
}

// example picks a real name to show in the --explain hint.
func (v view) example() string {
	switch {
	case len(v.workloads) > 0:
		return v.workloads[0].items[0].Subject.Name
	case len(v.hotspots) > 0:
		return v.hotspots[0].Subject.Name
	case len(v.other) > 0:
		return v.other[0].f.Subject.Name
	case len(v.nodes) > 0:
		return shortNode(v.nodes[0].Subject.Name)
	}
	return ""
}

// Markdown is a report to hand to app teams: the same sections as Summary, a
// section per namespace, and the exact change for each workload.
func Markdown(w io.Writer, r detect.Report, o Options) {
	o = o.withDefaults()
	v := newView(r, o.MinTier)
	ns := r.Namespace
	if ns == "" {
		ns = "all"
	}
	fmt.Fprintf(w, "# k8s-doctor report: %s\n\n", md(ShortCluster(r.Cluster)))
	fmt.Fprintf(w, "Captured %s · namespaces: %s · read-only scan.\n", r.CapturedAt.UTC().Format(time.RFC1123), md(ns))
	fmt.Fprintf(w, "Usage figures are one metrics-server sample: confirm sizing against p95/peak history before applying.\n\n")
	if verdict := v.verdict(); verdict != "" {
		fmt.Fprintf(w, "**Summary:** %s\n", verdict)
	} else {
		fmt.Fprintf(w, "**Summary:** no findings at or above %s.\n", o.MinTier)
	}
	for _, e := range r.Errors {
		fmt.Fprintf(w, "\n> Coverage: could not list %s; checks that need it were skipped.\n", md(e.Kind))
	}

	if len(v.other) > 0 {
		fmt.Fprintf(w, "\n## Findings\n\n| Tier | Finding |\n|---|---|\n")
		for _, row := range v.other {
			fmt.Fprintf(w, "| %s | %s |\n", row.tier, md(row.title))
			for _, s := range row.symptoms {
				fmt.Fprintf(w, "| | └ causes %s |\n", md(s.Title))
			}
		}
	}

	if len(v.nodes) > 0 {
		fmt.Fprintf(w, "\n## Nodes under pressure\n\n| Node | Resource | Used | Requested | Caused by |\n|---|---|---|---|---|\n")
		for _, f := range v.nodes {
			m := f.Measures[0]
			caused := strings.Join(v.causedBy(f), ", ")
			if caused == "" {
				caused = "no single workload"
			}
			fmt.Fprintf(w, "| `%s` | %s | %s | %s | %s |\n", f.Subject.Name, m.Resource, withPct(m.Resource, m.Used, m.Capacity), withPct(m.Resource, m.Requested, m.Capacity), md(caused))
		}
	}

	if len(v.workloads) > 0 {
		fmt.Fprintf(w, "\n## Workloads to resize, by namespace\n")
		for _, g := range v.workloads {
			fmt.Fprintf(w, "\n### `%s`\n\n| Workload | Uses | Requests | Fix in | Proposed | Note |\n|---|---|---|---|---|---|\n", g.ns)
			for _, f := range g.items {
				uses, reqs := measureCells(f)
				var notes []string
				if d := v.drives(f); len(d) > 0 {
					notes = append(notes, "drives "+strings.Join(d, ", "))
				}
				if f.Tier == detect.TierInfo {
					notes = append(notes, "minor")
				}
				if f.Note != "" {
					notes = append(notes, f.Note)
				}
				fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s |\n", md(f.Subject.Name), uses, reqs, md(f.FixIn), md(f.Proposal), md(strings.Join(notes, " · ")))
			}
			fmt.Fprintf(w, "\nHow to apply:\n\n")
			for _, f := range g.items {
				fmt.Fprintf(w, "- **%s**: %s\n", md(f.Subject.Name), applyStep(f))
			}
		}
	}

	if len(v.hotspots) > 0 {
		fmt.Fprintf(w, "\n## CPU hotspots\n\n| Namespace | Workload | Busiest replica | Of its node | Request | Scaling |\n|---|---|---|---|---|---|\n")
		for _, f := range v.hotspots {
			m := f.Measures[0]
			share := "?"
			if m.Capacity > 0 {
				share = fmt.Sprintf("%d%%", m.Used*100/m.Capacity)
			}
			fmt.Fprintf(w, "| `%s` | %s | %s | %s | %s | %s |\n", f.Subject.Namespace, md(f.Subject.Name), detect.FormatQuantity("cpu", m.Used), share, detect.FormatQuantity("cpu", m.Requested), md(f.Note))
		}
		fmt.Fprintf(w, "\nNext steps:\n\n")
		for _, f := range v.hotspots {
			for _, s := range f.Remediation.Steps {
				if s.Mutating && s.Command != "" {
					fmt.Fprintf(w, "- **%s**: %s: `%s`\n", md(f.Subject.Name), md(s.Description), s.Command)
				}
			}
		}
	}
	if v.hidden > 0 {
		fmt.Fprintf(w, "\n_%d lower-tier %s not shown (run with `--min-tier info`)._\n", v.hidden, plural(v.hidden, "finding", "findings"))
	}
}

// applyStep is the change a team makes: the first mutating step, with its command.
func applyStep(f detect.Finding) string {
	if f.Remediation == nil {
		return ""
	}
	for _, s := range f.Remediation.Steps {
		if !s.Mutating {
			continue
		}
		if s.Command != "" {
			return fmt.Sprintf("%s: `%s`", md(firstSentence(s.Description)), s.Command)
		}
		return md(firstSentence(s.Description))
	}
	return ""
}

func firstSentence(s string) string {
	if i := strings.Index(s, "; "); i > 0 {
		return s[:i]
	}
	return s
}

// md escapes text for a markdown table cell.
func md(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ").Replace(s)
}
