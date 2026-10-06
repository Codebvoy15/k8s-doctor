package detect

import (
	"strings"
)

// Explain narrows a report to the findings matching query: a finding ID, the
// name of its subject (workload, node, secret...), or one of its affected pods.
// Symptoms a match explains are kept so they print nested under it. A matched
// symptom is shown on its own, with its causes named in its evidence. ok=false
// when nothing matches.
func Explain(r Report, query string) (Report, bool) {
	q := strings.TrimSpace(query)
	if q == "" {
		return r, true
	}
	matches := func(f Finding) bool {
		if f.ID == q || f.Subject.Name == q || shortNode(f.Subject.Name) == q {
			return true
		}
		for _, a := range f.Affected {
			if a.Name == q {
				return true
			}
		}
		return false
	}
	byID := map[string]Finding{}
	for _, f := range r.Findings {
		byID[f.ID] = f
	}
	keep := map[string]bool{}
	var out []Finding
	for _, f := range r.Findings {
		if !matches(f) {
			continue
		}
		if len(f.CausedBy) > 0 {
			var names []string
			for _, id := range f.CausedBy {
				if c, ok := byID[id]; ok {
					names = append(names, c.Subject.Namespace+"/"+c.Subject.Name)
				}
			}
			f.Evidence = append(append([]string(nil), f.Evidence...), "caused by: "+strings.Join(names, ", "))
			f.CausedBy = nil // print on its own
		}
		keep[f.ID] = true
		out = append(out, f)
	}
	if len(out) == 0 {
		return Report{}, false
	}
	for _, f := range out {
		for _, id := range f.Explains {
			if s, ok := byID[id]; ok && !keep[id] {
				keep[id] = true
				out = append(out, s)
			}
		}
	}
	r.Findings = out
	r.Counts = map[Tier]int{}
	for _, f := range out {
		r.Counts[f.Tier]++
	}
	return r, true
}

// shortNode turns ip-10-0-1-11.ec2.internal into ip-10-0-1-11, so a node can be
// named the way tables print it.
func shortNode(name string) string {
	if i := strings.IndexByte(name, '.'); i > 0 && strings.HasPrefix(name, "ip-") {
		return name[:i]
	}
	return name
}
