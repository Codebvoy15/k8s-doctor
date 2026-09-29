package detect

import "sort"

// correlate links symptom findings to the findings that explain them, using
// the pods they share. Causes are findings about a dependency (missing
// Secret/ConfigMap/PVC, unhealthy node); symptoms are workload findings.
//
// This is deliberately simple and explainable: a workload finding is
// "caused by" a dependency finding when at least one of its unavailable pods is
// blocked by that dependency. A full resource graph (service -> endpoints ->
// pods, owner chains) comes later and plugs in here.
func correlate(fs []Finding) {
	causeByPod := map[string][]int{}
	for i, f := range fs {
		if !isCauseDetector(f.Detector) {
			continue
		}
		// only pods the cause provably blocks count; merely referencing is not enough
		for _, a := range f.blocks {
			causeByPod[a.Namespace+"/"+a.Name] = append(causeByPod[a.Namespace+"/"+a.Name], i)
		}
	}
	for i := range fs {
		if fs[i].Detector != "workload-unavailable" {
			continue
		}
		linked := map[int]bool{}
		for _, a := range fs[i].Affected {
			for _, ci := range causeByPod[a.Namespace+"/"+a.Name] {
				linked[ci] = true
			}
		}
		var idx []int
		for ci := range linked {
			idx = append(idx, ci)
		}
		sort.Ints(idx)
		for _, ci := range idx {
			fs[i].CausedBy = append(fs[i].CausedBy, fs[ci].ID)
			fs[ci].Explains = append(fs[ci].Explains, fs[i].ID)
			// the cause inherits the symptom's impact: a missing secret that
			// takes a whole workload down is itself IMPACTING
			if fs[i].Tier.Rank() < fs[ci].Tier.Rank() {
				fs[ci].Tier = fs[i].Tier
			}
		}
	}
}

func isCauseDetector(id string) bool {
	return id == "missing-reference" || id == "node-unhealthy"
}
