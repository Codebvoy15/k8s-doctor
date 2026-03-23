package diag

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

type WatchEvent struct {
	Timestamp    time.Time
	EventType    string
	Kind         string
	Name         string
	Namespace    string
	FieldManager string
}

func (e *Engine) WatchResources(kinds []string) (<-chan WatchEvent, error) {
	ch := make(chan WatchEvent, 100)
	watchAll := len(kinds) == 0
	wants := map[string]bool{}
	for _, k := range kinds {
		wants[k] = true
	}
	ns := e.namespace
	if ns == "" {
		ns = metav1.NamespaceAll
	}
	should := func(kind string) bool {
		if watchAll {
			return true
		}
		return wants[kind]
	}
	if should("Pod") {
		if pw, err := e.k8s.CoreV1().Pods(ns).Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(pw, "Pod", ch, e.ctx.Done())
		}
	}
	if should("Deployment") {
		if dw, err := e.k8s.AppsV1().Deployments(ns).Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(dw, "Deployment", ch, e.ctx.Done())
		}
	}
	if should("ConfigMap") {
		if cw, err := e.k8s.CoreV1().ConfigMaps(ns).Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(cw, "ConfigMap", ch, e.ctx.Done())
		}
	}
	if should("Service") {
		if sw, err := e.k8s.CoreV1().Services(ns).Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(sw, "Service", ch, e.ctx.Done())
		}
	}
	if should("Node") {
		if nw, err := e.k8s.CoreV1().Nodes().Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(nw, "Node", ch, e.ctx.Done())
		}
	}
	if should("StatefulSet") {
		if ssw, err := e.k8s.AppsV1().StatefulSets(ns).Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(ssw, "StatefulSet", ch, e.ctx.Done())
		}
	}
	if should("Secret") {
		if secw, err := e.k8s.CoreV1().Secrets(ns).Watch(e.ctx, metav1.ListOptions{}); err == nil {
			go streamEvents(secw, "Secret", ch, e.ctx.Done())
		}
	}
	return ch, nil
}

func streamEvents(watcher watch.Interface, kind string, ch chan<- WatchEvent, done <-chan struct{}) {
	defer watcher.Stop()
	for {
		select {
		case <-done:
			return
		case ev, ok := <-watcher.ResultChan():
			if !ok {
				return
			}
			we := WatchEvent{Timestamp: time.Now(), EventType: string(ev.Type), Kind: kind}
			if obj, ok := ev.Object.(metav1.Object); ok {
				we.Name = obj.GetName()
				we.Namespace = obj.GetNamespace()
				mfs := obj.GetManagedFields()
				if len(mfs) > 0 {
					we.FieldManager = mfs[len(mfs)-1].Manager
				}
			}
			if we.Name == "" {
				continue
			}
			select {
			case ch <- we:
			default:
			}
		}
	}
}
