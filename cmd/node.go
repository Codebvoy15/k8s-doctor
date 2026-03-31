package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
	"github.com/Codebvoy15/k8s-doctor/internal/output"
)

var nodeCmd = &cobra.Command{
	Use:   "node",
	Short: "Node diagnostics: pressure, taints, top, cordon",
}

var nodePressureCmd = &cobra.Command{
	Use:   "pressure",
	Short: "Deep node diagnosis — exact cause of NotReady, memory, disk, PID, Karpenter",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}

		fmt.Printf("\n%s\n\n",
			color.New(color.FgCyan, color.Bold).Sprintf("NODE DIAGNOSIS — cluster: %s", clusterName))

		diagnoses, err := engine.NodeDiagnoseAll()
		if err != nil {
			// fallback to old behaviour if new function fails
			printer := output.NewPrinter(outputFmt)
			printer.Header("NODE PRESSURE — cluster: %s", clusterName)
			findings, err2 := engine.NodePressure()
			if err2 != nil {
				return err2
			}
			printer.Findings(findings)
			printer.RootCauseSummary(findings)
			return nil
		}

		notReady := 0
		for _, d := range diagnoses {
			if !d.Ready {
				notReady++
			}
		}

		// Summary line
		if notReady == 0 {
			fmt.Printf("  %s  All %d nodes are healthy\n\n",
				color.GreenString("✓"), len(diagnoses))
		} else {
			fmt.Printf("  %s  %d/%d nodes are NotReady\n\n",
				color.RedString("●"), notReady, len(diagnoses))
		}

		for _, d := range diagnoses {
			printNodeDiagnosis(d)
		}

		return nil
	},
}

func printNodeDiagnosis(d diag.NodeDiagnosis) {
	// ── HEADER ────────────────────────────────────────────────────────────────
	if d.Ready && d.Reason == "Healthy" {
		fmt.Printf("  %s  %s\n",
			color.GreenString("✓"),
			color.HiBlackString(d.NodeName),
		)
		return
	}

	// Severity color
	sevColor := color.New(color.FgYellow, color.Bold)
	sevIcon := "◐"
	if d.Severity == "CRITICAL" {
		sevColor = color.New(color.FgRed, color.Bold)
		sevIcon = "●"
	}

	fmt.Printf("\n  %s  %s\n",
		sevColor.Sprint(sevIcon),
		color.New(color.Bold).Sprint(d.NodeName),
	)

	// Instance info
	info := []string{}
	if d.InstanceType != "" {
		info = append(info, d.InstanceType)
	}
	if d.AMIFamily != "" {
		info = append(info, d.AMIFamily)
	}
	if d.IsKarpenter {
		info = append(info, "Karpenter")
	}
	if d.IsSpot {
		info = append(info, color.YellowString("SPOT"))
	}
	if len(info) > 0 {
		fmt.Printf("     %s\n", color.HiBlackString(strings.Join(info, " · ")))
	}

	// NotReady duration
	if !d.Ready && d.NotReadyFor != "" {
		fmt.Printf("     %s NotReady for: %s\n",
			color.RedString("↳"),
			color.RedString(d.NotReadyFor),
		)
	}

	// Pressure badges
	pressures := []string{}
	if d.MemoryPressure {
		pressures = append(pressures, color.RedString("MemoryPressure"))
	}
	if d.DiskPressure {
		pressures = append(pressures, color.RedString("DiskPressure"))
	}
	if d.PIDPressure {
		pressures = append(pressures, color.YellowString("PIDPressure"))
	}
	if len(pressures) > 0 {
		fmt.Printf("     %s Conditions: %s\n",
			color.HiBlackString("↳"),
			strings.Join(pressures, " + "),
		)
	}

	// ── ROOT CAUSE ────────────────────────────────────────────────────────────
	if d.RootCause != "" {
		fmt.Printf("\n     %s %s\n",
			color.New(color.FgYellow, color.Bold).Sprint("⚡ Root cause:"),
			color.New(color.FgWhite).Sprint(d.RootCause),
		)
	}

	// ── EVIDENCE ──────────────────────────────────────────────────────────────
	if len(d.Evidence) > 0 {
		fmt.Printf("\n     %s\n", color.HiBlackString("Evidence:"))
		for _, ev := range d.Evidence {
			fmt.Printf("       %s %s\n",
				color.HiBlackString("·"),
				color.HiBlackString(ev),
			)
		}
	}

	// ── OOM KILLED PODS ───────────────────────────────────────────────────────
	if len(d.OOMKilledPods) > 0 {
		fmt.Printf("\n     %s\n", color.RedString("OOMKilled pods on this node:"))
		for _, pod := range d.OOMKilledPods {
			fmt.Printf("       %s %s\n", color.RedString("●"), pod)
		}
	}

	// ── TOP CONSUMERS ON THIS NODE ────────────────────────────────────────────
	if len(d.TopPods) > 0 && (d.MemoryPressure || d.PIDPressure) {
		fmt.Printf("\n     %s\n", color.HiBlackString("Top pods on this node (by memory request):"))
		fmt.Printf("       %-40s  %-16s  %s  %s\n",
			color.HiBlackString("POD"),
			color.HiBlackString("NAMESPACE"),
			color.HiBlackString("MEM REQ"),
			color.HiBlackString("RESTARTS"),
		)
		fmt.Printf("       %s\n", color.HiBlackString(strings.Repeat("─", 80)))
		for i, pod := range d.TopPods {
			if i >= 5 {
				fmt.Printf("       %s\n", color.HiBlackString(fmt.Sprintf("... and %d more", len(d.TopPods)-5)))
				break
			}
			oomTag := ""
			if pod.OOMKilled {
				oomTag = color.RedString(" [OOMKilled]")
			}
			restartColor := color.HiBlackString
			if pod.Restarts > 5 {
				restartColor = color.YellowString
			}
			if pod.Restarts > 10 {
				restartColor = color.RedString
			}
			fmt.Printf("       %-40s  %-16s  %-8s  %s%s\n",
				truncateStr(pod.PodName, 40),
				truncateStr(pod.Namespace, 16),
				color.YellowString(pod.MemRequest),
				restartColor(fmt.Sprintf("%d", pod.Restarts)),
				oomTag,
			)
		}
	}

	// ── DISK USAGE ────────────────────────────────────────────────────────────
	if d.DiskUsage != nil {
		usageColor := color.GreenString
		if d.DiskUsage.UsedPercent > 90 {
			usageColor = color.RedString
		} else if d.DiskUsage.UsedPercent > 75 {
			usageColor = color.YellowString
		}

		filled := int(d.DiskUsage.UsedPercent / 5)
		if filled > 20 {
			filled = 20
		}
		bar := strings.Repeat("█", filled) + strings.Repeat("░", 20-filled)

		fmt.Printf("\n     %s Disk: [%s] %s (%.1f/%.1fGB)\n",
			color.HiBlackString("↳"),
			usageColor(bar),
			usageColor(fmt.Sprintf("%.0f%%", d.DiskUsage.UsedPercent)),
			d.DiskUsage.UsedGB,
			d.DiskUsage.TotalGB,
		)

		if len(d.DiskUsage.TopDirs) > 0 {
			fmt.Printf("     %s Top directories:\n", color.HiBlackString("↳"))
			for _, dir := range d.DiskUsage.TopDirs {
				fmt.Printf("       %-30s  %.1fGB\n",
					dir.Path,
					dir.SizeGB,
				)
			}
		}
	}

	// ── RECENT EVENTS ─────────────────────────────────────────────────────────
	if len(d.RecentEvents) > 0 {
		fmt.Printf("\n     %s Recent events:\n", color.HiBlackString("↳"))
		for i, ev := range d.RecentEvents {
			if i >= 3 {
				break
			}
			fmt.Printf("       %s [x%d] %s: %s\n",
				color.HiBlackString(ev.Time.Format("15:04:05")),
				ev.Count,
				color.YellowString(ev.Reason),
				color.HiBlackString(truncateStr(ev.Message, 100)),
			)
		}
	}

	// ── REMEDY ────────────────────────────────────────────────────────────────
	if d.Remedy != "" {
		fmt.Printf("\n     %s\n", color.New(color.FgGreen, color.Bold).Sprint("Recommended action:"))
		for _, line := range strings.Split(d.Remedy, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				fmt.Printf("     %s\n", color.HiBlackString(line))
			} else if line != "" {
				fmt.Printf("     %s\n", color.CyanString(line))
			}
		}
	}

	fmt.Println()
}

var nodeTaintsCmd = &cobra.Command{
	Use:   "taints",
	Short: "List all node taints",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("NODE TAINTS — cluster: %s", clusterName)
		findings, err := engine.NodeTaints()
		if err != nil {
			return err
		}
		printer.Findings(findings)
		return nil
	},
}

var nodeTopCmd = &cobra.Command{
	Use:   "top",
	Short: "Show node resource usage with colour-coded thresholds",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		nodes, err := engine.NodeTop()
		if err != nil {
			return fmt.Errorf("node top failed (is metrics-server running?): %w", err)
		}
		fmt.Printf("\n%-44s %8s %8s %10s %10s\n",
			color.CyanString("NODE"), "CPU", "CPU%", "MEMORY", "MEM%")
		fmt.Println(color.HiBlackString("─────────────────────────────────────────────────────────────────────────────"))
		for _, n := range nodes {
			cpuFn := color.GreenString
			memFn := color.GreenString
			if n.CPUPercent > 80 {
				cpuFn = color.RedString
			} else if n.CPUPercent > 60 {
				cpuFn = color.YellowString
			}
			if n.MemPercent > 80 {
				memFn = color.RedString
			} else if n.MemPercent > 60 {
				memFn = color.YellowString
			}
			fmt.Printf("%-44s %8s %8s %10s %10s\n",
				n.Name,
				cpuFn(n.CPUUsage),
				cpuFn(fmt.Sprintf("%.0f%%", n.CPUPercent)),
				memFn(n.MemUsage),
				memFn(fmt.Sprintf("%.0f%%", n.MemPercent)),
			)
		}
		return nil
	},
}

var nodeCordonCmd = &cobra.Command{
	Use:   "cordon [node-name]",
	Short: "Cordon (and optionally drain) a problematic node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		color.Yellow("⚠  Cordoning node: %s", args[0])
		fmt.Print("Confirm? [y/N]: ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
		return engine.CordonNode(args[0], drainNode)
	},
}

var drainNode bool

func init() {
	nodeCmd.AddCommand(nodePressureCmd)
	nodeCmd.AddCommand(nodeTaintsCmd)
	nodeCmd.AddCommand(nodeTopCmd)
	nodeCmd.AddCommand(nodeCordonCmd)
	nodeCordonCmd.Flags().BoolVar(&drainNode, "drain", false, "also drain after cordoning")
	rootCmd.AddCommand(nodeCmd)
}
