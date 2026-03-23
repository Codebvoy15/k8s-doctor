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

var nodeCmd = &cobra.Command{
	Use:   "node",
	Short: "Node diagnostics: pressure, taints, top, cordon",
}

var nodePressureCmd = &cobra.Command{
	Use:   "pressure",
	Short: "Check all nodes for memory/disk/PID pressure and NotReady state",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("NODE PRESSURE — cluster: %s", clusterName)
		findings, err := engine.NodePressure()
		if err != nil {
			return err
		}
		printer.Findings(findings)
		printer.RootCauseSummary(findings)
		return nil
	},
}

var nodeTaintsCmd = &cobra.Command{
	Use:   "taints",
	Short: "List all node taints and flag pods missing tolerations",
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
}
