package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/Codebvoy15/k8s-doctor/internal/upgrade"
)

func (p pen) status(s upgrade.Status) string {
	label := fmt.Sprintf("%-4s", string(s))
	switch s {
	case upgrade.StatusFail:
		return p.c(red+bold, label)
	case upgrade.StatusWarn:
		return p.c(yellow, label)
	case upgrade.StatusSkip:
		return p.c(grey, label)
	}
	return p.c(cyan, label)
}

// Upgrade prints post-upgrade results: a fleet table when there is more than
// one cluster, then each cluster's checks and the reasoning for every problem.
func Upgrade(w io.Writer, results []upgrade.Result, opt Options) {
	p := pen{on: opt.Color}
	if len(results) > 1 {
		upgradeFleetTable(w, results, p)
	}
	for i, r := range results {
		if len(results) > 1 && r.Status == upgrade.StatusPass && !opt.Verbose {
			continue // the table already says it is clean
		}
		if i > 0 || len(results) > 1 {
			fmt.Fprintln(w)
		}
		upgradeCluster(w, r, p, opt.Verbose)
	}
}

func upgradeFleetTable(w io.Writer, results []upgrade.Result, p pen) {
	fail, warn := 0, 0
	for _, r := range results {
		switch r.Status {
		case upgrade.StatusFail:
			fail++
		case upgrade.StatusWarn, upgrade.StatusSkip:
			warn++
		}
	}
	fmt.Fprintf(w, "%s  %d cluster(s)  target 1.%d\n", p.c(bold, "k8s-doctor post-upgrade"), len(results), results[0].Target.Minor)
	fmt.Fprintf(w, "%d clean · %d need attention · %d failing\n\n", len(results)-fail-warn, warn, fail)
	t := table{indent: "  ", header: true}
	t.add("CLUSTER", "CONTROL PLANE", "NODES", "KUBE-PROXY", "COREDNS", "VPC-CNI", "SYSTEM PODS", "VERDICT")
	for _, r := range results {
		name := upgrade.ShortName(r.Cluster)
		if r.Error != "" {
			t.add(name, p.status(upgrade.StatusFail)+" unreachable", "-", "-", "-", "-", "-", p.status(upgrade.StatusFail))
			continue
		}
		cell := func(id string) string { return p.status(r.Check(id).Status) }
		t.add(name,
			cell(upgrade.CheckControlPlane)+" "+firstWord(r.ControlPlane),
			cell(upgrade.CheckNodeVersions)+fmt.Sprintf(" %d/%d", r.NodesOnTarget, r.NodesTotal),
			cell(upgrade.CheckKubeProxy),
			cell(upgrade.CheckCoreDNS),
			cell(upgrade.CheckVPCCNI),
			cell(upgrade.CheckSystemPods),
			p.status(r.Status))
	}
	t.write(w, p)
}

func upgradeCluster(w io.Writer, r upgrade.Result, p pen, verbose bool) {
	when := ""
	if !r.CapturedAt.IsZero() {
		when = "  " + r.CapturedAt.Format("2006-01-02 15:04 UTC")
	}
	fmt.Fprintf(w, "%s  %s  target 1.%d%s\n", p.c(bold, "k8s-doctor post-upgrade"), upgrade.ShortName(r.Cluster), r.Target.Minor, when)
	if r.Error != "" {
		fmt.Fprintf(w, "%s  %s\n", p.status(upgrade.StatusFail), r.Error)
		return
	}
	nFail, nWarn := 0, 0
	for _, c := range r.Checks {
		switch c.Status {
		case upgrade.StatusFail:
			nFail++
		case upgrade.StatusWarn, upgrade.StatusSkip:
			nWarn++
		}
	}
	headline := "upgrade complete: every check passed"
	if nFail+nWarn > 0 {
		headline = fmt.Sprintf("%d failing · %d warning · %d/%d nodes on 1.%d", nFail, nWarn, r.NodesOnTarget, r.NodesTotal, minorFromTarget(r))
	}
	fmt.Fprintf(w, "%s  %s\n\n", p.status(r.Status), headline)

	t := table{indent: "  ", header: true}
	t.add("CHECK", "STATUS", "DETAIL")
	for _, c := range r.Checks {
		t.add(c.Title, p.status(c.Status), c.Detail)
	}
	t.write(w, p)

	var shown bool
	for _, c := range r.Checks {
		if len(c.Problems) == 0 || (c.Status == upgrade.StatusPass && !verbose) {
			continue
		}
		if !shown {
			fmt.Fprintf(w, "\n%s\n", p.c(bold, "WHY"))
			shown = true
		}
		for _, pr := range c.Problems {
			fmt.Fprintf(w, "\n  %s %s — %s\n", p.status(pr.Status), p.c(bold, c.Title), pr.What)
			writeLines(w, p, "why", pr.Why)
			writeLines(w, p, "evidence", pr.Evidence)
			writeLines(w, p, "fix", pr.Fix)
		}
	}
	if r.Target.Source != "" {
		fmt.Fprintf(w, "\n%s\n", p.c(grey, "add-on targets: "+targetLine(r.Target)+" ("+r.Target.Source+")"))
	}
}

func writeLines(w io.Writer, p pen, label string, lines []string) {
	for i, l := range lines {
		tag := ""
		if i == 0 {
			tag = label
		}
		fmt.Fprintf(w, "      %s  %s\n", p.c(grey, fmt.Sprintf("%-8s", tag)), l)
	}
}

// UpgradeMarkdown writes the same results as a report to paste into a ticket or status update.
func UpgradeMarkdown(w io.Writer, results []upgrade.Result) {
	if len(results) == 0 {
		return
	}
	fmt.Fprintf(w, "# Post-upgrade check — target 1.%d\n\n", results[0].Target.Minor)
	fmt.Fprintln(w, "| Cluster | Control plane | Nodes on target | kube-proxy | CoreDNS | VPC CNI | System pods | Verdict |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|")
	for _, r := range results {
		name := upgrade.ShortName(r.Cluster)
		if r.Error != "" {
			fmt.Fprintf(w, "| %s | unreachable | - | - | - | - | - | **FAIL** |\n", name)
			continue
		}
		s := func(id string) string { return string(r.Check(id).Status) }
		fmt.Fprintf(w, "| %s | %s %s | %d/%d | %s | %s | %s | %s | **%s** |\n", name,
			s(upgrade.CheckControlPlane), firstWord(r.ControlPlane), r.NodesOnTarget, r.NodesTotal,
			s(upgrade.CheckKubeProxy), s(upgrade.CheckCoreDNS), s(upgrade.CheckVPCCNI), s(upgrade.CheckSystemPods), r.Status)
	}
	for _, r := range results {
		if r.Status == upgrade.StatusPass {
			continue
		}
		fmt.Fprintf(w, "\n## %s\n\n", upgrade.ShortName(r.Cluster))
		if r.Error != "" {
			fmt.Fprintf(w, "**FAIL** %s\n", r.Error)
			continue
		}
		for _, c := range r.Checks {
			for _, pr := range c.Problems {
				if c.Status == upgrade.StatusPass {
					continue
				}
				fmt.Fprintf(w, "### %s %s: %s\n\n", pr.Status, c.Title, pr.What)
				mdList(w, "Why", pr.Why)
				mdList(w, "Evidence", pr.Evidence)
				mdList(w, "Fix", pr.Fix)
			}
		}
	}
	fmt.Fprintf(w, "\n_Add-on targets: %s (%s)._\n", targetLine(results[0].Target), results[0].Target.Source)
}

func mdList(w io.Writer, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "**%s**\n\n", title)
	for _, l := range lines {
		fmt.Fprintf(w, "- %s\n", strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- ")))
	}
	fmt.Fprintln(w)
}

func targetLine(t upgrade.Targets) string {
	return fmt.Sprintf("kube-proxy %s · coredns %s · vpc-cni %s", dash(t.KubeProxy), dash(t.CoreDNS), dash(t.VPCCNI))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func firstWord(v string) string {
	if i := strings.Index(v, "-eks-"); i > 0 {
		return v[:i]
	}
	return v
}

func minorFromTarget(r upgrade.Result) int {
	if m, err := upgrade.ParseMinor(r.ControlPlane); err == nil {
		return m
	}
	return r.Target.Minor
}
