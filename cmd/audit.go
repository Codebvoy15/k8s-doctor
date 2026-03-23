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
	Short: "Who did what and when — change history with user attribution",
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
		printAuditLog(entries, window)
		return nil
	},
}

func printAuditLog(entries []diag.AuditEntry, window time.Duration) {
	fmt.Printf("\n%s\n\n", color.New(color.FgCyan, color.Bold).Sprintf("AUDIT LOG — last %s", window))
	if len(entries) == 0 {
		fmt.Println(color.GreenString("  No changes detected in this window."))
		return
	}
	fmt.Printf("  %-18s  %-12s  %-22s  %-20s  %-20s  %s\n",
		"TIME", "KIND", "NAME", "NAMESPACE", "CHANGED BY", "ACTION")
	fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 100)))
	for _, e := range entries {
		actionFn := color.GreenString
		if e.Action == "DELETE" {
			actionFn = color.RedString
		} else if e.Action == "UPDATE" {
			actionFn = color.YellowString
		}
		corr := ""
		if e.CorrelatedFault != "" {
			corr = color.RedString(" ⚠ " + e.CorrelatedFault)
		}
		name := e.Name
		if len(name) > 22 {
			name = name[:19] + "..."
		}
		ns := e.Namespace
		if len(ns) > 20 {
			ns = ns[:17] + "..."
		}
		fm := e.FieldManager
		if len(fm) > 20 {
			fm = fm[:17] + "..."
		}
		fmt.Printf("  %-18s  %-12s  %-22s  %-20s  %-20s  %s%s\n",
			color.HiBlackString(e.Timestamp.Format("01-02 15:04:05")),
			e.Kind, name, ns, color.CyanString(fm),
			actionFn(e.Action), corr,
		)
	}
	fmt.Printf("\n  Total: %d change(s)\n", len(entries))
	var correlated []diag.AuditEntry
	for _, e := range entries {
		if e.CorrelatedFault != "" {
			correlated = append(correlated, e)
		}
	}
	if len(correlated) > 0 {
		fmt.Printf("\n  %s\n", color.New(color.FgRed, color.Bold).Sprint("Changes correlated with active faults:"))
		for _, e := range correlated {
			fmt.Printf("  %s  %-12s  %-24s  by %-20s  → %s\n",
				color.RedString("●"), e.Kind, e.Name,
				color.CyanString(e.FieldManager), color.RedString(e.CorrelatedFault))
			if e.Mitigation != "" {
				fmt.Printf("    %s %s\n", color.GreenString("→ Fix:"), color.GreenString(e.Mitigation))
			}
		}
	}
	fmt.Println()
}

func init() {
	auditCmd.Flags().StringVar(&auditWindow, "window", "1h", "how far back to look (e.g. 30m, 2h, 24h)")
	auditCmd.Flags().StringVar(&auditKind, "kind", "", "filter by resource kind (e.g. Deployment, ConfigMap)")
	auditCmd.Flags().StringVar(&auditUser, "user", "", "filter by field manager / user name")
	rootCmd.AddCommand(auditCmd)
}
