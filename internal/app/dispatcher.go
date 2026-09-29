package app

import (
	"context"
	"hash/fnv"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/env"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/sinks"
	"github.com/aryasoni98/alertkube/internal/trace"
)

// Dispatch worker-pool defaults. Delivery (the blocking HTTP fan-out to sinks)
// runs on this pool, decoupled from the informer/receiver/source goroutines
// that produce alerts, so a slow sink can never stall Kubernetes event
// processing (the informer's pending-notification buffer would otherwise grow
// unbounded) - it only fills the bounded queue below. Both are overridable via
// ALERTKUBE_DISPATCH_WORKERS / ALERTKUBE_DISPATCH_QUEUE.
const (
	defaultDispatchWorkers   = 16
	defaultDispatchQueueSize = 2048
	// dispatchDrainTimeout bounds how long Shutdown waits for the workers to
	// finish the queued backlog. Each in-flight send is itself capped by the
	// per-sink timeout budget; this is the ceiling on the whole drain. The
	// controller passes an earlier shutdown deadline that keeps time back for
	// the final save (see the shutdown budget at finalSaveTimeout in
	// pipeline.go). Must stay >= dispatchTimeout so a send that has just
	// started is not cut off.
	dispatchDrainTimeout = 30 * time.Second
	// maxResolveRetries bounds how many times a failed resolve is re-queued.
	// A firing alert that fails delivery retries via the store (MarkFailed ->
	// re-emit on the next watch/poll), but a resolve has no such re-trigger:
	// if its delivery is lost, a stateful incident (PagerDuty/Opsgenie) dangles
	// forever. So resolves are retried a bounded number of times here.
	maxResolveRetries = 3
)

// resolveRetryDelay is the wait before a failed resolve is re-queued. A var so
// tests can shorten it.
var resolveRetryDelay = 2 * time.Second

// enqueueFunc submits an alert to a route for asynchronous delivery. onFail
// (may be nil) runs on the worker after the delivery attempt if every routed
// sink failed, so a firing alert's caller can roll back dedupe state and retry.
// The dispatcher's enqueue method has this signature; tests substitute a
// synchronous implementation so delivery outcomes are deterministic.
type enqueueFunc func(a *alert.Alert, route []string, onFail func())

// dispatchJob is one unit of delivery work handed to a worker.
type dispatchJob struct {
	id     uint64
	a      *alert.Alert
	route  []string
	onFail func()
	// traceCtx carries the producing span's linkage - and nothing else - so the
	// worker's delivery span attaches to the trace that created the alert. It is
	// deliberately NOT the producer's context: an informer handler's context is
	// cancelled the moment it returns, which is long before a worker performs
	// the HTTP send, so inheriting it would cancel every queued delivery.
	// trace.Detach strips cancellation and keeps the span context.
	traceCtx context.Context
	// retries counts how many times a failed resolve has already been
	// re-queued (see maxResolveRetries).
	retries int
}

// dispatcher decouples alert delivery from the goroutines that produce alerts
// (informer handlers, cloud-source polls, the receiver, the sweeper). Producers
// enqueue near-instantly; a fixed worker pool drains the bounded queue and
// performs the blocking sink fan-out. This bounds memory under a storm (excess
// alerts wait in the queue, and enqueue applies backpressure when it is full)
// and keeps a single slow sink from blocking event processing.
type dispatcher struct {
	reg *sinks.Registry
	// queues is one bounded queue per worker, not a single shared channel.
	// A shared channel gives FIFO at *dequeue* but says nothing about
	// completion order: with N workers, a FIRE picked up at t0 whose sink call
	// is slow can land after the RESOLVE picked up at t0+e by a free worker,
	// leaving a stateful sink (PagerDuty/Opsgenie keys on fingerprint) holding
	// an incident that never closes. Routing every delivery for one
	// fingerprint to one worker (see queueFor) makes that reordering
	// impossible. The cost is head-of-line blocking within a bucket - one slow
	// delivery stalls only the fingerprints that hash to its worker - which is
	// why the worker count should be sized up rather than down.
	queues []chan dispatchJob
	// queueMu[i] serializes producers on queues[i], and draining[i] (guarded
	// by it) records that submit has dropped a job from that queue after stop
	// closed. From then on every later job for the queue is dropped too, so it
	// stays in the outbox behind the dropped one instead of taking a slot a
	// worker frees and being delivered first: a RESOLVE delivered ahead of its
	// outboxed FIRE would close the incident, and the next boot would replay
	// the FIRE and open one that nothing resolves. The drop decision and the
	// flag must be atomic with respect to the next submit, which is why the
	// lock is held while a producer is parked on a full queue.
	queueMu  []sync.Mutex
	draining []bool
	stop     chan struct{}
	wg       sync.WaitGroup

	// mu guards closed and serializes enqueue against Shutdown so the jobs
	// channel is never sent-on after it is closed. Held as RLock by enqueue
	// (many concurrent producers) and Lock by Shutdown (once).
	mu     sync.RWMutex
	closed bool
	// Every caller of Shutdown waits for the same drain, including concurrent callers.
	shutdownOnce sync.Once
	// stopOnce closes stop once: controller shutdown closes it early through
	// unblockProducers, and Shutdown closes it for every other caller.
	stopOnce sync.Once

	// onDeadLetter, when set, records a delivery the dispatcher permanently
	// abandoned (no retry path). nil = no dead-letter capture (e.g. tests).
	onDeadLetter func(*alert.Alert)

	// Durable outbox (P3-full): every accepted delivery is tracked in pending
	// (keyed by a monotonic id) until it is delivered or dead-lettered. The set
	// is persisted (via PendingSnapshot) in the state ConfigMap and replayed on
	// startup, so an enqueued-but-undelivered alert survives a restart. nextID
	// mints ids; pendingGen bumps on every add/remove so the save loop is
	// generation-gated like the alert store.
	nextID     atomic.Uint64
	pendingMu  sync.Mutex
	pending    map[uint64]alert.PendingDelivery
	pendingGen uint64
}

// SetDeadLetter registers the callback invoked when a delivery is permanently
// abandoned. Call before Start.
func (d *dispatcher) SetDeadLetter(fn func(*alert.Alert)) { d.onDeadLetter = fn }

// newDispatcher builds a dispatcher over reg with the given worker count and
// queue capacity (non-positive values fall back to the defaults). Call Start
// to spawn the workers.
func newDispatcher(reg *sinks.Registry, workers, queueSize int) *dispatcher {
	if workers <= 0 {
		workers = defaultDispatchWorkers
	}
	if queueSize <= 0 {
		queueSize = defaultDispatchQueueSize
	}
	// queueSize stays the process-wide bound on buffered alerts; it is split
	// across the per-worker queues so raising the worker count does not
	// silently multiply the memory ceiling. Every queue holds at least one slot
	// so a pool with more workers than the configured capacity still works.
	perQueue := queueSize / workers
	if perQueue < 1 {
		perQueue = 1
	}
	queues := make([]chan dispatchJob, workers)
	for i := range queues {
		queues[i] = make(chan dispatchJob, perQueue)
	}
	return &dispatcher{
		reg:      reg,
		queues:   queues,
		queueMu:  make([]sync.Mutex, workers),
		draining: make([]bool, workers),
		stop:     make(chan struct{}),
		pending:  map[uint64]alert.PendingDelivery{},
	}
}

// queueFor picks the worker queue that owns an alert's deliveries. Affinity is
// by fingerprint, because that is what a stateful sink keys its incident on and
// therefore what must never be reordered: a FIRE and its RESOLVE share one
// fingerprint and so always land on the same worker, in enqueue order. Alerts
// enqueued before a fingerprint was computed fall back to object identity,
// which is stable for the same object across its lifetime.
func (d *dispatcher) queueFor(a *alert.Alert) chan dispatchJob { return d.queues[d.queueIndex(a)] }

// queueIndex is the index of the queue queueFor returns.
func (d *dispatcher) queueIndex(a *alert.Alert) int {
	key := a.Fingerprint
	if key == "" {
		key = shardKey(a) // the same object identity key shard ownership uses
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(uint64(h.Sum32()) % uint64(len(d.queues))) //nolint:gosec // G115: the result is below len(d.queues), so it fits an int
}

// queuedTotal is the number of jobs buffered across every worker queue. Used
// for the depth gauge and the shutdown-drain warning, both of which describe
// the pool as a whole rather than any one worker.
func (d *dispatcher) queuedTotal() int {
	n := 0
	for _, q := range d.queues {
		n += len(q)
	}
	return n
}

// dispatchWorkers and dispatchQueueSize read the pool tuning from the
// environment, falling back to the defaults.
func dispatchWorkers() int   { return env.IntOr("ALERTKUBE_DISPATCH_WORKERS", defaultDispatchWorkers) }
func dispatchQueueSize() int { return env.IntOr("ALERTKUBE_DISPATCH_QUEUE", defaultDispatchQueueSize) }

// Start spawns the worker goroutines. Each drains the queue until it is closed
// (by Shutdown), then exits, so a shutdown delivers the queued backlog before
// the workers stop.
func (d *dispatcher) Start() {
	klog.Infof("dispatch pool: %d workers, %d queued alerts per worker (deliveries are fingerprint-affine so a FIRE and its RESOLVE cannot reorder)", len(d.queues), cap(d.queues[0]))
	for _, q := range d.queues {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			for job := range q {
				d.observeDepth()
				span := startDeliverySpan(job)
				delivered := dispatch(d.reg, job.a, job.route)
				endDeliverySpan(span, delivered)
				d.settle(job, delivered)
			}
		}()
	}
}

// settle handles the outcome of one delivery attempt on the worker that made
// it: ack the outbox record, roll back a firing alert's dedupe, re-queue a
// failed resolve, or dead-letter a delivery with no retry path left.
func (d *dispatcher) settle(job dispatchJob, delivered bool) {
	if delivered {
		d.pendingDone(job.id) // delivered: ack the outbox record
		return
	}
	switch {
	case job.onFail != nil:
		// Firing path: roll back dedupe so the next firing retries
		// (as a fresh enqueue); this outbox record is done.
		job.onFail()
		d.pendingDone(job.id)
	case job.a.Resolved && job.retries < maxResolveRetries:
		// A lost resolve would leave a stateful incident dangling;
		// re-queue it after a short delay (bounded attempts). Keep
		// the outbox record - the delivery is still pending.
		d.scheduleResolveRetry(job)
	default:
		// No retry path left: an exhausted resolve (incident may
		// dangle) or a fire-once alert (ephemeral event, group
		// summary, escalation) that failed. Dead-letter it so the
		// permanently-undelivered alert is visible, not just logged.
		if job.a.Resolved {
			klog.Warningf("resolve for %s failed delivery after %d retries; dead-lettering (incident may dangle)", job.a, maxResolveRetries)
		} else {
			klog.Warningf("%s failed delivery with no retry path; dead-lettering", job.a)
		}
		if d.onDeadLetter != nil {
			d.onDeadLetter(job.a)
		}
		d.pendingDone(job.id)
	}
}

// enqueue submits an alert for delivery. It is near-instant while the queue has
// room; when the queue is full it blocks (backpressure) until a worker frees a
// slot or the dispatcher shuts down - it never blocks forever and never drops
// silently except during a shutdown drain race (recorded on DispatchDropped).
// It owns a copy of the alert and route before returning to the producer.
func (d *dispatcher) enqueue(a *alert.Alert, route []string, onFail func()) {
	if len(route) == 0 {
		return
	}
	a = a.Clone()
	route = slices.Clone(route)
	// Open the enqueue span and keep only its linkage on the job: the producer
	// goroutine (an informer handler) returns long before a worker delivers, so
	// the job must not inherit a context that is about to be cancelled.
	ctx, span := enqueueSpan(context.Background(), a, route)
	id := d.nextID.Add(1)
	d.pendingAdd(id, a, route)
	d.submit(dispatchJob{id: id, a: a, route: route, onFail: onFail, traceCtx: trace.Detach(ctx)})
	span.End()
}

// observeDepth republishes the queue depth gauge. Called wherever the queue
// length changes (enqueue, worker pickup) so the gauge tracks backpressure
// rather than being sampled on a timer.
func (d *dispatcher) observeDepth() { metrics.DispatchQueueDepth.Set(float64(d.queuedTotal())) }

// submit queues an already-built job (used by enqueue and by resolve retries,
// which carry a retry count). See enqueue for the backpressure/drop contract.
func (d *dispatcher) submit(job dispatchJob) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		metrics.DispatchDropped.Inc()
		return
	}
	// Fingerprint-affine: this job goes to the one worker that handles every
	// delivery for its alert, so it can never overtake an earlier delivery for
	// the same fingerprint.
	i := d.queueIndex(job.a)
	q := d.queues[i]
	// Producers behind one parked on this queue wait on queueMu, so the
	// blocked time below counts from before the lock.
	blockedSince := time.Now()
	d.queueMu[i].Lock()
	defer d.queueMu[i].Unlock()
	if d.draining[i] {
		// An earlier job for this queue was dropped into the outbox; keep this
		// one behind it (see draining).
		metrics.DispatchDropped.Inc()
		return
	}
	select {
	case q <- job:
		d.observeDepth()
		return
	default:
	}
	// Queue full: record the backpressure, then block until a worker drains a
	// slot or shutdown begins (unblockProducers closes stop and releases us).
	// The caller here is usually an informer handler, so the time spent parked
	// is time Kubernetes event processing is stalled - measure it, or the only
	// symptom is events being handled late with nothing to point at.
	metrics.DispatchQueueFull.Inc()
	select {
	case q <- job:
		metrics.DispatchEnqueueBlocked.Observe(time.Since(blockedSince).Seconds())
		d.observeDepth()
	case <-d.stop:
		d.draining[i] = true
		metrics.DispatchEnqueueBlocked.Observe(time.Since(blockedSince).Seconds())
		metrics.DispatchDropped.Inc()
	}
}

// scheduleResolveRetry re-queues a failed resolve after resolveRetryDelay,
// incrementing its retry count. The timer fires independently of the worker;
// if the dispatcher has since shut down, submit drops the job (recorded on
// DispatchDropped) rather than panicking.
func (d *dispatcher) scheduleResolveRetry(job dispatchJob) {
	job.retries++
	metrics.DispatchResolveRetries.Inc()
	time.AfterFunc(resolveRetryDelay, func() { d.submit(job) })
}

// unblockProducers releases every producer parked in submit on a full queue.
// From then on a submit that finds its queue full drops the job instead of
// waiting. Controller shutdown calls it first: an informer handler or the
// sweeper parked in submit would otherwise hold up stopInformers and wg.Wait,
// which run before Shutdown, for as long as a stuck sink holds the worker.
// With persistence on, a dropped job is not lost: enqueue records it in the
// outbox before submit, so the final state save persists it and it is
// replayed on the next startup. After the first such drop on a queue, every
// later job for that queue is dropped too (see draining), so replay by ID
// keeps a FIRE ahead of its RESOLVE.
func (d *dispatcher) unblockProducers() { d.stopOnce.Do(func() { close(d.stop) }) }

// Shutdown stops accepting new work, drains the queued backlog through the
// workers, and returns once they finish, ctx is done, or dispatchDrainTimeout
// elapses, whichever comes first. Jobs still queued or in flight then stay in
// the outbox for the final state save. It is safe against producers still
// calling enqueue during a shutdown race: closing stop unblocks any
// backpressured enqueue, and the write lock guarantees no send is in flight
// when the jobs channel is closed.
func (d *dispatcher) Shutdown(ctx context.Context) {
	d.shutdownOnce.Do(func() {
		// Unblock backpressured producers before taking the write lock.
		d.unblockProducers()
		d.mu.Lock()
		d.closed = true
		for _, q := range d.queues {
			close(q)
		}
		d.mu.Unlock()

		drainCtx, cancel := context.WithTimeout(ctx, dispatchDrainTimeout)
		defer cancel()
		done := make(chan struct{})
		go func() {
			d.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-drainCtx.Done():
			d.pendingMu.Lock()
			undelivered := len(d.pending)
			d.pendingMu.Unlock()
			klog.Warningf("dispatch drain stopped at its deadline with %d alert(s) still queued; abandoning them (%d undelivered stay in the outbox)", d.queuedTotal(), undelivered)
		}
	})
}
