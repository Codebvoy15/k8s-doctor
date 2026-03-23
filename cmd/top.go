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

var topCmd = &cobra.Command{
	Use:   "top",
	Short: "Who is eating your cluster — pods and nodes sorted by actual consumption",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		result, err := engine.TopConsumers(topSort, topLimit)
		if err != nil {
			return fmt.Errorf("top failed (is metrics-server running?): %w", err)
		}
		fmt.Printf("\n  %s\n", color.New(color.Bold).Sprint("Nodes"))
		fmt.Printf("  %-44s  %10s  %10s  %10s  %10s\n", "NAME", "CPU", "CPU%", "MEMORY", "MEM%")
		fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 88)))
		for _, n := range result.Nodes {
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
			fmt.Printf("  %-44s  %10s  %10s  %10s  %10s\n",
				n.Name, cpuFn(n.CPUUsage), cpuFn(fmt.Sprintf("%.0f%%", n.CPUPercent)),
				memFn(n.MemUsage), memFn(fmt.Sprintf("%.0f%%", n.MemPercent)))
		}
		fmt.Printf("\n  %s\n", color.New(color.Bold).Sprint("Top pods by "+topSort))
		fmt.Printf("  %-48s  %-20s  %10s  %10s\n", "POD", "NAMESPACE", "CPU", "MEMORY")
		fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 92)))
		for i, p := range result.Pods {
			if i >= topLimit {
				break
			}
			name := p.Name
			if len(name) > 48 {
				name = name[:45] + "..."
			}
			fmt.Printf("  %-48s  %-20s  %10s  %10s\n",
				name, p.Namespace, color.YellowString(p.CPUUsage), color.YellowString(p.MemUsage))
		}
		if len(result.NoisyNeighbours) > 0 {
			fmt.Printf("\n  %s\n", color.New(color.FgYellow, color.Bold).Sprint("Noisy neighbours:"))
			for _, n := range result.NoisyNeighbours {
				fmt.Printf("  %s  %s in ns/%s — CPU: %s  Mem: %s\n",
					color.YellowString("◐"), color.New(color.Bold).Sprint(n.PodName),
					n.Namespace, color.YellowString(n.CPUUsage), color.YellowString(n.MemUsage))
			}
		}
		fmt.Println()
		return nil
	},
}

var (
	topSort  string
	topLimit int
)

func init() {
	topCmd.Flags().StringVar(&topSort, "sort", "memory", "sort by: memory | cpu")
	topCmd.Flags().IntVar(&topLimit, "limit", 20, "number of pods to show")
	rootCmd.AddCommand(topCmd)
}
