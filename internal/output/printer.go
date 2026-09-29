package output

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/diag"
	"github.com/fatih/color"
)

type Printer struct {
	format string
	doc    *Document
}

// Document is the single JSON envelope emitted in -o json mode.
// Every command that uses Printer emits exactly one Document on stdout,
// so the output can be parsed as one value (by jq, an MCP server, or an LLM).
type Document struct {
	SchemaVersion string         `json:"schema_version"`
	Command       string         `json:"command,omitempty"`
	Cluster       string         `json:"cluster,omitempty"`
	Namespace     string         `json:"namespace,omitempty"`
	Title         string         `json:"title,omitempty"`
	GeneratedAt   string         `json:"generated_at"`
	Sections      []Section      `json:"sections"`
	RootCause     []diag.Finding `json:"root_cause"`
}

type Section struct {
	Name     string         `json:"name"`
	Findings []diag.Finding `json:"findings"`
}

// SchemaVersion is bumped whenever the Document shape changes incompatibly.
const SchemaVersion = "1"

var runContext struct{ command, cluster, namespace string }

// SetContext records the command path and target cluster/namespace so the
// JSON envelope is self-describing. Called once from the root PersistentPreRunE.
func SetContext(command, cluster, namespace string) {
	runContext.command, runContext.cluster, runContext.namespace = command, cluster, namespace
}

func NewPrinter(format string) *Printer {
	p := &Printer{format: format}
	if format == "json" {
		p.doc = &Document{
			SchemaVersion: SchemaVersion,
			Command:       runContext.command,
			Cluster:       runContext.cluster,
			Namespace:     runContext.namespace,
			GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
			Sections:      []Section{},
			RootCause:     []diag.Finding{},
		}
	}
	return p
}

// IsJSON reports whether the printer is collecting a JSON document.
// Commands use it to suppress ad-hoc fmt.Print output that would corrupt stdout.
func (p *Printer) IsJSON() bool { return p.format == "json" }

// Flush writes the collected JSON document. No-op for other formats.
// Call it with defer right after NewPrinter.
func (p *Printer) Flush() {
	if p.doc == nil {
		return
	}
	b, _ := json.MarshalIndent(p.doc, "", "  ")
	fmt.Println(string(b))
	p.doc = nil
}

func (p *Printer) Header(format string, args ...interface{}) {
	title := fmt.Sprintf(format, args...)
	switch p.format {
	case "markdown":
		fmt.Printf("# %s\n_generated: %s_\n\n", title, time.Now().Format("2006-01-02 15:04:05"))
	case "json":
		p.doc.Title = title
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
		p.doc.Sections = append(p.doc.Sections, Section{Name: label, Findings: []diag.Finding{}})
	default:
		fmt.Printf("\n%s\n", color.New(color.Bold).Sprint(strings.ToUpper(label)))
	}
}

func (p *Printer) Findings(findings []diag.Finding) {
	switch p.format {
	case "json":
		// Commands without an explicit Section get a single default one.
		if len(p.doc.Sections) == 0 {
			p.doc.Sections = append(p.doc.Sections, Section{Name: "findings", Findings: []diag.Finding{}})
		}
		last := &p.doc.Sections[len(p.doc.Sections)-1]
		last.Findings = append(last.Findings, findings...)
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
	sort.SliceStable(real, func(i, j int) bool { return real[i].Score > real[j].Score })

	switch p.format {
	case "markdown":
		fmt.Print("\n---\n\n## root cause\n\n")
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
		// Full ranked list (not truncated): the consumer decides how many to show.
		p.doc.RootCause = real
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
