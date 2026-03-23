package diag

import (
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RollbackTarget struct {
	Name        string
	Namespace   string
	ChangedBy   string
	ChangedAt   time.Time
	ImageChange string
	Generation  int64
}

func (e *Engine) RollbackTargets(filterNS string) ([]RollbackTarget, error) {
	ns := filterNS
	if ns == "" {
		ns = e.ns()
	}
	deploys, err := e.k8s.AppsV1().Deployments(ns).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing deployments: %w", err)
	}
	var targets []RollbackTarget
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, d := range deploys.Items {
		var latestFM string
		var latestTime time.Time
		for _, mf := range d.ManagedFields {
			if mf.Time != nil && mf.Time.Time.After(cutoff) && mf.Time.Time.After(latestTime) {
				latestTime = mf.Time.Time
				latestFM = mf.Manager
			}
		}
		if latestTime.IsZero() {
			continue
		}
		imageChange := ""
		for _, c := range d.Spec.Template.Spec.Containers {
			imageChange = fmt.Sprintf("%s: %s", c.Name, c.Image)
			break
		}
		targets = append(targets, RollbackTarget{
			Name: d.Name, Namespace: d.Namespace, ChangedBy: latestFM,
			ChangedAt: latestTime, ImageChange: imageChange, Generation: d.Generation,
		})
	}
	for i := 1; i < len(targets); i++ {
		for j := i; j > 0 && targets[j].ChangedAt.After(targets[j-1].ChangedAt); j-- {
			targets[j], targets[j-1] = targets[j-1], targets[j]
		}
	}
	_ = strings.Contains
	return targets, nil
}
