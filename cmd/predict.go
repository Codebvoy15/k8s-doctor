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

var predictCmd = &cobra.Command{
	Use:   "predict",
	Short: "Detect potential problems before they happen — proactive risk analysis",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("PREDICTIVE RISK ANALYSIS — cluster: %s | ns: %s", clusterName, nsDisplay())
		findings, err := engine.PredictRisks()
		if err != nil {
			return fmt.Errorf("prediction failed: %w", err)
		}
		var critical, warning, info []diag.Finding
		for _, f := range findings {
			switch f.Severity {
			case diag.SeverityCritical:
				critical = append(critical, f)
			case diag.SeverityWarning:
				warning = append(warning, f)
			default:
				info = append(info, f)
			}
		}
		if len(critical) > 0 {
			printer.Section(fmt.Sprintf("Critical risks (%d)", len(critical)))
			printer.Findings(critical)
		}
		if len(warning) > 0 {
			printer.Section(fmt.Sprintf("Warnings (%d)", len(warning)))
			printer.Findings(warning)
		}
		if len(info) > 0 {
			printer.Section(fmt.Sprintf("Observations (%d)", len(info)))
			printer.Findings(info)
		}
		if len(critical) == 0 && len(warning) == 0 {
			fmt.Printf("\n  %s  No predictive risks detected.\n\n", color.GreenString("✓"))
			return nil
		}
		fmt.Printf("\n  %s\n", color.New(color.FgYellow, color.Bold).Sprint("Risk summary:"))
		if len(critical) > 0 {
			fmt.Printf("  %s  %d critical risk(s)\n", color.RedString("●"), len(critical))
		}
		if len(warning) > 0 {
			fmt.Printf("  %s  %d warning(s)\n", color.YellowString("◐"), len(warning))
		}
		shown := 0
		for _, f := range critical {
			if shown >= 3 {
				break
			}
			fmt.Printf("\n  %s  %s\n    %s\n    %s %s\n",
				color.RedString("●"),
				color.New(color.Bold).Sprint(f.Title),
				color.HiBlackString(f.Detail),
				color.GreenString("→"), color.GreenString(f.Remedy),
			)
			shown++
		}
		fmt.Println()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(predictCmd)
}
