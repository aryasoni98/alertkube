package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingPathFails(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("expected error for missing config path, got nil")
	}
}

func TestLoadEmptyPathUsesDefaults(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if c.Behavior.MuteSeconds != 600 {
		t.Errorf("default muteSeconds = %d, want 600", c.Behavior.MuteSeconds)
	}
	if c.Behavior.ResolveTTLSeconds != 600 {
		t.Errorf("default resolveTTLSeconds = %d, want 600", c.Behavior.ResolveTTLSeconds)
	}
	if c.Behavior.IgnoreRestartCount != 30 {
		t.Errorf("default ignoreRestartCount = %d, want 30", c.Behavior.IgnoreRestartCount)
	}
	if c.Behavior.StartupGraceSeconds != 0 {
		t.Errorf("default startupGraceSeconds = %d, want 0", c.Behavior.StartupGraceSeconds)
	}
}

func TestLoadValidConfig(t *testing.T) {
	path := writeConfig(t, `
cluster: test
routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty]
inhibitions:
  - source: {kind: Node, reason: NodeNotReady}
    target: {kind: Pod}
    equal: [node]
    duration: 10m
silences:
  - matchers: {namespace: kube-system}
    until: "2030-01-01T00:00:00Z"
behavior:
  muteSeconds: 300
  startupGraceSeconds: 45
`)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Behavior.MuteSeconds != 300 {
		t.Errorf("muteSeconds = %d, want 300 (yaml should win over env default)", c.Behavior.MuteSeconds)
	}
	if c.Behavior.StartupGraceSeconds != 45 {
		t.Errorf("startupGraceSeconds = %d, want 45", c.Behavior.StartupGraceSeconds)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		errPart string
	}{
		{
			name:    "unknown sink",
			yaml:    "routing:\n  - match: {severity: critical}\n    sinks: [slak]\n",
			errPart: `unknown sink "slak"`,
		},
		{
			name:    "empty sinks",
			yaml:    "routing:\n  - match: {severity: critical}\n    sinks: []\n",
			errPart: "sinks list is empty",
		},
		{
			name:    "bad inhibition duration",
			yaml:    "inhibitions:\n  - source: {kind: Node}\n    target: {kind: Pod}\n    duration: tenminutes\n",
			errPart: "inhibitions[0]",
		},
		{
			name:    "bad silence timestamp",
			yaml:    "silences:\n  - matchers: {namespace: x}\n    until: tomorrow\n",
			errPart: "silences[0]",
		},
		{
			name:    "negative muteSeconds",
			yaml:    "behavior:\n  muteSeconds: -5\n",
			errPart: "muteSeconds",
		},
		{
			name:    "negative startupGraceSeconds",
			yaml:    "behavior:\n  startupGraceSeconds: -1\n",
			errPart: "startupGraceSeconds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.yaml))
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.errPart) {
				t.Fatalf("error %q does not mention %q", err, tc.errPart)
			}
		})
	}
}

func TestLoadMalformedYAMLFails(t *testing.T) {
	_, err := Load(writeConfig(t, "cluster: [unclosed"))
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}
