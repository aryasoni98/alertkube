package collectors

import (
	"context"
	"fmt"
	"sort"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PodEvents returns Warning events related to a pod, newest last.
func PodEvents(ctx context.Context, c kubernetes.Interface, ns, name string) (string, error) {
	events, err := c.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: "type!=Normal"})
	if err != nil {
		return "", fmt.Errorf("list pod events: %w", err)
	}
	items := events.Items
	if len(items) > 1 {
		sort.Sort(byLastTimestamp(items))
	}
	out := ""
	for _, e := range items {
		if e.InvolvedObject.Name == name {
			out += fmt.Sprintf("%s, %s, %s\n", e.LastTimestamp, e.Reason, e.Message)
		}
	}
	return out, nil
}

// NodeEvents returns events for a given node.
func NodeEvents(ctx context.Context, c kubernetes.Interface, nodeName string) (string, error) {
	events, err := c.CoreV1().Events(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.kind=Node"})
	if err != nil {
		return "", err
	}
	items := events.Items
	if len(items) > 1 {
		sort.Sort(byLastTimestamp(items))
	}
	out := ""
	for _, e := range items {
		if e.InvolvedObject.Name == nodeName {
			out += fmt.Sprintf("%s, %s, %s\n", e.LastTimestamp, e.Reason, e.Message)
		}
	}
	return out, nil
}

type byLastTimestamp []v1.Event

func (o byLastTimestamp) Len() int      { return len(o) }
func (o byLastTimestamp) Swap(i, j int) { o[i], o[j] = o[j], o[i] }
func (o byLastTimestamp) Less(i, j int) bool {
	if o[i].LastTimestamp.Equal(&o[j].LastTimestamp) {
		return o[i].InvolvedObject.Name < o[j].InvolvedObject.Name
	}
	return o[i].LastTimestamp.Before(&o[j].LastTimestamp)
}
