package router

import (
	"testing"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

func TestRouteMatch(t *testing.T) {
	r := New(
		[]config.Route{
			{Match: map[string]string{"severity": "critical"}, Sinks: []string{"slack", "pagerduty"}},
			{Match: map[string]string{"severity": "warning"}, Sinks: []string{"slack"}},
		},
		nil, nil, []string{"stdout"},
	)
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	got := r.Route(a)
	if len(got) != 2 || got[0] != "slack" || got[1] != "pagerduty" {
		t.Fatalf("critical should route to slack+pagerduty, got %v", got)
	}
}

func TestRouteDefault(t *testing.T) {
	r := New(nil, nil, nil, []string{"stdout"})
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)
	got := r.Route(a)
	if len(got) != 1 || got[0] != "stdout" {
		t.Fatalf("expected default sinks, got %v", got)
	}
}

func TestRouteSilencedAnnotation(t *testing.T) {
	r := New(nil, nil, nil, []string{"stdout"})
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)
	a.Annotations["alert-silence-until"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	if r.Route(a) != nil {
		t.Fatalf("annotation silence should drop alert")
	}
}

func TestInhibition(t *testing.T) {
	r := New(
		nil,
		[]config.Inhibition{{
			Source:   map[string]string{"kind": "Node", "reason": "NodeNotReady"},
			Target:   map[string]string{"kind": "Pod"},
			Equal:    []string{"node"},
			Duration: "1h",
		}},
		nil,
		[]string{"slack"},
	)

	src := alert.New(alert.KindNode, "", "node-1", "NodeNotReady", alert.SeverityCritical)
	src.NodeName = "node-1"
	if r.Route(src) == nil {
		t.Fatalf("source alert should route, not be inhibited")
	}

	pod := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	pod.NodeName = "node-1"
	if r.Route(pod) != nil {
		t.Fatalf("pod on the same node must be inhibited")
	}

	other := alert.New(alert.KindPod, "ns", "p2", "CrashLoopBackOff", alert.SeverityCritical)
	other.NodeName = "node-2"
	if r.Route(other) == nil {
		t.Fatalf("pod on a different node must not be inhibited")
	}
}

func TestInhibitionExpires(t *testing.T) {
	r := New(
		nil,
		[]config.Inhibition{{
			Source:   map[string]string{"kind": "Node", "reason": "NodeNotReady"},
			Target:   map[string]string{"kind": "Pod"},
			Equal:    []string{"node"},
			Duration: "10ms",
		}},
		nil,
		[]string{"slack"},
	)
	src := alert.New(alert.KindNode, "", "node-1", "NodeNotReady", alert.SeverityCritical)
	src.NodeName = "node-1"
	r.Route(src)

	time.Sleep(30 * time.Millisecond)

	pod := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	pod.NodeName = "node-1"
	if r.Route(pod) == nil {
		t.Fatalf("inhibition should have expired")
	}
	// Prune should have evicted the expired key.
	r.mu.Lock()
	n := len(r.activeInhibits)
	r.mu.Unlock()
	if n != 0 {
		t.Fatalf("expired inhibitions should be pruned, got %d", n)
	}
}
