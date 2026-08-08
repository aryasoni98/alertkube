package rules

import (
	"context"
	"testing"
	"time"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
)

func collect() (Emit, *[]*alert.Alert) {
	var got []*alert.Alert
	return func(a *alert.Alert) { got = append(got, a) }, &got
}

func podAlert(ns, name, reason string) *alert.Alert {
	return alert.New(alert.KindPod, ns, name, reason, alert.SeverityWarning)
}

func TestCountRuleFiresAtThreshold(t *testing.T) {
	emit, got := collect()
	e := New([]config.Rule{{
		Name:          "storm",
		Severity:      "critical",
		WindowSeconds: 300,
		Count:         &config.RuleCount{Match: map[string]string{"reason": "CrashLoopBackOff"}, Threshold: 3},
	}}, emit)

	e.Observe(podAlert("prod", "p1", "CrashLoopBackOff"))
	e.Observe(podAlert("prod", "p2", "CrashLoopBackOff"))
	if len(*got) != 0 {
		t.Fatalf("rule should not fire below threshold, got %d", len(*got))
	}
	e.Observe(podAlert("prod", "p3", "CrashLoopBackOff"))
	if len(*got) == 0 {
		t.Fatal("rule should fire at threshold")
	}
	d := (*got)[0]
	if d.Kind != alert.KindDerived || d.Name != "storm" || d.Severity != alert.SeverityCritical {
		t.Errorf("bad derived alert: kind=%s name=%s sev=%s", d.Kind, d.Name, d.Severity)
	}
	if d.Labels["rule"] != "storm" {
		t.Errorf("derived alert missing rule label: %v", d.Labels)
	}
}

func TestCountRuleWindowExpiry(t *testing.T) {
	emit, got := collect()
	now := time.Now()
	e := New([]config.Rule{{
		Name:          "storm",
		Severity:      "warning",
		WindowSeconds: 60,
		Count:         &config.RuleCount{Match: map[string]string{"reason": "X"}, Threshold: 3},
	}}, emit)
	e.now = func() time.Time { return now }

	e.Observe(podAlert("ns", "p", "X"))
	e.Observe(podAlert("ns", "p", "X")) // count = 2 at t0
	now = now.Add(120 * time.Second)    // both fall outside the 60s window
	e.Observe(podAlert("ns", "p", "X")) // old two pruned -> count = 1
	if len(*got) != 0 {
		t.Fatalf("expired matches must not reach the threshold, got %d", len(*got))
	}
}

func TestAllRuleFiresWhenAllGroupsActive(t *testing.T) {
	emit, got := collect()
	e := New([]config.Rule{{
		Name:          "composite",
		Severity:      "critical",
		WindowSeconds: 300,
		All: []map[string]string{
			{"kind": "Node", "reason": "NodeNotReady"},
			{"kind": "Pod", "reason": "CrashLoopBackOff"},
		},
	}}, emit)

	e.Observe(alert.New(alert.KindNode, "", "node1", "NodeNotReady", alert.SeverityCritical))
	if len(*got) != 0 {
		t.Fatal("one active group should not fire a composite rule")
	}
	e.Observe(podAlert("prod", "p", "CrashLoopBackOff"))
	if len(*got) == 0 {
		t.Fatal("both groups active should fire the composite rule")
	}
}

func TestAbsentRuleHeartbeat(t *testing.T) {
	emit, got := collect()
	now := time.Now()
	e := New([]config.Rule{{
		Name:     "watchdog",
		Severity: "critical",
		Absent:   &config.RuleAbsent{Match: map[string]string{"name": "watchdog"}, ForSeconds: 600},
	}}, emit)
	e.now = func() time.Time { return now }
	e.start = now

	now = now.Add(500 * time.Second) // still within the post-boot grace window
	e.evalAbsent()
	if len(*got) != 0 {
		t.Fatalf("absent rule should not fire within the grace window, got %d", len(*got))
	}
	now = now.Add(200 * time.Second) // 700s since start > 600s
	e.evalAbsent()
	if len(*got) == 0 {
		t.Fatal("absent rule should fire after the window with no match")
	}

	*got = nil
	e.Observe(alert.New(alert.KindExternal, "", "watchdog", "heartbeat", alert.SeverityInfo))
	e.evalAbsent() // a match just arrived
	if len(*got) != 0 {
		t.Fatalf("absent rule should not fire right after a match, got %d", len(*got))
	}
}

func TestObserveIgnoresDerivedAndResolved(t *testing.T) {
	emit, got := collect()
	e := New([]config.Rule{{
		Name:          "any",
		Severity:      "warning",
		WindowSeconds: 300,
		Count:         &config.RuleCount{Match: map[string]string{}, Threshold: 1}, // empty match = everything
	}}, emit)

	e.Observe(&alert.Alert{Kind: alert.KindDerived, Name: "self"}) // loop guard
	e.Observe(&alert.Alert{Kind: alert.KindPod, Name: "p", Resolved: true})
	if len(*got) != 0 {
		t.Fatalf("derived and resolved alerts must be ignored, got %d", len(*got))
	}
	e.Observe(podAlert("ns", "p", "R"))
	if len(*got) == 0 {
		t.Fatal("a normal alert should fire a threshold-1 rule")
	}
}

func TestRunReturnsWithoutAbsentRules(t *testing.T) {
	// Count/All rules need no timer; Run must return immediately, not block.
	e := New([]config.Rule{{Name: "c", Severity: "info", WindowSeconds: 60, Count: &config.RuleCount{Match: map[string]string{}, Threshold: 1}}}, func(*alert.Alert) {})
	done := make(chan struct{})
	go func() { e.Run(context.TODO()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run should return immediately when no Absent rules are configured")
	}
}
