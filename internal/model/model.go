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
}

func key(ns, name string) string { return ns + "/" + name }

func NewIndex(s *Snapshot) *Index {
	ix := &Index{S: s,
		secrets: map[string]bool{}, configs: map[string]bool{},
		pvcs: map[string]PVC{}, sas: map[string]ServiceAccount{},
		nodes: map[string]Node{}, podEvents: map[string][]Event{},
		workloads: map[string]Workload{},
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
