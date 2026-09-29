package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Codebvoy15/k8s-doctor/internal/collect"
	"github.com/Codebvoy15/k8s-doctor/internal/detect"
	"github.com/Codebvoy15/k8s-doctor/internal/model"
	"github.com/Codebvoy15/k8s-doctor/internal/render"
)

var (
	scanFrom      string
	scanSave      string
	scanDetectors []string
	scanMinTier   string
	scanFailOn    string
	scanMax       int
	scanTimeout   time.Duration
)

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Detect problems ranked by impact, with causes linked to symptoms",
	Long: `Snapshot the cluster once (read-only, secret names only), run every detector,
and print findings ranked by impact: IMPACTING > DEGRADED > LATENT > INFO.
Root causes are listed first with the symptoms they explain nested beneath.

  k8s-doctor scan
  k8s-doctor scan --context stage-us-east-1 -n shop -v
  k8s-doctor scan -o json > report.json
  k8s-doctor scan --save snap.json          # keep the snapshot (replay it, attach to a ticket, turn it into a test)
  k8s-doctor scan --from snap.json          # offline: no cluster access needed
  k8s-doctor scan --fail-on impacting       # exit 2 if anything is impacting (cron / CI)
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dets, err := detect.Select(scanDetectors)
		if err != nil {
			return err
		}
		minTier, err := detect.ParseTier(scanMinTier)
		if err != nil {
			return err
		}

		var snap *model.Snapshot
		if scanFrom != "" {
			snap, err = readSnapshot(scanFrom)
		} else {
			snap, err = liveSnapshot(resolveContext(), scanTimeout)
		}
		if err != nil {
			return err
		}
		if scanSave != "" {
			if err := writeJSON(scanSave, snap); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "snapshot saved to %s\n", scanSave)
		}

		rep := detect.Run(snap, dets)
		if outputFmt == "json" {
			b, _ := json.MarshalIndent(rep, "", "  ")
			fmt.Println(string(b))
		} else {
			render.Report(os.Stdout, rep, render.Options{MinTier: minTier, Color: colorOK(), Verbose: verbose, MaxShown: scanMax})
		}
		return failOn(scanFailOn, rep.Counts)
	},
}

// resolveContext: --context, else --cluster (treated as a context name, never
// switched), else the kubeconfig's current context.
func resolveContext() string {
	if kubeContext != "" {
		return kubeContext
	}
	return clusterName
}

func liveSnapshot(kctx string, timeout time.Duration) (*model.Snapshot, error) {
	cfg, err := collect.Config(kubeconfigPath, kctx)
	if err != nil {
		return nil, err
	}
	name := kctx
	if name == "" {
		if _, cur, err := collect.Contexts(kubeconfigPath); err == nil {
			name = cur
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	fmt.Fprintf(os.Stderr, "scanning %s (ns=%s)...\n", name, orAll(namespace))
	return collect.Snapshot(ctx, cfg, name, collect.Options{Namespace: namespace})
}

func readSnapshot(path string) (*model.Snapshot, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s model.Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s is not a k8s-doctor snapshot: %w", path, err)
	}
	if s.SchemaVersion != model.SchemaVersion {
		return nil, fmt.Errorf("%s has snapshot schema %q, this build reads %q", path, s.SchemaVersion, model.SchemaVersion)
	}
	return &s, nil
}

func writeJSON(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600) // snapshots name workloads and secrets: owner-only
}

// failOn returns an exit-code error when findings reach the given tier.
func failOn(tier string, counts map[detect.Tier]int) error {
	if tier == "" {
		return nil
	}
	t, err := detect.ParseTier(tier)
	if err != nil {
		return err
	}
	for ft, n := range counts {
		if n > 0 && ft.Rank() <= t.Rank() {
			fmt.Fprintf(os.Stderr, "fail-on %s: %d %s finding(s)\n", strings.ToLower(tier), n, ft)
			os.Exit(2)
		}
	}
	return nil
}

func colorOK() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func orAll(ns string) string {
	if ns == "" {
		return "all"
	}
	return ns
}

var detectorsCmd = &cobra.Command{
	Use:   "detectors",
	Short: "List the detectors used by scan and fleet",
	RunE: func(cmd *cobra.Command, args []string) error {
		for _, d := range detect.Registry() {
			fmt.Printf("%-22s %s\n", d.ID(), d.Description())
		}
		return nil
	},
}

func init() {
	scanCmd.Flags().StringVar(&scanFrom, "from", "", "analyze a saved snapshot instead of a live cluster")
	scanCmd.Flags().StringVar(&scanSave, "save", "", "also write the snapshot to this file")
	scanCmd.Flags().StringSliceVar(&scanDetectors, "detectors", nil, "only run these detectors (see: k8s-doctor detectors)")
	scanCmd.Flags().StringVar(&scanMinTier, "min-tier", "latent", "hide findings below this tier: impacting|degraded|latent|info")
	scanCmd.Flags().StringVar(&scanFailOn, "fail-on", "", "exit 2 if any finding is at or above this tier")
	scanCmd.Flags().IntVar(&scanMax, "max", 25, "max findings to print")
	scanCmd.Flags().DurationVar(&scanTimeout, "timeout", 3*time.Minute, "time budget for collecting the snapshot")
	rootCmd.AddCommand(scanCmd, detectorsCmd)
}
