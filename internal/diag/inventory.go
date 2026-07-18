package diag

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// ─────────────────────────────────────────────────────────────
// Noise profile — excluded by default, same spirit as kubectl-inventory
// ─────────────────────────────────────────────────────────────

var defaultExcludedGroups = map[string]bool{
	"metrics.k8s.io":      true,
	"coordination.k8s.io": true, // leases
}

var defaultExcludedResources = map[string]bool{
	"events":                    true,
	"componentstatuses":         true,
	"bindings":                  true,
	"localsubjectaccessreviews": true,
	"selfsubjectaccessreviews":  true,
	"selfsubjectrulesreviews":   true,
	"subjectaccessreviews":      true,
	"tokenreviews":              true,
}

// gitops signals we recognize on any object
var gitopsLabelKeys = []string{
	"argocd.argoproj.io/instance",
	"kustomize.toolkit.fluxcd.io/name",
	"helm.toolkit.fluxcd.io/name",
}

var gitopsAnnotationKeys = []string{
	"meta.helm.sh/release-name",
	"argocd.argoproj.io/tracking-id",
}

var gitopsManagers = map[string]bool{
	"argocd-application-controller": true,
	"helm":                          true,
	"kustomize-controller":          true,
	"helm-controller":               true,
	"flux":                          true,
}

// ─────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────

type InventoryOptions struct {
	Namespace        string
	AllNamespaces    bool
	APIGroups        []string
	ExcludeAPIGroups []string
	Resources        []string
	ExcludeResources []string
	IncludeEvents    bool
	IncludeNoisy     bool
	Selector         string
	AgeFilter        time.Duration // 0 = no filter; only used by orphans/stuck listing
}

type ResourceCount struct {
	Kind       string `json:"kind"`
	Count      int    `json:"count"`
	Suspicious int    `json:"suspicious,omitempty"`
}

type GroupSummary struct {
	Group     string           `json:"group"` // "" = core
	Resources []ResourceCount  `json:"resources"`
}

type ObjectRef struct {
	GVR               schema.GroupVersionResource `json:"-"`
	Kind              string                      `json:"kind"`
	Name              string                      `json:"name"`
	Namespace         string                      `json:"namespace,omitempty"`
	UID               string                      `json:"uid,omitempty"`
	Age               time.Duration               `json:"-"`
	CreatedAt         time.Time                   `json:"createdAt,omitempty"`
	Manager           string                      `json:"manager,omitempty"`
	OwnerRefs         []metav1.OwnerReference     `json:"ownerReferences,omitempty"`
	Labels            map[string]string           `json:"labels,omitempty"`
	Annotations       map[string]string           `json:"annotations,omitempty"`
	DeletionTimestamp *time.Time                  `json:"deletionTimestamp,omitempty"`
	Finalizers        []string                    `json:"finalizers,omitempty"`
}

type Classification struct {
	Status  string   `json:"status"` // OK | GITOPS | SUSPICIOUS | STUCK
	Reasons []string `json:"reasons,omitempty"`
}

type InventoryEntry struct {
	Obj   ObjectRef      `json:"object"`
	Class Classification `json:"classification"`
}

type InventoryReport struct {
	Namespace      string           `json:"namespace"`
	Groups         []GroupSummary   `json:"groups"`
	Entries        []InventoryEntry `json:"-"`
	Stuck          []InventoryEntry `json:"stuck"`
	Suspicious     []InventoryEntry `json:"suspicious"`
	TotalResources int              `json:"totalResources"`
	ScannedTypes   int              `json:"scannedTypes"`
	SkippedTypes   int              `json:"skippedTypes"`
	Duration       time.Duration    `json:"-"`
}

// ─────────────────────────────────────────────────────────────
// Resource discovery
// ─────────────────────────────────────────────────────────────

func (e *Engine) discoverResources(opts InventoryOptions) ([]schema.GroupVersionResource, map[schema.GroupVersionResource]bool, int, error) {
	lists, err := e.disc.ServerPreferredResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return nil, nil, 0, fmt.Errorf("discovery failed: %w", err)
	}

	var gvrs []schema.GroupVersionResource
	namespaced := map[schema.GroupVersionResource]bool{}
	skipped := 0

	for _, list := range lists {
		if list == nil {
			continue
		}
		gv, perr := schema.ParseGroupVersion(list.GroupVersion)
		if perr != nil {
			continue
		}
		for _, r := range list.APIResources {
			if strings.Contains(r.Name, "/") {
				continue // subresource, e.g. pods/log
			}
			if !hasVerb(r.Verbs, "list") {
				skipped++
				continue
			}
			if !opts.IncludeNoisy {
				if defaultExcludedGroups[gv.Group] {
					skipped++
					continue
				}
				if defaultExcludedResources[r.Name] {
					skipped++
					continue
				}
				if !opts.IncludeEvents && r.Name == "events" {
					skipped++
					continue
				}
			}
			if len(opts.APIGroups) > 0 && !inList(gv.Group, opts.APIGroups) {
				continue
			}
			if inList(gv.Group, opts.ExcludeAPIGroups) {
				continue
			}
			if len(opts.Resources) > 0 && !inList(r.Name, opts.Resources) {
				continue
			}
			if inList(r.Name, opts.ExcludeResources) {
				continue
			}
			gvr := gv.WithResource(r.Name)
			gvrs = append(gvrs, gvr)
			namespaced[gvr] = r.Namespaced
		}
	}
	return gvrs, namespaced, skipped, nil
}

// resolveGVR maps a kind/shortname/plural argument (e.g. "pod", "secret", "cm") to a GVR.
func (e *Engine) resolveGVR(kindArg string) (schema.GroupVersionResource, bool, error) {
	lists, err := e.disc.ServerPreferredResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return schema.GroupVersionResource{}, false, fmt.Errorf("discovery failed: %w", err)
	}
	target := strings.ToLower(kindArg)
	for _, list := range lists {
		if list == nil {
			continue
		}
		gv, perr := schema.ParseGroupVersion(list.GroupVersion)
		if perr != nil {
			continue
		}
		for _, r := range list.APIResources {
			if strings.Contains(r.Name, "/") {
				continue
			}
			if strings.ToLower(r.Name) == target ||
				strings.ToLower(r.Kind) == target ||
				strings.ToLower(r.SingularName) == target {
				return gv.WithResource(r.Name), r.Namespaced, nil
			}
			for _, sn := range r.ShortNames {
				if strings.ToLower(sn) == target {
					return gv.WithResource(r.Name), r.Namespaced, nil
				}
			}
		}
	}
	return schema.GroupVersionResource{}, false, fmt.Errorf("unknown resource kind %q", kindArg)
}

// ─────────────────────────────────────────────────────────────
// Scan
// ─────────────────────────────────────────────────────────────

func (e *Engine) ScanNamespace(opts InventoryOptions) (*InventoryReport, error) {
	start := time.Now()
	gvrs, namespaced, skipped, err := e.discoverResources(opts)
	if err != nil {
		return nil, err
	}

	report := &InventoryReport{Namespace: opts.Namespace}
	groupMap := map[string]map[string]*ResourceCount{}

	var listOpts metav1.ListOptions
	if opts.Selector != "" {
		listOpts.LabelSelector = opts.Selector
	}

	for _, gvr := range gvrs {
		nsed := namespaced[gvr]

		// A namespace was requested but this resource is cluster-scoped: skip,
		// unless the user asked for all-namespaces scope.
		if !nsed && opts.Namespace != "" && !opts.AllNamespaces {
			continue
		}

		var list *unstructured.UnstructuredList
		var lerr error
		if nsed {
			ns := opts.Namespace
			if opts.AllNamespaces {
				ns = metav1.NamespaceAll
			}
			list, lerr = e.dyn.Resource(gvr).Namespace(ns).List(e.ctx, listOpts)
		} else {
			list, lerr = e.dyn.Resource(gvr).List(e.ctx, listOpts)
		}
		if lerr != nil {
			report.SkippedTypes++
			continue
		}
		if len(list.Items) == 0 {
			continue
		}
		report.ScannedTypes++

		group := gvr.Group
		if _, ok := groupMap[group]; !ok {
			groupMap[group] = map[string]*ResourceCount{}
		}

		for i := range list.Items {
			item := &list.Items[i]
			obj := toObjectRef(gvr, item)
			class := classify(obj)

			entry := InventoryEntry{Obj: obj, Class: class}
			report.Entries = append(report.Entries, entry)
			report.TotalResources++

			kindKey := item.GetKind()
			if kindKey == "" {
				kindKey = gvr.Resource
			}
			rc, ok := groupMap[group][kindKey]
			if !ok {
				rc = &ResourceCount{Kind: kindKey}
				groupMap[group][kindKey] = rc
			}
			rc.Count++

			switch class.Status {
			case "SUSPICIOUS":
				rc.Suspicious++
				report.Suspicious = append(report.Suspicious, entry)
			case "STUCK":
				report.Stuck = append(report.Stuck, entry)
			}
		}
	}
	report.SkippedTypes += skipped

	var groupNames []string
	for g := range groupMap {
		groupNames = append(groupNames, g)
	}
	sort.Slice(groupNames, func(i, j int) bool {
		if groupNames[i] == "" {
			return true
		}
		if groupNames[j] == "" {
			return false
		}
		return groupNames[i] < groupNames[j]
	})
	for _, g := range groupNames {
		var rcs []ResourceCount
		for _, rc := range groupMap[g] {
			rcs = append(rcs, *rc)
		}
		sort.Slice(rcs, func(i, j int) bool { return rcs[i].Kind < rcs[j].Kind })
		report.Groups = append(report.Groups, GroupSummary{Group: g, Resources: rcs})
	}

	report.Duration = time.Since(start)
	return report, nil
}

// ─────────────────────────────────────────────────────────────
// Explain a single resource
// ─────────────────────────────────────────────────────────────

type ExplainResult struct {
	Entry        InventoryEntry
	ReferencedBy []string
}

func (e *Engine) ExplainResource(kindArg, name, namespace string) (*ExplainResult, error) {
	gvr, namespacedRes, err := e.resolveGVR(kindArg)
	if err != nil {
		return nil, err
	}

	var item *unstructured.Unstructured
	if namespacedRes {
		if namespace == "" {
			namespace = "default"
		}
		item, err = e.dyn.Resource(gvr).Namespace(namespace).Get(e.ctx, name, metav1.GetOptions{})
	} else {
		item, err = e.dyn.Resource(gvr).Get(e.ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("%s/%s not found in ns %s: %w", kindArg, name, namespace, err)
	}

	obj := toObjectRef(gvr, item)
	class := classify(obj)

	var refs []string
	switch strings.ToLower(item.GetKind()) {
	case "configmap":
		refs = e.findReferencingPods(namespace, "configmap", name)
	case "secret":
		refs = e.findReferencingPods(namespace, "secret", name)
	case "service":
		refs = e.findReferencingIngresses(namespace, name)
	}

	return &ExplainResult{Entry: InventoryEntry{Obj: obj, Class: class}, ReferencedBy: refs}, nil
}

// ─────────────────────────────────────────────────────────────
// Classification logic
// ─────────────────────────────────────────────────────────────

func classify(obj ObjectRef) Classification {
	// stuck takes priority over everything else
	if obj.DeletionTimestamp != nil && len(obj.Finalizers) > 0 {
		age := time.Since(*obj.DeletionTimestamp)
		if age > 2*time.Minute {
			return Classification{
				Status: "STUCK",
				Reasons: []string{
					fmt.Sprintf("deletionTimestamp set %s ago", age.Round(time.Second)),
					fmt.Sprintf("blocked by finalizers: %s", strings.Join(obj.Finalizers, ", ")),
				},
			}
		}
	}

	var reasons []string
	gitops := false

	for _, k := range gitopsLabelKeys {
		if v := obj.Labels[k]; v != "" {
			gitops = true
			reasons = append(reasons, fmt.Sprintf("gitops label %s=%s", k, v))
			break
		}
	}
	if !gitops {
		for _, k := range gitopsAnnotationKeys {
			if v := obj.Annotations[k]; v != "" {
				gitops = true
				reasons = append(reasons, fmt.Sprintf("gitops annotation %s=%s", k, v))
				break
			}
		}
	}
	if !gitops && gitopsManagers[obj.Manager] {
		gitops = true
		reasons = append(reasons, "managed by "+obj.Manager)
	}
	// app.kubernetes.io/managed-by=Helm is a strong signal too
	if !gitops && strings.EqualFold(obj.Labels["app.kubernetes.io/managed-by"], "Helm") {
		gitops = true
		reasons = append(reasons, "label app.kubernetes.io/managed-by=Helm")
	}

	hasOwner := len(obj.OwnerRefs) > 0

	if hasOwner || gitops {
		status := "OK"
		if gitops && !hasOwner {
			status = "GITOPS"
		}
		return Classification{Status: status, Reasons: reasons}
	}

	r := []string{"no ownerReferences", "no known GitOps/controller metadata detected"}
	if obj.Manager != "" {
		r = append(r, fmt.Sprintf("created/managed by: %s", obj.Manager))
	}
	r = append(r, fmt.Sprintf("age: %s", formatAge(obj.Age)))
	return Classification{Status: "SUSPICIOUS", Reasons: r}
}

// ─────────────────────────────────────────────────────────────
// Lightweight reference graph — pods -> configmap/secret, ingress -> service
// ─────────────────────────────────────────────────────────────

func (e *Engine) findReferencingPods(namespace, kind, name string) []string {
	pods, err := e.k8s.CoreV1().Pods(namespace).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	var refs []string
	for _, pod := range pods.Items {
		if podReferences(&pod, kind, name) {
			refs = append(refs, "pod/"+pod.Name)
		}
	}
	return refs
}

func podReferences(pod *corev1.Pod, kind, name string) bool {
	check := func(cs []corev1.Container) bool {
		for _, c := range cs {
			for _, ef := range c.EnvFrom {
				if kind == "configmap" && ef.ConfigMapRef != nil && ef.ConfigMapRef.Name == name {
					return true
				}
				if kind == "secret" && ef.SecretRef != nil && ef.SecretRef.Name == name {
					return true
				}
			}
			for _, ev := range c.Env {
				if ev.ValueFrom == nil {
					continue
				}
				if kind == "configmap" && ev.ValueFrom.ConfigMapKeyRef != nil && ev.ValueFrom.ConfigMapKeyRef.Name == name {
					return true
				}
				if kind == "secret" && ev.ValueFrom.SecretKeyRef != nil && ev.ValueFrom.SecretKeyRef.Name == name {
					return true
				}
			}
		}
		return false
	}
	if check(pod.Spec.Containers) || check(pod.Spec.InitContainers) {
		return true
	}
	for _, v := range pod.Spec.Volumes {
		if kind == "configmap" && v.ConfigMap != nil && v.ConfigMap.Name == name {
			return true
		}
		if kind == "secret" && v.Secret != nil && v.Secret.SecretName == name {
			return true
		}
	}
	return false
}

func (e *Engine) findReferencingIngresses(namespace, svcName string) []string {
	ings, err := e.k8s.NetworkingV1().Ingresses(namespace).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	var refs []string
	for _, ing := range ings.Items {
		if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil &&
			ing.Spec.DefaultBackend.Service.Name == svcName {
			refs = append(refs, "ingress/"+ing.Name)
			continue
		}
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, p := range rule.HTTP.Paths {
				if p.Backend.Service != nil && p.Backend.Service.Name == svcName {
					refs = append(refs, "ingress/"+ing.Name)
				}
			}
		}
	}
	return refs
}

// ─────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────

func toObjectRef(gvr schema.GroupVersionResource, item *unstructured.Unstructured) ObjectRef {
	obj := ObjectRef{
		GVR:         gvr,
		Kind:        item.GetKind(),
		Name:        item.GetName(),
		Namespace:   item.GetNamespace(),
		UID:         string(item.GetUID()),
		OwnerRefs:   item.GetOwnerReferences(),
		Labels:      item.GetLabels(),
		Annotations: item.GetAnnotations(),
		Finalizers:  item.GetFinalizers(),
	}
	if ts := item.GetCreationTimestamp(); !ts.IsZero() {
		obj.CreatedAt = ts.Time
		obj.Age = time.Since(ts.Time)
	}
	if dt := item.GetDeletionTimestamp(); dt != nil {
		t := dt.Time
		obj.DeletionTimestamp = &t
	}
	if fields := item.GetManagedFields(); len(fields) > 0 {
		obj.Manager = fields[len(fields)-1].Manager
	}
	if obj.Kind == "" {
		obj.Kind = gvr.Resource
	}
	return obj
}

func hasVerb(verbs metav1.Verbs, v string) bool {
	for _, x := range verbs {
		if x == v {
			return true
		}
	}
	return false
}

func inList(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func formatAge(d time.Duration) string {
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
