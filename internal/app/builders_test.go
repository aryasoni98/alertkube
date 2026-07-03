package app

import (
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"alertkube/internal/config"
	"alertkube/internal/watchers"
)

func testConfig() *config.Config {
	cfg := &config.Config{Cluster: "test-cluster"}
	cfg.Channels.Critical = "alerts-critical"
	cfg.Channels.Warning = "alerts-warning"
	cfg.Channels.Info = "alerts-info"
	return cfg
}

func TestBuildSinks(t *testing.T) {
	cfg := testConfig()
	cfg.SinkRates = map[string]config.SinkRate{
		"slack": {PerSecond: 2, Burst: 3},
	}

	reg := buildSinks(cfg)
	if reg == nil {
		t.Fatal("buildSinks returned nil registry")
	}
	// Every routable sink name must be registered (routing config validation
	// references config.KnownSinks; the registry must back all of them).
	for name := range config.KnownSinks {
		if !reg.Has(name) {
			t.Errorf("expected sink %q to be registered", name)
		}
	}
}

// TestKnownSinksMatchesRegistry pins config.KnownSinks (used by routing/
// escalation validation) to the sinks actually registered by buildSinks. The
// two are declared in separate files, so without this guard adding a sink to
// one but not the other silently breaks routing: a registered-but-not-known
// sink fails config validation, and a known-but-unregistered sink is skipped
// by dispatch with no delivery. They must be exactly equal.
func TestKnownSinksMatchesRegistry(t *testing.T) {
	reg := buildSinks(testConfig())
	registered := map[string]bool{}
	for _, n := range reg.Names() {
		registered[n] = true
	}
	for name := range config.KnownSinks {
		if !registered[name] {
			t.Errorf("config.KnownSinks has %q but buildSinks does not register it (dispatch would silently skip it)", name)
		}
	}
	for name := range registered {
		if !config.KnownSinks[name] {
			t.Errorf("buildSinks registers %q but config.KnownSinks omits it (routing to it would fail validation)", name)
		}
	}
}

func TestBuildWatchers_ClusterScopeIncludesNode(t *testing.T) {
	c := fake.NewSimpleClientset()
	cfg := testConfig()

	cluster := buildWatchers(c, cfg, "")
	ns := buildWatchers(c, cfg, "team-a")

	if len(cluster) != len(ns)+1 {
		t.Fatalf("cluster scope should add exactly the node watcher: cluster=%d ns=%d", len(cluster), len(ns))
	}

	// The node watcher must be present cluster-wide and absent namespace-scoped
	// (nodes are cluster-scoped resources).
	if !hasWatcher(cluster, "node") {
		t.Error("cluster-scoped watchers should include the Node watcher")
	}
	if hasWatcher(ns, "node") {
		t.Error("namespace-scoped watchers must not include the Node watcher")
	}

	// Every watcher must have a non-empty, unique name.
	seen := map[string]bool{}
	for _, w := range cluster {
		n := w.Name()
		if n == "" {
			t.Error("watcher has empty Name()")
		}
		if seen[n] {
			t.Errorf("duplicate watcher name %q", n)
		}
		seen[n] = true
	}
}

func hasWatcher(ws []watchers.Watcher, name string) bool {
	for _, w := range ws {
		if w.Name() == name {
			return true
		}
	}
	return false
}
