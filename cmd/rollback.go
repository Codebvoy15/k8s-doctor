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
			fmt.Printf("\n  no recent deployment changes in the last 24h\n\n")
			return nil
		}

		fmt.Printf("\nrollback  cluster=%s  %s\n",
			color.New(color.FgWhite, color.Bold).Sprint(clusterName),
			color.HiBlackString(time.Now().Format("15:04:05")),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 72)))
		fmt.Printf("\n%-4s  %-30s  %-16s  %-20s  %s\n",
			color.HiBlackString("#"),
			color.HiBlackString("deployment"),
			color.HiBlackString("namespace"),
			color.HiBlackString("changed by"),
			color.HiBlackString("when"),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 80)))

		for i, h := range history {
			if i >= 10 {
				break
			}
			age := time.Since(h.ChangedAt).Round(time.Minute)
			fmt.Printf("%-4d  %-30s  %-16s  %-20s  %s ago\n",
				i+1,
				h.Name,
				h.Namespace,
				color.HiBlackString(h.ChangedBy),
				age,
			)
			if h.ImageChange != "" {
				fmt.Printf("       %s\n", color.HiBlackString(h.ImageChange))
			}
		}

		fmt.Print("\nenter number to rollback (q to quit): ")
		var input string
		fmt.Scanln(&input)
		if input == "q" || input == "Q" || input == "" {
			fmt.Println("aborted")
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
	histOut, _ := exec.CommandContext(ctx, "kubectl", "rollout", "history",
		"deployment/"+name, "-n", ns).Output()
	if len(histOut) > 0 {
		fmt.Printf("\nrollout history:\n")
		for _, line := range strings.Split(string(histOut), "\n") {
			if line != "" {
				fmt.Printf("  %s\n", color.HiBlackString(line))
			}
		}
	}

	fmt.Printf("\nrollback  deployment/%s  ns=%s\n", name, ns)
	fmt.Print("confirm [y/N]: ")
	var confirm string
	fmt.Scanln(&confirm)
	if confirm != "y" && confirm != "Y" {
		fmt.Println("aborted")
		return nil
	}

	fmt.Printf("running  kubectl rollout undo deployment/%s -n %s\n", name, ns)
	out, err := exec.CommandContext(ctx, "kubectl", "rollout", "undo",
		"deployment/"+name, "-n", ns).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rollback failed: %w\n%s", err, string(out))
	}
	fmt.Printf("done     %s\n", strings.TrimSpace(string(out)))

	statusCmd := exec.CommandContext(ctx, "kubectl", "rollout", "status",
		"deployment/"+name, "-n", ns, "--timeout=2m")
	if err := statusCmd.Run(); err != nil {
		fmt.Printf("status   check: kubectl rollout status deployment/%s -n %s\n", name, ns)
	} else {
		fmt.Printf("status   %s\n", color.GreenString("healthy"))
	}
	fmt.Println()
	return nil
}

var _ = diag.RollbackTarget{}

func init() {
	rootCmd.AddCommand(rollbackCmd)
}
