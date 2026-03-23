package diag

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type CostResult struct {
	OverProvisioned      []OverProvisionedPod
	IdleNamespaces       []string
	UnderutilisedNodes   []UnderutilisedNode
	EstimatedWasteCPU    string
	EstimatedWasteMemory string
}

type OverProvisionedPod struct {
	Name           string
	Namespace      string
	CPURequest     string
	MemRequest     string
	WasteScore     int
	Recommendation string
}

type UnderutilisedNode struct {
	Name       string
	CPUPercent float64
	MemPercent float64
}

func (e *Engine) CostAnalysis(filterNS string) (*CostResult, error) {
	result := &CostResult{}
	ns := filterNS
	if ns == "" {
		ns = e.ns()
	}
	actualCPU := map[string]int64{}
	actualMem := map[string]int64{}
	args := []string{"top", "pods", "--no-headers"}
	if ns == metav1.NamespaceAll {
		args = append(args, "--all-namespaces")
	} else {
		args = append(args, "-n", ns)
	}
	topOut, err := exec.CommandContext(e.ctx, "kubectl", args...).Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(topOut)), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			key, cpuStr, memStr := "", "", ""
			if ns == metav1.NamespaceAll && len(fields) >= 4 {
				key = fields[0] + "/" + fields[1]
				cpuStr, memStr = fields[2], fields[3]
			} else if len(fields) >= 3 {
				key = fields[0]
				cpuStr, memStr = fields[1], fields[2]
			}
			if key != "" {
				actualCPU[key] = parseCPUMillis(cpuStr)
				actualMem[key] = parseMemMi(memStr)
			}
		}
	}
	podList, err := e.k8s.CoreV1().Pods(ns).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var totalWasteCPU, totalWasteMem int64
	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range pod.Spec.Containers {
			reqCPU := c.Resources.Requests.Cpu().MilliValue()
			reqMem := c.Resources.Requests.Memory().Value() / (1024 * 1024)
			if reqCPU == 0 && reqMem == 0 {
				continue
			}
			key := pod.Name
			if ns == metav1.NamespaceAll {
				key = pod.Namespace + "/" + pod.Name
			}
			actualC := actualCPU[key]
			actualM := actualMem[key]
			if actualC == 0 && actualM == 0 {
				continue
			}
			wasteScore := 0
			if reqCPU > 0 && actualC > 0 {
				ratio := float64(actualC) / float64(reqCPU) * 100
				if ratio < 10 {
					wasteScore += 50
					totalWasteCPU += reqCPU - actualC
				} else if ratio < 25 {
					wasteScore += 25
				}
			}
			if reqMem > 0 && actualM > 0 {
				ratio := float64(actualM) / float64(reqMem) * 100
				if ratio < 10 {
					wasteScore += 50
					totalWasteMem += reqMem - actualM
				} else if ratio < 25 {
					wasteScore += 25
				}
			}
			if wasteScore >= 40 {
				rec := ""
				if reqCPU > 0 && actualC > 0 {
					rec = fmt.Sprintf("consider reducing CPU request from %dm to ~%dm", reqCPU, actualC*2)
				}
				result.OverProvisioned = append(result.OverProvisioned, OverProvisionedPod{
					Name: pod.Name, Namespace: pod.Namespace,
					CPURequest: fmt.Sprintf("%dm", reqCPU), MemRequest: fmt.Sprintf("%dMi", reqMem),
					WasteScore: wasteScore, Recommendation: rec,
				})
			}
		}
	}
	sort.Slice(result.OverProvisioned, func(i, j int) bool {
		return result.OverProvisioned[i].WasteScore > result.OverProvisioned[j].WasteScore
	})
	nsList, _ := e.k8s.CoreV1().Namespaces().List(e.ctx, metav1.ListOptions{})
	if nsList != nil {
		for _, n := range nsList.Items {
			pods, _ := e.k8s.CoreV1().Pods(n.Name).List(e.ctx, metav1.ListOptions{FieldSelector: "status.phase=Running"})
			if pods != nil && len(pods.Items) == 0 && !strings.HasPrefix(n.Name, "kube-") && n.Name != "default" {
				result.IdleNamespaces = append(result.IdleNamespaces, n.Name)
			}
		}
	}
	nodeOut, _ := exec.CommandContext(e.ctx, "kubectl", "top", "nodes", "--no-headers").Output()
	if nodeOut != nil {
		for _, line := range strings.Split(strings.TrimSpace(string(nodeOut)), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 5 {
				continue
			}
			cpuPct := parsePercent(fields[2])
			memPct := parsePercent(fields[4])
			if cpuPct < 20 && memPct < 20 {
				result.UnderutilisedNodes = append(result.UnderutilisedNodes, UnderutilisedNode{
					Name: fields[0], CPUPercent: cpuPct, MemPercent: memPct,
				})
			}
		}
	}
	if totalWasteCPU > 0 {
		result.EstimatedWasteCPU = fmt.Sprintf("%dm", totalWasteCPU)
	}
	if totalWasteMem > 0 {
		result.EstimatedWasteMemory = fmt.Sprintf("%dMi", totalWasteMem)
	}
	return result, nil
}
