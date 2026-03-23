package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
	"github.com/Codebvoy15/k8s-doctor/internal/output"
)

var triageCmd = &cobra.Command{
	Use:   "triage",
	Short: "First-stop triage: unhealthy pods, events, crash loops, pending pods",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("TRIAGE — cluster: %s | ns: %s", clusterName, nsDisplay())
		printer.Section("Pod health")
		podFindings, err := engine.PodHealth()
		if err != nil {
			return fmt.Errorf("pod health check failed: %w", err)
		}
		printer.Findings(podFindings)
		printer.Section("Pending pods")
		pendingFindings, err := engine.PendingPods()
		if err != nil {
			return fmt.Errorf("pending pods check failed: %w", err)
		}
		printer.Findings(pendingFindings)
		printer.Section("Warning events (last 30m)")
		eventFindings, err := engine.RecentWarningEvents(30 * time.Minute)
		if err != nil {
			return fmt.Errorf("events check failed: %w", err)
		}
		printer.Findings(eventFindings)
		printer.Section("High restart pods (>3)")
		restartFindings, err := engine.HighRestartPods(3)
		if err != nil {
			return fmt.Errorf("restart check failed: %w", err)
		}
		printer.Findings(restartFindings)
		all := flatten(podFindings, pendingFindings, eventFindings, restartFindings)
		printer.RootCauseSummary(all)
		return nil
	},
}

var triageLogsCmd = &cobra.Command{
	Use:   "logs [pod-name]",
	Short: "Fetch crash logs from a pod",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		podName := ""
		if len(args) > 0 {
			podName = args[0]
		}
		logs, err := engine.FetchCrashLogs(podName, logLines)
		if err != nil {
			return err
		}
		for _, l := range logs {
			fmt.Println(color.HiWhiteString(l))
		}
		return nil
	},
}

var logLines int

func init() {
	triageCmd.AddCommand(triageLogsCmd)
	triageLogsCmd.Flags().IntVar(&logLines, "lines", 100, "number of log lines to fetch")
	rootCmd.AddCommand(triageCmd)
}

func flatten(sets ...[]diag.Finding) []diag.Finding {
	var all []diag.Finding
	for _, s := range sets {
		all = append(all, s...)
	}
	return all
}
