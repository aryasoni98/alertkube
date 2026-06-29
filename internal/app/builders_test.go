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
