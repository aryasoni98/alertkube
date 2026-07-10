package topology

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"alertkube/internal/alert"
)

// Neighbors returns the direct topological edges out of ref. Unknown kinds and
// the inert (unsynced) topology return nil.
func (l *lister) Neighbors(ref alert.Ref) []Edge {
	if !l.ready {
		return nil
	}
	switch ref.Kind {
	case string(alert.KindPod):
		return l.podEdges(ref)
	case string(alert.KindReplicaSet):
		return ownerEdges(l.rsOwners(ref))
	case string(alert.KindJob):
		return ownerEdges(l.jobOwners(ref))
	case string(alert.KindService):
		return l.serviceEdges(ref)
	}
	return nil
}

func (l *lister) podEdges(ref alert.Ref) []Edge {
	pod, err := l.pods.Pods(ref.Namespace).Get(ref.Name)
	if err != nil {
		return nil
	}
	var edges []Edge
	for _, o := range pod.OwnerReferences {
		edges = append(edges, Edge{To: alert.Ref{Kind: o.Kind, Namespace: ref.Namespace, Name: o.Name}, Rel: "owns"})
	}
	if pod.Spec.NodeName != "" {
		edges = append(edges, Edge{To: alert.Ref{Kind: string(alert.KindNode), Name: pod.Spec.NodeName}, Rel: "scheduledOn"})
	}
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			edges = append(edges, Edge{To: alert.Ref{Kind: string(alert.KindPVC), Namespace: ref.Namespace, Name: v.PersistentVolumeClaim.ClaimName}, Rel: "bound"})
		}
	}
	edges = append(edges, l.servicesForPod(ref)...)
	return edges
}

// serviceEdges finds the Pods a Service selects.
func (l *lister) serviceEdges(ref alert.Ref) []Edge {
	svc, err := l.svc.Services(ref.Namespace).Get(ref.Name)
	if err != nil || len(svc.Spec.Selector) == 0 {
		return nil
	}
	pods, err := l.pods.Pods(ref.Namespace).List(labels.SelectorFromSet(svc.Spec.Selector))
	if err != nil {
		return nil
	}
	out := make([]Edge, 0, len(pods))
	for _, p := range pods {
		out = append(out, Edge{To: alert.Ref{Kind: string(alert.KindPod), Namespace: ref.Namespace, Name: p.Name}, Rel: "selects"})
	}
	return out
}

// servicesForPod finds Services whose selector matches the pod (back-link).
func (l *lister) servicesForPod(ref alert.Ref) []Edge {
	pod, err := l.pods.Pods(ref.Namespace).Get(ref.Name)
	if err != nil || len(pod.Labels) == 0 {
		return nil
	}
	svcs, err := l.svc.Services(ref.Namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	var out []Edge
	for _, s := range svcs {
		if len(s.Spec.Selector) == 0 {
			continue
		}
		if labels.SelectorFromSet(s.Spec.Selector).Matches(labels.Set(pod.Labels)) {
			out = append(out, Edge{To: alert.Ref{Kind: string(alert.KindService), Namespace: ref.Namespace, Name: s.Name}, Rel: "selects"})
		}
	}
	return out
}

func (l *lister) rsOwners(ref alert.Ref) []alert.Ref {
	rs, err := l.rs.ReplicaSets(ref.Namespace).Get(ref.Name)
	if err != nil {
		return nil
	}
	return ownerRefs(rs.OwnerReferences, ref.Namespace)
}

func (l *lister) jobOwners(ref alert.Ref) []alert.Ref {
	j, err := l.jobs.Jobs(ref.Namespace).Get(ref.Name)
	if err != nil {
		return nil
	}
	return ownerRefs(j.OwnerReferences, ref.Namespace)
}

func ownerRefs(owners []metav1.OwnerReference, ns string) []alert.Ref {
	out := make([]alert.Ref, 0, len(owners))
	for _, o := range owners {
		out = append(out, alert.Ref{Kind: o.Kind, Namespace: ns, Name: o.Name})
	}
	return out
}

func ownerEdges(refs []alert.Ref) []Edge {
	out := make([]Edge, 0, len(refs))
	for _, r := range refs {
		out = append(out, Edge{To: r, Rel: "owns"})
	}
	return out
}
