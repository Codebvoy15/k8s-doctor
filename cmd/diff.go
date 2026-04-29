package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/diag"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var (
	diffSavePath string
	diffLoadPath string
	diffWindow   string
)

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "What changed, when, who changed it — exact field-level changes",
	Long: `Detects field-level changes — images, replicas, env vars, config, resources.

Live mode (default):
  ./k8s-doctor diff --window 30m

Snapshot mode (exact before/after values):
  ./k8s-doctor diff --save /tmp/before.json
  # make your change
  ./k8s-doctor diff --load /tmp/before.json
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}

		// SAVE mode
		if diffSavePath != "" {
			fmt.Fprintf(os.Stderr, "capturing snapshot to %s...\n", diffSavePath)
			snap, err := engine.CaptureDeepSnapshot()
			if err != nil {
				return err
			}
			b, err := json.MarshalIndent(snap, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(diffSavePath, b, 0600); err != nil {
				return err
			}
			fmt.Printf("snapshot  saved  %d resources  %s\n",
				snap.ResourceCount, diffSavePath)
			fmt.Printf("next      ./k8s-doctor diff --load %s\n\n", diffSavePath)
			return nil
		}

		// LOAD mode
		if diffLoadPath != "" {
			fmt.Fprintf(os.Stderr, "loading baseline from %s...\n", diffLoadPath)
			b, err := os.ReadFile(diffLoadPath)
			if err != nil {
				return fmt.Errorf("could not read snapshot: %w", err)
			}
			var baseline diag.DeepStateSnapshot
			if err := json.Unmarshal(b, &baseline); err != nil {
				return fmt.Errorf("invalid snapshot file: %w", err)
			}
			age := time.Since(baseline.CapturedAt).Round(time.Second)
			diffs, err := engine.DeepSnapshotDiff(&baseline)
			if err != nil {
				return err
			}
			printDiffs(diffs, fmt.Sprintf("since snapshot (%s ago)", age))
			return nil
		}

		// LIVE mode
		window, err := time.ParseDuration(diffWindow)
		if err != nil {
			return fmt.Errorf("invalid --window: use 30m, 1h, etc.")
		}
		diffs, err := engine.LiveDeepDiff(window)
		if err != nil {
			return fmt.Errorf("diff failed: %w", err)
		}
		printDiffs(diffs, fmt.Sprintf("last %s", diffWindow))
		if len(diffs) > 0 {
			fmt.Printf("tip  use --save before deploys and --load after for exact before/after values\n\n")
		}
		return nil
	},
}

func printDiffs(diffs []diag.DeepDiffEntry, windowLabel string) {
	fmt.Printf("\ndiff  cluster=%s  %s  %s\n",
		color.New(color.FgWhite, color.Bold).Sprint(clusterName),
		windowLabel,
		color.HiBlackString(time.Now().Format("15:04:05")),
	)
	fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))

	if len(diffs) == 0 {
		fmt.Printf("\n  no changes detected\n\n")
		return
	}

	// Correlated changes first
	var correlated []diag.DeepDiffEntry
	for _, d := range diffs {
		if d.CorrelatedFault != "" {
			correlated = append(correlated, d)
		}
	}

	if len(correlated) > 0 {
		fmt.Printf("\nCORRELATED WITH FAULTS  %s\n",
			color.RedString("%d changes", len(correlated)))
		fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
		for _, d := range correlated {
			fmt.Printf("  %s/%s  ns=%s  by %s\n",
				d.Kind, color.New(color.Bold).Sprint(d.Name),
				d.Namespace,
				color.HiBlackString(d.ChangedBy),
			)
			fmt.Printf("  field   %s\n", color.HiBlackString(d.Field))
			if d.OldValue != "" && !strings.Contains(d.OldValue, "use --save") {
				fmt.Printf("  before  %s\n", color.HiBlackString(truncateStr(d.OldValue, 60)))
			}
			fmt.Printf("  after   %s\n", truncateStr(d.NewValue, 60))
			fmt.Printf("  fault   %s\n", color.RedString(d.CorrelatedFault))
			if d.Mitigation != "" {
				fmt.Printf("  fix     %s\n", color.CyanString(d.Mitigation))
			}
			fmt.Println()
		}
	}

	// All changes grouped by resource
	grouped := map[string][]diag.DeepDiffEntry{}
	order := []string{}
	for _, d := range diffs {
		key := d.Kind + "/" + d.Namespace + "/" + d.Name
		if _, exists := grouped[key]; !exists {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], d)
	}

	fmt.Printf("\nALL CHANGES  %s\n", color.HiBlackString("%d field(s) across %d resource(s)", len(diffs), len(grouped)))
	fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))

	for _, key := range order {
		entries := grouped[key]
		first := entries[0]
		hasFault := first.CorrelatedFault != ""

		label := "  "
		if hasFault {
			label = color.RedString("! ")
		}

		fmt.Printf("%s%s/%s  ns=%s  by %s  at %s\n",
			label,
			first.Kind,
			color.New(color.Bold).Sprint(first.Name),
			color.HiBlackString(first.Namespace),
			color.HiBlackString(first.ChangedBy),
			color.HiBlackString(first.Timestamp.Format("15:04:05")),
		)

		for _, d := range entries {
			oldPart := ""
			if d.OldValue != "" && !strings.Contains(d.OldValue, "use --save") {
				oldPart = color.HiBlackString(truncateStr(d.OldValue, 40)) + " -> "
			}
			fmt.Printf("    %-40s  %s%s\n",
				color.HiBlackString(d.Field),
				oldPart,
				truncateStr(d.NewValue, 40),
			)
			if d.Risk != "" {
				fmt.Printf("    risk  %s\n", color.YellowString(d.Risk))
			}
		}
		fmt.Println()
	}
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func init() {
	diffCmd.Flags().StringVar(&diffWindow, "window", "30m", "live diff window (e.g. 30m, 1h, 2h)")
	diffCmd.Flags().StringVar(&diffSavePath, "save", "", "save snapshot to file")
	diffCmd.Flags().StringVar(&diffLoadPath, "load", "", "load baseline snapshot for exact diff")
	rootCmd.AddCommand(diffCmd)
}
