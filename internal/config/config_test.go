package config

import (
	"fmt"
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
		{
			name:    "silence empty matchers",
			yaml:    "silences:\n  - matchers: {}\n    until: \"2030-01-01T00:00:00Z\"\n",
			errPart: "empty matchers match every alert",
		},
		{
			name:    "silence empty unknown key",
			yaml:    "silences:\n  - matchers: {team: \"\"}\n    until: \"2030-01-01T00:00:00Z\"\n",
			errPart: "unknown key",
		},
		{
			name:    "silence namespace star",
			yaml:    "silences:\n  - matchers: {namespace: \".*\"}\n    until: \"2030-01-01T00:00:00Z\"\n",
			errPart: "matches every alert",
		},
		{
			name:    "maintenance reason star",
			yaml:    "maintenance:\n  - matchers: {reason: \".*\"}\n    start: \"01:00\"\n    end: \"02:00\"\n",
			errPart: "matches every alert",
		},
		{
			name:    "inhibition empty target",
			yaml:    "inhibitions:\n  - source: {kind: Node}\n    target: {}\n",
			errPart: "inhibitions[0].target",
		},
		{
			name:    "escalation empty match",
			yaml:    "escalations:\n  - match: {}\n    afterMinutes: 15\n    sinks: [slack]\n",
			errPart: "escalations[0].match",
		},
		{
			name:    "routing catch-all not last",
			yaml:    "routing:\n  - match: {}\n    sinks: [slack]\n  - match: {severity: critical}\n    sinks: [pagerduty]\n",
			errPart: "must be the final route",
		},
		{
			name:    "routing namespace star",
			yaml:    "routing:\n  - match: {namespace: \".*\"}\n    sinks: [slack]\n  - match: {}\n    sinks: [stdout]\n",
			errPart: `routing[0].match: namespace=".*" matches every alert`,
		},
		// A non-final match-all route shadows every later route.
		{
			name:    "non-final routing namespace plus",
			yaml:    "routing:\n  - match: {namespace: \".+\"}\n    sinks: [slack]\n  - match: {severity: critical}\n    sinks: [stdout]\n",
			errPart: `routing[0].match: namespace=".+" matches every alert`,
		},
		// The final route may be a catch-all, but its patterns must compile.
		{
			name:    "final routing invalid regex",
			yaml:    "routing:\n  - match: {severity: critical}\n    sinks: [slack]\n  - match: {namespace: \"(\"}\n    sinks: [stdout]\n",
			errPart: `routing[1].match: namespace pattern "("`,
		},
		// .+ does not match "", but no alert has an empty reason, so it still
		// silences everything.
		{
			name:    "silence reason plus",
			yaml:    "silences:\n  - matchers: {reason: \".+\"}\n    until: \"2030-01-01T00:00:00Z\"\n",
			errPart: `silences[0].matchers: reason=".+" matches every alert`,
		},
		{
			name:    "inhibition target non-whitespace star",
			yaml:    "inhibitions:\n  - source: {kind: Node}\n    target: {namespace: '\\S*'}\n",
			errPart: `inhibitions[0].target: namespace="\\S*" matches every alert`,
		},
		{
			name:    "escalation not-newline star",
			yaml:    "escalations:\n  - match: {reason: '[^\\n]*'}\n    afterMinutes: 15\n    sinks: [slack]\n",
			errPart: `escalations[0].match: reason="[^\\n]*" matches every alert`,
		},
		// An invalid namespace/reason regex never matches at runtime (it falls
		// back to literal equality), so the entry would silently do nothing.
		{
			name:    "routing invalid namespace regex",
			yaml:    "routing:\n  - match: {namespace: \"prod-(a|b\"}\n    sinks: [pagerduty]\n",
			errPart: `routing[0].match: namespace pattern "prod-(a|b"`,
		},
		{
			name:    "silence invalid namespace regex",
			yaml:    "silences:\n  - matchers: {namespace: \"prod-(\"}\n    until: \"2030-01-01T00:00:00Z\"\n",
			errPart: `silences[0].matchers: namespace pattern "prod-("`,
		},
		{
			name:    "inhibition invalid reason regex",
			yaml:    "inhibitions:\n  - source: {reason: \"Node[\"}\n    target: {kind: Pod}\n",
			errPart: `inhibitions[0].source: reason pattern "Node["`,
		},
		{
			name:    "escalation invalid reason regex",
			yaml:    "escalations:\n  - match: {reason: \"OOM(\"}\n    afterMinutes: 15\n    sinks: [slack]\n",
			errPart: `escalations[0].match: reason pattern "OOM("`,
		},
		{
			name:    "maintenance invalid namespace regex",
			yaml:    "maintenance:\n  - matchers: {namespace: \"db-[\"}\n    start: \"01:00\"\n    end: \"02:00\"\n",
			errPart: `maintenance[0]: matchers: namespace pattern "db-["`,
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

// TestFinalRouteMayBeCatchAll pins that the final route, which shadows no later
// route, may match every alert with a pattern as well as with match: {}.
func TestFinalRouteMayBeCatchAll(t *testing.T) {
	for _, pattern := range []string{".+", ".*"} {
		t.Run(pattern, func(t *testing.T) {
			yaml := "routing:\n  - match: {severity: critical}\n    sinks: [slack]\n  - match: {namespace: \"" + pattern + "\"}\n    sinks: [stdout]\n"
			if _, err := Load(writeConfig(t, yaml)); err != nil {
				t.Fatalf("final route namespace=%q rejected: %v", pattern, err)
			}
		})
	}
}

// TestSelectiveMatchersMatchAll pins the match-all heuristic both ways: a
// namespace/reason pattern that matches every probe value is rejected, and one
// that leaves out any realistic value is accepted.
func TestSelectiveMatchersMatchAll(t *testing.T) {
	tests := []struct {
		name  string
		match map[string]string
		valid bool
	}{
		{"dot star", map[string]string{"namespace": ".*"}, false},
		{"dot plus", map[string]string{"reason": ".+"}, false},
		{"non-whitespace star", map[string]string{"namespace": `\S*`}, false},
		{"not-newline star", map[string]string{"reason": `[^\n]*`}, false},
		{"optional x", map[string]string{"namespace": "x?"}, true},
		{"lowercase star", map[string]string{"namespace": "[a-z]*"}, true},
		{"kube prefix", map[string]string{"namespace": "kube-.*"}, true},
		{"dev prefix", map[string]string{"namespace": "dev-.*"}, true},
		// node is an alert field, so an empty value selects only alerts with
		// no node name, such as unscheduled pods and Deployment alerts.
		{"empty node", map[string]string{"node": ""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SelectiveMatchers("silences[0].matchers", tt.match)
			if tt.valid {
				if err != nil {
					t.Fatalf("SelectiveMatchers(%v) = %v, want nil", tt.match, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "matches every alert") {
				t.Fatalf("SelectiveMatchers(%v) = %v, want a match-all error", tt.match, err)
			}
		})
	}
}

func TestExamplesParseStrict(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "alertkube")
	matches, err := filepath.Glob("../../examples/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no examples/*.yaml found")
	}
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := ParseAndValidate(raw); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestStrictDecodeRejectsUnknownKey(t *testing.T) {
	_, err := Load(writeConfig(t, "cluster: prod\nnotARealKey: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "notARealKey") {
		t.Fatalf("unknown key must fail the decode, got %v", err)
	}
}

func TestConfigAliasesPreserveExplicitDefaults(t *testing.T) {
	t.Setenv("AWS_POLL_SECONDS", "900")
	t.Setenv("AZURE_POLL_SECONDS", "900")
	t.Setenv("GCP_POLL_SECONDS", "900")
	c := &Config{}
	set, err := decodeConfig([]byte(`aws: &poll
  enabled: true
  pollSeconds: 120
azure: *poll
gcp:
  <<: *poll
`), c)
	if err != nil {
		t.Fatal(err)
	}
	c.applyEnvDefaults(set)
	if c.AWS.PollSeconds != 120 || c.Azure.PollSeconds != 120 || c.GCP.PollSeconds != 120 {
		t.Fatalf("alias/merge values overridden: AWS=%d Azure=%d GCP=%d", c.AWS.PollSeconds, c.Azure.PollSeconds, c.GCP.PollSeconds)
	}
}

func TestConfigRejectsAdditionalDocuments(t *testing.T) {
	for _, raw := range []string{"cluster: prod\n---\ncluster: ignored\n", "cluster: prod\n---\n"} {
		if _, err := Load(writeConfig(t, raw)); err == nil {
			t.Fatal("Load silently ignored another YAML document")
		}
		if err := ParseAndValidate([]byte(raw)); err == nil {
			t.Fatal("API validation silently ignored another YAML document")
		}
	}
}

func TestExplicitZeroAndFalseBeatEnv(t *testing.T) {
	t.Setenv("MUTE_SECONDS", "900")
	t.Setenv("IGNORE_RESTARTS_WITH_EXIT_CODE_ZERO", "true")
	t.Setenv("STARTUP_GRACE_SECONDS", "30")

	raw := []byte("cluster: c\nbehavior:\n  muteSeconds: 0\n  ignoreRestartsWithExitCodeZero: false\n  startupGraceSeconds: 0\n  resolveTTLSeconds: 301\n  pvcPendingSeconds: 1\n")
	c := &Config{}
	set, err := decodeConfig(raw, c)
	if err != nil {
		t.Fatal(err)
	}
	c.applyEnvDefaults(set)
	if c.Behavior.MuteSeconds != 0 {
		t.Fatalf("explicit muteSeconds 0 became %d", c.Behavior.MuteSeconds)
	}
	if c.Behavior.IgnoreRestartsWithExitCodeZero {
		t.Fatal("explicit false was overwritten by the env var")
	}
	if c.Behavior.StartupGraceSeconds != 0 {
		t.Fatalf("explicit startupGraceSeconds 0 became %d", c.Behavior.StartupGraceSeconds)
	}

	omitted := &Config{}
	omitted.applyEnvDefaults(map[string]bool{})
	if omitted.Behavior.MuteSeconds != 900 {
		t.Fatalf("omitted muteSeconds = %d, want env 900", omitted.Behavior.MuteSeconds)
	}
	if !omitted.Behavior.IgnoreRestartsWithExitCodeZero {
		t.Fatal("omitted bool should take the env true")
	}
}

// TestPresentKeysBeatEnv covers every path applyEnvDefaults passes to absent().
// A key written in YAML must survive its env var; an omitted key takes the env
// value, or the built-in default when the env var is empty. The YAML is built
// from the row's path, so a typo in the row fails the strict decode, and a typo
// in applyEnvDefaults lets the env value overwrite the YAML one.
func TestPresentKeysBeatEnv(t *testing.T) {
	tests := []struct {
		path    string // section.key, as written in YAML
		gated   bool   // the default applies only while section.enabled is true
		env     string // fallback env var; empty for a static default
		get     func(*Config) any
		yaml    any // sentinel written in YAML
		fromEnv any // value of env when set; unused when env is empty
		def     any // built-in default
	}{
		{"behavior.muteSeconds", false, "MUTE_SECONDS", func(c *Config) any { return c.Behavior.MuteSeconds }, 0, 900, 600},
		{"behavior.ignoreRestartCount", false, "IGNORE_RESTART_COUNT", func(c *Config) any { return c.Behavior.IgnoreRestartCount }, 0, 900, 30},
		{"behavior.ignoreRestartsWithExitCodeZero", false, "IGNORE_RESTARTS_WITH_EXIT_CODE_ZERO", func(c *Config) any { return c.Behavior.IgnoreRestartsWithExitCodeZero }, false, true, false},
		{"behavior.resolveTTLSeconds", false, "RESOLVE_TTL_SECONDS", func(c *Config) any { return c.Behavior.ResolveTTLSeconds }, 0, 900, 600},
		{"behavior.startupGraceSeconds", false, "STARTUP_GRACE_SECONDS", func(c *Config) any { return c.Behavior.StartupGraceSeconds }, 0, 900, 0},
		{"behavior.pvcPendingSeconds", false, "PVC_PENDING_SECONDS", func(c *Config) any { return c.Behavior.PVCPendingSeconds }, 0, 900, 300},
		{"grouping.windowSeconds", false, "", func(c *Config) any { return c.Grouping.WindowSeconds }, 0, nil, 30},
		{"aws.pollSeconds", true, "AWS_POLL_SECONDS", func(c *Config) any { return c.AWS.PollSeconds }, 0, 900, 60},
		{"azure.pollSeconds", true, "AZURE_POLL_SECONDS", func(c *Config) any { return c.Azure.PollSeconds }, 0, 900, 60},
		{"gcp.pollSeconds", true, "GCP_POLL_SECONDS", func(c *Config) any { return c.GCP.PollSeconds }, 0, 900, 60},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			section, key, _ := strings.Cut(tt.path, ".")
			omitted := section + ":\n"
			if tt.gated {
				omitted += "  enabled: true\n"
			}
			present := omitted + "  " + key + ": " + fmt.Sprint(tt.yaml) + "\n"
			apply := func(raw string) any {
				t.Helper()
				c := &Config{}
				set, err := decodeConfig([]byte(raw), c)
				if err != nil {
					t.Fatal(err)
				}
				c.applyEnvDefaults(set)
				return tt.get(c)
			}

			if tt.env != "" {
				t.Setenv(tt.env, "")
			}
			if got := apply(omitted); got != tt.def {
				t.Errorf("omitted with no env: got %v, want default %v", got, tt.def)
			}
			if tt.env == "" {
				if got := apply(present); got != tt.yaml {
					t.Errorf("set in YAML to %v: got %v", tt.yaml, got)
				}
				return
			}
			t.Setenv(tt.env, fmt.Sprint(tt.fromEnv))
			if got := apply(omitted); got != tt.fromEnv {
				t.Errorf("omitted with %s set: got %v, want %v", tt.env, got, tt.fromEnv)
			}
			if got := apply(present); got != tt.yaml {
				t.Errorf("set in YAML to %v with %s=%v: got %v, the env value overwrote it", tt.yaml, tt.env, tt.fromEnv, got)
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

func TestValidateCorrelationEnabledRejected(t *testing.T) {
	// Enabled is rejected until an engine exists, even with in-range tunables.
	on := validBaseConfig()
	on.Correlation = Correlation{Enabled: true, IntervalSeconds: 15, MaxHops: 3, BlastRadiusCap: 50}
	if err := on.Validate(); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("enabled correlation must be rejected, got %v", err)
	}
	// Disabled with junk values must not fail (unused).
	off := validBaseConfig()
	off.Correlation = Correlation{Enabled: false, IntervalSeconds: 1, MaxHops: 99}
	if err := off.Validate(); err != nil {
		t.Fatalf("disabled correlation must skip bounds: %v", err)
	}
}

// TestEscalationKeyGolden pins the escalation mark key. Snapshots persist it
// (Snapshot.Escalated), so any change to the hash input re-escalates every
// standing alert after an upgrade.
func TestEscalationKeyGolden(t *testing.T) {
	tests := []struct {
		name string
		esc  Escalation
		want string
	}{
		{
			name: "match, delay and sinks",
			esc: Escalation{
				Match:        map[string]string{"severity": "critical", "namespace": "prod"},
				AfterMinutes: 30,
				Sinks:        []string{"pagerduty", "opsgenie"},
			},
			want: "c89bc8693904f82c",
		},
		{
			name: "no match",
			esc:  Escalation{AfterMinutes: 15, Sinks: []string{"slack"}},
			want: "f5e38d6476127f45",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EscalationKey(tt.esc); got != tt.want {
				t.Fatalf("EscalationKey = %q, want %q", got, tt.want)
			}
		})
	}
}
