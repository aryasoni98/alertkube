package sinks

import (
	"testing"
	"time"
)

// The blind spot this closes: a sink that answers 200 every time but takes
// 20s never increments the failure counter, so a failure-only breaker leaves it
// permanently occupying a dispatch worker.
func TestBreakerTripsOnSustainedSlowSuccesses(t *testing.T) {
	now := time.Unix(0, 0)
	b := &breaker{now: func() time.Time { return now }}

	for i := 0; i < breakerSlowRun; i++ {
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
	for i := 0; i < breakerSlowRun-1; i++ {
		b.RecordLatency(breakerSlowThreshold + time.Second)
	}
	b.RecordLatency(time.Millisecond) // recovered
	for i := 0; i < breakerSlowRun-1; i++ {
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
	for i := 0; i < breakerThreshold-1; i++ {
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
