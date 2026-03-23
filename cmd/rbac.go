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

var rbacSubject string

var rbacCmd = &cobra.Command{
	Use:   "rbac",
	Short: "Who can do what — RBAC permissions audit",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		result, err := engine.RBACAudit(namespace, rbacSubject)
		if err != nil {
			return fmt.Errorf("RBAC audit failed: %w", err)
		}
		fmt.Printf("\n%s\n\n", color.New(color.FgCyan, color.Bold).Sprint("RBAC AUDIT"))
		if len(result.DangerousBindings) > 0 {
			fmt.Printf("  %s\n\n",
				color.New(color.FgRed, color.Bold).Sprintf("Dangerous permissions (%d):", len(result.DangerousBindings)))
			for _, b := range result.DangerousBindings {
				fmt.Printf("  %s  %s\n", color.RedString("●"), color.New(color.Bold).Sprint(b.Subject))
				fmt.Printf("     role:      %s\n", color.YellowString(b.RoleName))
				fmt.Printf("     namespace: %s\n", b.Namespace)
				fmt.Printf("     reason:    %s\n\n", color.RedString(b.Risk))
			}
		}
		if len(result.ServiceAccounts) > 0 {
			fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Service accounts"))
			fmt.Printf("  %-30s  %-20s  %-30s  %s\n", "SERVICE ACCOUNT", "NAMESPACE", "ROLE", "SCOPE")
			fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 86)))
			for _, sa := range result.ServiceAccounts {
				scope := "namespace"
				if sa.ClusterWide {
					scope = color.YellowString("cluster-wide")
				}
				name := sa.Name
				if len(name) > 30 {
					name = name[:27] + "..."
				}
				fmt.Printf("  %-30s  %-20s  %-30s  %s\n",
					name, sa.Namespace, color.CyanString(sa.RoleName), scope)
			}
			fmt.Println()
		}
		if len(result.Users) > 0 {
			fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Users and groups"))
			fmt.Printf("  %-30s  %-20s  %s\n", "SUBJECT", "NAMESPACE", "ROLE")
			fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 82)))
			for _, u := range result.Users {
				fmt.Printf("  %-30s  %-20s  %s\n", u.Name, u.Namespace, color.CyanString(u.RoleName))
			}
			fmt.Println()
		}
		dangerous := len(result.DangerousBindings)
		dStr := color.GreenString("0")
		if dangerous > 0 {
			dStr = color.RedString("%d", dangerous)
		}
		fmt.Printf("  Summary: %d service accounts  |  %d users  |  %s dangerous bindings\n\n",
			len(result.ServiceAccounts), len(result.Users), dStr)
		return nil
	},
}

func init() {
	rbacCmd.Flags().StringVar(&rbacSubject, "subject", "", "filter by subject name")
	rootCmd.AddCommand(rbacCmd)
}
