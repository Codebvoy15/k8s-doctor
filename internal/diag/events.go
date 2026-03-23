package diag

import (
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type EventEntry struct {
	LastSeen   time.Time
	Type       string
	Reason     string
	ObjectKind string
	ObjectName string
	Namespace  string
	Message    string
	Count      int32
}

func (e *Engine) EventTimeline(window time.Duration, filterKind string, warningOnly bool) ([]EventEntry, error) {
	ns := e.ns()
	cutoff := time.Now().Add(-window)
	fieldSelector := ""
	if warningOnly {
		fieldSelector = "type=Warning"
	}
	events, err := e.k8s.CoreV1().Events(ns).List(e.ctx, metav1.ListOptions{FieldSelector: fieldSelector})
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}
	seen := map[string]bool{}
	var entries []EventEntry
	for _, ev := range events.Items {
		t := ev.LastTimestamp.Time
		if t.IsZero() {
			t = ev.EventTime.Time
		}
		if t.IsZero() || t.Before(cutoff) {
			continue
		}
		if filterKind != "" && !strings.EqualFold(ev.InvolvedObject.Kind, filterKind) {
			continue
		}
		key := fmt.Sprintf("%s/%s/%s", ev.Reason, ev.InvolvedObject.Name, ev.Namespace)
		if seen[key] {
			for i, entry := range entries {
				if entry.Reason == ev.Reason && entry.ObjectName == ev.InvolvedObject.Name {
					entries[i].Count += ev.Count
					break
				}
			}
			continue
		}
		seen[key] = true
		entries = append(entries, EventEntry{
			LastSeen: t, Type: ev.Type, Reason: ev.Reason,
			ObjectKind: ev.InvolvedObject.Kind, ObjectName: ev.InvolvedObject.Name,
			Namespace: ev.Namespace, Message: ev.Message, Count: ev.Count,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].LastSeen.After(entries[j].LastSeen) })
	return entries, nil
}
