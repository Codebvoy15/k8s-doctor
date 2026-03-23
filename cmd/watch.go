package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
)

var watchKinds string

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Live stream of every resource change across the cluster in real time",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sig
			fmt.Println(color.HiBlackString("\n\nStopped."))
			cancel()
		}()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		kinds := []string{}
		if watchKinds != "" {
			kinds = strings.Split(watchKinds, ",")
		}
		fmt.Printf("%s\n", color.New(color.FgCyan, color.Bold).Sprintf(
			"LIVE WATCH — cluster: %s | ns: %s | Ctrl+C to stop", clusterName, nsDisplay()))
		fmt.Printf("  %-18s  %-10s  %-14s  %-28s  %-20s  %s\n",
			"TIME", "EVENT", "KIND", "NAME", "NAMESPACE", "BY")
		fmt.Println(color.HiBlackString("  " + strings.Repeat("─", 100)))
		eventCh, err := engine.WatchResources(kinds)
		if err != nil {
			return fmt.Errorf("watch failed: %w", err)
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case ev, ok := <-eventCh:
				if !ok {
					return nil
				}
				timeStr := ev.Timestamp.Format("15:04:05.000")
				eventFn := color.GreenString
				eventLabel := "ADD    "
				switch ev.EventType {
				case "MODIFIED":
					eventFn = color.YellowString
					eventLabel = "UPDATE "
				case "DELETED":
					eventFn = color.RedString
					eventLabel = "DELETE "
				}
				criticalKinds := map[string]bool{
					"Deployment": true, "StatefulSet": true, "DaemonSet": true,
					"ConfigMap": true, "Secret": true, "Service": true, "Ingress": true,
				}
				star := ""
				if criticalKinds[ev.Kind] {
					star = color.YellowString(" ★")
				}
				fm := ev.FieldManager
				if fm == "" {
					fm = "unknown"
				}
				name := ev.Name
				if len(name) > 28 {
					name = name[:25] + "..."
				}
				ns := ev.Namespace
				if len(ns) > 20 {
					ns = ns[:17] + "..."
				}
				fmt.Printf("  %-18s  %s  %-14s  %-28s  %-20s  %s\n",
					color.HiBlackString(timeStr),
					eventFn(eventLabel),
					ev.Kind+star,
					name, ns,
					color.CyanString(fm),
				)
			}
		}
	},
}

func init() {
	watchCmd.Flags().StringVar(&watchKinds, "kinds", "",
		"comma-separated kinds to watch (default: all). e.g. Deployment,ConfigMap,Secret")
	rootCmd.AddCommand(watchCmd)
}
