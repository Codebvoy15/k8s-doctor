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
		fmt.Printf("# %s\n_generated: %s_\n\n", title, time.Now().Format("2006-01-02 15:04:05"))
	case "json":
	default:
		fmt.Printf("\n%s  %s\n",
			color.New(color.FgWhite, color.Bold).Sprint(strings.ToLower(title)),
			color.HiBlackString(time.Now().Format("15:04:05")),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
	}
}

func (p *Printer) Section(label string) {
	switch p.format {
	case "markdown":
		fmt.Printf("\n## %s\n\n", label)
	case "json":
	default:
		fmt.Printf("\n%s\n", color.New(color.Bold).Sprint(strings.ToUpper(label)))
	}
}

func (p *Printer) Findings(findings []diag.Finding) {
	switch p.format {
	case "json":
		b, _ := json.MarshalIndent(findings, "", "  ")
		fmt.Println(string(b))
	case "markdown":
		for _, f := range findings {
			sev := "info"
			if f.Severity == diag.SeverityCritical {
				sev = "critical"
			} else if f.Severity == diag.SeverityWarning {
				sev = "warning"
			}
			ref := ""
			if f.Namespace != "" && f.Object != "" {
				ref = f.Namespace + "/" + f.Object
			} else if f.Object != "" {
				ref = f.Object
			}
			if ref != "" {
				fmt.Printf("- [%s] %s  %s\n", sev, f.Title, ref)
			} else {
				fmt.Printf("- [%s] %s\n", sev, f.Title)
			}
			if f.Detail != "" {
				fmt.Printf("  %s\n", f.Detail)
			}
			if f.Remedy != "" {
				fmt.Printf("  fix: %s\n", f.Remedy)
			}
		}
	default:
		for _, f := range findings {
			sevLabel, sevColor := severityStyle(f.Severity)
			ref := ""
			if f.Object != "" {
				ns := ""
				if f.Namespace != "" {
					ns = f.Namespace + "/"
				}
				ref = color.HiBlackString("  %s%s", ns, f.Object)
			}
			fmt.Printf("  %s  %s%s\n", sevColor(sevLabel), f.Title, ref)
			if f.Detail != "" {
				fmt.Printf("        %s\n", color.HiBlackString(f.Detail))
			}
			if f.Remedy != "" {
				fmt.Printf("        fix: %s\n", color.CyanString(f.Remedy))
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
		fmt.Println("\n---\n\n## root cause\n")
		for i, f := range real {
			if i >= 3 {
				break
			}
			fmt.Printf("%d. [%d%%] %s\n", i+1, f.Score, f.Title)
			if f.Detail != "" {
				fmt.Printf("   %s\n", f.Detail)
			}
			if f.Remedy != "" {
				fmt.Printf("   fix: %s\n", f.Remedy)
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
		fmt.Printf("\n%s\n", color.New(color.Bold).Sprint("ROOT CAUSE"))
		fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
		for i, f := range real {
			if i >= 3 {
				break
			}
			_, sevColor := severityStyle(f.Severity)
			scoreColor := color.HiBlackString
			if f.Score >= 80 {
				scoreColor = color.RedString
			} else if f.Score >= 60 {
				scoreColor = color.YellowString
			}
			fmt.Printf("  %s  %s\n",
				scoreColor(fmt.Sprintf("%d%%", f.Score)),
				sevColor(f.Title),
			)
			if f.Object != "" {
				fmt.Printf("        object  %s/%s\n",
					color.HiBlackString(f.Namespace),
					color.HiBlackString(f.Object),
				)
			}
			if f.Detail != "" {
				fmt.Printf("        detail  %s\n", color.HiBlackString(f.Detail))
			}
			if f.Remedy != "" {
				fmt.Printf("        fix     %s\n", color.CyanString(f.Remedy))
			}
			fmt.Println()
		}
	}
}

func severityStyle(s diag.Severity) (string, func(string, ...interface{}) string) {
	switch s {
	case diag.SeverityCritical:
		return "CRIT", color.New(color.FgRed).Sprintf
	case diag.SeverityWarning:
		return "WARN", color.New(color.FgYellow).Sprintf
	default:
		return "ok  ", color.New(color.FgGreen).Sprintf
	}
}
