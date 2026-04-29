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
	Short: "Kubernetes troubleshooting CLI",
	Long: `k8s-doctor — SRE-grade Kubernetes troubleshooting CLI

Zero config. Connect to your cluster the way you normally do, then run commands.

Examples:
  k8s-doctor triage
  k8s-doctor diagnose
  k8s-doctor node pressure
  k8s-doctor diff --window 1h
  k8s-doctor audit --window 2h
  k8s-doctor serve --port 8080

  # switch clusters on the fly
  k8s-doctor triage --cluster prod-us-east-1
`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		skip := []string{"help", "list", "__complete", "completion"}
		for _, s := range skip {
			if cmd.Name() == s {
				return nil
			}
		}

		if clusterName == "" {
			out, err := exec.Command("kubectl", "config", "current-context").Output()
			if err != nil {
				return fmt.Errorf("no active kubectl context\nconnect to a cluster first or pass --cluster <name>")
			}
			clusterName = strings.TrimSpace(string(out))
			fmt.Fprintf(os.Stderr, "context  %s\n", color.HiBlackString(clusterName))
			return nil
		}

		return switchContext(clusterName, region, awsProfile, verbose)
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&clusterName, "cluster", "c", "", "cluster name or kube context (default: active context)")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "namespace (default: all)")
	rootCmd.PersistentFlags().StringVarP(&outputFmt, "output", "o", "terminal", "output format: terminal | json | markdown")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "show raw commands being run")
	rootCmd.PersistentFlags().StringVar(&region, "region", "", "AWS region")
	rootCmd.PersistentFlags().StringVar(&awsProfile, "profile", "", "AWS profile")
	rootCmd.AddCommand(listCmd)
}

func switchContext(cluster, reg, profile string, verbose bool) error {
	fmt.Fprintf(os.Stderr, "context  switching to %s\n", cluster)

	out, err := exec.Command("kubectl", "config", "use-context", cluster).CombinedOutput()
	if err == nil {
		fmt.Fprintf(os.Stderr, "context  %s\n", color.HiBlackString(strings.TrimSpace(string(out))))
		return nil
	}

	fmt.Fprintf(os.Stderr, "context  not in kubeconfig, fetching via aws eks...\n")

	if reg == "" {
		reg = guessRegion(cluster)
		if verbose {
			fmt.Fprintf(os.Stderr, "region   %s (auto-detected)\n", reg)
		}
	}

	args := []string{"eks", "update-kubeconfig", "--name", cluster, "--region", reg}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "running  aws %s\n", strings.Join(args, " "))
	}

	ekscmd := exec.Command("aws", args...)
	ekscmd.Env = os.Environ()
	if out, err := ekscmd.CombinedOutput(); err != nil {
		return fmt.Errorf("aws eks update-kubeconfig failed: %w\n%s", err, string(out))
	}

	exec.Command("kubectl", "config", "use-context", cluster).Run()
	fmt.Fprintf(os.Stderr, "context  %s\n", color.HiBlackString(cluster))
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

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all kube contexts",
	RunE: func(cmd *cobra.Command, args []string) error {
		out, err := exec.Command("kubectl", "config", "get-contexts").Output()
		if err != nil {
			return fmt.Errorf("kubectl not found or no contexts configured")
		}
		fmt.Println(string(out))
		return nil
	},
}

func nsDisplay() string {
	if namespace == "" {
		return "all"
	}
	return namespace
}

func pickFromKubeContexts() (string, error) {
	out, err := exec.Command("kubectl", "config", "get-contexts", "-o", "name").Output()
	if err != nil {
		return "", fmt.Errorf("no kube contexts found")
	}
	contexts := strings.Split(strings.TrimSpace(string(out)), "\n")
	fmt.Printf("\ncontexts (%d):\n", len(contexts))
	for i, c := range contexts {
		marker := "  "
		if strings.Contains(strings.ToLower(c), "prod") {
			marker = color.RedString("! ")
		}
		fmt.Printf("%s[%3d] %s\n", marker, i+1, c)
	}
	fmt.Print("\nenter number or name: ")
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
