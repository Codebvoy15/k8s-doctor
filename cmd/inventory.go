package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/diag"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var (
	invAllNamespaces    bool
	invAPIGroups        string
	invExcludeAPIGroups string
	invResources        string
	invExcludeResources string
	invSelector         string
	invIncludeEvents    bool
	invIncludeNoisy     bool
)

var inventoryCmd = &cobra.Command{
	Use:   "inventory",
	Short: "Full namespace resource inventory — because 'kubectl get all' doesn't get all",
	Long: `Scans every API resource in a namespace, including CRDs, and classifies each
one as OK, GITOPS-managed, SUSPICIOUS (orphaned), or STUCK (finalizer-blocked).

  k8s-doctor inventory -n production
  k8s-doctor inventory -n production -o json
  k8s-doctor inventory --all-namespaces

Subcommands:
  k8s-doctor inventory explain <kind>/<name> -n <ns>
  k8s-doctor inventory orphans -n <ns>
  k8s-doctor inventory stuck -n <ns>
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}

		opts := buildInventoryOptions()
		report, err := engine.ScanNamespace(opts)
		if err != nil {
			return fmt.Errorf("inventory scan failed: %w", err)
		}
		printInventoryReport(report)
		return nil
	},
}

var explainCmd = &cobra.Command{
	Use:   "explain <kind>/<name>",
	Short: "Per-resource classification report — owner chain, referrers, confidence",
	Long: `Explains why a specific resource is classified the way it is.

  k8s-doctor inventory explain secret/old-api-key -n production
  k8s-doctor inventory explain cm/app-config -n default
  k8s-doctor inventory explain pod/debug-net -n production
`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		kind, name, err := splitKindName(args[0])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		res, err := engine.ExplainResource(kind, name, namespace)
		if err != nil {
			return err
		}
		printExplain(res)
		return nil
	},
}

var orphansCmd = &cobra.Command{
	Use:   "orphans",
	Short: "Show only suspicious/orphaned resources with reasons",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		opts := buildInventoryOptions()
		report, err := engine.ScanNamespace(opts)
		if err != nil {
			return fmt.Errorf("inventory scan failed: %w", err)
		}
		printEntryList("SUSPICIOUS RESOURCES", report.Suspicious, report.Namespace)
		return nil
	},
}

var stuckCmd = &cobra.Command{
	Use:   "stuck",
	Short: "Show only resources stuck behind finalizers",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		engine, err := diag.NewEngine(ctx, namespace, verbose)
		if err != nil {
			return err
		}
		opts := buildInventoryOptions()
		report, err := engine.ScanNamespace(opts)
		if err != nil {
			return fmt.Errorf("inventory scan failed: %w", err)
		}
		printEntryList("STUCK RESOURCES", report.Stuck, report.Namespace)
		return nil
	},
}

func buildInventoryOptions() diag.InventoryOptions {
	return diag.InventoryOptions{
		Namespace:        namespace,
		AllNamespaces:    invAllNamespaces,
		APIGroups:        splitCSV(invAPIGroups),
		ExcludeAPIGroups: splitCSV(invExcludeAPIGroups),
		Resources:        splitCSV(invResources),
		ExcludeResources: splitCSV(invExcludeResources),
		Selector:         invSelector,
		IncludeEvents:    invIncludeEvents,
		IncludeNoisy:     invIncludeNoisy,
	}
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitKindName(arg string) (string, string, error) {
	parts := strings.SplitN(arg, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected <kind>/<name>, e.g. secret/my-secret")
	}
	return parts[0], parts[1], nil
}

// ─────────────────────────────────────────────────────────────
// Rendering
// ─────────────────────────────────────────────────────────────

func printInventoryReport(r *diag.InventoryReport) {
	if outputFmt == "json" {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
		return
	}

	nsLabel := r.Namespace
	if nsLabel == "" {
		nsLabel = "all"
	}
	fmt.Printf("\nNamespace: %s   Context: %s\n", nsLabel, clusterName)
	fmt.Println(color.HiBlackString(strings.Repeat("─", 64)))

	for _, g := range r.Groups {
		label := g.Group
		if label == "" {
			label = "core"
		}
		fmt.Printf("\n%s\n", color.New(color.Bold).Sprint(label))
		for _, rc := range g.Resources {
			suspiciousLabel := ""
			if rc.Suspicious > 0 {
				suspiciousLabel = color.YellowString("  (suspicious: %d ⚠)", rc.Suspicious)
			}
			fmt.Printf("  %-16s %3d%s\n", rc.Kind, rc.Count, suspiciousLabel)
		}
	}

	fmt.Printf("\n%s\n", color.New(color.Bold).Sprint("Stuck Resources"))
	if len(r.Stuck) == 0 {
		fmt.Printf("  None  %s\n", color.GreenString("✓"))
	} else {
		for _, e := range r.Stuck {
			fmt.Printf("  %s  %s/%s\n", color.RedString("!"), strings.ToLower(e.Obj.Kind), e.Obj.Name)
		}
	}

	fmt.Println()
	fmt.Printf("Total resources:  %d\n", r.TotalResources)
	fmt.Printf("Scanned types:    %d   Skipped: %d\n", r.ScannedTypes, r.SkippedTypes)
	fmt.Printf("Duration:         %s\n\n", r.Duration.Round(time.Millisecond))

	if len(r.Suspicious) > 0 {
		fmt.Printf("tip  %d suspicious resource(s) found — run 'k8s-doctor inventory orphans' for details\n\n", len(r.Suspicious))
	}
}

func printExplain(res *diag.ExplainResult) {
	if outputFmt == "json" {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		return
	}

	o := res.Entry.Obj
	c := res.Entry.Class
	fmt.Printf("\n%s/%s\n", strings.ToLower(o.Kind), o.Name)
	fmt.Println(color.HiBlackString(strings.Repeat("─", 64)))

	fmt.Println(color.New(color.Bold).Sprint("Identity"))
	fmt.Printf("  Namespace:  %s\n", o.Namespace)
	fmt.Printf("  Kind:       %s\n", o.Kind)
	if o.UID != "" {
		fmt.Printf("  UID:        %s\n", o.UID)
	}
	fmt.Printf("  Age:        %s\n", formatDuration(o.Age))
	if !o.CreatedAt.IsZero() {
		fmt.Printf("  Created:    %s\n", o.CreatedAt.Format(time.RFC3339))
	}
	if o.Manager != "" {
		fmt.Printf("  Manager:    %s\n", o.Manager)
	}

	fmt.Printf("\n%s\n", color.New(color.Bold).Sprint("Classification"))
	statusColor := color.GreenString
	switch c.Status {
	case "SUSPICIOUS":
		statusColor = color.YellowString
	case "STUCK":
		statusColor = color.RedString
	}
	fmt.Printf("  Status:     %s\n", statusColor(c.Status))
	if len(c.Reasons) > 0 {
		fmt.Println("  Reasons:")
		for _, r := range c.Reasons {
			fmt.Printf("    • %s\n", r)
		}
	}

	fmt.Printf("\n%s\n", color.New(color.Bold).Sprint("Ownership"))
	if len(o.OwnerRefs) == 0 {
		fmt.Println("  ownerReferences: none")
	} else {
		for _, ref := range o.OwnerRefs {
			fmt.Printf("  ownerReferences: %s/%s\n", ref.Kind, ref.Name)
		}
	}

	fmt.Printf("\n%s\n", color.New(color.Bold).Sprint("Referenced By"))
	if len(res.ReferencedBy) == 0 {
		fmt.Println("  not referenced by any scanned resource")
	} else {
		for _, r := range res.ReferencedBy {
			fmt.Printf("  %s\n", r)
		}
	}

	fmt.Printf("\n%s\n", color.New(color.Bold).Sprint("Finalizers"))
	if len(o.Finalizers) == 0 {
		fmt.Println("  none")
	} else {
		fmt.Println("  " + strings.Join(o.Finalizers, ", "))
	}
	fmt.Println()
}

func printEntryList(title string, entries []diag.InventoryEntry, ns string) {
	if outputFmt == "json" {
		b, _ := json.MarshalIndent(entries, "", "  ")
		fmt.Println(string(b))
		return
	}

	nsLabel := ns
	if nsLabel == "" {
		nsLabel = "all"
	}
	fmt.Printf("\n%s  ns=%s  %s\n", color.New(color.Bold).Sprint(title), nsLabel,
		color.HiBlackString("%d found", len(entries)))
	fmt.Println(color.HiBlackString(strings.Repeat("─", 64)))

	if len(entries) == 0 {
		fmt.Println("\n  none  ✓\n")
		return
	}

	for _, e := range entries {
		fmt.Printf("\n%s/%s\n", strings.ToLower(e.Obj.Kind), color.New(color.Bold).Sprint(e.Obj.Name))
		if e.Obj.Namespace != "" {
			fmt.Printf("  ns   %s\n", color.HiBlackString(e.Obj.Namespace))
		}
		for _, r := range e.Class.Reasons {
			fmt.Printf("  •    %s\n", color.HiBlackString(r))
		}
	}
	fmt.Println()
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	days := int(d.Hours()) / 24
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	hours := int(d.Hours())
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

func init() {
	inventoryCmd.Flags().BoolVarP(&invAllNamespaces, "all-namespaces", "A", false, "scan all namespaces")
	inventoryCmd.Flags().StringVar(&invAPIGroups, "api-groups", "", "comma-separated API groups to include")
	inventoryCmd.Flags().StringVar(&invExcludeAPIGroups, "exclude-api-groups", "", "comma-separated API groups to exclude")
	inventoryCmd.Flags().StringVar(&invResources, "resources", "", "comma-separated resource names to include")
	inventoryCmd.Flags().StringVar(&invExcludeResources, "exclude-resources", "", "comma-separated resource names to exclude")
	inventoryCmd.Flags().StringVarP(&invSelector, "selector", "l", "", "label selector")
	inventoryCmd.Flags().BoolVar(&invIncludeEvents, "include-events", false, "include Events (excluded by default)")
	inventoryCmd.Flags().BoolVar(&invIncludeNoisy, "include-noisy", false, "include all noise-profile excluded resources")

	// share the same flags with subcommands
	for _, c := range []*cobra.Command{explainCmd, orphansCmd, stuckCmd} {
		c.Flags().AddFlagSet(inventoryCmd.Flags())
	}

	inventoryCmd.AddCommand(explainCmd, orphansCmd, stuckCmd)
	rootCmd.AddCommand(inventoryCmd)
}
