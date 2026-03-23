package diag

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DiffEntry struct {
	Timestamp       time.Time
	Kind            string
	Name            string
	Namespace       string
	Field           string
	OldValue        string
	NewValue        string
	Action          string
	FieldManager    string
	CorrelatedFault string
	Mitigation      string
}

type StateSnapshot struct {
	CapturedAt    time.Time                 `json:"captured_at"`
	ResourceCount int                       `json:"resource_count"`
	Resources     map[string]ResourceRecord `json:"resources"`
}

type ResourceRecord struct {
	Kind            string            `json:"kind"`
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	ResourceVersion string            `json:"resource_version"`
	Generation      int64             `json:"generation"`
	Labels          map[string]string `json:"labels"`
	FieldManager    string            `json:"field_manager"`
	CapturedAt      time.Time         `json:"captured_at"`
}

func (e *Engine) CaptureStateSnapshot() (*StateSnapshot, error) {
	snap := &StateSnapshot{CapturedAt: time.Now(), Resources: map[string]ResourceRecord{}}
	ns := e.ns()
	if deploys, err := e.k8s.AppsV1().Deployments(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range deploys.Items {
			fm := ""
			if len(obj.ManagedFields) > 0 {
				fm = obj.ManagedFields[len(obj.ManagedFields)-1].Manager
			}
			snap.Resources[fmt.Sprintf("Deployment/%s/%s", obj.Namespace, obj.Name)] = ResourceRecord{
				Kind: "Deployment", Name: obj.Name, Namespace: obj.Namespace,
				ResourceVersion: obj.ResourceVersion, Generation: obj.Generation,
				Labels: obj.Labels, FieldManager: fm, CapturedAt: snap.CapturedAt,
			}
		}
	}
	if cms, err := e.k8s.CoreV1().ConfigMaps(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range cms.Items {
			if obj.Namespace == "kube-system" {
				continue
			}
			fm := ""
			if len(obj.ManagedFields) > 0 {
				fm = obj.ManagedFields[len(obj.ManagedFields)-1].Manager
			}
			snap.Resources[fmt.Sprintf("ConfigMap/%s/%s", obj.Namespace, obj.Name)] = ResourceRecord{
				Kind: "ConfigMap", Name: obj.Name, Namespace: obj.Namespace,
				ResourceVersion: obj.ResourceVersion, Labels: obj.Labels,
				FieldManager: fm, CapturedAt: snap.CapturedAt,
			}
		}
	}
	if svcs, err := e.k8s.CoreV1().Services(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range svcs.Items {
			snap.Resources[fmt.Sprintf("Service/%s/%s", obj.Namespace, obj.Name)] = ResourceRecord{
				Kind: "Service", Name: obj.Name, Namespace: obj.Namespace,
				ResourceVersion: obj.ResourceVersion, Labels: obj.Labels, CapturedAt: snap.CapturedAt,
			}
		}
	}
	if podList, err := e.k8s.CoreV1().Pods(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range podList.Items {
			snap.Resources[fmt.Sprintf("Pod/%s/%s", obj.Namespace, obj.Name)] = ResourceRecord{
				Kind: "Pod", Name: obj.Name, Namespace: obj.Namespace,
				ResourceVersion: obj.ResourceVersion, Labels: obj.Labels, CapturedAt: snap.CapturedAt,
			}
		}
	}
	snap.ResourceCount = len(snap.Resources)
	return snap, nil
}

func (e *Engine) SnapshotDiff(baseline *StateSnapshot) ([]DiffEntry, error) {
	current, err := e.CaptureStateSnapshot()
	if err != nil {
		return nil, err
	}
	var diffs []DiffEntry
	for key, cur := range current.Resources {
		if _, existed := baseline.Resources[key]; !existed {
			diffs = append(diffs, DiffEntry{
				Timestamp: current.CapturedAt, Kind: cur.Kind,
				Name: cur.Name, Namespace: cur.Namespace,
				Field: "existence", NewValue: "created",
				Action: "ADDED", FieldManager: cur.FieldManager,
			})
		}
	}
	for key, base := range baseline.Resources {
		cur, exists := current.Resources[key]
		if !exists {
			diffs = append(diffs, DiffEntry{
				Timestamp: current.CapturedAt, Kind: base.Kind,
				Name: base.Name, Namespace: base.Namespace,
				Field: "existence", OldValue: "existed", NewValue: "deleted",
				Action: "DELETED", FieldManager: base.FieldManager,
			})
			continue
		}
		if cur.ResourceVersion != base.ResourceVersion {
			fm := cur.FieldManager
			if fm == "" {
				fm = base.FieldManager
			}
			diffs = append(diffs, DiffEntry{
				Timestamp: current.CapturedAt, Kind: cur.Kind,
				Name: cur.Name, Namespace: cur.Namespace,
				Field: "spec/metadata",
				OldValue: fmt.Sprintf("rv=%s", base.ResourceVersion),
				NewValue: fmt.Sprintf("rv=%s gen=%d", cur.ResourceVersion, cur.Generation),
				Action: "UPDATED", FieldManager: fm,
			})
		}
	}
	faults, _ := e.PodHealth()
	faultMap := map[string]Finding{}
	for _, f := range faults {
		if f.Score > 0 && f.Object != "" {
			faultMap[f.Object] = f
		}
	}
	for i, d := range diffs {
		if fault, ok := faultMap[d.Name]; ok {
			diffs[i].CorrelatedFault = fault.Title
			diffs[i].Mitigation = mitigationFor(fault.Title, d.Kind, d.Name, d.Namespace)
		}
	}
	return diffs, nil
}

func (e *Engine) LiveDiff(window time.Duration) ([]DiffEntry, error) {
	cutoff := time.Now().Add(-window)
	var diffs []DiffEntry
	ns := e.ns()
	if deploys, err := e.k8s.AppsV1().Deployments(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range deploys.Items {
			for _, mf := range obj.ManagedFields {
				if mf.Time == nil || mf.Time.Time.Before(cutoff) {
					continue
				}
				replicas := int32(0)
				if obj.Spec.Replicas != nil {
					replicas = *obj.Spec.Replicas
				}
				diffs = append(diffs, DiffEntry{
					Timestamp: mf.Time.Time, Kind: "Deployment",
					Name: obj.Name, Namespace: obj.Namespace,
					Field: "spec",
					NewValue: fmt.Sprintf("generation=%d replicas=%d", obj.Generation, replicas),
					Action: "UPDATED", FieldManager: mf.Manager,
				})
			}
		}
	}
	if cms, err := e.k8s.CoreV1().ConfigMaps(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range cms.Items {
			if obj.Namespace == "kube-system" {
				continue
			}
			for _, mf := range obj.ManagedFields {
				if mf.Time == nil || mf.Time.Time.Before(cutoff) {
					continue
				}
				diffs = append(diffs, DiffEntry{
					Timestamp: mf.Time.Time, Kind: "ConfigMap",
					Name: obj.Name, Namespace: obj.Namespace,
					Field: "data",
					NewValue: fmt.Sprintf("keys=%d", len(obj.Data)),
					Action: "UPDATED", FieldManager: mf.Manager,
				})
			}
		}
	}
	faults, _ := e.PodHealth()
	faultMap := map[string]Finding{}
	for _, f := range faults {
		if f.Score > 0 && f.Object != "" {
			faultMap[f.Object] = f
		}
	}
	for i, d := range diffs {
		if fault, ok := faultMap[d.Name]; ok {
			diffs[i].CorrelatedFault = fault.Title
			diffs[i].Mitigation = mitigationFor(fault.Title, d.Kind, d.Name, d.Namespace)
		}
	}
	for i := 1; i < len(diffs); i++ {
		for j := i; j > 0 && diffs[j].Timestamp.After(diffs[j-1].Timestamp); j-- {
			diffs[j], diffs[j-1] = diffs[j-1], diffs[j]
		}
	}
	return diffs, nil
}
