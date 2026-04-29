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

var certWarnDays int

var certCmd = &cobra.Command{
	Use:   "cert",
	Short: "TLS certificate expiry check",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		certs, err := engine.CertCheck(namespace, certWarnDays)
		if err != nil {
			return fmt.Errorf("cert check failed: %w", err)
		}

		fmt.Printf("\ncert  cluster=%s  warn-days=%d  %s\n",
			color.New(color.FgWhite, color.Bold).Sprint(clusterName),
			certWarnDays,
			color.HiBlackString(time.Now().Format("15:04:05")),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))

		if len(certs) == 0 {
			fmt.Printf("\n  no certificates expiring within %d days\n\n", certWarnDays)
			return nil
		}

		fmt.Printf("\n%-36s  %-16s  %-12s  %-10s  %s\n",
			color.HiBlackString("secret"),
			color.HiBlackString("namespace"),
			color.HiBlackString("expires"),
			color.HiBlackString("days left"),
			color.HiBlackString("status"),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 88)))

		for _, c := range certs {
			statusFn := color.GreenString
			status := "ok"
			if c.DaysLeft < 0 {
				statusFn = color.RedString
				status = "expired"
			} else if c.DaysLeft < 7 {
				statusFn = color.RedString
				status = "critical"
			} else if c.DaysLeft < 30 {
				statusFn = color.YellowString
				status = "warning"
			}
			fmt.Printf("%-36s  %-16s  %-12s  %-10s  %s\n",
				truncateStr(c.Name, 36),
				c.Namespace,
				c.Expiry.Format("2006-01-02"),
				statusFn("%d days", c.DaysLeft),
				statusFn(status),
			)
			if c.CommonName != "" {
				fmt.Printf("  cn=%s\n", color.HiBlackString(c.CommonName))
			}
		}
		fmt.Println()
		return nil
	},
}

func init() {
	certCmd.Flags().IntVar(&certWarnDays, "days", 30, "warn if expiring within N days")
	rootCmd.AddCommand(certCmd)
}
