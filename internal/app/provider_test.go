package app

import (
	"testing"

	"alertkube/internal/config"
	"alertkube/internal/sources"
)

func TestCloudProvidersSelfRegister(t *testing.T) {
	got := map[string]sources.Provider{}
	for _, p := range sources.Providers() {
		got[p.Name] = p
	}
	for _, name := range []string{"aws", "azure", "gcp"} {
		if _, ok := got[name]; !ok {
			t.Errorf("cloud provider %q did not self-register", name)
		}
	}

	// Each provider's Enabled/PollSeconds must read its own config section.
	for name, p := range got {
		cfg := &config.Config{}
		switch name {
		case "aws":
			cfg.AWS.Enabled, cfg.AWS.PollSeconds = true, 42
		case "azure":
			cfg.Azure.Enabled, cfg.Azure.PollSeconds = true, 42
		case "gcp":
			cfg.GCP.Enabled, cfg.GCP.PollSeconds = true, 42
		default:
			continue
		}
		if !p.Enabled(cfg) {
			t.Errorf("%s: Enabled should be true when its section is enabled", name)
		}
		if p.PollSeconds(cfg) != 42 {
			t.Errorf("%s: PollSeconds = %d, want 42", name, p.PollSeconds(cfg))
		}
		// A zero-value config must report disabled.
		if p.Enabled(&config.Config{}) {
			t.Errorf("%s: Enabled should be false for a zero config", name)
		}
	}
}
