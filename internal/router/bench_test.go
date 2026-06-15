package router

import (
	"testing"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

var benchSinks []string

// BenchmarkRoute measures the hot path an alert takes through routing:
// silence check, inhibition check, inhibition arming, and route matching
// (including an anchored-regex namespace match).
func BenchmarkRoute(b *testing.B) {
	routes := []config.Route{
		{Match: map[string]string{"severity": "critical"}, Sinks: []string{"slack", "pagerduty"}},
		{Match: map[string]string{"severity": "warning", "namespace": "prod-.*"}, Sinks: []string{"slack"}},
		{Match: map[string]string{"severity": "info"}, Sinks: []string{"slack"}},
	}
	inhibitions := []config.Inhibition{
		{
			Source:   map[string]string{"kind": "Node", "reason": "NodeNotReady"},
			Target:   map[string]string{"kind": "Pod"},
			Equal:    []string{"node"},
			Duration: "10m",
		},
	}
	r := New(routes, inhibitions, nil, []string{"stdout"})

	a := alert.New(alert.KindPod, "prod-us-east-1", "api-0", "OOMKilled", alert.SeverityCritical)
	a.NodeName = "node-1"

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchSinks = r.Route(a)
	}
}

// BenchmarkRoute_Silenced measures the suppressed path (silence matches, alert
// dropped before route matching).
func BenchmarkRoute_Silenced(b *testing.B) {
	silences := []config.Silence{
		{Matchers: map[string]string{"namespace": "kube-system"}, Until: "2999-01-01T00:00:00Z"},
	}
	r := New(nil, nil, silences, []string{"stdout"})
	a := alert.New(alert.KindPod, "kube-system", "coredns-0", "CrashLoopBackOff", alert.SeverityWarning)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchSinks = r.Route(a)
	}
}
