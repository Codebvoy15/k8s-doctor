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
	Short: "Live stream of every resource change in real time",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sig
			fmt.Println(color.HiBlackString("\nstopped"))
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

		fmt.Printf("\nwatch  cluster=%s  ns=%s  Ctrl+C to stop\n",
			color.New(color.FgWhite, color.Bold).Sprint(clusterName),
			nsDisplay(),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 100)))
		fmt.Printf("%-18s  %-8s  %-14s  %-28s  %-20s  %s\n",
			color.HiBlackString("time"),
			color.HiBlackString("event"),
			color.HiBlackString("kind"),
			color.HiBlackString("name"),
			color.HiBlackString("namespace"),
			color.HiBlackString("by"),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 100)))

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
				eventFn := color.GreenString
				eventLabel := "add   "
				switch ev.EventType {
				case "MODIFIED":
					eventFn = color.YellowString
					eventLabel = "update"
				case "DELETED":
					eventFn = color.RedString
					eventLabel = "delete"
				}
				criticalKinds := map[string]bool{
					"Deployment": true, "StatefulSet": true, "DaemonSet": true,
					"ConfigMap": true, "Secret": true, "Service": true, "Ingress": true,
				}
				star := ""
				if criticalKinds[ev.Kind] {
					star = " *"
				}
				fm := ev.FieldManager
				if fm == "" {
					fm = "unknown"
				}
				fmt.Printf("%-18s  %-8s  %-14s  %-28s  %-20s  %s\n",
					color.HiBlackString(ev.Timestamp.Format("15:04:05.000")),
					eventFn(eventLabel),
					ev.Kind+star,
					truncateStr(ev.Name, 28),
					truncateStr(ev.Namespace, 20),
					color.HiBlackString(fm),
				)
			}
		}
	},
}

func init() {
	watchCmd.Flags().StringVar(&watchKinds, "kinds", "", "comma-separated kinds to watch (default: all)")
	rootCmd.AddCommand(watchCmd)
}
