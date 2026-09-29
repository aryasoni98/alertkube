package app

import (
	"fmt"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/config"
	"github.com/aryasoni98/alertkube/v2/internal/group"
	"github.com/aryasoni98/alertkube/v2/internal/router"
	"github.com/aryasoni98/alertkube/v2/internal/rules"
	"github.com/aryasoni98/alertkube/v2/internal/sinks"
)

// pipelineHarness wires a store + router + registry + emitter + rules engine
// the way runController does, but with a recording sink so tests can assert
// what actually reached delivery.
type pipelineHarness struct {
	store *alert.Store
	emit  func(*alert.Alert)
	sink  *testSink
	reg   *sinks.Registry
}

// defaultSinks is the router's fallback for alerts no route matches; pass
// defaultRoute to mirror runController, or nil so an unmatched alert is dropped.
func newPipeline(t *testing.T, cfg *config.Config, defaultSinks []string) *pipelineHarness {
	t.Helper()
	return newPipelineWindows(t, cfg, defaultSinks,
		time.Duration(cfg.Behavior.MuteSeconds)*time.Second,
		time.Duration(cfg.Behavior.ResolveTTLSeconds)*time.Second)
}

// newPipelineWindows takes the store's mute window and resolve TTL directly
// so a test can run them in milliseconds.
func newPipelineWindows(t *testing.T, cfg *config.Config, defaultSinks []string, mute, resolveTTL time.Duration) *pipelineHarness {
	t.Helper()
	sink := &testSink{name: "slack"}
	reg := sinks.NewRegistry()
	reg.Add(sink)
	r := router.New(cfg.Routing, cfg.Inhibitions, cfg.Silences, defaultSinks)

	enqueue := syncEnqueue(reg)
	store := alert.NewStore(mute, resolveTTL, makeResolver(r, nil, enqueue))
	var engine *rules.Engine
	emit := makeEmitter(store, r, enqueue, cfg, nil, func(a *alert.Alert) { engine.Observe(a) })
	engine = rules.New(cfg.Rules, emit)
	return &pipelineHarness{store: store, emit: emit, sink: sink, reg: reg}
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
	h := newPipeline(t, cfg, defaultRoute)

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
	h := newPipeline(t, cfg, defaultRoute)

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
	// No default sinks: a critical alert matches no route and is dropped, so
	// delivery proves the override demoted it before routing ran.
	h := newPipeline(t, cfg, nil)
	h.emit(alert.New(alert.KindPod, "ns", "p1", "ImagePullBackOff", alert.SeverityCritical))
	if h.sink.count() != 1 {
		t.Fatalf("override+route should deliver, got %d", h.sink.count())
	}
	if h.sink.got[0].Severity != alert.SeverityWarning {
		t.Fatalf("severity override not applied: %s", h.sink.got[0].Severity)
	}
}

// An alert.Event dispatches once to non-stateful sinks, never reaches a
// stateful incident sink (pagerduty), never enters the active set, and is
// deduped by fingerprint on re-emit.
func TestPipeline_EventDispatchedOnceNeverActive(t *testing.T) {
	cfg := basePipelineConfig()
	cfg.Routing = []config.Route{{Match: map[string]string{}, Sinks: []string{"slack", "pagerduty"}}}
	h := newPipeline(t, cfg, defaultRoute)
	pd := &testSink{name: "pagerduty"}
	h.reg.Add(pd)

	ev := alert.New(alert.KindCloudTrailEvent, "us-east-1", "evt-1", "SecurityGroupChanged", alert.SeverityWarning)
	ev.Event = true
	h.emit(ev)
	if h.sink.count() != 1 {
		t.Fatalf("event should dispatch once, got %d", h.sink.count())
	}
	if pd.count() != 0 {
		t.Fatalf("event must not reach the stateful pagerduty sink, got %d", pd.count())
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

	sink := &testSink{name: "slack"}
	reg := sinks.NewRegistry()
	reg.Add(sink)
	r := router.New(cfg.Routing, cfg.Inhibitions, cfg.Silences, []string{"slack"})
	store := alert.NewStore(600*time.Second, 600*time.Second, nil)
	enqueue := syncEnqueue(reg)
	grouper := buildGrouper(cfg.Grouping, r, enqueue)
	emit := makeEmitter(store, r, enqueue, cfg, grouper, nil)

	// First alert of the group passes; the rest fold into the pending summary.
	for i := range 5 {
		emit(alert.New(alert.KindPod, "ns", "p"+string(rune('a'+i)), "CrashLoopBackOff", alert.SeverityCritical))
	}
	if c := sink.count(); c != 1 {
		t.Fatalf("only the first of a group should pass immediately, got %d", c)
	}
	// Absorbed chat-only members are muted but never active: no sink got
	// them individually, so none may receive a resolve for them.
	if store.ActiveCount() != 1 {
		t.Fatalf("absorbed chat-only alerts must not become active, active=%d", store.ActiveCount())
	}
	if fp := alert.ComputeFingerprint(alert.KindPod, "ns", "pb", "CrashLoopBackOff"); !store.Muted(fp) {
		t.Fatal("absorbed chat-only alert must enter the mute window")
	}
	// Flushing the window emits the summary for the absorbed members.
	grouper.FlushAll()
	if c := sink.count(); c != 2 {
		t.Fatalf("window flush should emit one summary, got %d", c)
	}
}

// Watchers re-emit on every Update and resync, and cloud sources on every
// poll. An absorbed chat-only member must stay muted like any delivered
// alert: a re-fire neither joins the summary again nor, once the window has
// closed, opens a new one and pages chat on its own.
func TestPipeline_GroupingRefireOfAbsorbedMembers(t *testing.T) {
	const window = 50 * time.Millisecond
	tests := []struct {
		name  string
		close func(*group.Grouper) // runs between the storm and the re-fires
	}{
		{name: "inside the window", close: func(*group.Grouper) {}},
		{name: "after FlushAll", close: (*group.Grouper).FlushAll},
		{name: "after the window expires", close: func(*group.Grouper) { time.Sleep(2 * window) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := basePipelineConfig()
			sink := &testSink{name: "slack"}
			reg := sinks.NewRegistry()
			reg.Add(sink)
			reg.SetRate("slack", rate.Limit(100000), 100000) // a regression pages fast, not after 1/s waits
			r := router.New(cfg.Routing, cfg.Inhibitions, cfg.Silences, []string{"slack"})
			store := alert.NewStore(600*time.Second, 600*time.Second, nil)
			enqueue := syncEnqueue(reg)
			// Mirrors buildGrouper, with a window short enough to expire.
			grouper := group.New(window, []string{"kind", "reason", "severity"}, func(s *alert.Alert) {
				if route := dropStateful(r.Route(s)); len(route) > 0 {
					enqueue(s, route, nil)
				}
			})
			emit := makeEmitter(store, r, enqueue, cfg, grouper, nil)
			storm := func() {
				for _, name := range []string{"pa", "pb", "pc", "pd", "pe"} {
					emit(alert.New(alert.KindPod, "ns", name, "CrashLoopBackOff", alert.SeverityCritical))
				}
			}

			storm()
			tt.close(grouper)
			storm()
			storm()
			grouper.FlushAll() // emit whatever summary is still pending

			var pages, summaries []*alert.Alert
			for _, a := range sink.got {
				if a.Labels["alertkube-grouped"] == "true" {
					summaries = append(summaries, a)
				} else {
					pages = append(pages, a)
				}
			}
			if len(pages) != 1 || pages[0].Name != "pa" {
				t.Fatalf("only the first alert may page chat individually inside muteSeconds, got %d pages", len(pages))
			}
			if len(summaries) != 1 {
				t.Fatalf("want one summary, got %d", len(summaries))
			}
			if got, want := summaries[0].Details["Grouped Resources"], "ns/pb\nns/pc\nns/pd\nns/pe"; got != want {
				t.Fatalf("summary members = %q, want %q (each member once)", got, want)
			}
			if summaries[0].Name != "4-grouped" {
				t.Fatalf("summary count must not grow with re-fires, got %s", summaries[0].Name)
			}
			if store.ActiveCount() != 1 {
				t.Fatalf("absorbed chat-only alerts must not become active, active=%d", store.ActiveCount())
			}
		})
	}
}

func TestPipeline_AbsorbedFireReachesStatefulSink(t *testing.T) {
	cfg := basePipelineConfig()
	cfg.Grouping.Enabled = true
	cfg.Grouping.WindowSeconds = 60
	cfg.Grouping.By = []string{"kind", "reason", "severity"}
	cfg.Routing = []config.Route{{Match: map[string]string{}, Sinks: []string{"slack", "pagerduty"}}}

	slack := &testSink{name: "slack"}
	pd := &testSink{name: "pagerduty"}
	reg := sinks.NewRegistry()
	reg.Add(slack)
	reg.Add(pd)
	r := router.New(cfg.Routing, cfg.Inhibitions, cfg.Silences, []string{"slack"})
	store := alert.NewStore(600*time.Second, 600*time.Second, nil)
	enqueue := syncEnqueue(reg)
	grouper := buildGrouper(cfg.Grouping, r, enqueue)
	emit := makeEmitter(store, r, enqueue, cfg, grouper, nil)

	emit(alert.New(alert.KindPod, "ns", "pa", "CrashLoopBackOff", alert.SeverityCritical))
	emit(alert.New(alert.KindPod, "ns", "pb", "CrashLoopBackOff", alert.SeverityCritical))

	if slack.count() != 1 {
		t.Fatalf("slack should see only the first fire, got %d", slack.count())
	}
	if pd.count() != 2 {
		t.Fatalf("pagerduty should see both fires, got %d", pd.count())
	}
	if store.ActiveCount() != 2 {
		t.Fatalf("both stateful fires must be tracked, active=%d", store.ActiveCount())
	}
}

// The resolve path applies the same grouping gate as the fire path: the first
// resolve of a group reaches every routed sink, an absorbed one only the
// stateful sinks that must close the member's incident.
func TestMakeResolver_AbsorbedResolveReachesOnlyStatefulSinks(t *testing.T) {
	cfg := basePipelineConfig()
	cfg.Grouping.Enabled = true
	cfg.Grouping.WindowSeconds = 60
	cfg.Grouping.By = []string{"kind", "reason", "severity"}
	cfg.Routing = []config.Route{{Match: map[string]string{}, Sinks: []string{"slack", "pagerduty"}}}

	slack := &testSink{name: "slack"}
	pd := &testSink{name: "pagerduty"}
	reg := sinks.NewRegistry()
	reg.Add(slack)
	reg.Add(pd)
	r := router.New(cfg.Routing, cfg.Inhibitions, cfg.Silences, []string{"slack"})
	enqueue := syncEnqueue(reg)
	resolve := makeResolver(r, buildGrouper(cfg.Grouping, r, enqueue), enqueue)

	for _, name := range []string{"pa", "pb"} {
		a := alert.New(alert.KindPod, "ns", name, "CrashLoopBackOff", alert.SeverityCritical)
		a.Resolved = true
		resolve(a)
	}

	if slack.count() != 1 {
		t.Fatalf("slack should see only the first resolve, got %d", slack.count())
	}
	if pd.count() != 2 {
		t.Fatalf("pagerduty should see both resolves, got %d", pd.count())
	}
}

// A derived alert stays active only while its rule keeps re-emitting it: each
// muted re-emit Touches the record. A storm that outlasts resolveTTL must not
// resolve the derived alert, and it must page once per mute window.
func TestPipeline_DerivedRuleStaysActivePastResolveTTL(t *testing.T) {
	const (
		mute = 500 * time.Millisecond
		ttl  = 100 * time.Millisecond
		tick = 10 * time.Millisecond
	)
	cfg := basePipelineConfig()
	cfg.Rules = []config.Rule{{
		Name:          "storm",
		Severity:      "critical",
		WindowSeconds: 300,
		Count:         &config.RuleCount{Match: map[string]string{"reason": "CrashLoopBackOff"}, Threshold: 3},
	}}
	h := newPipelineWindows(t, cfg, defaultRoute, mute, ttl)
	h.reg.SetRate("slack", rate.Limit(100000), 100000) // every crashing pod pages

	// A new pod crashes every tick; the sweeper runs between crashes.
	start := time.Now()
	n := 0
	crash := func() {
		h.emit(alert.New(alert.KindPod, "ns", fmt.Sprintf("p%d", n), "CrashLoopBackOff", alert.SeverityCritical))
		n++
		h.store.SweepResolved()
		time.Sleep(tick)
	}

	for time.Since(start) < 3*ttl {
		crash()
	}
	if fires, resolves := h.sink.derived(); fires != 1 || resolves != 0 {
		t.Fatalf("storm past resolveTTL: derived fires=%d resolves=%d, want 1 and 0", fires, resolves)
	}

	// The rule is still true when the mute window lapses, so it pages again.
	for deadline := start.Add(mute + time.Second); time.Now().Before(deadline); {
		crash()
		if fires, _ := h.sink.derived(); fires == 2 {
			if elapsed := time.Since(start); elapsed < mute {
				t.Fatalf("derived alert re-paged after %v, inside the %v mute window", elapsed, mute)
			}
			break
		}
	}
	if fires, resolves := h.sink.derived(); fires != 2 || resolves != 0 {
		t.Fatalf("after the mute window: derived fires=%d resolves=%d, want 2 and 0", fires, resolves)
	}
}

// A derived alert emitted during startup grace is only seeded. The rule keeps
// re-emitting while it holds, so the page goes out after grace instead of
// being lost. CloudTrail events bypass grace, so they reach the rule engine
// during it.
func TestPipeline_DerivedRuleSeededInGracePagesAfterGrace(t *testing.T) {
	cfg := basePipelineConfig()
	cfg.Behavior.StartupGraceSeconds = 1
	cfg.Rules = []config.Rule{{
		Name:          "sg-churn",
		Severity:      "warning",
		WindowSeconds: 300,
		Count:         &config.RuleCount{Match: map[string]string{"reason": "SecurityGroupChanged"}, Threshold: 1},
	}}
	// The mute window is shorter than grace, so the seed has lapsed by then.
	h := newPipelineWindows(t, cfg, defaultRoute, 100*time.Millisecond, time.Minute)
	graceEnd := time.Now().Add(time.Duration(cfg.Behavior.StartupGraceSeconds) * time.Second)

	n := 0
	event := func() {
		ev := alert.New(alert.KindCloudTrailEvent, "us-east-1", fmt.Sprintf("evt-%d", n), "SecurityGroupChanged", alert.SeverityWarning)
		ev.Event = true
		n++
		h.emit(ev)
	}

	event()
	if fires, _ := h.sink.derived(); fires != 0 {
		t.Fatalf("derived alert during startup grace must be seeded, not paged, got %d", fires)
	}
	time.Sleep(time.Until(graceEnd))
	event()
	if fires, _ := h.sink.derived(); fires != 1 {
		t.Fatalf("derived alert seeded during grace must page after grace, got %d", fires)
	}
}

// ---- delivery budget ----

// TestDeliveryBudgetNests pins the outer link of the budget described at
// dispatchTimeout: one fan-out must outlast one sink's send, or a slow sink
// is cut before its own retries finish. internal/sinks pins the inner links
// (slow threshold < httpx.DefaultTimeout < perSinkTimeout).
func TestDeliveryBudgetNests(t *testing.T) {
	if sinks.PerSinkTimeout >= dispatchTimeout {
		t.Fatalf("sinks.PerSinkTimeout %s >= dispatchTimeout %s", sinks.PerSinkTimeout, dispatchTimeout)
	}
}
