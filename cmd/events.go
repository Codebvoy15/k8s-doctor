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

var (
	eventsWindow  string
	eventsKind    string
	eventsWarning bool
)

var eventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Clean chronological event timeline — the cluster's flight recorder",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		window, err := time.ParseDuration(eventsWindow)
		if err != nil {
			return fmt.Errorf("invalid --window: use 30m, 1h, 2h")
		}
		events, err := engine.EventTimeline(window, eventsKind, eventsWarning)
		if err != nil {
			return fmt.Errorf("events failed: %w", err)
		}
		fmt.Printf("\n%s\n\n",
			color.New(color.FgCyan, color.Bold).Sprintf("EVENT TIMELINE — last %s | %d events", eventsWindow, len(events)))
		if len(events) == 0 {
			fmt.Println(color.GreenString("  No events found in this window."))
			return nil
		}
		fmt.Printf("  %-18s  %-10s  %-14s  %-28s  %-16s  %s\n",
			"TIME", "TYPE", "REASON", "OBJECT", "NAMESPACE", "MESSAGE")
		fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 110)))
		for _, ev := range events {
			typeFn := color.GreenString
			typeLabel := "Normal "
			if ev.Type == "Warning" {
				typeFn = color.YellowString
				typeLabel = "Warning"
			}
			obj := ev.ObjectName
			if len(obj) > 28 {
				obj = obj[:25] + "..."
			}
			msg := ev.Message
			if len(msg) > 60 {
				msg = msg[:57] + "..."
			}
			countStr := ""
			if ev.Count > 1 {
				countStr = color.HiBlackString(" [x%d]", ev.Count)
			}
			fmt.Printf("  %-18s  %s  %-14s  %-28s  %-16s  %s%s\n",
				color.HiBlackString(ev.LastSeen.Format("01-02 15:04:05")),
				typeFn(typeLabel), ev.Reason, obj, ev.Namespace, msg, countStr)
		}
		warnings := 0
		for _, ev := range events {
			if ev.Type == "Warning" {
				warnings++
			}
		}
		warnStr := color.GreenString("%d", warnings)
		if warnings > 5 {
			warnStr = color.RedString("%d", warnings)
		} else if warnings > 0 {
			warnStr = color.YellowString("%d", warnings)
		}
		fmt.Printf("\n  Total: %d events  |  %s warnings\n\n", len(events), warnStr)
		return nil
	},
}

func init() {
	eventsCmd.Flags().StringVar(&eventsWindow, "window", "1h", "how far back to look (e.g. 30m, 1h, 6h)")
	eventsCmd.Flags().StringVar(&eventsKind, "kind", "", "filter by object kind (e.g. Pod, Node)")
	eventsCmd.Flags().BoolVar(&eventsWarning, "warning", false, "show only Warning events")
	rootCmd.AddCommand(eventsCmd)
}
