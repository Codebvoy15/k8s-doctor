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
	Short: "RBAC permissions audit — who can do what",
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

		fmt.Printf("\nrbac  cluster=%s  %s\n",
			color.New(color.FgWhite, color.Bold).Sprint(clusterName),
			color.HiBlackString(time.Now().Format("15:04:05")),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))

		if len(result.DangerousBindings) > 0 {
			fmt.Printf("\nDANGEROUS PERMISSIONS  %s\n",
				color.RedString("%d", len(result.DangerousBindings)))
			fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
			for _, b := range result.DangerousBindings {
				fmt.Printf("  subject    %s\n", color.RedString(b.Subject))
				fmt.Printf("  role       %s\n", color.YellowString(b.RoleName))
				fmt.Printf("  namespace  %s\n", b.Namespace)
				fmt.Printf("  reason     %s\n\n", color.RedString(b.Risk))
			}
		}

		if len(result.ServiceAccounts) > 0 {
			fmt.Printf("\nSERVICE ACCOUNTS  %s\n",
				color.HiBlackString("%d", len(result.ServiceAccounts)))
			fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
			fmt.Printf("%-30s  %-20s  %-30s  %s\n",
				color.HiBlackString("service account"),
				color.HiBlackString("namespace"),
				color.HiBlackString("role"),
				color.HiBlackString("scope"),
			)
			fmt.Println(color.HiBlackString(strings.Repeat("─", 86)))
			for _, sa := range result.ServiceAccounts {
				scope := "namespace"
				if sa.ClusterWide {
					scope = color.YellowString("cluster-wide")
				}
				fmt.Printf("%-30s  %-20s  %-30s  %s\n",
					truncateStr(sa.Name, 30),
					sa.Namespace,
					color.HiBlackString(sa.RoleName),
					scope,
				)
			}
			fmt.Println()
		}

		if len(result.Users) > 0 {
			fmt.Printf("\nUSERS AND GROUPS\n")
			fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
			fmt.Printf("%-30s  %-20s  %s\n",
				color.HiBlackString("subject"),
				color.HiBlackString("namespace"),
				color.HiBlackString("role"),
			)
			fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
			for _, u := range result.Users {
				fmt.Printf("%-30s  %-20s  %s\n",
					u.Name, u.Namespace,
					color.HiBlackString(u.RoleName),
				)
			}
			fmt.Println()
		}

		dangerous := len(result.DangerousBindings)
		dStr := color.GreenString("0")
		if dangerous > 0 {
			dStr = color.RedString("%d", dangerous)
		}
		fmt.Printf("summary  %d service accounts  %d users  %s dangerous bindings\n\n",
			len(result.ServiceAccounts), len(result.Users), dStr)
		return nil
	},
}

func init() {
	rbacCmd.Flags().StringVar(&rbacSubject, "subject", "", "filter by subject name")
	rootCmd.AddCommand(rbacCmd)
}
