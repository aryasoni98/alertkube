package sinks

import (
	"testing"
	"time"

	"github.com/aryasoni98/alertkube/internal/httpx"
)

// The slow check has to sit inside the delivery budget described at
// dispatchTimeout (internal/app/pipeline.go). One HTTP attempt that reaches
// httpx.DefaultTimeout is a failure, so a threshold at or above it would count
// only retried successes as slow. internal/app pins the outer
// perSinkTimeout < dispatchTimeout link.
func TestBreakerSlowThresholdNestsInDeliveryBudget(t *testing.T) {
	tests := []struct {
		name         string
		inner, outer time.Duration
	}{
		{"slow threshold < one HTTP attempt", breakerSlowThreshold, httpx.DefaultTimeout},
		{"one HTTP attempt < one sink send", httpx.DefaultTimeout, perSinkTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.inner >= tt.outer {
				t.Fatalf("%s >= %s", tt.inner, tt.outer)
			}
		})
	}
}

// The blind spot this closes: a sink that answers 200 every time but takes
// seconds to do it never increments the failure counter, so a failure-only
// breaker leaves it permanently occupying a dispatch worker.
func TestBreakerTripsOnSustainedSlowSuccesses(t *testing.T) {
	now := time.Unix(0, 0)
	b := &breaker{now: func() time.Time { return now }}

	for range breakerSlowRun {
		b.Record(true) // every send succeeds
		b.RecordLatency(breakerSlowThreshold + time.Second)
	}
	if !b.Open() {
		t.Fatal("breaker stayed closed after a run of slow-but-successful sends; a slow sink would keep tying up dispatch workers")
	}
}

// One slow send is a cold connection or a GC pause at the far end, not an
// outage. It must not short-circuit a healthy sink.
func TestBreakerToleratesIsolatedSlowSend(t *testing.T) {
	now := time.Unix(0, 0)
	b := &breaker{now: func() time.Time { return now }}
	b.RecordLatency(breakerSlowThreshold + time.Second)
	if b.Open() {
		t.Fatal("a single slow send must not open the breaker")
	}
}

// A fast send is evidence the sink recovered, so it must clear the slow run the
// same way a success clears the failure run.
func TestBreakerFastSendClearsSlowRun(t *testing.T) {
	now := time.Unix(0, 0)
	b := &breaker{now: func() time.Time { return now }}
	for range breakerSlowRun - 1 {
		b.RecordLatency(breakerSlowThreshold + time.Second)
	}
	b.RecordLatency(time.Millisecond) // recovered
	for range breakerSlowRun - 1 {
		b.RecordLatency(breakerSlowThreshold + time.Second)
	}
	if b.Open() {
		t.Fatal("a fast send must reset the slow run; the breaker opened on two partial runs")
	}
}

// Slow detection must not disturb the failure path, which drives resolve
// retries and the open gauge.
func TestBreakerLatencyDoesNotDisturbFailureCounting(t *testing.T) {
	now := time.Unix(0, 0)
	b := &breaker{now: func() time.Time { return now }}
	for range breakerThreshold - 1 {
		b.Record(false)
		b.RecordLatency(time.Millisecond) // fast failures
	}
	if b.Open() {
		t.Fatal("breaker opened before the failure threshold")
	}
	b.Record(false)
	if !b.Open() {
		t.Fatal("breaker did not open at the failure threshold; the failure path regressed")
	}
}
