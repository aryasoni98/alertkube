package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDispatchSubcommand_NoArgsFallsThrough(t *testing.T) {
	handled, code := dispatchSubcommand(nil, io.Discard, io.Discard)
	if handled || code != 0 {
		t.Fatalf("empty args must fall through: handled=%v code=%d", handled, code)
	}
	handled, _ = dispatchSubcommand([]string{"--leader-elect=true"}, io.Discard, io.Discard)
	if handled {
		t.Fatal("unknown/flag arg must fall through to the controller")
	}
}

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	handled, code := dispatchSubcommand([]string{"version"}, &out, io.Discard)
	if !handled || code != 0 {
		t.Fatalf("version: handled=%v code=%d", handled, code)
	}
	s := out.String()
	if !strings.Contains(s, appName) {
		t.Errorf("version output missing app name: %q", s)
	}
	if !strings.Contains(s, "go:") || !strings.Contains(s, "arch:") {
		t.Errorf("version output missing provenance: %q", s)
	}
}

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const validConfigYAML = `
cluster: test
behavior:
  muteSeconds: 600
  resolveTTLSeconds: 600
  pvcPendingSeconds: 300
routing:
  - match: {severity: critical}
    sinks: [slack]
`

func TestRunValidate_Valid(t *testing.T) {
	p := writeTempConfig(t, validConfigYAML)
	var out, errOut bytes.Buffer
	handled, code := dispatchSubcommand([]string{"validate", "--config", p}, &out, &errOut)
	if !handled {
		t.Fatal("validate must be handled")
	}
	if code != 0 {
		t.Fatalf("valid config should exit 0, got %d (stderr=%q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "valid") {
		t.Errorf("expected success message, got %q", out.String())
	}
}

func TestRunValidate_PositionalPath(t *testing.T) {
	p := writeTempConfig(t, validConfigYAML)
	var out, errOut bytes.Buffer
	_, code := dispatchSubcommand([]string{"validate", p}, &out, &errOut)
	if code != 0 {
		t.Fatalf("positional path should validate, got %d (stderr=%q)", code, errOut.String())
	}
}

func TestRunValidate_Invalid(t *testing.T) {
	// muteSeconds below the informer resync period is rejected by Validate.
	p := writeTempConfig(t, `
cluster: test
behavior:
  muteSeconds: 5
  resolveTTLSeconds: 600
  pvcPendingSeconds: 300
`)
	var out, errOut bytes.Buffer
	_, code := dispatchSubcommand([]string{"validate", "--config", p}, &out, &errOut)
	if code != 1 {
		t.Fatalf("invalid config should exit 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "muteSeconds") {
		t.Errorf("error should name the offending field, got %q", errOut.String())
	}
}

func TestRunValidate_MissingFile(t *testing.T) {
	var out, errOut bytes.Buffer
	_, code := dispatchSubcommand([]string{"validate", "--config", "/nonexistent/x.yaml"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("unreadable config should exit 1, got %d", code)
	}
}

func TestRunValidate_NoPath(t *testing.T) {
	t.Setenv("ALERTKUBE_CONFIG", "")
	var out, errOut bytes.Buffer
	_, code := dispatchSubcommand([]string{"validate"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("missing path should be a usage error (exit 2), got %d", code)
	}
}

func TestRunValidate_EnvFallback(t *testing.T) {
	p := writeTempConfig(t, validConfigYAML)
	t.Setenv("ALERTKUBE_CONFIG", p)
	var out, errOut bytes.Buffer
	_, code := dispatchSubcommand([]string{"validate"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("ALERTKUBE_CONFIG fallback should validate, got %d (stderr=%q)", code, errOut.String())
	}
}
