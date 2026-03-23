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
	diffDeep     bool
)

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "What changed, when, who changed it, and did it cause your problem?",
	Long: `Detects exact field-level changes — images, replicas, env vars, config, resources.

Two modes:

  LIVE (default) — shows recently touched resources using managedFields timestamps:
    ./k8s-doctor diff --cluster prod-us-east-1 --window 30m

  SNAPSHOT — save state before a change, compare after (gives exact before/after values):
    ./k8s-doctor diff --save /tmp/before.json --cluster prod-us-east-1
    # make your change / wait
    ./k8s-doctor diff --load /tmp/before.json --cluster prod-us-east-1

What it detects:
  - Image changes        (container[app].image: nginx:1.24 → nginx:1.25)
  - Replica changes      (replicas: 3 → 1)
  - Env var changes      (container[app].env.DB_HOST: old → new)
  - Resource limit changes
  - ConfigMap data changes (key by key)
  - Service type changes
  - New or deleted resources
  - Correlation with active pod faults
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
			color.Cyan("→ Capturing deep snapshot to %s...", diffSavePath)
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
			color.Green("✓ Snapshot saved — %d resources captured", snap.ResourceCount)
			color.HiBlack("  Captures: images, replicas, env vars, resource limits, configmap data")
			color.HiBlack("  Run diff: ./k8s-doctor diff --load %s --cluster %s", diffSavePath, clusterName)
			return nil
		}

		// LOAD + deep diff mode
		if diffLoadPath != "" {
			color.Cyan("→ Loading baseline from %s...", diffLoadPath)
			b, err := os.ReadFile(diffLoadPath)
			if err != nil {
				return fmt.Errorf("could not read snapshot: %w", err)
			}
			var baseline diag.DeepStateSnapshot
			if err := json.Unmarshal(b, &baseline); err != nil {
				return fmt.Errorf("invalid snapshot file: %w", err)
			}
			age := time.Since(baseline.CapturedAt)
			color.HiBlack("  Baseline captured %s ago", age.Round(time.Second))

			diffs, err := engine.DeepSnapshotDiff(&baseline)
			if err != nil {
				return err
			}
			printDeepDiffs(diffs, fmt.Sprintf("since snapshot (%s ago)", age.Round(time.Second)))
			return nil
		}

		// LIVE mode — show recently changed resources with current values
		window, err := time.ParseDuration(diffWindow)
		if err != nil {
			return fmt.Errorf("invalid --window: use 30m, 1h, etc.")
		}

		diffs, err := engine.LiveDeepDiff(window)
		if err != nil {
			return fmt.Errorf("diff failed: %w", err)
		}
		printDeepDiffs(diffs, fmt.Sprintf("last %s", diffWindow))

		if len(diffs) > 0 {
			fmt.Printf("  %s\n",
				color.HiBlackString("Tip: use --save before deploys and --load after to see exact before/after values"))
		}
		return nil
	},
}

func printDeepDiffs(diffs []diag.DeepDiffEntry, windowLabel string) {
	fmt.Printf("\n%s\n\n",
		color.New(color.FgCyan, color.Bold).Sprintf("DIFF — changes %s", windowLabel))

	if len(diffs) == 0 {
		fmt.Println(color.GreenString("  No changes detected in this window."))
		return
	}

	// Show correlated changes first — these are the most important
	var correlated []diag.DeepDiffEntry
	for _, d := range diffs {
		if d.CorrelatedFault != "" {
			correlated = append(correlated, d)
		}
	}

	if len(correlated) > 0 {
		fmt.Printf("  %s\n\n",
			color.New(color.FgRed, color.Bold).Sprintf("⚠  Changes correlated with active faults (%d):", len(correlated)))
		for _, d := range correlated {
			fmt.Printf("  %s  %s/%s  %s\n",
				color.RedString("●"),
				d.Kind,
				color.New(color.Bold).Sprint(d.Name),
				color.HiBlackString("(ns: %s)", d.Namespace),
			)
			fmt.Printf("    %s  %s\n", color.HiBlackString("field:   "), color.YellowString(d.Field))
			if d.OldValue != "" && !strings.Contains(d.OldValue, "use --save") {
				fmt.Printf("    %s  %s\n", color.HiBlackString("before:  "), color.RedString(d.OldValue))
			}
			fmt.Printf("    %s  %s\n", color.HiBlackString("after:   "), color.YellowString(d.NewValue))
			fmt.Printf("    %s  %s\n", color.HiBlackString("by:      "), color.CyanString(d.ChangedBy))
			fmt.Printf("    %s  %s\n", color.HiBlackString("at:      "), d.Timestamp.Format("2006-01-02 15:04:05"))
			fmt.Printf("    %s  %s\n", color.RedString("fault:   "), color.RedString(d.CorrelatedFault))
			if d.Mitigation != "" {
				fmt.Printf("    %s  %s\n", color.GreenString("fix:     "), color.GreenString(d.Mitigation))
			}
			if d.Risk != "" {
				fmt.Printf("    %s  %s\n", color.YellowString("risk:    "), d.Risk)
			}
			fmt.Println()
		}
	}

	// All changes grouped by resource
	fmt.Printf("  %s\n", color.New(color.Bold).Sprintf("All changes (%d):", len(diffs)))
	fmt.Println()

	// Group by kind/name
	grouped := map[string][]diag.DeepDiffEntry{}
	order := []string{}
	for _, d := range diffs {
		key := d.Kind + "/" + d.Namespace + "/" + d.Name
		if _, exists := grouped[key]; !exists {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], d)
	}

	for _, key := range order {
		entries := grouped[key]
		first := entries[0]
		hasFault := first.CorrelatedFault != ""

		prefix := color.HiBlackString("  ○")
		if hasFault {
			prefix = color.RedString("  ●")
		}

		fmt.Printf("%s  %s  %s  %s  %s\n",
			prefix,
			color.CyanString(first.Kind),
			color.New(color.Bold).Sprint(first.Name),
			color.HiBlackString("ns:"+first.Namespace),
			color.HiBlackString("by: "+first.ChangedBy),
		)

		for _, d := range entries {
			oldPart := ""
			if d.OldValue != "" && !strings.Contains(d.OldValue, "use --save") {
				oldPart = color.RedString(truncateStr(d.OldValue, 50)) + " → "
			}
			fmt.Printf("      %-45s  %s%s\n",
				color.YellowString(d.Field),
				oldPart,
				color.GreenString(truncateStr(d.NewValue, 60)),
			)
			if d.Risk != "" {
				fmt.Printf("      %s %s\n",
					color.HiBlackString("↳"),
					color.HiBlackString(d.Risk),
				)
			}
		}
		fmt.Println()
	}

	fmt.Printf("  Total: %d field change(s) across %d resource(s) | %d correlated with faults\n\n",
		len(diffs), len(grouped), len(correlated))
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func init() {
	diffCmd.Flags().StringVar(&diffWindow, "window", "30m", "live diff window (e.g. 30m, 1h, 2h)")
	diffCmd.Flags().StringVar(&diffSavePath, "save", "", "save deep snapshot to file (captures images, env, config)")
	diffCmd.Flags().StringVar(&diffLoadPath, "load", "", "load baseline snapshot and show exact before/after")
	rootCmd.AddCommand(diffCmd)
}
