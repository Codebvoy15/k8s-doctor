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
	Short: "Scale a deployment with confirmation",
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

		fmt.Printf("\nscale  %s  %s -> %d  ns=%s\n",
			color.New(color.FgWhite, color.Bold).Sprint(deployName),
			current, replicas, ns,
		)
		if replicas == 0 {
			fmt.Printf("warn   scaling to 0 will take the service completely down\n")
		} else if replicas == 1 {
			fmt.Printf("warn   scaling to 1 removes fault tolerance\n")
		}
		if replicas < currentInt {
			fmt.Printf("dir    scaling down\n")
		} else {
			fmt.Printf("dir    scaling up\n")
		}

		fmt.Print("confirm [y/N]: ")
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("aborted")
			return nil
		}

		scaleOut, err := exec.CommandContext(ctx, "kubectl", "scale",
			"deployment/"+deployName,
			fmt.Sprintf("--replicas=%d", replicas),
			"-n", ns,
		).CombinedOutput()
		if err != nil {
			return fmt.Errorf("scale failed: %w\n%s", err, string(scaleOut))
		}
		fmt.Printf("done   %s\n", strings.TrimSpace(string(scaleOut)))

		if replicas > 0 {
			fmt.Fprintf(nil, "")
			statusOut, err := exec.CommandContext(ctx, "kubectl", "rollout", "status",
				"deployment/"+deployName, "-n", ns, "--timeout=3m").CombinedOutput()
			if err != nil {
				fmt.Printf("status %s\n", color.YellowString(strings.TrimSpace(string(statusOut))))
			} else {
				fmt.Printf("status %s\n", color.GreenString("complete"))
			}
		}
		fmt.Println()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(scaleCmd)
}
