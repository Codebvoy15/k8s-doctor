package diag

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (e *Engine) PredictRisks() ([]Finding, error) {
	var findings []Finding
	pods, err := e.k8s.CoreV1().Pods(e.ns()).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	nodes, err := e.k8s.CoreV1().Nodes().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	deploys, _ := e.k8s.AppsV1().Deployments(e.ns()).List(e.ctx, metav1.ListOptions{})
	hpas, _ := e.k8s.AutoscalingV2().HorizontalPodAutoscalers(e.ns()).List(e.ctx, metav1.ListOptions{})
	pvcs, _ := e.k8s.CoreV1().PersistentVolumeClaims(e.ns()).List(e.ctx, metav1.ListOptions{})
	pdbs, _ := e.k8s.PolicyV1().PodDisruptionBudgets(e.ns()).List(e.ctx, metav1.ListOptions{})

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range pod.Spec.Containers {
			if c.Resources.Limits == nil ||
				(c.Resources.Limits.Cpu().IsZero() && c.Resources.Limits.Memory().IsZero()) {
				findings = append(findings, Finding{
					Severity: SeverityWarning, Category: "predict", Title: "No resource limits",
					Detail: fmt.Sprintf("container=%s in pod=%s", c.Name, pod.Name),
					Remedy: "add resources.limits to container spec",
					Score: 55, Object: pod.Name, Namespace: pod.Namespace,
				})
				break
			}
		}
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range pod.Spec.Containers {
			if c.Resources.Limits == nil || c.Resources.Requests == nil {
				continue
			}
			limMem := c.Resources.Limits.Memory().Value()
			reqMem := c.Resources.Requests.Memory().Value()
			if limMem > 0 && reqMem > 0 {
				pct := float64(reqMem) / float64(limMem) * 100
				if pct >= 85 {
					findings = append(findings, Finding{
						Severity: SeverityCritical, Category: "predict", Title: "OOM risk — memory request near limit",
						Detail: fmt.Sprintf("container=%s at %.0f%% of limit (%s/%s)", c.Name, pct, formatBytes(reqMem), formatBytes(limMem)),
						Remedy: fmt.Sprintf("increase memory limit for %s in ns %s", pod.Name, pod.Namespace),
						Score: 80, Object: pod.Name, Namespace: pod.Namespace,
					})
				}
			}
		}
	}

	seen := map[string]bool{}
	for _, pod := range pods.Items {
		for _, c := range pod.Spec.Containers {
			if strings.HasSuffix(c.Image, ":latest") || !strings.Contains(c.Image, ":") {
				key := pod.Namespace + "/" + c.Image
				if !seen[key] {
					seen[key] = true
					findings = append(findings, Finding{
						Severity: SeverityWarning, Category: "predict", Title: "Image uses :latest tag",
						Detail: fmt.Sprintf("image=%s — uncontrolled updates", c.Image),
						Remedy: "pin to a specific digest or semver tag",
						Score: 50, Object: pod.Name, Namespace: pod.Namespace,
					})
				}
			}
		}
	}

	if deploys != nil {
		for _, d := range deploys.Items {
			if d.Spec.Replicas != nil && *d.Spec.Replicas == 1 {
				findings = append(findings, Finding{
					Severity: SeverityWarning, Category: "predict", Title: "Single replica deployment",
					Detail: fmt.Sprintf("deployment=%s — any pod failure = full outage", d.Name),
					Remedy: fmt.Sprintf("kubectl scale deployment/%s --replicas=2 -n %s", d.Name, d.Namespace),
					Score: 60, Object: d.Name, Namespace: d.Namespace,
				})
			}
		}
	}

	if deploys != nil && pdbs != nil {
		pdbTargets := map[string]bool{}
		for _, pdb := range pdbs.Items {
			if pdb.Spec.Selector != nil {
				for k, v := range pdb.Spec.Selector.MatchLabels {
					pdbTargets[pdb.Namespace+"/"+k+"="+v] = true
				}
			}
		}
		for _, d := range deploys.Items {
			if d.Spec.Replicas != nil && *d.Spec.Replicas <= 1 {
				continue
			}
			hasPDB := false
			for k, v := range d.Spec.Template.Labels {
				if pdbTargets[d.Namespace+"/"+k+"="+v] {
					hasPDB = true
					break
				}
			}
			if !hasPDB {
				findings = append(findings, Finding{
					Severity: SeverityInfo, Category: "predict", Title: "No PodDisruptionBudget",
					Detail: fmt.Sprintf("deployment=%s", d.Name),
					Remedy: fmt.Sprintf("create a PDB for deployment/%s -n %s", d.Name, d.Namespace),
					Score: 35, Object: d.Name, Namespace: d.Namespace,
				})
			}
		}
	}

	if hpas != nil {
		for _, hpa := range hpas.Items {
			if hpa.Status.CurrentReplicas >= hpa.Spec.MaxReplicas {
				findings = append(findings, Finding{
					Severity: SeverityCritical, Category: "predict", Title: "HPA at max replicas — no scale headroom",
					Detail: fmt.Sprintf("hpa=%s current=%d max=%d", hpa.Name, hpa.Status.CurrentReplicas, hpa.Spec.MaxReplicas),
					Remedy: fmt.Sprintf("increase maxReplicas on hpa/%s -n %s", hpa.Name, hpa.Namespace),
					Score: 85, Object: hpa.Name, Namespace: hpa.Namespace,
				})
			}
		}
	}

	if pvcs != nil {
		for _, pvc := range pvcs.Items {
			if pvc.Status.Phase == corev1.ClaimPending {
				age := time.Since(pvc.CreationTimestamp.Time)
				if age > 2*time.Minute {
					findings = append(findings, Finding{
						Severity: SeverityWarning, Category: "predict", Title: "PVC stuck Pending",
						Detail: fmt.Sprintf("pvc=%s pending for %s", pvc.Name, age.Round(time.Second)),
						Remedy: fmt.Sprintf("kubectl describe pvc %s -n %s", pvc.Name, pvc.Namespace),
						Score: 70, Object: pvc.Name, Namespace: pvc.Namespace,
					})
				}
			}
		}
	}

	podsByNode := map[string]int{}
	for _, p := range pods.Items {
		podsByNode[p.Spec.NodeName]++
	}
	for _, node := range nodes.Items {
		maxPods := int64(110)
		if cap, ok := node.Status.Capacity.Pods().AsInt64(); ok {
			maxPods = cap
		}
		count := int64(podsByNode[node.Name])
		pct := float64(count) / float64(maxPods) * 100
		if pct >= 90 {
			findings = append(findings, Finding{
				Severity: SeverityCritical, Category: "predict", Title: "Node near pod capacity",
				Detail: fmt.Sprintf("node=%s pods=%d/%d (%.0f%%)", node.Name, count, maxPods, pct),
				Remedy: "add more nodes or reduce pod density",
				Score: 82, Object: node.Name,
			})
		}
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range pod.Spec.Containers {
			if c.LivenessProbe == nil {
				continue
			}
			p := c.LivenessProbe
			if p.FailureThreshold <= 1 && p.PeriodSeconds <= 5 {
				findings = append(findings, Finding{
					Severity: SeverityWarning, Category: "predict", Title: "Aggressive liveness probe",
					Detail: fmt.Sprintf("container=%s failureThreshold=%d period=%ds", c.Name, p.FailureThreshold, p.PeriodSeconds),
					Remedy: "increase failureThreshold to 3+ and periodSeconds to 10+",
					Score: 50, Object: pod.Name, Namespace: pod.Namespace,
				})
			}
		}
	}

	if len(findings) == 0 {
		findings = append(findings, Finding{
			Severity: SeverityInfo, Category: "predict",
			Title: "No predictive risks detected — cluster looks healthy",
		})
	}
	return findings, nil
}
