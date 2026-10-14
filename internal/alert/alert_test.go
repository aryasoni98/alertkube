package alert

import "testing"

func TestKindRegistryMatchesDeclarations(t *testing.T) {
	if Kind("NotAKind").valid() {
		t.Fatal("unknown kind was accepted")
	}
	for _, k := range []Kind{KindPod, KindNode, KindExternal, KindDerived, KindACMCertificate} {
		if !k.valid() || string(k) == "" {
			t.Fatalf("declared kind %q is not registered", k)
		}
	}
	if len(knownKinds) < 40 {
		t.Fatalf("kind registry has %d entries, want the full set", len(knownKinds))
	}
}

func TestComputeFingerprintStable(t *testing.T) {
	a := ComputeFingerprint(KindPod, "prod", "api-78", "CrashLoopBackOff")
	b := ComputeFingerprint(KindPod, "prod", "api-78", "CrashLoopBackOff")
	if a != b {
		t.Fatalf("fingerprint not stable: %s != %s", a, b)
	}
	if len(a) != fingerprintLen {
		t.Fatalf("fingerprint length: want %d, got %d", fingerprintLen, len(a))
	}
}

func TestFingerprintFieldBoundaries(t *testing.T) {
	a := ComputeFingerprint(KindPod, "a|b", "c", "reason")
	b := ComputeFingerprint(KindPod, "a", "b|c", "reason")
	if a == b {
		t.Fatal("a pipe inside a field collided with the next field")
	}
}

func TestComputeFingerprintDistinct(t *testing.T) {
	// Each row varies exactly one identity field against the base, so a
	// fingerprint that ignored namespace, name or reason would collide.
	base := ComputeFingerprint(KindPod, "prod", "api", "CrashLoop")
	cases := []struct {
		field, ns, name, reason string
	}{
		{"reason", "prod", "api", "OOMKilled"},
		{"name", "prod", "worker", "CrashLoop"},
		{"namespace", "staging", "api", "CrashLoop"},
	}
	for _, c := range cases {
		if got := ComputeFingerprint(KindPod, c.ns, c.name, c.reason); got == base {
			t.Fatalf("changing %s did not change the fingerprint: %s/%s/%s -> %s", c.field, c.ns, c.name, c.reason, got)
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

// TestFieldKeysResolveToFields pins IsFieldKey to FieldValue's switch. A key
// still listed after FieldValue stopped resolving it would let config accept
// `key: ""`, a matcher that matches every alert missing that label.
func TestFieldKeysResolveToFields(t *testing.T) {
	for k := range fieldKeys {
		a := &Alert{Labels: map[string]string{k: "L"}}
		if a.FieldValue(k) == "L" {
			t.Errorf("IsFieldKey(%q) is true but FieldValue reads it from Labels", k)
		}
	}
	for _, k := range []string{"team", "app", ""} {
		if IsFieldKey(k) {
			t.Errorf("IsFieldKey(%q) = true, want false for a label key", k)
		}
	}
}

func TestIsPatternKey(t *testing.T) {
	for k, want := range map[string]bool{
		"namespace": true, "reason": true,
		"severity": false, "kind": false, "node": false, "name": false, "team": false,
	} {
		if got := IsPatternKey(k); got != want {
			t.Errorf("IsPatternKey(%q) = %v, want %v", k, got, want)
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

func TestGroupKeyKeepsFieldsAndValuesDistinct(t *testing.T) {
	for _, tc := range []struct {
		left, right map[string]string
	}{
		{map[string]string{"a": "x", "b": "y"}, map[string]string{"a": "y", "b": "x"}},
		{map[string]string{"a": "x|y", "b": "z"}, map[string]string{"a": "x", "b": "y|z"}},
		{map[string]string{"a": "x&b=y", "b": "z"}, map[string]string{"a": "x", "b": "y&b=z"}},
	} {
		a, b := &Alert{Labels: tc.left}, &Alert{Labels: tc.right}
		if a.GroupKey([]string{"a", "b"}) == b.GroupKey([]string{"a", "b"}) {
			t.Errorf("distinct alerts share a group key: %v and %v", tc.left, tc.right)
		}
	}
}
