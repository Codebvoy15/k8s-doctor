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
	Short: "Network diagnostics: dns, svc, netpol, ingress",
}

var networkDNSCmd = &cobra.Command{
	Use:   "dns",
	Short: "CoreDNS health and resolution errors",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("network dns  cluster=%s", clusterName)
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
	Short: "Check a service has healthy endpoints",
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
		printer.Header("network svc  %s", svcName)
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
		printer.Header("network netpol  cluster=%s  ns=%s", clusterName, nsDisplay())
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
	Short: "Ingress and ALB health",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		printer := output.NewPrinter(outputFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("network ingress  cluster=%s", clusterName)
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
