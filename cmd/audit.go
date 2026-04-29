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

var (
	auditWindow string
	auditKind   string
	auditUser   string
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Who changed what and when — change history with user attribution",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		window, err := time.ParseDuration(auditWindow)
		if err != nil {
			return fmt.Errorf("invalid --window %q: use formats like 1h, 30m", auditWindow)
		}
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		entries, err := engine.AuditLog(window, auditKind, auditUser)
		if err != nil {
			return fmt.Errorf("audit failed: %w", err)
		}

		fmt.Printf("\naudit  cluster=%s  window=%s  %s\n",
			color.New(color.FgWhite, color.Bold).Sprint(clusterName),
			auditWindow,
			color.HiBlackString(time.Now().Format("15:04:05")),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))

		if len(entries) == 0 {
			fmt.Printf("\n  no changes in this window\n\n")
			return nil
		}

		fmt.Printf("\n%-18s  %-10s  %-22s  %-18s  %-18s  %s\n",
			color.HiBlackString("time"),
			color.HiBlackString("kind"),
			color.HiBlackString("name"),
			color.HiBlackString("namespace"),
			color.HiBlackString("changed by"),
			color.HiBlackString("action"),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 100)))

		for _, e := range entries {
			actionFn := color.GreenString
			if e.Action == "DELETE" {
				actionFn = color.RedString
			} else if e.Action == "UPDATE" {
				actionFn = color.YellowString
			}
			corr := ""
			if e.CorrelatedFault != "" {
				corr = color.RedString("  correlated: " + e.CorrelatedFault)
			}
			fmt.Printf("%-18s  %-10s  %-22s  %-18s  %-18s  %s%s\n",
				color.HiBlackString(e.Timestamp.Format("01-02 15:04:05")),
				e.Kind,
				truncateStr(e.Name, 22),
				truncateStr(e.Namespace, 18),
				color.HiBlackString(truncateStr(e.FieldManager, 18)),
				actionFn(e.Action),
				corr,
			)
		}

		fmt.Printf("\ntotal  %d change(s)\n", len(entries))

		// Correlated changes
		var correlated []diag.AuditEntry
		for _, e := range entries {
			if e.CorrelatedFault != "" {
				correlated = append(correlated, e)
			}
		}
		if len(correlated) > 0 {
			fmt.Printf("\nCORRELATED WITH FAULTS\n")
			fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
			for _, e := range correlated {
				fmt.Printf("  %s  %s/%s  by %s\n",
					color.RedString(e.Action),
					e.Kind,
					e.Name,
					color.HiBlackString(e.FieldManager),
				)
				fmt.Printf("  fault  %s\n", color.RedString(e.CorrelatedFault))
				if e.Mitigation != "" {
					fmt.Printf("  fix    %s\n", color.CyanString(e.Mitigation))
				}
				fmt.Println()
			}
		}

		fmt.Println()
		return nil
	},
}

func init() {
	auditCmd.Flags().StringVar(&auditWindow, "window", "1h", "how far back to look (e.g. 30m, 2h, 24h)")
	auditCmd.Flags().StringVar(&auditKind, "kind", "", "filter by resource kind (e.g. Deployment, ConfigMap)")
	auditCmd.Flags().StringVar(&auditUser, "user", "", "filter by field manager / user name")
	rootCmd.AddCommand(auditCmd)
}
