package upgrade

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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Collect reads the facts for one cluster with read-only calls. It fails only
// when the API server cannot be reached; everything else degrades into
// Facts.Errors and an uncollected kind (reported as SKIP).
func Collect(ctx context.Context, cfg *rest.Config, cluster string) (*Facts, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	f := &Facts{SchemaVersion: FactsSchemaVersion, Cluster: cluster, CapturedAt: time.Now().UTC(), Collected: map[string]bool{}}

	ver, err := cs.Discovery().ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("API server unreachable: %w", err)
	}
	f.ServerVersion = ver.GitVersion
	f.Collected[KindVersion] = true

	var mu sync.Mutex
	fail := func(kind string, err error) {
		mu.Lock()
		f.Errors = append(f.Errors, kind+": "+err.Error())
		mu.Unlock()
	}
	var wg sync.WaitGroup
	run := func(kind string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				fail(kind, err)
				return
			}
			mu.Lock()
			f.Collected[kind] = true
			mu.Unlock()
		}()
	}

	run(KindNodes, func() error {
		nodes, err := listNodes(ctx, cs)
		if err == nil {
			f.Nodes = nodes
		}
		return err
	})
	run(KindAddons, func() error {
		ads, err := getAddons(ctx, cs)
		if err == nil {
			f.Addons = ads
		}
		return err
	})
	run(KindPods, func() error {
		pods, err := cs.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for i := range pods.Items {
			f.KubeSystemPods = append(f.KubeSystemPods, convertPod(&pods.Items[i]))
		}
		return nil
	})
	run(KindPDBs, func() error {
		l, err := cs.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, p := range l.Items {
			if p.Status.DisruptionsAllowed == 0 && p.Status.ExpectedPods > 0 {
				f.PDBs = append(f.PDBs, PDB{Namespace: p.Namespace, Name: p.Name, Expected: p.Status.ExpectedPods,
					CurrentHealthy: p.Status.CurrentHealthy, DesiredHealthy: p.Status.DesiredHealthy})
			}
		}
		sort.Slice(f.PDBs, func(i, j int) bool {
			return f.PDBs[i].Namespace+"/"+f.PDBs[i].Name < f.PDBs[j].Namespace+"/"+f.PDBs[j].Name
		})
		return nil
	})
	var karp *Karpenter
	run(KindKarpenter, func() error {
		k, err := getKarpenter(ctx, cs, dyn)
		karp = k
		return err
	})
	wg.Wait()
	f.Karpenter = karp

	// do-not-disrupt pods, only for Karpenter nodes behind the control plane:
	// one field-selected list per lagging node, never a cluster-wide pod list.
	if cp := minorOf(f.ServerVersion); cp >= 0 && f.Karpenter != nil {
		for i := range f.Nodes {
			n := &f.Nodes[i]
			if n.Owner != OwnerKarpenter || minorOf(n.Kubelet) == cp {
				continue
			}
			pods, err := cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + n.Name})
			if err != nil {
				fail(KindKarpenter, fmt.Errorf("pods on %s: %w", n.Name, err))
				continue
			}
			for _, p := range pods.Items {
				if p.Annotations["karpenter.sh/do-not-disrupt"] == "true" {
					n.DoNotDisruptPods = append(n.DoNotDisruptPods, p.Namespace+"/"+p.Name)
				}
			}
		}
	}
	sort.Strings(f.Errors)
	return f, nil
}

func listNodes(ctx context.Context, cs kubernetes.Interface) ([]Node, error) {
	var out []Node
	opts := metav1.ListOptions{Limit: 500}
	for {
		l, err := cs.CoreV1().Nodes().List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for i := range l.Items {
			out = append(out, convertNode(&l.Items[i]))
		}
		if l.Continue == "" {
			break
		}
		opts.Continue = l.Continue
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func convertNode(n *corev1.Node) Node {
	l := n.Labels
	out := Node{
		Name:          n.Name,
		Kubelet:       n.Status.NodeInfo.KubeletVersion,
		OSImage:       n.Status.NodeInfo.OSImage,
		CreatedAt:     n.CreationTimestamp.Time.UTC(),
		Unschedulable: n.Spec.Unschedulable,
		NodeClass:     l["karpenter.k8s.aws/ec2nodeclass"],
		Image:         l["eks.amazonaws.com/nodegroup-image"],
		DoNotDisrupt:  n.Annotations["karpenter.sh/do-not-disrupt"] == "true",
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			out.Ready = c.Status == corev1.ConditionTrue
		}
	}
	switch {
	case l["eks.amazonaws.com/nodegroup"] != "":
		out.Owner, out.OwnerName = OwnerMNG, l["eks.amazonaws.com/nodegroup"]
	case l["eks.amazonaws.com/compute-type"] == "fargate":
		out.Owner = OwnerFargate
	case l["eks.amazonaws.com/compute-type"] == "auto":
		out.Owner, out.OwnerName = OwnerAutoMode, l["karpenter.sh/nodepool"]
	case l["karpenter.sh/nodepool"] != "":
		out.Owner, out.OwnerName = OwnerKarpenter, l["karpenter.sh/nodepool"]
	case l["karpenter.sh/provisioner-name"] != "": // Karpenter v1alpha5
		out.Owner, out.OwnerName = OwnerKarpenter, l["karpenter.sh/provisioner-name"]
	default:
		out.Owner = OwnerSelfManaged
	}
	return out
}

func getAddons(ctx context.Context, cs kubernetes.Interface) ([]Addon, error) {
	type spec struct{ name, kind, obj string }
	specs := []spec{
		{AddonKubeProxy, "DaemonSet", "kube-proxy"},
		{AddonCoreDNS, "Deployment", "coredns"},
		{AddonVPCCNI, "DaemonSet", "aws-node"},
	}
	var out []Addon
	for _, s := range specs {
		ad := Addon{Name: s.name, Kind: s.kind, Object: "kube-system/" + s.obj}
		var err error
		switch s.kind {
		case "DaemonSet":
			var ds *appsv1.DaemonSet
			ds, err = cs.AppsV1().DaemonSets("kube-system").Get(ctx, s.obj, metav1.GetOptions{})
			if err == nil {
				fillDaemonSet(&ad, ds)
			}
		case "Deployment":
			var d *appsv1.Deployment
			d, err = cs.AppsV1().Deployments("kube-system").Get(ctx, s.obj, metav1.GetOptions{})
			if err == nil {
				fillDeployment(&ad, d)
			}
		}
		switch {
		case err == nil:
			ad.Found = true
		case apierrors.IsNotFound(err):
			// reported by the check
		default:
			return nil, fmt.Errorf("%s: %w", ad.Object, err)
		}
		out = append(out, ad)
	}
	return out, nil
}

func managedByEKS(m metav1.ObjectMeta) bool {
	for _, mf := range m.ManagedFields {
		if mf.Manager == "eks" {
			return true
		}
	}
	return false
}

func containers(spec corev1.PodSpec) []AddonContainer {
	var out []AddonContainer
	for _, c := range spec.InitContainers {
		out = append(out, AddonContainer{Name: c.Name, Init: true, Image: c.Image})
	}
	for _, c := range spec.Containers {
		out = append(out, AddonContainer{Name: c.Name, Image: c.Image})
	}
	return out
}

func fillDaemonSet(ad *Addon, ds *appsv1.DaemonSet) {
	ad.ManagedByEKS = managedByEKS(ds.ObjectMeta)
	ad.Containers = containers(ds.Spec.Template.Spec)
	ad.Desired = ds.Status.DesiredNumberScheduled
	ad.Updated = ds.Status.UpdatedNumberScheduled
	ad.Ready = ds.Status.NumberReady
	ad.Unavailable = ds.Status.NumberUnavailable
	ad.GenerationPending = ds.Status.ObservedGeneration < ds.Generation
}

func fillDeployment(ad *Addon, d *appsv1.Deployment) {
	ad.ManagedByEKS = managedByEKS(d.ObjectMeta)
	ad.Containers = containers(d.Spec.Template.Spec)
	ad.Desired = 1
	if d.Spec.Replicas != nil {
		ad.Desired = *d.Spec.Replicas
	}
	ad.Updated = d.Status.UpdatedReplicas
	ad.Ready = d.Status.ReadyReplicas
	ad.Unavailable = d.Status.UnavailableReplicas
	ad.GenerationPending = d.Status.ObservedGeneration < d.Generation
}

func convertPod(p *corev1.Pod) Pod {
	out := Pod{Name: p.Name, Node: p.Spec.NodeName, Phase: string(p.Status.Phase), Reason: p.Status.Reason}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			out.Ready = c.Status == corev1.ConditionTrue
		}
	}
	statuses := append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
	for _, cs := range statuses {
		out.Restarts += cs.RestartCount
		switch {
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && cs.State.Waiting.Reason != "PodInitializing":
			out.Reason = cs.State.Waiting.Reason
		case cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 && out.Reason == "":
			out.Reason = cs.State.Terminated.Reason
		}
	}
	return out
}

// ---------------------------------------------------------------- Karpenter

var (
	gvrNodeClaimV1      = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}
	gvrNodePoolV1       = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	gvrEC2NodeClassV1   = schema.GroupVersionResource{Group: "karpenter.k8s.aws", Version: "v1", Resource: "ec2nodeclasses"}
	gvrNodeClaimBeta    = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1beta1", Resource: "nodeclaims"}
	gvrNodePoolBeta     = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1beta1", Resource: "nodepools"}
	gvrEC2NodeClassBeta = schema.GroupVersionResource{Group: "karpenter.k8s.aws", Version: "v1beta1", Resource: "ec2nodeclasses"}
)

// getKarpenter returns nil, nil when Karpenter's CRDs are not installed.
func getKarpenter(ctx context.Context, cs kubernetes.Interface, dyn dynamic.Interface) (*Karpenter, error) {
	k := &Karpenter{APIVersion: "v1"}
	claims, err := dyn.Resource(gvrNodeClaimV1).List(ctx, metav1.ListOptions{})
	gNP, gNC := gvrNodePoolV1, gvrEC2NodeClassV1
	if apierrors.IsNotFound(err) {
		k.APIVersion = "v1beta1"
		claims, err = dyn.Resource(gvrNodeClaimBeta).List(ctx, metav1.ListOptions{})
		gNP, gNC = gvrNodePoolBeta, gvrEC2NodeClassBeta
	}
	if apierrors.IsNotFound(err) {
		return nil, nil // no Karpenter
	}
	if err != nil {
		return nil, fmt.Errorf("nodeclaims: %w", err)
	}
	for _, u := range claims.Items {
		k.NodeClaims = append(k.NodeClaims, convertNodeClaim(&u))
	}
	if l, err := dyn.Resource(gNP).List(ctx, metav1.ListOptions{}); err == nil {
		for _, u := range l.Items {
			k.NodePools = append(k.NodePools, convertNodePool(&u))
		}
	} else {
		return k, fmt.Errorf("nodepools: %w", err)
	}
	if l, err := dyn.Resource(gNC).List(ctx, metav1.ListOptions{}); err == nil {
		for _, u := range l.Items {
			k.NodeClasses = append(k.NodeClasses, convertNodeClass(&u))
		}
	} else {
		return k, fmt.Errorf("ec2nodeclasses: %w", err)
	}
	// Controller version is evidence only; not finding it is not an error.
	if d, err := cs.AppsV1().Deployments("").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=karpenter"}); err == nil {
		for _, dep := range d.Items {
			for _, c := range dep.Spec.Template.Spec.Containers {
				if c.Name == "controller" || k.ControllerImage == "" {
					k.ControllerImage = c.Image
				}
			}
		}
	}
	return k, nil
}

func convertNodeClaim(u *unstructured.Unstructured) NodeClaim {
	nc := NodeClaim{Name: u.GetName(), NodePool: u.GetLabels()["karpenter.sh/nodepool"]}
	nc.NodeName, _, _ = unstructured.NestedString(u.Object, "status", "nodeName")
	nc.ImageID, _, _ = unstructured.NestedString(u.Object, "status", "imageID")
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == "Drifted" {
			nc.Drifted, _ = m["status"].(string)
		}
	}
	return nc
}

func convertNodePool(u *unstructured.Unstructured) NodePool {
	np := NodePool{Name: u.GetName()}
	np.ConsolidationPolicy, _, _ = unstructured.NestedString(u.Object, "spec", "disruption", "consolidationPolicy")
	np.ExpireAfter, _, _ = unstructured.NestedString(u.Object, "spec", "template", "spec", "expireAfter") // v1
	if np.ExpireAfter == "" {
		np.ExpireAfter, _, _ = unstructured.NestedString(u.Object, "spec", "disruption", "expireAfter") // v1beta1
	}
	budgets, _, _ := unstructured.NestedSlice(u.Object, "spec", "disruption", "budgets")
	for _, b := range budgets {
		m, ok := b.(map[string]interface{})
		if !ok {
			continue
		}
		bd := Budget{}
		bd.Nodes, _ = m["nodes"].(string)
		bd.Schedule, _ = m["schedule"].(string)
		bd.Duration, _ = m["duration"].(string)
		if rs, ok := m["reasons"].([]interface{}); ok {
			for _, r := range rs {
				if s, ok := r.(string); ok {
					bd.Reasons = append(bd.Reasons, s)
				}
			}
		}
		np.Budgets = append(np.Budgets, bd)
	}
	return np
}

func convertNodeClass(u *unstructured.Unstructured) NodeClass {
	nc := NodeClass{Name: u.GetName()}
	if terms, ok, _ := unstructured.NestedSlice(u.Object, "spec", "amiSelectorTerms"); ok {
		b, _ := json.Marshal(terms)
		nc.AMISelectorTerms = string(b)
	} else if fam, _, _ := unstructured.NestedString(u.Object, "spec", "amiFamily"); fam != "" { // v1beta1 without terms
		nc.AMISelectorTerms = fmt.Sprintf(`{"amiFamily":%q}`, fam)
	}
	amis, _, _ := unstructured.NestedSlice(u.Object, "status", "amis")
	for _, a := range amis {
		m, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		name, _ := m["name"].(string)
		nc.AMIs = append(nc.AMIs, ResolvedAMI{ID: id, Name: name})
	}
	return nc
}

// ---------------------------------------------------------------- context names

// ResolveContexts maps what the user typed (a context name, or a bare EKS
// cluster name such as "rd-bcg-sbx-01") to kubeconfig contexts. A bare name
// matches a context that equals it or ends in "/<name>" (EKS ARNs),
// "@<name>" or "@<name>.<region>..." (eksctl). Ambiguous or unknown names are errors.
func ResolveContexts(names, contexts []string) ([]string, error) {
	var out []string
	var problems []string
	seen := map[string]bool{}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		var hits []string
		for _, c := range contexts {
			if c == name {
				hits = []string{c}
				break
			}
			if strings.HasSuffix(c, "/"+name) || strings.HasSuffix(c, "@"+name) || strings.Contains(c, "@"+name+".") {
				hits = append(hits, c)
			}
		}
		switch len(hits) {
		case 0:
			problems = append(problems, fmt.Sprintf("%q: no matching kube context", name))
		case 1:
			if !seen[hits[0]] {
				seen[hits[0]] = true
				out = append(out, hits[0])
			}
		default:
			problems = append(problems, fmt.Sprintf("%q is ambiguous: %s", name, strings.Join(hits, ", ")))
		}
	}
	if len(problems) > 0 {
		return out, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return out, nil
}

// ShortName is how a context is shown: the EKS cluster name for ARNs.
func ShortName(ctx string) string {
	if strings.HasPrefix(ctx, "arn:aws") {
		if i := strings.LastIndex(ctx, "/"); i >= 0 {
			return ctx[i+1:]
		}
	}
	return ctx
}
