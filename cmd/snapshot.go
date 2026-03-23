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

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Full cluster state at a glance — everything an ops engineer needs to know",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		snap, err := engine.ClusterSnapshot()
		if err != nil {
			return fmt.Errorf("snapshot failed: %w", err)
		}
		printSnapshot(snap)
		return nil
	},
}

func printSnapshot(s *diag.ClusterSnapshot) {
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	fmt.Printf("\n%s\n", color.New(color.FgCyan, color.Bold).Sprintf("╔══════════════════════════════════════════════════════════════╗"))
	fmt.Printf("%s\n", color.CyanString("  CLUSTER SNAPSHOT — %s", now))
	fmt.Printf("%s\n\n", color.New(color.FgCyan, color.Bold).Sprintf("╚══════════════════════════════════════════════════════════════╝"))
	scoreColor := color.GreenString
	scoreLabel := "HEALTHY"
	if s.HealthScore < 80 {
		scoreColor = color.YellowString
		scoreLabel = "DEGRADED"
	}
	if s.HealthScore < 60 {
		scoreColor = color.RedString
		scoreLabel = "CRITICAL"
	}
	fmt.Printf("  %s  %s  (%d/100)\n\n", color.CyanString("Cluster health:"), scoreColor("● "+scoreLabel), s.HealthScore)
	fmt.Printf("  %s  %s\n\n", color.HiBlackString("Server version:"), s.ServerVersion)
	fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Nodes"))
	fmt.Printf("  %-40s  %-10s  %-12s  %-12s  %-12s  %-12s\n", "NAME", "STATUS", "CPU REQ", "CPU CAP", "MEM REQ", "MEM CAP")
	fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 104)))
	for _, n := range s.Nodes {
		statusFn := color.GreenString
		if n.Status != "Ready" {
			statusFn = color.RedString
		}
		fmt.Printf("  %-40s  %-10s  %-12s  %-12s  %-12s  %-12s\n",
			n.Name, statusFn(n.Status), n.CPURequested, n.CPUCapacity, n.MemRequested, n.MemCapacity)
	}
	fmt.Println()
	fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Workloads by namespace"))
	fmt.Printf("  %-24s  %8s  %8s  %8s  %8s  %8s\n", "NAMESPACE", "DEPLOY", "PODS", "RUNNING", "FAILING", "STATEFUL")
	fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 72)))
	for _, ns := range s.Namespaces {
		failFn := color.GreenString
		if ns.FailingPods > 0 {
			failFn = color.RedString
		}
		fmt.Printf("  %-24s  %8d  %8d  %8d  %8s  %8d\n",
			ns.Name, ns.Deployments, ns.TotalPods, ns.RunningPods,
			failFn(fmt.Sprintf("%d", ns.FailingPods)), ns.StatefulSets)
	}
	fmt.Println()
	fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Top resource consumers (by memory)"))
	fmt.Printf("  %-44s  %-16s  %s\n", "POD", "NAMESPACE", "CPU REQ / MEM REQ")
	fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 80)))
	for i, c := range s.TopConsumers {
		if i >= 10 {
			break
		}
		name := c.Name
		if len(name) > 44 {
			name = name[:41] + "..."
		}
		fmt.Printf("  %-44s  %-16s  %s / %s\n", name, c.Namespace,
			color.YellowString(c.CPURequest), color.YellowString(c.MemRequest))
	}
	fmt.Println()
	if len(s.PVCs) > 0 {
		fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Persistent volume claims"))
		for _, pvc := range s.PVCs {
			fn := color.GreenString
			if pvc.Status != "Bound" {
				fn = color.RedString
			}
			fmt.Printf("  %-36s  %-16s  %-8s  %s\n", pvc.Name, pvc.Namespace, pvc.Capacity, fn(pvc.Status))
		}
		fmt.Println()
	}
	if len(s.Quotas) > 0 {
		fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Resource quota usage (>75%)"))
		for _, q := range s.Quotas {
			filled := int(q.UsedPercent / 10)
			if filled > 10 {
				filled = 10
			}
			bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
			barStr := color.GreenString(bar)
			if q.UsedPercent >= 90 {
				barStr = color.RedString(bar)
			} else if q.UsedPercent >= 75 {
				barStr = color.YellowString(bar)
			}
			fmt.Printf("  %-20s  %-20s  %s  %.0f%%\n", q.Namespace, q.Resource, barStr, q.UsedPercent)
		}
		fmt.Println()
	}
	if len(s.RecentWarnings) > 0 {
		fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Recent warnings (last 1h)"))
		for i, w := range s.RecentWarnings {
			if i >= 5 {
				fmt.Printf("  %s\n", color.HiBlackString("  ... and %d more", len(s.RecentWarnings)-5))
				break
			}
			fmt.Printf("  %s %-20s  %s\n", color.YellowString("◐"), w.Reason, color.HiBlackString(w.Message))
		}
		fmt.Println()
	}
}

func init() {
	rootCmd.AddCommand(snapshotCmd)
}
