package app

import (
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/env"
	"alertkube/internal/metrics"
	"alertkube/internal/sinks"
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
	// dispatchDrainTimeout bounds how long shutdown waits for the workers to
	// finish the queued backlog. Each in-flight send is itself capped by the
	// per-sink timeout budget; this is the ceiling on the whole drain.
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
	reg     *sinks.Registry
	jobs    chan dispatchJob
	stop    chan struct{}
	workers int
	wg      sync.WaitGroup

	// mu guards closed and serializes enqueue against Shutdown so the jobs
	// channel is never sent-on after it is closed. Held as RLock by enqueue
	// (many concurrent producers) and Lock by Shutdown (once).
	mu     sync.RWMutex
	closed bool

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
	return &dispatcher{
		reg:     reg,
		jobs:    make(chan dispatchJob, queueSize),
		stop:    make(chan struct{}),
		workers: workers,
		pending: map[uint64]alert.PendingDelivery{},
	}
}

// dispatchWorkers and dispatchQueueSize read the pool tuning from the
// environment, falling back to the defaults.
func dispatchWorkers() int   { return env.IntOr("ALERTKUBE_DISPATCH_WORKERS", defaultDispatchWorkers) }
func dispatchQueueSize() int { return env.IntOr("ALERTKUBE_DISPATCH_QUEUE", defaultDispatchQueueSize) }

// Start spawns the worker goroutines. Each drains the queue until it is closed
// (by Shutdown), then exits, so a shutdown delivers the queued backlog before
// the workers stop.
func (d *dispatcher) Start() {
	klog.Infof("dispatch pool: %d workers, queue capacity %d", d.workers, cap(d.jobs))
	for i := 0; i < d.workers; i++ {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			for job := range d.jobs {
				metrics.DispatchQueueDepth.Set(float64(len(d.jobs)))
				if dispatch(d.reg, job.a, job.route) {
					d.pendingDone(job.id) // delivered: ack the outbox record
					continue
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
		}()
	}
}

// enqueue submits an alert for delivery. It is near-instant while the queue has
// room; when the queue is full it blocks (backpressure) until a worker frees a
// slot or the dispatcher shuts down - it never blocks forever and never drops
// silently except during a shutdown drain race (recorded on DispatchDropped).
func (d *dispatcher) enqueue(a *alert.Alert, route []string, onFail func()) {
	if len(route) == 0 {
		return
	}
	id := d.nextID.Add(1)
	d.pendingAdd(id, a, route)
	d.submit(dispatchJob{id: id, a: a, route: route, onFail: onFail})
}

// pendingAdd records a delivery in the durable outbox. It stores a
// Details-stripped clone so the persisted record stays small and does not share
// mutable maps with the live alert.
func (d *dispatcher) pendingAdd(id uint64, a *alert.Alert, route []string) {
	cp := a.Clone()
	cp.Details = nil
	rec := alert.PendingDelivery{ID: id, Alert: cp, Route: append([]string(nil), route...)}
	d.pendingMu.Lock()
	d.pending[id] = rec
	d.pendingGen++
	metrics.OutboxPending.Set(float64(len(d.pending)))
	d.pendingMu.Unlock()
}

// pendingDone acks (removes) an outbox record once its delivery reaches a
// terminal outcome (delivered, rolled back, or dead-lettered). id 0 (replayed
// jobs may reuse this path) is a no-op.
func (d *dispatcher) pendingDone(id uint64) {
	d.pendingMu.Lock()
	if _, ok := d.pending[id]; ok {
		delete(d.pending, id)
		d.pendingGen++
		metrics.OutboxPending.Set(float64(len(d.pending)))
	}
	d.pendingMu.Unlock()
}

// PendingSnapshot returns the outbox as durable records for persistence.
func (d *dispatcher) PendingSnapshot() []alert.PendingDelivery {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	out := make([]alert.PendingDelivery, 0, len(d.pending))
	for _, rec := range d.pending {
		out = append(out, rec)
	}
	return out
}

// PendingGeneration increments on every outbox add/remove; the save loop
// compares it to skip no-op saves (mirrors alert.Store.Generation).
func (d *dispatcher) PendingGeneration() uint64 {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	return d.pendingGen
}

// ReplayPending re-enqueues outbox records restored from a snapshot so an
// enqueued-but-undelivered alert resumes delivery after a restart. Records are
// replayed as fire-once (no dedupe rollback): they were already routed before
// the crash, so a re-delivery is the correct at-least-once behavior. Returns
// the number replayed. Call after Start.
func (d *dispatcher) ReplayPending(recs []alert.PendingDelivery) int {
	n := 0
	for _, rec := range recs {
		if rec.Alert == nil || len(rec.Route) == 0 {
			continue
		}
		id := d.nextID.Add(1)
		d.pendingAdd(id, rec.Alert, rec.Route)
		d.submit(dispatchJob{id: id, a: rec.Alert, route: rec.Route})
		n++
	}
	return n
}

// submit queues an already-built job (used by enqueue and by resolve retries,
// which carry a retry count). See enqueue for the backpressure/drop contract.
func (d *dispatcher) submit(job dispatchJob) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		metrics.DispatchDropped.Inc()
		return
	}
	select {
	case d.jobs <- job:
		metrics.DispatchQueueDepth.Set(float64(len(d.jobs)))
		return
	default:
	}
	// Queue full: record the backpressure, then block until a worker drains a
	// slot or shutdown begins (close(stop) unblocks us so Shutdown can proceed).
	metrics.DispatchQueueFull.Inc()
	select {
	case d.jobs <- job:
		metrics.DispatchQueueDepth.Set(float64(len(d.jobs)))
	case <-d.stop:
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

// Shutdown stops accepting new work, drains the queued backlog through the
// workers, and returns once they finish or dispatchDrainTimeout elapses. It is
// safe against producers still calling enqueue during a shutdown race: closing
// stop unblocks any backpressured enqueue, and the write lock guarantees no
// send is in flight when the jobs channel is closed.
func (d *dispatcher) Shutdown() {
	// Unblock any enqueue parked on a full queue so it can observe the shutdown
	// and release its read lock, letting the write lock below proceed.
	close(d.stop)
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	close(d.jobs)
	d.mu.Unlock()

	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(dispatchDrainTimeout):
		klog.Warningf("dispatch drain timed out after %s with %d alert(s) still queued; abandoning them", dispatchDrainTimeout, len(d.jobs))
	}
}
