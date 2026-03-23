package cmd

import (
	"context"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
	"github.com/Codebvoy15/k8s-doctor/internal/output"
)

var ticketID string

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Run all diagnostics and produce a ticket-ready incident report",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		fmt := outputFmt
		if fmt == "terminal" {
			fmt = "markdown"
		}
		printer := output.NewPrinter(fmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("INCIDENT REPORT — cluster: %s | ticket: %s", clusterName, ticketID)
		color.Cyan("→ Running full diagnostic suite...")
		var all []diag.Finding
		run := func(name string, fn func() ([]diag.Finding, error)) {
			printer.Section(name)
			findings, err := fn()
			if err != nil {
				color.Yellow("  ⚠  %s failed: %v", name, err)
				return
			}
			printer.Findings(findings)
			all = append(all, findings...)
		}
		run("Pod health", engine.PodHealth)
		run("Pending pods", engine.PendingPods)
		run("Warning events (30m)", func() ([]diag.Finding, error) {
			return engine.RecentWarningEvents(30 * time.Minute)
		})
		run("High restart pods", func() ([]diag.Finding, error) {
			return engine.HighRestartPods(3)
		})
		run("Node pressure", engine.NodePressure)
		run("DNS diagnostics", engine.DNSDiag)
		run("Ingress health", engine.IngressHealth)
		printer.Section("Root cause summary")
		printer.RootCauseSummary(all)
		return nil
	},
}

func init() {
	reportCmd.Flags().StringVar(&ticketID, "ticket", "", "ticket ID (e.g. INC-1234)")
	rootCmd.AddCommand(reportCmd)
}
