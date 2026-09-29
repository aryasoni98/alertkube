package router

import (
	"testing"
	"time"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/silence"
)

func TestInhibitionsAreIsolatedByRuleAndEqualValues(t *testing.T) {
	for _, targetNS := range []string{"ns", "ns|name:other"} {
		t.Run(targetNS, func(t *testing.T) {
			r := New(nil, []config.Inhibition{
				{Source: map[string]string{"reason": "NodeNotReady"}, Target: map[string]string{"kind": "Pod"}, Equal: []string{"namespace"}},
				{Source: map[string]string{"reason": "DatabaseDown"}, Target: map[string]string{"kind": "Deployment"}, Equal: []string{"namespace"}},
			}, nil, []string{"stdout"})
			r.ArmInhibitions(alert.New(alert.KindNode, "ns", "node", "NodeNotReady", alert.SeverityCritical))
			if got := r.Route(alert.New(alert.KindDeployment, targetNS, "app", "Unavailable", alert.SeverityWarning)); len(got) == 0 {
				t.Fatal("one rule armed another rule's inhibition")
			}
			if got := r.Route(alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)); got != nil {
				t.Fatal("the matching rule must still inhibit its target")
			}
		})
	}
	r := New(nil, []config.Inhibition{{Source: map[string]string{"kind": "Node"}, Target: map[string]string{"kind": "Pod"}, Equal: []string{"namespace", "name"}}}, nil, []string{"stdout"})
	r.ArmInhibitions(alert.New(alert.KindNode, "ns|name:x", "y", "NodeNotReady", alert.SeverityCritical))
	if got := r.Route(alert.New(alert.KindPod, "ns", "x|name:y", "CrashLoopBackOff", alert.SeverityCritical)); len(got) == 0 {
		t.Fatal("delimiter-containing equal values must not collide")
	}
}

func TestRouteMatch(t *testing.T) {
	r := New(
		[]config.Route{
			{Match: map[string]string{"severity": "critical"}, Sinks: []string{"slack", "pagerduty"}},
			{Match: map[string]string{"severity": "warning"}, Sinks: []string{"slack"}},
		},
		nil, nil, []string{"stdout"},
	)
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	got := r.Route(a)
	if len(got) != 2 || got[0] != "slack" || got[1] != "pagerduty" {
		t.Fatalf("critical should route to slack+pagerduty, got %v", got)
	}
}

// TestMatcherValidationAgreesWithRouting pins that config validation and the
// router compile namespace/reason patterns the same way. A pattern validation
// accepts routes the alert it names; a pattern the router cannot compile would
// leave the route silently unmatched, so validation must reject it.
func TestMatcherValidationAgreesWithRouting(t *testing.T) {
	cases := []struct {
		name, key, pattern, value string
		valid                     bool // config.SelectiveMatchers accepts the pattern
		routed                    bool // an alert carrying value reaches the route
	}{
		{"alternation", "namespace", "prod|staging", "staging", true, true},
		{"already anchored", "namespace", "^kube-.*$", "kube-system", true, true},
		{"prefix regex", "namespace", "prod-.*", "prod-api", true, true},
		{"prefix regex is anchored", "namespace", "prod-.*", "dev-prod-tools", true, false},
		{"escaped trailing dollar", "reason", `Cost\$`, "Cost$", true, true},
		{"unclosed group", "namespace", "prod-(a|b", "prod-a", false, false},
		{"unclosed class", "reason", "OOM[", "OOMKilled", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			match := map[string]string{tc.key: tc.pattern}
			if err := config.SelectiveMatchers("routing[0].match", match); (err == nil) != tc.valid {
				t.Fatalf("SelectiveMatchers(%v) = %v, want valid=%v", match, err, tc.valid)
			}
			r := New([]config.Route{{Match: match, Sinks: []string{"pagerduty"}}}, nil, nil, []string{"stdout"})
			a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
			if tc.key == "namespace" {
				a.Namespace = tc.value
			} else {
				a.Reason = tc.value
			}
			got := r.Route(a)
			if routed := len(got) == 1 && got[0] == "pagerduty"; routed != tc.routed {
				t.Fatalf("Route(%s=%q) with match %v = %v, want routed=%v", tc.key, tc.value, match, got, tc.routed)
			}
		})
	}
}

func TestRouteDefault(t *testing.T) {
	r := New(nil, nil, nil, []string{"stdout"})
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)
	got := r.Route(a)
	if len(got) != 1 || got[0] != "stdout" {
		t.Fatalf("expected default sinks, got %v", got)
	}
}

func TestRouteSilencedAnnotation(t *testing.T) {
	r := New(nil, nil, nil, []string{"stdout"})
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)
	a.Annotations["alert-silence-until"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	if r.Route(a) != nil {
		t.Fatalf("annotation silence should drop alert")
	}
}

func TestRouteAnnotationSilenceDisabled(t *testing.T) {
	r := New(nil, nil, nil, []string{"stdout"})
	r.SetDisableAnnotationSilences(true)
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)
	a.Annotations["alert-silence-until"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	if r.Route(a) == nil {
		t.Fatalf("annotation silence must be ignored when disabled")
	}
}

func TestRuntimeSilenceSuppresses(t *testing.T) {
	r := New(nil, nil, nil, []string{"stdout"})
	st := silence.NewStore()
	r.SetRuntimeSilences(st)

	a := alert.New(alert.KindPod, "prod", "p", "X", alert.SeverityCritical)

	// No silence yet -> routes.
	if r.Route(a) == nil {
		t.Fatal("alert should route before any runtime silence")
	}
	// Add a matching, live silence -> dropped.
	st.Add(silence.Silence{Matchers: map[string]string{"namespace": "prod"}, Until: time.Now().Add(time.Hour)})
	if r.Route(a) != nil {
		t.Fatal("matching runtime silence should drop the alert")
	}
}

func TestRuntimeSilenceExpired(t *testing.T) {
	r := New(nil, nil, nil, []string{"stdout"})
	st := silence.NewStore()
	r.SetRuntimeSilences(st)
	st.Add(silence.Silence{Matchers: map[string]string{"namespace": "prod"}, Until: time.Now().Add(-time.Minute)})

	a := alert.New(alert.KindPod, "prod", "p", "X", alert.SeverityCritical)
	if r.Route(a) == nil {
		t.Fatal("expired runtime silence must not suppress")
	}
}

func TestRuntimeSilenceNonMatch(t *testing.T) {
	r := New(nil, nil, nil, []string{"stdout"})
	st := silence.NewStore()
	r.SetRuntimeSilences(st)
	st.Add(silence.Silence{Matchers: map[string]string{"namespace": "staging"}, Until: time.Now().Add(time.Hour)})

	a := alert.New(alert.KindPod, "prod", "p", "X", alert.SeverityCritical)
	if r.Route(a) == nil {
		t.Fatal("non-matching runtime silence must not suppress")
	}
}

func TestRuntimeSilenceSkippedOnResolve(t *testing.T) {
	r := New(nil, nil, nil, []string{"stdout"})
	st := silence.NewStore()
	r.SetRuntimeSilences(st)
	st.Add(silence.Silence{Matchers: map[string]string{"namespace": "prod"}, Until: time.Now().Add(time.Hour)})

	a := alert.New(alert.KindPod, "prod", "p", "X", alert.SeverityCritical)
	a.Resolved = true
	// Resolves must reach sinks even under a silence (close incidents).
	if r.Route(a) == nil {
		t.Fatal("resolve must bypass runtime silences")
	}
}

func TestInhibition(t *testing.T) {
	r := New(
		nil,
		[]config.Inhibition{{
			Source:   map[string]string{"kind": "Node", "reason": "NodeNotReady"},
			Target:   map[string]string{"kind": "Pod"},
			Equal:    []string{"node"},
			Duration: "1h",
		}},
		nil,
		[]string{"slack"},
	)

	src := alert.New(alert.KindNode, "", "node-1", "NodeNotReady", alert.SeverityCritical)
	src.NodeName = "node-1"
	if r.Route(src) == nil {
		t.Fatalf("source alert should route, not be inhibited")
	}

	pod := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	pod.NodeName = "node-1"
	if r.Route(pod) != nil {
		t.Fatalf("pod on the same node must be inhibited")
	}

	other := alert.New(alert.KindPod, "ns", "p2", "CrashLoopBackOff", alert.SeverityCritical)
	other.NodeName = "node-2"
	if r.Route(other) == nil {
		t.Fatalf("pod on a different node must not be inhibited")
	}
}

func TestArmInhibitionsRefreshesExpiry(t *testing.T) {
	r := New(
		nil,
		[]config.Inhibition{{
			Source:   map[string]string{"kind": "Node", "reason": "NodeNotReady"},
			Target:   map[string]string{"kind": "Pod"},
			Equal:    []string{"node"},
			Duration: "40ms",
		}},
		nil,
		[]string{"slack"},
	)
	src := alert.New(alert.KindNode, "", "node-1", "NodeNotReady", alert.SeverityCritical)
	src.NodeName = "node-1"
	r.Route(src)

	// Simulate muted re-fires keeping the inhibition alive past its
	// original expiry.
	for range 3 {
		time.Sleep(25 * time.Millisecond)
		r.ArmInhibitions(src)
	}

	pod := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	pod.NodeName = "node-1"
	if r.Route(pod) != nil {
		t.Fatalf("re-armed inhibition must still suppress the pod alert")
	}

	// Resolved source alerts must not arm anything.
	resolved := alert.New(alert.KindNode, "", "node-2", "NodeNotReady", alert.SeverityCritical)
	resolved.NodeName = "node-2"
	resolved.Resolved = true
	r.ArmInhibitions(resolved)
	other := alert.New(alert.KindPod, "ns", "p2", "CrashLoopBackOff", alert.SeverityCritical)
	other.NodeName = "node-2"
	if r.Route(other) == nil {
		t.Fatalf("resolved source must not arm an inhibition")
	}
}

func TestInhibitionExpires(t *testing.T) {
	r := New(
		nil,
		[]config.Inhibition{{
			Source:   map[string]string{"kind": "Node", "reason": "NodeNotReady"},
			Target:   map[string]string{"kind": "Pod"},
			Equal:    []string{"node"},
			Duration: "10ms",
		}},
		nil,
		[]string{"slack"},
	)
	src := alert.New(alert.KindNode, "", "node-1", "NodeNotReady", alert.SeverityCritical)
	src.NodeName = "node-1"
	r.Route(src)

	time.Sleep(30 * time.Millisecond)

	pod := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	pod.NodeName = "node-1"
	if r.Route(pod) == nil {
		t.Fatalf("inhibition should have expired")
	}
	// Prune should have evicted the expired key.
	r.mu.Lock()
	n := len(r.activeInhibits)
	r.mu.Unlock()
	if n != 0 {
		t.Fatalf("expired inhibitions should be pruned, got %d", n)
	}
}

func TestRouteMaintenanceWindowSuppresses(t *testing.T) {
	r := New(
		[]config.Route{{Match: map[string]string{}, Sinks: []string{"slack"}}},
		nil, nil, []string{"slack"},
	)
	// A window spanning now-1h..now+1h UTC matching prod must drop prod alerts.
	// A fixed 00:00-23:59 window is end-exclusive and so goes inactive for the
	// 23:59 UTC minute; deriving the bounds from now keeps it active whenever
	// the test runs (wrap-around past midnight is supported).
	now := time.Now().UTC()
	r.SetMaintenance([]config.MaintenanceWindow{{
		Matchers: map[string]string{"namespace": "prod"},
		Start:    now.Add(-time.Hour).Format("15:04"),
		End:      now.Add(time.Hour).Format("15:04"),
	}})

	prod := alert.New(alert.KindPod, "prod", "p", "X", alert.SeverityCritical)
	if got := r.Route(prod); got != nil {
		t.Fatalf("prod alert should be suppressed by the maintenance window, got %v", got)
	}
	// A non-matching namespace still routes.
	dev := alert.New(alert.KindPod, "dev", "p", "X", alert.SeverityCritical)
	if got := r.Route(dev); got == nil {
		t.Fatal("dev alert must not be suppressed by a prod-only window")
	}
	// Resolves bypass maintenance (must always reach sinks).
	res := alert.New(alert.KindPod, "prod", "p", "X", alert.SeverityCritical)
	res.Resolved = true
	if got := r.Route(res); got == nil {
		t.Fatal("resolve must bypass the maintenance window")
	}
}

func TestRouteMaintenanceWindowInactiveAllowsRouting(t *testing.T) {
	r := New(
		[]config.Route{{Match: map[string]string{}, Sinks: []string{"slack"}}},
		nil, nil, []string{"slack"},
	)
	// An empty window (start==end) is never active, so alerts route normally.
	r.SetMaintenance([]config.MaintenanceWindow{
		{Matchers: map[string]string{"namespace": "prod"}, Start: "03:00", End: "03:00"},
	})
	prod := alert.New(alert.KindPod, "prod", "p", "X", alert.SeverityCritical)
	if got := r.Route(prod); got == nil {
		t.Fatal("an inactive maintenance window must not suppress alerts")
	}
}

func TestRouteCRDSilenceSuppresses(t *testing.T) {
	r := New(
		[]config.Route{{Match: map[string]string{}, Sinks: []string{"slack"}}},
		nil, nil, []string{"slack"},
	)
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	r.SetCRDSilences(func() []config.Silence {
		return []config.Silence{{Matchers: map[string]string{"namespace": "prod"}, Until: future}}
	})

	prod := alert.New(alert.KindPod, "prod", "p", "X", alert.SeverityCritical)
	if got := r.Route(prod); got != nil {
		t.Fatalf("prod alert should be suppressed by the Silence CR, got %v", got)
	}
	dev := alert.New(alert.KindPod, "dev", "p", "X", alert.SeverityCritical)
	if got := r.Route(dev); got == nil {
		t.Fatal("dev alert must not be suppressed by a prod-only Silence CR")
	}
}

func TestRouteCRDSilenceExpired(t *testing.T) {
	r := New(
		[]config.Route{{Match: map[string]string{}, Sinks: []string{"slack"}}},
		nil, nil, []string{"slack"},
	)
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	r.SetCRDSilences(func() []config.Silence {
		return []config.Silence{{Matchers: map[string]string{"namespace": "prod"}, Until: past}}
	})
	prod := alert.New(alert.KindPod, "prod", "p", "X", alert.SeverityCritical)
	if got := r.Route(prod); got == nil {
		t.Fatal("an expired Silence CR must not suppress alerts")
	}
}
