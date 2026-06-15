package config

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLoad feeds arbitrary bytes to the YAML config loader. The contract is
// simple but important: Load must never panic on malformed or hostile input —
// it either returns a valid *Config or a non-nil error. A panic here would crash
// the controller at startup on a bad ConfigMap.
func FuzzLoad(f *testing.F) {
	seeds := []string{
		"cluster: prod\nbehavior:\n  muteSeconds: 600\n  resolveTTLSeconds: 600\n  pvcPendingSeconds: 300\n",
		"routing:\n  - match: {severity: critical}\n    sinks: [slack, pagerduty]\n",
		"silences:\n  - matchers: {namespace: kube-system}\n    until: \"2026-06-15T00:00:00Z\"\n",
		"inhibitions:\n  - source: {kind: Node}\n    target: {kind: Pod}\n    duration: 10m\n",
		"",
		"::: not yaml :::",
		"behavior: {muteSeconds: -1}",
		"routing: [{match: {}, sinks: [bogus]}]",
		"grouping: {enabled: true, windowSeconds: 0}",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Skip()
		}
		// Only assertion: Load does not panic. A nil error must yield a non-nil
		// Config; a non-nil error must yield a nil Config.
		cfg, err := Load(p)
		if err == nil && cfg == nil {
			t.Fatalf("Load returned (nil, nil) for input %q", data)
		}
		if err != nil && cfg != nil {
			t.Fatalf("Load returned both a config and an error for input %q: %v", data, err)
		}
	})
}
