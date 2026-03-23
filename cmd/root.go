package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var (
	clusterName string
	namespace   string
	outputFmt   string
	verbose     bool
	region      string
	awsProfile  string
)

var rootCmd = &cobra.Command{
	Use:   "k8s-doctor",
	Short: "Kubernetes troubleshooting CLI — zero config, jumpserver ready",
	Long: `
  k8s-doctor — SRE-grade Kubernetes troubleshooting CLI

  Zero config. Connect to your cluster the way you normally do,
  then just run commands. --cluster flag is optional.

  Examples (jumpserver workflow — no flags needed):
    ./k8s-doctor triage
    ./k8s-doctor snapshot
    ./k8s-doctor predict
    ./k8s-doctor diff --window 1h
    ./k8s-doctor audit --window 1h
    ./k8s-doctor watch

  Or switch clusters on the fly:
    ./k8s-doctor triage --cluster prod-us-east-1
    ./k8s-doctor snapshot --cluster staging-eu-west-1
`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		skip := []string{"help", "list", "__complete", "completion"}
		for _, s := range skip {
			if cmd.Name() == s {
				return nil
			}
		}

		// No --cluster flag — use whatever context is already active
		// This is the jumpserver workflow: you already connected, just run
		if clusterName == "" {
			out, err := exec.Command("kubectl", "config", "current-context").Output()
			if err != nil {
				return fmt.Errorf("no active kubectl context found\nConnect to a cluster first:\n  aws eks update-kubeconfig --name <cluster> --region <region>\nOr pass --cluster flag:\n  ./k8s-doctor triage --cluster <name>")
			}
			clusterName = strings.TrimSpace(string(out))
			color.Green("✓ Using active context: %s", clusterName)
			return nil
		}

		// --cluster flag was passed — switch context
		return switchContext(clusterName, region, awsProfile, verbose)
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&clusterName, "cluster", "c", "", "cluster name or kube context (optional — uses active context if not set)")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "namespace (default: all)")
	rootCmd.PersistentFlags().StringVarP(&outputFmt, "output", "o", "terminal", "output format: terminal | json | markdown")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "show raw commands being run")
	rootCmd.PersistentFlags().StringVar(&region, "region", "", "AWS region (auto-detected from cluster name if omitted)")
	rootCmd.PersistentFlags().StringVar(&awsProfile, "profile", "", "AWS profile (uses default if omitted)")
	rootCmd.AddCommand(listCmd)
}

func switchContext(cluster, reg, profile string, verbose bool) error {
	color.Cyan("→ Switching to cluster: %s", cluster)

	// Try existing context first
	out, err := exec.Command("kubectl", "config", "use-context", cluster).CombinedOutput()
	if err == nil {
		color.Green("✓ Context: %s", strings.TrimSpace(string(out)))
		return nil
	}

	// Not found locally — fetch via AWS EKS
	color.HiBlack("  Context not in kubeconfig, fetching via AWS EKS...")

	if reg == "" {
		reg = guessRegion(cluster)
		if verbose {
			color.HiBlack("  Auto-detected region: %s", reg)
		}
	}

	args := []string{"eks", "update-kubeconfig", "--name", cluster, "--region", reg}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	if verbose {
		color.HiBlack("  Running: aws %s", strings.Join(args, " "))
	}

	cmd := exec.Command("aws", args...)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("aws eks update-kubeconfig failed: %w\n%s", err, string(out))
	}

	exec.Command("kubectl", "config", "use-context", cluster).Run()
	color.Green("✓ Context switched to: %s", cluster)
	return nil
}

func guessRegion(name string) string {
	regions := []string{
		"us-east-1", "us-east-2", "us-west-1", "us-west-2",
		"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1", "eu-north-1",
		"ap-southeast-1", "ap-southeast-2", "ap-south-1", "ap-northeast-1", "ap-northeast-2",
		"ca-central-1", "sa-east-1",
	}
	for _, r := range regions {
		if strings.Contains(name, r) {
			return r
		}
	}
	return "us-east-1"
}

func pickFromKubeContexts() (string, error) {
	out, err := exec.Command("kubectl", "config", "get-contexts", "-o", "name").Output()
	if err != nil {
		return "", fmt.Errorf("no kube contexts found — connect to a cluster first")
	}
	contexts := strings.Split(strings.TrimSpace(string(out)), "\n")
	fmt.Println(color.CyanString("\nAvailable contexts (%d):", len(contexts)))
	for i, c := range contexts {
		marker := "  "
		if strings.Contains(strings.ToLower(c), "prod") {
			marker = color.RedString("⚠ ")
		}
		fmt.Printf("%s[%3d] %s\n", marker, i+1, c)
	}
	fmt.Print(color.CyanString("\nEnter number or name fragment: "))
	var input string
	fmt.Scanln(&input)
	var idx int
	if _, err := fmt.Sscanf(input, "%d", &idx); err == nil {
		if idx >= 1 && idx <= len(contexts) {
			return contexts[idx-1], nil
		}
	}
	lower := strings.ToLower(input)
	for _, c := range contexts {
		if strings.Contains(strings.ToLower(c), lower) {
			return c, nil
		}
	}
	return "", fmt.Errorf("no context matched %q", input)
}

func nsDisplay() string {
	if namespace == "" {
		return "all"
	}
	return namespace
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all kube contexts available on this machine",
	RunE: func(cmd *cobra.Command, args []string) error {
		out, err := exec.Command("kubectl", "config", "get-contexts").Output()
		if err != nil {
			return fmt.Errorf("kubectl not found or no contexts configured")
		}
		fmt.Println(string(out))
		return nil
	},
}
