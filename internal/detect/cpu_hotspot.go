package detect

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Codebvoy15/k8s-doctor/internal/model"
)

// CPUHotspot reports workloads where a single replica takes most of its node's
// CPU. That node then sits just under the alert threshold and every traffic
// spike crosses it: the "intermittent high CPU, cleared on its own" pattern.
//
// The finding is about scaling, not sizing: whether anything scales the
// workload out (HPA), whether load is spread evenly across replicas, and
// whether the replicas are restarting under it.
//
// LATENT when nothing can absorb more load (no HPA, or the HPA is at max);
// INFO when an HPA exists with headroom (it may need a lower target).
type CPUHotspot struct{}

func (CPUHotspot) ID() string { return "cpu-hotspot" }
func (CPUHotspot) Description() string {
	return "Workloads where one replica takes most of a node's CPU, with HPA and replica-balance context"
}

const (
	hotCPU       = 1000 // millicores used by one replica
	hotNodeShare = 50   // percent of its node's allocatable CPU
	// replicas are "uneven" when the busiest uses this many times the quietest...
	imbalanceFactor = 2
	// ...and the difference is at least this much CPU (millicores)
	imbalanceMin = 500
)

func (d CPUHotspot) Detect(ix *model.Index) []Finding {
	type grp struct {
		ns   string
		ref  model.OwnerRef
		pods []usagePod
	}
	groups := map[string]*grp{}
	var order []string
	for _, up := range runningWithUsage(ix) {
		w := up.pod.Workload()
		if w.Kind != model.KindDeployment && w.Kind != model.KindStatefulSet {
			continue // DaemonSets cannot scale out; bare pods have nothing to scale
		}
		k := up.pod.Namespace + "|" + w.Kind + "|" + w.Name
		if groups[k] == nil {
			groups[k] = &grp{ns: up.pod.Namespace, ref: w}
			order = append(order, k)
		}
		groups[k].pods = append(groups[k].pods, up)
	}
	var out []Finding
	for _, k := range order {
		g := groups[k]
		sort.SliceStable(g.pods, func(i, j int) bool { return g.pods[i].cpu > g.pods[j].cpu })
		hot := g.pods[0]
		if hot.cpu < hotCPU || !hot.nodeKnown || pct(hot.cpu, hot.allocCPU) < hotNodeShare {
			continue
		}
		out = append(out, d.finding(ix, g.ns, g.ref, g.pods))
	}
	return out
}

func (d CPUHotspot) finding(ix *model.Index, ns string, w model.OwnerRef, pods []usagePod) Finding {
	now := ix.S.CapturedAt
	hot := pods[0]
	share := pct(hot.cpu, hot.allocCPU)
	h, found, known := ix.HPA(ns, w)

	tier := TierInfo
	var scaling, scalingTitle string
	switch {
	case !known:
		scaling = "HPAs were not collected, so whether it autoscales is unknown."
		scalingTitle = "HPA unknown"
	case !found:
		tier = TierLatent
		scaling = "No HPA targets it, so nothing scales it out when load rises: the existing replicas absorb all of it."
		scalingTitle = "no HPA"
	case h.Current >= h.Max:
		tier = TierLatent
		scaling = fmt.Sprintf("HPA %s is at its maximum (%d/%d): it cannot add replicas.", h.Name, h.Current, h.Max)
		scalingTitle = fmt.Sprintf("HPA at max %d/%d", h.Current, h.Max)
	default:
		scaling = fmt.Sprintf("HPA %s scales it between %d and %d (now %d) and still has headroom; its target may be set too high for this load.", h.Name, h.Min, h.Max, h.Current)
		scalingTitle = fmt.Sprintf("HPA %d-%d", h.Min, h.Max)
	}

	summary := []string{fmt.Sprintf("One replica uses %s, %d%% of its node's CPU, so that node sits near the alert threshold and every load spike crosses it.", fmtCPU(hot.cpu), share), scaling}
	var ev []string
	var affected []ObjectRef
	var contrib []string
	for i, u := range pods {
		affected = append(affected, podRef(u.pod))
		if u.nodeKnown && pct(u.cpu, u.allocCPU) >= hotNodeShare {
			contrib = append(contrib, "cpu:"+u.pod.Namespace+"/"+u.pod.Name)
		}
		if i < 4 {
			node := u.pod.Node
			if u.nodeKnown {
				node += fmt.Sprintf(", %d%% of its CPU", pct(u.cpu, u.allocCPU))
			}
			ev = append(ev, fmt.Sprintf("%s [node %s]: cpu %s (requested %s)", u.pod.Name, node, fmtCPU(u.cpu), fmtCPU(u.reqCPU)))
		}
	}
	uneven := false
	if len(pods) >= 2 {
		lo := pods[len(pods)-1]
		if hot.cpu >= lo.cpu*imbalanceFactor && hot.cpu-lo.cpu >= imbalanceMin {
			uneven = true
			ratio := "all"
			if lo.cpu > 0 {
				ratio = fmt.Sprintf("%.1fx", float64(hot.cpu)/float64(lo.cpu))
			}
			ev = append(ev, fmt.Sprintf("replica load is uneven: busiest %s vs quietest %s (%s)", fmtCPU(hot.cpu), fmtCPU(lo.cpu), ratio))
			summary = append(summary, "Load is uneven across replicas, which points at long-lived connections pinning clients to one pod, or uneven work distribution.")
		}
	}
	if hot.reqCPU == 0 {
		ev = append(ev, "no CPU request: the scheduler cannot spread replicas by CPU, and an HPA has no utilization to scale on")
	}
	if line := restartLine(now, pods); line != "" {
		ev = append(ev, line)
		summary = append(summary, "Replicas also restart regularly; check whether they fail liveness probes while CPU-starved.")
	}

	pl := &Plan{}
	add := func(s Step) { pl.Steps = append(pl.Steps, s) }
	add(Step{Description: "See per-replica CPU now", Command: fmt.Sprintf("kubectl top pods -n %s --sort-by=cpu | grep %s", ns, w.Name)})
	if hot.reqCPU == 0 {
		add(Step{Description: "Set a CPU request near observed usage first (see the resource-requests finding): HPA utilization is measured against the request"})
	}
	if known && !found {
		replicas := int32(len(pods))
		add(Step{
			Description: "Add an HPA so the workload scales out under load (after CPU requests are set)",
			Command:     fmt.Sprintf("kubectl autoscale %s %s -n %s --cpu-percent=70 --min=%d --max=%d", strings.ToLower(w.Kind), w.Name, ns, replicas, replicas*3),
			Mutating:    true,
		})
	} else if found && h.Current >= h.Max {
		add(Step{Description: fmt.Sprintf("Raise maxReplicas on HPA %s, or move the workload to larger nodes", h.Name), Mutating: true})
	}
	if uneven {
		add(Step{Description: "Check how traffic reaches the replicas: keep-alive/HTTP2/gRPC connections pin clients to one pod; use an L7 balancer or a connection max-age"})
	}
	add(Step{Description: "Spread replicas across nodes with topologySpreadConstraints (maxSkew 1 on kubernetes.io/hostname)"})
	if line := restartLine(now, pods); line != "" {
		add(Step{Description: "Find why replicas restart (OOMKilled, liveness probe, or error)", Command: fmt.Sprintf("kubectl describe pod %s -n %s | grep -A6 'Last State'", hot.pod.Name, ns)})
	}
	pl.Verify = []string{fmt.Sprintf("kubectl top pods -n %s | grep %s", ns, w.Name)}

	notes := []string{scalingTitle}
	if uneven {
		notes = append(notes, fmt.Sprintf("%.1fx uneven", float64(hot.cpu)/float64(max64(pods[len(pods)-1].cpu, 1))))
	}
	if r := totalRestarts(pods); r >= 5 {
		notes = append(notes, fmt.Sprintf("%d restarts", r))
	}

	return Finding{
		ID:          Fingerprint(d.ID(), ix.S.Cluster, ns, w.Kind, w.Name),
		Tier:        tier,
		Title:       fmt.Sprintf("%s %s/%s: one replica uses %s (%d%% of node %s), %s", w.Kind, ns, w.Name, fmtCPU(hot.cpu), share, hot.pod.Node, scalingTitle),
		Summary:     strings.Join(summary, " "),
		Subject:     ObjectRef{Kind: w.Kind, Namespace: ns, Name: w.Name},
		Affected:    affected,
		Evidence:    ev,
		Remediation: pl,
		PatternKey:  "cpu-hotspot|" + w.Kind + "|" + w.Name,
		Measures:    []Measure{{Resource: "cpu", Used: hot.cpu, Requested: hot.reqCPU, Capacity: hot.allocCPU}},
		Note:        strings.Join(notes, " · "),
		contrib:     contrib,
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func totalRestarts(pods []usagePod) int32 {
	var total int32
	for _, u := range pods {
		for _, c := range u.pod.Containers {
			if !c.Init {
				total += c.Restarts
			}
		}
	}
	return total
}

// restartLine summarizes restarts across replicas, or "" if there are none worth noting.
func restartLine(now time.Time, pods []usagePod) string {
	var total int32
	var last *time.Time
	reason := ""
	for _, u := range pods {
		for _, c := range u.pod.Containers {
			if c.Init {
				continue
			}
			total += c.Restarts
			if c.LastTermAt != nil && (last == nil || c.LastTermAt.After(*last)) {
				last = c.LastTermAt
				reason = c.LastTermReason
			}
		}
	}
	if total < 5 {
		return ""
	}
	s := fmt.Sprintf("replicas restarted %d times in total", total)
	if last != nil {
		s += fmt.Sprintf(", last %s ago", humanDuration(now.Sub(*last)))
		if reason != "" {
			s += " (" + reason + ")"
		}
	}
	return s
}
