package app

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/sinks"
)

// The regression D4 exists for: after a shard rebalance an object's owner
// moves, so replaying its outbox record from the old owner double-pages
// alongside the new one. Foreign records must be dropped and counted.
func TestReplayPendingDropsForeignShardRecords(t *testing.T) {
	d := newDispatcher(sinks.NewRegistry(), 2, 8)
	mine := alert.New(alert.KindPod, "ns", "mine", "Boom", alert.SeverityCritical)
	theirs := alert.New(alert.KindPod, "ns", "theirs", "Boom", alert.SeverityCritical)

	before := testutil.ToFloat64(metrics.OutboxReplayForeign)
	n := d.ReplayPending([]alert.PendingDelivery{
		{ID: 1, Alert: mine, Route: []string{"stdout"}},
		{ID: 2, Alert: theirs, Route: []string{"stdout"}},
	}, func(a *alert.Alert) bool { return a.Name == "mine" }, nil)

	if n != 1 {
		t.Fatalf("replayed %d records, want only the owned one", n)
	}
	if after := testutil.ToFloat64(metrics.OutboxReplayForeign); after != before+1 {
		t.Fatalf("OutboxReplayForeign = %v, want %v (a dropped foreign record must be observable)", after, before+1)
	}
	// Only the owned record may occupy an outbox slot; a dropped one must not
	// linger as pending forever.
	if got := len(d.PendingSnapshot()); got != 1 {
		t.Fatalf("outbox holds %d records, want 1 (the foreign one must not be tracked)", got)
	}
}

// A nil owns func means "own everything" - the unsharded path, which must be
// unchanged.
func TestReplayPendingNilOwnsReplaysEverything(t *testing.T) {
	d := newDispatcher(sinks.NewRegistry(), 2, 8)
	a := alert.New(alert.KindPod, "ns", "p", "Boom", alert.SeverityCritical)
	b := alert.New(alert.KindPod, "ns", "q", "Boom", alert.SeverityCritical)
	if n := d.ReplayPending([]alert.PendingDelivery{
		{ID: 1, Alert: a, Route: []string{"stdout"}},
		{ID: 2, Alert: b, Route: []string{"stdout"}},
	}, nil, nil); n != 2 {
		t.Fatalf("replayed %d, want 2 when unsharded", n)
	}
}

// The regression D5 exists for: a FIRE and its RESOLVE share a fingerprint, so
// they must be handled by one worker. If they can land on two workers, a slow
// FIRE can complete after its RESOLVE and leave a stateful sink holding an
// incident that never closes.
func TestQueueForIsFingerprintAffine(t *testing.T) {
	d := newDispatcher(sinks.NewRegistry(), 8, 64)
	fire := alert.New(alert.KindPod, "ns", "web-1", "CrashLoopBackOff", alert.SeverityCritical)

	// The resolve for the same alert carries the same fingerprint (the store
	// hands back the stored alert), so it must route identically.
	resolve := fire.Clone()
	resolve.Resolved = true

	if d.queueFor(fire) != d.queueFor(resolve) {
		t.Fatal("a FIRE and its RESOLVE hashed to different workers; they can complete out of order and dangle a stateful incident")
	}
}

// Affinity must not collapse everything onto one worker - that would serialize
// the whole pipeline and erase the point of the pool.
func TestQueueForSpreadsAcrossWorkers(t *testing.T) {
	d := newDispatcher(sinks.NewRegistry(), 8, 64)
	seen := map[chan dispatchJob]bool{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"} {
		seen[d.queueFor(alert.New(alert.KindPod, "ns", name, "Boom", alert.SeverityCritical))] = true
	}
	if len(seen) < 2 {
		t.Fatalf("12 distinct fingerprints hashed to %d worker(s); affinity must still spread load", len(seen))
	}
}

// An alert enqueued before a fingerprint was computed must still route by a
// stable key, so two separate deliveries for the same object cannot reorder.
func TestQueueForFallsBackToObjectIdentity(t *testing.T) {
	d := newDispatcher(sinks.NewRegistry(), 4, 16)
	first := &alert.Alert{Kind: alert.KindPod, Namespace: "ns", Name: "no-fp"}
	// A distinct value with the same identity: it must land on the same worker.
	second := &alert.Alert{Kind: alert.KindPod, Namespace: "ns", Name: "no-fp", Resolved: true}
	if d.queueFor(first) != d.queueFor(second) {
		t.Fatal("two fingerprint-less deliveries for one object hashed to different workers; they can reorder")
	}
	// A different object must not be forced onto the same worker as a side
	// effect of the fallback (e.g. by hashing a constant).
	other := &alert.Alert{Kind: alert.KindPod, Namespace: "ns", Name: "other-object"}
	if d.queueFor(first) == d.queueFor(other) && d.queueFor(first) == d.queueFor(&alert.Alert{Kind: alert.KindNode, Name: "n1"}) {
		t.Fatal("the fingerprint-less fallback appears to ignore identity; all alerts would serialize on one worker")
	}
}

// The process-wide queue bound must not silently multiply with the worker
// count: capacity is split across workers, not duplicated per worker.
func TestPerWorkerQueueSplitsTheConfiguredCapacity(t *testing.T) {
	d := newDispatcher(sinks.NewRegistry(), 4, 64)
	if got := cap(d.queues[0]); got != 16 {
		t.Fatalf("per-worker capacity = %d, want 64/4 = 16", got)
	}
	// More workers than slots must still give every worker a usable queue.
	d2 := newDispatcher(sinks.NewRegistry(), 8, 4)
	if got := cap(d2.queues[7]); got < 1 {
		t.Fatalf("per-worker capacity = %d, want at least 1", got)
	}
}
