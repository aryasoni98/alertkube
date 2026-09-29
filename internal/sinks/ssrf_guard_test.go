package sinks

import (
	"context"
	"strings"
	"testing"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
)

// TestRegisteredSinksGuardDestination is the registry-level guard. A sink that
// dials an operator-supplied URL must refuse a link-local destination, and a
// failed dial must not quote the URL, whose path is where webhook secrets
// live. Sinks that only talk to a fixed vendor host, or that do not dial, are
// listed so a new registration cannot skip the classification.
func TestRegisteredSinksGuardDestination(t *testing.T) {
	dests := []struct{ name, url string }{
		{"link-local", "http://169.254.169.254/latest/meta-data/SECRET"},
		// A refused connect reaches the transport, so its error is the one
		// that could quote the URL.
		{"connect-refused", "http://127.0.0.1:1/services/T0/B1/SECRET"},
	}
	urlEnv := map[string][]string{
		"slack":      {"SLACK_WEBHOOK_URL"},
		"discord":    {"DISCORD_WEBHOOK_URL"},
		"googlechat": {"GOOGLECHAT_WEBHOOK_URL"},
		"mattermost": {"MATTERMOST_WEBHOOK_URL"},
		"teams":      {"TEAMS_WEBHOOK_URL"},
		"webhook":    {"GENERIC_WEBHOOK_URL"},
		"opsgenie":   {"OPSGENIE_API_KEY", "OPSGENIE_API_URL"},
	}
	fixedHost := map[string]bool{"stdout": true, "pagerduty": true, "telegram": true}

	reg := BuildDefault(SinkConfig{Cluster: "c"})
	a := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	for _, name := range reg.Names() {
		envs, isURL := urlEnv[name]
		if !isURL && !fixedHost[name] {
			t.Errorf("sink %q has no SSRF classification", name)
			continue
		}
		if !isURL {
			continue
		}
		for _, dest := range dests {
			t.Run(name+"/"+dest.name, func(t *testing.T) {
				if name == "slack" {
					t.Setenv("SLACK_BOT_TOKEN", "")
				}
				for _, env := range envs {
					if strings.HasSuffix(env, "_API_KEY") {
						t.Setenv(env, "test-key")
						continue
					}
					t.Setenv(env, dest.url)
				}
				err := reg.TestSend(context.Background(), name, a)
				if err == nil {
					t.Fatalf("%s destination was accepted", dest.name)
				}
				if strings.Contains(err.Error(), "SECRET") {
					t.Fatalf("error leaks the destination secret: %v", err)
				}
			})
		}
	}
}
