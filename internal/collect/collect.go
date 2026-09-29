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
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
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
		Events: []model.Event{},
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
	wg.Wait()

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

func convertPod(p *corev1.Pod) model.Pod {
	mp := model.Pod{
		Namespace: p.Namespace, Name: p.Name, Node: p.Spec.NodeName,
		Phase: string(p.Status.Phase), ServiceAccount: p.Spec.ServiceAccountName,
		CreatedAt: p.CreationTimestamp.Time.UTC(),
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
