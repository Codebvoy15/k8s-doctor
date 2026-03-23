package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback [deployment-name]",
	Short: "Safely revert the last deployment change",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		if len(args) == 1 {
			return doRollback(ctx, args[0], namespace)
		}
		history, err := engine.RollbackTargets(namespace)
		if err != nil {
			return fmt.Errorf("could not fetch rollout history: %w", err)
		}
		if len(history) == 0 {
			fmt.Println(color.YellowString("  No recent deployment changes found in the last 24h."))
			return nil
		}
		fmt.Printf("\n%s\n\n", color.New(color.FgCyan, color.Bold).Sprint("RECENT DEPLOYMENT CHANGES — pick one to rollback"))
		fmt.Printf("  %-4s  %-30s  %-16s  %-20s  %s\n", "#", "DEPLOYMENT", "NAMESPACE", "CHANGED BY", "WHEN")
		fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 80)))
		for i, h := range history {
			if i >= 10 {
				break
			}
			age := time.Since(h.ChangedAt).Round(time.Minute)
			fmt.Printf("  %-4d  %-30s  %-16s  %-20s  %s ago\n",
				i+1, color.CyanString(h.Name), h.Namespace,
				color.HiBlackString(h.ChangedBy), age)
			if h.ImageChange != "" {
				fmt.Printf("        %s %s\n", color.HiBlackString("↳"), color.YellowString(h.ImageChange))
			}
		}
		fmt.Print(color.CyanString("\n  Enter number to rollback (or q to quit): "))
		var input string
		fmt.Scanln(&input)
		if input == "q" || input == "Q" || input == "" {
			fmt.Println("  Aborted.")
			return nil
		}
		var idx int
		if _, err := fmt.Sscanf(input, "%d", &idx); err != nil || idx < 1 || idx > len(history) {
			return fmt.Errorf("invalid selection")
		}
		selected := history[idx-1]
		return doRollback(ctx, selected.Name, selected.Namespace)
	},
}

func doRollback(ctx context.Context, name, ns string) error {
	if ns == "" {
		ns = "default"
	}
	histOut, _ := exec.CommandContext(ctx, "kubectl", "rollout", "history", "deployment/"+name, "-n", ns).Output()
	if len(histOut) > 0 {
		fmt.Printf("\n  %s\n", color.HiBlackString("Rollout history:"))
		for _, line := range strings.Split(string(histOut), "\n") {
			if line != "" {
				fmt.Printf("  %s\n", color.HiBlackString(line))
			}
		}
	}
	fmt.Printf("\n  %s Roll back deployment/%s in namespace %s? [y/N]: ",
		color.YellowString("⚠"), color.New(color.Bold).Sprint(name), color.CyanString(ns))
	var confirm string
	fmt.Scanln(&confirm)
	if confirm != "y" && confirm != "Y" {
		fmt.Println("  Aborted.")
		return nil
	}
	color.Cyan("  → kubectl rollout undo deployment/%s -n %s", name, ns)
	out, err := exec.CommandContext(ctx, "kubectl", "rollout", "undo", "deployment/"+name, "-n", ns).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rollback failed: %w\n%s", err, string(out))
	}
	color.Green("  ✓ %s", strings.TrimSpace(string(out)))
	color.HiBlack("  Watching rollout status...")
	statusCmd := exec.CommandContext(ctx, "kubectl", "rollout", "status", "deployment/"+name, "-n", ns, "--timeout=2m")
	if err := statusCmd.Run(); err != nil {
		color.Yellow("  ⚠  Check: kubectl rollout status deployment/%s -n %s", name, ns)
	} else {
		color.Green("  ✓ Rollback complete and healthy")
	}
	return nil
}

var _ = diag.RollbackTarget{}

func init() {
	rootCmd.AddCommand(rollbackCmd)
}
