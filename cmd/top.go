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
	Short: "Who is eating your cluster — pods and nodes by resource consumption",
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

		fmt.Printf("\ntop  cluster=%s  sort=%s  %s\n",
			color.New(color.FgWhite, color.Bold).Sprint(clusterName),
			topSort,
			color.HiBlackString(time.Now().Format("15:04:05")),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 88)))

		fmt.Printf("\nNODES\n")
		fmt.Println(color.HiBlackString(strings.Repeat("─", 88)))
		fmt.Printf("%-44s  %10s  %6s  %10s  %6s\n",
			color.HiBlackString("name"),
			color.HiBlackString("cpu"),
			color.HiBlackString("cpu%"),
			color.HiBlackString("memory"),
			color.HiBlackString("mem%"),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 88)))
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
			fmt.Printf("%-44s  %10s  %6s  %10s  %6s\n",
				n.Name,
				cpuFn(n.CPUUsage),
				cpuFn(fmt.Sprintf("%.0f%%", n.CPUPercent)),
				memFn(n.MemUsage),
				memFn(fmt.Sprintf("%.0f%%", n.MemPercent)),
			)
		}

		fmt.Printf("\nTOP PODS  by %s\n", topSort)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 88)))
		fmt.Printf("%-48s  %-20s  %10s  %10s\n",
			color.HiBlackString("pod"),
			color.HiBlackString("namespace"),
			color.HiBlackString("cpu"),
			color.HiBlackString("memory"),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 88)))
		for i, p := range result.Pods {
			if i >= topLimit {
				break
			}
			fmt.Printf("%-48s  %-20s  %10s  %10s\n",
				truncateStr(p.Name, 48),
				p.Namespace,
				color.YellowString(p.CPUUsage),
				color.YellowString(p.MemUsage),
			)
		}

		if len(result.NoisyNeighbours) > 0 {
			fmt.Printf("\nNOISY NEIGHBOURS\n")
			fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
			for _, n := range result.NoisyNeighbours {
				fmt.Printf("  %-48s  ns=%-20s  cpu=%-10s  mem=%s\n",
					n.PodName, n.Namespace,
					color.YellowString(n.CPUUsage),
					color.YellowString(n.MemUsage),
				)
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
