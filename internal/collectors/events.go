package collectors

import (
	"context"
	"fmt"
	"sort"
	"strings"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// PodEvents returns Warning events related to a pod, newest last. The
// involvedObject selector is applied server-side so a busy namespace does
// not return (and the client does not scan) every Warning event in it.
func PodEvents(ctx context.Context, c kubernetes.Interface, ns, name string) (string, error) {
	events, err := c.CoreV1().Events(ns).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.kind=Pod,involvedObject.name=%s,type!=Normal", name),
	})
	if err != nil {
		return "", fmt.Errorf("list pod events: %w", err)
	}
	return formatEvents(events.Items, name), nil
}

// NodeEvents returns events for a given node from across all namespaces
// (kubelet typically writes Node-scoped events into `default`, but some
// distributions route them to `kube-system` or the involved object's
// namespace - list cluster-wide and filter server-side).
func NodeEvents(ctx context.Context, c kubernetes.Interface, nodeName string) (string, error) {
	events, err := c.CoreV1().Events(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("involvedObject.kind=Node,involvedObject.name=%s", nodeName),
	})
	if err != nil {
		return "", fmt.Errorf("list node events: %w", err)
	}
	return formatEvents(events.Items, nodeName), nil
}

// formatEvents sorts items by timestamp and renders entries matching involvedObject.Name.
func formatEvents(items []v1.Event, name string) string {
	if len(items) > 1 {
		sort.Sort(byLastTimestamp(items))
	}
	var b strings.Builder
	for _, e := range items {
		if e.InvolvedObject.Name != name {
			continue
		}
		fmt.Fprintf(&b, "%s, %s, %s\n", e.LastTimestamp, e.Reason, e.Message)
	}
	return b.String()
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
