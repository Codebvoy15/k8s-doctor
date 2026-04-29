package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
	"github.com/Codebvoy15/k8s-doctor/internal/output"
)

var predictCmd = &cobra.Command{
	Use:   "predict",
	Short: "Proactive risk analysis — find problems before they happen",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}

		printer.Header("predict  cluster=%s  ns=%s", clusterName, nsDisplay())

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
			printer.Section(fmt.Sprintf("critical risks (%d)", len(critical)))
			printer.Findings(critical)
		}
		if len(warning) > 0 {
			printer.Section(fmt.Sprintf("warnings (%d)", len(warning)))
			printer.Findings(warning)
		}
		if len(info) > 0 {
			printer.Section(fmt.Sprintf("observations (%d)", len(info)))
			printer.Findings(info)
		}

		if len(critical) == 0 && len(warning) == 0 {
			fmt.Printf("\n  no predictive risks detected\n\n")
		}

		fmt.Println()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(predictCmd)
}
