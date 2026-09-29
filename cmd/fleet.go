package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Codebvoy15/k8s-doctor/internal/collect"
	"github.com/Codebvoy15/k8s-doctor/internal/detect"
	"github.com/Codebvoy15/k8s-doctor/internal/fleet"
	"github.com/Codebvoy15/k8s-doctor/internal/model"
	"github.com/Codebvoy15/k8s-doctor/internal/render"
)

var (
	fleetContexts  []string
	fleetFile      string
	fleetAll       bool
	fleetMatch     string
	fleetExclude   string
	fleetParallel  int
	fleetTimeout   time.Duration
	fleetDetectors []string
	fleetMinTier   string
	fleetFailOn    string
	fleetMax       int
	fleetSaveDir   string
)

var fleetCmd = &cobra.Command{
	Use:   "fleet",
	Short: "Scan many clusters in parallel and find fleet-wide patterns",
	Long: `Sweep many kube contexts in parallel (read-only), run every detector on each,
and aggregate the same failure across clusters and namespaces into one pattern.

The kubeconfig is never modified: each cluster gets its own client.
A slow or broken cluster is reported with its error and never stops the sweep.

  k8s-doctor fleet --all-contexts
  k8s-doctor fleet --match 'prod' --exclude 'sandbox'
  k8s-doctor fleet --contexts-file clusters.txt --parallel 12 --timeout 90s
  k8s-doctor fleet --contexts stage-us-east-1,prod-eu-west-1 -o json > fleet.json
  k8s-doctor fleet --all-contexts --save-dir /tmp/snaps     # keep every snapshot
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dets, err := detect.Select(fleetDetectors)
		if err != nil {
			return err
		}
		minTier, err := detect.ParseTier(fleetMinTier)
		if err != nil {
			return err
		}
		targets, err := fleetTargets()
		if err != nil {
			return err
		}
		if fleetSaveDir != "" {
			if err := os.MkdirAll(fleetSaveDir, 0o700); err != nil {
				return err
			}
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		collectFn := func(ctx context.Context, kctx string) (*model.Snapshot, error) {
			cfg, err := collect.Config(kubeconfigPath, kctx)
			if err != nil {
				return nil, err
			}
			s, err := collect.Snapshot(ctx, cfg, kctx, collect.Options{Namespace: namespace})
			if err == nil && fleetSaveDir != "" {
				_ = writeJSON(fmt.Sprintf("%s/%s.json", fleetSaveDir, safeName(kctx)), s)
			}
			return s, err
		}
		fmt.Fprintf(os.Stderr, "sweeping %d cluster(s), %d at a time, %s each...\n", len(targets), fleetParallel, fleetTimeout)
		res := fleet.Run(ctx, targets, collectFn, fleet.Options{
			Parallel: fleetParallel, Timeout: fleetTimeout, Detectors: dets,
			Progress: func(done, total int, r fleet.ClusterResult) {
				status := "ok"
				if r.Error != "" {
					status = "ERROR: " + firstLine(r.Error)
				} else if r.Report != nil {
					status = fmt.Sprintf("impacting=%d degraded=%d latent=%d",
						r.Report.Counts[detect.TierImpacting], r.Report.Counts[detect.TierDegraded], r.Report.Counts[detect.TierLatent])
				}
				fmt.Fprintf(os.Stderr, "  [%d/%d] %-40s %6s  %s\n", done, total, r.Cluster, r.Duration, status)
			},
		})

		if outputFmt == "json" {
			b, _ := json.MarshalIndent(res, "", "  ")
			fmt.Println(string(b))
		} else {
			fmt.Fprintln(os.Stderr)
			render.Fleet(os.Stdout, res, render.Options{MinTier: minTier, Color: colorOK(), Verbose: verbose, MaxShown: fleetMax})
		}
		return failOn(fleetFailOn, res.Counts)
	},
}

// fleetTargets resolves which contexts to sweep. Explicit lists win; otherwise
// --all-contexts or --match select from the kubeconfig.
func fleetTargets() ([]string, error) {
	var targets []string
	targets = append(targets, fleetContexts...)
	if fleetFile != "" {
		f, err := os.Open(fleetFile)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line != "" && !strings.HasPrefix(line, "#") {
				targets = append(targets, line)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	if len(targets) == 0 && (fleetAll || fleetMatch != "") {
		all, _, err := collect.Contexts(kubeconfigPath)
		if err != nil {
			return nil, err
		}
		targets = all
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("no clusters selected: use --all-contexts, --match, --contexts or --contexts-file")
	}
	var inc, exc *regexp.Regexp
	var err error
	if fleetMatch != "" {
		if inc, err = regexp.Compile(fleetMatch); err != nil {
			return nil, fmt.Errorf("--match: %w", err)
		}
	}
	if fleetExclude != "" {
		if exc, err = regexp.Compile(fleetExclude); err != nil {
			return nil, fmt.Errorf("--exclude: %w", err)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range targets {
		if seen[t] || (inc != nil && !inc.MatchString(t)) || (exc != nil && exc.MatchString(t)) {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no contexts left after --match/--exclude")
	}
	return out, nil
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, s)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 90 {
		s = s[:87] + "..."
	}
	return s
}

func init() {
	fleetCmd.Flags().StringSliceVar(&fleetContexts, "contexts", nil, "comma-separated kube contexts to sweep")
	fleetCmd.Flags().StringVar(&fleetFile, "contexts-file", "", "file with one kube context per line (# comments allowed)")
	fleetCmd.Flags().BoolVar(&fleetAll, "all-contexts", false, "sweep every context in the kubeconfig")
	fleetCmd.Flags().StringVar(&fleetMatch, "match", "", "only contexts matching this regex")
	fleetCmd.Flags().StringVar(&fleetExclude, "exclude", "", "skip contexts matching this regex")
	fleetCmd.Flags().IntVar(&fleetParallel, "parallel", 8, "clusters scanned at once")
	fleetCmd.Flags().DurationVar(&fleetTimeout, "timeout", 2*time.Minute, "time budget per cluster")
	fleetCmd.Flags().StringSliceVar(&fleetDetectors, "detectors", nil, "only run these detectors")
	fleetCmd.Flags().StringVar(&fleetMinTier, "min-tier", "latent", "hide patterns below this tier")
	fleetCmd.Flags().StringVar(&fleetFailOn, "fail-on", "", "exit 2 if any finding is at or above this tier")
	fleetCmd.Flags().IntVar(&fleetMax, "max", 25, "max patterns to print")
	fleetCmd.Flags().StringVar(&fleetSaveDir, "save-dir", "", "write each cluster's snapshot to this directory")
	rootCmd.AddCommand(fleetCmd)
}
