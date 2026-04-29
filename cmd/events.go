// ── events.go ─────────────────────────────────────────────────────────────────
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
	Short: "Chronological event timeline",
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

		warnings := 0
		for _, ev := range events {
			if ev.Type == "Warning" {
				warnings++
			}
		}

		fmt.Printf("\nevents  cluster=%s  window=%s  total=%d  warnings=%d  %s\n",
			color.New(color.FgWhite, color.Bold).Sprint(clusterName),
			eventsWindow,
			len(events),
			warnings,
			color.HiBlackString(time.Now().Format("15:04:05")),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 100)))

		if len(events) == 0 {
			fmt.Printf("\n  no events in this window\n\n")
			return nil
		}

		fmt.Printf("\n%-18s  %-8s  %-14s  %-26s  %-14s  %s\n",
			color.HiBlackString("time"),
			color.HiBlackString("type"),
			color.HiBlackString("reason"),
			color.HiBlackString("object"),
			color.HiBlackString("namespace"),
			color.HiBlackString("message"),
		)
		fmt.Println(color.HiBlackString(strings.Repeat("─", 100)))

		for _, ev := range events {
			typeFn := color.HiBlackString
			if ev.Type == "Warning" {
				typeFn = color.YellowString
			}
			countStr := ""
			if ev.Count > 1 {
				countStr = color.HiBlackString(" x%d", ev.Count)
			}
			fmt.Printf("%-18s  %-8s  %-14s  %-26s  %-14s  %s%s\n",
				color.HiBlackString(ev.LastSeen.Format("01-02 15:04:05")),
				typeFn(ev.Type),
				ev.Reason,
				truncateStr(ev.ObjectName, 26),
				truncateStr(ev.Namespace, 14),
				truncateStr(ev.Message, 60),
				countStr,
			)
		}
		fmt.Println()
		return nil
	},
}

func init() {
	eventsCmd.Flags().StringVar(&eventsWindow, "window", "1h", "how far back to look (e.g. 30m, 1h, 6h)")
	eventsCmd.Flags().StringVar(&eventsKind, "kind", "", "filter by object kind (e.g. Pod, Node)")
	eventsCmd.Flags().BoolVar(&eventsWarning, "warning", false, "show only Warning events")
	rootCmd.AddCommand(eventsCmd)
}
