package sinks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aryasoni98/alertkube/internal/alert"
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

// CredentialEnv is what the console's Secret-reference test-fire injects. Each
// listed env var must be the one the registered sink actually reads, or the
// injected credential is ignored and the sink silently no-ops. telegram needs a
// second credential, so it must stay unlisted.
func TestCredentialEnvDrivesTestSend(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	oldPD := pagerdutyEventsURL
	pagerdutyEventsURL = srv.URL
	t.Cleanup(func() { pagerdutyEventsURL = oldPD })

	reg := BuildDefault(SinkConfig{Cluster: "c"})
	for name := range singleCredEnv {
		if !reg.Has(name) {
			t.Errorf("CredentialEnv lists %q, which is not a registered sink", name)
		}
	}
	if _, ok := CredentialEnv("telegram"); ok {
		t.Error("telegram needs two credentials and must not be test-fired by one Secret reference")
	}

	a := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	for _, name := range reg.Names() {
		env, ok := CredentialEnv(name)
		if !ok {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for _, e := range singleCredEnv {
				t.Setenv(e, "")
			}
			t.Setenv(envSlackBotToken, "")
			t.Setenv("OPSGENIE_API_URL", srv.URL)
			val := "test-key"
			if strings.HasSuffix(env, "_URL") {
				val = srv.URL
			}
			before := hits.Load()
			ctx := WithCreds(context.Background(), map[string]string{env: val})
			if err := reg.TestSend(ctx, name, a); err != nil {
				t.Fatalf("TestSend: %v", err)
			}
			if got := hits.Load() - before; got != 1 {
				t.Fatalf("sink made %d requests with only %s injected, want 1", got, env)
			}
		})
	}
}
