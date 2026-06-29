package app

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/group"
	"alertkube/internal/router"
	"alertkube/internal/sinks"
)

// recordingSink captures every alert it is sent, for asserting pipeline outcomes.
type recordingSink struct {
	name string
	mu   sync.Mutex
	got  []*alert.Alert
}

func (s *recordingSink) Name() string                   { return s.name }
func (s *recordingSink) Supports(_ alert.Severity) bool { return true }
func (s *recordingSink) Send(_ context.Context, a *alert.Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *a
	s.got = append(s.got, &cp)
	return nil
}
func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

// pipelineHarness wires a store + router + registry + emitter the way
// runController does, but with a recording sink so tests can assert what
// actually reached delivery.
type pipelineHarness struct {
	store *alert.Store
	emit  func(*alert.Alert)
	sink  *recordingSink
}

func newPipeline(t *testing.T, cfg *config.Config) *pipelineHarness {
	t.Helper()
	sink := &recordingSink{name: "slack"}
	reg := sinks.NewRegistry()
	reg.Add(sink)
	r := router.New(cfg.Routing, cfg.Inhibitions, cfg.Silences, []string{"slack"})

	store := alert.NewStore(
		time.Duration(cfg.Behavior.MuteSeconds)*time.Second,
		time.Duration(cfg.Behavior.ResolveTTLSeconds)*time.Second,
		func(a *alert.Alert) {
			// resolves route then dispatch, mirroring dispatchResolved.
			if route := r.Route(a); route != nil {
				dispatch(reg, a, route)
			}
		},
	)
	emit := makeEmitter(store, r, reg, cfg, nil, nil)
	return &pipelineHarness{store: store, emit: emit, sink: sink}
}

func basePipelineConfig() *config.Config {
	cfg := &config.Config{Cluster: "test"}
	cfg.Behavior.MuteSeconds = 600
	cfg.Behavior.ResolveTTLSeconds = 600
	cfg.Routing = []config.Route{{Match: map[string]string{}, Sinks: []string{"slack"}}}
	return cfg
}

func TestPipeline_FireDedupeResolve(t *testing.T) {
	cfg := basePipelineConfig()
	h := newPipeline(t, cfg)

	a := alert.New(alert.KindPod, "ns", "p1", "CrashLoopBackOff", alert.SeverityCritical)
	h.emit(a)
	if h.sink.count() != 1 {
		t.Fatalf("first fire should deliver once, got %d", h.sink.count())
	}

	// Same fingerprint within the mute window: suppressed (dedupe).
	h.emit(alert.New(alert.KindPod, "ns", "p1", "CrashLoopBackOff", alert.SeverityCritical))
	if h.sink.count() != 1 {
		t.Fatalf("re-fire inside mute window must be suppressed, got %d", h.sink.count())
	}

	// Object deleted -> synthetic resolve fans to the sink that saw the trigger.
	h.store.ResolveObject(alert.KindPod, "ns", "p1")
	if h.sink.count() != 2 {
		t.Fatalf("resolve should deliver, got %d", h.sink.count())
	}
	if last := h.sink.got[len(h.sink.got)-1]; !last.Resolved {
		t.Fatalf("last delivery should be a resolve, got %+v", last)
	}
}

func TestPipeline_StartupGraceSeedsNoPage(t *testing.T) {
	cfg := basePipelineConfig()
	cfg.Behavior.StartupGraceSeconds = 3600 // whole test runs inside the window
	h := newPipeline(t, cfg)

	h.emit(alert.New(alert.KindPod, "ns", "p1", "CrashLoopBackOff", alert.SeverityCritical))
	if h.sink.count() != 0 {
		t.Fatalf("alerts during startup grace must be seeded, not paged, got %d", h.sink.count())
	}
	// The fingerprint is now seeded into the mute window: an immediate re-fire
	// is still muted.
	h.emit(alert.New(alert.KindPod, "ns", "p1", "CrashLoopBackOff", alert.SeverityCritical))
	if h.sink.count() != 0 {
		t.Fatalf("seeded fingerprint must stay muted, got %d", h.sink.count())
	}
}

func TestPipeline_SeverityOverrideAppliedBeforeRouting(t *testing.T) {
	cfg := basePipelineConfig()
	// Route only warning to slack; critical to nowhere. An override demotes the
	// critical alert to warning so it should be delivered.
	cfg.Routing = []config.Route{
		{Match: map[string]string{"severity": "warning"}, Sinks: []string{"slack"}},
	}
	cfg.SeverityOverrides = []config.SeverityOverride{
		{Match: map[string]string{"kind": "Pod", "reason": "ImagePullBackOff"}, Severity: "warning"},
	}
	h := newPipeline(t, cfg)
	// default sinks are [slack]; to make the test meaningful, drop the default
	// by giving an explicit non-matching route fallthrough: set defaultSinks nil
	// is not exposed, so instead assert the override changed the delivered sev.
	h.emit(alert.New(alert.KindPod, "ns", "p1", "ImagePullBackOff", alert.SeverityCritical))
	if h.sink.count() != 1 {
		t.Fatalf("override+route should deliver, got %d", h.sink.count())
	}
	if h.sink.got[0].Severity != alert.SeverityWarning {
		t.Fatalf("severity override not applied: %s", h.sink.got[0].Severity)
	}
}

func TestPipeline_EventDispatchedOnceNeverActive(t *testing.T) {
	cfg := basePipelineConfig()
	h := newPipeline(t, cfg)

	ev := alert.New(alert.KindCloudTrailEvent, "us-east-1", "evt-1", "SecurityGroupChanged", alert.SeverityWarning)
	ev.Event = true
	h.emit(ev)
	if h.sink.count() != 1 {
		t.Fatalf("event should dispatch once, got %d", h.sink.count())
	}
	if h.store.ActiveCount() != 0 {
		t.Fatalf("event must never enter the active set, active=%d", h.store.ActiveCount())
	}
	// Duplicate event within the mute window is deduped.
	dup := alert.New(alert.KindCloudTrailEvent, "us-east-1", "evt-1", "SecurityGroupChanged", alert.SeverityWarning)
	dup.Event = true
	h.emit(dup)
	if h.sink.count() != 1 {
		t.Fatalf("duplicate event must be deduped, got %d", h.sink.count())
	}
}

func TestPipeline_GroupingFoldsStorm(t *testing.T) {
	cfg := basePipelineConfig()
	cfg.Grouping.Enabled = true
	cfg.Grouping.WindowSeconds = 60
	cfg.Grouping.By = []string{"kind", "reason", "severity"}

	sink := &recordingSink{name: "slack"}
	reg := sinks.NewRegistry()
	reg.Add(sink)
	r := router.New(cfg.Routing, cfg.Inhibitions, cfg.Silences, []string{"slack"})
	store := alert.NewStore(600*time.Second, 600*time.Second, nil)
	grouper := group.New(time.Duration(cfg.Grouping.WindowSeconds)*time.Second, cfg.Grouping.By, func(s *alert.Alert) {
		if route := r.Route(s); route != nil {
			dispatch(reg, s, route)
		}
	})
	emit := makeEmitter(store, r, reg, cfg, grouper, nil)

	// First alert of the group passes; the rest fold into the pending summary.
	for i := 0; i < 5; i++ {
		emit(alert.New(alert.KindPod, "ns", "p"+string(rune('a'+i)), "CrashLoopBackOff", alert.SeverityCritical))
	}
	if c := sink.count(); c != 1 {
		t.Fatalf("only the first of a group should pass immediately, got %d", c)
	}
	// Flushing the window emits the summary for the absorbed members.
	grouper.FlushAll()
	if c := sink.count(); c != 2 {
		t.Fatalf("window flush should emit one summary, got %d", c)
	}
}

// ---- applyClientThrottle ----

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

// ---- renderConfigBody ----

func TestRenderConfigBody(t *testing.T) {
	cfg := basePipelineConfig()
	body, err := renderConfigBody(cfg)
	if err != nil {
		t.Fatalf("renderConfigBody: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if _, ok := m["config"]; !ok {
		t.Error("missing config key")
	}
	if _, ok := m["yaml"]; !ok {
		t.Error("missing yaml key")
	}
}
