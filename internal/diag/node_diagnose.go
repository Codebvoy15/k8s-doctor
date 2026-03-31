package diag

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeDiagnosis is the full deep diagnosis of a single node
type NodeDiagnosis struct {
	NodeName       string            `json:"node_name"`
	Ready          bool              `json:"ready"`
	NotReadySince  time.Time         `json:"not_ready_since,omitempty"`
	NotReadyFor    string            `json:"not_ready_for,omitempty"`
	Reason         string            `json:"reason"`          // single word: MemoryPressure, DiskPressure, PIDPressure, KubeletDead, EC2Issue, KarpenterInit, Unknown
	RootCause      string            `json:"root_cause"`      // one plain English sentence
	Evidence       []string          `json:"evidence"`        // bullet points of what we found
	Remedy         string            `json:"remedy"`          // exact command to run
	Severity       string            `json:"severity"`        // CRITICAL, WARNING
	MemoryPressure bool              `json:"memory_pressure"`
	DiskPressure   bool              `json:"disk_pressure"`
	PIDPressure    bool              `json:"pid_pressure"`
	Conditions     []NodeCondition   `json:"conditions"`
	TopPods        []NodePodUsage    `json:"top_pods"`
	OOMKilledPods  []string          `json:"oom_killed_pods"`
	RecentEvents   []NodeEvent       `json:"recent_events"`
	IsKarpenter    bool              `json:"is_karpenter"`
	IsSpot         bool              `json:"is_spot"`
	InstanceType   string            `json:"instance_type"`
	AMIFamily      string            `json:"ami_family"`
	KernelVersion  string            `json:"kernel_version"`
	DiskUsage      *NodeDiskUsage    `json:"disk_usage,omitempty"`
}

type NodeCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type NodePodUsage struct {
	PodName      string  `json:"pod_name"`
	Namespace    string  `json:"namespace"`
	CPURequest   string  `json:"cpu_request"`
	MemRequest   string  `json:"mem_request"`
	CPUMillis    int64   `json:"cpu_millis"`
	MemBytes     int64   `json:"mem_bytes"`
	OOMKilled    bool    `json:"oom_killed"`
	Restarts     int32   `json:"restarts"`
}

type NodeEvent struct {
	Time    time.Time `json:"time"`
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	Count   int32     `json:"count"`
}

type NodeDiskUsage struct {
	TotalGB     float64            `json:"total_gb"`
	UsedGB      float64            `json:"used_gb"`
	UsedPercent float64            `json:"used_percent"`
	TopDirs     []DiskDirUsage     `json:"top_dirs"`
}

type DiskDirUsage struct {
	Path  string  `json:"path"`
	SizeGB float64 `json:"size_gb"`
}

// NodeDiagnoseAll runs deep diagnosis on ALL nodes — not just NotReady ones
// It returns full diagnosis for every node so the dashboard can show details
func (e *Engine) NodeDiagnoseAll() ([]NodeDiagnosis, error) {
	nodes, err := e.k8s.CoreV1().Nodes().List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var results []NodeDiagnosis
	for _, node := range nodes.Items {
		diag := e.diagnoseNode(node)
		results = append(results, diag)
	}

	// Sort: NotReady first, then by severity
	sort.Slice(results, func(i, j int) bool {
		if results[i].Ready != results[j].Ready {
			return !results[i].Ready
		}
		return results[i].Severity > results[j].Severity
	})

	return results, nil
}

func (e *Engine) diagnoseNode(node corev1.Node) NodeDiagnosis {
	d := NodeDiagnosis{
		NodeName:      node.Name,
		Ready:         true,
		KernelVersion: node.Status.NodeInfo.KernelVersion,
		InstanceType:  node.Labels["node.kubernetes.io/instance-type"],
		AMIFamily:     node.Labels["karpenter.k8s.aws/instance-ami-id"],
		IsKarpenter:   node.Labels["karpenter.sh/nodepool"] != "",
		IsSpot:        node.Labels["karpenter.sh/capacity-type"] == "spot" || node.Labels["eks.amazonaws.com/capacityType"] == "SPOT",
	}

	// Determine AMI family
	osImage := strings.ToLower(node.Status.NodeInfo.OSImage)
	if strings.Contains(osImage, "amazon linux 2023") || strings.Contains(osImage, "al2023") {
		d.AMIFamily = "AL2023"
	} else if strings.Contains(osImage, "amazon linux 2") {
		d.AMIFamily = "AL2"
	}

	// Read all conditions
	for _, cond := range node.Status.Conditions {
		nc := NodeCondition{
			Type:    string(cond.Type),
			Status:  string(cond.Status),
			Reason:  cond.Reason,
			Message: cond.Message,
		}
		d.Conditions = append(d.Conditions, nc)

		switch cond.Type {
		case corev1.NodeReady:
			if cond.Status != corev1.ConditionTrue {
				d.Ready = false
				d.NotReadySince = cond.LastTransitionTime.Time
				d.NotReadyFor = time.Since(cond.LastTransitionTime.Time).Round(time.Second).String()
			}
		case corev1.NodeMemoryPressure:
			if cond.Status == corev1.ConditionTrue {
				d.MemoryPressure = true
			}
		case corev1.NodeDiskPressure:
			if cond.Status == corev1.ConditionTrue {
				d.DiskPressure = true
			}
		case corev1.NodePIDPressure:
			if cond.Status == corev1.ConditionTrue {
				d.PIDPressure = true
			}
		}
	}

	// Get pods running on this node
	d.TopPods = e.getPodsOnNode(node.Name)

	// Get recent node events
	d.RecentEvents = e.getNodeEvents(node.Name)

	// Get OOMKilled pods on this node
	d.OOMKilledPods = e.getOOMKilledPodsOnNode(node.Name)

	// If ready, just return basic info
	if d.Ready && !d.MemoryPressure && !d.DiskPressure && !d.PIDPressure {
		d.Reason = "Healthy"
		d.Severity = "OK"
		return d
	}

	// Deep diagnosis based on what we found
	e.buildDiagnosis(&d, node)

	return d
}

func (e *Engine) buildDiagnosis(d *NodeDiagnosis, node corev1.Node) {
	d.Severity = "CRITICAL"

	// ── MEMORY PRESSURE ───────────────────────────────────────────────────────
	if d.MemoryPressure {
		d.Reason = "MemoryPressure"

		// Find the biggest memory consumer on this node
		topConsumer := ""
		var topMem int64
		for _, pod := range d.TopPods {
			if pod.MemBytes > topMem {
				topMem = pod.MemBytes
				topConsumer = pod.Namespace + "/" + pod.PodName
			}
		}

		// Check for OOMKilled
		if len(d.OOMKilledPods) > 0 {
			d.RootCause = fmt.Sprintf("Node %s went NotReady due to memory exhaustion — OOM killer fired on %d pod(s). Biggest offender: %s using %s",
				d.NodeName, len(d.OOMKilledPods), d.OOMKilledPods[0], formatBytes(topMem))
			d.Evidence = append(d.Evidence, fmt.Sprintf("OOM killed pods: %s", strings.Join(d.OOMKilledPods, ", ")))
		} else if topConsumer != "" {
			d.RootCause = fmt.Sprintf("Node %s has MemoryPressure — top consumer is %s using %s of memory",
				d.NodeName, topConsumer, formatBytes(topMem))
		} else {
			d.RootCause = fmt.Sprintf("Node %s has MemoryPressure — no resource limits on pods is likely cause", d.NodeName)
			d.Evidence = append(d.Evidence, "Check pods without memory limits: kubectl describe node "+d.NodeName)
		}

		// Add pod evidence
		for i, pod := range d.TopPods {
			if i >= 3 {
				break
			}
			d.Evidence = append(d.Evidence, fmt.Sprintf("Pod %s/%s: mem_request=%s", pod.Namespace, pod.PodName, pod.MemRequest))
		}

		d.Remedy = fmt.Sprintf("kubectl top pods --all-namespaces --sort-by=memory | grep -v kube-system | head -10\n# Then evict the top consumer:\nkubectl delete pod <pod-name> -n <namespace>")
		return
	}

	// ── DISK PRESSURE ─────────────────────────────────────────────────────────
	if d.DiskPressure {
		d.Reason = "DiskPressure"

		// Try to get disk usage via kubectl exec into a pod on that node
		diskInfo := e.getDiskUsageOnNode(node.Name)
		if diskInfo != nil {
			d.DiskUsage = diskInfo

			// Find the biggest disk consumer
			topDir := ""
			if len(diskInfo.TopDirs) > 0 {
				topDir = fmt.Sprintf("%s (%.1fGB)", diskInfo.TopDirs[0].Path, diskInfo.TopDirs[0].SizeGB)
			}

			d.RootCause = fmt.Sprintf("Node %s has DiskPressure — disk is %.0f%% full (%.1f/%.1fGB used). Largest consumer: %s",
				d.NodeName, diskInfo.UsedPercent, diskInfo.UsedGB, diskInfo.TotalGB, topDir)

			for _, dir := range diskInfo.TopDirs {
				d.Evidence = append(d.Evidence, fmt.Sprintf("%s: %.1fGB", dir.Path, dir.SizeGB))
			}

			// Specific advice based on what's big
			if len(diskInfo.TopDirs) > 0 {
				bigDir := diskInfo.TopDirs[0].Path
				if strings.Contains(bigDir, "containers") || strings.Contains(bigDir, "overlay") {
					d.Remedy = "# Container image cache is full — clean it:\ncrictl rmi --prune\n# Or on the node:\nssh " + d.NodeName + " 'sudo crictl rmi --prune'"
				} else if strings.Contains(bigDir, "log") {
					d.Remedy = "# Logs are filling disk — clean them:\nssh " + d.NodeName + " 'sudo journalctl --vacuum-size=500M && sudo find /var/log/pods -name \"*.log\" -mtime +1 -delete'"
				} else {
					d.Remedy = "# Check disk usage on node:\nkubectl debug node/" + d.NodeName + " -it --image=busybox -- df -h\nkubectl debug node/" + d.NodeName + " -it --image=busybox -- du -sh /host/var/log/* 2>/dev/null | sort -rh | head -10"
				}
			}
		} else {
			d.RootCause = fmt.Sprintf("Node %s has DiskPressure — unable to get detailed disk info. Check /var/log and container image cache.", d.NodeName)
			d.Remedy = "kubectl debug node/" + d.NodeName + " -it --image=busybox -- df -h"
		}

		d.Evidence = append(d.Evidence, "Check container logs: kubectl debug node/"+d.NodeName+" -it --image=busybox -- du -sh /host/var/log/pods/* 2>/dev/null | sort -rh | head -5")
		return
	}

	// ── PID PRESSURE ──────────────────────────────────────────────────────────
	if d.PIDPressure {
		d.Reason = "PIDPressure"
		d.RootCause = fmt.Sprintf("Node %s has PIDPressure — too many processes running. Likely a fork bomb or thread leak in a pod.", d.NodeName)
		d.Evidence = append(d.Evidence, "Check which pod has most processes")

		// Find pod with most restarts — likely the leaker
		var maxRestarts int32
		leakyPod := ""
		for _, pod := range d.TopPods {
			if pod.Restarts > maxRestarts {
				maxRestarts = pod.Restarts
				leakyPod = pod.Namespace + "/" + pod.PodName
			}
		}
		if leakyPod != "" {
			d.Evidence = append(d.Evidence, fmt.Sprintf("Highest restarts on this node: %s (%d restarts) — likely the source", leakyPod, maxRestarts))
		}

		d.Remedy = "kubectl debug node/" + d.NodeName + " -it --image=busybox -- cat /host/proc/sys/kernel/pid_max\n# Kill the leaky pod:\nkubectl delete pod <pod-name> -n <namespace> --force"
		return
	}

	// ── NODE NOT READY — no pressure conditions ───────────────────────────────
	// This means kubelet is dead or EC2 has an issue

	// Check events for clues
	for _, ev := range d.RecentEvents {
		msg := strings.ToLower(ev.Message)

		// Karpenter node failed to initialize
		if d.IsKarpenter && (strings.Contains(msg, "not yet registered") || strings.Contains(msg, "failed to initialize")) {
			d.Reason = "KarpenterInit"
			d.RootCause = fmt.Sprintf("Karpenter node %s failed to initialize — NodeClaim was created but node never became Ready. Likely an EC2NodeClass misconfiguration or VPC issue.", d.NodeName)
			d.Evidence = append(d.Evidence, "Event: "+ev.Message)
			d.Remedy = "kubectl get nodeclaims -o wide\nkubectl describe nodeclaim <name>\nkubectl logs -n kube-system -l app.kubernetes.io/name=karpenter | tail -50"
			return
		}

		// Spot interruption
		if d.IsSpot && (strings.Contains(msg, "spot") || strings.Contains(msg, "interrupted") || strings.Contains(msg, "terminating")) {
			d.Reason = "SpotInterruption"
			d.RootCause = fmt.Sprintf("Spot node %s (%s) was interrupted by AWS — this is expected behavior. Karpenter will provision a replacement.", d.NodeName, d.InstanceType)
			d.Evidence = append(d.Evidence, "Spot interruption detected in node events")
			d.Remedy = "# No action needed — Karpenter auto-replaces spot nodes\n# Check replacement is happening:\nkubectl get nodeclaims -o wide"
			d.Severity = "WARNING"
			return
		}

		// Network issue
		if strings.Contains(msg, "network") || strings.Contains(msg, "timeout") || strings.Contains(msg, "connection") {
			d.Reason = "NetworkIssue"
			d.RootCause = fmt.Sprintf("Node %s went NotReady due to network connectivity loss — kubelet cannot reach the API server. Check VPC/subnet routing and security groups.", d.NodeName)
			d.Evidence = append(d.Evidence, "Event: "+ev.Message)
			d.Remedy = "aws ec2 describe-instance-status --instance-ids <id> --query 'InstanceStatuses[].{Status:InstanceStatus.Status,System:SystemStatus.Status}'"
			return
		}
	}

	// AL2023 cgroup v2 issue — kernel version check
	if d.AMIFamily == "AL2023" && strings.HasPrefix(d.KernelVersion, "6.") {
		// Check if this is a recent node (might have just upgraded)
		d.Reason = "KubeletDead"
		d.RootCause = fmt.Sprintf("Node %s (AL2023, kernel %s) is NotReady — kubelet may have crashed. AL2023 uses cgroup v2 which can cause issues with some kubelet configs or monitoring agents.", d.NodeName, d.KernelVersion)
		d.Evidence = append(d.Evidence, "AL2023 node with cgroup v2")
		d.Evidence = append(d.Evidence, "Check kubelet logs: journalctl -u kubelet -n 100 --no-pager")
		d.Remedy = "# Check kubelet status on node:\nkubectl debug node/" + d.NodeName + " -it --image=ubuntu -- bash\n# Inside: systemctl status kubelet && journalctl -u kubelet -n 50"
		return
	}

	// Karpenter node — generic
	if d.IsKarpenter {
		d.Reason = "KarpenterInit"
		d.RootCause = fmt.Sprintf("Karpenter node %s (%s) is NotReady — provisioned but kubelet never reported healthy. Check EC2NodeClass and VPC CNI.", d.NodeName, d.InstanceType)
		d.Evidence = append(d.Evidence, "Node managed by Karpenter")
		if d.IsSpot {
			d.Evidence = append(d.Evidence, "Running on SPOT instance — may have been interrupted")
		}
		d.Remedy = "kubectl get nodeclaims\nkubectl describe nodeclaim -l karpenter.sh/node-name=" + d.NodeName + "\nkubectl logs -n kube-system -l app.kubernetes.io/name=karpenter --tail=100"
		return
	}

	// Generic — kubelet dead, no specific clue
	d.Reason = "KubeletDead"
	d.RootCause = fmt.Sprintf("Node %s is NotReady — kubelet stopped responding to the API server. This is usually a kubelet crash, EC2 hardware issue, or network partition.", d.NodeName)
	d.Evidence = append(d.Evidence, "No pressure conditions — kubelet likely crashed or EC2 is failing")
	d.Remedy = fmt.Sprintf("# Check EC2 instance health:\naws ec2 describe-instance-status --filters Name=private-dns-name,Values=%s\n# Check kubelet:\nkubectl debug node/%s -it --image=ubuntu -- journalctl -u kubelet -n 100 --no-pager", d.NodeName, d.NodeName)
}

// ── HELPERS ───────────────────────────────────────────────────────────────────

func (e *Engine) getPodsOnNode(nodeName string) []NodePodUsage {
	pods, err := e.k8s.CoreV1().Pods("").List(e.ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return nil
	}

	var usage []NodePodUsage
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
			continue
		}

		pu := NodePodUsage{
			PodName:   pod.Name,
			Namespace: pod.Namespace,
		}

		// Sum resource requests across containers
		var cpuMillis, memBytes int64
		for _, c := range pod.Spec.Containers {
			cpuMillis += c.Resources.Requests.Cpu().MilliValue()
			memBytes += c.Resources.Requests.Memory().Value()
		}
		pu.CPUMillis = cpuMillis
		pu.MemBytes = memBytes
		pu.CPURequest = fmt.Sprintf("%dm", cpuMillis)
		pu.MemRequest = formatBytes(memBytes)

		// Check for OOMKilled and restarts
		for _, cs := range pod.Status.ContainerStatuses {
			pu.Restarts += cs.RestartCount
			if cs.LastTerminationState.Terminated != nil &&
				cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
				pu.OOMKilled = true
			}
		}

		usage = append(usage, pu)
	}

	// Sort by memory descending
	sort.Slice(usage, func(i, j int) bool {
		return usage[i].MemBytes > usage[j].MemBytes
	})

	// Return top 10
	if len(usage) > 10 {
		return usage[:10]
	}
	return usage
}

func (e *Engine) getOOMKilledPodsOnNode(nodeName string) []string {
	pods, err := e.k8s.CoreV1().Pods("").List(e.ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return nil
	}

	var oomPods []string
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.LastTerminationState.Terminated != nil &&
				cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
				oomPods = append(oomPods, pod.Namespace+"/"+pod.Name)
				break
			}
		}
	}
	return oomPods
}

func (e *Engine) getNodeEvents(nodeName string) []NodeEvent {
	events, err := e.k8s.CoreV1().Events("").List(e.ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Node", nodeName),
	})
	if err != nil {
		return nil
	}

	cutoff := time.Now().Add(-2 * time.Hour)
	var nodeEvents []NodeEvent
	for _, ev := range events.Items {
		t := ev.LastTimestamp.Time
		if t.Before(cutoff) {
			continue
		}
		nodeEvents = append(nodeEvents, NodeEvent{
			Time:    t,
			Reason:  ev.Reason,
			Message: truncate(ev.Message, 200),
			Count:   ev.Count,
		})
	}

	// Sort newest first
	sort.Slice(nodeEvents, func(i, j int) bool {
		return nodeEvents[i].Time.After(nodeEvents[j].Time)
	})

	if len(nodeEvents) > 10 {
		return nodeEvents[:10]
	}
	return nodeEvents
}

func (e *Engine) getDiskUsageOnNode(nodeName string) *NodeDiskUsage {
	// Try kubectl debug node — works on EKS
	out, err := exec.CommandContext(e.ctx, "kubectl", "debug", "node/"+nodeName,
		"-it", "--image=busybox",
		"--", "df", "-h", "/host").Output()
	if err != nil {
		// Fallback: try exec into a running pod on that node
		return e.getDiskViaNodePod(nodeName)
	}

	// Parse df output
	// Filesystem      Size  Used Avail Use% Mounted on
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "/host") || strings.Contains(line, "overlay") {
			fields := strings.Fields(line)
			if len(fields) >= 5 {
				du := &NodeDiskUsage{}
				fmt.Sscanf(strings.TrimSuffix(fields[4], "%"), "%f", &du.UsedPercent)
				du.TotalGB = parseStorageGB(fields[1])
				du.UsedGB = parseStorageGB(fields[2])
				du.TopDirs = e.getTopDirsOnNode(nodeName)
				return du
			}
		}
	}
	return nil
}

func (e *Engine) getDiskViaNodePod(nodeName string) *NodeDiskUsage {
	// Find a running pod on this node to exec into
	pods, err := e.k8s.CoreV1().Pods("kube-system").List(e.ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName + ",status.phase=Running",
	})
	if err != nil || len(pods.Items) == 0 {
		return nil
	}

	pod := pods.Items[0]
	out, err := exec.CommandContext(e.ctx, "kubectl", "exec", "-n", "kube-system",
		pod.Name, "--", "df", "-h", "/").Output()
	if err != nil {
		return nil
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "/") {
			fields := strings.Fields(line)
			if len(fields) >= 5 {
				du := &NodeDiskUsage{}
				fmt.Sscanf(strings.TrimSuffix(fields[4], "%"), "%f", &du.UsedPercent)
				du.TotalGB = parseStorageGB(fields[1])
				du.UsedGB = parseStorageGB(fields[2])
				return du
			}
		}
	}
	return nil
}

func (e *Engine) getTopDirsOnNode(nodeName string) []DiskDirUsage {
	out, err := exec.CommandContext(e.ctx, "kubectl", "debug", "node/"+nodeName,
		"-it", "--image=busybox",
		"--", "du", "-sh", "/host/var/log", "/host/var/lib/containers",
		"/host/var/lib/kubelet", "/host/tmp").Output()
	if err != nil {
		return nil
	}

	var dirs []DiskDirUsage
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		path := strings.TrimPrefix(fields[1], "/host")
		dirs = append(dirs, DiskDirUsage{
			Path:   path,
			SizeGB: parseStorageGB(fields[0]),
		})
	}

	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].SizeGB > dirs[j].SizeGB
	})

	return dirs
}

func parseStorageGB(s string) float64 {
	s = strings.TrimSpace(s)
	var val float64
	if strings.HasSuffix(s, "G") || strings.HasSuffix(s, "Gi") {
		fmt.Sscanf(strings.TrimRight(s, "GiB"), "%f", &val)
	} else if strings.HasSuffix(s, "M") || strings.HasSuffix(s, "Mi") {
		fmt.Sscanf(strings.TrimRight(s, "MiB"), "%f", &val)
		val = val / 1024
	} else if strings.HasSuffix(s, "T") {
		fmt.Sscanf(strings.TrimRight(s, "T"), "%f", &val)
		val = val * 1024
	}
	return val
}
