package diag

import (
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ClusterSnapshot struct {
	ServerVersion  string
	HealthScore    int
	Nodes          []NodeSummary
	Namespaces     []NamespaceSummary
	TopConsumers   []PodConsumer
	PVCs           []PVCSummary
	Quotas         []QuotaUsage
	RecentWarnings []EventSummary
	CapturedAt     time.Time
}

type NodeSummary struct {
	Name         string
	Status       string
	CPUCapacity  string
	CPURequested string
	MemCapacity  string
	MemRequested string
}

type NamespaceSummary struct {
	Name         string
	Deployments  int
	StatefulSets int
	TotalPods    int
	RunningPods  int
	FailingPods  int
}

type PodConsumer struct {
	Name       string
	Namespace  string
	CPURequest string
	MemRequest string
	CPUMillis  int64
	MemBytes   int64
}

type PVCSummary struct {
	Name      string
	Namespace string
	Capacity  string
	Status    string
}

type QuotaUsage struct {
	Namespace   string
	Resource    string
	Used        string
	Hard        string
	UsedPercent float64
}

type EventSummary struct {
	Reason    string
	Message   string
	Count     int32
}

func (e *Engine) ClusterSnapshot() (*ClusterSnapshot, error) {
	snap := &ClusterSnapshot{CapturedAt: time.Now()}
	ver, err := e.k8s.Discovery().ServerVersion()
	if err == nil {
		snap.ServerVersion = ver.GitVersion
	} else {
		snap.ServerVersion = "unknown"
	}
	nodes, err := e.k8s.CoreV1().Nodes().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	allPods, err := e.k8s.CoreV1().Pods(metav1.NamespaceAll).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}
	cpuReqByNode := map[string]int64{}
	memReqByNode := map[string]int64{}
	for _, p := range allPods.Items {
		if p.Status.Phase == corev1.PodRunning || p.Status.Phase == corev1.PodPending {
			for _, c := range p.Spec.Containers {
				cpuReqByNode[p.Spec.NodeName] += c.Resources.Requests.Cpu().MilliValue()
				memReqByNode[p.Spec.NodeName] += c.Resources.Requests.Memory().Value()
			}
		}
	}
	healthDeductions := 0
	for _, node := range nodes.Items {
		ns := NodeSummary{
			Name: node.Name, Status: "Ready",
			CPUCapacity:  fmt.Sprintf("%dm", node.Status.Capacity.Cpu().MilliValue()),
			MemCapacity:  formatBytes(node.Status.Capacity.Memory().Value()),
			CPURequested: fmt.Sprintf("%dm", cpuReqByNode[node.Name]),
			MemRequested: formatBytes(memReqByNode[node.Name]),
		}
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue {
				ns.Status = "NotReady"
				healthDeductions += 20
			}
			if cond.Type == corev1.NodeMemoryPressure && cond.Status == corev1.ConditionTrue {
				healthDeductions += 15
			}
			if cond.Type == corev1.NodeDiskPressure && cond.Status == corev1.ConditionTrue {
				healthDeductions += 15
			}
		}
		snap.Nodes = append(snap.Nodes, ns)
	}
	nsList, err := e.k8s.CoreV1().Namespaces().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, ns := range nsList.Items {
		summary := NamespaceSummary{Name: ns.Name}
		deploys, _ := e.k8s.AppsV1().Deployments(ns.Name).List(e.ctx, metav1.ListOptions{})
		if deploys != nil {
			summary.Deployments = len(deploys.Items)
		}
		ss, _ := e.k8s.AppsV1().StatefulSets(ns.Name).List(e.ctx, metav1.ListOptions{})
		if ss != nil {
			summary.StatefulSets = len(ss.Items)
		}
		for _, p := range allPods.Items {
			if p.Namespace != ns.Name {
				continue
			}
			summary.TotalPods++
			if p.Status.Phase == corev1.PodRunning {
				summary.RunningPods++
			}
			for _, cs := range p.Status.ContainerStatuses {
				if cs.State.Waiting != nil &&
					(cs.State.Waiting.Reason == "CrashLoopBackOff" ||
						cs.State.Waiting.Reason == "ImagePullBackOff") {
					summary.FailingPods++
					healthDeductions += 5
					break
				}
			}
		}
		if summary.TotalPods > 0 || summary.Deployments > 0 {
			snap.Namespaces = append(snap.Namespaces, summary)
		}
	}
	sort.Slice(snap.Namespaces, func(i, j int) bool {
		return snap.Namespaces[i].FailingPods > snap.Namespaces[j].FailingPods
	})
	var consumers []PodConsumer
	for _, p := range allPods.Items {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		var cpuMillis, memBytes int64
		for _, c := range p.Spec.Containers {
			cpuMillis += c.Resources.Requests.Cpu().MilliValue()
			memBytes += c.Resources.Requests.Memory().Value()
		}
		consumers = append(consumers, PodConsumer{
			Name: p.Name, Namespace: p.Namespace,
			CPURequest: fmt.Sprintf("%dm", cpuMillis),
			MemRequest: formatBytes(memBytes),
			CPUMillis: cpuMillis, MemBytes: memBytes,
		})
	}
	sort.Slice(consumers, func(i, j int) bool {
		return consumers[i].MemBytes > consumers[j].MemBytes
	})
	snap.TopConsumers = consumers
	pvcs, _ := e.k8s.CoreV1().PersistentVolumeClaims(metav1.NamespaceAll).List(e.ctx, metav1.ListOptions{})
	if pvcs != nil {
		for _, pvc := range pvcs.Items {
			cap := "unknown"
			if storage, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
				cap = storage.String()
			}
			snap.PVCs = append(snap.PVCs, PVCSummary{
				Name: pvc.Name, Namespace: pvc.Namespace,
				Capacity: cap, Status: string(pvc.Status.Phase),
			})
			if pvc.Status.Phase != corev1.ClaimBound {
				healthDeductions += 5
			}
		}
	}
	quotas, _ := e.k8s.CoreV1().ResourceQuotas(metav1.NamespaceAll).List(e.ctx, metav1.ListOptions{})
	if quotas != nil {
		for _, q := range quotas.Items {
			for res, hard := range q.Status.Hard {
				used := q.Status.Used[res]
				hardVal := hard.MilliValue()
				pct := 0.0
				if hardVal > 0 {
					pct = float64(used.MilliValue()) / float64(hardVal) * 100
				}
				if pct >= 75 {
					snap.Quotas = append(snap.Quotas, QuotaUsage{
						Namespace: q.Namespace, Resource: string(res),
						Used: used.String(), Hard: hard.String(), UsedPercent: pct,
					})
				}
			}
		}
	}
	events, _ := e.k8s.CoreV1().Events(metav1.NamespaceAll).List(e.ctx, metav1.ListOptions{
		FieldSelector: "type=Warning",
	})
	if events != nil {
		cutoff := time.Now().Add(-1 * time.Hour)
		seen := map[string]bool{}
		for _, ev := range events.Items {
			if ev.LastTimestamp.Time.Before(cutoff) {
				continue
			}
			key := ev.Reason + "/" + ev.InvolvedObject.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			snap.RecentWarnings = append(snap.RecentWarnings, EventSummary{
				Reason: ev.Reason, Message: truncate(ev.Message, 80), Count: ev.Count,
			})
		}
		sort.Slice(snap.RecentWarnings, func(i, j int) bool {
			return snap.RecentWarnings[i].Count > snap.RecentWarnings[j].Count
		})
	}
	snap.HealthScore = 100 - healthDeductions
	if snap.HealthScore < 0 {
		snap.HealthScore = 0
	}
	return snap, nil
}

func formatBytes(b int64) string {
	if b == 0 {
		return "0"
	}
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fGi", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fMi", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0fKi", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%dB", b)
}
