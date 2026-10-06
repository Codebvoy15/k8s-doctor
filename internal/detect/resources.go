package detect

import (
	"fmt"
	"strings"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// Shared helpers for the resource detectors (resource-requests, node-saturation,
// cpu-hotspot). They all start from the same fact: the scheduler places pods by
// their *requests*, never by their usage. When usage and requests disagree, nodes
// get packed blind, and that is what node CPU/memory alerts end up reporting.
//
// Usage comes from metrics-server: one recent sample, the numbers `kubectl top`
// shows. Memory is the working set, which includes active page cache (Kafka,
// databases). One sample is enough to prove a gap exists; it is not enough to
// size a workload, so every fix says to confirm against p95/peak history first.

const (
	miB = int64(1) << 20
	giB = int64(1) << 30
)

func fmtMem(b int64) string {
	switch {
	case b <= 0:
		return "none"
	case b >= giB:
		return fmt.Sprintf("%.1fGi", float64(b)/float64(giB))
	}
	return fmt.Sprintf("%dMi", b/miB)
}

func fmtCPU(m int64) string {
	switch {
	case m <= 0:
		return "none"
	case m >= 1000:
		return fmt.Sprintf("%.1f cores", float64(m)/1000)
	}
	return fmt.Sprintf("%dm", m)
}

func pct(used, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(used * 100 / total)
}

func roundUp(v, step int64) int64 {
	if v <= 0 {
		return step
	}
	return (v + step - 1) / step * step
}

// suggestMem: 20% headroom over the observed peak, rounded to 256Mi.
func suggestMem(observed int64) int64 { return roundUp(observed*12/10, 256*miB) }

// suggestCPU: the MEAN usage across replicas, rounded to 100m. Sizing every
// replica for the busiest one would reserve CPU the others never use (and with
// uneven load, the busiest one is the anomaly). CPU is compressible: bursts are
// absorbed by the missing CPU limit and scaled out by the HPA.
func suggestCPU(meanObserved int64) int64 { return roundUp(meanObserved, 100) }

// kubectl quantity syntax
func qtyMem(b int64) string {
	if b%giB == 0 {
		return fmt.Sprintf("%dGi", b/giB)
	}
	return fmt.Sprintf("%dMi", b/miB)
}

func qtyCPU(m int64) string { return fmt.Sprintf("%dm", m) }

// sizing is what a fix proposes. Zero fields are left as they are.
type sizing struct {
	memRequest int64 // also proposed as the memory limit
	cpuRequest int64
}

func (s sizing) yaml() string {
	var req []string
	if s.cpuRequest > 0 {
		req = append(req, "cpu: "+qtyCPU(s.cpuRequest))
	}
	if s.memRequest > 0 {
		req = append(req, "memory: "+qtyMem(s.memRequest))
	}
	out := "requests: {" + strings.Join(req, ", ") + "}"
	if s.memRequest > 0 {
		out += ", limits: {memory: " + qtyMem(s.memRequest) + "}"
	}
	return out
}

// isBuiltinController reports whether a workload's controller is a plain
// Kubernetes controller (then the workload object itself is the place to fix).
func isBuiltinController(kind string) bool {
	switch kind {
	case model.KindDeployment, model.KindStatefulSet, model.KindDaemonSet, "ReplicaSet", "Job", "CronJob":
		return true
	}
	return false
}

// ownerLine describes who owns a workload's pod template, or "" if it is the workload itself.
func ownerLine(st model.Workload, known bool) string {
	if !known {
		return ""
	}
	if c := st.Controller; c != nil && !isBuiltinController(c.Kind) {
		return fmt.Sprintf("pod template owned by %s %s (%s): edits to the %s are reverted by the operator", c.Kind, c.Name, c.APIVersion, strings.ToLower(st.Kind))
	}
	if st.HelmRelease != "" {
		return fmt.Sprintf("deployed by Helm release %q: kubectl edits are overwritten on the next helm upgrade", st.HelmRelease)
	}
	return ""
}

// fixTarget names where a resize must be made, for one table cell.
func fixTarget(st model.Workload, known bool, w model.OwnerRef) string {
	if known {
		if c := st.Controller; c != nil && !isBuiltinController(c.Kind) {
			return c.Kind + " CR " + c.Name
		}
		if st.HelmRelease != "" {
			return "Helm release " + st.HelmRelease
		}
	}
	return w.Kind
}

// resizePlan proposes requests/limits and puts the change where it will stick:
// the operator's custom resource, the Helm values, or the workload itself.
func resizePlan(ix *model.Index, ns string, w model.OwnerRef, sz sizing) *Plan {
	pl := &Plan{}
	add := func(s Step) { pl.Steps = append(pl.Steps, s) }
	add(Step{Description: "Confirm the numbers against p95/peak usage over 7-14 days (Sysdig) before applying; this scan saw a single sample. Proposed starting point: " + sz.yaml()})

	st, known := ix.Workload(ns, w)
	c := st.Controller
	switch {
	case known && c != nil && !isBuiltinController(c.Kind) && c.Group() == "platform.confluent.io":
		add(Step{
			Description: fmt.Sprintf("Set the resources in the Confluent for Kubernetes %s CR %q under spec.podTemplate.resources (CFK owns the %s and reverts direct edits); for Kafka/Connect/ksqlDB also align the JVM heap (-Xmx) with the new limit", c.Kind, c.Name, strings.ToLower(st.Kind)),
			Command:     fmt.Sprintf("kubectl edit %s %s -n %s", strings.ToLower(c.Kind), c.Name, ns),
			Mutating:    true,
		})
	case known && c != nil && !isBuiltinController(c.Kind):
		add(Step{
			Description: fmt.Sprintf("Find where the %s CR %q sets pod resources (the operator owns the %s and reverts direct edits)", c.Kind, c.Name, strings.ToLower(st.Kind)),
			Command:     fmt.Sprintf("kubectl explain %s.spec --api-version=%s --recursive | grep -i -B2 resources", strings.ToLower(c.Kind), c.APIVersion),
		})
		add(Step{Description: fmt.Sprintf("Set the resources in the %s CR %q", c.Kind, c.Name), Command: fmt.Sprintf("kubectl edit %s %s -n %s", strings.ToLower(c.Kind), c.Name, ns), Mutating: true})
	case known && st.HelmRelease != "":
		add(Step{Description: fmt.Sprintf("Read the current values of Helm release %q", st.HelmRelease), Command: fmt.Sprintf("helm get values %s -n %s", st.HelmRelease, ns)})
		add(Step{Description: fmt.Sprintf("Set the resources in the release's values and roll out with helm upgrade (a kubectl edit is overwritten on the next upgrade)"), Mutating: true})
	default:
		args := []string{}
		var req []string
		if sz.cpuRequest > 0 {
			req = append(req, "cpu="+qtyCPU(sz.cpuRequest))
		}
		if sz.memRequest > 0 {
			req = append(req, "memory="+qtyMem(sz.memRequest))
		}
		args = append(args, "--requests="+strings.Join(req, ","))
		if sz.memRequest > 0 {
			args = append(args, "--limits=memory="+qtyMem(sz.memRequest))
		}
		add(Step{
			Description: "Set requests (and a memory limit equal to the request: memory is not compressible) on the workload",
			Command:     fmt.Sprintf("kubectl set resources %s/%s -n %s %s", strings.ToLower(w.Kind), w.Name, ns, strings.Join(args, " ")),
			Mutating:    true,
		})
		add(Step{Description: "Commit the same values to the manifest in Git, or the next deploy reverts them"})
	}
	pl.Verify = []string{fmt.Sprintf("kubectl get pods -n %s -o custom-columns=NAME:.metadata.name,QOS:.status.qosClass | grep %s", ns, w.Name)}
	return pl
}

// usagePod is one pod with its usage and requests, as the resource detectors see it.
type usagePod struct {
	pod                 model.Pod
	cpu, mem            int64 // usage
	reqCPU, reqMem      int64
	limMem              int64
	missingCPU, missMem bool
	allocCPU, allocMem  int64 // allocatable of its node (0 if unknown)
	nodeKnown           bool
}

// runningWithUsage returns running, scheduled, non-Job pods that have both a
// usage sample and recorded requests. Pods without either are skipped: absence
// of data is never treated as a finding.
func runningWithUsage(ix *model.Index) []usagePod {
	var out []usagePod
	for _, p := range ix.S.Pods {
		if p.Phase != "Running" || p.Node == "" || p.DeletingSince != nil || p.Owner.Kind == "Job" {
			continue
		}
		cpu, mem, ok := ix.PodUsage(p.Namespace, p.Name)
		if !ok {
			continue
		}
		rc, rm, mc, mm, recorded := p.Requests()
		if !recorded {
			continue
		}
		up := usagePod{pod: p, cpu: cpu, mem: mem, reqCPU: rc, reqMem: rm, missingCPU: mc, missMem: mm}
		for _, c := range p.Containers {
			if !c.Init && c.Resources != nil {
				up.limMem += c.Resources.LimitMemBytes
			}
		}
		if n, ok := ix.Node(p.Node); ok && n.Allocatable != nil {
			up.allocCPU, up.allocMem, up.nodeKnown = n.Allocatable.CPUMilli, n.Allocatable.MemBytes, true
		}
		out = append(out, up)
	}
	return out
}

func positive(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
