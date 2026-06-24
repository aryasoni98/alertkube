package sinks

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"golang.org/x/time/rate"

	"alertkube/internal/alert"
)

// stubSink is a controllable Sink implementation for Dispatch tests.
type stubSink struct {
	name     string
	err      error
	supports bool
	panics   bool
	sends    atomic.Int32
}

func (s *stubSink) Name() string { return s.name }

func (s *stubSink) Send(_ context.Context, _ *alert.Alert) error {
	s.sends.Add(1)
	if s.panics {
		panic("boom")
	}
	return s.err
}

func (s *stubSink) Supports(_ alert.Severity) bool { return s.supports }

// fastRegistry builds a Registry with generous rate limits so tests never
// wait on the default 1/s limiter.
func fastRegistry(sinks ...*stubSink) *Registry {
	r := NewRegistry()
	for _, s := range sinks {
		r.Add(s)
		r.SetRate(s.name, rate.Limit(1000), 1000)
	}
	return r
}

func testAlert() *alert.Alert {
	return alert.New(alert.KindPod, "default", "app-1", "CrashLoopBackOff", alert.SeverityCritical)
}

func TestDispatchAllSucceed(t *testing.T) {
	s1 := &stubSink{name: "a", supports: true}
	s2 := &stubSink{name: "b", supports: true}
	r := fastRegistry(s1, s2)

	if !r.Dispatch(context.Background(), testAlert(), []string{"a", "b"}) {
		t.Fatalf("Dispatch should return true when all sinks succeed")
	}
	if s1.sends.Load() != 1 || s2.sends.Load() != 1 {
		t.Errorf("each sink should be sent once, got a=%d b=%d", s1.sends.Load(), s2.sends.Load())
	}
}

func TestDispatchAllFail(t *testing.T) {
	s1 := &stubSink{name: "a", supports: true, err: errors.New("down")}
	s2 := &stubSink{name: "b", supports: true, err: errors.New("down")}
	r := fastRegistry(s1, s2)

	if r.Dispatch(context.Background(), testAlert(), []string{"a", "b"}) {
		t.Fatalf("Dispatch should return false when every attempted sink fails")
	}
}

func TestDispatchPartialFailure(t *testing.T) {
	s1 := &stubSink{name: "a", supports: true, err: errors.New("down")}
	s2 := &stubSink{name: "b", supports: true}
	r := fastRegistry(s1, s2)

	if !r.Dispatch(context.Background(), testAlert(), []string{"a", "b"}) {
		t.Fatalf("Dispatch should return true when at least one sink succeeds")
	}
}

func TestDispatchNoMatchingSinks(t *testing.T) {
	s1 := &stubSink{name: "a", supports: true}
	r := fastRegistry(s1)

	if !r.Dispatch(context.Background(), testAlert(), []string{"nonexistent"}) {
		t.Fatalf("Dispatch should return true when no sinks were attempted")
	}
	if s1.sends.Load() != 0 {
		t.Errorf("unmatched sink must not be sent, got %d sends", s1.sends.Load())
	}
}

func TestDispatchSupportsGateSkips(t *testing.T) {
	s1 := &stubSink{name: "a", supports: false}
	r := fastRegistry(s1)

	if !r.Dispatch(context.Background(), testAlert(), []string{"a"}) {
		t.Fatalf("Dispatch should return true when the only sink was skipped (attempted == 0)")
	}
	if s1.sends.Load() != 0 {
		t.Errorf("sink not supporting the severity must be skipped, got %d sends", s1.sends.Load())
	}
}

func TestDispatchResolvedBypassesSupportsGate(t *testing.T) {
	// Regression test: a resolve must always follow its trigger even when
	// the sink's Supports gate would reject the severity.
	s1 := &stubSink{name: "a", supports: false}
	r := fastRegistry(s1)

	a := testAlert()
	a.Resolved = true

	if !r.Dispatch(context.Background(), a, []string{"a"}) {
		t.Fatalf("Dispatch of resolved alert should succeed")
	}
	if s1.sends.Load() != 1 {
		t.Errorf("resolved alert must bypass Supports gate, got %d sends", s1.sends.Load())
	}
}

func TestDispatchPanicCountsAsFailure(t *testing.T) {
	s1 := &stubSink{name: "a", supports: true, panics: true}
	r := fastRegistry(s1)

	if r.Dispatch(context.Background(), testAlert(), []string{"a"}) {
		t.Fatalf("Dispatch should return false when the only attempted sink panicked")
	}
	if s1.sends.Load() != 1 {
		t.Errorf("panicking sink should still have been attempted once, got %d", s1.sends.Load())
	}
}

func TestDispatchPanicDoesNotSinkOthers(t *testing.T) {
	s1 := &stubSink{name: "a", supports: true, panics: true}
	s2 := &stubSink{name: "b", supports: true}
	r := fastRegistry(s1, s2)

	if !r.Dispatch(context.Background(), testAlert(), []string{"a", "b"}) {
		t.Fatalf("Dispatch should return true: one sink panicked but the other succeeded")
	}
	if s2.sends.Load() != 1 {
		t.Errorf("healthy sink should still be sent, got %d", s2.sends.Load())
	}
}

func TestNamesSorted(t *testing.T) {
	r := fastRegistry(
		&stubSink{name: "slack", supports: true},
		&stubSink{name: "discord", supports: true},
		&stubSink{name: "pagerduty", supports: true},
	)
	got := r.Names()
	want := []string{"discord", "pagerduty", "slack"}
	if len(got) != len(want) {
		t.Fatalf("Names len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names[%d] = %q, want %q (must be sorted)", i, got[i], want[i])
		}
	}
}

func TestTestSendSuccess(t *testing.T) {
	s := &stubSink{name: "slack", supports: true}
	r := fastRegistry(s)
	if err := r.TestSend(context.Background(), "slack", testAlert()); err != nil {
		t.Fatalf("TestSend returned error: %v", err)
	}
	if s.sends.Load() != 1 {
		t.Fatalf("sink got %d sends, want 1", s.sends.Load())
	}
}

func TestTestSendReturnsSinkError(t *testing.T) {
	r := fastRegistry(&stubSink{name: "slack", supports: true, err: errors.New("401 invalid_auth")})
	err := r.TestSend(context.Background(), "slack", testAlert())
	if err == nil || err.Error() != "401 invalid_auth" {
		t.Fatalf("TestSend err = %v, want the sink's error surfaced", err)
	}
}

func TestTestSendUnknownSink(t *testing.T) {
	r := fastRegistry(&stubSink{name: "slack", supports: true})
	if err := r.TestSend(context.Background(), "nope", testAlert()); err == nil {
		t.Fatal("TestSend to unknown sink must return an error")
	}
}

func TestTestSendIgnoresSupportsGate(t *testing.T) {
	// A test-fire must send even if the sink would normally skip this severity.
	s := &stubSink{name: "slack", supports: false}
	r := fastRegistry(s)
	if err := r.TestSend(context.Background(), "slack", testAlert()); err != nil {
		t.Fatalf("TestSend error: %v", err)
	}
	if s.sends.Load() != 1 {
		t.Fatalf("TestSend should bypass Supports gate; sends = %d, want 1", s.sends.Load())
	}
}

func TestTestSendRecoversPanic(t *testing.T) {
	r := fastRegistry(&stubSink{name: "slack", supports: true, panics: true})
	if err := r.TestSend(context.Background(), "slack", testAlert()); err == nil {
		t.Fatal("TestSend must convert a sink panic into an error, not crash")
	}
}
