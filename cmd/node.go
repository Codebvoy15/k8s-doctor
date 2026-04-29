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

		diagnoses, err := engine.NodeDiagnoseAll()
		if err != nil {
			// fallback to old NodePressure if NodeDiagnoseAll not available
			printer := output.NewPrinter(outputFmt)
			printer.Header("node pressure  cluster=%s", clusterName)
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

		fmt.Printf("\nnode pressure  cluster=%s  %s\n",
			color.New(color.FgWhite, color.Bold).Sprint(clusterName),
			color.HiBlackString(time.Now().Format("15:04:05")),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))

		if notReady == 0 {
			fmt.Printf("\nnodes  %s\n\n",
				color.GreenString("all %d healthy", len(diagnoses)),
			)
			return nil
		}

		fmt.Printf("\nnodes  %s\n\n",
			color.RedString("%d/%d not ready", notReady, len(diagnoses)),
		)

		for _, d := range diagnoses {
			printNodeDiagnosis(d)
		}

		return nil
	},
}

func printNodeDiagnosis(d diag.NodeDiagnosis) {
	// healthy nodes — single line
	if d.Ready && d.Reason == "Healthy" {
		fmt.Printf("  %s  %s\n",
			color.GreenString("ok  "),
			color.HiBlackString(d.NodeName),
		)
		return
	}

	// separator between problem nodes
	fmt.Println()

	// node name + status
	statusColor := color.RedString
	if d.Severity == "WARNING" {
		statusColor = color.YellowString
	}

	fmt.Printf("%s\n", color.New(color.Bold).Sprint(d.NodeName))

	// instance metadata — one line
	meta := []string{}
	if d.InstanceType != "" {
		meta = append(meta, d.InstanceType)
	}
	if d.AMIFamily != "" {
		meta = append(meta, strings.ToLower(d.AMIFamily))
	}
	if d.IsKarpenter {
		meta = append(meta, "karpenter")
	}
	if d.IsSpot {
		meta = append(meta, "spot")
	}
	if d.KernelVersion != "" {
		meta = append(meta, "kernel="+d.KernelVersion)
	}
	if len(meta) > 0 {
		fmt.Printf("  %-12s %s\n", "instance", color.HiBlackString(strings.Join(meta, "  ")))
	}

	// status
	if !d.Ready && d.NotReadyFor != "" {
		fmt.Printf("  %-12s %s\n", "status", statusColor("not ready (%s)", d.NotReadyFor))
	}

	// reason
	if d.Reason != "" && d.Reason != "Healthy" {
		fmt.Printf("  %-12s %s\n", "reason", statusColor(d.Reason))
	}

	// pressure conditions
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
		fmt.Printf("  %-12s %s\n", "conditions", strings.Join(pressures, "  "))
	}

	// root cause
	if d.RootCause != "" {
		// wrap long lines
		words := strings.Fields(d.RootCause)
		line := ""
		first := true
		for _, word := range words {
			if len(line)+len(word)+1 > 58 {
				if first {
					fmt.Printf("  %-12s %s\n", "root cause", line)
					first = false
				} else {
					fmt.Printf("  %-12s %s\n", "", line)
				}
				line = word
			} else {
				if line == "" {
					line = word
				} else {
					line += " " + word
				}
			}
		}
		if line != "" {
			if first {
				fmt.Printf("  %-12s %s\n", "root cause", line)
			} else {
				fmt.Printf("  %-12s %s\n", "", line)
			}
		}
	}

	// evidence
	for _, ev := range d.Evidence {
		fmt.Printf("  %-12s %s\n", "evidence", color.HiBlackString(ev))
	}

	// OOMKilled pods
	for _, pod := range d.OOMKilledPods {
		fmt.Printf("  %-12s %s\n", "oomkilled", color.RedString(pod))
	}

	// top pods by memory — only shown for memory/PID pressure
	if len(d.TopPods) > 0 && (d.MemoryPressure || d.PIDPressure) {
		fmt.Printf("  %-12s %s\n", "top pods", color.HiBlackString("by memory request"))
		fmt.Printf("  %s\n", color.HiBlackString(strings.Repeat("─", 68)))
		fmt.Printf("  %-40s  %-16s  %-8s  %s\n",
			color.HiBlackString("pod"),
			color.HiBlackString("namespace"),
			color.HiBlackString("mem req"),
			color.HiBlackString("restarts"),
		)
		for i, pod := range d.TopPods {
			if i >= 5 {
				fmt.Printf("  %s\n", color.HiBlackString("... %d more", len(d.TopPods)-5))
				break
			}
			oomSuffix := ""
			if pod.OOMKilled {
				oomSuffix = color.RedString("  OOMKilled")
			}
			restartFn := color.HiBlackString
			if pod.Restarts > 10 {
				restartFn = color.RedString
			} else if pod.Restarts > 5 {
				restartFn = color.YellowString
			}
			fmt.Printf("  %-40s  %-16s  %-8s  %s%s\n",
				truncateStr(pod.PodName, 40),
				truncateStr(pod.Namespace, 16),
				color.YellowString(pod.MemRequest),
				restartFn(fmt.Sprintf("%d", pod.Restarts)),
				oomSuffix,
			)
		}
		fmt.Printf("  %s\n", color.HiBlackString(strings.Repeat("─", 68)))
	}

	// disk usage
	if d.DiskUsage != nil {
		usageFn := color.GreenString
		if d.DiskUsage.UsedPercent > 90 {
			usageFn = color.RedString
		} else if d.DiskUsage.UsedPercent > 75 {
			usageFn = color.YellowString
		}
		fmt.Printf("  %-12s %s\n", "disk",
			usageFn("%.0f%% used  %.1f/%.1f GB",
				d.DiskUsage.UsedPercent,
				d.DiskUsage.UsedGB,
				d.DiskUsage.TotalGB,
			),
		)
		for _, dir := range d.DiskUsage.TopDirs {
			fmt.Printf("  %-12s %-30s  %.1f GB\n", "", dir.Path, dir.SizeGB)
		}
	}

	// recent events
	if len(d.RecentEvents) > 0 {
		for i, ev := range d.RecentEvents {
			if i >= 3 {
				break
			}
			label := "event"
			if i > 0 {
				label = ""
			}
			fmt.Printf("  %-12s %s  [x%d] %s: %s\n",
				label,
				color.HiBlackString(ev.Time.Format("15:04:05")),
				ev.Count,
				color.YellowString(ev.Reason),
				color.HiBlackString(truncateStr(ev.Message, 80)),
			)
		}
	}

	// fix
	if d.Remedy != "" {
		lines := strings.Split(d.Remedy, "\n")
		for i, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			label := "fix"
			if i > 0 {
				label = ""
			}
			if strings.HasPrefix(line, "#") {
				fmt.Printf("  %-12s %s\n", label, color.HiBlackString(line))
			} else {
				fmt.Printf("  %-12s %s\n", label, color.CyanString(line))
			}
		}
	}
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
		printer.Header("node taints  cluster=%s", clusterName)
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
	Short: "Show node resource usage",
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

		fmt.Printf("\nnode top  cluster=%s  %s\n",
			color.New(color.FgWhite, color.Bold).Sprint(clusterName),
			color.HiBlackString(time.Now().Format("15:04:05")),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
		fmt.Printf("\n%-44s  %8s  %6s  %10s  %6s\n",
			color.HiBlackString("node"),
			color.HiBlackString("cpu"),
			color.HiBlackString("cpu%"),
			color.HiBlackString("memory"),
			color.HiBlackString("mem%"),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))

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
			fmt.Printf("%-44s  %8s  %6s  %10s  %6s\n",
				n.Name,
				cpuFn(n.CPUUsage),
				cpuFn(fmt.Sprintf("%.0f%%", n.CPUPercent)),
				memFn(n.MemUsage),
				memFn(fmt.Sprintf("%.0f%%", n.MemPercent)),
			)
		}
		fmt.Println()
		return nil
	},
}

var nodeCordonCmd = &cobra.Command{
	Use:   "cordon [node-name]",
	Short: "Cordon (and optionally drain) a node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		fmt.Printf("cordon  node=%s\n", args[0])
		fmt.Print("confirm [y/N]: ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("aborted")
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
