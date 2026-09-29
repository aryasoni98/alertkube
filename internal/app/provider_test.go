package app

import (
	"testing"

	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/sources"
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
		bound := p.Bind(cfg)
		if !bound.Enabled {
			t.Errorf("%s: Enabled should be true when its section is enabled", name)
		}
		if bound.PollSeconds != 42 {
			t.Errorf("%s: PollSeconds = %d, want 42", name, bound.PollSeconds)
		}
		if bound.Build == nil {
			t.Errorf("%s: Bind must supply Build", name)
		}
		// A zero-value config must report disabled.
		if p.Bind(&config.Config{}).Enabled {
			t.Errorf("%s: Enabled should be false for a zero config", name)
		}
	}
}
