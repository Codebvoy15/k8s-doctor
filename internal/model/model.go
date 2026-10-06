// Package model is k8s-doctor's cluster snapshot: a small, serializable view of
// the resources detectors need. It deliberately has no Kubernetes library
// dependencies, so the detection engine can be built, tested and replayed
// anywhere (golden tests use snapshots captured from real clusters).
//
// Secrets are recorded by name and type only. Secret data is never collected.
package model

import (
	"strings"
	"time"
)

// SchemaVersion changes whenever the Snapshot JSON shape changes incompatibly.
const SchemaVersion = "1"

// Resource kinds the collector may (or may not) have been allowed to list.
const (
	KindPod            = "Pod"
	KindNode           = "Node"
	KindSecret         = "Secret"
	KindConfigMap      = "ConfigMap"
	KindPVC            = "PersistentVolumeClaim"
	KindServiceAccount = "ServiceAccount"
	KindEvent          = "Event"
	KindDeployment     = "Deployment"
	KindStatefulSet    = "StatefulSet"
	KindDaemonSet      = "DaemonSet"
	KindHPA            = "HorizontalPodAutoscaler"
	// metrics.k8s.io (metrics-server). Usage is a single recent sample, the
	// same numbers `kubectl top` shows; memory is the working set.
	KindPodMetrics  = "PodMetrics"
	KindNodeMetrics = "NodeMetrics"
)

type Snapshot struct {
	SchemaVersion string    `json:"schema_version"`
	Cluster       string    `json:"cluster"`
	Namespace     string    `json:"namespace,omitempty"` // empty = all namespaces
	CapturedAt    time.Time `json:"captured_at"`
	// Collected records which kinds were listed successfully. Detectors must not
	// report something as missing when its kind was not collected (e.g. RBAC
	// forbade listing secrets), or they would produce false positives.
	Collected       map[string]bool  `json:"collected"`
	Errors          []CollectError   `json:"errors,omitempty"`
	Pods            []Pod            `json:"pods"`
	Nodes           []Node           `json:"nodes"`
	Secrets         []NamedObject    `json:"secrets"`
	ConfigMaps      []NamedObject    `json:"configmaps"`
	PVCs            []PVC            `json:"pvcs"`
	ServiceAccounts []ServiceAccount `json:"service_accounts"`
	Events          []Event          `json:"events"`
	// Workloads carry the controller's own view of availability (desired vs
	// ready). Older snapshots have none; detectors then fall back to pods.
	Workloads []Workload `json:"workloads,omitempty"`
	HPAs      []HPA      `json:"hpas,omitempty"`
	// Usage is what metrics-server reported at capture time. nil in older
	// snapshots or when metrics-server is missing (see Collected).
	Usage *Usage `json:"usage,omitempty"`
}

// Workload is a controller's status: the authority on whether it is available.
type Workload struct {
	Kind      string    `json:"kind"` // Deployment | StatefulSet | DaemonSet
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Desired   int32     `json:"desired"`
	Ready     int32     `json:"ready"`
	Available int32     `json:"available"`
	Updated   int32     `json:"updated"`
	CreatedAt time.Time `json:"created_at"`
	// Who owns the pod template. Fixes must go there, or they are reverted:
	// an operator rewrites its StatefulSet, Helm overwrites on the next upgrade.
	Controller  *ControllerRef `json:"controller,omitempty"`   // controller ownerReference (e.g. a Kafka CR)
	HelmRelease string         `json:"helm_release,omitempty"` // meta.helm.sh/release-name
	ManagedBy   string         `json:"managed_by,omitempty"`   // app.kubernetes.io/managed-by
}

// ControllerRef is an owner of a workload, usually an operator's custom resource.
type ControllerRef struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

// Group returns the API group ("platform.confluent.io"), or "" for the core group.
func (c ControllerRef) Group() string {
	if i := strings.Index(c.APIVersion, "/"); i > 0 {
		return c.APIVersion[:i]
	}
	return ""
}

// HPA is a HorizontalPodAutoscaler and the workload it scales.
type HPA struct {
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	TargetKind string `json:"target_kind"`
	TargetName string `json:"target_name"`
	Min        int32  `json:"min"`
	Max        int32  `json:"max"`
	Current    int32  `json:"current"`
}

// Usage is one metrics-server sample (CPU in millicores, memory working set in bytes).
type Usage struct {
	Nodes []NodeUsage `json:"nodes,omitempty"`
	Pods  []PodUsage  `json:"pods,omitempty"`
}

type NodeUsage struct {
	Name     string `json:"name"`
	CPUMilli int64  `json:"cpu_m"`
	MemBytes int64  `json:"mem_bytes"`
}

type PodUsage struct {
	Namespace  string           `json:"namespace"`
	Name       string           `json:"name"`
	Containers []ContainerUsage `json:"containers"`
}

type ContainerUsage struct {
	Name     string `json:"name"`
	CPUMilli int64  `json:"cpu_m"`
	MemBytes int64  `json:"mem_bytes"`
}

// Resources are a container's requests and limits (0 = not set). A nil
// *Resources means they were not recorded (older snapshot), which is not the
// same as "none set".
type Resources struct {
	RequestCPUMilli int64 `json:"req_cpu_m,omitempty"`
	LimitCPUMilli   int64 `json:"lim_cpu_m,omitempty"`
	RequestMemBytes int64 `json:"req_mem_bytes,omitempty"`
	LimitMemBytes   int64 `json:"lim_mem_bytes,omitempty"`
}

// Capacity is a node's allocatable CPU and memory.
type Capacity struct {
	CPUMilli int64 `json:"cpu_m"`
	MemBytes int64 `json:"mem_bytes"`
}

type CollectError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type NamedObject struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Type      string    `json:"type,omitempty"` // Secret type; never data
	CreatedAt time.Time `json:"created_at,omitempty"`
}

type PVC struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	Phase        string `json:"phase"`
	StorageClass string `json:"storage_class,omitempty"`
	VolumeName   string `json:"volume_name,omitempty"`
}

type ServiceAccount struct {
	Namespace        string   `json:"namespace"`
	Name             string   `json:"name"`
	ImagePullSecrets []string `json:"image_pull_secrets,omitempty"`
}

type OwnerRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Ref is a dependency from a pod on another namespaced object.
type Ref struct {
	Kind     string `json:"kind"` // Secret | ConfigMap | PersistentVolumeClaim | ServiceAccount
	Name     string `json:"name"`
	Via      string `json:"via"` // e.g. "volume:dynatrace-input", "env:app/DB_PASS", "envFrom:app", "imagePullSecret", "serviceAccount"
	Optional bool   `json:"optional,omitempty"`
}

type Container struct {
	Name           string     `json:"name"`
	Init           bool       `json:"init,omitempty"`
	Image          string     `json:"image"`
	ImageID        string     `json:"image_id,omitempty"` // set once the image has been pulled
	Ready          bool       `json:"ready"`
	Restarts       int32      `json:"restarts"`
	State          string     `json:"state"`            // running | waiting | terminated
	Reason         string     `json:"reason,omitempty"` // waiting/terminated reason, e.g. CrashLoopBackOff
	Message        string     `json:"message,omitempty"`
	ExitCode       int32      `json:"exit_code,omitempty"`
	LastTermReason string     `json:"last_term_reason,omitempty"` // e.g. OOMKilled, Error
	LastTermExit   int32      `json:"last_term_exit,omitempty"`
	LastTermAt     *time.Time `json:"last_term_at,omitempty"`
	Resources      *Resources `json:"resources,omitempty"` // nil = not recorded (older snapshot)
}

type Pod struct {
	Namespace      string      `json:"namespace"`
	Name           string      `json:"name"`
	Node           string      `json:"node,omitempty"`
	Phase          string      `json:"phase"`
	Reason         string      `json:"reason,omitempty"`  // status.reason, e.g. Evicted, NodeShutdown
	Message        string      `json:"message,omitempty"` // status.message, e.g. the eviction cause
	Ready          bool        `json:"ready"`
	ServiceAccount string      `json:"service_account,omitempty"`
	Owner          OwnerRef    `json:"owner,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	DeletingSince  *time.Time  `json:"deleting_since,omitempty"`
	Unschedulable  string      `json:"unschedulable,omitempty"` // PodScheduled=False message
	QOS            string      `json:"qos,omitempty"`           // Guaranteed | Burstable | BestEffort
	Containers     []Container `json:"containers"`
	Refs           []Ref       `json:"refs,omitempty"`
}

type Condition struct {
	Type    string    `json:"type"`
	Status  string    `json:"status"`
	Reason  string    `json:"reason,omitempty"`
	Message string    `json:"message,omitempty"`
	Since   time.Time `json:"since,omitempty"`
}

type Node struct {
	Name           string            `json:"name"`
	Ready          bool              `json:"ready"`
	Unschedulable  bool              `json:"unschedulable,omitempty"`
	Conditions     []Condition       `json:"conditions,omitempty"`
	Taints         []string          `json:"taints,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"` // curated subset: instance type, zone, capacity type, nodepool
	KubeletVersion string            `json:"kubelet_version,omitempty"`
	OSImage        string            `json:"os_image,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	Allocatable    *Capacity         `json:"allocatable,omitempty"`
}

type Event struct {
	Namespace string    `json:"namespace"`
	Kind      string    `json:"kind"` // involved object kind
	Name      string    `json:"name"` // involved object name
	Type      string    `json:"type"` // Warning | Normal
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Count     int32     `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// --- lookup helpers (snapshots are small enough that maps built on demand are fine) ---

// Index gives O(1) lookups over a snapshot. Build it once per detection run.
type Index struct {
	S         *Snapshot
	secrets   map[string]bool
	configs   map[string]bool
	pvcs      map[string]PVC
	sas       map[string]ServiceAccount
	nodes     map[string]Node
	podEvents map[string][]Event
	workloads map[string]Workload
	podUsage  map[string]PodUsage
	nodeUsage map[string]NodeUsage
	hpas      map[string]HPA
}

func key(ns, name string) string { return ns + "/" + name }

func NewIndex(s *Snapshot) *Index {
	ix := &Index{S: s,
		secrets: map[string]bool{}, configs: map[string]bool{},
		pvcs: map[string]PVC{}, sas: map[string]ServiceAccount{},
		nodes: map[string]Node{}, podEvents: map[string][]Event{},
		workloads: map[string]Workload{},
		podUsage:  map[string]PodUsage{}, nodeUsage: map[string]NodeUsage{},
		hpas: map[string]HPA{},
	}
	if s.Usage != nil {
		for _, u := range s.Usage.Pods {
			ix.podUsage[key(u.Namespace, u.Name)] = u
		}
		for _, u := range s.Usage.Nodes {
			ix.nodeUsage[u.Name] = u
		}
	}
	for _, h := range s.HPAs {
		ix.hpas[h.TargetKind+"|"+key(h.Namespace, h.TargetName)] = h
	}
	for _, w := range s.Workloads {
		ix.workloads[w.Kind+"|"+key(w.Namespace, w.Name)] = w
	}
	for _, o := range s.Secrets {
		ix.secrets[key(o.Namespace, o.Name)] = true
	}
	for _, o := range s.ConfigMaps {
		ix.configs[key(o.Namespace, o.Name)] = true
	}
	for _, p := range s.PVCs {
		ix.pvcs[key(p.Namespace, p.Name)] = p
	}
	for _, a := range s.ServiceAccounts {
		ix.sas[key(a.Namespace, a.Name)] = a
	}
	for _, n := range s.Nodes {
		ix.nodes[n.Name] = n
	}
	for _, e := range s.Events {
		if e.Kind == KindPod {
			ix.podEvents[key(e.Namespace, e.Name)] = append(ix.podEvents[key(e.Namespace, e.Name)], e)
		}
	}
	return ix
}

// Exists reports whether a namespaced object exists, and whether that is known.
// known=false means the kind was not collected, so absence proves nothing.
func (ix *Index) Exists(kind, ns, name string) (exists, known bool) {
	if !ix.S.Collected[kind] {
		return false, false
	}
	k := key(ns, name)
	switch kind {
	case KindSecret:
		return ix.secrets[k], true
	case KindConfigMap:
		return ix.configs[k], true
	case KindPVC:
		_, ok := ix.pvcs[k]
		return ok, true
	case KindServiceAccount:
		_, ok := ix.sas[k]
		return ok, true
	}
	return false, false
}

func (ix *Index) ServiceAccount(ns, name string) (ServiceAccount, bool) {
	a, ok := ix.sas[key(ns, name)]
	return a, ok
}

func (ix *Index) Node(name string) (Node, bool) {
	n, ok := ix.nodes[name]
	return n, ok
}

func (ix *Index) PodEvents(ns, name string) []Event { return ix.podEvents[key(ns, name)] }

// Workload returns the controller status for a pod's workload. ok=false when
// that controller kind was not collected (older snapshot, RBAC) or it was not found.
func (ix *Index) Workload(ns string, ref OwnerRef) (w Workload, ok bool) {
	if !ix.S.Collected[ref.Kind] {
		return Workload{}, false
	}
	w, ok = ix.workloads[ref.Kind+"|"+key(ns, ref.Name)]
	return w, ok
}

// PodUsage returns a pod's summed usage. ok=false when pod metrics were not
// collected or metrics-server had no sample for this pod.
func (ix *Index) PodUsage(ns, name string) (cpuMilli, memBytes int64, ok bool) {
	if !ix.S.Collected[KindPodMetrics] {
		return 0, 0, false
	}
	u, ok := ix.podUsage[key(ns, name)]
	if !ok {
		return 0, 0, false
	}
	for _, c := range u.Containers {
		cpuMilli += c.CPUMilli
		memBytes += c.MemBytes
	}
	return cpuMilli, memBytes, true
}

// NodeUsage returns a node's usage. ok=false when node metrics were not collected.
func (ix *Index) NodeUsage(name string) (NodeUsage, bool) {
	if !ix.S.Collected[KindNodeMetrics] {
		return NodeUsage{}, false
	}
	u, ok := ix.nodeUsage[name]
	return u, ok
}

// HPA returns the autoscaler targeting a workload. known=false when HPAs were
// not collected, so "no HPA" can only be claimed when known is true.
func (ix *Index) HPA(ns string, ref OwnerRef) (h HPA, found, known bool) {
	if !ix.S.Collected[KindHPA] {
		return HPA{}, false, false
	}
	h, found = ix.hpas[ref.Kind+"|"+key(ns, ref.Name)]
	return h, found, true
}

// Requests sums a pod's app-container requests. recorded=false when the
// snapshot predates resource collection for any app container.
// missingCPU/missingMem report whether any app container sets no request.
func (p Pod) Requests() (cpuMilli, memBytes int64, missingCPU, missingMem, recorded bool) {
	n := 0
	for _, c := range p.Containers {
		if c.Init {
			continue
		}
		if c.Resources == nil {
			return 0, 0, false, false, false
		}
		n++
		cpuMilli += c.Resources.RequestCPUMilli
		memBytes += c.Resources.RequestMemBytes
		missingCPU = missingCPU || c.Resources.RequestCPUMilli == 0
		missingMem = missingMem || c.Resources.RequestMemBytes == 0
	}
	return cpuMilli, memBytes, missingCPU, missingMem, n > 0
}

// Workload returns a stable workload identity for a pod: Deployment name for
// ReplicaSet-owned pods (hash suffix stripped), otherwise the owner, otherwise the pod.
func (p Pod) Workload() OwnerRef {
	switch p.Owner.Kind {
	case "ReplicaSet":
		name := p.Owner.Name
		if i := strings.LastIndex(name, "-"); i > 0 && looksLikeHash(name[i+1:]) {
			return OwnerRef{Kind: "Deployment", Name: name[:i]}
		}
		return p.Owner
	case "":
		return OwnerRef{Kind: "Pod", Name: p.Name}
	}
	return p.Owner
}

func looksLikeHash(s string) bool {
	if len(s) < 5 || len(s) > 10 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// AllImagesPulled reports whether every app container already has an image on
// its node. If so, a missing pull secret cannot hurt the running pod.
func (p Pod) AllImagesPulled() bool {
	n := 0
	for _, c := range p.Containers {
		if c.Init {
			continue
		}
		n++
		if c.ImageID == "" {
			return false
		}
	}
	return n > 0
}
