#!/bin/bash

# Run this from inside your k8s-doctor folder
# Adds all v3 commands — diagnose, top, events, rbac, cert, rollback, cost, scale, slack report

echo "Adding v3 files..."

# ── cmd/diagnose.go ───────────────────────────────────────────────────────────
cat > cmd/diagnose.go << 'EOF'
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

var diagnoseWindow string

var diagnoseCmd = &cobra.Command{
	Use:   "diagnose",
	Short: "One command root cause analysis — runs everything and gives you ONE answer",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		window, err := time.ParseDuration(diagnoseWindow)
		if err != nil {
			return fmt.Errorf("invalid --window: use 30m, 1h, 2h")
		}
		fmt.Printf("\n%s\n", color.New(color.FgCyan, color.Bold).Sprint("╔══════════════════════════════════════════════════════════════╗"))
		fmt.Printf("%s\n", color.CyanString("  DIAGNOSE — running full correlation analysis..."))
		fmt.Printf("%s\n\n", color.New(color.FgCyan, color.Bold).Sprint("╚══════════════════════════════════════════════════════════════╝"))
		color.HiBlack("  [1/4] Checking pod health...")
		podFindings, _ := engine.PodHealth()
		color.HiBlack("  [2/4] Checking pending pods and events...")
		pendingFindings, _ := engine.PendingPods()
		eventFindings, _ := engine.RecentWarningEvents(window)
		color.HiBlack("  [3/4] Checking recent changes...")
		diffs, _ := engine.LiveDeepDiff(window)
		color.HiBlack("  [4/4] Checking audit log...")
		auditEntries, _ := engine.AuditLog(window, "", "")
		fmt.Println()
		var activeFaults []diag.Finding
		for _, f := range podFindings {
			if f.Score > 0 {
				activeFaults = append(activeFaults, f)
			}
		}
		for _, f := range pendingFindings {
			if f.Score > 0 {
				activeFaults = append(activeFaults, f)
			}
		}
		for _, f := range eventFindings {
			if f.Score > 40 {
				activeFaults = append(activeFaults, f)
			}
		}
		for i := 1; i < len(activeFaults); i++ {
			for j := i; j > 0 && activeFaults[j].Score > activeFaults[j-1].Score; j-- {
				activeFaults[j], activeFaults[j-1] = activeFaults[j-1], activeFaults[j]
			}
		}
		if len(activeFaults) == 0 && len(diffs) == 0 {
			fmt.Printf("  %s  No active problems found and no recent changes detected.\n\n", color.GreenString("✓"))
			fmt.Printf("  %s\n", color.HiBlackString("Run ./k8s-doctor predict for proactive risks."))
			return nil
		}
		if len(activeFaults) > 0 {
			fmt.Printf("  %s\n\n", color.New(color.FgRed, color.Bold).Sprint("Active problems detected:"))
			for i, f := range activeFaults {
				if i >= 5 {
					break
				}
				icon := color.YellowString("◐")
				if f.Severity == diag.SeverityCritical {
					icon = color.RedString("●")
				}
				fmt.Printf("  %s  %s\n", icon, color.New(color.Bold).Sprint(f.Title))
				if f.Object != "" {
					fmt.Printf("     object: %s/%s\n", f.Namespace, f.Object)
				}
				if f.Detail != "" {
					fmt.Printf("     detail: %s\n", color.HiBlackString(f.Detail))
				}
			}
			fmt.Println()
		}
		if len(diffs) > 0 {
			fmt.Printf("  %s\n\n", color.New(color.FgYellow, color.Bold).Sprint("Recent changes in this window:"))
			shown := 0
			for _, d := range diffs {
				if shown >= 5 {
					break
				}
				if strings.Contains(d.OldValue, "use --save") {
					continue
				}
				fmt.Printf("  %s  %s/%s  %s\n",
					color.YellowString("△"),
					color.CyanString(d.Kind),
					color.New(color.Bold).Sprint(d.Name),
					color.HiBlackString("by "+d.ChangedBy),
				)
				fmt.Printf("     field: %s\n", d.Field)
				if d.OldValue != "" {
					fmt.Printf("     %s → %s\n",
						color.RedString(d.OldValue),
						color.GreenString(d.NewValue),
					)
				}
				shown++
			}
			fmt.Println()
		}
		fmt.Printf("%s\n\n", color.New(color.FgYellow, color.Bold).Sprint("  ⚡ Root cause assessment:"))
		rc := correlateRootCause(activeFaults, diffs, auditEntries)
		if rc.Conclusion != "" {
			fmt.Printf("  %s\n\n", color.New(color.FgWhite, color.Bold).Sprint(rc.Conclusion))
		}
		if rc.Evidence != "" {
			fmt.Printf("  %s %s\n", color.HiBlackString("Evidence:  "), rc.Evidence)
		}
		if rc.ChangedBy != "" {
			fmt.Printf("  %s %s\n", color.HiBlackString("Changed by:"), color.CyanString(rc.ChangedBy))
		}
		if rc.ChangedAt != "" {
			fmt.Printf("  %s %s\n", color.HiBlackString("Changed at:"), rc.ChangedAt)
		}
		if rc.Remedy != "" {
			fmt.Printf("\n  %s\n", color.New(color.FgGreen, color.Bold).Sprint("Recommended action:"))
			fmt.Printf("  %s\n", color.GreenString(rc.Remedy))
		}
		if rc.Confidence > 0 {
			filled := rc.Confidence / 10
			bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
			barStr := color.GreenString(bar)
			if rc.Confidence < 50 {
				barStr = color.RedString(bar)
			} else if rc.Confidence < 80 {
				barStr = color.YellowString(bar)
			}
			fmt.Printf("\n  %s %s (%d%% confidence)\n", color.HiBlackString("Confidence:"), barStr, rc.Confidence)
		}
		fmt.Printf("\n  %s\n", color.HiBlackString("Next steps:"))
		fmt.Printf("  %s\n", color.HiBlackString("  ./k8s-doctor triage          — detailed pod health"))
		fmt.Printf("  %s\n", color.HiBlackString("  ./k8s-doctor events           — full event timeline"))
		fmt.Printf("  %s\n\n", color.HiBlackString("  ./k8s-doctor report           — generate ticket report"))
		return nil
	},
}

type RootCause struct {
	Conclusion string
	Evidence   string
	ChangedBy  string
	ChangedAt  string
	Remedy     string
	Confidence int
}

func correlateRootCause(faults []diag.Finding, diffs []diag.DeepDiffEntry, audit []diag.AuditEntry) RootCause {
	if len(faults) == 0 {
		return RootCause{
			Conclusion: "No active pod faults — problem may be at the infrastructure layer.",
			Remedy:     "./k8s-doctor aws ec2  →  check EC2 instance health",
			Confidence: 30,
		}
	}
	topFault := faults[0]
	for _, d := range diffs {
		if d.CorrelatedFault != "" || strings.HasPrefix(topFault.Object, d.Name) || d.Name == topFault.Object {
			remedy := d.Mitigation
			if remedy == "" {
				remedy = fmt.Sprintf("kubectl rollout undo deployment/%s -n %s", d.Name, d.Namespace)
			}
			changedAt := ""
			if !d.Timestamp.IsZero() {
				changedAt = d.Timestamp.Format("2006-01-02 15:04:05")
			}
			return RootCause{
				Conclusion: fmt.Sprintf("%s on %s/%s is likely caused by a recent %s change.", topFault.Title, topFault.Namespace, topFault.Object, d.Kind),
				Evidence:   fmt.Sprintf("%s field '%s' changed: %s → %s", d.Kind, d.Field, d.OldValue, d.NewValue),
				ChangedBy:  d.ChangedBy,
				ChangedAt:  changedAt,
				Remedy:     remedy,
				Confidence: 85,
			}
		}
	}
	for _, a := range audit {
		if strings.HasPrefix(topFault.Object, a.Name) || a.Name == topFault.Object {
			remedy := a.Mitigation
			if remedy == "" {
				remedy = fmt.Sprintf("kubectl rollout undo deployment/%s -n %s", a.Name, a.Namespace)
			}
			return RootCause{
				Conclusion: fmt.Sprintf("%s on %s/%s — a %s change was detected around the same time.", topFault.Title, topFault.Namespace, topFault.Object, a.Kind),
				Evidence:   fmt.Sprintf("%s '%s' was %s", a.Kind, a.Name, a.Action),
				ChangedBy:  a.FieldManager,
				ChangedAt:  a.Timestamp.Format("2006-01-02 15:04:05"),
				Remedy:     remedy,
				Confidence: 65,
			}
		}
	}
	remedy := topFault.Remedy
	if remedy == "" {
		remedy = fmt.Sprintf("kubectl describe pod -l app=%s -n %s", topFault.Object, topFault.Namespace)
	}
	return RootCause{
		Conclusion: fmt.Sprintf("%s on %s/%s — no recent changes detected that correlate directly.", topFault.Title, topFault.Namespace, topFault.Object),
		Evidence:   topFault.Detail,
		Remedy:     remedy,
		Confidence: 45,
	}
}

func init() {
	diagnoseCmd.Flags().StringVar(&diagnoseWindow, "window", "1h", "how far back to look (e.g. 30m, 1h, 2h)")
	rootCmd.AddCommand(diagnoseCmd)
}
EOF

# ── cmd/top.go ────────────────────────────────────────────────────────────────
cat > cmd/top.go << 'EOF'
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

var topCmd = &cobra.Command{
	Use:   "top",
	Short: "Who is eating your cluster — pods and nodes sorted by actual consumption",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		result, err := engine.TopConsumers(topSort, topLimit)
		if err != nil {
			return fmt.Errorf("top failed (is metrics-server running?): %w", err)
		}
		fmt.Printf("\n  %s\n", color.New(color.Bold).Sprint("Nodes"))
		fmt.Printf("  %-44s  %10s  %10s  %10s  %10s\n", "NAME", "CPU", "CPU%", "MEMORY", "MEM%")
		fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 88)))
		for _, n := range result.Nodes {
			cpuFn := color.GreenString
			memFn := color.GreenString
			if n.CPUPercent > 80 {
				cpuFn = color.RedString
			} else if n.CPUPercent > 60 {
				cpuFn = color.YellowString
			}
			if n.MemPercent > 80 {
				memFn = color.RedString
			} else if n.MemPercent > 60 {
				memFn = color.YellowString
			}
			fmt.Printf("  %-44s  %10s  %10s  %10s  %10s\n",
				n.Name, cpuFn(n.CPUUsage), cpuFn(fmt.Sprintf("%.0f%%", n.CPUPercent)),
				memFn(n.MemUsage), memFn(fmt.Sprintf("%.0f%%", n.MemPercent)))
		}
		fmt.Printf("\n  %s\n", color.New(color.Bold).Sprint("Top pods by "+topSort))
		fmt.Printf("  %-48s  %-20s  %10s  %10s\n", "POD", "NAMESPACE", "CPU", "MEMORY")
		fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 92)))
		for i, p := range result.Pods {
			if i >= topLimit {
				break
			}
			name := p.Name
			if len(name) > 48 {
				name = name[:45] + "..."
			}
			fmt.Printf("  %-48s  %-20s  %10s  %10s\n",
				name, p.Namespace, color.YellowString(p.CPUUsage), color.YellowString(p.MemUsage))
		}
		if len(result.NoisyNeighbours) > 0 {
			fmt.Printf("\n  %s\n", color.New(color.FgYellow, color.Bold).Sprint("Noisy neighbours:"))
			for _, n := range result.NoisyNeighbours {
				fmt.Printf("  %s  %s in ns/%s — CPU: %s  Mem: %s\n",
					color.YellowString("◐"), color.New(color.Bold).Sprint(n.PodName),
					n.Namespace, color.YellowString(n.CPUUsage), color.YellowString(n.MemUsage))
			}
		}
		fmt.Println()
		return nil
	},
}

var (
	topSort  string
	topLimit int
)

func init() {
	topCmd.Flags().StringVar(&topSort, "sort", "memory", "sort by: memory | cpu")
	topCmd.Flags().IntVar(&topLimit, "limit", 20, "number of pods to show")
	rootCmd.AddCommand(topCmd)
}
EOF

# ── cmd/events.go ─────────────────────────────────────────────────────────────
cat > cmd/events.go << 'EOF'
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
EOF

# ── cmd/rbac.go ───────────────────────────────────────────────────────────────
cat > cmd/rbac.go << 'EOF'
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

var rbacSubject string

var rbacCmd = &cobra.Command{
	Use:   "rbac",
	Short: "Who can do what — RBAC permissions audit",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		result, err := engine.RBACAudit(namespace, rbacSubject)
		if err != nil {
			return fmt.Errorf("RBAC audit failed: %w", err)
		}
		fmt.Printf("\n%s\n\n", color.New(color.FgCyan, color.Bold).Sprint("RBAC AUDIT"))
		if len(result.DangerousBindings) > 0 {
			fmt.Printf("  %s\n\n",
				color.New(color.FgRed, color.Bold).Sprintf("Dangerous permissions (%d):", len(result.DangerousBindings)))
			for _, b := range result.DangerousBindings {
				fmt.Printf("  %s  %s\n", color.RedString("●"), color.New(color.Bold).Sprint(b.Subject))
				fmt.Printf("     role:      %s\n", color.YellowString(b.RoleName))
				fmt.Printf("     namespace: %s\n", b.Namespace)
				fmt.Printf("     reason:    %s\n\n", color.RedString(b.Risk))
			}
		}
		if len(result.ServiceAccounts) > 0 {
			fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Service accounts"))
			fmt.Printf("  %-30s  %-20s  %-30s  %s\n", "SERVICE ACCOUNT", "NAMESPACE", "ROLE", "SCOPE")
			fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 86)))
			for _, sa := range result.ServiceAccounts {
				scope := "namespace"
				if sa.ClusterWide {
					scope = color.YellowString("cluster-wide")
				}
				name := sa.Name
				if len(name) > 30 {
					name = name[:27] + "..."
				}
				fmt.Printf("  %-30s  %-20s  %-30s  %s\n",
					name, sa.Namespace, color.CyanString(sa.RoleName), scope)
			}
			fmt.Println()
		}
		if len(result.Users) > 0 {
			fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Users and groups"))
			fmt.Printf("  %-30s  %-20s  %s\n", "SUBJECT", "NAMESPACE", "ROLE")
			fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 82)))
			for _, u := range result.Users {
				fmt.Printf("  %-30s  %-20s  %s\n", u.Name, u.Namespace, color.CyanString(u.RoleName))
			}
			fmt.Println()
		}
		dangerous := len(result.DangerousBindings)
		dStr := color.GreenString("0")
		if dangerous > 0 {
			dStr = color.RedString("%d", dangerous)
		}
		fmt.Printf("  Summary: %d service accounts  |  %d users  |  %s dangerous bindings\n\n",
			len(result.ServiceAccounts), len(result.Users), dStr)
		return nil
	},
}

func init() {
	rbacCmd.Flags().StringVar(&rbacSubject, "subject", "", "filter by subject name")
	rootCmd.AddCommand(rbacCmd)
}
EOF

# ── cmd/cert.go ───────────────────────────────────────────────────────────────
cat > cmd/cert.go << 'EOF'
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
EOF

# ── cmd/rollback.go ───────────────────────────────────────────────────────────
cat > cmd/rollback.go << 'EOF'
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
EOF

# ── cmd/cost.go ───────────────────────────────────────────────────────────────
cat > cmd/cost.go << 'EOF'
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

var costCmd = &cobra.Command{
	Use:   "cost",
	Short: "Resource waste analysis — find over-provisioned and idle workloads",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		result, err := engine.CostAnalysis(namespace)
		if err != nil {
			return fmt.Errorf("cost analysis failed: %w", err)
		}
		fmt.Printf("\n%s\n\n", color.New(color.FgCyan, color.Bold).Sprint("RESOURCE WASTE ANALYSIS"))
		if len(result.OverProvisioned) > 0 {
			fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Over-provisioned pods (request >> actual usage)"))
			fmt.Printf("  %-44s  %-16s  %-12s  %-12s  %s\n", "POD", "NAMESPACE", "CPU REQ", "MEM REQ", "WASTE")
			fmt.Println("  " + color.HiBlackString(strings.Repeat("─", 92)))
			for _, p := range result.OverProvisioned {
				name := p.Name
				if len(name) > 44 {
					name = name[:41] + "..."
				}
				wasteFn := color.YellowString
				if p.WasteScore > 70 {
					wasteFn = color.RedString
				}
				fmt.Printf("  %-44s  %-16s  %-12s  %-12s  %s\n",
					name, p.Namespace, p.CPURequest, p.MemRequest, wasteFn(fmt.Sprintf("%d/100", p.WasteScore)))
				if p.Recommendation != "" {
					fmt.Printf("  %s %s\n", color.HiBlackString("    ↳"), color.HiBlackString(p.Recommendation))
				}
			}
			fmt.Println()
		}
		if len(result.IdleNamespaces) > 0 {
			fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Idle namespaces (no running pods)"))
			for _, ns := range result.IdleNamespaces {
				fmt.Printf("  %s  %s\n", color.HiBlackString("○"), ns)
			}
			fmt.Println()
		}
		if len(result.UnderutilisedNodes) > 0 {
			fmt.Printf("  %s\n", color.New(color.Bold).Sprint("Under-utilised nodes (<20% CPU and memory)"))
			for _, n := range result.UnderutilisedNodes {
				fmt.Printf("  %s  %-44s  CPU: %s  Mem: %s\n",
					color.HiBlackString("○"), n.Name,
					color.GreenString("%.0f%%", n.CPUPercent),
					color.GreenString("%.0f%%", n.MemPercent))
			}
			fmt.Println()
		}
		fmt.Printf("  %d over-provisioned  |  %d idle namespaces  |  %d under-utilised nodes\n",
			len(result.OverProvisioned), len(result.IdleNamespaces), len(result.UnderutilisedNodes))
		if result.EstimatedWasteCPU != "" {
			fmt.Printf("  Estimated wasted CPU: %s  |  Memory: %s\n",
				color.YellowString(result.EstimatedWasteCPU), color.YellowString(result.EstimatedWasteMemory))
		}
		fmt.Println()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(costCmd)
}
EOF

# ── cmd/scale.go ──────────────────────────────────────────────────────────────
cat > cmd/scale.go << 'EOF'
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
EOF

# ── cmd/report.go — replace with Slack support ───────────────────────────────
cat > cmd/report.go << 'EOF'
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
EOF

echo "All cmd/ files created."
echo ""
echo "Creating internal/diag/ files..."

# ── internal/diag/top.go ──────────────────────────────────────────────────────
cat > internal/diag/top.go << 'EOF'
package diag

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

type TopResult struct {
	Nodes           []NodeMetric
	Pods            []PodMetric
	NoisyNeighbours []NoisyNeighbour
}

type PodMetric struct {
	Name      string
	Namespace string
	CPUUsage  string
	MemUsage  string
	CPUMillis int64
	MemMi     int64
}

type NoisyNeighbour struct {
	PodName   string
	Namespace string
	CPUUsage  string
	MemUsage  string
}

func (e *Engine) TopConsumers(sortBy string, limit int) (*TopResult, error) {
	result := &TopResult{}
	nodeOut, err := exec.CommandContext(e.ctx, "kubectl", "top", "nodes", "--no-headers").Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl top nodes failed (is metrics-server running?): %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(nodeOut)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		result.Nodes = append(result.Nodes, NodeMetric{
			Name:       fields[0],
			CPUUsage:   fields[1],
			CPUPercent: parsePercent(fields[2]),
			MemUsage:   fields[3],
			MemPercent: parsePercent(fields[4]),
		})
	}
	args := []string{"top", "pods", "--no-headers", "--all-namespaces"}
	if e.namespace != "" {
		args = []string{"top", "pods", "--no-headers", "-n", e.namespace}
	}
	podOut, err := exec.CommandContext(e.ctx, "kubectl", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl top pods failed: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(podOut)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		var pm PodMetric
		if e.namespace == "" && len(fields) >= 4 {
			pm = PodMetric{Namespace: fields[0], Name: fields[1], CPUUsage: fields[2], MemUsage: fields[3]}
		} else {
			pm = PodMetric{Namespace: e.namespace, Name: fields[0], CPUUsage: fields[1], MemUsage: fields[2]}
		}
		pm.CPUMillis = parseCPUMillis(pm.CPUUsage)
		pm.MemMi = parseMemMi(pm.MemUsage)
		result.Pods = append(result.Pods, pm)
	}
	if sortBy == "cpu" {
		sort.Slice(result.Pods, func(i, j int) bool { return result.Pods[i].CPUMillis > result.Pods[j].CPUMillis })
	} else {
		sort.Slice(result.Pods, func(i, j int) bool { return result.Pods[i].MemMi > result.Pods[j].MemMi })
	}
	for i, p := range result.Pods {
		if i >= 3 {
			break
		}
		if p.CPUMillis > 500 || p.MemMi > 512 {
			result.NoisyNeighbours = append(result.NoisyNeighbours, NoisyNeighbour{
				PodName: p.Name, Namespace: p.Namespace, CPUUsage: p.CPUUsage, MemUsage: p.MemUsage,
			})
		}
	}
	return result, nil
}

func parseCPUMillis(s string) int64 {
	s = strings.TrimSuffix(s, "m")
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}

func parseMemMi(s string) int64 {
	s = strings.TrimSuffix(s, "Mi")
	s = strings.TrimSuffix(s, "Gi")
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}
EOF

# ── internal/diag/events.go ───────────────────────────────────────────────────
cat > internal/diag/events.go << 'EOF'
package diag

import (
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type EventEntry struct {
	LastSeen   time.Time
	Type       string
	Reason     string
	ObjectKind string
	ObjectName string
	Namespace  string
	Message    string
	Count      int32
}

func (e *Engine) EventTimeline(window time.Duration, filterKind string, warningOnly bool) ([]EventEntry, error) {
	ns := e.ns()
	cutoff := time.Now().Add(-window)
	fieldSelector := ""
	if warningOnly {
		fieldSelector = "type=Warning"
	}
	events, err := e.k8s.CoreV1().Events(ns).List(e.ctx, metav1.ListOptions{FieldSelector: fieldSelector})
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}
	seen := map[string]bool{}
	var entries []EventEntry
	for _, ev := range events.Items {
		t := ev.LastTimestamp.Time
		if t.IsZero() {
			t = ev.EventTime.Time
		}
		if t.IsZero() || t.Before(cutoff) {
			continue
		}
		if filterKind != "" && !strings.EqualFold(ev.InvolvedObject.Kind, filterKind) {
			continue
		}
		key := fmt.Sprintf("%s/%s/%s", ev.Reason, ev.InvolvedObject.Name, ev.Namespace)
		if seen[key] {
			for i, entry := range entries {
				if entry.Reason == ev.Reason && entry.ObjectName == ev.InvolvedObject.Name {
					entries[i].Count += ev.Count
					break
				}
			}
			continue
		}
		seen[key] = true
		entries = append(entries, EventEntry{
			LastSeen: t, Type: ev.Type, Reason: ev.Reason,
			ObjectKind: ev.InvolvedObject.Kind, ObjectName: ev.InvolvedObject.Name,
			Namespace: ev.Namespace, Message: ev.Message, Count: ev.Count,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].LastSeen.After(entries[j].LastSeen) })
	return entries, nil
}
EOF

# ── internal/diag/rbac_impl.go ────────────────────────────────────────────────
cat > internal/diag/rbac_impl.go << 'EOF'
package diag

import (
	"fmt"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RBACResult struct {
	DangerousBindings []DangerousBinding
	ServiceAccounts   []SubjectBinding
	Users             []SubjectBinding
}

type DangerousBinding struct {
	Subject   string
	RoleName  string
	Namespace string
	Risk      string
}

type SubjectBinding struct {
	Name        string
	Namespace   string
	RoleName    string
	ClusterWide bool
}

func (e *Engine) RBACAudit(filterNS, filterSubject string) (*RBACResult, error) {
	result := &RBACResult{}
	ns := filterNS
	if ns == "" {
		ns = e.ns()
	}
	rbs, err := e.k8s.RbacV1().RoleBindings(ns).List(e.ctx, metav1.ListOptions{})
	if err == nil {
		for _, rb := range rbs.Items {
			for _, subject := range rb.Subjects {
				if filterSubject != "" && !strings.Contains(strings.ToLower(subject.Name), strings.ToLower(filterSubject)) {
					continue
				}
				binding := SubjectBinding{Name: subject.Name, Namespace: rb.Namespace, RoleName: rb.RoleRef.Name}
				if subject.Kind == "ServiceAccount" {
					result.ServiceAccounts = append(result.ServiceAccounts, binding)
				} else {
					result.Users = append(result.Users, binding)
				}
			}
		}
	}
	crbs, err := e.k8s.RbacV1().ClusterRoleBindings().List(e.ctx, metav1.ListOptions{})
	if err == nil {
		for _, crb := range crbs.Items {
			for _, subject := range crb.Subjects {
				if strings.HasPrefix(subject.Name, "system:") {
					continue
				}
				if filterSubject != "" && !strings.Contains(strings.ToLower(subject.Name), strings.ToLower(filterSubject)) {
					continue
				}
				binding := SubjectBinding{Name: subject.Name, Namespace: subject.Namespace, RoleName: crb.RoleRef.Name, ClusterWide: true}
				if subject.Kind == "ServiceAccount" {
					result.ServiceAccounts = append(result.ServiceAccounts, binding)
				} else {
					result.Users = append(result.Users, binding)
				}
			}
		}
	}
	roles, err := e.k8s.RbacV1().Roles(ns).List(e.ctx, metav1.ListOptions{})
	if err == nil {
		roleSubjects := map[string][]string{}
		if rbs != nil {
			for _, rb := range rbs.Items {
				for _, s := range rb.Subjects {
					roleSubjects[rb.RoleRef.Name] = append(roleSubjects[rb.RoleRef.Name], s.Name)
				}
			}
		}
		for _, role := range roles.Items {
			risk := checkPolicyRules(role.Rules)
			if risk == "" {
				continue
			}
			subjects := strings.Join(roleSubjects[role.Name], ", ")
			if subjects == "" {
				subjects = "(unbound)"
			}
			result.DangerousBindings = append(result.DangerousBindings, DangerousBinding{
				Subject: subjects, RoleName: role.Name, Namespace: role.Namespace, Risk: risk,
			})
		}
	}
	crs, err := e.k8s.RbacV1().ClusterRoles().List(e.ctx, metav1.ListOptions{})
	if err == nil {
		crSubjects := map[string][]string{}
		if crbs != nil {
			for _, crb := range crbs.Items {
				for _, s := range crb.Subjects {
					if !strings.HasPrefix(s.Name, "system:") {
						crSubjects[crb.RoleRef.Name] = append(crSubjects[crb.RoleRef.Name], s.Name)
					}
				}
			}
		}
		for _, cr := range crs.Items {
			if strings.HasPrefix(cr.Name, "system:") {
				continue
			}
			risk := checkPolicyRules(cr.Rules)
			if risk == "" {
				continue
			}
			subjects := crSubjects[cr.Name]
			if len(subjects) == 0 {
				continue
			}
			result.DangerousBindings = append(result.DangerousBindings, DangerousBinding{
				Subject: strings.Join(subjects, ", "), RoleName: cr.Name, Namespace: "cluster-wide", Risk: risk,
			})
		}
	}
	return result, nil
}

func checkPolicyRules(rules []rbacv1.PolicyRule) string {
	for _, rule := range rules {
		hasWildcardVerb := false
		hasDangerousVerb := false
		for _, v := range rule.Verbs {
			if v == "*" {
				hasWildcardVerb = true
			}
			for _, dv := range []string{"delete", "patch", "update", "create", "escalate", "bind"} {
				if v == dv {
					hasDangerousVerb = true
				}
			}
		}
		for _, r := range rule.Resources {
			if r == "*" && hasWildcardVerb {
				return "wildcard on all resources — effectively cluster-admin"
			}
			if r == "secrets" && hasDangerousVerb {
				return "can read/modify secrets — credential exposure risk"
			}
			if r == "pods/exec" {
				return "can exec into pods — container escape risk"
			}
			if (r == "clusterroles" || r == "clusterrolebindings") && hasDangerousVerb {
				return "can modify RBAC — privilege escalation risk"
			}
		}
	}
	return ""
}

var _ = fmt.Sprintf
var _ = metav1.ListOptions{}
EOF

# ── internal/diag/cert.go ─────────────────────────────────────────────────────
cat > internal/diag/cert.go << 'EOF'
package diag

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type CertInfo struct {
	Name       string
	Namespace  string
	SecretName string
	CommonName string
	Expiry     time.Time
	DaysLeft   int
}

func (e *Engine) CertCheck(filterNS string, warnDays int) ([]CertInfo, error) {
	ns := filterNS
	if ns == "" {
		ns = e.ns()
	}
	var certs []CertInfo
	secrets, err := e.k8s.CoreV1().Secrets(ns).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing secrets: %w", err)
	}
	for _, secret := range secrets.Items {
		if secret.Type != "kubernetes.io/tls" {
			continue
		}
		certData, ok := secret.Data["tls.crt"]
		if !ok {
			continue
		}
		info := parseCert(certData)
		if info == nil {
			continue
		}
		daysLeft := int(time.Until(info.NotAfter).Hours() / 24)
		if daysLeft > warnDays {
			continue
		}
		certs = append(certs, CertInfo{
			Name: secret.Name, Namespace: secret.Namespace, SecretName: secret.Name,
			CommonName: info.Subject.CommonName, Expiry: info.NotAfter, DaysLeft: daysLeft,
		})
	}
	ingresses, err := e.k8s.NetworkingV1().Ingresses(ns).List(e.ctx, metav1.ListOptions{})
	if err == nil {
		for _, ing := range ingresses.Items {
			for _, tls := range ing.Spec.TLS {
				if tls.SecretName == "" {
					continue
				}
				already := false
				for _, c := range certs {
					if c.SecretName == tls.SecretName && c.Namespace == ing.Namespace {
						already = true
						break
					}
				}
				if already {
					continue
				}
				secret, err := e.k8s.CoreV1().Secrets(ing.Namespace).Get(e.ctx, tls.SecretName, metav1.GetOptions{})
				if err != nil {
					continue
				}
				certData, ok := secret.Data["tls.crt"]
				if !ok {
					continue
				}
				info := parseCert(certData)
				if info == nil {
					continue
				}
				daysLeft := int(time.Until(info.NotAfter).Hours() / 24)
				if daysLeft > warnDays {
					continue
				}
				certs = append(certs, CertInfo{
					Name:       fmt.Sprintf("%s (ingress: %s)", tls.SecretName, ing.Name),
					Namespace:  ing.Namespace, SecretName: tls.SecretName,
					CommonName: info.Subject.CommonName, Expiry: info.NotAfter, DaysLeft: daysLeft,
				})
			}
		}
	}
	sort.Slice(certs, func(i, j int) bool { return certs[i].DaysLeft < certs[j].DaysLeft })
	return certs, nil
}

func parseCert(data []byte) *x509.Certificate {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return cert
}
EOF

# ── internal/diag/rollback.go ─────────────────────────────────────────────────
cat > internal/diag/rollback.go << 'EOF'
package diag

import (
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RollbackTarget struct {
	Name        string
	Namespace   string
	ChangedBy   string
	ChangedAt   time.Time
	ImageChange string
	Generation  int64
}

func (e *Engine) RollbackTargets(filterNS string) ([]RollbackTarget, error) {
	ns := filterNS
	if ns == "" {
		ns = e.ns()
	}
	deploys, err := e.k8s.AppsV1().Deployments(ns).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing deployments: %w", err)
	}
	var targets []RollbackTarget
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, d := range deploys.Items {
		var latestFM string
		var latestTime time.Time
		for _, mf := range d.ManagedFields {
			if mf.Time != nil && mf.Time.Time.After(cutoff) && mf.Time.Time.After(latestTime) {
				latestTime = mf.Time.Time
				latestFM = mf.Manager
			}
		}
		if latestTime.IsZero() {
			continue
		}
		imageChange := ""
		for _, c := range d.Spec.Template.Spec.Containers {
			imageChange = fmt.Sprintf("%s: %s", c.Name, c.Image)
			break
		}
		targets = append(targets, RollbackTarget{
			Name: d.Name, Namespace: d.Namespace, ChangedBy: latestFM,
			ChangedAt: latestTime, ImageChange: imageChange, Generation: d.Generation,
		})
	}
	for i := 1; i < len(targets); i++ {
		for j := i; j > 0 && targets[j].ChangedAt.After(targets[j-1].ChangedAt); j-- {
			targets[j], targets[j-1] = targets[j-1], targets[j]
		}
	}
	_ = strings.Contains
	return targets, nil
}
EOF

# ── internal/diag/cost.go ─────────────────────────────────────────────────────
cat > internal/diag/cost.go << 'EOF'
package diag

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type CostResult struct {
	OverProvisioned      []OverProvisionedPod
	IdleNamespaces       []string
	UnderutilisedNodes   []UnderutilisedNode
	EstimatedWasteCPU    string
	EstimatedWasteMemory string
}

type OverProvisionedPod struct {
	Name           string
	Namespace      string
	CPURequest     string
	MemRequest     string
	WasteScore     int
	Recommendation string
}

type UnderutilisedNode struct {
	Name       string
	CPUPercent float64
	MemPercent float64
}

func (e *Engine) CostAnalysis(filterNS string) (*CostResult, error) {
	result := &CostResult{}
	ns := filterNS
	if ns == "" {
		ns = e.ns()
	}
	actualCPU := map[string]int64{}
	actualMem := map[string]int64{}
	args := []string{"top", "pods", "--no-headers"}
	if ns == metav1.NamespaceAll {
		args = append(args, "--all-namespaces")
	} else {
		args = append(args, "-n", ns)
	}
	topOut, err := exec.CommandContext(e.ctx, "kubectl", args...).Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(topOut)), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			key, cpuStr, memStr := "", "", ""
			if ns == metav1.NamespaceAll && len(fields) >= 4 {
				key = fields[0] + "/" + fields[1]
				cpuStr, memStr = fields[2], fields[3]
			} else if len(fields) >= 3 {
				key = fields[0]
				cpuStr, memStr = fields[1], fields[2]
			}
			if key != "" {
				actualCPU[key] = parseCPUMillis(cpuStr)
				actualMem[key] = parseMemMi(memStr)
			}
		}
	}
	podList, err := e.k8s.CoreV1().Pods(ns).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var totalWasteCPU, totalWasteMem int64
	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range pod.Spec.Containers {
			reqCPU := c.Resources.Requests.Cpu().MilliValue()
			reqMem := c.Resources.Requests.Memory().Value() / (1024 * 1024)
			if reqCPU == 0 && reqMem == 0 {
				continue
			}
			key := pod.Name
			if ns == metav1.NamespaceAll {
				key = pod.Namespace + "/" + pod.Name
			}
			actualC := actualCPU[key]
			actualM := actualMem[key]
			if actualC == 0 && actualM == 0 {
				continue
			}
			wasteScore := 0
			if reqCPU > 0 && actualC > 0 {
				ratio := float64(actualC) / float64(reqCPU) * 100
				if ratio < 10 {
					wasteScore += 50
					totalWasteCPU += reqCPU - actualC
				} else if ratio < 25 {
					wasteScore += 25
				}
			}
			if reqMem > 0 && actualM > 0 {
				ratio := float64(actualM) / float64(reqMem) * 100
				if ratio < 10 {
					wasteScore += 50
					totalWasteMem += reqMem - actualM
				} else if ratio < 25 {
					wasteScore += 25
				}
			}
			if wasteScore >= 40 {
				rec := ""
				if reqCPU > 0 && actualC > 0 {
					rec = fmt.Sprintf("consider reducing CPU request from %dm to ~%dm", reqCPU, actualC*2)
				}
				result.OverProvisioned = append(result.OverProvisioned, OverProvisionedPod{
					Name: pod.Name, Namespace: pod.Namespace,
					CPURequest: fmt.Sprintf("%dm", reqCPU), MemRequest: fmt.Sprintf("%dMi", reqMem),
					WasteScore: wasteScore, Recommendation: rec,
				})
			}
		}
	}
	sort.Slice(result.OverProvisioned, func(i, j int) bool {
		return result.OverProvisioned[i].WasteScore > result.OverProvisioned[j].WasteScore
	})
	nsList, _ := e.k8s.CoreV1().Namespaces().List(e.ctx, metav1.ListOptions{})
	if nsList != nil {
		for _, n := range nsList.Items {
			pods, _ := e.k8s.CoreV1().Pods(n.Name).List(e.ctx, metav1.ListOptions{FieldSelector: "status.phase=Running"})
			if pods != nil && len(pods.Items) == 0 && !strings.HasPrefix(n.Name, "kube-") && n.Name != "default" {
				result.IdleNamespaces = append(result.IdleNamespaces, n.Name)
			}
		}
	}
	nodeOut, _ := exec.CommandContext(e.ctx, "kubectl", "top", "nodes", "--no-headers").Output()
	if nodeOut != nil {
		for _, line := range strings.Split(strings.TrimSpace(string(nodeOut)), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 5 {
				continue
			}
			cpuPct := parsePercent(fields[2])
			memPct := parsePercent(fields[4])
			if cpuPct < 20 && memPct < 20 {
				result.UnderutilisedNodes = append(result.UnderutilisedNodes, UnderutilisedNode{
					Name: fields[0], CPUPercent: cpuPct, MemPercent: memPct,
				})
			}
		}
	}
	if totalWasteCPU > 0 {
		result.EstimatedWasteCPU = fmt.Sprintf("%dm", totalWasteCPU)
	}
	if totalWasteMem > 0 {
		result.EstimatedWasteMemory = fmt.Sprintf("%dMi", totalWasteMem)
	}
	return result, nil
}
EOF

# ── Clean up old rbac.go if it exists ─────────────────────────────────────────
rm -f internal/diag/rbac.go

echo ""
echo "✓ All v3 files created!"
echo ""
echo "Now run:"
echo "  go mod tidy"
echo "  go build -o k8s-doctor ."
echo "  ./k8s-doctor --help"
echo ""
echo "New commands available:"
echo "  ./k8s-doctor diagnose          # ONE answer root cause"
echo "  ./k8s-doctor top               # who is eating resources"
echo "  ./k8s-doctor events            # full event timeline"
echo "  ./k8s-doctor rbac              # who can do what"
echo "  ./k8s-doctor cert              # TLS expiry check"
echo "  ./k8s-doctor rollback          # safe revert"
echo "  ./k8s-doctor cost              # resource waste"
echo "  ./k8s-doctor scale <dep> <n>   # safe scale"
echo "  ./k8s-doctor report --slack <webhook>"
