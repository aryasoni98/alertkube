package alert

import "testing"

// Package-level sinks prevent the compiler from optimizing the benchmarked
// calls away.
var (
	benchStr  string
	benchBool bool
)

func BenchmarkComputeFingerprint(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchStr = ComputeFingerprint(KindPod, "production", "web-frontend-7d9f8b6c5-abcde", "CrashLoopBackOff")
	}
}

func BenchmarkMatchLabels_Regex(b *testing.B) {
	a := New(KindPod, "prod-us-east-1", "api-0", "OOMKilled", SeverityCritical)
	want := map[string]string{"severity": "critical", "namespace": "prod-.*"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchBool = a.MatchLabels(want)
	}
}

func BenchmarkMatchLabels_Exact(b *testing.B) {
	a := New(KindPod, "default", "api-0", "OOMKilled", SeverityCritical)
	want := map[string]string{"severity": "critical", "kind": "Pod"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchBool = a.MatchLabels(want)
	}
}

func BenchmarkGroupKey(b *testing.B) {
	a := New(KindPod, "ns", "name", "reason", SeverityWarning)
	by := []string{"kind", "namespace", "reason", "severity"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchStr = a.GroupKey(by)
	}
}
