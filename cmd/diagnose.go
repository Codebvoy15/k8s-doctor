package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
)

var diagnoseWindow string

var diagnoseCmd = &cobra.Command{
	Use:   "diagnose",
	Short: "One command root cause analysis — runs everything and gives you ONE answer",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		window, err := time.ParseDuration(diagnoseWindow)
		if err != nil {
			return fmt.Errorf("invalid --window: use 30m, 1h, 2h")
		}
		fmt.Printf("\n%s\n", color.New(color.FgCyan, color.Bold).Sprint("╔══════════════════════════════════════════════════════════════╗"))
		fmt.Printf("%s\n", color.CyanString("  DIAGNOSE — running full correlation analysis..."))
		fmt.Printf("%s\n\n", color.New(color.FgCyan, color.Bold).Sprint("╚══════════════════════════════════════════════════════════════╝"))
		color.HiBlack("  [1/4] Checking pod health...")
		podFindings, _ := engine.PodHealth()
		color.HiBlack("  [2/4] Checking pending pods and events...")
		pendingFindings, _ := engine.PendingPods()
		eventFindings, _ := engine.RecentWarningEvents(window)
		color.HiBlack("  [3/4] Checking recent changes...")
		diffs, _ := engine.LiveDeepDiff(window)
		color.HiBlack("  [4/4] Checking audit log...")
		auditEntries, _ := engine.AuditLog(window, "", "")
		fmt.Println()
		var activeFaults []diag.Finding
		for _, f := range podFindings {
			if f.Score > 0 {
				activeFaults = append(activeFaults, f)
			}
		}
		for _, f := range pendingFindings {
			if f.Score > 0 {
				activeFaults = append(activeFaults, f)
			}
		}
		for _, f := range eventFindings {
			if f.Score > 40 {
				activeFaults = append(activeFaults, f)
			}
		}
		for i := 1; i < len(activeFaults); i++ {
			for j := i; j > 0 && activeFaults[j].Score > activeFaults[j-1].Score; j-- {
				activeFaults[j], activeFaults[j-1] = activeFaults[j-1], activeFaults[j]
			}
		}
		if len(activeFaults) == 0 && len(diffs) == 0 {
			fmt.Printf("  %s  No active problems found and no recent changes detected.\n\n", color.GreenString("✓"))
			fmt.Printf("  %s\n", color.HiBlackString("Run ./k8s-doctor predict for proactive risks."))
			return nil
		}
		if len(activeFaults) > 0 {
			fmt.Printf("  %s\n\n", color.New(color.FgRed, color.Bold).Sprint("Active problems detected:"))
			for i, f := range activeFaults {
				if i >= 5 {
					break
				}
				icon := color.YellowString("◐")
				if f.Severity == diag.SeverityCritical {
					icon = color.RedString("●")
				}
				fmt.Printf("  %s  %s\n", icon, color.New(color.Bold).Sprint(f.Title))
				if f.Object != "" {
					fmt.Printf("     object: %s/%s\n", f.Namespace, f.Object)
				}
				if f.Detail != "" {
					fmt.Printf("     detail: %s\n", color.HiBlackString(f.Detail))
				}
			}
			fmt.Println()
		}
		if len(diffs) > 0 {
			fmt.Printf("  %s\n\n", color.New(color.FgYellow, color.Bold).Sprint("Recent changes in this window:"))
			shown := 0
			for _, d := range diffs {
				if shown >= 5 {
					break
				}
				if strings.Contains(d.OldValue, "use --save") {
					continue
				}
				fmt.Printf("  %s  %s/%s  %s\n",
					color.YellowString("△"),
					color.CyanString(d.Kind),
					color.New(color.Bold).Sprint(d.Name),
					color.HiBlackString("by "+d.ChangedBy),
				)
				fmt.Printf("     field: %s\n", d.Field)
				if d.OldValue != "" {
					fmt.Printf("     %s → %s\n",
						color.RedString(d.OldValue),
						color.GreenString(d.NewValue),
					)
				}
				shown++
			}
			fmt.Println()
		}
		fmt.Printf("%s\n\n", color.New(color.FgYellow, color.Bold).Sprint("  ⚡ Root cause assessment:"))
		rc := correlateRootCause(activeFaults, diffs, auditEntries)
		if rc.Conclusion != "" {
			fmt.Printf("  %s\n\n", color.New(color.FgWhite, color.Bold).Sprint(rc.Conclusion))
		}
		if rc.Evidence != "" {
			fmt.Printf("  %s %s\n", color.HiBlackString("Evidence:  "), rc.Evidence)
		}
		if rc.ChangedBy != "" {
			fmt.Printf("  %s %s\n", color.HiBlackString("Changed by:"), color.CyanString(rc.ChangedBy))
		}
		if rc.ChangedAt != "" {
			fmt.Printf("  %s %s\n", color.HiBlackString("Changed at:"), rc.ChangedAt)
		}
		if rc.Remedy != "" {
			fmt.Printf("\n  %s\n", color.New(color.FgGreen, color.Bold).Sprint("Recommended action:"))
			fmt.Printf("  %s\n", color.GreenString(rc.Remedy))
		}
		if rc.Confidence > 0 {
			filled := rc.Confidence / 10
			bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
			barStr := color.GreenString(bar)
			if rc.Confidence < 50 {
				barStr = color.RedString(bar)
			} else if rc.Confidence < 80 {
				barStr = color.YellowString(bar)
			}
			fmt.Printf("\n  %s %s (%d%% confidence)\n", color.HiBlackString("Confidence:"), barStr, rc.Confidence)
		}
		fmt.Printf("\n  %s\n", color.HiBlackString("Next steps:"))
		fmt.Printf("  %s\n", color.HiBlackString("  ./k8s-doctor triage          — detailed pod health"))
		fmt.Printf("  %s\n", color.HiBlackString("  ./k8s-doctor events           — full event timeline"))
		fmt.Printf("  %s\n\n", color.HiBlackString("  ./k8s-doctor report           — generate ticket report"))
		return nil
	},
}

type RootCause struct {
	Conclusion string
	Evidence   string
	ChangedBy  string
	ChangedAt  string
	Remedy     string
	Confidence int
}

func correlateRootCause(faults []diag.Finding, diffs []diag.DeepDiffEntry, audit []diag.AuditEntry) RootCause {
	if len(faults) == 0 {
		return RootCause{
			Conclusion: "No active pod faults — problem may be at the infrastructure layer.",
			Remedy:     "./k8s-doctor aws ec2  →  check EC2 instance health",
			Confidence: 30,
		}
	}
	topFault := faults[0]
	for _, d := range diffs {
		if d.CorrelatedFault != "" || strings.HasPrefix(topFault.Object, d.Name) || d.Name == topFault.Object {
			remedy := d.Mitigation
			if remedy == "" {
				remedy = fmt.Sprintf("kubectl rollout undo deployment/%s -n %s", d.Name, d.Namespace)
			}
			changedAt := ""
			if !d.Timestamp.IsZero() {
				changedAt = d.Timestamp.Format("2006-01-02 15:04:05")
			}
			return RootCause{
				Conclusion: fmt.Sprintf("%s on %s/%s is likely caused by a recent %s change.", topFault.Title, topFault.Namespace, topFault.Object, d.Kind),
				Evidence:   fmt.Sprintf("%s field '%s' changed: %s → %s", d.Kind, d.Field, d.OldValue, d.NewValue),
				ChangedBy:  d.ChangedBy,
				ChangedAt:  changedAt,
				Remedy:     remedy,
				Confidence: 85,
			}
		}
	}
	for _, a := range audit {
		if strings.HasPrefix(topFault.Object, a.Name) || a.Name == topFault.Object {
			remedy := a.Mitigation
			if remedy == "" {
				remedy = fmt.Sprintf("kubectl rollout undo deployment/%s -n %s", a.Name, a.Namespace)
			}
			return RootCause{
				Conclusion: fmt.Sprintf("%s on %s/%s — a %s change was detected around the same time.", topFault.Title, topFault.Namespace, topFault.Object, a.Kind),
				Evidence:   fmt.Sprintf("%s '%s' was %s", a.Kind, a.Name, a.Action),
				ChangedBy:  a.FieldManager,
				ChangedAt:  a.Timestamp.Format("2006-01-02 15:04:05"),
				Remedy:     remedy,
				Confidence: 65,
			}
		}
	}
	remedy := topFault.Remedy
	if remedy == "" {
		remedy = fmt.Sprintf("kubectl describe pod -l app=%s -n %s", topFault.Object, topFault.Namespace)
	}
	return RootCause{
		Conclusion: fmt.Sprintf("%s on %s/%s — no recent changes detected that correlate directly.", topFault.Title, topFault.Namespace, topFault.Object),
		Evidence:   topFault.Detail,
		Remedy:     remedy,
		Confidence: 45,
	}
}

func init() {
	diagnoseCmd.Flags().StringVar(&diagnoseWindow, "window", "1h", "how far back to look (e.g. 30m, 1h, 2h)")
	rootCmd.AddCommand(diagnoseCmd)
}
