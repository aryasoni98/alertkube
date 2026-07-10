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
severityOverrides:
  - match: {kind: Pod, reason: ImagePullBackOff, namespace: dev-.*}
    severity: info
sinkRates:
  slack:
    perSecond: 2
    burst: 10
behavior:
  muteSeconds: 900
  startupGraceSeconds: 45
  pvcPendingSeconds: 120
`)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Behavior.MuteSeconds != 900 {
		t.Errorf("muteSeconds = %d, want 900 (yaml should win over env default)", c.Behavior.MuteSeconds)
	}
	if c.Behavior.StartupGraceSeconds != 45 {
		t.Errorf("startupGraceSeconds = %d, want 45", c.Behavior.StartupGraceSeconds)
	}
	if c.Behavior.PVCPendingSeconds != 120 {
		t.Errorf("pvcPendingSeconds = %d, want 120", c.Behavior.PVCPendingSeconds)
	}
	if len(c.SeverityOverrides) != 1 || c.SeverityOverrides[0].Severity != "info" {
		t.Errorf("severityOverrides not parsed: %+v", c.SeverityOverrides)
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
			name:    "muteSeconds equals resync floor",
			yaml:    "behavior:\n  muteSeconds: 300\n",
			errPart: "must exceed the informer resync period",
		},
		{
			name:    "resolveTTLSeconds below resync floor",
			yaml:    "behavior:\n  resolveTTLSeconds: 120\n",
			errPart: "resolveTTLSeconds",
		},
		{
			name:    "negative startupGraceSeconds",
			yaml:    "behavior:\n  startupGraceSeconds: -1\n",
			errPart: "startupGraceSeconds",
		},
		{
			name:    "negative pvcPendingSeconds",
			yaml:    "behavior:\n  pvcPendingSeconds: -1\n",
			errPart: "pvcPendingSeconds",
		},
		{
			name:    "severity override bad severity",
			yaml:    "severityOverrides:\n  - match: {kind: Pod}\n    severity: urgent\n",
			errPart: `got "urgent"`,
		},
		{
			name:    "severity override empty match",
			yaml:    "severityOverrides:\n  - severity: info\n",
			errPart: "match is empty",
		},
		{
			name:    "sinkRates unknown sink",
			yaml:    "sinkRates:\n  slak:\n    perSecond: 1\n    burst: 5\n",
			errPart: `unknown sink "slak"`,
		},
		{
			name:    "sinkRates zero perSecond",
			yaml:    "sinkRates:\n  slack:\n    perSecond: 0\n    burst: 5\n",
			errPart: "perSecond",
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

func TestValidateCorrelationBounds(t *testing.T) {
	base := func() *Config {
		c := &Config{Cluster: "c"}
		c.Behavior.MuteSeconds = InformerResyncSeconds + 1
		c.Behavior.ResolveTTLSeconds = InformerResyncSeconds + 1
		// Validate() unconditionally requires this to be positive (not gated
		// behind any Enabled flag), and this test builds *Config directly
		// rather than through Load/applyEnvDefaults, so it must be set here.
		c.Behavior.PVCPendingSeconds = 1
		return c
	}
	ok := base()
	ok.Correlation = Correlation{Enabled: true, IntervalSeconds: 15, MaxHops: 3, BlastRadiusCap: 50}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid correlation rejected: %v", err)
	}
	// Enabled with all-zero numerics ⇒ "use engine defaults" ⇒ must pass.
	def := base()
	def.Correlation = Correlation{Enabled: true}
	if err := def.Validate(); err != nil {
		t.Fatalf("enabled correlation with all-zero (defaults) rejected: %v", err)
	}
	bad := base()
	bad.Correlation = Correlation{Enabled: true, MaxHops: 99}
	if err := bad.Validate(); err == nil {
		t.Fatal("maxHops 99 should be rejected")
	}
	// Disabled with junk values must not fail (unused).
	off := base()
	off.Correlation = Correlation{Enabled: false, IntervalSeconds: 1, MaxHops: 99}
	if err := off.Validate(); err != nil {
		t.Fatalf("disabled correlation must skip bounds: %v", err)
	}
}
