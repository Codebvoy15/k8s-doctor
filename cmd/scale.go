package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var scaleCmd = &cobra.Command{
	Use:   "scale [deployment] [replicas]",
	Short: "Safely scale a deployment with confirmation and rollout watch",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		deployName := args[0]
		replicas, err := strconv.Atoi(args[1])
		if err != nil || replicas < 0 {
			return fmt.Errorf("replicas must be a non-negative number")
		}
		ns := namespace
		if ns == "" {
			ns = "default"
		}
		out, err := exec.CommandContext(ctx, "kubectl", "get", "deployment", deployName,
			"-n", ns, "-o", "jsonpath={.spec.replicas}").Output()
		if err != nil {
			return fmt.Errorf("deployment %s not found in namespace %s", deployName, ns)
		}
		current := strings.TrimSpace(string(out))
		currentInt, _ := strconv.Atoi(current)
		if replicas == 0 {
			fmt.Printf("\n  %s  Scaling to 0 will take the service completely down!\n", color.RedString("⚠⚠"))
		} else if replicas == 1 {
			fmt.Printf("\n  %s  Scaling to 1 removes fault tolerance.\n", color.YellowString("⚠"))
		}
		direction := color.GreenString("up")
		if replicas < currentInt {
			direction = color.YellowString("down")
		}
		fmt.Printf("\n  Scale %s: %s → %s replicas (scaling %s)\n",
			color.CyanString(deployName),
			color.New(color.Bold).Sprint(current),
			color.New(color.Bold).Sprint(replicas),
			direction)
		fmt.Print("  Confirm? [y/N]: ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("  Aborted.")
			return nil
		}
		color.Cyan("  → kubectl scale deployment/%s --replicas=%d -n %s", deployName, replicas, ns)
		scaleOut, err := exec.CommandContext(ctx, "kubectl", "scale",
			"deployment/"+deployName, fmt.Sprintf("--replicas=%d", replicas), "-n", ns).CombinedOutput()
		if err != nil {
			return fmt.Errorf("scale failed: %w\n%s", err, string(scaleOut))
		}
		color.Green("  ✓ %s", strings.TrimSpace(string(scaleOut)))
		if replicas > 0 {
			color.HiBlack("  Watching rollout (Ctrl+C to stop)...")
			statusOut, err := exec.CommandContext(ctx, "kubectl", "rollout", "status",
				"deployment/"+deployName, "-n", ns, "--timeout=3m").CombinedOutput()
			if err != nil {
				color.Yellow("  ⚠  Still in progress: %s", strings.TrimSpace(string(statusOut)))
			} else {
				color.Green("  ✓ Scale complete")
			}
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(scaleCmd)
}
