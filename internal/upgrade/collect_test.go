package upgrade

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

// fakeAPI serves just enough of the Kubernetes API for Collect, with the JSON
// shapes a real EKS cluster returns (including Karpenter v1 CRDs).
func fakeAPI(t *testing.T, karpenter bool) *httptest.Server {
	t.Helper()
	list := func(kind, apiVersion string, items ...string) string {
		return `{"kind":"` + kind + `","apiVersion":"` + apiVersion + `","metadata":{},"items":[` + strings.Join(items, ",") + `]}`
	}
	routes := map[string]string{
		"/version": `{"major":"1","minor":"36","gitVersion":"v1.36.2-eks-1a2b3c"}`,
		"/api/v1/nodes": list("NodeList", "v1",
			`{"metadata":{"name":"ip-1","labels":{"eks.amazonaws.com/nodegroup":"ng-app","eks.amazonaws.com/nodegroup-image":"ami-0old"}},
			  "spec":{"unschedulable":true},
			  "status":{"nodeInfo":{"kubeletVersion":"v1.34.6-eks-1","osImage":"Amazon Linux 2023"},"conditions":[{"type":"Ready","status":"True"}]}}`,
			`{"metadata":{"name":"ip-2","labels":{"karpenter.sh/nodepool":"default","karpenter.k8s.aws/ec2nodeclass":"default"},"annotations":{"karpenter.sh/do-not-disrupt":"true"}},
			  "status":{"nodeInfo":{"kubeletVersion":"v1.35.1-eks-1"},"conditions":[{"type":"Ready","status":"False"}]}}`,
			`{"metadata":{"name":"ip-3","labels":{"eks.amazonaws.com/compute-type":"fargate"}},
			  "status":{"nodeInfo":{"kubeletVersion":"v1.36.2-eks-1"},"conditions":[{"type":"Ready","status":"True"}]}}`),
		"/apis/apps/v1/namespaces/kube-system/daemonsets/kube-proxy": `{"kind":"DaemonSet","apiVersion":"apps/v1",
			"metadata":{"name":"kube-proxy","namespace":"kube-system","generation":3,"managedFields":[{"manager":"eks","operation":"Apply"}]},
			"spec":{"template":{"spec":{"containers":[{"name":"kube-proxy","image":"602401143452.dkr.ecr.us-east-1.amazonaws.com/eks/kube-proxy:v1.35.3-eksbuild.25"}]}}},
			"status":{"observedGeneration":3,"desiredNumberScheduled":3,"updatedNumberScheduled":3,"numberReady":3}}`,
		"/apis/apps/v1/namespaces/kube-system/daemonsets/aws-node": `{"kind":"DaemonSet","apiVersion":"apps/v1",
			"metadata":{"name":"aws-node","namespace":"kube-system","generation":5,"managedFields":[{"manager":"kubectl-set"}]},
			"spec":{"template":{"spec":{
			  "initContainers":[{"name":"aws-vpc-cni-init","image":"r/amazon-k8s-cni-init:v1.20.0-eksbuild.1"}],
			  "containers":[{"name":"aws-node","image":"r/amazon-k8s-cni:v1.23.1-eksbuild.1"},{"name":"aws-eks-nodeagent","image":"r/aws-network-policy-agent:v1.3.0-eksbuild.1"}]}}},
			"status":{"observedGeneration":4,"desiredNumberScheduled":3,"updatedNumberScheduled":2,"numberReady":3}}`,
		"/api/v1/namespaces/kube-system/pods": list("PodList", "v1",
			`{"metadata":{"name":"aws-node-abc","namespace":"kube-system"},"spec":{"nodeName":"ip-2"},
			  "status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}],
			  "containerStatuses":[{"name":"aws-node","restartCount":9,"ready":false,"image":"x","imageID":"","state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}`),
		"/apis/policy/v1/poddisruptionbudgets": list("PodDisruptionBudgetList", "policy/v1",
			`{"metadata":{"name":"web","namespace":"shop"},"status":{"disruptionsAllowed":0,"expectedPods":2,"currentHealthy":2,"desiredHealthy":2}}`,
			`{"metadata":{"name":"ok","namespace":"shop"},"status":{"disruptionsAllowed":1,"expectedPods":3,"currentHealthy":3,"desiredHealthy":2}}`),
		"/apis/apps/v1/deployments": list("DeploymentList", "apps/v1",
			`{"metadata":{"name":"karpenter","namespace":"karpenter"},"spec":{"selector":{},"template":{"spec":{"containers":[{"name":"controller","image":"public.ecr.aws/karpenter/controller:1.6.2@sha256:abc"}]}}}}`),
		"/api/v1/pods": list("PodList", "v1",
			`{"metadata":{"name":"train-0","namespace":"ml","annotations":{"karpenter.sh/do-not-disrupt":"true"}},"spec":{"nodeName":"ip-2"},"status":{}}`,
			`{"metadata":{"name":"web-1","namespace":"shop"},"spec":{"nodeName":"ip-2"},"status":{}}`),
	}
	if karpenter {
		routes["/apis/karpenter.sh/v1/nodeclaims"] = list("NodeClaimList", "karpenter.sh/v1",
			`{"apiVersion":"karpenter.sh/v1","kind":"NodeClaim","metadata":{"name":"default-x","labels":{"karpenter.sh/nodepool":"default"}},
			  "status":{"nodeName":"ip-2","imageID":"ami-1","conditions":[{"type":"Drifted","status":"True"}]}}`)
		routes["/apis/karpenter.sh/v1/nodepools"] = list("NodePoolList", "karpenter.sh/v1",
			`{"apiVersion":"karpenter.sh/v1","kind":"NodePool","metadata":{"name":"default"},
			  "spec":{"template":{"spec":{"expireAfter":"720h"}},"disruption":{"consolidationPolicy":"WhenEmptyOrUnderutilized","budgets":[{"nodes":"0","schedule":"0 9 * * mon-fri","duration":"8h"}]}}}`)
		routes["/apis/karpenter.k8s.aws/v1/ec2nodeclasses"] = list("EC2NodeClassList", "karpenter.k8s.aws/v1",
			`{"apiVersion":"karpenter.k8s.aws/v1","kind":"EC2NodeClass","metadata":{"name":"default"},
			  "spec":{"amiSelectorTerms":[{"alias":"al2023@latest"}]},
			  "status":{"amis":[{"id":"ami-2","name":"amazon-eks-node-al2023-x86_64-standard-1.36-v20260901"}]}}`)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/pods" && r.URL.Query().Get("fieldSelector") != "spec.nodeName=ip-2" {
			t.Errorf("unexpected cluster-wide pod list: %s", r.URL.String())
		}
		body, ok := routes[r.URL.Path]
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "NotFound", "code": 404})
			return
		}
		w.Write([]byte(body))
	}))
}

func TestCollectAgainstFakeAPI(t *testing.T) {
	srv := fakeAPI(t, true)
	defer srv.Close()
	f, err := Collect(context.Background(), &rest.Config{Host: srv.URL}, "sbx")
	if err != nil {
		t.Fatal(err)
	}
	if f.ServerVersion != "v1.36.2-eks-1a2b3c" || len(f.Nodes) != 3 {
		t.Fatalf("version %q nodes %d errors %v", f.ServerVersion, len(f.Nodes), f.Errors)
	}
	// coredns is absent (404): a finding, not a collection error
	if !f.Collected[KindAddons] || !f.Collected[KindKarpenter] || !f.Collected[KindPDBs] || !f.Collected[KindPods] {
		t.Fatalf("collected %v errors %v", f.Collected, f.Errors)
	}
	n := map[string]Node{}
	for _, x := range f.Nodes {
		n[x.Name] = x
	}
	if n["ip-1"].Owner != OwnerMNG || n["ip-1"].Image != "ami-0old" || !n["ip-1"].Unschedulable {
		t.Errorf("ip-1 %+v", n["ip-1"])
	}
	if n["ip-2"].Owner != OwnerKarpenter || n["ip-2"].Ready || !n["ip-2"].DoNotDisrupt || strings.Join(n["ip-2"].DoNotDisruptPods, ",") != "ml/train-0" {
		t.Errorf("ip-2 %+v", n["ip-2"])
	}
	if n["ip-3"].Owner != OwnerFargate {
		t.Errorf("ip-3 %+v", n["ip-3"])
	}
	if len(f.PDBs) != 1 || f.PDBs[0].Name != "web" {
		t.Errorf("pdbs %+v", f.PDBs)
	}
	k := f.Karpenter
	if k == nil || k.ControllerImage == "" || len(k.NodeClaims) != 1 || k.NodeClaims[0].Drifted != "True" ||
		len(k.NodePools[0].Budgets) != 1 || k.NodePools[0].ExpireAfter != "720h" ||
		k.NodeClasses[0].AMISelectorTerms != `[{"alias":"al2023@latest"}]` || len(k.NodeClasses[0].AMIs) != 1 {
		t.Fatalf("karpenter %+v", k)
	}
	var cni Addon
	for _, a := range f.Addons {
		if a.Name == AddonVPCCNI {
			cni = a
		}
		if a.Name == AddonCoreDNS && a.Found {
			t.Error("coredns should be not found")
		}
	}
	if !cni.Found || cni.ManagedByEKS || !cni.GenerationPending || len(cni.Containers) != 3 || !cni.Containers[0].Init {
		t.Errorf("aws-node %+v", cni)
	}
	if p := f.KubeSystemPods[0]; p.Reason != "CrashLoopBackOff" || p.Restarts != 9 || p.Ready {
		t.Errorf("pod %+v", p)
	}

	// and the whole thing analyzes into the expected verdicts
	r := Analyze(f, DefaultTargets(36))
	want := map[string]Status{
		CheckControlPlane: StatusPass, CheckNodeVersions: StatusFail, CheckNodeHealth: StatusFail,
		CheckKubeProxy: StatusFail, CheckCoreDNS: StatusWarn, CheckVPCCNI: StatusFail, CheckSystemPods: StatusFail,
	}
	for id, s := range want {
		if got := r.Check(id).Status; got != s {
			t.Errorf("%s: %s, want %s\n%s", id, got, s, problemText(r.Check(id)))
		}
	}
	mustContain(t, problemText(r.Check(CheckNodeVersions)), `while schedule "0 9 * * mon-fri" (for 8h) is active`, "ml/train-0")
}

func TestCollectWithoutKarpenter(t *testing.T) {
	srv := fakeAPI(t, false)
	defer srv.Close()
	f, err := Collect(context.Background(), &rest.Config{Host: srv.URL}, "sbx")
	if err != nil {
		t.Fatal(err)
	}
	if f.Karpenter != nil || !f.Collected[KindKarpenter] {
		t.Fatalf("karpenter %+v collected=%v errors=%v", f.Karpenter, f.Collected[KindKarpenter], f.Errors)
	}
	// Karpenter-labelled node on a cluster without CRDs: explained, not a crash
	mustContain(t, problemText(Analyze(f, DefaultTargets(36)).Check(CheckNodeVersions)), "no Karpenter CRDs are installed")
}

func TestCollectUnreachable(t *testing.T) {
	_, err := Collect(context.Background(), &rest.Config{Host: "http://127.0.0.1:1"}, "x")
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("got %v", err)
	}
}
