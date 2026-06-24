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
