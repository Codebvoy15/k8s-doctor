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

var costCmd = &cobra.Command{
	Use:   "cost",
	Short: "Resource waste analysis — find over-provisioned and idle workloads",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		result, err := engine.CostAnalysis(namespace)
		if err != nil {
			return fmt.Errorf("cost analysis failed: %w", err)
		}
		fmt.Printf("\n%s\n\n", color.New(color.FgCyan, color.Bold).Sprint("RESOURCE WASTE ANALYSIS"))
		if len(result.OverProvisioned) > 0 {
			fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Over-provisioned pods (request >> actual usage)"))
			fmt.Printf("  %-44s  %-16s  %-12s  %-12s  %s\n", "POD", "NAMESPACE", "CPU REQ", "MEM REQ", "WASTE")
			fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 92)))
			for _, p := range result.OverProvisioned {
				name := p.Name
				if len(name) > 44 {
					name = name[:41] + "..."
				}
				wasteFn := color.YellowString
				if p.WasteScore > 70 {
					wasteFn = color.RedString
				}
				fmt.Printf("  %-44s  %-16s  %-12s  %-12s  %s\n",
					name, p.Namespace, p.CPURequest, p.MemRequest, wasteFn(fmt.Sprintf("%d/100", p.WasteScore)))
				if p.Recommendation != "" {
					fmt.Printf("  %s %s\n", color.HiBlackString("    ↳"), color.HiBlackString(p.Recommendation))
				}
			}
			fmt.Println()
		}
		if len(result.IdleNamespaces) > 0 {
			fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Idle namespaces (no running pods)"))
			for _, ns := range result.IdleNamespaces {
				fmt.Printf("  %s  %s\n", color.HiBlackString("○"), ns)
			}
			fmt.Println()
		}
		if len(result.UnderutilisedNodes) > 0 {
			fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Under-utilised nodes (<20% CPU and memory)"))
			for _, n := range result.UnderutilisedNodes {
				fmt.Printf("  %s  %-44s  CPU: %s  Mem: %s\n",
					color.HiBlackString("○"), n.Name,
					color.GreenString("%.0f%%", n.CPUPercent),
					color.GreenString("%.0f%%", n.MemPercent))
			}
			fmt.Println()
		}
		fmt.Printf("  %d over-provisioned  |  %d idle namespaces  |  %d under-utilised nodes\n",
			len(result.OverProvisioned), len(result.IdleNamespaces), len(result.UnderutilisedNodes))
		if result.EstimatedWasteCPU != "" {
			fmt.Printf("  Estimated wasted CPU: %s  |  Memory: %s\n",
				color.YellowString(result.EstimatedWasteCPU), color.YellowString(result.EstimatedWasteMemory))
		}
		fmt.Println()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(costCmd)
}
