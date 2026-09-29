package app

import (
	"context"
	"errors"
	"slices"
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
	s := &testSink{name: "a"}
	d := dispatcherWith(t, s, 4, 64)
	defer d.Shutdown(context.Background())

	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	d.enqueue(a, []string{"a"}, nil)
	waitFor(t, 1, s.sends.Load)
}

func TestDispatcherOnFailRunsWhenDeliveryFails(t *testing.T) {
	s := &testSink{name: "a", err: errors.New("down")}
	d := dispatcherWith(t, s, 2, 64)
	defer d.Shutdown(context.Background())

	var failed atomic.Int32
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	d.enqueue(a, []string{"a"}, func() { failed.Add(1) })
	waitFor(t, 1, failed.Load)
}

func TestDispatcherEmptyRouteIsNoop(t *testing.T) {
	s := &testSink{name: "a"}
	d := dispatcherWith(t, s, 2, 8)
	defer d.Shutdown(context.Background())

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
	d := dispatcherWith(t, &testSink{name: "a", fn: func(*alert.Alert) error {
		time.Sleep(2 * time.Millisecond)
		mu.Lock()
		delivered++
		mu.Unlock()
		return nil
	}}, 2, 256)

	const n = 50
	for range n {
		d.enqueue(alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical), []string{"a"}, nil)
	}
	d.Shutdown(context.Background()) // must block until the backlog is delivered

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
	withRetryDelay(t, 5*time.Millisecond)

	var attempts atomic.Int32
	d := dispatcherWith(t, &testSink{name: "a", fn: func(*alert.Alert) error {
		if attempts.Add(1) == 1 {
			return errors.New("transient")
		}
		return nil
	}}, 2, 64)
	defer d.Shutdown(context.Background())

	resolved := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
	resolved.Resolved = true
	d.enqueue(resolved, []string{"a"}, nil) // onFail nil: resolve path

	// First attempt fails, retry succeeds -> 2 attempts total.
	waitFor(t, 2, attempts.Load)
}

func TestDispatcherDeadLettersExhaustedResolve(t *testing.T) {
	// A resolve that keeps failing must, after its bounded retries, be
	// dead-lettered (not silently dropped) so a dangling incident is visible.
	withRetryDelay(t, time.Millisecond)

	s := &testSink{name: "a", err: errors.New("down")}
	d := dispatcherWith(t, s, 2, 64)
	defer d.Shutdown(context.Background())

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
	s := &testSink{name: "a", err: errors.New("down")}
	d := dispatcherWith(t, s, 2, 64)
	defer d.Shutdown(context.Background())

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
	d := dispatcherWith(t, &testSink{name: "a", fn: func(*alert.Alert) error { <-release; return nil }}, 1, 8)

	d.enqueue(alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical), []string{"a"}, nil)
	waitForPending(t, d, 1) // worker is blocked in Send; record is pending
	close(release)          // let delivery complete
	waitForPending(t, d, 0) // acked
	d.Shutdown(context.Background())
}

func TestDispatcherReplayResumesDelivery(t *testing.T) {
	// Records restored from a snapshot must be re-delivered (at-least-once
	// across restart).
	s := &testSink{name: "a"}
	d := dispatcherWith(t, s, 2, 64)
	defer d.Shutdown(context.Background())

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
	s := &testSink{name: "a"}
	d := dispatcherWith(t, s, 2, 8)
	d.Shutdown(context.Background())

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
	d := dispatcherWith(t, &testSink{name: "a", fn: func(*alert.Alert) error {
		close(started)
		<-release
		return nil
	}}, 1, 8)
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
			d.Shutdown(context.Background())
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
	d.Shutdown(context.Background()) // Repeated shutdown must also be safe after the drain completed.
}

// Shutdown stops at the caller's deadline, not only at dispatchDrainTimeout:
// the controller passes one shutdown deadline that keeps time back for the
// final state save. The abandoned delivery stays in the outbox for that save.
func TestDispatcherShutdownStopsAtDeadline(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	d := dispatcherWith(t, &testSink{name: "a", fn: func(*alert.Alert) error {
		close(started)
		<-release
		return nil
	}}, 1, 8)
	d.enqueue(alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical), []string{"a"}, nil)
	select {
	case <-started:
	case <-timeoutAfter():
		t.Fatal("delivery did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	returned := make(chan struct{})
	go func() {
		d.Shutdown(ctx)
		close(returned)
	}()
	select {
	case <-returned:
	case <-timeoutAfter():
		t.Fatal("Shutdown ignored the caller's deadline and kept waiting on a stuck delivery")
	}
	if got := len(d.PendingSnapshot()); got != 1 {
		t.Fatalf("the abandoned delivery must stay in the outbox, got %d records", got)
	}
}

// recordSink blocks its first send until release is closed and records the
// name and resolve state of every alert it delivers.
type recordSink struct {
	started, release chan struct{}
	once             sync.Once
	mu               sync.Mutex
	sent             []string
}

func (s *recordSink) Name() string                   { return "a" }
func (s *recordSink) Supports(_ alert.Severity) bool { return true }
func (s *recordSink) Send(_ context.Context, a *alert.Alert) error {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, sentKey(a))
	return nil
}

func (s *recordSink) delivered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

func sentKey(a *alert.Alert) string {
	if a.Resolved {
		return "RESOLVE " + a.Name
	}
	return "FIRE " + a.Name
}

// Once unblockProducers has made submit drop a job from a full queue, a later
// job on that queue must not be delivered ahead of it. Otherwise a FIRE for x
// is dropped into the outbox, the RESOLVE for x takes the slot a worker frees
// and closes the incident, and the next boot replays the FIRE and opens an
// incident nothing resolves.
func TestDispatcherKeepsOutboxOrderAfterStopDrop(t *testing.T) {
	s := &recordSink{started: make(chan struct{}), release: make(chan struct{})}
	releaseOnce := sync.OnceFunc(func() { close(s.release) })
	t.Cleanup(releaseOnce)
	reg := sinks.NewRegistry()
	reg.Add(s)
	reg.SetRate("a", rate.Limit(100000), 100000)
	d := newDispatcher(reg, 1, 1) // one worker, one queue slot
	d.Start()

	route := []string{"a"}
	d.enqueue(alert.New(alert.KindPod, "ns", "in-flight", "X", alert.SeverityCritical), route, nil)
	select {
	case <-s.started:
	case <-timeoutAfter():
		t.Fatal("delivery did not start")
	}
	d.enqueue(alert.New(alert.KindPod, "ns", "queued", "X", alert.SeverityCritical), route, nil)
	fire := alert.New(alert.KindPod, "ns", "x", "X", alert.SeverityCritical)
	parked := make(chan struct{})
	go func() {
		defer close(parked)
		d.enqueue(fire, route, nil)
	}()
	waitForPending(t, d, 3)

	d.unblockProducers()
	select {
	case <-parked:
	case <-timeoutAfter():
		t.Fatal("unblockProducers did not release the parked producer")
	}
	// Let the worker drain the queue so the next submit finds a free slot.
	releaseOnce()
	waitForPending(t, d, 1)

	resolve := fire.Clone()
	resolve.Resolved = true
	d.enqueue(resolve, route, nil)
	d.Shutdown(context.Background())

	for _, got := range s.delivered() {
		if got == "RESOLVE x" {
			t.Fatalf("RESOLVE x was delivered while FIRE x stayed in the outbox; delivered %v", s.delivered())
		}
	}
	var pending []string
	for _, rec := range d.PendingSnapshot() {
		pending = append(pending, sentKey(rec.Alert))
	}
	if want := []string{"FIRE x", "RESOLVE x"}; !slices.Equal(pending, want) {
		t.Fatalf("outbox = %v, want %v in ID order", pending, want)
	}
}
