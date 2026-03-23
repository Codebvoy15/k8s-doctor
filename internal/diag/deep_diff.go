package diag

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeepDiffEntry captures the exact field that changed, old value, and new value
type DeepDiffEntry struct {
	Timestamp       time.Time
	Kind            string
	Name            string
	Namespace       string
	Field           string
	OldValue        string
	NewValue        string
	ChangedBy       string
	CorrelatedFault string
	Mitigation      string
	Risk            string
}

// DeepResourceRecord stores the full spec we care about for diffing
type DeepResourceRecord struct {
	Kind            string    `json:"kind"`
	Name            string    `json:"name"`
	Namespace       string    `json:"namespace"`
	ResourceVersion string    `json:"resource_version"`
	FieldManager    string    `json:"field_manager"`
	CapturedAt      time.Time `json:"captured_at"`

	// Deployment / StatefulSet / DaemonSet / Job
	Replicas    int32             `json:"replicas,omitempty"`
	Images      map[string]string `json:"images,omitempty"`
	EnvVars     map[string]string `json:"env_vars,omitempty"`
	Resources   map[string]string `json:"resources,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`

	// ConfigMap / Secret
	Data       map[string]string `json:"data,omitempty"`
	SecretKeys []string          `json:"secret_keys,omitempty"` // only keys, never values

	// Service
	ServiceType string            `json:"service_type,omitempty"`
	Ports       []string          `json:"ports,omitempty"`
	Selector    map[string]string `json:"selector,omitempty"`

	// Ingress
	IngressRules []string `json:"ingress_rules,omitempty"`

	// HPA
	HPAMin    int32  `json:"hpa_min,omitempty"`
	HPAMax    int32  `json:"hpa_max,omitempty"`
	HPATarget string `json:"hpa_target,omitempty"`

	// RBAC
	Rules []string `json:"rules,omitempty"`

	// PVC
	StorageRequest string `json:"storage_request,omitempty"`
	StorageClass   string `json:"storage_class,omitempty"`
}

// DeepStateSnapshot stores full specs for accurate diffing
type DeepStateSnapshot struct {
	CapturedAt    time.Time                     `json:"captured_at"`
	ResourceCount int                           `json:"resource_count"`
	Resources     map[string]DeepResourceRecord `json:"resources"`
}

// CaptureDeepSnapshot captures full specs of ALL resource types
func (e *Engine) CaptureDeepSnapshot() (*DeepStateSnapshot, error) {
	snap := &DeepStateSnapshot{
		CapturedAt: time.Now(),
		Resources:  map[string]DeepResourceRecord{},
	}
	ns := e.ns()

	// ── Deployments ──────────────────────────────────────────────────────────
	if deploys, err := e.k8s.AppsV1().Deployments(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range deploys.Items {
			rec := newRecord("Deployment", obj.Name, obj.Namespace, obj.ResourceVersion, obj.ManagedFields)
			if obj.Spec.Replicas != nil {
				rec.Replicas = *obj.Spec.Replicas
			}
			rec.Labels = obj.Labels
			rec.Annotations = filterAnnotations(obj.Annotations)
			extractContainerSpec(obj.Spec.Template.Spec.Containers, &rec)
			snap.Resources["Deployment/"+obj.Namespace+"/"+obj.Name] = rec
		}
	}

	// ── StatefulSets ─────────────────────────────────────────────────────────
	if ssets, err := e.k8s.AppsV1().StatefulSets(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range ssets.Items {
			rec := newRecord("StatefulSet", obj.Name, obj.Namespace, obj.ResourceVersion, obj.ManagedFields)
			if obj.Spec.Replicas != nil {
				rec.Replicas = *obj.Spec.Replicas
			}
			rec.Labels = obj.Labels
			extractContainerSpec(obj.Spec.Template.Spec.Containers, &rec)
			snap.Resources["StatefulSet/"+obj.Namespace+"/"+obj.Name] = rec
		}
	}

	// ── DaemonSets ───────────────────────────────────────────────────────────
	if dsets, err := e.k8s.AppsV1().DaemonSets(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range dsets.Items {
			rec := newRecord("DaemonSet", obj.Name, obj.Namespace, obj.ResourceVersion, obj.ManagedFields)
			rec.Labels = obj.Labels
			extractContainerSpec(obj.Spec.Template.Spec.Containers, &rec)
			snap.Resources["DaemonSet/"+obj.Namespace+"/"+obj.Name] = rec
		}
	}

	// ── ConfigMaps ───────────────────────────────────────────────────────────
	if cms, err := e.k8s.CoreV1().ConfigMaps(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range cms.Items {
			if obj.Namespace == "kube-system" {
				continue
			}
			rec := newRecord("ConfigMap", obj.Name, obj.Namespace, obj.ResourceVersion, obj.ManagedFields)
			rec.Data = map[string]string{}
			for k, v := range obj.Data {
				if len(v) > 300 {
					rec.Data[k] = v[:300] + "...[truncated]"
				} else {
					rec.Data[k] = v
				}
			}
			snap.Resources["ConfigMap/"+obj.Namespace+"/"+obj.Name] = rec
		}
	}

	// ── Secrets (keys only — never store values) ──────────────────────────────
	if secrets, err := e.k8s.CoreV1().Secrets(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range secrets.Items {
			if obj.Namespace == "kube-system" || obj.Type == "kubernetes.io/service-account-token" {
				continue
			}
			rec := newRecord("Secret", obj.Name, obj.Namespace, obj.ResourceVersion, obj.ManagedFields)
			for k := range obj.Data {
				rec.SecretKeys = append(rec.SecretKeys, k)
			}
			snap.Resources["Secret/"+obj.Namespace+"/"+obj.Name] = rec
		}
	}

	// ── Services ─────────────────────────────────────────────────────────────
	if svcs, err := e.k8s.CoreV1().Services(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range svcs.Items {
			rec := newRecord("Service", obj.Name, obj.Namespace, obj.ResourceVersion, obj.ManagedFields)
			rec.ServiceType = string(obj.Spec.Type)
			rec.Selector = obj.Spec.Selector
			for _, p := range obj.Spec.Ports {
				rec.Ports = append(rec.Ports, fmt.Sprintf("%s:%d→%d", p.Protocol, p.Port, p.NodePort))
			}
			snap.Resources["Service/"+obj.Namespace+"/"+obj.Name] = rec
		}
	}

	// ── Ingresses ────────────────────────────────────────────────────────────
	if ingresses, err := e.k8s.NetworkingV1().Ingresses(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range ingresses.Items {
			rec := newRecord("Ingress", obj.Name, obj.Namespace, obj.ResourceVersion, obj.ManagedFields)
			for _, rule := range obj.Spec.Rules {
				if rule.HTTP != nil {
					for _, path := range rule.HTTP.Paths {
						rec.IngressRules = append(rec.IngressRules,
							fmt.Sprintf("%s%s→%s:%d",
								rule.Host, path.Path,
								path.Backend.Service.Name,
								path.Backend.Service.Port.Number,
							))
					}
				}
			}
			snap.Resources["Ingress/"+obj.Namespace+"/"+obj.Name] = rec
		}
	}

	// ── HPA ──────────────────────────────────────────────────────────────────
	if hpas, err := e.k8s.AutoscalingV2().HorizontalPodAutoscalers(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range hpas.Items {
			rec := newRecord("HPA", obj.Name, obj.Namespace, obj.ResourceVersion, obj.ManagedFields)
			rec.HPAMin = *obj.Spec.MinReplicas
			rec.HPAMax = obj.Spec.MaxReplicas
			rec.HPATarget = fmt.Sprintf("%s/%s", obj.Spec.ScaleTargetRef.Kind, obj.Spec.ScaleTargetRef.Name)
			snap.Resources["HPA/"+obj.Namespace+"/"+obj.Name] = rec
		}
	}

	// ── PVCs ─────────────────────────────────────────────────────────────────
	if pvcs, err := e.k8s.CoreV1().PersistentVolumeClaims(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range pvcs.Items {
			rec := newRecord("PVC", obj.Name, obj.Namespace, obj.ResourceVersion, obj.ManagedFields)
			if storage, ok := obj.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
				rec.StorageRequest = storage.String()
			}
			if obj.Spec.StorageClassName != nil {
				rec.StorageClass = *obj.Spec.StorageClassName
			}
			snap.Resources["PVC/"+obj.Namespace+"/"+obj.Name] = rec
		}
	}

	// ── Roles ────────────────────────────────────────────────────────────────
	if roles, err := e.k8s.RbacV1().Roles(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range roles.Items {
			rec := newRecord("Role", obj.Name, obj.Namespace, obj.ResourceVersion, obj.ManagedFields)
			for _, rule := range obj.Rules {
				rec.Rules = append(rec.Rules,
					fmt.Sprintf("%v on %v", rule.Verbs, rule.Resources))
			}
			snap.Resources["Role/"+obj.Namespace+"/"+obj.Name] = rec
		}
	}

	// ── ClusterRoles ─────────────────────────────────────────────────────────
	if crs, err := e.k8s.RbacV1().ClusterRoles().List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range crs.Items {
			if strings.HasPrefix(obj.Name, "system:") {
				continue // skip system roles
			}
			rec := newRecord("ClusterRole", obj.Name, "", obj.ResourceVersion, obj.ManagedFields)
			for _, rule := range obj.Rules {
				rec.Rules = append(rec.Rules,
					fmt.Sprintf("%v on %v", rule.Verbs, rule.Resources))
			}
			snap.Resources["ClusterRole//"+obj.Name] = rec
		}
	}

	snap.ResourceCount = len(snap.Resources)
	return snap, nil
}

// DeepSnapshotDiff compares two deep snapshots — exact field-level changes
func (e *Engine) DeepSnapshotDiff(baseline *DeepStateSnapshot) ([]DeepDiffEntry, error) {
	current, err := e.CaptureDeepSnapshot()
	if err != nil {
		return nil, err
	}

	var diffs []DeepDiffEntry
	now := current.CapturedAt

	// New resources
	for key, cur := range current.Resources {
		if _, existed := baseline.Resources[key]; !existed {
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: now, Kind: cur.Kind,
				Name: cur.Name, Namespace: cur.Namespace,
				Field: "existence", OldValue: "(did not exist)", NewValue: "created",
				ChangedBy: cur.FieldManager,
				Risk:      "new resource — verify this was intentional",
			})
		}
	}

	// Deleted or changed resources
	for key, base := range baseline.Resources {
		cur, exists := current.Resources[key]
		if !exists {
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: now, Kind: base.Kind,
				Name: base.Name, Namespace: base.Namespace,
				Field: "existence", OldValue: "existed", NewValue: "(deleted)",
				ChangedBy: base.FieldManager,
				Risk:      "resource deleted — verify this was intentional",
			})
			continue
		}
		if cur.ResourceVersion == base.ResourceVersion {
			continue
		}

		fm := cur.FieldManager
		if fm == "" {
			fm = base.FieldManager
		}

		switch base.Kind {
		case "Deployment", "StatefulSet", "DaemonSet":
			diffs = append(diffs, diffWorkload(base, cur, fm, now)...)
		case "ConfigMap":
			diffs = append(diffs, diffConfigMap(base, cur, fm, now)...)
		case "Secret":
			diffs = append(diffs, diffSecret(base, cur, fm, now)...)
		case "Service":
			diffs = append(diffs, diffService(base, cur, fm, now)...)
		case "Ingress":
			diffs = append(diffs, diffIngress(base, cur, fm, now)...)
		case "HPA":
			diffs = append(diffs, diffHPA(base, cur, fm, now)...)
		case "Role", "ClusterRole":
			diffs = append(diffs, diffRBAC(base, cur, fm, now)...)
		case "PVC":
			diffs = append(diffs, diffPVC(base, cur, fm, now)...)
		}
	}

	correlateDiffs(diffs, e)
	sortDiffs(diffs)
	return diffs, nil
}

// LiveDeepDiff detects recently changed resources and reports current values
func (e *Engine) LiveDeepDiff(window time.Duration) ([]DeepDiffEntry, error) {
	cutoff := time.Now().Add(-window)
	var diffs []DeepDiffEntry
	ns := e.ns()

	// Deployments
	if deploys, err := e.k8s.AppsV1().Deployments(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range deploys.Items {
			fm, changeTime, changed := recentlyChanged(obj.ManagedFields, cutoff)
			if !changed {
				continue
			}
			for _, c := range obj.Spec.Template.Spec.Containers {
				risk := "image may have changed"
				if strings.Contains(c.Image, ":latest") {
					risk = "WARNING: :latest tag in use — cannot determine previous version"
				}
				diffs = append(diffs, DeepDiffEntry{
					Timestamp: changeTime, Kind: "Deployment",
					Name: obj.Name, Namespace: obj.Namespace,
					Field:     fmt.Sprintf("container[%s].image", c.Name),
					OldValue:  "(save snapshot before changes for exact value)",
					NewValue:  c.Image,
					ChangedBy: fm, Risk: risk,
				})
			}
			if obj.Spec.Replicas != nil {
				diffs = append(diffs, DeepDiffEntry{
					Timestamp: changeTime, Kind: "Deployment",
					Name: obj.Name, Namespace: obj.Namespace,
					Field:     "replicas",
					OldValue:  "(unknown — use --save/--load)",
					NewValue:  fmt.Sprintf("%d", *obj.Spec.Replicas),
					ChangedBy: fm,
				})
			}
		}
	}

	// StatefulSets
	if ssets, err := e.k8s.AppsV1().StatefulSets(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range ssets.Items {
			fm, changeTime, changed := recentlyChanged(obj.ManagedFields, cutoff)
			if !changed {
				continue
			}
			for _, c := range obj.Spec.Template.Spec.Containers {
				diffs = append(diffs, DeepDiffEntry{
					Timestamp: changeTime, Kind: "StatefulSet",
					Name: obj.Name, Namespace: obj.Namespace,
					Field:     fmt.Sprintf("container[%s].image", c.Name),
					OldValue:  "(use --save/--load for exact value)",
					NewValue:  c.Image,
					ChangedBy: fm, Risk: "statefulset image changed — rolling update may cause data issues",
				})
			}
		}
	}

	// DaemonSets
	if dsets, err := e.k8s.AppsV1().DaemonSets(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range dsets.Items {
			fm, changeTime, changed := recentlyChanged(obj.ManagedFields, cutoff)
			if !changed {
				continue
			}
			for _, c := range obj.Spec.Template.Spec.Containers {
				diffs = append(diffs, DeepDiffEntry{
					Timestamp: changeTime, Kind: "DaemonSet",
					Name: obj.Name, Namespace: obj.Namespace,
					Field:     fmt.Sprintf("container[%s].image", c.Name),
					OldValue:  "(use --save/--load for exact value)",
					NewValue:  c.Image,
					ChangedBy: fm, Risk: "daemonset changed — will roll out to ALL nodes",
				})
			}
		}
	}

	// ConfigMaps
	if cms, err := e.k8s.CoreV1().ConfigMaps(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range cms.Items {
			if obj.Namespace == "kube-system" {
				continue
			}
			fm, changeTime, changed := recentlyChanged(obj.ManagedFields, cutoff)
			if !changed {
				continue
			}
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: changeTime, Kind: "ConfigMap",
				Name: obj.Name, Namespace: obj.Namespace,
				Field:     fmt.Sprintf("data (%d keys)", len(obj.Data)),
				OldValue:  "(use --save/--load for exact key values)",
				NewValue:  fmt.Sprintf("%d keys present", len(obj.Data)),
				ChangedBy: fm, Risk: "config changed — pods may need restart to pick up new values",
			})
		}
	}

	// Secrets
	if secrets, err := e.k8s.CoreV1().Secrets(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range secrets.Items {
			if obj.Namespace == "kube-system" || obj.Type == "kubernetes.io/service-account-token" {
				continue
			}
			fm, changeTime, changed := recentlyChanged(obj.ManagedFields, cutoff)
			if !changed {
				continue
			}
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: changeTime, Kind: "Secret",
				Name: obj.Name, Namespace: obj.Namespace,
				Field:     fmt.Sprintf("%d keys modified", len(obj.Data)),
				OldValue:  "(secret values never logged)",
				NewValue:  "secret updated",
				ChangedBy: fm, Risk: "secret changed — pods using this secret may need restart",
			})
		}
	}

	// Services
	if svcs, err := e.k8s.CoreV1().Services(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range svcs.Items {
			fm, changeTime, changed := recentlyChanged(obj.ManagedFields, cutoff)
			if !changed {
				continue
			}
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: changeTime, Kind: "Service",
				Name: obj.Name, Namespace: obj.Namespace,
				Field:     "spec",
				OldValue:  "(use --save/--load for exact value)",
				NewValue:  fmt.Sprintf("type=%s", obj.Spec.Type),
				ChangedBy: fm, Risk: "service changed — could affect traffic routing",
			})
		}
	}

	// Ingresses
	if ingresses, err := e.k8s.NetworkingV1().Ingresses(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range ingresses.Items {
			fm, changeTime, changed := recentlyChanged(obj.ManagedFields, cutoff)
			if !changed {
				continue
			}
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: changeTime, Kind: "Ingress",
				Name: obj.Name, Namespace: obj.Namespace,
				Field:     fmt.Sprintf("rules (%d)", len(obj.Spec.Rules)),
				OldValue:  "(use --save/--load for exact rules)",
				NewValue:  "ingress rules updated",
				ChangedBy: fm, Risk: "ingress changed — could break external traffic routing",
			})
		}
	}

	// HPA
	if hpas, err := e.k8s.AutoscalingV2().HorizontalPodAutoscalers(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range hpas.Items {
			fm, changeTime, changed := recentlyChanged(obj.ManagedFields, cutoff)
			if !changed {
				continue
			}
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: changeTime, Kind: "HPA",
				Name: obj.Name, Namespace: obj.Namespace,
				Field:     "spec",
				OldValue:  "(use --save/--load for exact value)",
				NewValue:  fmt.Sprintf("min=%d max=%d target=%s", *obj.Spec.MinReplicas, obj.Spec.MaxReplicas, obj.Spec.ScaleTargetRef.Name),
				ChangedBy: fm, Risk: "HPA changed — scaling behaviour may be affected",
			})
		}
	}

	// RBAC Roles
	if roles, err := e.k8s.RbacV1().Roles(ns).List(e.ctx, metav1.ListOptions{}); err == nil {
		for _, obj := range roles.Items {
			fm, changeTime, changed := recentlyChanged(obj.ManagedFields, cutoff)
			if !changed {
				continue
			}
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: changeTime, Kind: "Role",
				Name: obj.Name, Namespace: obj.Namespace,
				Field:     fmt.Sprintf("rules (%d)", len(obj.Rules)),
				OldValue:  "(use --save/--load for exact rules)",
				NewValue:  "role rules updated",
				ChangedBy: fm, Risk: "RBAC role changed — could grant or revoke permissions",
			})
		}
	}

	correlateDiffs(diffs, e)
	sortDiffs(diffs)
	return diffs, nil
}

// ── field-level diff helpers ──────────────────────────────────────────────────

func diffWorkload(base, cur DeepResourceRecord, fm string, now time.Time) []DeepDiffEntry {
	var diffs []DeepDiffEntry

	// Replicas
	if cur.Replicas != base.Replicas {
		risk := "scaled up"
		if cur.Replicas < base.Replicas {
			risk = "scaled DOWN — reduced fault tolerance"
		}
		diffs = append(diffs, DeepDiffEntry{
			Timestamp: now, Kind: base.Kind, Name: base.Name, Namespace: base.Namespace,
			Field:     "replicas",
			OldValue:  fmt.Sprintf("%d", base.Replicas),
			NewValue:  fmt.Sprintf("%d", cur.Replicas),
			ChangedBy: fm, Risk: risk,
		})
	}

	// Images
	for container, newImage := range cur.Images {
		oldImage := base.Images[container]
		if oldImage != newImage && oldImage != "" {
			risk := "image changed — verify the new image is stable"
			if strings.Contains(newImage, ":latest") {
				risk = "WARNING: changed to :latest — uncontrolled, not reproducible"
			}
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: now, Kind: base.Kind, Name: base.Name, Namespace: base.Namespace,
				Field:    fmt.Sprintf("container[%s].image", container),
				OldValue: oldImage, NewValue: newImage,
				ChangedBy: fm, Risk: risk,
			})
		}
	}

	// Env vars added/changed
	for key, newVal := range cur.EnvVars {
		oldVal, existed := base.EnvVars[key]
		if !existed {
			parts := strings.SplitN(key, "/", 2)
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: now, Kind: base.Kind, Name: base.Name, Namespace: base.Namespace,
				Field:    fmt.Sprintf("container[%s].env.%s", parts[0], parts[1]),
				OldValue: "(not set)", NewValue: maskSecret(parts[1], newVal),
				ChangedBy: fm, Risk: "new env var added",
			})
		} else if oldVal != newVal {
			parts := strings.SplitN(key, "/", 2)
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: now, Kind: base.Kind, Name: base.Name, Namespace: base.Namespace,
				Field:     fmt.Sprintf("container[%s].env.%s", parts[0], parts[1]),
				OldValue:  maskSecret(parts[1], oldVal),
				NewValue:  maskSecret(parts[1], newVal),
				ChangedBy: fm, Risk: "env var changed — may affect app behaviour",
			})
		}
	}
	// Env vars removed
	for key, oldVal := range base.EnvVars {
		if _, exists := cur.EnvVars[key]; !exists {
			parts := strings.SplitN(key, "/", 2)
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: now, Kind: base.Kind, Name: base.Name, Namespace: base.Namespace,
				Field:    fmt.Sprintf("container[%s].env.%s", parts[0], parts[1]),
				OldValue: maskSecret(parts[1], oldVal), NewValue: "(removed)",
				ChangedBy: fm, Risk: "env var removed — app may crash if it was required",
			})
		}
	}

	// Resource limits/requests
	for key, newVal := range cur.Resources {
		oldVal := base.Resources[key]
		if oldVal != newVal && oldVal != "" {
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: now, Kind: base.Kind, Name: base.Name, Namespace: base.Namespace,
				Field: key, OldValue: oldVal, NewValue: newVal,
				ChangedBy: fm, Risk: "resource changed — monitor for OOM or CPU throttling",
			})
		}
	}

	return diffs
}

func diffConfigMap(base, cur DeepResourceRecord, fm string, now time.Time) []DeepDiffEntry {
	var diffs []DeepDiffEntry
	for key, newVal := range cur.Data {
		oldVal, existed := base.Data[key]
		if !existed {
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: now, Kind: "ConfigMap", Name: base.Name, Namespace: base.Namespace,
				Field:    fmt.Sprintf("data.%s", key),
				OldValue: "(key did not exist)", NewValue: truncate(newVal, 80),
				ChangedBy: fm, Risk: "new config key added",
			})
		} else if oldVal != newVal {
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: now, Kind: "ConfigMap", Name: base.Name, Namespace: base.Namespace,
				Field:    fmt.Sprintf("data.%s", key),
				OldValue: truncate(oldVal, 80), NewValue: truncate(newVal, 80),
				ChangedBy: fm, Risk: "config value changed — pods may need restart",
			})
		}
	}
	for key := range base.Data {
		if _, exists := cur.Data[key]; !exists {
			diffs = append(diffs, DeepDiffEntry{
				Timestamp: now, Kind: "ConfigMap", Name: base.Name, Namespace: base.Namespace,
				Field:    fmt.Sprintf("data.%s", key),
				OldValue: "(had value)", NewValue: "(key removed)",
				ChangedBy: fm, Risk: "config key removed — pods referencing it will fail",
			})
		}
	}
	return diffs
}

func diffSecret(base, cur DeepResourceRecord, fm string, now time.Time) []DeepDiffEntry {
	// Never log secret values — only report that keys changed
	addedKeys := []string{}
	removedKeys := []string{}
	baseKeys := map[string]bool{}
	curKeys := map[string]bool{}
	for _, k := range base.SecretKeys {
		baseKeys[k] = true
	}
	for _, k := range cur.SecretKeys {
		curKeys[k] = true
		if !baseKeys[k] {
			addedKeys = append(addedKeys, k)
		}
	}
	for _, k := range base.SecretKeys {
		if !curKeys[k] {
			removedKeys = append(removedKeys, k)
		}
	}
	var diffs []DeepDiffEntry
	if len(addedKeys) > 0 {
		diffs = append(diffs, DeepDiffEntry{
			Timestamp: time.Now(), Kind: "Secret", Name: base.Name, Namespace: base.Namespace,
			Field: "keys added", OldValue: "(not present)", NewValue: strings.Join(addedKeys, ", "),
			ChangedBy: fm, Risk: "new secret keys added",
		})
	}
	if len(removedKeys) > 0 {
		diffs = append(diffs, DeepDiffEntry{
			Timestamp: time.Now(), Kind: "Secret", Name: base.Name, Namespace: base.Namespace,
			Field: "keys removed", OldValue: strings.Join(removedKeys, ", "), NewValue: "(removed)",
			ChangedBy: fm, Risk: "secret keys removed — apps relying on them will fail",
		})
	}
	if len(addedKeys) == 0 && len(removedKeys) == 0 && base.ResourceVersion != cur.ResourceVersion {
		diffs = append(diffs, DeepDiffEntry{
			Timestamp: time.Now(), Kind: "Secret", Name: base.Name, Namespace: base.Namespace,
			Field: "secret data", OldValue: "***", NewValue: "*** (values changed, keys unchanged)",
			ChangedBy: fm, Risk: "secret values rotated — pods may need restart",
		})
	}
	return diffs
}

func diffService(base, cur DeepResourceRecord, fm string, now time.Time) []DeepDiffEntry {
	var diffs []DeepDiffEntry
	if base.ServiceType != cur.ServiceType {
		diffs = append(diffs, DeepDiffEntry{
			Timestamp: now, Kind: "Service", Name: base.Name, Namespace: base.Namespace,
			Field: "spec.type", OldValue: base.ServiceType, NewValue: cur.ServiceType,
			ChangedBy: fm, Risk: "service type changed — could affect traffic routing or expose service publicly",
		})
	}
	return diffs
}

func diffIngress(base, cur DeepResourceRecord, fm string, now time.Time) []DeepDiffEntry {
	var diffs []DeepDiffEntry
	oldRules := strings.Join(base.IngressRules, "|")
	newRules := strings.Join(cur.IngressRules, "|")
	if oldRules != newRules {
		diffs = append(diffs, DeepDiffEntry{
			Timestamp: now, Kind: "Ingress", Name: base.Name, Namespace: base.Namespace,
			Field: "rules", OldValue: oldRules, NewValue: newRules,
			ChangedBy: fm, Risk: "ingress rules changed — external routing may be broken",
		})
	}
	return diffs
}

func diffHPA(base, cur DeepResourceRecord, fm string, now time.Time) []DeepDiffEntry {
	var diffs []DeepDiffEntry
	if base.HPAMax != cur.HPAMax {
		diffs = append(diffs, DeepDiffEntry{
			Timestamp: now, Kind: "HPA", Name: base.Name, Namespace: base.Namespace,
			Field:     "maxReplicas",
			OldValue:  fmt.Sprintf("%d", base.HPAMax),
			NewValue:  fmt.Sprintf("%d", cur.HPAMax),
			ChangedBy: fm, Risk: "HPA max changed — may limit or expand scaling headroom",
		})
	}
	if base.HPAMin != cur.HPAMin {
		diffs = append(diffs, DeepDiffEntry{
			Timestamp: now, Kind: "HPA", Name: base.Name, Namespace: base.Namespace,
			Field:     "minReplicas",
			OldValue:  fmt.Sprintf("%d", base.HPAMin),
			NewValue:  fmt.Sprintf("%d", cur.HPAMin),
			ChangedBy: fm, Risk: "HPA min changed",
		})
	}
	return diffs
}

func diffRBAC(base, cur DeepResourceRecord, fm string, now time.Time) []DeepDiffEntry {
	var diffs []DeepDiffEntry
	oldRules := strings.Join(base.Rules, "|")
	newRules := strings.Join(cur.Rules, "|")
	if oldRules != newRules {
		diffs = append(diffs, DeepDiffEntry{
			Timestamp: now, Kind: base.Kind, Name: base.Name, Namespace: base.Namespace,
			Field: "rules", OldValue: oldRules, NewValue: newRules,
			ChangedBy: fm, Risk: "RBAC rules changed — review for privilege escalation",
		})
	}
	return diffs
}

func diffPVC(base, cur DeepResourceRecord, fm string, now time.Time) []DeepDiffEntry {
	var diffs []DeepDiffEntry
	if base.StorageRequest != cur.StorageRequest {
		diffs = append(diffs, DeepDiffEntry{
			Timestamp: now, Kind: "PVC", Name: base.Name, Namespace: base.Namespace,
			Field:    "storage request",
			OldValue: base.StorageRequest, NewValue: cur.StorageRequest,
			ChangedBy: fm, Risk: "PVC storage changed",
		})
	}
	return diffs
}

// ── shared helpers ────────────────────────────────────────────────────────────

func newRecord(kind, name, namespace, rv string, mfs []metav1.ManagedFieldsEntry) DeepResourceRecord {
	fm := ""
	if len(mfs) > 0 {
		fm = mfs[len(mfs)-1].Manager
	}
	return DeepResourceRecord{
		Kind: kind, Name: name, Namespace: namespace,
		ResourceVersion: rv, FieldManager: fm,
		CapturedAt: time.Now(),
		Images:     map[string]string{}, EnvVars: map[string]string{},
		Resources: map[string]string{}, Data: map[string]string{},
	}
}

func extractContainerSpec(containers []corev1.Container, rec *DeepResourceRecord) {
	for _, c := range containers {
		rec.Images[c.Name] = c.Image
		for _, env := range c.Env {
			rec.EnvVars[c.Name+"/"+env.Name] = env.Value
		}
		if c.Resources.Limits != nil {
			if !c.Resources.Limits.Cpu().IsZero() {
				rec.Resources[c.Name+"/limits/cpu"] = c.Resources.Limits.Cpu().String()
			}
			if !c.Resources.Limits.Memory().IsZero() {
				rec.Resources[c.Name+"/limits/memory"] = c.Resources.Limits.Memory().String()
			}
		}
		if c.Resources.Requests != nil {
			if !c.Resources.Requests.Cpu().IsZero() {
				rec.Resources[c.Name+"/requests/cpu"] = c.Resources.Requests.Cpu().String()
			}
			if !c.Resources.Requests.Memory().IsZero() {
				rec.Resources[c.Name+"/requests/memory"] = c.Resources.Requests.Memory().String()
			}
		}
	}
}

func recentlyChanged(mfs []metav1.ManagedFieldsEntry, cutoff time.Time) (string, time.Time, bool) {
	fm := ""
	var latest time.Time
	for _, mf := range mfs {
		if mf.Time != nil && mf.Time.Time.After(cutoff) {
			if mf.Time.Time.After(latest) {
				latest = mf.Time.Time
				fm = mf.Manager
			}
		}
	}
	return fm, latest, !latest.IsZero()
}

func filterAnnotations(annotations map[string]string) map[string]string {
	out := map[string]string{}
	skip := []string{"kubectl.kubernetes.io/last-applied-configuration", "deployment.kubernetes.io/revision"}
	for k, v := range annotations {
		skipThis := false
		for _, s := range skip {
			if k == s {
				skipThis = true
				break
			}
		}
		if !skipThis {
			out[k] = v
		}
	}
	return out
}

func maskSecret(key, value string) string {
	lower := strings.ToLower(key)
	for _, s := range []string{"password", "secret", "token", "key", "auth", "credential", "passwd", "pwd"} {
		if strings.Contains(lower, s) {
			if value == "" {
				return "(empty)"
			}
			return "***REDACTED***"
		}
	}
	return value
}

func correlateDiffs(diffs []DeepDiffEntry, e *Engine) {
	faults, _ := e.PodHealth()
	for i, d := range diffs {
		for _, f := range faults {
			if f.Score > 0 && f.Object != "" &&
				(strings.HasPrefix(f.Object, d.Name) || d.Name == f.Object) {
				diffs[i].CorrelatedFault = f.Title
				diffs[i].Mitigation = mitigationFor(f.Title, d.Kind, d.Name, d.Namespace)
				break
			}
		}
	}
}

func sortDiffs(diffs []DeepDiffEntry) {
	for i := 1; i < len(diffs); i++ {
		for j := i; j > 0 && diffs[j].Timestamp.After(diffs[j-1].Timestamp); j-- {
			diffs[j], diffs[j-1] = diffs[j-1], diffs[j]
		}
	}
}
