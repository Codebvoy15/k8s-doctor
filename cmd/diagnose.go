package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
)

var diagnoseWindow string

var diagnoseCmd = &cobra.Command{
	Use:   "diagnose",
	Short: "Full correlation analysis — pod health, changes, audit, root cause",
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

		fmt.Printf("\ndiagnose  cluster=%s  %s\n",
			color.New(color.FgWhite, color.Bold).Sprint(clusterName),
			color.HiBlackString(time.Now().Format("15:04:05")),
		)
		fmt.Fprintln(os.Stderr, color.HiBlackString(strings.Repeat("─", 72)))

		fmt.Fprintln(os.Stderr, color.HiBlackString("  checking pod health..."))
		podFindings, _ := engine.PodHealth()

		fmt.Fprintln(os.Stderr, color.HiBlackString("  checking pending pods and events..."))
		pendingFindings, _ := engine.PendingPods()
		eventFindings, _ := engine.RecentWarningEvents(window)

		fmt.Fprintln(os.Stderr, color.HiBlackString("  checking recent changes..."))
		diffs, _ := engine.LiveDeepDiff(window)

		fmt.Fprintln(os.Stderr, color.HiBlackString("  checking audit log..."))
		auditEntries, _ := engine.AuditLog(window, "", "")
		fmt.Fprintln(os.Stderr)

		// Collect active faults
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

		// Sort by score descending
		for i := 1; i < len(activeFaults); i++ {
			for j := i; j > 0 && activeFaults[j].Score > activeFaults[j-1].Score; j-- {
				activeFaults[j], activeFaults[j-1] = activeFaults[j-1], activeFaults[j]
			}
		}

		if len(activeFaults) == 0 && len(diffs) == 0 {
			fmt.Printf("status    no active problems and no recent changes\n\n")
			fmt.Printf("next      ./k8s-doctor predict\n\n")
			return nil
		}

		// Active faults
		if len(activeFaults) > 0 {
			fmt.Printf("FAULTS    %s\n", color.RedString("%d active", len(activeFaults)))
			fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
			for i, f := range activeFaults {
				if i >= 5 {
					fmt.Printf("          ... %d more\n", len(activeFaults)-5)
					break
				}
				sevLabel := "WARN"
				sevColor := color.YellowString
				if f.Severity == diag.SeverityCritical {
					sevLabel = "CRIT"
					sevColor = color.RedString
				}
				ref := ""
				if f.Object != "" {
					ref = fmt.Sprintf("  %s", color.HiBlackString(f.Namespace+"/"+f.Object))
				}
				fmt.Printf("  %s  %s%s\n", sevColor(sevLabel), f.Title, ref)
				if f.Detail != "" {
					fmt.Printf("          %s\n", color.HiBlackString(f.Detail))
				}
			}
			fmt.Println()
		}

		// Recent changes
		if len(diffs) > 0 {
			shown := 0
			fmt.Printf("CHANGES   %s\n", color.YellowString("%d in last %s", len(diffs), diagnoseWindow))
			fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
			for _, d := range diffs {
				if shown >= 5 {
					break
				}
				if strings.Contains(d.OldValue, "use --save") {
					continue
				}
				corr := ""
				if d.CorrelatedFault != "" {
					corr = color.RedString("  [correlated fault]")
				}
				fmt.Printf("  %s  %s/%s  by %s%s\n",
					color.YellowString("upd"),
					d.Kind,
					color.New(color.Bold).Sprint(d.Name),
					color.HiBlackString(d.ChangedBy),
					corr,
				)
				fmt.Printf("          field  %s\n", color.HiBlackString(d.Field))
				if d.OldValue != "" {
					fmt.Printf("          %s -> %s\n",
						color.HiBlackString(truncateStr(d.OldValue, 40)),
						truncateStr(d.NewValue, 40),
					)
				}
				shown++
			}
			fmt.Println()
		}

		// Root cause
		rc := correlateRootCause(activeFaults, diffs, auditEntries)
		fmt.Printf("ROOT CAUSE\n")
		fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
		if rc.Conclusion != "" {
			fmt.Printf("  conclusion  %s\n", rc.Conclusion)
		}
		if rc.Evidence != "" {
			fmt.Printf("  evidence    %s\n", color.HiBlackString(rc.Evidence))
		}
		if rc.ChangedBy != "" {
			fmt.Printf("  changed by  %s\n", color.HiBlackString(rc.ChangedBy))
		}
		if rc.ChangedAt != "" {
			fmt.Printf("  changed at  %s\n", color.HiBlackString(rc.ChangedAt))
		}
		if rc.Confidence > 0 {
			confColor := color.HiBlackString
			if rc.Confidence >= 80 {
				confColor = color.GreenString
			} else if rc.Confidence >= 60 {
				confColor = color.YellowString
			} else {
				confColor = color.RedString
			}
			fmt.Printf("  confidence  %s\n", confColor("%d%%", rc.Confidence))
		}
		if rc.Remedy != "" {
			fmt.Printf("  fix         %s\n", color.CyanString(rc.Remedy))
		}
		fmt.Println()

		// Next steps
		fmt.Printf("NEXT STEPS\n")
		fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
		fmt.Printf("  ./k8s-doctor triage          detailed pod health\n")
		fmt.Printf("  ./k8s-doctor events           full event timeline\n")
		fmt.Printf("  ./k8s-doctor node pressure    node diagnosis\n")
		fmt.Printf("  ./k8s-doctor report           generate ticket report\n\n")

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
			Conclusion: "no active pod faults — problem may be at the infrastructure layer",
			Remedy:     "./k8s-doctor node pressure",
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
				Conclusion: fmt.Sprintf("%s on %s/%s — likely caused by recent %s change", topFault.Title, topFault.Namespace, topFault.Object, d.Kind),
				Evidence:   fmt.Sprintf("%s field '%s' changed: %s -> %s", d.Kind, d.Field, truncateStr(d.OldValue, 40), truncateStr(d.NewValue, 40)),
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
				Conclusion: fmt.Sprintf("%s on %s/%s — %s change detected around the same time", topFault.Title, topFault.Namespace, topFault.Object, a.Kind),
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
		Conclusion: fmt.Sprintf("%s on %s/%s — no correlated changes found", topFault.Title, topFault.Namespace, topFault.Object),
		Evidence:   topFault.Detail,
		Remedy:     remedy,
		Confidence: 45,
	}
}

func init() {
	diagnoseCmd.Flags().StringVar(&diagnoseWindow, "window", "1h", "how far back to look (e.g. 30m, 1h, 2h)")
	rootCmd.AddCommand(diagnoseCmd)
}
