package sinks

import (
	"context"
	"testing"
)

func TestCredPrefersOverrideThenEnv(t *testing.T) {
	ctx := context.Background()
	// No override, no env -> empty.
	if got := cred(ctx, "ALERTKUBE_TEST_CRED_X"); got != "" {
		t.Fatalf("no override/env: got %q, want empty", got)
	}
	// Override wins.
	ctx2 := WithCreds(ctx, map[string]string{"ALERTKUBE_TEST_CRED_X": "from-secret"})
	if got := cred(ctx2, "ALERTKUBE_TEST_CRED_X"); got != "from-secret" {
		t.Fatalf("override: got %q, want from-secret", got)
	}
	// Env fallback when no override key.
	t.Setenv("ALERTKUBE_TEST_CRED_Y", "from-env")
	if got := cred(ctx2, "ALERTKUBE_TEST_CRED_Y"); got != "from-env" {
		t.Fatalf("env fallback: got %q, want from-env", got)
	}
	// Empty override is treated as absent -> falls back to env.
	ctx3 := WithCreds(ctx, map[string]string{"ALERTKUBE_TEST_CRED_Y": ""})
	if got := cred(ctx3, "ALERTKUBE_TEST_CRED_Y"); got != "from-env" {
		t.Fatalf("empty override should fall back to env: got %q", got)
	}
}

func TestRequireCredReportsPresence(t *testing.T) {
	ctx := context.Background()
	// Absent credential -> ("", false) so the caller no-ops observably.
	if v, ok := requireCred(ctx, "testsink", "ALERTKUBE_TEST_CRED_ABSENT"); ok || v != "" {
		t.Fatalf("absent credential: got (%q,%v), want (\"\",false)", v, ok)
	}
	// Present credential -> (value, true).
	t.Setenv("ALERTKUBE_TEST_CRED_PRESENT", "secret-value")
	if v, ok := requireCred(ctx, "testsink", "ALERTKUBE_TEST_CRED_PRESENT"); !ok || v != "secret-value" {
		t.Fatalf("present credential: got (%q,%v), want (secret-value,true)", v, ok)
	}
	// Override still wins over env.
	octx := WithCreds(ctx, map[string]string{"ALERTKUBE_TEST_CRED_PRESENT": "override-value"})
	if v, ok := requireCred(octx, "testsink", "ALERTKUBE_TEST_CRED_PRESENT"); !ok || v != "override-value" {
		t.Fatalf("override credential: got (%q,%v), want (override-value,true)", v, ok)
	}
}
