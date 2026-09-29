package app

import (
	"context"
	"time"

	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/config"
	"github.com/aryasoni98/alertkube/v2/internal/metrics"
	"github.com/aryasoni98/alertkube/v2/internal/persist"
	"github.com/aryasoni98/alertkube/v2/internal/silence"
)

// sweepInterval is the period of the sweep loop: TTL resolves, history
// cleanup, the liveness heartbeat, silence pruning, escalations, and the
// periodic state save all run once per tick.
const sweepInterval = 30 * time.Second

func runSweeper(ctx context.Context, store *alert.Store, silStore *silence.Store, persister persist.Store, disp *dispatcher, escalations []config.Escalation) {
	// The sweeper is the controller's liveness heartbeat source: it runs only
	// on the leader (or the sole process), touches the store's global mutex
	// every tick, and so a stalled sweep (e.g. a store-lock deadlock) makes
	// /healthz fail and the kubelet restart the pod. SetLeading resets the
	// staleness window to now; the defer clears it on shutdown/lease loss so a
	// demoted follower is not judged by a stale leader heartbeat.
	metrics.SetLeading(true)
	defer metrics.SetLeading(false)
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	var savedGen, savedSilGen, savedPendGen uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			store.SweepResolved()
			store.CleanOldHistory()
			// Heartbeat after the store-touching work above so a stalled store
			// lock (not just a dead ticker) is what keeps the beat fresh.
			metrics.Heartbeat()
			// Age out expired runtime silences so the persisted set stays
			// bounded; a prune bumps the silence generation, which triggers
			// the save below.
			silStore.PruneExpired(time.Now())
			runEscalations(store, disp.enqueue, escalations)
			if persister == nil {
				continue
			}
			// Capture the generations before exporting: a mutation racing
			// the export is included in the snapshot AND re-saved next
			// sweep, so no state change is ever silently dropped. A change in
			// the alert store, the silence store, OR the delivery outbox
			// warrants a save.
			gen, silGen, pendGen := store.Generation(), silStore.Generation(), disp.PendingGeneration()
			if gen == savedGen && silGen == savedSilGen && pendGen == savedPendGen {
				continue
			}
			saveCtx, saveCancel := context.WithTimeout(ctx, 10*time.Second)
			err := persister.Save(saveCtx, exportState(store, silStore, disp))
			saveCancel()
			if err != nil {
				klog.Warningf("state save: %v", err)
				continue
			}
			savedGen, savedSilGen, savedPendGen = gen, silGen, pendGen
		}
	}
}

// runEscalations re-dispatches still-active alerts that outlived an
// escalation rule's delay. Store.Overdue marks matches so each rule fires
// at most once per alert lifetime; marks clear when the alert resolves.
//
// Each overdue alert is handed to the dispatch worker pool via enqueue rather
// than sent inline: delivery blocks up to dispatchTimeout, so serializing it
// here would stall the sweep loop (and the resolve sweep behind it) when
// several alerts escalate at once. Enqueue returns immediately; the pool
// performs the fan-out.
func runEscalations(store *alert.Store, enqueue enqueueFunc, escalations []config.Escalation) {
	for _, esc := range escalations {
		after := time.Duration(esc.AfterMinutes) * time.Minute
		ruleKey := config.EscalationKey(esc)
		for _, a := range store.Overdue(after, ruleKey, esc.Match) {
			// Overdue returns an owned clone, so tagging it cannot touch the
			// stored alert. Labels is nil for an alert restored from a
			// snapshot with null labels; assigning into it would panic.
			if a.Labels == nil {
				a.Labels = map[string]string{}
			}
			a.Labels["alertkube-escalated"] = "true"
			a.Summary = "[ESCALATED - unresolved after " + after.String() + "] " + a.Summary
			metrics.EscalationsTotal.Inc()
			klog.Infof("escalating %s to %v (%s)", a, esc.Sinks, ruleKey)
			enqueue(a, esc.Sinks, nil)
		}
	}
}
