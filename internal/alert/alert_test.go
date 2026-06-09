package alert

import "testing"

func TestComputeFingerprintStable(t *testing.T) {
	a := ComputeFingerprint(KindPod, "prod", "api-78", "CrashLoopBackOff")
	b := ComputeFingerprint(KindPod, "prod", "api-78", "CrashLoopBackOff")
	if a != b {
		t.Fatalf("fingerprint not stable: %s != %s", a, b)
	}
	if len(a) != 12 {
		t.Fatalf("fingerprint length: want 12, got %d", len(a))
	}
}

func TestComputeFingerprintDistinct(t *testing.T) {
	cases := [][2]string{
		{"prod|api|CrashLoop", "prod|api|OOMKilled"},
		{"prod|api|CrashLoop", "prod|worker|CrashLoop"},
		{"prod|api|CrashLoop", "staging|api|CrashLoop"},
	}
	for _, c := range cases {
		a := ComputeFingerprint(KindPod, "prod", "api", c[0])
		b := ComputeFingerprint(KindPod, "prod", "api", c[1])
		if a == b {
			t.Fatalf("collisions: %s -> %s", c[0]+" vs "+c[1], a)
		}
	}
}

func TestFieldValue(t *testing.T) {
	a := New(KindPod, "prod", "api", "CrashLoopBackOff", SeverityCritical)
	a.NodeName = "node-1"
	a.Labels["team"] = "payments"

	cases := map[string]string{
		"kind":      "Pod",
		"severity":  "critical",
		"namespace": "prod",
		"name":      "api",
		"reason":    "CrashLoopBackOff",
		"node":      "node-1",
		"team":      "payments",
		"missing":   "",
	}
	for k, want := range cases {
		if got := a.FieldValue(k); got != want {
			t.Errorf("FieldValue(%q): want %q, got %q", k, want, got)
		}
	}
}

func TestMatchLabels(t *testing.T) {
	a := New(KindPod, "prod-eu-1", "api", "CrashLoopBackOff", SeverityCritical)

	if !a.MatchLabels(map[string]string{"severity": "critical"}) {
		t.Fatalf("exact severity match failed")
	}
	if a.MatchLabels(map[string]string{"severity": "warning"}) {
		t.Fatalf("severity warning should not match")
	}
	if !a.MatchLabels(map[string]string{"namespace": "prod-.*"}) {
		t.Fatalf("namespace regex prefix should match")
	}
	if !a.MatchLabels(map[string]string{"severity": "critical", "kind": "Pod"}) {
		t.Fatalf("multi-key match failed")
	}
}

func TestMatchLabelsAnchored(t *testing.T) {
	// Regression: substring shim used to match `dev-prod-tools` against
	// the rule `prod-.*`. Anchored regex must reject the leading `dev-`.
	a := New(KindPod, "dev-prod-tools", "api", "X", SeverityInfo)
	if a.MatchLabels(map[string]string{"namespace": "prod-.*"}) {
		t.Fatalf("anchored regex must not match dev-prod-tools")
	}
	if a.MatchLabels(map[string]string{"namespace": "kube-system"}) {
		t.Fatalf("exact namespace mismatch must not match")
	}
	if !a.MatchLabels(map[string]string{"namespace": ".*prod.*"}) {
		t.Fatalf("explicit .*prod.* should match dev-prod-tools")
	}
}

func TestMatchLabelsInvalidRegex(t *testing.T) {
	a := New(KindPod, "prod", "api", "Crash", SeverityInfo)
	// Invalid regex must not over-match. Falls back to literal equality.
	if a.MatchLabels(map[string]string{"reason": "([invalid"}) {
		t.Fatalf("invalid regex pattern must not match")
	}
}

func TestGroupKeyStable(t *testing.T) {
	a := New(KindPod, "prod", "api", "CrashLoopBackOff", SeverityCritical)
	a.NodeName = "node-1"
	k1 := a.GroupKey([]string{"namespace", "node"})
	k2 := a.GroupKey([]string{"node", "namespace"})
	if k1 != k2 {
		t.Fatalf("GroupKey is order-dependent: %q vs %q", k1, k2)
	}
}
