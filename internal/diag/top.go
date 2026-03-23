package diag

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

type TopResult struct {
	Nodes           []NodeMetric
	Pods            []PodMetric
	NoisyNeighbours []NoisyNeighbour
}

type PodMetric struct {
	Name      string
	Namespace string
	CPUUsage  string
	MemUsage  string
	CPUMillis int64
	MemMi     int64
}

type NoisyNeighbour struct {
	PodName   string
	Namespace string
	CPUUsage  string
	MemUsage  string
}

func (e *Engine) TopConsumers(sortBy string, limit int) (*TopResult, error) {
	result := &TopResult{}
	nodeOut, err := exec.CommandContext(e.ctx, "kubectl", "top", "nodes", "--no-headers").Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl top nodes failed (is metrics-server running?): %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(nodeOut)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		result.Nodes = append(result.Nodes, NodeMetric{
			Name:       fields[0],
			CPUUsage:   fields[1],
			CPUPercent: parsePercent(fields[2]),
			MemUsage:   fields[3],
			MemPercent: parsePercent(fields[4]),
		})
	}
	args := []string{"top", "pods", "--no-headers", "--all-namespaces"}
	if e.namespace != "" {
		args = []string{"top", "pods", "--no-headers", "-n", e.namespace}
	}
	podOut, err := exec.CommandContext(e.ctx, "kubectl", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl top pods failed: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(podOut)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		var pm PodMetric
		if e.namespace == "" && len(fields) >= 4 {
			pm = PodMetric{Namespace: fields[0], Name: fields[1], CPUUsage: fields[2], MemUsage: fields[3]}
		} else {
			pm = PodMetric{Namespace: e.namespace, Name: fields[0], CPUUsage: fields[1], MemUsage: fields[2]}
		}
		pm.CPUMillis = parseCPUMillis(pm.CPUUsage)
		pm.MemMi = parseMemMi(pm.MemUsage)
		result.Pods = append(result.Pods, pm)
	}
	if sortBy == "cpu" {
		sort.Slice(result.Pods, func(i, j int) bool { return result.Pods[i].CPUMillis > result.Pods[j].CPUMillis })
	} else {
		sort.Slice(result.Pods, func(i, j int) bool { return result.Pods[i].MemMi > result.Pods[j].MemMi })
	}
	for i, p := range result.Pods {
		if i >= 3 {
			break
		}
		if p.CPUMillis > 500 || p.MemMi > 512 {
			result.NoisyNeighbours = append(result.NoisyNeighbours, NoisyNeighbour{
				PodName: p.Name, Namespace: p.Namespace, CPUUsage: p.CPUUsage, MemUsage: p.MemUsage,
			})
		}
	}
	return result, nil
}

func parseCPUMillis(s string) int64 {
	s = strings.TrimSuffix(s, "m")
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}

func parseMemMi(s string) int64 {
	s = strings.TrimSuffix(s, "Mi")
	s = strings.TrimSuffix(s, "Gi")
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}
