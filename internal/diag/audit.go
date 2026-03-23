package diag

import (
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type AuditEntry struct {
	Timestamp       time.Time
	Kind            string
	Name            string
	Namespace       string
	Action          string
	FieldManager    string
	Detail          string
	CorrelatedFault string
	Mitigation      string
}

func (e *Engine) AuditLog(window time.Duration, filterKind, filterUser string) ([]AuditEntry, error) {
	cutoff := time.Now().Add(-window)
	var entries []AuditEntry
	ns := e.ns()
	events, err := e.k8s.CoreV1().Events(ns).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, ev := range events.Items {
		t := ev.LastTimestamp.Time
		if t.IsZero() {
			t = ev.EventTime.Time
		}
		if t.Before(cutoff) {
			continue
		}
		action := "UPDATE"
		switch ev.Reason {
		case "Created", "Started", "Pulled", "Scheduled":
			action = "CREATE"
		case "Killing", "Evicting":
			action = "DELETE"
		}
		kind := ev.InvolvedObject.Kind
		if filterKind != "" && !strings.EqualFold(kind, filterKind) {
			continue
		}
		fm := ev.Source.Component
		if fm == "" {
			fm = ev.ReportingInstance
		}
		if fm == "" {
			fm = "kubernetes"
		}
		if filterUser != "" && !strings.Contains(strings.ToLower(fm), strings.ToLower(filterUser)) {
			continue
		}
		entries = append(entries, AuditEntry{
			Timestamp: t, Kind: kind,
			Name: ev.InvolvedObject.Name, Namespace: ev.Namespace,
			Action: action, FieldManager: fm,
			Detail: truncate(ev.Message, 120),
		})
	}
	fieldEntries, err := e.managedFieldsAudit(cutoff, filterKind, filterUser)
	if err == nil {
		entries = append(entries, fieldEntries...)
	}
	faults, _ := e.PodHealth()
	faultMap := map[string]string{}
	for _, f := range faults {
		if f.Score > 0 && f.Object != "" {
			faultMap[f.Object] = f.Title
		}
	}
	for i, entry := range entries {
		if fault, ok := faultMap[entry.Name]; ok {
			entries[i].CorrelatedFault = fault
			entries[i].Mitigation = mitigationFor(fault, entry.Kind, entry.Name, entry.Namespace)
		}
	}
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].Timestamp.After(entries[j-1].Timestamp); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	seen := map[string]bool{}
	var deduped []AuditEntry
	for _, e := range entries {
		key := fmt.Sprintf("%s/%s/%s/%s", e.Kind, e.Name, e.FieldManager, e.Timestamp.Truncate(time.Minute))
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, e)
	}
	if len(deduped) > 100 {
		deduped = deduped[:100]
	}
	return deduped, nil
}

func (e *Engine) managedFieldsAudit(cutoff time.Time, filterKind, filterUser string) ([]AuditEntry, error) {
	var entries []AuditEntry
	ns := e.ns()
	if filterKind == "" || strings.EqualFold(filterKind, "Deployment") {
		deploys, err := e.k8s.AppsV1().Deployments(ns).List(e.ctx, metav1.ListOptions{})
		if err == nil {
			for _, obj := range deploys.Items {
				for _, mf := range obj.ManagedFields {
					if mf.Time == nil || mf.Time.Time.Before(cutoff) {
						continue
					}
					fm := mf.Manager
					if filterUser != "" && !strings.Contains(strings.ToLower(fm), strings.ToLower(filterUser)) {
						continue
					}
					op := "UPDATE"
					if strings.ToLower(string(mf.Operation)) == "create" {
						op = "CREATE"
					}
					entries = append(entries, AuditEntry{
						Timestamp: mf.Time.Time, Kind: "Deployment",
						Name: obj.Name, Namespace: obj.Namespace,
						Action: op, FieldManager: fm,
					})
				}
			}
		}
	}
	if filterKind == "" || strings.EqualFold(filterKind, "ConfigMap") {
		cms, err := e.k8s.CoreV1().ConfigMaps(ns).List(e.ctx, metav1.ListOptions{})
		if err == nil {
			for _, obj := range cms.Items {
				if obj.Namespace == "kube-system" {
					continue
				}
				for _, mf := range obj.ManagedFields {
					if mf.Time == nil || mf.Time.Time.Before(cutoff) {
						continue
					}
					fm := mf.Manager
					if filterUser != "" && !strings.Contains(strings.ToLower(fm), strings.ToLower(filterUser)) {
						continue
					}
					entries = append(entries, AuditEntry{
						Timestamp: mf.Time.Time, Kind: "ConfigMap",
						Name: obj.Name, Namespace: obj.Namespace,
						Action: "UPDATE", FieldManager: fm,
					})
				}
			}
		}
	}
	return entries, nil
}

func mitigationFor(fault, kind, name, namespace string) string {
	switch {
	case strings.Contains(fault, "CrashLoop"):
		return fmt.Sprintf("kubectl rollout undo deployment/%s -n %s", name, namespace)
	case strings.Contains(fault, "OOMKilled"):
		return fmt.Sprintf("increase memory limit for %s in ns %s", name, namespace)
	case strings.Contains(fault, "ImagePull"):
		return "check image tag and ECR permissions"
	case strings.Contains(fault, "Pending"):
		return "./k8s-doctor node pressure"
	default:
		return fmt.Sprintf("kubectl describe %s %s -n %s", strings.ToLower(kind), name, namespace)
	}
}
