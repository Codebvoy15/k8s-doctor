// Package collect captures a model.Snapshot from a live cluster using
// read-only list calls.
//
//   - Every resource type is listed once, paginated, in parallel.
//   - Secrets and ConfigMaps are listed through the metadata API, so their data
//     never crosses the wire (only names). This also makes the calls cheaper.
//   - Each call gets its own kube context via config overrides: the user's
//     kubeconfig current-context is never modified, so parallel fleet sweeps are safe.
//   - A forbidden or failed list is recorded in Snapshot.Errors and marks that
//     kind uncollected; detectors then skip checks that depend on it.
package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

type Options struct {
	Namespace string // empty = all namespaces
	PageSize  int64  // list page size (default 500)
	MaxEvents int    // cap on warning events kept (default 20000)
}

// Config returns a rest.Config for kubeContext (empty = kubeconfig's current
// context) without modifying the kubeconfig file.
func Config(kubeconfig, kubeContext string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kube context %q: %w", kubeContext, err)
	}
	cfg.QPS, cfg.Burst = 50, 100
	cfg.Timeout = 60 * time.Second
	cfg.UserAgent = "k8s-doctor"
	return cfg, nil
}

// Contexts returns all context names in the kubeconfig (sorted) and the current one.
func Contexts(kubeconfig string) ([]string, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil {
		return nil, "", err
	}
	var names []string
	for n := range raw.Contexts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, raw.CurrentContext, nil
}

// Snapshot collects one cluster. It fails only if the API server is
// unreachable or pods cannot be listed; everything else degrades gracefully.
func Snapshot(ctx context.Context, cfg *rest.Config, cluster string, opt Options) (*model.Snapshot, error) {
	if opt.PageSize <= 0 {
		opt.PageSize = 500
	}
	if opt.MaxEvents <= 0 {
		opt.MaxEvents = 20000
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	mc, err := metadata.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	// fail fast with a clear message when the cluster is unreachable or credentials are bad
	probeCfg := rest.CopyConfig(cfg)
	probeCfg.Timeout = 15 * time.Second
	if pc, err := kubernetes.NewForConfig(probeCfg); err == nil {
		if _, err := pc.Discovery().ServerVersion(); err != nil {
			return nil, fmt.Errorf("cannot reach API server: %w", err)
		}
	}

	s := &model.Snapshot{
		SchemaVersion: model.SchemaVersion,
		Cluster:       cluster,
		Namespace:     opt.Namespace,
		CapturedAt:    time.Now().UTC(),
		Collected:     map[string]bool{},
		Pods:          []model.Pod{}, Nodes: []model.Node{}, Secrets: []model.NamedObject{},
		ConfigMaps: []model.NamedObject{}, PVCs: []model.PVC{}, ServiceAccounts: []model.ServiceAccount{},
		Events: []model.Event{}, Workloads: []model.Workload{},
	}
	ns := opt.Namespace // "" lists across all namespaces

	var mu sync.Mutex
	var wg sync.WaitGroup
	record := func(kind string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			s.Collected[kind] = false
			s.Errors = append(s.Errors, model.CollectError{Kind: kind, Message: err.Error()})
			return
		}
		s.Collected[kind] = true
	}
	run := func(kind string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record(kind, fn())
		}()
	}

	run(model.KindPod, func() error {
		opts := metav1.ListOptions{Limit: opt.PageSize}
		for {
			l, err := cs.CoreV1().Pods(ns).List(ctx, opts)
			if err != nil {
				return err
			}
			for i := range l.Items {
				s.Pods = append(s.Pods, convertPod(&l.Items[i]))
			}
			if l.Continue == "" {
				return nil
			}
			opts.Continue = l.Continue
		}
	})
	run(model.KindNode, func() error {
		opts := metav1.ListOptions{Limit: opt.PageSize}
		for {
			l, err := cs.CoreV1().Nodes().List(ctx, opts)
			if err != nil {
				return err
			}
			for i := range l.Items {
				s.Nodes = append(s.Nodes, convertNode(&l.Items[i]))
			}
			if l.Continue == "" {
				return nil
			}
			opts.Continue = l.Continue
		}
	})
	// names only, via the metadata API: secret data is never fetched
	run(model.KindSecret, func() error {
		return listNames(ctx, mc, "secrets", ns, opt.PageSize, &s.Secrets)
	})
	run(model.KindConfigMap, func() error {
		return listNames(ctx, mc, "configmaps", ns, opt.PageSize, &s.ConfigMaps)
	})
	run(model.KindPVC, func() error {
		opts := metav1.ListOptions{Limit: opt.PageSize}
		for {
			l, err := cs.CoreV1().PersistentVolumeClaims(ns).List(ctx, opts)
			if err != nil {
				return err
			}
			for _, p := range l.Items {
				sc := ""
				if p.Spec.StorageClassName != nil {
					sc = *p.Spec.StorageClassName
				}
				s.PVCs = append(s.PVCs, model.PVC{Namespace: p.Namespace, Name: p.Name, Phase: string(p.Status.Phase), StorageClass: sc, VolumeName: p.Spec.VolumeName})
			}
			if l.Continue == "" {
				return nil
			}
			opts.Continue = l.Continue
		}
	})
	run(model.KindServiceAccount, func() error {
		opts := metav1.ListOptions{Limit: opt.PageSize}
		for {
			l, err := cs.CoreV1().ServiceAccounts(ns).List(ctx, opts)
			if err != nil {
				return err
			}
			for _, a := range l.Items {
				sa := model.ServiceAccount{Namespace: a.Namespace, Name: a.Name}
				for _, r := range a.ImagePullSecrets {
					sa.ImagePullSecrets = append(sa.ImagePullSecrets, r.Name)
				}
				s.ServiceAccounts = append(s.ServiceAccounts, sa)
			}
			if l.Continue == "" {
				return nil
			}
			opts.Continue = l.Continue
		}
	})
	run(model.KindEvent, func() error {
		opts := metav1.ListOptions{Limit: opt.PageSize, FieldSelector: "type=Warning"}
		for {
			l, err := cs.CoreV1().Events(ns).List(ctx, opts)
			if err != nil {
				return err
			}
			for i := range l.Items {
				if len(s.Events) >= opt.MaxEvents {
					return nil
				}
				s.Events = append(s.Events, convertEvent(&l.Items[i]))
			}
			if l.Continue == "" {
				return nil
			}
			opts.Continue = l.Continue
		}
	})
	// Controllers are the authority on availability (desired vs ready). Each kind
	// appends to its own slice; they are merged after wg.Wait() (no shared writes).
	var deps, stss, dss []model.Workload
	run(model.KindDeployment, func() error {
		opts := metav1.ListOptions{Limit: opt.PageSize}
		for {
			l, err := cs.AppsV1().Deployments(ns).List(ctx, opts)
			if err != nil {
				return err
			}
			for i := range l.Items {
				deps = append(deps, convertDeployment(&l.Items[i]))
			}
			if l.Continue == "" {
				return nil
			}
			opts.Continue = l.Continue
		}
	})
	run(model.KindStatefulSet, func() error {
		opts := metav1.ListOptions{Limit: opt.PageSize}
		for {
			l, err := cs.AppsV1().StatefulSets(ns).List(ctx, opts)
			if err != nil {
				return err
			}
			for i := range l.Items {
				stss = append(stss, convertStatefulSet(&l.Items[i]))
			}
			if l.Continue == "" {
				return nil
			}
			opts.Continue = l.Continue
		}
	})
	run(model.KindDaemonSet, func() error {
		opts := metav1.ListOptions{Limit: opt.PageSize}
		for {
			l, err := cs.AppsV1().DaemonSets(ns).List(ctx, opts)
			if err != nil {
				return err
			}
			for i := range l.Items {
				dss = append(dss, convertDaemonSet(&l.Items[i]))
			}
			if l.Continue == "" {
				return nil
			}
			opts.Continue = l.Continue
		}
	})
	var hpas []model.HPA
	run(model.KindHPA, func() error {
		opts := metav1.ListOptions{Limit: opt.PageSize}
		for {
			l, err := cs.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, opts)
			if err != nil {
				return err
			}
			for _, h := range l.Items {
				minR := int32(1) // API default
				if h.Spec.MinReplicas != nil {
					minR = *h.Spec.MinReplicas
				}
				hpas = append(hpas, model.HPA{
					Namespace: h.Namespace, Name: h.Name,
					TargetKind: h.Spec.ScaleTargetRef.Kind, TargetName: h.Spec.ScaleTargetRef.Name,
					Min: minR, Max: h.Spec.MaxReplicas, Current: h.Status.CurrentReplicas,
				})
			}
			if l.Continue == "" {
				return nil
			}
			opts.Continue = l.Continue
		}
	})
	// Usage from metrics-server (metrics.k8s.io), read as raw JSON so no extra
	// module is needed. Missing metrics-server or RBAC degrades gracefully: the
	// resource detectors then report nothing rather than guess.
	var podUsage []model.PodUsage
	var nodeUsage []model.NodeUsage
	run(model.KindPodMetrics, func() error {
		path := "/apis/metrics.k8s.io/v1beta1/pods"
		if ns != "" {
			path = "/apis/metrics.k8s.io/v1beta1/namespaces/" + ns + "/pods"
		}
		l, err := getMetrics(ctx, cs, path)
		if err != nil {
			return err
		}
		for _, it := range l.Items {
			pu := model.PodUsage{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name}
			for _, c := range it.Containers {
				cpu, mem := parseUsage(c.Usage)
				pu.Containers = append(pu.Containers, model.ContainerUsage{Name: c.Name, CPUMilli: cpu, MemBytes: mem})
			}
			podUsage = append(podUsage, pu)
		}
		return nil
	})
	run(model.KindNodeMetrics, func() error {
		l, err := getMetrics(ctx, cs, "/apis/metrics.k8s.io/v1beta1/nodes")
		if err != nil {
			return err
		}
		for _, it := range l.Items {
			cpu, mem := parseUsage(it.Usage)
			nodeUsage = append(nodeUsage, model.NodeUsage{Name: it.Metadata.Name, CPUMilli: cpu, MemBytes: mem})
		}
		return nil
	})
	wg.Wait()
	s.Workloads = append(append(append(s.Workloads, deps...), stss...), dss...)
	s.HPAs = hpas
	if len(podUsage) > 0 || len(nodeUsage) > 0 {
		s.Usage = &model.Usage{Pods: podUsage, Nodes: nodeUsage}
	}

	if !s.Collected[model.KindPod] {
		for _, e := range s.Errors {
			if e.Kind == model.KindPod {
				return nil, fmt.Errorf("listing pods: %s", e.Message)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(s.Errors, func(i, j int) bool { return s.Errors[i].Kind < s.Errors[j].Kind })
	return s, nil
}

func listNames(ctx context.Context, mc metadata.Interface, resource, ns string, page int64, out *[]model.NamedObject) error {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: resource}
	opts := metav1.ListOptions{Limit: page}
	for {
		l, err := mc.Resource(gvr).Namespace(ns).List(ctx, opts)
		if err != nil {
			return err
		}
		for _, it := range l.Items {
			*out = append(*out, model.NamedObject{Namespace: it.Namespace, Name: it.Name, CreatedAt: it.CreationTimestamp.Time.UTC()})
		}
		if l.Continue == "" {
			return nil
		}
		opts.Continue = l.Continue
	}
}

// metricsList is the subset of metrics.k8s.io PodMetricsList/NodeMetricsList we read.
type metricsList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Usage      map[string]string `json:"usage"` // NodeMetrics
		Containers []struct {
			Name  string            `json:"name"`
			Usage map[string]string `json:"usage"`
		} `json:"containers"` // PodMetrics
	} `json:"items"`
}

func getMetrics(ctx context.Context, cs kubernetes.Interface, path string) (*metricsList, error) {
	raw, err := cs.CoreV1().RESTClient().Get().AbsPath(path).DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("metrics.k8s.io (is metrics-server installed?): %w", err)
	}
	var l metricsList
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("metrics.k8s.io: %w", err)
	}
	return &l, nil
}

// parseUsage converts metrics-server quantities ("851123n", "24979632Ki").
func parseUsage(u map[string]string) (cpuMilli, memBytes int64) {
	if q, err := resource.ParseQuantity(u["cpu"]); err == nil {
		cpuMilli = q.MilliValue()
	}
	if q, err := resource.ParseQuantity(u["memory"]); err == nil {
		memBytes = q.Value()
	}
	return cpuMilli, memBytes
}

func convertResources(r corev1.ResourceRequirements) *model.Resources {
	get := func(l corev1.ResourceList, name corev1.ResourceName, milli bool) int64 {
		q, ok := l[name]
		if !ok {
			return 0
		}
		if milli {
			return q.MilliValue()
		}
		return q.Value()
	}
	return &model.Resources{
		RequestCPUMilli: get(r.Requests, corev1.ResourceCPU, true),
		LimitCPUMilli:   get(r.Limits, corev1.ResourceCPU, true),
		RequestMemBytes: get(r.Requests, corev1.ResourceMemory, false),
		LimitMemBytes:   get(r.Limits, corev1.ResourceMemory, false),
	}
}

// workloadOwner returns who owns a workload's pod template: an operator's
// custom resource (controller ownerReference), a Helm release, or neither.
func workloadOwner(m metav1.ObjectMeta) (*model.ControllerRef, string, string) {
	var c *model.ControllerRef
	for _, o := range m.OwnerReferences {
		if o.Controller != nil && *o.Controller {
			c = &model.ControllerRef{APIVersion: o.APIVersion, Kind: o.Kind, Name: o.Name}
		}
	}
	return c, m.Annotations["meta.helm.sh/release-name"], m.Labels["app.kubernetes.io/managed-by"]
}

func convertPod(p *corev1.Pod) model.Pod {
	mp := model.Pod{
		Namespace: p.Namespace, Name: p.Name, Node: p.Spec.NodeName,
		Phase: string(p.Status.Phase), Reason: p.Status.Reason, Message: p.Status.Message,
		ServiceAccount: p.Spec.ServiceAccountName,
		CreatedAt:      p.CreationTimestamp.Time.UTC(),
		QOS:            string(p.Status.QOSClass),
	}
	if p.DeletionTimestamp != nil {
		t := p.DeletionTimestamp.Time.UTC()
		mp.DeletingSince = &t
	}
	for _, o := range p.OwnerReferences {
		if o.Controller != nil && *o.Controller {
			mp.Owner = model.OwnerRef{Kind: o.Kind, Name: o.Name}
		}
	}
	if mp.Owner.Kind == "" && len(p.OwnerReferences) > 0 {
		mp.Owner = model.OwnerRef{Kind: p.OwnerReferences[0].Kind, Name: p.OwnerReferences[0].Name}
	}
	for _, c := range p.Status.Conditions {
		switch c.Type {
		case corev1.PodReady:
			mp.Ready = c.Status == corev1.ConditionTrue
		case corev1.PodScheduled:
			if c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
				mp.Unschedulable = c.Message
			}
		}
	}
	for _, cs := range p.Status.InitContainerStatuses {
		mp.Containers = append(mp.Containers, convertStatus(cs, true))
	}
	for _, cs := range p.Status.ContainerStatuses {
		mp.Containers = append(mp.Containers, convertStatus(cs, false))
	}
	if len(p.Status.ContainerStatuses) == 0 { // not started yet: record spec so detectors see the images
		for _, c := range p.Spec.Containers {
			mp.Containers = append(mp.Containers, model.Container{Name: c.Name, Image: c.Image, State: "waiting"})
		}
	}
	// requests/limits come from the spec; status entries are matched by name
	specRes := map[string]*model.Resources{}
	for _, c := range p.Spec.InitContainers {
		specRes["init/"+c.Name] = convertResources(c.Resources)
	}
	for _, c := range p.Spec.Containers {
		specRes[c.Name] = convertResources(c.Resources)
	}
	for i := range mp.Containers {
		k := mp.Containers[i].Name
		if mp.Containers[i].Init {
			k = "init/" + k
		}
		mp.Containers[i].Resources = specRes[k]
	}
	mp.Refs = podRefs(&p.Spec)
	return mp
}

func convertStatus(cs corev1.ContainerStatus, init bool) model.Container {
	c := model.Container{Name: cs.Name, Init: init, Image: cs.Image, ImageID: cs.ImageID, Ready: cs.Ready, Restarts: cs.RestartCount}
	switch {
	case cs.State.Waiting != nil:
		c.State, c.Reason, c.Message = "waiting", cs.State.Waiting.Reason, cs.State.Waiting.Message
	case cs.State.Running != nil:
		c.State = "running"
	case cs.State.Terminated != nil:
		t := cs.State.Terminated
		c.State, c.Reason, c.Message, c.ExitCode = "terminated", t.Reason, t.Message, t.ExitCode
	}
	if t := cs.LastTerminationState.Terminated; t != nil {
		c.LastTermReason, c.LastTermExit = t.Reason, t.ExitCode
		if !t.FinishedAt.IsZero() {
			at := t.FinishedAt.Time.UTC()
			c.LastTermAt = &at
		}
	}
	return c
}

func optional(b *bool) bool { return b != nil && *b }

// podRefs lists every named object the pod needs to start.
func podRefs(spec *corev1.PodSpec) []model.Ref {
	var refs []model.Ref
	if spec.ServiceAccountName != "" {
		refs = append(refs, model.Ref{Kind: model.KindServiceAccount, Name: spec.ServiceAccountName, Via: "serviceAccount"})
	}
	for _, s := range spec.ImagePullSecrets {
		if s.Name != "" {
			refs = append(refs, model.Ref{Kind: model.KindSecret, Name: s.Name, Via: "imagePullSecret"})
		}
	}
	for _, v := range spec.Volumes {
		via := "volume:" + v.Name
		switch {
		case v.Secret != nil:
			refs = append(refs, model.Ref{Kind: model.KindSecret, Name: v.Secret.SecretName, Via: via, Optional: optional(v.Secret.Optional)})
		case v.ConfigMap != nil:
			refs = append(refs, model.Ref{Kind: model.KindConfigMap, Name: v.ConfigMap.Name, Via: via, Optional: optional(v.ConfigMap.Optional)})
		case v.PersistentVolumeClaim != nil:
			refs = append(refs, model.Ref{Kind: model.KindPVC, Name: v.PersistentVolumeClaim.ClaimName, Via: via})
		case v.Projected != nil:
			for _, src := range v.Projected.Sources {
				if src.Secret != nil {
					refs = append(refs, model.Ref{Kind: model.KindSecret, Name: src.Secret.Name, Via: via, Optional: optional(src.Secret.Optional)})
				}
				if src.ConfigMap != nil {
					refs = append(refs, model.Ref{Kind: model.KindConfigMap, Name: src.ConfigMap.Name, Via: via, Optional: optional(src.ConfigMap.Optional)})
				}
			}
		}
	}
	containers := append(append([]corev1.Container(nil), spec.InitContainers...), spec.Containers...)
	for _, c := range containers {
		for _, e := range c.Env {
			if e.ValueFrom == nil {
				continue
			}
			via := "env:" + c.Name + "/" + e.Name
			if r := e.ValueFrom.SecretKeyRef; r != nil {
				refs = append(refs, model.Ref{Kind: model.KindSecret, Name: r.Name, Via: via, Optional: optional(r.Optional)})
			}
			if r := e.ValueFrom.ConfigMapKeyRef; r != nil {
				refs = append(refs, model.Ref{Kind: model.KindConfigMap, Name: r.Name, Via: via, Optional: optional(r.Optional)})
			}
		}
		for _, ef := range c.EnvFrom {
			via := "envFrom:" + c.Name
			if ef.SecretRef != nil {
				refs = append(refs, model.Ref{Kind: model.KindSecret, Name: ef.SecretRef.Name, Via: via, Optional: optional(ef.SecretRef.Optional)})
			}
			if ef.ConfigMapRef != nil {
				refs = append(refs, model.Ref{Kind: model.KindConfigMap, Name: ef.ConfigMapRef.Name, Via: via, Optional: optional(ef.ConfigMapRef.Optional)})
			}
		}
	}
	return refs
}

// nodeLabels is the curated subset kept in snapshots (full label sets are large and noisy).
var nodeLabels = []string{
	"node.kubernetes.io/instance-type", "topology.kubernetes.io/zone",
	"karpenter.sh/capacity-type", "karpenter.sh/nodepool", "eks.amazonaws.com/nodegroup",
	"kubernetes.io/os", "kubernetes.io/arch",
}

func convertNode(n *corev1.Node) model.Node {
	mn := model.Node{
		Name: n.Name, Unschedulable: n.Spec.Unschedulable,
		KubeletVersion: n.Status.NodeInfo.KubeletVersion, OSImage: n.Status.NodeInfo.OSImage,
		CreatedAt: n.CreationTimestamp.Time.UTC(), Labels: map[string]string{},
	}
	for _, c := range n.Status.Conditions {
		mn.Conditions = append(mn.Conditions, model.Condition{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message,
			Since: c.LastTransitionTime.Time.UTC(),
		})
		if c.Type == corev1.NodeReady {
			mn.Ready = c.Status == corev1.ConditionTrue
		}
	}
	for _, t := range n.Spec.Taints {
		mn.Taints = append(mn.Taints, strings.TrimSuffix(t.Key+"="+t.Value, "=")+":"+string(t.Effect))
	}
	for _, k := range nodeLabels {
		if v, ok := n.Labels[k]; ok {
			mn.Labels[k] = v
		}
	}
	cpu, okCPU := n.Status.Allocatable[corev1.ResourceCPU]
	mem, okMem := n.Status.Allocatable[corev1.ResourceMemory]
	if okCPU && okMem {
		mn.Allocatable = &model.Capacity{CPUMilli: cpu.MilliValue(), MemBytes: mem.Value()}
	}
	return mn
}

func convertEvent(e *corev1.Event) model.Event {
	first, last, count := e.FirstTimestamp.Time, e.LastTimestamp.Time, e.Count
	// events created through events.k8s.io/v1 leave the legacy timestamps empty
	if first.IsZero() {
		first = e.EventTime.Time
	}
	if e.Series != nil {
		if last.IsZero() {
			last = e.Series.LastObservedTime.Time
		}
		if count == 0 {
			count = e.Series.Count
		}
	}
	if last.IsZero() {
		last = first
	}
	if count == 0 {
		count = 1
	}
	ns := e.InvolvedObject.Namespace
	if ns == "" {
		ns = e.Namespace
	}
	return model.Event{
		Namespace: ns, Kind: e.InvolvedObject.Kind, Name: e.InvolvedObject.Name,
		Type: e.Type, Reason: e.Reason, Message: e.Message, Count: count,
		FirstSeen: first.UTC(), LastSeen: last.UTC(),
	}
}

func replicas(r *int32) int32 {
	if r == nil {
		return 1 // API default
	}
	return *r
}

func convertDeployment(d *appsv1.Deployment) model.Workload {
	w := model.Workload{
		Kind: model.KindDeployment, Namespace: d.Namespace, Name: d.Name,
		Desired: replicas(d.Spec.Replicas), Ready: d.Status.ReadyReplicas,
		Available: d.Status.AvailableReplicas, Updated: d.Status.UpdatedReplicas,
		CreatedAt: d.CreationTimestamp.Time.UTC(),
	}
	w.Controller, w.HelmRelease, w.ManagedBy = workloadOwner(d.ObjectMeta)
	return w
}

func convertStatefulSet(st *appsv1.StatefulSet) model.Workload {
	w := model.Workload{
		Kind: model.KindStatefulSet, Namespace: st.Namespace, Name: st.Name,
		Desired: replicas(st.Spec.Replicas), Ready: st.Status.ReadyReplicas,
		Available: st.Status.AvailableReplicas, Updated: st.Status.UpdatedReplicas,
		CreatedAt: st.CreationTimestamp.Time.UTC(),
	}
	w.Controller, w.HelmRelease, w.ManagedBy = workloadOwner(st.ObjectMeta)
	return w
}

func convertDaemonSet(ds *appsv1.DaemonSet) model.Workload {
	w := model.Workload{
		Kind: model.KindDaemonSet, Namespace: ds.Namespace, Name: ds.Name,
		Desired: ds.Status.DesiredNumberScheduled, Ready: ds.Status.NumberReady,
		Available: ds.Status.NumberAvailable, Updated: ds.Status.UpdatedNumberScheduled,
		CreatedAt: ds.CreationTimestamp.Time.UTC(),
	}
	w.Controller, w.HelmRelease, w.ManagedBy = workloadOwner(ds.ObjectMeta)
	return w
}
