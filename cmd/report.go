package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/Codebvoy15/k8s-doctor/internal/diag"
	"github.com/Codebvoy15/k8s-doctor/internal/output"
)

var (
	ticketID     string
	slackWebhook string
	reportSince  string
)

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Full incident report — terminal, markdown, or post to Slack",
	Long: `Runs all diagnostics and produces a structured incident report.

  ./k8s-doctor report
  ./k8s-doctor report --ticket INC-1234 -o markdown
  ./k8s-doctor report --ticket INC-1234 -o markdown > report.md
  ./k8s-doctor report --slack https://hooks.slack.com/...
  ./k8s-doctor report --since 2h
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		since := time.Hour
		if reportSince != "" {
			d, err := time.ParseDuration(reportSince)
			if err != nil {
				return fmt.Errorf("invalid --since: use 1h, 2h, 30m")
			}
			since = d
		}
		webhook := slackWebhook
		if webhook == "" {
			webhook = os.Getenv("K8S_DOCTOR_SLACK_WEBHOOK")
		}
		isSlack := webhook != ""
		outFmt := outputFmt
		if outFmt == "terminal" && !isSlack {
			outFmt = "markdown"
		}
		printer := output.NewPrinter(outFmt)
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		printer.Header("INCIDENT REPORT — cluster: %s | ticket: %s | %s",
			clusterName, ticketID, time.Now().Format("2006-01-02 15:04:05"))
		color.Cyan("→ Running full diagnostic suite...")
		var all []diag.Finding
		run := func(name string, fn func() ([]diag.Finding, error)) {
			printer.Section(name)
			findings, err := fn()
			if err != nil {
				color.Yellow("  ⚠  %s failed: %v", name, err)
				return
			}
			printer.Findings(findings)
			all = append(all, findings...)
		}
		run("Pod health", engine.PodHealth)
		run("Pending pods", engine.PendingPods)
		run("Warning events", func() ([]diag.Finding, error) {
			return engine.RecentWarningEvents(since)
		})
		run("High restart pods", func() ([]diag.Finding, error) {
			return engine.HighRestartPods(3)
		})
		run("Node pressure", engine.NodePressure)
		run("DNS diagnostics", engine.DNSDiag)
		run("Ingress health", engine.IngressHealth)
		run("Predictive risks", engine.PredictRisks)
		printer.Section("Root cause summary")
		printer.RootCauseSummary(all)
		if isSlack {
			return postToSlack(webhook, all, ticketID, clusterName)
		}
		return nil
	},
}

func postToSlack(webhook string, findings []diag.Finding, ticket, cluster string) error {
	color.Cyan("→ Posting report to Slack...")
	critical, warnings := 0, 0
	var topFindings []string
	for _, f := range findings {
		if f.Score > 0 {
			if f.Severity == diag.SeverityCritical {
				critical++
			} else if f.Severity == diag.SeverityWarning {
				warnings++
			}
			if len(topFindings) < 3 {
				obj := ""
				if f.Object != "" {
					obj = fmt.Sprintf(" (`%s/%s`)", f.Namespace, f.Object)
				}
				topFindings = append(topFindings, fmt.Sprintf("• *%s*%s — %s", f.Title, obj, f.Detail))
			}
		}
	}
	emoji := ":white_check_mark:"
	headerColor := "#36a64f"
	status := "Healthy"
	if critical > 0 {
		emoji = ":red_circle:"
		headerColor = "#ff0000"
		status = "CRITICAL"
	} else if warnings > 0 {
		emoji = ":warning:"
		headerColor = "#ffaa00"
		status = "Degraded"
	}
	ticketStr := ""
	if ticket != "" {
		ticketStr = fmt.Sprintf(" | Ticket: %s", ticket)
	}
	text := ""
	for _, f := range topFindings {
		text += f + "\n"
	}
	if text == "" {
		text = "No active issues detected."
	}
	payload := map[string]interface{}{
		"attachments": []map[string]interface{}{
			{
				"color": headerColor,
				"blocks": []map[string]interface{}{
					{"type": "header", "text": map[string]string{
						"type": "plain_text",
						"text": fmt.Sprintf("%s k8s-doctor — %s", emoji, status),
					}},
					{"type": "section", "fields": []map[string]string{
						{"type": "mrkdwn", "text": fmt.Sprintf("*Cluster:*\n%s", cluster)},
						{"type": "mrkdwn", "text": fmt.Sprintf("*Critical:*\n%d", critical)},
						{"type": "mrkdwn", "text": fmt.Sprintf("*Warnings:*\n%d", warnings)},
					}},
					{"type": "section", "text": map[string]string{
						"type": "mrkdwn",
						"text": fmt.Sprintf("*Top findings:*\n%s", text),
					}},
					{"type": "context", "elements": []map[string]string{
						{"type": "mrkdwn", "text": fmt.Sprintf("%s%s", time.Now().Format("2006-01-02 15:04:05 MST"), ticketStr)},
					}},
				},
			},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to build Slack payload: %w", err)
	}
	resp, err := http.Post(webhook, "application/json", bytes.NewBuffer(b))
	if err != nil {
		return fmt.Errorf("failed to post to Slack: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Slack returned status %d", resp.StatusCode)
	}
	color.Green("✓ Report posted to Slack")
	return nil
}

func init() {
	reportCmd.Flags().StringVar(&ticketID, "ticket", "", "ticket ID (e.g. INC-1234)")
	reportCmd.Flags().StringVar(&slackWebhook, "slack", "", "Slack webhook URL")
	reportCmd.Flags().StringVar(&reportSince, "since", "1h", "how far back to look (e.g. 1h, 2h, 30m)")
	rootCmd.AddCommand(reportCmd)
}
