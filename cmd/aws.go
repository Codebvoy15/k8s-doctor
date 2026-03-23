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
	Short: "EC2 instance status checks for EKS nodes (system + instance checks)",
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
	Short: "Audit EKS security group rules for common misconfigs",
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
	Short: "Check Auto Scaling Groups — capacity, desired vs in-service, last activity",
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
			if g.LastScalingActivity != "" {
				fmt.Printf("  %s %s\n", color.HiBlackString("↳"), g.LastScalingActivity)
			}
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
}
