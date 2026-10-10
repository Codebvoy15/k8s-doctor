// Package upgrade answers "is this cluster done upgrading?" after an EKS
// control-plane upgrade: control plane version, node kubelet skew (and why a
// node has not rolled), and the core add-ons (kube-proxy, CoreDNS, VPC CNI)
// against the latest EKS add-on versions for the target minor.
//
// It is split like the scan engine: the collector (collect.go, client-go)
// gathers plain Facts; Analyze is a pure function over Facts, so every
// reasoning path is unit-tested from fixtures and can be replayed offline
// from a saved facts file.
package upgrade

import "time"

// FactsSchemaVersion changes whenever the Facts JSON shape changes incompatibly.
const FactsSchemaVersion = "1"

// Collected/Errors keys.
const (
	KindVersion   = "version"
	KindNodes     = "nodes"
	KindAddons    = "addons"
	KindPods      = "kube-system-pods"
	KindPDBs      = "pdbs"
	KindKarpenter = "karpenter"
)

// Facts is everything the post-upgrade checks need from one cluster.
type Facts struct {
	SchemaVersion string    `json:"schema_version"`
	Cluster       string    `json:"cluster"`
	CapturedAt    time.Time `json:"captured_at"`
	ServerVersion string    `json:"server_version"` // kube-apiserver gitVersion, e.g. v1.36.2-eks-1a2b3c

	// Collected records which kinds were read successfully. A check whose
	// input was not collected (RBAC, timeout) is reported as SKIP, never as
	// a pass or a false failure.
	Collected map[string]bool `json:"collected"`
	Errors    []string        `json:"errors,omitempty"`

	Nodes          []Node     `json:"nodes"`
	Addons         []Addon    `json:"addons"`
	KubeSystemPods []Pod      `json:"kube_system_pods,omitempty"`
	PDBs           []PDB      `json:"pdbs,omitempty"`      // only PDBs that currently allow 0 disruptions
	Karpenter      *Karpenter `json:"karpenter,omitempty"` // nil = Karpenter CRDs not installed
}

// Node owner kinds.
const (
	OwnerMNG         = "managed-nodegroup"
	OwnerKarpenter   = "karpenter"
	OwnerFargate     = "fargate"
	OwnerAutoMode    = "auto-mode"
	OwnerSelfManaged = "self-managed"
)

type Node struct {
	Name          string    `json:"name"`
	Kubelet       string    `json:"kubelet"` // v1.34.6-eks-...
	OSImage       string    `json:"os_image,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	Ready         bool      `json:"ready"`
	Unschedulable bool      `json:"unschedulable,omitempty"`

	Owner     string `json:"owner"`                // one of the Owner* constants
	OwnerName string `json:"owner_name,omitempty"` // nodegroup or nodepool name
	NodeClass string `json:"node_class,omitempty"` // Karpenter EC2NodeClass
	Image     string `json:"image,omitempty"`      // MNG AMI from the eks.amazonaws.com/nodegroup-image label

	DoNotDisrupt     bool     `json:"do_not_disrupt,omitempty"`      // node annotation karpenter.sh/do-not-disrupt=true
	DoNotDisruptPods []string `json:"do_not_disrupt_pods,omitempty"` // ns/name; collected for lagging Karpenter nodes only
}

// Add-on names.
const (
	AddonKubeProxy = "kube-proxy"
	AddonCoreDNS   = "coredns"
	AddonVPCCNI    = "vpc-cni"
)

type Addon struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`   // DaemonSet | Deployment
	Object       string `json:"object"` // kube-system/aws-node
	Found        bool   `json:"found"`
	ManagedByEKS bool   `json:"managed_by_eks"` // a managedFields entry from manager "eks"

	Containers []AddonContainer `json:"containers,omitempty"`

	Desired     int32 `json:"desired"`
	Updated     int32 `json:"updated"`
	Ready       int32 `json:"ready"`
	Unavailable int32 `json:"unavailable,omitempty"`
	// GenerationPending: the controller has not observed the latest spec yet.
	GenerationPending bool `json:"generation_pending,omitempty"`
}

type AddonContainer struct {
	Name  string `json:"name"`
	Init  bool   `json:"init,omitempty"`
	Image string `json:"image"`
}

type Pod struct {
	Name     string `json:"name"`
	Node     string `json:"node,omitempty"`
	Phase    string `json:"phase"`
	Ready    bool   `json:"ready"`
	Reason   string `json:"reason,omitempty"` // worst container waiting/terminated reason
	Restarts int32  `json:"restarts"`
}

type PDB struct {
	Namespace      string `json:"namespace"`
	Name           string `json:"name"`
	Expected       int32  `json:"expected"`
	CurrentHealthy int32  `json:"current_healthy"`
	DesiredHealthy int32  `json:"desired_healthy"`
}

type Karpenter struct {
	APIVersion      string      `json:"api_version"`                // v1 | v1beta1
	ControllerImage string      `json:"controller_image,omitempty"` // empty = controller not found in-cluster
	NodeClaims      []NodeClaim `json:"node_claims"`
	NodeClasses     []NodeClass `json:"node_classes"`
	NodePools       []NodePool  `json:"node_pools"`
}

type NodeClaim struct {
	Name     string `json:"name"`
	NodeName string `json:"node_name"`
	NodePool string `json:"node_pool,omitempty"`
	Drifted  string `json:"drifted,omitempty"` // condition status: True | False | Unknown | "" (absent)
	ImageID  string `json:"image_id,omitempty"`
}

type NodeClass struct {
	Name             string        `json:"name"`
	AMISelectorTerms string        `json:"ami_selector_terms"` // compact JSON, shown as evidence
	AMIs             []ResolvedAMI `json:"amis,omitempty"`     // status.amis: what the selector resolves to now
}

type ResolvedAMI struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type NodePool struct {
	Name                string   `json:"name"`
	Budgets             []Budget `json:"budgets,omitempty"`
	ConsolidationPolicy string   `json:"consolidation_policy,omitempty"`
	ExpireAfter         string   `json:"expire_after,omitempty"`
}

type Budget struct {
	Nodes    string   `json:"nodes"`              // "0", "10%", "3"
	Schedule string   `json:"schedule,omitempty"` // cron; empty = always active
	Duration string   `json:"duration,omitempty"`
	Reasons  []string `json:"reasons,omitempty"` // empty = all disruption reasons
}
