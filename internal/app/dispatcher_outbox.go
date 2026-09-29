package app

import (
	"cmp"
	"slices"

	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/metrics"
)

// pendingAdd records a delivery in the durable outbox. It stores a
// Details-stripped clone so the persisted record stays small and does not share
// mutable maps with the live alert.
func (d *dispatcher) pendingAdd(id uint64, a *alert.Alert, route []string) {
	rec := alert.PendingDelivery{ID: id, Alert: a.CloneWithoutDetails(), Route: slices.Clone(route)}
	d.pendingMu.Lock()
	d.pending[id] = rec
	d.pendingGen++
	metrics.OutboxPending.Set(float64(len(d.pending)))
	d.pendingMu.Unlock()
}

// pendingDone acks (removes) an outbox record once its delivery reaches a
// terminal outcome (delivered, rolled back, or dead-lettered). Unknown IDs are
// a no-op.
func (d *dispatcher) pendingDone(id uint64) {
	d.pendingMu.Lock()
	if _, ok := d.pending[id]; ok {
		delete(d.pending, id)
		d.pendingGen++
		metrics.OutboxPending.Set(float64(len(d.pending)))
	}
	d.pendingMu.Unlock()
}

// PendingSnapshot returns owned records in delivery-ID order for persistence.
func (d *dispatcher) PendingSnapshot() []alert.PendingDelivery {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	out := make([]alert.PendingDelivery, 0, len(d.pending))
	for _, rec := range d.pending {
		rec.Alert = rec.Alert.Clone()
		rec.Route = slices.Clone(rec.Route)
		out = append(out, rec)
	}
	slices.SortFunc(out, func(a, b alert.PendingDelivery) int { return cmp.Compare(a.ID, b.ID) })
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
// enqueued-but-undelivered alert resumes delivery after a restart. Delivery IDs
// restore order even for older snapshots that serialized the pending map in
// arbitrary order. Returns the number replayed. Call after Start.
//
// owns (nil means own everything) gates each record on shard ownership. The
// emit path is gated at the producer, but a replay bypasses it entirely: after
// a shard rebalance - which is exactly the ALERTKUBE_SHARD_TOTAL rollout the
// docs prescribe - an object's owner moves, and replaying its record here would
// double-page alongside the new owner. Foreign records are dropped, not
// delivered, and counted so a rebalance's fallout is visible.
// rollback (may be nil) forgets a fingerprint's dedupe state after a replayed
// *firing* alert fails every sink, so the next watch event or resync re-emits
// it. Without this a replayed firing is strictly less durable than a fresh one:
// a fresh firing carries an onFail that rolls dedupe back, while a replayed one
// had none and so was dead-lettered on its first failure - the outbox making an
// alert *less* likely to survive, which inverts its purpose. Resolves are
// excluded: they already have the bounded resolve-retry path. Ephemeral events
// and grouped summaries retain their fire-once, dead-letter-on-failure behavior.
func (d *dispatcher) ReplayPending(recs []alert.PendingDelivery, owns func(*alert.Alert) bool, rollback func(fingerprint string)) int {
	recs = slices.Clone(recs)
	slices.SortStableFunc(recs, func(a, b alert.PendingDelivery) int { return cmp.Compare(a.ID, b.ID) })
	n, foreign := 0, 0
	for _, rec := range recs {
		if rec.Alert == nil || len(rec.Route) == 0 {
			continue
		}
		if owns != nil && !owns(rec.Alert) {
			metrics.OutboxReplayForeign.Inc()
			foreign++
			continue
		}
		var onFail func()
		if fp := rec.Alert.Fingerprint; rollback != nil && !rec.Alert.Resolved && !rec.Alert.Event && rec.Alert.Labels["alertkube-grouped"] != "true" && fp != "" {
			onFail = func() { rollback(fp) }
		}
		d.enqueue(rec.Alert, rec.Route, onFail)
		n++
	}
	if foreign > 0 {
		klog.Infof("outbox replay: dropped %d record(s) owned by another shard (expected after a shard rebalance); replayed %d", foreign, n)
	}
	return n
}
