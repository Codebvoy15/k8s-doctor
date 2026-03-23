package output

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
)

type Printer struct{ format string }

func NewPrinter(format string) *Printer { return &Printer{format: format} }

func (p *Printer) Header(format string, args ...interface{}) {
	title := fmt.Sprintf(format, args...)
	switch p.format {
	case "markdown":
		fmt.Printf("# %s\n_Generated: %s_\n\n", title, time.Now().Format("2006-01-02 15:04:05 MST"))
	case "json":
	default:
		fmt.Printf("\n%s %s\n%s\n",
			color.CyanString("┌─"),
			color.New(color.FgCyan, color.Bold).Sprint(title),
			color.HiBlackString("   %s", time.Now().Format("15:04:05")),
		)
	}
}

func (p *Printer) Section(label string) {
	switch p.format {
	case "markdown":
		fmt.Printf("\n## %s\n\n", label)
	case "json":
	default:
		fmt.Printf("\n  %s %s\n", color.HiBlackString("▸"), color.New(color.Bold).Sprint(label))
	}
}

func (p *Printer) Findings(findings []diag.Finding) {
	switch p.format {
	case "json":
		b, _ := json.MarshalIndent(findings, "", "  ")
		fmt.Println(string(b))
	case "markdown":
		for _, f := range findings {
			icon := "ℹ️"
			if f.Severity == diag.SeverityCritical {
				icon = "🔴"
			} else if f.Severity == diag.SeverityWarning {
				icon = "🟡"
			}
			ref := f.Object
			if f.Namespace != "" && f.Object != "" {
				ref = f.Namespace + "/" + f.Object
			}
			if ref != "" {
				fmt.Printf("- %s **%s** `%s`\n", icon, f.Title, ref)
			} else {
				fmt.Printf("- %s **%s**\n", icon, f.Title)
			}
			if f.Detail != "" {
				fmt.Printf("  - %s\n", f.Detail)
			}
			if f.Remedy != "" {
				fmt.Printf("  - Fix: `%s`\n", f.Remedy)
			}
		}
	default:
		for _, f := range findings {
			icon, clr := severityStyle(f.Severity)
			obj := ""
			if f.Object != "" {
				ns := ""
				if f.Namespace != "" {
					ns = f.Namespace + "/"
				}
				obj = color.HiBlackString(" [%s%s]", ns, f.Object)
			}
			fmt.Printf("    %s %s%s\n", icon, clr(f.Title), obj)
			if f.Detail != "" {
				fmt.Printf("      %s %s\n", color.HiBlackString("↳"), f.Detail)
			}
			if f.Remedy != "" {
				fmt.Printf("      %s %s\n", color.CyanString("→"), color.CyanString(f.Remedy))
			}
		}
	}
}

func (p *Printer) RootCauseSummary(findings []diag.Finding) {
	var real []diag.Finding
	for _, f := range findings {
		if f.Score > 0 {
			real = append(real, f)
		}
	}
	if len(real) == 0 {
		return
	}
	sort.Slice(real, func(i, j int) bool { return real[i].Score > real[j].Score })

	switch p.format {
	case "markdown":
		fmt.Println("\n---\n\n## Root cause assessment\n")
		for i, f := range real {
			if i >= 3 {
				break
			}
			fmt.Printf("%d. **[%d%%]** %s — %s\n", i+1, f.Score, f.Title, f.Detail)
			if f.Remedy != "" {
				fmt.Printf("   - `%s`\n", f.Remedy)
			}
		}
	case "json":
		top := real
		if len(top) > 5 {
			top = top[:5]
		}
		b, _ := json.MarshalIndent(map[string]interface{}{"top_findings": top}, "", "  ")
		fmt.Println(string(b))
	default:
		fmt.Printf("\n  %s\n", color.New(color.FgYellow, color.Bold).Sprint("⚡ Root cause (top signals):"))
		for i, f := range real {
			if i >= 3 {
				break
			}
			icon, _ := severityStyle(f.Severity)
			bar := scoreBar(f.Score)
			fmt.Printf("  %s [%s] %s\n", icon, bar, color.New(color.Bold).Sprint(f.Title))
			if f.Object != "" {
				fmt.Printf("      object: %s/%s\n", f.Namespace, f.Object)
			}
			if f.Remedy != "" {
				fmt.Printf("      action: %s\n", color.CyanString(f.Remedy))
			}
		}
		fmt.Println()
	}
}

func severityStyle(s diag.Severity) (string, func(string, ...interface{}) string) {
	switch s {
	case diag.SeverityCritical:
		return "●", color.New(color.FgRed, color.Bold).Sprintf
	case diag.SeverityWarning:
		return "◐", color.New(color.FgYellow).Sprintf
	default:
		return "○", color.New(color.FgGreen).Sprintf
	}
}

func scoreBar(score int) string {
	filled := score / 10
	bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
	if score >= 80 {
		return color.RedString(bar+" %d%%", score)
	} else if score >= 60 {
		return color.YellowString(bar+" %d%%", score)
	}
	return color.GreenString(bar+" %d%%", score)
}
