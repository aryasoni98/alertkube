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

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/sinks"
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
	for range n {
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
	n := d.ReplayPending([]alert.PendingDelivery{{ID: 7, Alert: a, Route: []string{"a"}}}, nil, nil)
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
	for range deadLetterCap + 50 {
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

func TestDispatcherOwnsEnqueuedDelivery(t *testing.T) {
	d := newDispatcher(sinks.NewRegistry(), 1, 8)
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	a.Labels["team"], a.Details["logs"] = "platform", "original"
	route := []string{"a"}
	d.enqueue(a, route, nil)
	a.Name, a.Labels["team"], a.Details["logs"], route[0] = "changed", "changed", "changed", "changed"
	job := <-d.queues[0]
	if job.a.Name != "p" || job.a.Labels["team"] != "platform" || job.a.Details["logs"] != "original" || job.route[0] != "a" {
		t.Fatalf("queued delivery shares caller-owned state: %+v %v", job.a, job.route)
	}
}

func TestPendingSnapshotOwnsAndOrdersRecords(t *testing.T) {
	d := newDispatcher(sinks.NewRegistry(), 1, 8)
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	a.Labels["team"] = "platform"
	for _, id := range []uint64{3, 1, 2} {
		d.pendingAdd(id, a, []string{"a"})
	}
	snapshot := d.PendingSnapshot()
	for i, rec := range snapshot {
		if rec.ID != uint64(i+1) {
			t.Errorf("snapshot record %d has ID %d", i, rec.ID)
		}
		rec.Alert.Labels["team"] = "changed"
		rec.Route[0] = "changed"
	}
	for _, rec := range d.PendingSnapshot() {
		if rec.Alert.Labels["team"] != "platform" || rec.Route[0] != "a" {
			t.Fatal("persisted snapshot aliases the live outbox")
		}
	}
}

func TestReplayRestoresDeliveryOrder(t *testing.T) {
	d := newDispatcher(sinks.NewRegistry(), 1, 8)
	first := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	first.Summary = "fire"
	resolved := first.Clone()
	resolved.Summary, resolved.Resolved = "resolve", true
	refire := first.Clone()
	refire.Summary = "refire"
	// Older snapshots serialized a map, so their records can be in any order.
	records := []alert.PendingDelivery{
		{ID: 20, Alert: resolved, Route: []string{"a"}},
		{ID: 30, Alert: refire, Route: []string{"a"}},
		{ID: 10, Alert: first, Route: []string{"a"}},
	}
	if n := d.ReplayPending(records, nil, nil); n != 3 {
		t.Fatalf("replayed %d, want 3", n)
	}
	for _, want := range []string{"fire", "resolve", "refire"} {
		if job := <-d.queues[0]; job.a.Summary != want {
			t.Errorf("replayed %q, want %q", job.a.Summary, want)
		}
	}
	if records[0].ID != 20 {
		t.Fatal("replay reordered the caller's snapshot")
	}
}

func TestConcurrentShutdownWaitsForDrain(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	reg := sinks.NewRegistry()
	reg.Add(&funcSink{name: "a", fn: func() error {
		close(started)
		<-release
		return nil
	}})
	d := newDispatcher(reg, 1, 8)
	d.Start()
	d.enqueue(alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical), []string{"a"}, nil)
	select {
	case <-started:
	case <-timeoutAfter():
		close(release)
		t.Fatal("delivery did not start")
	}
	returned := make(chan struct{}, 4)
	for range cap(returned) {
		go func() {
			d.Shutdown()
			returned <- struct{}{}
		}()
	}
	select {
	case <-returned:
		close(release)
		t.Fatal("Shutdown returned before the in-flight delivery finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for range cap(returned) {
		select {
		case <-returned:
		case <-timeoutAfter():
			t.Fatal("Shutdown did not return after delivery finished")
		}
	}
	d.Shutdown() // Repeated shutdown must also be safe after the drain completed.
}

// funcSink runs an arbitrary function per send.
type funcSink struct {
	name string
	fn   func() error
}

func (f *funcSink) Name() string                                 { return f.name }
func (f *funcSink) Supports(_ alert.Severity) bool               { return true }
func (f *funcSink) Send(_ context.Context, _ *alert.Alert) error { return f.fn() }
