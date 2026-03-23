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
	Short: "TLS certificate expiry check — find certs expiring soon",
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
		fmt.Printf("\n%s\n\n", color.New(color.FgCyan, color.Bold).Sprint("TLS CERTIFICATE CHECK"))
		if len(certs) == 0 {
			fmt.Printf("  %s  No certificates expiring within %d days.\n\n", color.GreenString("✓"), certWarnDays)
			return nil
		}
		fmt.Printf("  %-36s  %-16s  %-12s  %-12s  %s\n",
			"SECRET", "NAMESPACE", "EXPIRES", "DAYS LEFT", "STATUS")
		fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 92)))
		for _, c := range certs {
			statusFn := color.GreenString
			status := "OK"
			if c.DaysLeft < 0 {
				statusFn = color.RedString
				status = "EXPIRED"
			} else if c.DaysLeft < 7 {
				statusFn = color.RedString
				status = "CRITICAL"
			} else if c.DaysLeft < 30 {
				statusFn = color.YellowString
				status = "WARNING"
			}
			name := c.Name
			if len(name) > 36 {
				name = name[:33] + "..."
			}
			fmt.Printf("  %-36s  %-16s  %-12s  %-12s  %s\n",
				name, c.Namespace,
				c.Expiry.Format("2006-01-02"),
				statusFn(fmt.Sprintf("%d days", c.DaysLeft)),
				statusFn(status))
			if c.CommonName != "" {
				fmt.Printf("  %s CN=%s\n", color.HiBlackString("    ↳"), c.CommonName)
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
