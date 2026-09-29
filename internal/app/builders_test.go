package app

import (
	"testing"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/aryasoni98/alertkube/v2/internal/config"
	"github.com/aryasoni98/alertkube/v2/internal/watchers"
)

func testConfig() *config.Config {
	cfg := &config.Config{Cluster: "test-cluster"}
	cfg.Channels.Critical = "alerts-critical"
	cfg.Channels.Warning = "alerts-warning"
	cfg.Channels.Info = "alerts-info"
	return cfg
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

func TestApplyClientThrottle_Defaults(t *testing.T) {
	t.Setenv("ALERTKUBE_CLIENT_QPS", "")
	t.Setenv("ALERTKUBE_CLIENT_BURST", "")
	cfg := &rest.Config{}
	applyClientThrottle(cfg)
	if cfg.QPS != float32(defaultClientQPS) || cfg.Burst != defaultClientBurst {
		t.Fatalf("defaults not applied: qps=%v burst=%d", cfg.QPS, cfg.Burst)
	}
}

func TestApplyClientThrottle_Override(t *testing.T) {
	t.Setenv("ALERTKUBE_CLIENT_QPS", "200")
	t.Setenv("ALERTKUBE_CLIENT_BURST", "400")
	cfg := &rest.Config{}
	applyClientThrottle(cfg)
	if cfg.QPS != 200 || cfg.Burst != 400 {
		t.Fatalf("overrides not applied: qps=%v burst=%d", cfg.QPS, cfg.Burst)
	}
}
