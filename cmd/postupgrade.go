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
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/Codebvoy15/k8s-doctor/internal/collect"
	"github.com/Codebvoy15/k8s-doctor/internal/render"
	"github.com/Codebvoy15/k8s-doctor/internal/upgrade"
)

var (
	puTarget    string
	puKubeProxy string
	puCoreDNS   string
	puVPCCNI    string
	puFile      string
	puMatch     string
	puExclude   string
	puParallel  int
	puTimeout   time.Duration
	puSaveDir   string
	puFrom      []string
	puFailOn    string
)

var postUpgradeCmd = &cobra.Command{
	Use:     "post-upgrade [cluster ...]",
	Aliases: []string{"upgrade-check"},
	Short:   "Verify an EKS upgrade finished: control plane, node skew (with why), kube-proxy/CoreDNS/VPC CNI",
	Long: `Check that clusters finished upgrading to a Kubernetes minor (read-only):

  control plane    on the target minor
  node versions    every kubelet on the control plane's minor; for each group of
                   lagging nodes, WHY it has not rolled: managed nodegroup not
                   updated, Karpenter EC2NodeClass still resolving old AMIs,
                   drift blocked by a NodePool budget / do-not-disrupt / PDB,
                   Fargate pods not recreated, self-managed ASG on an old AMI
  node health      NotReady and leftover cordoned nodes
  kube-proxy       latest EKS add-on version for the target, minor matches the control plane
  CoreDNS          latest EKS add-on version for the target, rollout finished
  VPC CNI          latest version, aws-vpc-cni-init in step with aws-node
  kube-system pods nothing crash-looping or unready after the upgrade

Clusters are kube contexts or bare EKS cluster names (matched against context
ARNs). The kubeconfig is never modified.

  k8s-doctor post-upgrade                                   # current context
  k8s-doctor post-upgrade atp-mdmhub-sbx-amer rd-bcg-sbx-01  # by cluster name
  k8s-doctor post-upgrade --contexts-file sbx.txt --parallel 10
  k8s-doctor post-upgrade --match 'sbx' --target 1.36
  k8s-doctor post-upgrade rd-bcg-sbx-01 -o markdown > upgrade-report.md
  k8s-doctor post-upgrade --coredns v1.14.3-eksbuild.4      # AWS shipped a newer build
  k8s-doctor post-upgrade --save-dir ./facts ...            # keep the raw facts (ticket / replay)
  k8s-doctor post-upgrade --from ./facts/rd-bcg-sbx-01.json # offline, no cluster access
  k8s-doctor post-upgrade --match sbx --fail-on fail        # exit 2 if any check fails (CI)
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		minor, err := upgrade.ParseMinor(puTarget)
		if err != nil {
			return fmt.Errorf("--target: %w", err)
		}
		t := upgrade.DefaultTargets(minor)
		overridden := false
		for _, o := range []struct {
			flag, val string
			dst       *string
		}{{"kube-proxy", puKubeProxy, &t.KubeProxy}, {"coredns", puCoreDNS, &t.CoreDNS}, {"vpc-cni", puVPCCNI, &t.VPCCNI}} {
			if o.val != "" {
				*o.dst = o.val
				overridden = true
			}
		}
		if overridden {
			t.Source += ", with flag overrides"
		}

		var results []upgrade.Result
		if len(puFrom) > 0 {
			for _, path := range puFrom {
				f, err := readFacts(path)
				if err != nil {
					return err
				}
				results = append(results, upgrade.Analyze(f, t))
			}
		} else {
			targets, err := postUpgradeTargets(args)
			if err != nil {
				return err
			}
			results = runPostUpgrade(targets, t)
		}

		switch outputFmt {
		case "json":
			b, _ := json.MarshalIndent(results, "", "  ")
			fmt.Println(string(b))
		case "markdown":
			render.UpgradeMarkdown(os.Stdout, results)
		default:
			render.Upgrade(os.Stdout, results, render.Options{Color: colorOK(), Verbose: verbose})
		}
		return postUpgradeFailOn(puFailOn, results)
	},
}

// postUpgradeTargets: positional names and --contexts-file are resolved against
// the kubeconfig (bare EKS names match context ARNs); --match/--exclude filter
// all contexts; with none of these, --context or the current context.
func postUpgradeTargets(args []string) ([]string, error) {
	names := append([]string{}, args...)
	if puFile != "" {
		f, err := os.Open(puFile)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line != "" && !strings.HasPrefix(line, "#") {
				names = append(names, line)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	all, current, err := collect.Contexts(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	var out []string
	switch {
	case len(names) > 0:
		if out, err = upgrade.ResolveContexts(names, all); err != nil {
			return nil, err
		}
	case puMatch != "":
		out = all
	case resolveContext() != "":
		out = []string{resolveContext()}
	case current != "":
		out = []string{current}
	default:
		return nil, fmt.Errorf("no cluster selected and no current context: pass cluster names, --context, --match or --contexts-file")
	}
	var inc, exc *regexp.Regexp
	if puMatch != "" {
		if inc, err = regexp.Compile(puMatch); err != nil {
			return nil, fmt.Errorf("--match: %w", err)
		}
	}
	if puExclude != "" {
		if exc, err = regexp.Compile(puExclude); err != nil {
			return nil, fmt.Errorf("--exclude: %w", err)
		}
	}
	var filtered []string
	for _, c := range out {
		if (inc != nil && !inc.MatchString(c)) || (exc != nil && exc.MatchString(c)) {
			continue
		}
		filtered = append(filtered, c)
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("no contexts left after --match/--exclude")
	}
	return filtered, nil
}

// runPostUpgrade checks clusters with bounded parallelism; one slow or broken
// cluster is reported with its error and never blocks the others.
func runPostUpgrade(targets []string, t upgrade.Targets) []upgrade.Result {
	if puParallel <= 0 {
		puParallel = 8
	}
	if puSaveDir != "" {
		if err := os.MkdirAll(puSaveDir, 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "--save-dir: %v (facts will not be saved)\n", err)
			puSaveDir = ""
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	results := make([]upgrade.Result, len(targets))
	sem := make(chan struct{}, puParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	if len(targets) > 1 {
		fmt.Fprintf(os.Stderr, "checking %d cluster(s), %d at a time, %s each...\n", len(targets), puParallel, puTimeout)
	}
	for i, kctx := range targets {
		wg.Add(1)
		go func(i int, kctx string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			r := checkOne(ctx, kctx, t)
			results[i] = r
			mu.Lock()
			done++
			fmt.Fprintf(os.Stderr, "  [%d/%d] %-32s %6s  %s\n", done, len(targets), upgrade.ShortName(kctx),
				time.Since(start).Round(100*time.Millisecond), progressNote(r))
			mu.Unlock()
		}(i, kctx)
	}
	wg.Wait()
	if len(targets) > 1 {
		fmt.Fprintln(os.Stderr)
	}
	return results
}

func checkOne(parent context.Context, kctx string, t upgrade.Targets) upgrade.Result {
	ctx, cancel := context.WithTimeout(parent, puTimeout)
	defer cancel()
	cfg, err := collect.Config(kubeconfigPath, kctx)
	if err != nil {
		return upgrade.Result{SchemaVersion: upgrade.ResultSchemaVersion, Cluster: kctx, Target: t, Status: upgrade.StatusFail, Error: err.Error()}
	}
	cfg.Timeout = puTimeout
	f, err := upgrade.Collect(ctx, cfg, kctx)
	if err != nil {
		return upgrade.Result{SchemaVersion: upgrade.ResultSchemaVersion, Cluster: kctx, Target: t, Status: upgrade.StatusFail, Error: firstLine(err.Error())}
	}
	if puSaveDir != "" {
		_ = writeJSON(fmt.Sprintf("%s/%s.json", puSaveDir, safeName(upgrade.ShortName(kctx))), f)
	}
	return upgrade.Analyze(f, t)
}

func progressNote(r upgrade.Result) string {
	if r.Error != "" {
		return "ERROR: " + firstLine(r.Error)
	}
	var bad []string
	for _, c := range r.Checks {
		if c.Status == upgrade.StatusFail || c.Status == upgrade.StatusWarn {
			bad = append(bad, strings.ToLower(string(c.Status))+":"+c.ID)
		}
	}
	if len(bad) == 0 {
		return "clean"
	}
	return strings.Join(bad, " ")
}

func readFacts(path string) (*upgrade.Facts, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f upgrade.Facts
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s is not a post-upgrade facts file: %w", path, err)
	}
	if f.SchemaVersion != upgrade.FactsSchemaVersion {
		return nil, fmt.Errorf("%s has facts schema %q, this build reads %q", path, f.SchemaVersion, upgrade.FactsSchemaVersion)
	}
	return &f, nil
}

func postUpgradeFailOn(level string, results []upgrade.Result) error {
	if level == "" {
		return nil
	}
	var threshold upgrade.Status
	switch strings.ToLower(level) {
	case "fail":
		threshold = upgrade.StatusFail
	case "warn":
		threshold = upgrade.StatusWarn
	default:
		return fmt.Errorf("--fail-on: want fail or warn, got %q", level)
	}
	n := 0
	for _, r := range results {
		if r.Status.Rank() <= threshold.Rank() {
			n++
		}
	}
	if n > 0 {
		fmt.Fprintf(os.Stderr, "fail-on %s: %d cluster(s)\n", strings.ToLower(level), n)
		os.Exit(2)
	}
	return nil
}

func init() {
	f := postUpgradeCmd.Flags()
	f.StringVar(&puTarget, "target", fmt.Sprintf("1.%d", upgrade.LatestKnownMinor), "Kubernetes minor the clusters should be on")
	f.StringVar(&puKubeProxy, "kube-proxy", "", "expected kube-proxy version (default: built-in table for --target)")
	f.StringVar(&puCoreDNS, "coredns", "", "expected CoreDNS version (default: built-in table for --target)")
	f.StringVar(&puVPCCNI, "vpc-cni", "", "expected VPC CNI version (default: built-in table)")
	f.StringVar(&puFile, "contexts-file", "", "file with one cluster name or context per line (# comments allowed)")
	f.StringVar(&puMatch, "match", "", "only contexts matching this regex (alone: every matching context in the kubeconfig)")
	f.StringVar(&puExclude, "exclude", "", "skip contexts matching this regex")
	f.IntVar(&puParallel, "parallel", 8, "clusters checked at once")
	f.DurationVar(&puTimeout, "timeout", 90*time.Second, "time budget per cluster")
	f.StringVar(&puSaveDir, "save-dir", "", "write each cluster's raw facts (JSON) to this directory")
	f.StringSliceVar(&puFrom, "from", nil, "analyze saved facts files instead of live clusters")
	f.StringVar(&puFailOn, "fail-on", "", "exit 2 if any cluster's verdict is at or worse than: fail | warn")
	rootCmd.AddCommand(postUpgradeCmd)
}
