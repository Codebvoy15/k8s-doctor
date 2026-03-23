package diag

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Severity string

const (
	SeverityCritical Severity = "CRITICAL"
	SeverityWarning  Severity = "WARNING"
	SeverityInfo     Severity = "INFO"
)

type Finding struct {
	Severity  Severity `json:"severity"`
	Category  string   `json:"category"`
	Title     string   `json:"title"`
	Detail    string   `json:"detail,omitempty"`
	Remedy    string   `json:"remedy,omitempty"`
	Score     int      `json:"score"`
	Object    string   `json:"object,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
}

type NodeMetric struct {
	Name       string
	CPUUsage   string
	CPUPercent float64
	MemUsage   string
	MemPercent float64
}

type ASGGroup struct {
	Name                string
	MinSize             int32
	MaxSize             int32
	DesiredCapacity     int32
	InServiceCount      int32
	Status              string
	LastScalingActivity string
}

type Engine struct {
	ctx       context.Context
	namespace string
	verbose   bool
	k8s       kubernetes.Interface
}

func NewEngine(ctx context.Context, namespace string, verbose bool) (*Engine, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	restCfg, err := cfg.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("could not build kubeconfig: %w", err)
	}
	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("could not create k8s client: %w", err)
	}
	return &Engine{ctx: ctx, namespace: namespace, verbose: verbose, k8s: k8sClient}, nil
}

func (e *Engine) ns() string {
	if e.namespace == "" {
		return metav1.NamespaceAll
	}
	return e.namespace
}

func (e *Engine) PodHealth() ([]Finding, error) {
	pods, err := e.k8s.CoreV1().Pods(e.ns()).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil {
				switch cs.State.Waiting.Reason {
				case "CrashLoopBackOff":
					findings = append(findings, Finding{
						Severity: SeverityCritical, Category: "pods", Title: "CrashLoopBackOff",
						Detail: fmt.Sprintf("container=%s restarts=%d", cs.Name, cs.RestartCount),
						Remedy: fmt.Sprintf("./k8s-doctor triage logs %s -n %s", pod.Name, pod.Namespace),
						Score: 90, Object: pod.Name, Namespace: pod.Namespace,
					})
				case "ImagePullBackOff", "ErrImagePull":
					findings = append(findings, Finding{
						Severity: SeverityWarning, Category: "pods", Title: "ImagePullBackOff",
						Detail: fmt.Sprintf("image=%s", cs.Image),
						Remedy: fmt.Sprintf("./k8s-doctor aws iam -n %s", pod.Namespace),
						Score: 75, Object: pod.Name, Namespace: pod.Namespace,
					})
				}
			}
			if cs.LastTerminationState.Terminated != nil &&
				cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
				findings = append(findings, Finding{
					Severity: SeverityCritical, Category: "pods", Title: "OOMKilled",
					Detail: fmt.Sprintf("container=%s restarts=%d", cs.Name, cs.RestartCount),
					Remedy: "kubectl top pod " + pod.Name + " -n " + pod.Namespace,
					Score: 85, Object: pod.Name, Namespace: pod.Namespace,
				})
			}
		}
		if pod.DeletionTimestamp != nil {
			age := time.Since(pod.DeletionTimestamp.Time)
			if age > 5*time.Minute {
				findings = append(findings, Finding{
					Severity: SeverityWarning, Category: "pods", Title: "Stuck Terminating",
					Detail: fmt.Sprintf("stuck for %s", age.Round(time.Second)),
					Remedy: fmt.Sprintf("kubectl delete pod %s -n %s --force --grace-period=0", pod.Name, pod.Namespace),
					Score: 60, Object: pod.Name, Namespace: pod.Namespace,
				})
			}
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "pods", Title: "All pods healthy"}}, nil
	}
	return findings, nil
}

func (e *Engine) PendingPods() ([]Finding, error) {
	pods, err := e.k8s.CoreV1().Pods(e.ns()).List(e.ctx, metav1.ListOptions{
		FieldSelector: "status.phase=Pending",
	})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, pod := range pods.Items {
		events, _ := e.k8s.CoreV1().Events(pod.Namespace).List(e.ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Pod", pod.Name),
		})
		reason := "unknown scheduler reason"
		for _, ev := range events.Items {
			if ev.Reason == "FailedScheduling" {
				reason = ev.Message
				break
			}
		}
		score := 70
		remedy := "./k8s-doctor node pressure"
		if strings.Contains(reason, "Insufficient memory") {
			score, remedy = 85, "./k8s-doctor node top"
		} else if strings.Contains(reason, "Insufficient cpu") {
			score, remedy = 85, "./k8s-doctor node top"
		} else if strings.Contains(reason, "had taint") {
			score, remedy = 80, "./k8s-doctor node taints"
		}
		findings = append(findings, Finding{
			Severity: SeverityWarning, Category: "pods", Title: "Pending Pod",
			Detail: truncate(reason, 200), Remedy: remedy,
			Score: score, Object: pod.Name, Namespace: pod.Namespace,
		})
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "pods", Title: "No pending pods"}}, nil
	}
	return findings, nil
}

func (e *Engine) RecentWarningEvents(window time.Duration) ([]Finding, error) {
	events, err := e.k8s.CoreV1().Events(e.ns()).List(e.ctx, metav1.ListOptions{
		FieldSelector: "type=Warning",
	})
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-window)
	seen := map[string]bool{}
	var findings []Finding
	for _, ev := range events.Items {
		if ev.LastTimestamp.Time.Before(cutoff) {
			continue
		}
		key := ev.Reason + "/" + ev.InvolvedObject.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		score := 40
		if ev.Count > 10 {
			score = 70
		}
		findings = append(findings, Finding{
			Severity: SeverityWarning, Category: "events", Title: ev.Reason,
			Detail: fmt.Sprintf("[%dx] %s — %s", ev.Count, ev.InvolvedObject.Name, truncate(ev.Message, 120)),
			Score: score, Object: ev.InvolvedObject.Name, Namespace: ev.Namespace,
		})
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "events",
			Title: fmt.Sprintf("No warning events in last %s", window)}}, nil
	}
	return findings, nil
}

func (e *Engine) HighRestartPods(threshold int32) ([]Finding, error) {
	pods, err := e.k8s.CoreV1().Pods(e.ns()).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.RestartCount >= threshold {
				findings = append(findings, Finding{
					Severity: SeverityWarning, Category: "pods", Title: "Frequent Restarts",
					Detail: fmt.Sprintf("container=%s restarts=%d", cs.Name, cs.RestartCount),
					Remedy: fmt.Sprintf("./k8s-doctor triage logs %s -n %s", pod.Name, pod.Namespace),
					Score: 65, Object: pod.Name, Namespace: pod.Namespace,
				})
			}
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "pods", Title: "No high-restart pods"}}, nil
	}
	return findings, nil
}

func (e *Engine) FetchCrashLogs(podName string, tailLines int) ([]string, error) {
	ns := e.namespace
	if ns == "" {
		ns = "default"
	}
	if podName == "" {
		pods, err := e.k8s.CoreV1().Pods(ns).List(e.ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, pod := range pods.Items {
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
					podName = pod.Name
					break
				}
			}
			if podName != "" {
				break
			}
		}
		if podName == "" {
			return nil, fmt.Errorf("no crashing pods found — specify pod name explicitly")
		}
	}
	pod, err := e.k8s.CoreV1().Pods(ns).Get(e.ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("pod %s not found in ns %s", podName, ns)
	}
	var logs []string
	tail := int64(tailLines)
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.LastTerminationState.Terminated != nil {
			req := e.k8s.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
				Container: cs.Name, Previous: true, TailLines: &tail,
			})
			if b, err := req.DoRaw(e.ctx); err == nil {
				logs = append(logs, fmt.Sprintf("=== PREVIOUS (crashed) container: %s ===", cs.Name))
				logs = append(logs, string(b))
			}
		}
		req := e.k8s.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
			Container: cs.Name, TailLines: &tail,
		})
		if b, err := req.DoRaw(e.ctx); err == nil {
			logs = append(logs, fmt.Sprintf("=== CURRENT container: %s ===", cs.Name))
			logs = append(logs, string(b))
		}
	}
	return logs, nil
}

func (e *Engine) NodePressure() ([]Finding, error) {
	nodes, err := e.k8s.CoreV1().Nodes().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, node := range nodes.Items {
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue {
				findings = append(findings, Finding{
					Severity: SeverityCritical, Category: "nodes", Title: "Node NotReady",
					Detail: fmt.Sprintf("reason=%s: %s", cond.Reason, cond.Message),
					Remedy: "journalctl -u kubelet -n 50", Score: 95, Object: node.Name,
				})
			}
			if cond.Status == corev1.ConditionTrue {
				switch cond.Type {
				case corev1.NodeMemoryPressure:
					findings = append(findings, Finding{
						Severity: SeverityCritical, Category: "nodes", Title: "MemoryPressure",
						Detail: cond.Message, Remedy: "evict pods or scale up node group",
						Score: 88, Object: node.Name,
					})
				case corev1.NodeDiskPressure:
					findings = append(findings, Finding{
						Severity: SeverityCritical, Category: "nodes", Title: "DiskPressure",
						Detail: cond.Message, Remedy: "clean /var/log or increase EBS volume",
						Score: 85, Object: node.Name,
					})
				case corev1.NodePIDPressure:
					findings = append(findings, Finding{
						Severity: SeverityWarning, Category: "nodes", Title: "PIDPressure",
						Detail: cond.Message, Remedy: "check for fork bombs",
						Score: 70, Object: node.Name,
					})
				}
			}
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "nodes", Title: "All nodes healthy"}}, nil
	}
	return findings, nil
}

func (e *Engine) NodeTaints() ([]Finding, error) {
	nodes, err := e.k8s.CoreV1().Nodes().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, node := range nodes.Items {
		for _, t := range node.Spec.Taints {
			findings = append(findings, Finding{
				Severity: SeverityInfo, Category: "nodes",
				Title: fmt.Sprintf("Taint: %s=%s:%s", t.Key, t.Value, t.Effect),
				Object: node.Name,
			})
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "nodes", Title: "No taints on any node"}}, nil
	}
	return findings, nil
}

func (e *Engine) NodeTop() ([]NodeMetric, error) {
	out, err := exec.CommandContext(e.ctx, "kubectl", "top", "nodes", "--no-headers").Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl top nodes failed: %w", err)
	}
	var results []NodeMetric
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		results = append(results, NodeMetric{
			Name: fields[0], CPUUsage: fields[1],
			CPUPercent: parsePercent(fields[2]),
			MemUsage: fields[3], MemPercent: parsePercent(fields[4]),
		})
	}
	return results, nil
}

func (e *Engine) CordonNode(nodeName string, drain bool) error {
	node, err := e.k8s.CoreV1().Nodes().Get(e.ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("node %s not found: %w", nodeName, err)
	}
	node.Spec.Unschedulable = true
	if _, err := e.k8s.CoreV1().Nodes().Update(e.ctx, node, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("cordon failed: %w", err)
	}
	fmt.Printf("✓ Node %s cordoned\n", nodeName)
	if drain {
		cmd := exec.CommandContext(e.ctx, "kubectl", "drain", nodeName,
			"--ignore-daemonsets", "--delete-emptydir-data", "--force")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("drain failed: %w", err)
		}
		fmt.Printf("✓ Node %s drained\n", nodeName)
	}
	return nil
}

func (e *Engine) DNSDiag() ([]Finding, error) {
	pods, err := e.k8s.CoreV1().Pods("kube-system").List(e.ctx, metav1.ListOptions{
		LabelSelector: "k8s-app=kube-dns",
	})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			findings = append(findings, Finding{
				Severity: SeverityCritical, Category: "network", Title: "CoreDNS pod not running",
				Detail: fmt.Sprintf("pod=%s phase=%s", pod.Name, pod.Status.Phase),
				Remedy: "kubectl describe pod " + pod.Name + " -n kube-system",
				Score: 90, Object: pod.Name, Namespace: "kube-system",
			})
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.RestartCount > 5 {
				findings = append(findings, Finding{
					Severity: SeverityWarning, Category: "network", Title: "CoreDNS high restarts",
					Detail: fmt.Sprintf("pod=%s restarts=%d", pod.Name, cs.RestartCount),
					Score: 75, Object: pod.Name, Namespace: "kube-system",
				})
			}
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "network", Title: "CoreDNS pods healthy"}}, nil
	}
	return findings, nil
}

func (e *Engine) ServiceEndpoints(svcName string) ([]Finding, error) {
	ns := e.namespace
	if ns == "" {
		ns = "default"
	}
	if svcName == "" {
		return []Finding{{Severity: SeverityInfo, Category: "network",
			Title: "Specify a service: ./k8s-doctor network svc <n> -n <namespace>"}}, nil
	}
	ep, err := e.k8s.CoreV1().Endpoints(ns).Get(e.ctx, svcName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("service %s not found in ns %s: %w", svcName, ns, err)
	}
	total := 0
	for _, s := range ep.Subsets {
		total += len(s.Addresses)
	}
	if total == 0 {
		return []Finding{{
			Severity: SeverityCritical, Category: "network", Title: "Service has no endpoints",
			Detail: fmt.Sprintf("service=%s — selector may not match any pods", svcName),
			Remedy: "kubectl get pods -l <selector> -n " + ns,
			Score: 88, Object: svcName, Namespace: ns,
		}}, nil
	}
	return []Finding{{Severity: SeverityInfo, Category: "network",
		Title: fmt.Sprintf("Service %s has %d healthy endpoint(s)", svcName, total),
		Object: svcName, Namespace: ns,
	}}, nil
}

func (e *Engine) NetworkPolicies() ([]Finding, error) {
	netpols, err := e.k8s.NetworkingV1().NetworkPolicies(e.ns()).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, np := range netpols.Items {
		if len(np.Spec.Ingress) == 0 && len(np.Spec.Egress) == 0 {
			findings = append(findings, Finding{
				Severity: SeverityWarning, Category: "network", Title: "Deny-all NetworkPolicy",
				Detail: fmt.Sprintf("policy=%s blocks all traffic", np.Name),
				Remedy: "add explicit ingress/egress rules",
				Score: 60, Object: np.Name, Namespace: np.Namespace,
			})
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "network",
			Title: fmt.Sprintf("%d NetworkPolicies — none flagged", len(netpols.Items))}}, nil
	}
	return findings, nil
}

func (e *Engine) IngressHealth() ([]Finding, error) {
	ingresses, err := e.k8s.NetworkingV1().Ingresses(e.ns()).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, ing := range ingresses.Items {
		if len(ing.Status.LoadBalancer.Ingress) == 0 {
			findings = append(findings, Finding{
				Severity: SeverityWarning, Category: "network", Title: "Ingress missing LB address",
				Detail: fmt.Sprintf("ingress=%s — ALB may not be provisioned", ing.Name),
				Remedy: "./k8s-doctor aws alb",
				Score: 70, Object: ing.Name, Namespace: ing.Namespace,
			})
		}
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "network",
			Title: "All ingresses have load balancer addresses"}}, nil
	}
	return findings, nil
}

func (e *Engine) EC2NodeHealth(clusterName, region, profile string) ([]Finding, error) {
	args := []string{"ec2", "describe-instance-status",
		"--filters", "Name=tag:eks:cluster-name,Values=" + clusterName,
		"--query", "InstanceStatuses[?InstanceStatus.Status!='ok' || SystemStatus.Status!='ok'].[InstanceId,InstanceStatus.Status,SystemStatus.Status]",
		"--output", "text",
	}
	if region != "" {
		args = append(args, "--region", region)
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	out, err := exec.CommandContext(e.ctx, "aws", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("aws ec2 describe-instance-status failed: %w", err)
	}
	var findings []Finding
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		findings = append(findings, Finding{
			Severity: SeverityCritical, Category: "aws", Title: "EC2 status check FAILED",
			Detail: fmt.Sprintf("instance=%s status=%s/%s", fields[0], fields[1], fields[2]),
			Remedy: "aws ec2 terminate-instances --instance-ids " + fields[0],
			Score: 92, Object: fields[0],
		})
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "aws", Title: "All EC2 instance checks passing"}}, nil
	}
	return findings, nil
}

func (e *Engine) ALBHealth(clusterName, region, profile string) ([]Finding, error) {
	return []Finding{{Severity: SeverityInfo, Category: "aws",
		Title:  "ALB check — run: aws elbv2 describe-target-health --target-group-arn <arn>",
	}}, nil
}

func (e *Engine) SGAudit(clusterName, region, profile string) ([]Finding, error) {
	return []Finding{{Severity: SeverityInfo, Category: "aws",
		Title:  "SG audit",
		Detail: "aws ec2 describe-security-groups --filters Name=tag:aws:eks:cluster-name,Values=" + clusterName,
		Remedy: "ensure: control-plane→nodes 443/10250, nodes→nodes all, ALB→nodes 30000-32767",
	}}, nil
}

func (e *Engine) IAMAudit(clusterName, namespace, region, profile string) ([]Finding, error) {
	ns := namespace
	if ns == "" {
		ns = metav1.NamespaceAll
	}
	sas, err := e.k8s.CoreV1().ServiceAccounts(ns).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, sa := range sas.Items {
		roleARN := sa.Annotations["eks.amazonaws.com/role-arn"]
		if roleARN == "" {
			continue
		}
		findings = append(findings, Finding{
			Severity: SeverityInfo, Category: "aws", Title: "IRSA annotation found",
			Detail: fmt.Sprintf("sa=%s role=%s", sa.Name, roleARN),
			Remedy: "verify trust policy allows oidc provider for this cluster",
			Object: sa.Name, Namespace: sa.Namespace,
		})
	}
	if len(findings) == 0 {
		return []Finding{{Severity: SeverityInfo, Category: "aws",
			Title: "No IRSA-annotated service accounts found"}}, nil
	}
	return findings, nil
}

func (e *Engine) ASGStatus(clusterName, region, profile string) ([]ASGGroup, error) {
	args := []string{"autoscaling", "describe-auto-scaling-groups",
		"--filters", "Name=tag-key,Values=k8s.io/cluster/" + clusterName,
		"--query", "AutoScalingGroups[].[AutoScalingGroupName,MinSize,MaxSize,DesiredCapacity]",
		"--output", "text",
	}
	if region != "" {
		args = append(args, "--region", region)
	}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	out, err := exec.CommandContext(e.ctx, "aws", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("aws autoscaling failed: %w", err)
	}
	var groups []ASGGroup
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		groups = append(groups, ASGGroup{
			Name: fields[0], MinSize: parseInt32(fields[1]),
			MaxSize: parseInt32(fields[2]), DesiredCapacity: parseInt32(fields[3]),
			Status: "OK",
		})
	}
	return groups, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func parsePercent(s string) float64 {
	s = strings.TrimSuffix(s, "%")
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

func parseInt32(s string) int32 {
	var i int32
	fmt.Sscanf(s, "%d", &i)
	return i
}
