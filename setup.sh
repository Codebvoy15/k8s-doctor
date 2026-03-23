#!/bin/bash

# Run this from inside your k8s-doctor folder
# It will create all files in the right place automatically

echo "Creating folder structure..."
mkdir -p cmd
mkdir -p internal/diag
mkdir -p internal/output

echo "Creating main.go..."
cat > main.go << 'EOF'
package main

import "github.com/Codebvoy15/k8s-doctor/cmd"

func main() {
	cmd.Execute()
}
EOF

echo "Creating go.mod..."
cat > go.mod << 'EOF'
module github.com/Codebvoy15/k8s-doctor

go 1.22

require (
	github.com/aws/aws-sdk-go-v2 v1.26.1
	github.com/aws/aws-sdk-go-v2/config v1.27.9
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.40.5
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.155.1
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2 v1.31.1
	github.com/aws/aws-sdk-go-v2/service/iam v1.31.1
	github.com/fatih/color v1.16.0
	github.com/spf13/cobra v1.8.0
	k8s.io/api v0.30.0
	k8s.io/apimachinery v0.30.0
	k8s.io/client-go v0.30.0
	k8s.io/metrics v0.30.0
)
EOF

echo "Creating .gitignore..."
cat > .gitignore << 'EOF'
k8s-doctor-mac
EOF

echo "Creating cmd/root.go..."
cat > cmd/root.go << 'EOF'
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
  Zero config. Drop the binary, run it. Context switches on the fly.
`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		skip := []string{"help", "list", "__complete", "completion"}
		for _, s := range skip {
			if cmd.Name() == s {
				return nil
			}
		}
		if clusterName == "" {
			chosen, err := pickFromKubeContexts()
			if err != nil {
				return err
			}
			clusterName = chosen
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
	rootCmd.PersistentFlags().StringVarP(&clusterName, "cluster", "c", "", "cluster name or kube context (fuzzy match)")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "namespace (default: all)")
	rootCmd.PersistentFlags().StringVarP(&outputFmt, "output", "o", "terminal", "output format: terminal | json | markdown")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "show raw commands being run")
	rootCmd.PersistentFlags().StringVar(&region, "region", "", "AWS region (auto-detected from cluster name if omitted)")
	rootCmd.PersistentFlags().StringVar(&awsProfile, "profile", "", "AWS profile (uses default if omitted)")
	rootCmd.AddCommand(listCmd)
}

func switchContext(cluster, reg, profile string, verbose bool) error {
	color.Cyan("→ Switching to cluster: %s", cluster)
	out, err := exec.Command("kubectl", "config", "use-context", cluster).CombinedOutput()
	if err == nil {
		color.Green("✓ Context: %s", strings.TrimSpace(string(out)))
		return nil
	}
	color.HiBlack("  Context not in kubeconfig, fetching via AWS EKS...")
	if reg == "" {
		reg = guessRegion(cluster)
	}
	args := []string{"eks", "update-kubeconfig", "--name", cluster, "--region", reg}
	if profile != "" {
		args = append(args, "--profile", profile)
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
		return "", fmt.Errorf("no kube contexts found — use --cluster <n> --region <region>")
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
EOF

echo "Creating cmd/triage.go..."
cat > cmd/triage.go << 'EOF'
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

var triageCmd = &cobra.Command{
	Use:   "triage",
	Short: "First-stop triage: unhealthy pods, events, crash loops, pending pods",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("TRIAGE — cluster: %s | ns: %s", clusterName, nsDisplay())
		printer.Section("Pod health")
		podFindings, err := engine.PodHealth()
		if err != nil {
			return fmt.Errorf("pod health check failed: %w", err)
		}
		printer.Findings(podFindings)
		printer.Section("Pending pods")
		pendingFindings, err := engine.PendingPods()
		if err != nil {
			return fmt.Errorf("pending pods check failed: %w", err)
		}
		printer.Findings(pendingFindings)
		printer.Section("Warning events (last 30m)")
		eventFindings, err := engine.RecentWarningEvents(30 * time.Minute)
		if err != nil {
			return fmt.Errorf("events check failed: %w", err)
		}
		printer.Findings(eventFindings)
		printer.Section("High restart pods (>3)")
		restartFindings, err := engine.HighRestartPods(3)
		if err != nil {
			return fmt.Errorf("restart check failed: %w", err)
		}
		printer.Findings(restartFindings)
		all := flatten(podFindings, pendingFindings, eventFindings, restartFindings)
		printer.RootCauseSummary(all)
		return nil
	},
}

var triageLogsCmd = &cobra.Command{
	Use:   "logs [pod-name]",
	Short: "Fetch crash logs from a pod",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		podName := ""
		if len(args) > 0 {
			podName = args[0]
		}
		logs, err := engine.FetchCrashLogs(podName, logLines)
		if err != nil {
			return err
		}
		for _, l := range logs {
			fmt.Println(color.HiWhiteString(l))
		}
		return nil
	},
}

var logLines int

func init() {
	triageCmd.AddCommand(triageLogsCmd)
	triageLogsCmd.Flags().IntVar(&logLines, "lines", 100, "number of log lines to fetch")
	rootCmd.AddCommand(triageCmd)
}

func flatten(sets ...[]diag.Finding) []diag.Finding {
	var all []diag.Finding
	for _, s := range sets {
		all = append(all, s...)
	}
	return all
}
EOF

echo "Creating cmd/node.go..."
cat > cmd/node.go << 'EOF'
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

var nodeCmd = &cobra.Command{
	Use:   "node",
	Short: "Node diagnostics: pressure, taints, top, cordon",
}

var nodePressureCmd = &cobra.Command{
	Use:   "pressure",
	Short: "Check all nodes for memory/disk/PID pressure and NotReady state",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("NODE PRESSURE — cluster: %s", clusterName)
		findings, err := engine.NodePressure()
		if err != nil {
			return err
		}
		printer.Findings(findings)
		printer.RootCauseSummary(findings)
		return nil
	},
}

var nodeTaintsCmd = &cobra.Command{
	Use:   "taints",
	Short: "List all node taints",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("NODE TAINTS — cluster: %s", clusterName)
		findings, err := engine.NodeTaints()
		if err != nil {
			return err
		}
		printer.Findings(findings)
		return nil
	},
}

var nodeTopCmd = &cobra.Command{
	Use:   "top",
	Short: "Show node resource usage with colour-coded thresholds",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		nodes, err := engine.NodeTop()
		if err != nil {
			return fmt.Errorf("node top failed (is metrics-server running?): %w", err)
		}
		fmt.Printf("\n%-44s %8s %8s %10s %10s\n",
			color.CyanString("NODE"), "CPU", "CPU%", "MEMORY", "MEM%")
		fmt.Println(color.HiBlackString("─────────────────────────────────────────────────────────────────────────────"))
		for _, n := range nodes {
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
			fmt.Printf("%-44s %8s %8s %10s %10s\n",
				n.Name,
				cpuFn(n.CPUUsage),
				cpuFn(fmt.Sprintf("%.0f%%", n.CPUPercent)),
				memFn(n.MemUsage),
				memFn(fmt.Sprintf("%.0f%%", n.MemPercent)),
			)
		}
		return nil
	},
}

var nodeCordonCmd = &cobra.Command{
	Use:   "cordon [node-name]",
	Short: "Cordon (and optionally drain) a problematic node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		color.Yellow("⚠  Cordoning node: %s", args[0])
		fmt.Print("Confirm? [y/N]: ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("Aborted.")
			return nil
		}
		return engine.CordonNode(args[0], drainNode)
	},
}

var drainNode bool

func init() {
	nodeCmd.AddCommand(nodePressureCmd)
	nodeCmd.AddCommand(nodeTaintsCmd)
	nodeCmd.AddCommand(nodeTopCmd)
	nodeCmd.AddCommand(nodeCordonCmd)
	nodeCordonCmd.Flags().BoolVar(&drainNode, "drain", false, "also drain after cordoning")
	rootCmd.AddCommand(nodeCmd)
}
EOF

echo "Creating cmd/network.go..."
cat > cmd/network.go << 'EOF'
package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
	"github.com/Codebvoy15/k8s-doctor/internal/output"
)

var networkCmd = &cobra.Command{
	Use:   "network",
	Short: "Network diagnostics: DNS, services, network policies, ingress",
}

var networkDNSCmd = &cobra.Command{
	Use:   "dns",
	Short: "Diagnose DNS: CoreDNS health, resolution errors",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("DNS DIAGNOSTICS — cluster: %s", clusterName)
		findings, err := engine.DNSDiag()
		if err != nil {
			return fmt.Errorf("DNS diagnostic failed: %w", err)
		}
		printer.Findings(findings)
		printer.RootCauseSummary(findings)
		return nil
	},
}

var networkSvcCmd = &cobra.Command{
	Use:   "svc [service-name]",
	Short: "Check a Service has healthy endpoints",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		svcName := ""
		if len(args) > 0 {
			svcName = args[0]
		}
		printer.Header("SERVICE CHECK — %s", svcName)
		findings, err := engine.ServiceEndpoints(svcName)
		if err != nil {
			return err
		}
		printer.Findings(findings)
		printer.RootCauseSummary(findings)
		return nil
	},
}

var networkNetpolCmd = &cobra.Command{
	Use:   "netpol",
	Short: "Show NetworkPolicies and flag deny-all rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("NETWORK POLICIES — cluster: %s | ns: %s", clusterName, nsDisplay())
		findings, err := engine.NetworkPolicies()
		if err != nil {
			return err
		}
		printer.Findings(findings)
		return nil
	},
}

var networkIngressCmd = &cobra.Command{
	Use:   "ingress",
	Short: "Check Ingress/ALB health",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("INGRESS / ALB — cluster: %s", clusterName)
		findings, err := engine.IngressHealth()
		if err != nil {
			return err
		}
		printer.Findings(findings)
		printer.RootCauseSummary(findings)
		return nil
	},
}

func init() {
	networkCmd.AddCommand(networkDNSCmd)
	networkCmd.AddCommand(networkSvcCmd)
	networkCmd.AddCommand(networkNetpolCmd)
	networkCmd.AddCommand(networkIngressCmd)
	rootCmd.AddCommand(networkCmd)
}
EOF

echo "Creating cmd/aws.go..."
cat > cmd/aws.go << 'EOF'
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

var awsCmd = &cobra.Command{
	Use:   "aws",
	Short: "AWS-layer checks: EC2, ALB, security groups, IAM/IRSA, ASG",
}

var awsEC2Cmd = &cobra.Command{
	Use:   "ec2",
	Short: "EC2 instance status checks for EKS nodes",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("EC2 NODE CHECKS — cluster: %s", clusterName)
		findings, err := engine.EC2NodeHealth(clusterName, region, awsProfile)
		if err != nil {
			return err
		}
		printer.Findings(findings)
		printer.RootCauseSummary(findings)
		return nil
	},
}

var awsALBCmd = &cobra.Command{
	Use:   "alb",
	Short: "Check ALB target group health",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("ALB TARGET GROUPS — cluster: %s", clusterName)
		findings, err := engine.ALBHealth(clusterName, region, awsProfile)
		if err != nil {
			return err
		}
		printer.Findings(findings)
		printer.RootCauseSummary(findings)
		return nil
	},
}

var awsSGCmd = &cobra.Command{
	Use:   "sg",
	Short: "Audit EKS security group rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("SECURITY GROUP AUDIT — cluster: %s", clusterName)
		findings, err := engine.SGAudit(clusterName, region, awsProfile)
		if err != nil {
			return err
		}
		printer.Findings(findings)
		printer.RootCauseSummary(findings)
		return nil
	},
}

var awsIAMCmd = &cobra.Command{
	Use:   "iam",
	Short: "Audit node IAM roles and IRSA service account annotations",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("IAM / IRSA AUDIT — cluster: %s | ns: %s", clusterName, nsDisplay())
		findings, err := engine.IAMAudit(clusterName, namespace, region, awsProfile)
		if err != nil {
			return err
		}
		printer.Findings(findings)
		printer.RootCauseSummary(findings)
		return nil
	},
}

var awsASGCmd = &cobra.Command{
	Use:   "asg",
	Short: "Check Auto Scaling Groups",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		groups, err := engine.ASGStatus(clusterName, region, awsProfile)
		if err != nil {
			return err
		}
		fmt.Printf("\n%-50s %8s %10s %5s %5s  %s\n",
			color.CyanString("ASG NAME"), "DESIRED", "IN-SERVICE", "MIN", "MAX", "STATUS")
		fmt.Println(color.HiBlackString("─────────────────────────────────────────────────────────────────────────────────────"))
		for _, g := range groups {
			statusFn := color.GreenString
			if g.Status == "BELOW_MIN" {
				statusFn = color.RedString
			} else if g.Status == "SCALING" {
				statusFn = color.YellowString
			}
			fmt.Printf("%-50s %8d %10d %5d %5d  %s\n",
				g.Name, g.DesiredCapacity, g.InServiceCount, g.MinSize, g.MaxSize,
				statusFn(g.Status))
		}
		return nil
	},
}

func init() {
	awsCmd.AddCommand(awsEC2Cmd)
	awsCmd.AddCommand(awsALBCmd)
	awsCmd.AddCommand(awsSGCmd)
	awsCmd.AddCommand(awsIAMCmd)
	awsCmd.AddCommand(awsASGCmd)
	rootCmd.AddCommand(awsCmd)
}
EOF

echo "Creating cmd/report.go..."
cat > cmd/report.go << 'EOF'
package cmd

import (
	"context"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
	"github.com/Codebvoy15/k8s-doctor/internal/output"
)

var ticketID string

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Run all diagnostics and produce a ticket-ready incident report",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		fmt := outputFmt
		if fmt == "terminal" {
			fmt = "markdown"
		}
		printer := output.NewPrinter(fmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("INCIDENT REPORT — cluster: %s | ticket: %s", clusterName, ticketID)
		color.Cyan("→ Running full diagnostic suite...")
		var all []diag.Finding
		run := func(name string, fn func() ([]diag.Finding, error)) {
			printer.Section(name)
			findings, err := fn()
			if err != nil {
				color.Yellow("  ⚠  %s failed: %v", name, err)
				return
			}
			printer.Findings(findings)
			all = append(all, findings...)
		}
		run("Pod health", engine.PodHealth)
		run("Pending pods", engine.PendingPods)
		run("Warning events (30m)", func() ([]diag.Finding, error) {
			return engine.RecentWarningEvents(30 * time.Minute)
		})
		run("High restart pods", func() ([]diag.Finding, error) {
			return engine.HighRestartPods(3)
		})
		run("Node pressure", engine.NodePressure)
		run("DNS diagnostics", engine.DNSDiag)
		run("Ingress health", engine.IngressHealth)
		printer.Section("Root cause summary")
		printer.RootCauseSummary(all)
		return nil
	},
}

func init() {
	reportCmd.Flags().StringVar(&ticketID, "ticket", "", "ticket ID (e.g. INC-1234)")
	rootCmd.AddCommand(reportCmd)
}
EOF

echo "Creating cmd/snapshot.go..."
cat > cmd/snapshot.go << 'EOF'
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

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Full cluster state at a glance — everything an ops engineer needs to know",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		snap, err := engine.ClusterSnapshot()
		if err != nil {
			return fmt.Errorf("snapshot failed: %w", err)
		}
		printSnapshot(snap)
		return nil
	},
}

func printSnapshot(s *diag.ClusterSnapshot) {
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	fmt.Printf("\n%s\n", color.New(color.FgCyan, color.Bold).Sprintf("╔══════════════════════════════════════════════════════════════╗"))
	fmt.Printf("%s\n", color.CyanString("  CLUSTER SNAPSHOT — %s", now))
	fmt.Printf("%s\n\n", color.New(color.FgCyan, color.Bold).Sprintf("╚══════════════════════════════════════════════════════════════╝"))
	scoreColor := color.GreenString
	scoreLabel := "HEALTHY"
	if s.HealthScore < 80 {
		scoreColor = color.YellowString
		scoreLabel = "DEGRADED"
	}
	if s.HealthScore < 60 {
		scoreColor = color.RedString
		scoreLabel = "CRITICAL"
	}
	fmt.Printf("  %s  %s  (%d/100)\n\n", color.CyanString("Cluster health:"), scoreColor("● "+scoreLabel), s.HealthScore)
	fmt.Printf("  %s  %s\n\n", color.HiBlackString("Server version:"), s.ServerVersion)
	fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Nodes"))
	fmt.Printf("  %-40s  %-10s  %-12s  %-12s  %-12s  %-12s\n", "NAME", "STATUS", "CPU REQ", "CPU CAP", "MEM REQ", "MEM CAP")
	fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 104)))
	for _, n := range s.Nodes {
		statusFn := color.GreenString
		if n.Status != "Ready" {
			statusFn = color.RedString
		}
		fmt.Printf("  %-40s  %-10s  %-12s  %-12s  %-12s  %-12s\n",
			n.Name, statusFn(n.Status), n.CPURequested, n.CPUCapacity, n.MemRequested, n.MemCapacity)
	}
	fmt.Println()
	fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Workloads by namespace"))
	fmt.Printf("  %-24s  %8s  %8s  %8s  %8s  %8s\n", "NAMESPACE", "DEPLOY", "PODS", "RUNNING", "FAILING", "STATEFUL")
	fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 72)))
	for _, ns := range s.Namespaces {
		failFn := color.GreenString
		if ns.FailingPods > 0 {
			failFn = color.RedString
		}
		fmt.Printf("  %-24s  %8d  %8d  %8d  %8s  %8d\n",
			ns.Name, ns.Deployments, ns.TotalPods, ns.RunningPods,
			failFn(fmt.Sprintf("%d", ns.FailingPods)), ns.StatefulSets)
	}
	fmt.Println()
	fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Top resource consumers (by memory)"))
	fmt.Printf("  %-44s  %-16s  %s\n", "POD", "NAMESPACE", "CPU REQ / MEM REQ")
	fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 80)))
	for i, c := range s.TopConsumers {
		if i >= 10 {
			break
		}
		name := c.Name
		if len(name) > 44 {
			name = name[:41] + "..."
		}
		fmt.Printf("  %-44s  %-16s  %s / %s\n", name, c.Namespace,
			color.YellowString(c.CPURequest), color.YellowString(c.MemRequest))
	}
	fmt.Println()
	if len(s.PVCs) > 0 {
		fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Persistent volume claims"))
		for _, pvc := range s.PVCs {
			fn := color.GreenString
			if pvc.Status != "Bound" {
				fn = color.RedString
			}
			fmt.Printf("  %-36s  %-16s  %-8s  %s\n", pvc.Name, pvc.Namespace, pvc.Capacity, fn(pvc.Status))
		}
		fmt.Println()
	}
	if len(s.Quotas) > 0 {
		fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Resource quota usage (>75%)"))
		for _, q := range s.Quotas {
			filled := int(q.UsedPercent / 10)
			if filled > 10 {
				filled = 10
			}
			bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
			barStr := color.GreenString(bar)
			if q.UsedPercent >= 90 {
				barStr = color.RedString(bar)
			} else if q.UsedPercent >= 75 {
				barStr = color.YellowString(bar)
			}
			fmt.Printf("  %-20s  %-20s  %s  %.0f%%\n", q.Namespace, q.Resource, barStr, q.UsedPercent)
		}
		fmt.Println()
	}
	if len(s.RecentWarnings) > 0 {
		fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Recent warnings (last 1h)"))
		for i, w := range s.RecentWarnings {
			if i >= 5 {
				fmt.Printf("  %s\n", color.HiBlackString("  ... and %d more", len(s.RecentWarnings)-5))
				break
			}
			fmt.Printf("  %s %-20s  %s\n", color.YellowString("◐"), w.Reason, color.HiBlackString(w.Message))
		}
		fmt.Println()
	}
}

func init() {
	rootCmd.AddCommand(snapshotCmd)
}
EOF

echo "Creating cmd/audit.go..."
cat > cmd/audit.go << 'EOF'
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

var (
	auditWindow string
	auditKind   string
	auditUser   string
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Who did what and when — change history with user attribution",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		window, err := time.ParseDuration(auditWindow)
		if err != nil {
			return fmt.Errorf("invalid --window %q: use formats like 1h, 30m", auditWindow)
		}
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		entries, err := engine.AuditLog(window, auditKind, auditUser)
		if err != nil {
			return fmt.Errorf("audit failed: %w", err)
		}
		printAuditLog(entries, window)
		return nil
	},
}

func printAuditLog(entries []diag.AuditEntry, window time.Duration) {
	fmt.Printf("\n%s\n\n", color.New(color.FgCyan, color.Bold).Sprintf("AUDIT LOG — last %s", window))
	if len(entries) == 0 {
		fmt.Println(color.GreenString("  No changes detected in this window."))
		return
	}
	fmt.Printf("  %-18s  %-12s  %-22s  %-20s  %-20s  %s\n",
		"TIME", "KIND", "NAME", "NAMESPACE", "CHANGED BY", "ACTION")
	fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 100)))
	for _, e := range entries {
		actionFn := color.GreenString
		if e.Action == "DELETE" {
			actionFn = color.RedString
		} else if e.Action == "UPDATE" {
			actionFn = color.YellowString
		}
		corr := ""
		if e.CorrelatedFault != "" {
			corr = color.RedString(" ⚠ " + e.CorrelatedFault)
		}
		name := e.Name
		if len(name) > 22 {
			name = name[:19] + "..."
		}
		ns := e.Namespace
		if len(ns) > 20 {
			ns = ns[:17] + "..."
		}
		fm := e.FieldManager
		if len(fm) > 20 {
			fm = fm[:17] + "..."
		}
		fmt.Printf("  %-18s  %-12s  %-22s  %-20s  %-20s  %s%s\n",
			color.HiBlackString(e.Timestamp.Format("01-02 15:04:05")),
			e.Kind, name, ns, color.CyanString(fm),
			actionFn(e.Action), corr,
		)
	}
	fmt.Printf("\n  Total: %d change(s)\n", len(entries))
	var correlated []diag.AuditEntry
	for _, e := range entries {
		if e.CorrelatedFault != "" {
			correlated = append(correlated, e)
		}
	}
	if len(correlated) > 0 {
		fmt.Printf("\n  %s\n", color.New(color.FgRed, color.Bold).Sprint("Changes correlated with active faults:"))
		for _, e := range correlated {
			fmt.Printf("  %s  %-12s  %-24s  by %-20s  → %s\n",
				color.RedString("●"), e.Kind, e.Name,
				color.CyanString(e.FieldManager), color.RedString(e.CorrelatedFault))
			if e.Mitigation != "" {
				fmt.Printf("    %s %s\n", color.GreenString("→ Fix:"), color.GreenString(e.Mitigation))
			}
		}
	}
	fmt.Println()
}

func init() {
	auditCmd.Flags().StringVar(&auditWindow, "window", "1h", "how far back to look (e.g. 30m, 2h, 24h)")
	auditCmd.Flags().StringVar(&auditKind, "kind", "", "filter by resource kind (e.g. Deployment, ConfigMap)")
	auditCmd.Flags().StringVar(&auditUser, "user", "", "filter by field manager / user name")
	rootCmd.AddCommand(auditCmd)
}
EOF

echo "Creating cmd/watch.go..."
cat > cmd/watch.go << 'EOF'
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
)

var watchKinds string

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Live stream of every resource change across the cluster in real time",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sig
			fmt.Println(color.HiBlackString("\n\nStopped."))
			cancel()
		}()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		kinds := []string{}
		if watchKinds != "" {
			kinds = strings.Split(watchKinds, ",")
		}
		fmt.Printf("%s\n", color.New(color.FgCyan, color.Bold).Sprintf(
			"LIVE WATCH — cluster: %s | ns: %s | Ctrl+C to stop", clusterName, nsDisplay()))
		fmt.Printf("  %-18s  %-10s  %-14s  %-28s  %-20s  %s\n",
			"TIME", "EVENT", "KIND", "NAME", "NAMESPACE", "BY")
		fmt.Println(color.HiBlackString("  " + strings.Repeat("─", 100)))
		eventCh, err := engine.WatchResources(kinds)
		if err != nil {
			return fmt.Errorf("watch failed: %w", err)
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case ev, ok := <-eventCh:
				if !ok {
					return nil
				}
				timeStr := ev.Timestamp.Format("15:04:05.000")
				eventFn := color.GreenString
				eventLabel := "ADD    "
				switch ev.EventType {
				case "MODIFIED":
					eventFn = color.YellowString
					eventLabel = "UPDATE "
				case "DELETED":
					eventFn = color.RedString
					eventLabel = "DELETE "
				}
				criticalKinds := map[string]bool{
					"Deployment": true, "StatefulSet": true, "DaemonSet": true,
					"ConfigMap": true, "Secret": true, "Service": true, "Ingress": true,
				}
				star := ""
				if criticalKinds[ev.Kind] {
					star = color.YellowString(" ★")
				}
				fm := ev.FieldManager
				if fm == "" {
					fm = "unknown"
				}
				name := ev.Name
				if len(name) > 28 {
					name = name[:25] + "..."
				}
				ns := ev.Namespace
				if len(ns) > 20 {
					ns = ns[:17] + "..."
				}
				fmt.Printf("  %-18s  %s  %-14s  %-28s  %-20s  %s\n",
					color.HiBlackString(timeStr),
					eventFn(eventLabel),
					ev.Kind+star,
					name, ns,
					color.CyanString(fm),
				)
			}
		}
	},
}

func init() {
	watchCmd.Flags().StringVar(&watchKinds, "kinds", "",
		"comma-separated kinds to watch (default: all). e.g. Deployment,ConfigMap,Secret")
	rootCmd.AddCommand(watchCmd)
}
EOF

echo "Creating cmd/predict.go..."
cat > cmd/predict.go << 'EOF'
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
EOF

echo "Creating cmd/diff.go..."
cat > cmd/diff.go << 'EOF'
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
)

var (
	diffSavePath string
	diffLoadPath string
	diffWindow   string
)

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "What changed, when, and did it cause the problem you are seeing?",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		if diffSavePath != "" {
			color.Cyan("→ Capturing cluster snapshot to %s...", diffSavePath)
			snap, err := engine.CaptureStateSnapshot()
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
			color.HiBlack("  Run diff later: ./k8s-doctor diff --load %s --cluster %s", diffSavePath, clusterName)
			return nil
		}
		if diffLoadPath != "" {
			color.Cyan("→ Loading baseline from %s...", diffLoadPath)
			b, err := os.ReadFile(diffLoadPath)
			if err != nil {
				return fmt.Errorf("could not read snapshot: %w", err)
			}
			var baseline diag.StateSnapshot
			if err := json.Unmarshal(b, &baseline); err != nil {
				return fmt.Errorf("invalid snapshot file: %w", err)
			}
			age := time.Since(baseline.CapturedAt)
			color.HiBlack("  Baseline captured %s ago", age.Round(time.Second))
			diffs, err := engine.SnapshotDiff(&baseline)
			if err != nil {
				return err
			}
			printDiffs(diffs, fmt.Sprintf("since snapshot (%s ago)", age.Round(time.Second)))
			return nil
		}
		window, err := time.ParseDuration(diffWindow)
		if err != nil {
			return fmt.Errorf("invalid --window: use 30m, 1h, etc.")
		}
		diffs, err := engine.LiveDiff(window)
		if err != nil {
			return fmt.Errorf("diff failed: %w", err)
		}
		printDiffs(diffs, fmt.Sprintf("last %s", diffWindow))
		return nil
	},
}

func printDiffs(diffs []diag.DiffEntry, windowLabel string) {
	fmt.Printf("\n%s\n\n", color.New(color.FgCyan, color.Bold).Sprintf("DIFF — changes %s", windowLabel))
	if len(diffs) == 0 {
		fmt.Println(color.GreenString("  No changes detected."))
		return
	}
	var correlated []diag.DiffEntry
	for _, d := range diffs {
		if d.CorrelatedFault != "" {
			correlated = append(correlated, d)
		}
	}
	if len(correlated) > 0 {
		fmt.Printf("  %s\n", color.New(color.FgRed, color.Bold).Sprintf("Changes correlated with active faults (%d):", len(correlated)))
		for _, d := range correlated {
			fmt.Printf("\n  %s  %s/%s  (by %s)\n",
				color.RedString("●"), d.Kind, d.Name, color.CyanString(d.FieldManager))
			fmt.Printf("    field:  %s\n", d.Field)
			if d.OldValue != "" {
				fmt.Printf("    before: %s\n", color.RedString(d.OldValue))
			}
			fmt.Printf("    after:  %s\n", color.YellowString(d.NewValue))
			fmt.Printf("    at:     %s\n", d.Timestamp.Format("2006-01-02 15:04:05"))
			fmt.Printf("    fault:  %s\n", color.RedString(d.CorrelatedFault))
			if d.Mitigation != "" {
				fmt.Printf("    fix:    %s\n", color.GreenString(d.Mitigation))
			}
		}
		fmt.Println()
	}
	fmt.Printf("  %s\n", color.New(color.Bold).Sprintf("All changes (%d):", len(diffs)))
	fmt.Printf("  %-18s  %-14s  %-24s  %-20s  %-20s  %s\n",
		"TIME", "KIND", "NAME", "FIELD", "CHANGED BY", "ACTION")
	fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 100)))
	for _, d := range diffs {
		actionFn := color.GreenString
		switch d.Action {
		case "UPDATED":
			actionFn = color.YellowString
		case "DELETED":
			actionFn = color.RedString
		}
		corr := ""
		if d.CorrelatedFault != "" {
			corr = color.RedString(" ⚠")
		}
		name := d.Name
		if len(name) > 24 {
			name = name[:21] + "..."
		}
		fm := d.FieldManager
		if len(fm) > 20 {
			fm = fm[:17] + "..."
		}
		fmt.Printf("  %-18s  %-14s  %-24s  %-20s  %-20s  %s%s\n",
			color.HiBlackString(d.Timestamp.Format("01-02 15:04:05")),
			d.Kind, name, d.Field, color.CyanString(fm),
			actionFn(d.Action), corr,
		)
	}
	fmt.Printf("\n  Total: %d change(s) | %d correlated with faults\n\n", len(diffs), len(correlated))
}

func init() {
	diffCmd.Flags().StringVar(&diffWindow, "window", "30m", "live diff window (e.g. 30m, 1h, 2h)")
	diffCmd.Flags().StringVar(&diffSavePath, "save", "", "save current state snapshot to file")
	diffCmd.Flags().StringVar(&diffLoadPath, "load", "", "load baseline snapshot and diff against current")
	rootCmd.AddCommand(diffCmd)
}
EOF

echo "All cmd/ files created."
echo ""
echo "Now creating internal/diag/ files..."

echo "Creating internal/diag/engine.go..."
cat > internal/diag/engine.go << 'EOF'
package diag

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Severity string

const (
	SeverityCritical Severity = "CRITICAL"
	SeverityWarning  Severity = "WARNING"
	SeverityInfo     Severity = "INFO"
)

type Finding struct {
	Severity  Severity `json:"severity"`
	Category  string   `json:"category"`
	Title     string   `json:"title"`
	Detail    string   `json:"detail,omitempty"`
	Remedy    string   `json:"remedy,omitempty"`
	Score     int      `json:"score"`
	Object    string   `json:"object,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
}

type NodeMetric struct {
	Name       string
	CPUUsage   string
	CPUPercent float64
	MemUsage   string
	MemPercent float64
}

type ASGGroup struct {
	Name                string
	MinSize             int32
	MaxSize             int32
	DesiredCapacity     int32
	InServiceCount      int32
	Status              string
	LastScalingActivity string
}

type Engine struct {
	ctx       context.Context
	namespace string
	verbose   bool
	k8s       kubernetes.Interface
}

func NewEngine(ctx context.Context, namespace string, verbose bool) (*Engine, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	restCfg, err := cfg.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("could not build kubeconfig: %w", err)
	}
	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("could not create k8s client: %w", err)
	}
	return &Engine{ctx: ctx, namespace: namespace, verbose: verbose, k8s: k8sClient}, nil
}

func (e *Engine) ns() string {
	if e.namespace == "" {
		return metav1.NamespaceAll
	}
	return e.namespace
}

func (e *Engine) PodHealth() ([]Finding, error) {
	pods, err := e.k8s.CoreV1().Pods(e.ns()).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil {
				switch cs.State.Waiting.Reason {
				case "CrashLoopBackOff":
					findings = append(findings, Finding{
						Severity: SeverityCritical, Category: "pods", Title: "CrashLoopBackOff",
						Detail: fmt.Sprintf("container=%s restarts=%d", cs.Name, cs.RestartCount),
						Remedy: fmt.Sprintf("./k8s-doctor triage logs %s -n %s", pod.Name, pod.Namespace),
						Score: 90, Object: pod.Name, Namespace: pod.Namespace,
					})
				case "ImagePullBackOff", "ErrImagePull":
					findings = append(findings, Finding{
						Severity: SeverityWarning, Category: "pods", Title: "ImagePullBackOff",
						Detail: fmt.Sprintf("image=%s", cs.Image),
						Remedy: fmt.Sprintf("./k8s-doctor aws iam -n %s", pod.Namespace),
						Score: 75, Object: pod.Name, Namespace: pod.Namespace,
					})
				}
			}
			if cs.LastTerminationState.Terminated != nil &&
				cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
				findings = append(findings, Finding{
					Severity: SeverityCritical, Category: "pods", Title: "OOMKilled",
					Detail: fmt.Sprintf("container=%s restarts=%d", cs.Name, cs.RestartCount),
					Remedy: "kubectl top pod " + pod.Name + " -n " + pod.Namespace,
					Score: 85, Object: pod.Name, Namespace: pod.Namespace,
				})
			}
		}
		if pod.DeletionTimestamp != nil {
			age := time.Since(pod.DeletionTimestamp.Time)
			if age > 5*time.Minute {
				findings = append(findings, Finding{
					Severity: SeverityWarning, Category: "pods", Title: "Stuck Terminating",
					Detail: fmt.Sprintf("stuck for %s", age.Round(time.Second)),
					Remedy: fmt.Sprintf("kubectl delete pod %s -n %s --force --grace-period=0", pod.Name, pod.Namespace),
					Score: 60, Object: pod.Name, Namespace: pod.Namespace,
				})
			}
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "pods", Title: "All pods healthy"}}, nil
	}
	return findings, nil
}

func (e *Engine) PendingPods() ([]Finding, error) {
	pods, err := e.k8s.CoreV1().Pods(e.ns()).List(e.ctx, metav1.ListOptions{
		FieldSelector: "status.phase=Pending",
	})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, pod := range pods.Items {
		events, _ := e.k8s.CoreV1().Events(pod.Namespace).List(e.ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Pod", pod.Name),
		})
		reason := "unknown scheduler reason"
		for _, ev := range events.Items {
			if ev.Reason == "FailedScheduling" {
				reason = ev.Message
				break
			}
		}
		score := 70
		remedy := "./k8s-doctor node pressure"
		if strings.Contains(reason, "Insufficient memory") {
			score, remedy = 85, "./k8s-doctor node top"
		} else if strings.Contains(reason, "Insufficient cpu") {
			score, remedy = 85, "./k8s-doctor node top"
		} else if strings.Contains(reason, "had taint") {
			score, remedy = 80, "./k8s-doctor node taints"
		}
		findings = append(findings, Finding{
			Severity: SeverityWarning, Category: "pods", Title: "Pending Pod",
			Detail: truncate(reason, 200), Remedy: remedy,
			Score: score, Object: pod.Name, Namespace: pod.Namespace,
		})
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "pods", Title: "No pending pods"}}, nil
	}
	return findings, nil
}

func (e *Engine) RecentWarningEvents(window time.Duration) ([]Finding, error) {
	events, err := e.k8s.CoreV1().Events(e.ns()).List(e.ctx, metav1.ListOptions{
		FieldSelector: "type=Warning",
	})
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-window)
	seen := map[string]bool{}
	var findings []Finding
	for _, ev := range events.Items {
		if ev.LastTimestamp.Time.Before(cutoff) {
			continue
		}
		key := ev.Reason + "/" + ev.InvolvedObject.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		score := 40
		if ev.Count > 10 {
			score = 70
		}
		findings = append(findings, Finding{
			Severity: SeverityWarning, Category: "events", Title: ev.Reason,
			Detail: fmt.Sprintf("[%dx] %s — %s", ev.Count, ev.InvolvedObject.Name, truncate(ev.Message, 120)),
			Score: score, Object: ev.InvolvedObject.Name, Namespace: ev.Namespace,
		})
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "events",
			Title: fmt.Sprintf("No warning events in last %s", window)}}, nil
	}
	return findings, nil
}

func (e *Engine) HighRestartPods(threshold int32) ([]Finding, error) {
	pods, err := e.k8s.CoreV1().Pods(e.ns()).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.RestartCount >= threshold {
				findings = append(findings, Finding{
					Severity: SeverityWarning, Category: "pods", Title: "Frequent Restarts",
					Detail: fmt.Sprintf("container=%s restarts=%d", cs.Name, cs.RestartCount),
					Remedy: fmt.Sprintf("./k8s-doctor triage logs %s -n %s", pod.Name, pod.Namespace),
					Score: 65, Object: pod.Name, Namespace: pod.Namespace,
				})
			}
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "pods", Title: "No high-restart pods"}}, nil
	}
	return findings, nil
}

func (e *Engine) FetchCrashLogs(podName string, tailLines int) ([]string, error) {
	ns := e.namespace
	if ns == "" {
		ns = "default"
	}
	if podName == "" {
		pods, err := e.k8s.CoreV1().Pods(ns).List(e.ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, pod := range pods.Items {
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
					podName = pod.Name
					break
				}
			}
			if podName != "" {
				break
			}
		}
		if podName == "" {
			return nil, fmt.Errorf("no crashing pods found — specify pod name explicitly")
		}
	}
	pod, err := e.k8s.CoreV1().Pods(ns).Get(e.ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("pod %s not found in ns %s", podName, ns)
	}
	var logs []string
	tail := int64(tailLines)
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.LastTerminationState.Terminated != nil {
			req := e.k8s.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
				Container: cs.Name, Previous: true, TailLines: &tail,
			})
			if b, err := req.DoRaw(e.ctx); err == nil {
				logs = append(logs, fmt.Sprintf("=== PREVIOUS (crashed) container: %s ===", cs.Name))
				logs = append(logs, string(b))
			}
		}
		req := e.k8s.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
			Container: cs.Name, TailLines: &tail,
		})
		if b, err := req.DoRaw(e.ctx); err == nil {
			logs = append(logs, fmt.Sprintf("=== CURRENT container: %s ===", cs.Name))
			logs = append(logs, string(b))
		}
	}
	return logs, nil
}

func (e *Engine) NodePressure() ([]Finding, error) {
	nodes, err := e.k8s.CoreV1().Nodes().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, node := range nodes.Items {
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue {
				findings = append(findings, Finding{
					Severity: SeverityCritical, Category: "nodes", Title: "Node NotReady",
					Detail: fmt.Sprintf("reason=%s: %s", cond.Reason, cond.Message),
					Remedy: "journalctl -u kubelet -n 50", Score: 95, Object: node.Name,
				})
			}
			if cond.Status == corev1.ConditionTrue {
				switch cond.Type {
				case corev1.NodeMemoryPressure:
					findings = append(findings, Finding{
						Severity: SeverityCritical, Category: "nodes", Title: "MemoryPressure",
						Detail: cond.Message, Remedy: "evict pods or scale up node group",
						Score: 88, Object: node.Name,
					})
				case corev1.NodeDiskPressure:
					findings = append(findings, Finding{
						Severity: SeverityCritical, Category: "nodes", Title: "DiskPressure",
						Detail: cond.Message, Remedy: "clean /var/log or increase EBS volume",
						Score: 85, Object: node.Name,
					})
				case corev1.NodePIDPressure:
					findings = append(findings, Finding{
						Severity: SeverityWarning, Category: "nodes", Title: "PIDPressure",
						Detail: cond.Message, Remedy: "check for fork bombs",
						Score: 70, Object: node.Name,
					})
				}
			}
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "nodes", Title: "All nodes healthy"}}, nil
	}
	return findings, nil
}

func (e *Engine) NodeTaints() ([]Finding, error) {
	nodes, err := e.k8s.CoreV1().Nodes().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, node := range nodes.Items {
		for _, t := range node.Spec.Taints {
			findings = append(findings, Finding{
				Severity: SeverityInfo, Category: "nodes",
				Title: fmt.Sprintf("Taint: %s=%s:%s", t.Key, t.Value, t.Effect),
				Object: node.Name,
			})
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "nodes", Title: "No taints on any node"}}, nil
	}
	return findings, nil
}

func (e *Engine) NodeTop() ([]NodeMetric, error) {
	out, err := exec.CommandContext(e.ctx, "kubectl", "top", "nodes", "--no-headers").Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl top nodes failed: %w", err)
	}
	var results []NodeMetric
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		results = append(results, NodeMetric{
			Name: fields[0], CPUUsage: fields[1],
			CPUPercent: parsePercent(fields[2]),
			MemUsage: fields[3], MemPercent: parsePercent(fields[4]),
		})
	}
	return results, nil
}

func (e *Engine) CordonNode(nodeName string, drain bool) error {
	node, err := e.k8s.CoreV1().Nodes().Get(e.ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("node %s not found: %w", nodeName, err)
	}
	node.Spec.Unschedulable = true
	if _, err := e.k8s.CoreV1().Nodes().Update(e.ctx, node, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("cordon failed: %w", err)
	}
	fmt.Printf("✓ Node %s cordoned\n", nodeName)
	if drain {
		cmd := exec.CommandContext(e.ctx, "kubectl", "drain", nodeName,
			"--ignore-daemonsets", "--delete-emptydir-data", "--force")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("drain failed: %w", err)
		}
		fmt.Printf("✓ Node %s drained\n", nodeName)
	}
	return nil
}

func (e *Engine) DNSDiag() ([]Finding, error) {
	pods, err := e.k8s.CoreV1().Pods("kube-system").List(e.ctx, metav1.ListOptions{
		LabelSelector: "k8s-app=kube-dns",
	})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			findings = append(findings, Finding{
				Severity: SeverityCritical, Category: "network", Title: "CoreDNS pod not running",
				Detail: fmt.Sprintf("pod=%s phase=%s", pod.Name, pod.Status.Phase),
				Remedy: "kubectl describe pod " + pod.Name + " -n kube-system",
				Score: 90, Object: pod.Name, Namespace: "kube-system",
			})
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.RestartCount > 5 {
				findings = append(findings, Finding{
					Severity: SeverityWarning, Category: "network", Title: "CoreDNS high restarts",
					Detail: fmt.Sprintf("pod=%s restarts=%d", pod.Name, cs.RestartCount),
					Score: 75, Object: pod.Name, Namespace: "kube-system",
				})
			}
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "network", Title: "CoreDNS pods healthy"}}, nil
	}
	return findings, nil
}

func (e *Engine) ServiceEndpoints(svcName string) ([]Finding, error) {
	ns := e.namespace
	if ns == "" {
		ns = "default"
	}
	if svcName == "" {
		return []Finding{{Severity: SeverityInfo, Category: "network",
			Title: "Specify a service: ./k8s-doctor network svc <n> -n <namespace>"}}, nil
	}
	ep, err := e.k8s.CoreV1().Endpoints(ns).Get(e.ctx, svcName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("service %s not found in ns %s: %w", svcName, ns, err)
	}
	total := 0
	for _, s := range ep.Subsets {
		total += len(s.Addresses)
	}
	if total == 0 {
		return []Finding{{
			Severity: SeverityCritical, Category: "network", Title: "Service has no endpoints",
			Detail: fmt.Sprintf("service=%s — selector may not match any pods", svcName),
			Remedy: "kubectl get pods -l <selector> -n " + ns,
			Score: 88, Object: svcName, Namespace: ns,
		}}, nil
	}
	return []Finding{{Severity: SeverityInfo, Category: "network",
		Title: fmt.Sprintf("Service %s has %d healthy endpoint(s)", svcName, total),
		Object: svcName, Namespace: ns,
	}}, nil
}

func (e *Engine) NetworkPolicies() ([]Finding, error) {
	netpols, err := e.k8s.NetworkingV1().NetworkPolicies(e.ns()).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, np := range netpols.Items {
		if len(np.Spec.Ingress) == 0 && len(np.Spec.Egress) == 0 {
			findings = append(findings, Finding{
				Severity: SeverityWarning, Category: "network", Title: "Deny-all NetworkPolicy",
				Detail: fmt.Sprintf("policy=%s blocks all traffic", np.Name),
				Remedy: "add explicit ingress/egress rules",
				Score: 60, Object: np.Name, Namespace: np.Namespace,
			})
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "network",
			Title: fmt.Sprintf("%d NetworkPolicies — none flagged", len(netpols.Items))}}, nil
	}
	return findings, nil
}

func (e *Engine) IngressHealth() ([]Finding, error) {
	ingresses, err := e.k8s.NetworkingV1().Ingresses(e.ns()).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, ing := range ingresses.Items {
		if len(ing.Status.LoadBalancer.Ingress) == 0 {
			findings = append(findings, Finding{
				Severity: SeverityWarning, Category: "network", Title: "Ingress missing LB address",
				Detail: fmt.Sprintf("ingress=%s — ALB may not be provisioned", ing.Name),
				Remedy: "./k8s-doctor aws alb",
				Score: 70, Object: ing.Name, Namespace: ing.Namespace,
			})
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "network",
			Title: "All ingresses have load balancer addresses"}}, nil
	}
	return findings, nil
}

func (e *Engine) EC2NodeHealth(clusterName, region, profile string) ([]Finding, error) {
	args := []string{"ec2", "describe-instance-status",
		"--filters", "Name=tag:eks:cluster-name,Values=" + clusterName,
		"--query", "InstanceStatuses[?InstanceStatus.Status!='ok' || SystemStatus.Status!='ok'].[InstanceId,InstanceStatus.Status,SystemStatus.Status]",
		"--output", "text",
	}
	if region != "" {
		args = append(args, "--region", region)
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	out, err := exec.CommandContext(e.ctx, "aws", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("aws ec2 describe-instance-status failed: %w", err)
	}
	var findings []Finding
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		findings = append(findings, Finding{
			Severity: SeverityCritical, Category: "aws", Title: "EC2 status check FAILED",
			Detail: fmt.Sprintf("instance=%s status=%s/%s", fields[0], fields[1], fields[2]),
			Remedy: "aws ec2 terminate-instances --instance-ids " + fields[0],
			Score: 92, Object: fields[0],
		})
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "aws", Title: "All EC2 instance checks passing"}}, nil
	}
	return findings, nil
}

func (e *Engine) ALBHealth(clusterName, region, profile string) ([]Finding, error) {
	return []Finding{{Severity: SeverityInfo, Category: "aws",
		Title:  "ALB check — run: aws elbv2 describe-target-health --target-group-arn <arn>",
	}}, nil
}

func (e *Engine) SGAudit(clusterName, region, profile string) ([]Finding, error) {
	return []Finding{{Severity: SeverityInfo, Category: "aws",
		Title:  "SG audit",
		Detail: "aws ec2 describe-security-groups --filters Name=tag:aws:eks:cluster-name,Values=" + clusterName,
		Remedy: "ensure: control-plane→nodes 443/10250, nodes→nodes all, ALB→nodes 30000-32767",
	}}, nil
}

func (e *Engine) IAMAudit(clusterName, namespace, region, profile string) ([]Finding, error) {
	ns := namespace
	if ns == "" {
		ns = metav1.NamespaceAll
	}
	sas, err := e.k8s.CoreV1().ServiceAccounts(ns).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, sa := range sas.Items {
		roleARN := sa.Annotations["eks.amazonaws.com/role-arn"]
		if roleARN == "" {
			continue
		}
		findings = append(findings, Finding{
			Severity: SeverityInfo, Category: "aws", Title: "IRSA annotation found",
			Detail: fmt.Sprintf("sa=%s role=%s", sa.Name, roleARN),
			Remedy: "verify trust policy allows oidc provider for this cluster",
			Object: sa.Name, Namespace: sa.Namespace,
		})
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "aws",
			Title: "No IRSA-annotated service accounts found"}}, nil
	}
	return findings, nil
}

func (e *Engine) ASGStatus(clusterName, region, profile string) ([]ASGGroup, error) {
	args := []string{"autoscaling", "describe-auto-scaling-groups",
		"--filters", "Name=tag-key,Values=k8s.io/cluster/" + clusterName,
		"--query", "AutoScalingGroups[].[AutoScalingGroupName,MinSize,MaxSize,DesiredCapacity]",
		"--output", "text",
	}
	if region != "" {
		args = append(args, "--region", region)
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	out, err := exec.CommandContext(e.ctx, "aws", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("aws autoscaling failed: %w", err)
	}
	var groups []ASGGroup
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		groups = append(groups, ASGGroup{
			Name: fields[0], MinSize: parseInt32(fields[1]),
			MaxSize: parseInt32(fields[2]), DesiredCapacity: parseInt32(fields[3]),
			Status: "OK",
		})
	}
	return groups, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func parsePercent(s string) float64 {
	s = strings.TrimSuffix(s, "%")
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

func parseInt32(s string) int32 {
	var i int32
	fmt.Sscanf(s, "%d", &i)
	return i
}
EOF

echo "internal/diag/engine.go created"

echo "Creating internal/diag/snapshot.go..."
cat > internal/diag/snapshot.go << 'EOF'
package diag

import (
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ClusterSnapshot struct {
	ServerVersion  string
	HealthScore    int
	Nodes          []NodeSummary
	Namespaces     []NamespaceSummary
	TopConsumers   []PodConsumer
	PVCs           []PVCSummary
	Quotas         []QuotaUsage
	RecentWarnings []EventSummary
	CapturedAt     time.Time
}

type NodeSummary struct {
	Name         string
	Status       string
	CPUCapacity  string
	CPURequested string
	MemCapacity  string
	MemRequested string
}

type NamespaceSummary struct {
	Name         string
	Deployments  int
	StatefulSets int
	TotalPods    int
	RunningPods  int
	FailingPods  int
}

type PodConsumer struct {
	Name       string
	Namespace  string
	CPURequest string
	MemRequest string
	CPUMillis  int64
	MemBytes   int64
}

type PVCSummary struct {
	Name      string
	Namespace string
	Capacity  string
	Status    string
}

type QuotaUsage struct {
	Namespace   string
	Resource    string
	Used        string
	Hard        string
	UsedPercent float64
}

type EventSummary struct {
	Reason    string
	Message   string
	Count     int32
}

func (e *Engine) ClusterSnapshot() (*ClusterSnapshot, error) {
	snap := &ClusterSnapshot{CapturedAt: time.Now()}
	ver, err := e.k8s.Discovery().ServerVersion()
	if err == nil {
		snap.ServerVersion = ver.GitVersion
	} else {
		snap.ServerVersion = "unknown"
	}
	nodes, err := e.k8s.CoreV1().Nodes().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	allPods, err := e.k8s.CoreV1().Pods(metav1.NamespaceAll).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}
	cpuReqByNode := map[string]int64{}
	memReqByNode := map[string]int64{}
	for _, p := range allPods.Items {
		if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodPending {
			for _, c := range p.Spec.Containers {
				cpuReqByNode[p.Spec.NodeName] += c.Resources.Requests.Cpu().MilliValue()
				memReqByNode[p.Spec.NodeName] += c.Resources.Requests.Memory().Value()
			}
		}
	}
	healthDeductions := 0
	for _, node := range nodes.Items {
		ns := NodeSummary{
			Name: node.Name, Status: "Ready",
			CPUCapacity:  fmt.Sprintf("%dm", node.Status.Capacity.Cpu().MilliValue()),
			MemCapacity:  formatBytes(node.Status.Capacity.Memory().Value()),
			CPURequested: fmt.Sprintf("%dm", cpuReqByNode[node.Name]),
			MemRequested: formatBytes(memReqByNode[node.Name]),
		}
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue {
				ns.Status = "NotReady"
				healthDeductions += 20
			}
			if cond.Type == corev1.NodeMemoryPressure && cond.Status == corev1.ConditionTrue {
				healthDeductions += 15
			}
			if cond.Type == corev1.NodeDiskPressure && cond.Status == corev1.ConditionTrue {
				healthDeductions += 15
			}
		}
		snap.Nodes = append(snap.Nodes, ns)
	}
	nsList, err := e.k8s.CoreV1().Namespaces().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, ns := range nsList.Items {
		summary := NamespaceSummary{Name: ns.Name}
		deploys, _ := e.k8s.AppsV1().Deployments(ns.Name).List(e.ctx, metav1.ListOptions{})
		if deploys != nil {
			summary.Deployments = len(deploys.Items)
		}
		ss, _ := e.k8s.AppsV1().StatefulSets(ns.Name).List(e.ctx, metav1.ListOptions{})
		if ss != nil {
			summary.StatefulSets = len(ss.Items)
		}
		for _, p := range allPods.Items {
			if p.Namespace != ns.Name {
				continue
			}
			summary.TotalPods++
			if p.Status.Phase == corev1.PodRunning {
				summary.RunningPods++
			}
			for _, cs := range p.Status.ContainerStatuses {
				if cs.State.Waiting != nil &&
					(cs.State.Waiting.Reason == "CrashLoopBackOff" ||
						cs.State.Waiting.Reason == "ImagePullBackOff") {
					summary.FailingPods++
					healthDeductions += 5
					break
				}
			}
		}
		if summary.TotalPods > 0 || summary.Deployments > 0 {
			snap.Namespaces = append(snap.Namespaces, summary)
		}
	}
	sort.Slice(snap.Namespaces, func(i, j int) bool {
		return snap.Namespaces[i].FailingPods > snap.Namespaces[j].FailingPods
	})
	var consumers []PodConsumer
	for _, p := range allPods.Items {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		var cpuMillis, memBytes int64
		for _, c := range p.Spec.Containers {
			cpuMillis += c.Resources.Requests.Cpu().MilliValue()
			memBytes += c.Resources.Requests.Memory().Value()
		}
		consumers = append(consumers, PodConsumer{
			Name: p.Name, Namespace: p.Namespace,
			CPURequest: fmt.Sprintf("%dm", cpuMillis),
			MemRequest: formatBytes(memBytes),
			CPUMillis: cpuMillis, MemBytes: memBytes,
		})
	}
	sort.Slice(consumers, func(i, j int) bool {
		return consumers[i].MemBytes > consumers[j].MemBytes
	})
	snap.TopConsumers = consumers
	pvcs, _ := e.k8s.CoreV1().PersistentVolumeClaims(metav1.NamespaceAll).List(e.ctx, metav1.ListOptions{})
	if pvcs != nil {
		for _, pvc := range pvcs.Items {
			cap := "unknown"
			if storage, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
				cap = storage.String()
			}
			snap.PVCs = append(snap.PVCs, PVCSummary{
				Name: pvc.Name, Namespace: pvc.Namespace,
				Capacity: cap, Status: string(pvc.Status.Phase),
			})
			if pvc.Status.Phase != corev1.ClaimBound {
				healthDeductions += 5
			}
		}
	}
	quotas, _ := e.k8s.CoreV1().ResourceQuotas(metav1.NamespaceAll).List(e.ctx, metav1.ListOptions{})
	if quotas != nil {
		for _, q := range quotas.Items {
			for res, hard := range q.Status.Hard {
				used := q.Status.Used[res]
				hardVal := hard.MilliValue()
				pct := 0.0
				if hardVal > 0 {
					pct = float64(used.MilliValue()) / float64(hardVal) * 100
				}
				if pct >= 75 {
					snap.Quotas = append(snap.Quotas, QuotaUsage{
						Namespace: q.Namespace, Resource: string(res),
						Used: used.String(), Hard: hard.String(), UsedPercent: pct,
					})
				}
			}
		}
	}
	events, _ := e.k8s.CoreV1().Events(metav1.NamespaceAll).List(e.ctx, metav1.ListOptions{
		FieldSelector: "type=Warning",
	})
	if events != nil {
		cutoff := time.Now().Add(-1 * time.Hour)
		seen := map[string]bool{}
		for _, ev := range events.Items {
			if ev.LastTimestamp.Time.Before(cutoff) {
				continue
			}
			key := ev.Reason + "/" + ev.InvolvedObject.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			snap.RecentWarnings = append(snap.RecentWarnings, EventSummary{
				Reason: ev.Reason, Message: truncate(ev.Message, 80), Count: ev.Count,
			})
		}
		sort.Slice(snap.RecentWarnings, func(i, j int) bool {
			return snap.RecentWarnings[i].Count > snap.RecentWarnings[j].Count
		})
	}
	snap.HealthScore = 100 - healthDeductions
	if snap.HealthScore < 0 {
		snap.HealthScore = 0
	}
	return snap, nil
}

func formatBytes(b int64) string {
	if b == 0 {
		return "0"
	}
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fGi", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fMi", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0fKi", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%dB", b)
}
EOF

echo "internal/diag/snapshot.go created"

echo "Creating internal/diag/audit.go..."
cat > internal/diag/audit.go << 'EOF'
package diag

import (
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type AuditEntry struct {
	Timestamp       time.Time
	Kind            string
	Name            string
	Namespace       string
	Action          string
	FieldManager    string
	Detail          string
	CorrelatedFault string
	Mitigation      string
}

func (e *Engine) AuditLog(window time.Duration, filterKind, filterUser string) ([]AuditEntry, error) {
	cutoff := time.Now().Add(-window)
	var entries []AuditEntry
	ns := e.ns()
	events, err := e.k8s.CoreV1().Events(ns).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, ev := range events.Items {
		t := ev.LastTimestamp.Time
		if t.IsZero() {
			t = ev.EventTime.Time
		}
		if t.Before(cutoff) {
			continue
		}
		action := "UPDATE"
		switch ev.Reason {
		case "Created", "Started", "Pulled", "Scheduled":
			action = "CREATE"
		case "Killing", "Evicting":
			action = "DELETE"
		}
		kind := ev.InvolvedObject.Kind
		if filterKind != "" && !strings.EqualFold(kind, filterKind) {
			continue
		}
		fm := ev.ReportingComponent
		if fm == "" {
			fm = ev.Source.Component
		}
		if fm == "" {
			fm = "kubernetes"
		}
		if filterUser != "" && !strings.Contains(strings.ToLower(fm), strings.ToLower(filterUser)) {
			continue
		}
		entries = append(entries, AuditEntry{
			Timestamp: t, Kind: kind,
			Name: ev.InvolvedObject.Name, Namespace: ev.Namespace,
			Action: action, FieldManager: fm,
			Detail: truncate(ev.Message, 120),
		})
	}
	fieldEntries, err := e.managedFieldsAudit(cutoff, filterKind, filterUser)
	if err == nil {
		entries = append(entries, fieldEntries...)
	}
	faults, _ := e.PodHealth()
	faultMap := map[string]string{}
	for _, f := range faults {
		if f.Score > 0 && f.Object != "" {
			faultMap[f.Object] = f.Title
		}
	}
	for i, entry := range entries {
		if fault, ok := faultMap[entry.Name]; ok {
			entries[i].CorrelatedFault = fault
			entries[i].Mitigation = mitigationFor(fault, entry.Kind, entry.Name, entry.Namespace)
		}
	}
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].Timestamp.After(entries[j-1].Timestamp); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	seen := map[string]bool{}
	var deduped []AuditEntry
	for _, e := range entries {
		key := fmt.Sprintf("%s/%s/%s/%s", e.Kind, e.Name, e.FieldManager, e.Timestamp.Truncate(time.Minute))
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, e)
	}
	if len(deduped) > 100 {
		deduped = deduped[:100]
	}
	return deduped, nil
}

func (e *Engine) managedFieldsAudit(cutoff time.Time, filterKind, filterUser string) ([]AuditEntry, error) {
	var entries []AuditEntry
	ns := e.ns()
	if filterKind == "" || strings.EqualFold(filterKind, "Deployment") {
		deploys, err := e.k8s.AppsV1().Deployments(ns).List(e.ctx, metav1.ListOptions{})
		if err == nil {
			for _, obj := range deploys.Items {
				for _, mf := range obj.ManagedFields {
					if mf.Time == nil || mf.Time.Time.Before(cutoff) {
						continue
					}
					fm := mf.Manager
					if filterUser != "" && !strings.Contains(strings.ToLower(fm), strings.ToLower(filterUser)) {
						continue
					}
					op := "UPDATE"
					if strings.ToLower(string(mf.Operation)) == "create" {
						op = "CREATE"
					}
					entries = append(entries, AuditEntry{
						Timestamp: mf.Time.Time, Kind: "Deployment",
						Name: obj.Name, Namespace: obj.Namespace,
						Action: op, FieldManager: fm,
					})
				}
			}
		}
	}
	if filterKind == "" || strings.EqualFold(filterKind, "ConfigMap") {
		cms, err := e.k8s.CoreV1().ConfigMaps(ns).List(e.ctx, metav1.ListOptions{})
		if err == nil {
			for _, obj := range cms.Items {
				if obj.Namespace == "kube-system" {
					continue
				}
				for _, mf := range obj.ManagedFields {
					if mf.Time == nil || mf.Time.Time.Before(cutoff) {
						continue
					}
					fm := mf.Manager
					if filterUser != "" && !strings.Contains(strings.ToLower(fm), strings.ToLower(filterUser)) {
						continue
					}
					entries = append(entries, AuditEntry{
						Timestamp: mf.Time.Time, Kind: "ConfigMap",
						Name: obj.Name, Namespace: obj.Namespace,
						Action: "UPDATE", FieldManager: fm,
					})
				}
			}
		}
	}
	return entries, nil
}

func mitigationFor(fault, kind, name, namespace string) string {
	switch {
	case strings.Contains(fault, "CrashLoop"):
		return fmt.Sprintf("kubectl rollout undo deployment/%s -n %s", name, namespace)
	case strings.Contains(fault, "OOMKilled"):
		return fmt.Sprintf("increase memory limit for %s in ns %s", name, namespace)
	case strings.Contains(fault, "ImagePull"):
		return "check image tag and ECR permissions"
	case strings.Contains(fault, "Pending"):
		return "./k8s-doctor node pressure"
	default:
		return fmt.Sprintf("kubectl describe %s %s -n %s", strings.ToLower(kind), name, namespace)
	}
}
EOF

echo "internal/diag/audit.go created"

echo "Creating internal/diag/watch.go..."
cat > internal/diag/watch.go << 'EOF'
package diag

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

type WatchEvent struct {
	Timestamp    time.Time
	EventType    string
	Kind         string
	Name         string
	Namespace    string
	FieldManager string
}

func (e *Engine) WatchResources(kinds []string) (<-chan WatchEvent, error) {
	ch := make(chan WatchEvent, 100)
	watchAll := len(kinds) == 0
	wants := map[string]bool{}
	for _, k := range kinds {
		wants[k] = true
	}
	ns := e.namespace
	if ns == "" {
		ns = metav1.NamespaceAll
	}
	should := func(kind string) bool {
		if watchAll {
			return true
		}
		return wants[kind]
	}
	if should("Pod") {
		if pw, err := e.k8s.CoreV1().Pods(ns).Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(pw, "Pod", ch, e.ctx.Done())
		}
	}
	if should("Deployment") {
		if dw, err := e.k8s.AppsV1().Deployments(ns).Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(dw, "Deployment", ch, e.ctx.Done())
		}
	}
	if should("ConfigMap") {
		if cw, err := e.k8s.CoreV1().ConfigMaps(ns).Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(cw, "ConfigMap", ch, e.ctx.Done())
		}
	}
	if should("Service") {
		if sw, err := e.k8s.CoreV1().Services(ns).Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(sw, "Service", ch, e.ctx.Done())
		}
	}
	if should("Node") {
		if nw, err := e.k8s.CoreV1().Nodes().Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(nw, "Node", ch, e.ctx.Done())
		}
	}
	if should("StatefulSet") {
		if ssw, err := e.k8s.AppsV1().StatefulSets(ns).Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(ssw, "StatefulSet", ch, e.ctx.Done())
		}
	}
	if should("Secret") {
		if secw, err := e.k8s.CoreV1().Secrets(ns).Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(secw, "Secret", ch, e.ctx.Done())
		}
	}
	return ch, nil
}

func streamEvents(watcher watch.Interface, kind string, ch chan<- WatchEvent, done <-chan struct{}) {
	defer watcher.Stop()
	for {
		select {
		case <-done:
			return
		case ev, ok := <-watcher.ResultChan():
			if !ok {
				return
			}
			we := WatchEvent{Timestamp: time.Now(), EventType: string(ev.Type), Kind: kind}
			if obj, ok := ev.Object.(metav1.Object); ok {
				we.Name = obj.GetName()
				we.Namespace = obj.GetNamespace()
				mfs := obj.GetManagedFields()
				if len(mfs) > 0 {
					we.FieldManager = mfs[len(mfs)-1].Manager
				}
			}
			if we.Name == "" {
				continue
			}
			select {
			case ch <- we:
			default:
			}
		}
	}
}
EOF

echo "internal/diag/watch.go created"

echo "Creating internal/diag/predict.go..."
cat > internal/diag/predict.go << 'EOF'
package diag

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (e *Engine) PredictRisks() ([]Finding, error) {
	var findings []Finding
	pods, err := e.k8s.CoreV1().Pods(e.ns()).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	nodes, err := e.k8s.CoreV1().Nodes().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	deploys, _ := e.k8s.AppsV1().Deployments(e.ns()).List(e.ctx, metav1.ListOptions{})
	hpas, _ := e.k8s.AutoscalingV2().HorizontalPodAutoscalers(e.ns()).List(e.ctx, metav1.ListOptions{})
	pvcs, _ := e.k8s.CoreV1().PersistentVolumeClaims(e.ns()).List(e.ctx, metav1.ListOptions{})
	pdbs, _ := e.k8s.PolicyV1().PodDisruptionBudgets(e.ns()).List(e.ctx, metav1.ListOptions{})

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range pod.Spec.Containers {
			if c.Resources.Limits == nil ||
				(c.Resources.Limits.Cpu().IsZero() && c.Resources.Limits.Memory().IsZero()) {
				findings = append(findings, Finding{
					Severity: SeverityWarning, Category: "predict", Title: "No resource limits",
					Detail: fmt.Sprintf("container=%s in pod=%s", c.Name, pod.Name),
					Remedy: "add resources.limits to container spec",
					Score: 55, Object: pod.Name, Namespace: pod.Namespace,
				})
				break
			}
		}
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range pod.Spec.Containers {
			if c.Resources.Limits == nil || c.Resources.Requests == nil {
				continue
			}
			limMem := c.Resources.Limits.Memory().Value()
			reqMem := c.Resources.Requests.Memory().Value()
			if limMem > 0 && reqMem > 0 {
				pct := float64(reqMem) / float64(limMem) * 100
				if pct >= 85 {
					findings = append(findings, Finding{
						Severity: SeverityCritical, Category: "predict", Title: "OOM risk — memory request near limit",
						Detail: fmt.Sprintf("container=%s at %.0f%% of limit (%s/%s)", c.Name, pct, formatBytes(reqMem), formatBytes(limMem)),
						Remedy: fmt.Sprintf("increase memory limit for %s in ns %s", pod.Name, pod.Namespace),
						Score: 80, Object: pod.Name, Namespace: pod.Namespace,
					})
				}
			}
		}
	}

	seen := map[string]bool{}
	for _, pod := range pods.Items {
		for _, c := range pod.Spec.Containers {
			if strings.HasSuffix(c.Image, ":latest") || !strings.Contains(c.Image, ":") {
				key := pod.Namespace + "/" + c.Image
				if !seen[key] {
					seen[key] = true
					findings = append(findings, Finding{
						Severity: SeverityWarning, Category: "predict", Title: "Image uses :latest tag",
						Detail: fmt.Sprintf("image=%s — uncontrolled updates", c.Image),
						Remedy: "pin to a specific digest or semver tag",
						Score: 50, Object: pod.Name, Namespace: pod.Namespace,
					})
				}
			}
		}
	}

	if deploys != nil {
		for _, d := range deploys.Items {
			if d.Spec.Replicas != nil && *d.Spec.Replicas == 1 {
				findings = append(findings, Finding{
					Severity: SeverityWarning, Category: "predict", Title: "Single replica deployment",
					Detail: fmt.Sprintf("deployment=%s — any pod failure = full outage", d.Name),
					Remedy: fmt.Sprintf("kubectl scale deployment/%s --replicas=2 -n %s", d.Name, d.Namespace),
					Score: 60, Object: d.Name, Namespace: d.Namespace,
				})
			}
		}
	}

	if deploys != nil && pdbs != nil {
		pdbTargets := map[string]bool{}
		for _, pdb := range pdbs.Items {
			if pdb.Spec.Selector != nil {
				for k, v := range pdb.Spec.Selector.MatchLabels {
					pdbTargets[pdb.Namespace+"/"+k+"="+v] = true
				}
			}
		}
		for _, d := range deploys.Items {
			if d.Spec.Replicas != nil && *d.Spec.Replicas <= 1 {
				continue
			}
			hasPDB := false
			for k, v := range d.Spec.Template.Labels {
				if pdbTargets[d.Namespace+"/"+k+"="+v] {
					hasPDB = true
					break
				}
			}
			if !hasPDB {
				findings = append(findings, Finding{
					Severity: SeverityInfo, Category: "predict", Title: "No PodDisruptionBudget",
					Detail: fmt.Sprintf("deployment=%s", d.Name),
					Remedy: fmt.Sprintf("create a PDB for deployment/%s -n %s", d.Name, d.Namespace),
					Score: 35, Object: d.Name, Namespace: d.Namespace,
				})
			}
		}
	}

	if hpas != nil {
		for _, hpa := range hpas.Items {
			if hpa.Status.CurrentReplicas >= hpa.Spec.MaxReplicas {
				findings = append(findings, Finding{
					Severity: SeverityCritical, Category: "predict", Title: "HPA at max replicas — no scale headroom",
					Detail: fmt.Sprintf("hpa=%s current=%d max=%d", hpa.Name, hpa.Status.CurrentReplicas, hpa.Spec.MaxReplicas),
					Remedy: fmt.Sprintf("increase maxReplicas on hpa/%s -n %s", hpa.Name, hpa.Namespace),
					Score: 85, Object: hpa.Name, Namespace: hpa.Namespace,
				})
			}
		}
	}

	if pvcs != nil {
		for _, pvc := range pvcs.Items {
			if pvc.Status.Phase == corev1.ClaimPending {
				age := time.Since(pvc.CreationTimestamp.Time)
				if age > 2*time.Minute {
					findings = append(findings, Finding{
						Severity: SeverityWarning, Category: "predict", Title: "PVC stuck Pending",
						Detail: fmt.Sprintf("pvc=%s pending for %s", pvc.Name, age.Round(time.Second)),
						Remedy: fmt.Sprintf("kubectl describe pvc %s -n %s", pvc.Name, pvc.Namespace),
						Score: 70, Object: pvc.Name, Namespace: pvc.Namespace,
					})
				}
			}
		}
	}

	podsByNode := map[string]int{}
	for _, p := range pods.Items {
		podsByNode[p.Spec.NodeName]++
	}
	for _, node := range nodes.Items {
		maxPods := int64(110)
		if cap, ok := node.Status.Capacity.Pods().AsInt64(); ok {
			maxPods = cap
		}
		count := int64(podsByNode[node.Name])
		pct := float64(count) / float64(maxPods) * 100
		if pct >= 90 {
			findings = append(findings, Finding{
				Severity: SeverityCritical, Category: "predict", Title: "Node near pod capacity",
				Detail: fmt.Sprintf("node=%s pods=%d/%d (%.0f%%)", node.Name, count, maxPods, pct),
				Remedy: "add more nodes or reduce pod density",
				Score: 82, Object: node.Name,
			})
		}
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range pod.Spec.Containers {
			if c.LivenessProbe == nil {
				continue
			}
			p := c.LivenessProbe
			if p.FailureThreshold <= 1 && p.PeriodSeconds <= 5 {
				findings = append(findings, Finding{
					Severity: SeverityWarning, Category: "predict", Title: "Aggressive liveness probe",
					Detail: fmt.Sprintf("container=%s failureThreshold=%d period=%ds", c.Name, p.FailureThreshold, p.PeriodSeconds),
					Remedy: "increase failureThreshold to 3+ and periodSeconds to 10+",
					Score: 50, Object: pod.Name, Namespace: pod.Namespace,
				})
			}
		}
	}

	if len(findings) == 0 {
		findings = append(findings, Finding{
			Severity: SeverityInfo, Category: "predict",
			Title: "No predictive risks detected — cluster looks healthy",
		})
	}
	return findings, nil
}
EOF

echo "internal/diag/predict.go created"

echo "Creating internal/diag/diff.go..."
cat > internal/diag/diff.go << 'EOF'
package diag

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DiffEntry struct {
	Timestamp       time.Time
	Kind            string
	Name            string
	Namespace       string
	Field           string
	OldValue        string
	NewValue        string
	Action          string
	FieldManager    string
	CorrelatedFault string
	Mitigation      string
}

type StateSnapshot struct {
	CapturedAt    time.Time                 `json:"captured_at"`
	ResourceCount int                       `json:"resource_count"`
	Resources     map[string]ResourceRecord `json:"resources"`
}

type ResourceRecord struct {
	Kind            string            `json:"kind"`
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	ResourceVersion string            `json:"resource_version"`
	Generation      int64             `json:"generation"`
	Labels          map[string]string `json:"labels"`
	FieldManager    string            `json:"field_manager"`
	CapturedAt      time.Time         `json:"captured_at"`
}

func (e *Engine) CaptureStateSnapshot() (*StateSnapshot, error) {
	snap := &StateSnapshot{CapturedAt: time.Now(), Resources: map[string]ResourceRecord{}}
	ns := e.ns()
	if deploys, err := e.k8s.AppsV1().Deployments(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range deploys.Items {
			fm := ""
			if len(obj.ManagedFields) > 0 {
				fm = obj.ManagedFields[len(obj.ManagedFields)-1].Manager
			}
			snap.Resources[fmt.Sprintf("Deployment/%s/%s", obj.Namespace, obj.Name)] = ResourceRecord{
				Kind: "Deployment", Name: obj.Name, Namespace: obj.Namespace,
				ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
				Labels: obj.Labels, FieldManager: fm, CapturedAt: snap.CapturedAt,
			}
		}
	}
	if cms, err := e.k8s.CoreV1().ConfigMaps(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range cms.Items {
			if obj.Namespace == "kube-system" {
				continue
			}
			fm := ""
			if len(obj.ManagedFields) > 0 {
				fm = obj.ManagedFields[len(obj.ManagedFields)-1].Manager
			}
			snap.Resources[fmt.Sprintf("ConfigMap/%s/%s", obj.Namespace, obj.Name)] = ResourceRecord{
				Kind: "ConfigMap", Name: obj.Name, Namespace: obj.Namespace,
				ResourceVersion: obj.ResourceVersion, Labels: obj.Labels,
				FieldManager: fm, CapturedAt: snap.CapturedAt,
			}
		}
	}
	if svcs, err := e.k8s.CoreV1().Services(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range svcs.Items {
			snap.Resources[fmt.Sprintf("Service/%s/%s", obj.Namespace, obj.Name)] = ResourceRecord{
				Kind: "Service", Name: obj.Name, Namespace: obj.Namespace,
				ResourceVersion: obj.ResourceVersion, Labels: obj.Labels, CapturedAt: snap.CapturedAt,
			}
		}
	}
	if podList, err := e.k8s.CoreV1().Pods(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range podList.Items {
			snap.Resources[fmt.Sprintf("Pod/%s/%s", obj.Namespace, obj.Name)] = ResourceRecord{
				Kind: "Pod", Name: obj.Name, Namespace: obj.Namespace,
				ResourceVersion: obj.ResourceVersion, Labels: obj.Labels, CapturedAt: snap.CapturedAt,
			}
		}
	}
	snap.ResourceCount = len(snap.Resources)
	return snap, nil
}

func (e *Engine) SnapshotDiff(baseline *StateSnapshot) ([]DiffEntry, error) {
	current, err := e.CaptureStateSnapshot()
	if err != nil {
		return nil, err
	}
	var diffs []DiffEntry
	for key, cur := range current.Resources {
		if _, existed := baseline.Resources[key]; !existed {
			diffs = append(diffs, DiffEntry{
				Timestamp: current.CapturedAt, Kind: cur.Kind,
				Name: cur.Name, Namespace: cur.Namespace,
				Field: "existence", NewValue: "created",
				Action: "ADDED", FieldManager: cur.FieldManager,
			})
		}
	}
	for key, base := range baseline.Resources {
		cur, exists := current.Resources[key]
		if !exists {
			diffs = append(diffs, DiffEntry{
				Timestamp: current.CapturedAt, Kind: base.Kind,
				Name: base.Name, Namespace: base.Namespace,
				Field: "existence", OldValue: "existed", NewValue: "deleted",
				Action: "DELETED", FieldManager: base.FieldManager,
			})
			continue
		}
		if cur.ResourceVersion != base.ResourceVersion {
			fm := cur.FieldManager
			if fm == "" {
				fm = base.FieldManager
			}
			diffs = append(diffs, DiffEntry{
				Timestamp: current.CapturedAt, Kind: cur.Kind,
				Name: cur.Name, Namespace: cur.Namespace,
				Field: "spec/metadata",
				OldValue: fmt.Sprintf("rv=%s", base.ResourceVersion),
				NewValue: fmt.Sprintf("rv=%s gen=%d", cur.ResourceVersion, cur.Generation),
				Action: "UPDATED", FieldManager: fm,
			})
		}
	}
	faults, _ := e.PodHealth()
	faultMap := map[string]Finding{}
	for _, f := range faults {
		if f.Score > 0 && f.Object != "" {
			faultMap[f.Object] = f
		}
	}
	for i, d := range diffs {
		if fault, ok := faultMap[d.Name]; ok {
			diffs[i].CorrelatedFault = fault.Title
			diffs[i].Mitigation = mitigationFor(fault.Title, d.Kind, d.Name, d.Namespace)
		}
	}
	return diffs, nil
}

func (e *Engine) LiveDiff(window time.Duration) ([]DiffEntry, error) {
	cutoff := time.Now().Add(-window)
	var diffs []DiffEntry
	ns := e.ns()
	if deploys, err := e.k8s.AppsV1().Deployments(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range deploys.Items {
			for _, mf := range obj.ManagedFields {
				if mf.Time == nil || mf.Time.Time.Before(cutoff) {
					continue
				}
				replicas := int32(0)
				if obj.Spec.Replicas != nil {
					replicas = *obj.Spec.Replicas
				}
				diffs = append(diffs, DiffEntry{
					Timestamp: mf.Time.Time, Kind: "Deployment",
					Name: obj.Name, Namespace: obj.Namespace,
					Field: "spec",
					NewValue: fmt.Sprintf("generation=%d replicas=%d", obj.Generation, replicas),
					Action: "UPDATED", FieldManager: mf.Manager,
				})
			}
		}
	}
	if cms, err := e.k8s.CoreV1().ConfigMaps(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range cms.Items {
			if obj.Namespace == "kube-system" {
				continue
			}
			for _, mf := range obj.ManagedFields {
				if mf.Time == nil || mf.Time.Time.Before(cutoff) {
					continue
				}
				diffs = append(diffs, DiffEntry{
					Timestamp: mf.Time.Time, Kind: "ConfigMap",
					Name: obj.Name, Namespace: obj.Namespace,
					Field: "data",
					NewValue: fmt.Sprintf("keys=%d", len(obj.Data)),
					Action: "UPDATED", FieldManager: mf.Manager,
				})
			}
		}
	}
	faults, _ := e.PodHealth()
	faultMap := map[string]Finding{}
	for _, f := range faults {
		if f.Score > 0 && f.Object != "" {
			faultMap[f.Object] = f
		}
	}
	for i, d := range diffs {
		if fault, ok := faultMap[d.Name]; ok {
			diffs[i].CorrelatedFault = fault.Title
			diffs[i].Mitigation = mitigationFor(fault.Title, d.Kind, d.Name, d.Namespace)
		}
	}
	for i := 1; i < len(diffs); i++ {
		for j := i; j > 0 && diffs[j].Timestamp.After(diffs[j-1].Timestamp); j-- {
			diffs[j], diffs[j-1] = diffs[j-1], diffs[j]
		}
	}
	return diffs, nil
}
EOF

echo "internal/diag/diff.go created"

echo "Creating internal/output/printer.go..."
cat > internal/output/printer.go << 'EOF'
package output

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
)

type Printer struct{ format string }

func NewPrinter(format string) *Printer { return &Printer{format: format} }

func (p *Printer) Header(format string, args ...interface{}) {
	title := fmt.Sprintf(format, args...)
	switch p.format {
	case "markdown":
		fmt.Printf("# %s\n_Generated: %s_\n\n", title, time.Now().Format("2006-01-02 15:04:05 MST"))
	case "json":
	default:
		fmt.Printf("\n%s %s\n%s\n",
			color.CyanString("┌─"),
			color.New(color.FgCyan, color.Bold).Sprint(title),
			color.HiBlackString("   %s", time.Now().Format("15:04:05")),
		)
	}
}

func (p *Printer) Section(label string) {
	switch p.format {
	case "markdown":
		fmt.Printf("\n## %s\n\n", label)
	case "json":
	default:
		fmt.Printf("\n  %s %s\n", color.HiBlackString("▸"), color.New(color.Bold).Sprint(label))
	}
}

func (p *Printer) Findings(findings []diag.Finding) {
	switch p.format {
	case "json":
		b, _ := json.MarshalIndent(findings, "", "  ")
		fmt.Println(string(b))
	case "markdown":
		for _, f := range findings {
			icon := "ℹ️"
			if f.Severity == diag.SeverityCritical {
				icon = "🔴"
			} else if f.Severity == diag.SeverityWarning {
				icon = "🟡"
			}
			ref := f.Object
			if f.Namespace != "" && f.Object != "" {
				ref = f.Namespace + "/" + f.Object
			}
			if ref != "" {
				fmt.Printf("- %s **%s** `%s`\n", icon, f.Title, ref)
			} else {
				fmt.Printf("- %s **%s**\n", icon, f.Title)
			}
			if f.Detail != "" {
				fmt.Printf("  - %s\n", f.Detail)
			}
			if f.Remedy != "" {
				fmt.Printf("  - Fix: `%s`\n", f.Remedy)
			}
		}
	default:
		for _, f := range findings {
			icon, clr := severityStyle(f.Severity)
			obj := ""
			if f.Object != "" {
				ns := ""
				if f.Namespace != "" {
					ns = f.Namespace + "/"
				}
				obj = color.HiBlackString(" [%s%s]", ns, f.Object)
			}
			fmt.Printf("    %s %s%s\n", icon, clr(f.Title), obj)
			if f.Detail != "" {
				fmt.Printf("      %s %s\n", color.HiBlackString("↳"), f.Detail)
			}
			if f.Remedy != "" {
				fmt.Printf("      %s %s\n", color.CyanString("→"), color.CyanString(f.Remedy))
			}
		}
	}
}

func (p *Printer) RootCauseSummary(findings []diag.Finding) {
	var real []diag.Finding
	for _, f := range findings {
		if f.Score > 0 {
			real = append(real, f)
		}
	}
	if len(real) == 0 {
		return
	}
	sort.Slice(real, func(i, j int) bool { return real[i].Score > real[j].Score })
	switch p.format {
	case "markdown":
		fmt.Println("\n---\n\n## Root cause assessment\n")
		for i, f := range real {
			if i >= 3 {
				break
			}
			fmt.Printf("%d. **[%d%%]** %s — %s\n", i+1, f.Score, f.Title, f.Detail)
			if f.Remedy != "" {
				fmt.Printf("   - `%s`\n", f.Remedy)
			}
		}
	case "json":
		top := real
		if len(top) > 5 {
			top = top[:5]
		}
		b, _ := json.MarshalIndent(map[string]interface{}{"top_findings": top}, "", "  ")
		fmt.Println(string(b))
	default:
		fmt.Printf("\n  %s\n", color.New(color.FgYellow, color.Bold).Sprint("⚡ Root cause (top signals):"))
		for i, f := range real {
			if i >= 3 {
				break
			}
			icon, _ := severityStyle(f.Severity)
			filled := f.Score / 10
			bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
			barStr := color.GreenString(bar+" %d%%", f.Score)
			if f.Score >= 80 {
				barStr = color.RedString(bar+" %d%%", f.Score)
			} else if f.Score >= 60 {
				barStr = color.YellowString(bar+" %d%%", f.Score)
			}
			fmt.Printf("  %s [%s] %s\n", icon, barStr, color.New(color.Bold).Sprint(f.Title))
			if f.Object != "" {
				fmt.Printf("      object: %s/%s\n", f.Namespace, f.Object)
			}
			if f.Remedy != "" {
				fmt.Printf("      action: %s\n", color.CyanString(f.Remedy))
			}
		}
		fmt.Println()
	}
}

func severityStyle(s diag.Severity) (string, func(string, ...interface{}) string) {
	switch s {
	case diag.SeverityCritical:
		return "●", color.New(color.FgRed, color.Bold).Sprintf
	case diag.SeverityWarning:
		return "◐", color.New(color.FgYellow).Sprintf
	default:
		return "○", color.New(color.FgGreen).Sprintf
	}
}
EOF

echo ""
echo "✓ All files created successfully!"
echo ""
echo "Now run:"
echo "  go mod tidy"
echo "  go build -o k8s-doctor ."
echo "  ./k8s-doctor --help"
