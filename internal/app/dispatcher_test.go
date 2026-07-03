package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/time/rate"

	"alertkube/internal/alert"
	"alertkube/internal/metrics"
	"alertkube/internal/sinks"
)

// dispatchStub is a controllable sink for dispatcher tests.
type dispatchStub struct {
	name  string
	err   error
	sends atomic.Int32
}

func (s *dispatchStub) Name() string                   { return s.name }
func (s *dispatchStub) Supports(_ alert.Severity) bool { return true }
func (s *dispatchStub) Send(_ context.Context, _ *alert.Alert) error {
	s.sends.Add(1)
	return s.err
}

func dispatcherWith(t *testing.T, s *dispatchStub, workers, queue int) (*dispatcher, *sinks.Registry) {
	t.Helper()
	reg := sinks.NewRegistry()
	reg.Add(s)
	reg.SetRate(s.name, rate.Limit(1000), 1000) // never wait on the limiter
	d := newDispatcher(reg, workers, queue)
	d.Start()
	return d, reg
}

func waitFor(t *testing.T, want int32, get func() int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if get() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for count %d, got %d", want, get())
}

func TestDispatcherDeliversAsync(t *testing.T) {
	s := &dispatchStub{name: "a"}
	d, _ := dispatcherWith(t, s, 4, 64)
	defer d.Shutdown()

	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	d.enqueue(a, []string{"a"}, nil)
	waitFor(t, 1, s.sends.Load)
}

func TestDispatcherOnFailRunsWhenDeliveryFails(t *testing.T) {
	s := &dispatchStub{name: "a", err: errors.New("down")}
	d, _ := dispatcherWith(t, s, 2, 64)
	defer d.Shutdown()

	var failed atomic.Int32
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	d.enqueue(a, []string{"a"}, func() { failed.Add(1) })
	waitFor(t, 1, failed.Load)
}

func TestDispatcherEmptyRouteIsNoop(t *testing.T) {
	s := &dispatchStub{name: "a"}
	d, _ := dispatcherWith(t, s, 2, 8)
	defer d.Shutdown()

	var failed atomic.Int32
	d.enqueue(alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo), nil, func() { failed.Add(1) })
	// An empty route is dropped before enqueue; onFail must not run.
	time.Sleep(20 * time.Millisecond)
	if failed.Load() != 0 {
		t.Fatalf("empty route must be a no-op, onFail ran %d times", failed.Load())
	}
}

func TestDispatcherShutdownDrainsQueue(t *testing.T) {
	// One slow-ish worker with a backlog: Shutdown must deliver all queued
	// alerts before returning.
	var mu sync.Mutex
	delivered := 0
	reg := sinks.NewRegistry()
	slow := &funcSink{name: "a", fn: func() error {
		time.Sleep(2 * time.Millisecond)
		mu.Lock()
		delivered++
		mu.Unlock()
		return nil
	}}
	reg.Add(slow)
	reg.SetRate("a", rate.Limit(100000), 100000)
	d := newDispatcher(reg, 2, 256)
	d.Start()

	const n = 50
	for i := 0; i < n; i++ {
		d.enqueue(alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical), []string{"a"}, nil)
	}
	d.Shutdown() // must block until the backlog is delivered

	mu.Lock()
	got := delivered
	mu.Unlock()
	if got != n {
		t.Fatalf("shutdown should drain the queue: delivered %d of %d", got, n)
	}
}

func TestDispatcherRetriesFailedResolve(t *testing.T) {
	// A resolve that fails the first attempt must be re-queued (bounded) and
	// delivered on a later attempt, so a stateful incident does not dangle.
	old := resolveRetryDelay
	resolveRetryDelay = 5 * time.Millisecond
	t.Cleanup(func() { resolveRetryDelay = old })

	var attempts atomic.Int32
	reg := sinks.NewRegistry()
	flaky := &funcSink{name: "a", fn: func() error {
		if attempts.Add(1) == 1 {
			return errors.New("transient")
		}
		return nil
	}}
	reg.Add(flaky)
	reg.SetRate("a", rate.Limit(100000), 100000)
	d := newDispatcher(reg, 2, 64)
	d.Start()
	defer d.Shutdown()

	resolved := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	resolved.Resolved = true
	d.enqueue(resolved, []string{"a"}, nil) // onFail nil: resolve path

	// First attempt fails, retry succeeds -> 2 attempts total.
	waitFor(t, 2, attempts.Load)
}

func TestDispatcherDeadLettersExhaustedResolve(t *testing.T) {
	// A resolve that keeps failing must, after its bounded retries, be
	// dead-lettered (not silently dropped) so a dangling incident is visible.
	old := resolveRetryDelay
	resolveRetryDelay = time.Millisecond
	t.Cleanup(func() { resolveRetryDelay = old })

	s := &dispatchStub{name: "a", err: errors.New("down")}
	d, _ := dispatcherWith(t, s, 2, 64)
	defer d.Shutdown()

	var dead atomic.Int32
	d.SetDeadLetter(func(*alert.Alert) { dead.Add(1) })

	resolved := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	resolved.Resolved = true
	d.enqueue(resolved, []string{"a"}, nil)

	waitFor(t, 1, dead.Load)
}

func TestDispatcherDeadLettersFailedFireOnce(t *testing.T) {
	// A fire-once alert (onFail nil, not a resolve - e.g. an ephemeral event or
	// group summary) that fails has no retry path, so it must be dead-lettered.
	s := &dispatchStub{name: "a", err: errors.New("down")}
	d, _ := dispatcherWith(t, s, 2, 64)
	defer d.Shutdown()

	var dead atomic.Int32
	d.SetDeadLetter(func(*alert.Alert) { dead.Add(1) })

	ev := alert.New(alert.KindCloudTrailEvent, "us-east-1", "e1", "X", alert.SeverityWarning)
	ev.Event = true
	d.enqueue(ev, []string{"a"}, nil) // onFail nil, not resolved -> no retry path
	waitFor(t, 1, dead.Load)
}

func waitForPending(t *testing.T, d *dispatcher, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(d.PendingSnapshot()) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for pending==%d, got %d", want, len(d.PendingSnapshot()))
}

func TestDispatcherPendingTrackedThenAcked(t *testing.T) {
	// A delivery is tracked in the outbox while in flight and acked once
	// delivered, so the persisted outbox reflects only undelivered work.
	release := make(chan struct{})
	reg := sinks.NewRegistry()
	reg.Add(&funcSink{name: "a", fn: func() error { <-release; return nil }})
	reg.SetRate("a", rate.Limit(100000), 100000)
	d := newDispatcher(reg, 1, 8)
	d.Start()

	d.enqueue(alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical), []string{"a"}, nil)
	waitForPending(t, d, 1) // worker is blocked in Send; record is pending
	close(release)          // let delivery complete
	waitForPending(t, d, 0) // acked
	d.Shutdown()
}

func TestDispatcherReplayResumesDelivery(t *testing.T) {
	// Records restored from a snapshot must be re-delivered (at-least-once
	// across restart).
	s := &dispatchStub{name: "a"}
	d, _ := dispatcherWith(t, s, 2, 64)
	defer d.Shutdown()

	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	n := d.ReplayPending([]alert.PendingDelivery{{ID: 7, Alert: a, Route: []string{"a"}}})
	if n != 1 {
		t.Fatalf("ReplayPending returned %d, want 1", n)
	}
	waitFor(t, 1, s.sends.Load)
	waitForPending(t, d, 0) // delivered -> acked
}

func TestDispatcherPendingGenerationMoves(t *testing.T) {
	d := newDispatcher(sinks.NewRegistry(), 1, 8)
	before := d.PendingGeneration()
	d.pendingAdd(1, alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo), []string{"a"})
	if d.PendingGeneration() == before {
		t.Fatal("pending generation must advance on add")
	}
	if got := testutil.ToFloat64(metrics.OutboxPending); got != 1 {
		t.Fatalf("OutboxPending gauge = %v, want 1 after add", got)
	}
	mid := d.PendingGeneration()
	d.pendingDone(1)
	if d.PendingGeneration() == mid {
		t.Fatal("pending generation must advance on ack")
	}
	if got := testutil.ToFloat64(metrics.OutboxPending); got != 0 {
		t.Fatalf("OutboxPending gauge = %v, want 0 after ack", got)
	}
}

func TestDeadLetterLogBounded(t *testing.T) {
	dl := newDeadLetterLog()
	for i := 0; i < deadLetterCap+50; i++ {
		dl.Record(alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo))
	}
	if got := len(dl.List()); got != deadLetterCap {
		t.Fatalf("dead-letter ring should cap at %d, got %d", deadLetterCap, got)
	}
}

func TestDispatcherEnqueueAfterShutdownDrops(t *testing.T) {
	s := &dispatchStub{name: "a"}
	d, _ := dispatcherWith(t, s, 2, 8)
	d.Shutdown()

	// Enqueue after shutdown must not panic and must not deliver.
	d.enqueue(alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical), []string{"a"}, nil)
	time.Sleep(20 * time.Millisecond)
	if s.sends.Load() != 0 {
		t.Fatalf("enqueue after shutdown must be dropped, got %d sends", s.sends.Load())
	}
}

// funcSink runs an arbitrary function per send.
type funcSink struct {
	name string
	fn   func() error
}

func (f *funcSink) Name() string                                 { return f.name }
func (f *funcSink) Supports(_ alert.Severity) bool               { return true }
func (f *funcSink) Send(_ context.Context, _ *alert.Alert) error { return f.fn() }
