package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/metrics"
	"alertkube/internal/persist"
	"alertkube/internal/sinks"
)

func runSweeper(ctx context.Context, wg *sync.WaitGroup, store *alert.Store, persister *persist.ConfigMapStore, reg *sinks.Registry, cfg *config.Config) {
	defer wg.Done()
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	var savedGen uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			store.SweepResolved()
			store.CleanOldHistory()
			runEscalations(ctx, store, reg, cfg)
			if persister == nil {
				continue
			}
			// Capture the generation before exporting: a mutation racing
			// the export is included in the snapshot AND re-saved next
			// sweep, so no state change is ever silently dropped.
			gen := store.Generation()
			if gen == savedGen {
				continue
			}
			saveCtx, saveCancel := context.WithTimeout(ctx, 10*time.Second)
			err := persister.Save(saveCtx, store.Export())
			saveCancel()
			if err != nil {
				klog.Warningf("state save: %v", err)
				continue
			}
			savedGen = gen
		}
	}
}

// runEscalations re-dispatches still-active alerts that outlived an
// escalation rule's delay. Store.Overdue marks matches so each rule fires
// at most once per alert lifetime; marks clear when the alert resolves.
func runEscalations(ctx context.Context, store *alert.Store, reg *sinks.Registry, cfg *config.Config) {
	for i, esc := range cfg.Escalations {
		after := time.Duration(esc.AfterMinutes) * time.Minute
		ruleKey := fmt.Sprintf("rule%d", i)
		for _, a := range store.Overdue(after, ruleKey, esc.Match) {
			// Clone Labels before tagging: the copy still shares the map
			// with the stored alert.
			labels := make(map[string]string, len(a.Labels)+1)
			for k, v := range a.Labels {
				labels[k] = v
			}
			labels["alertkube-escalated"] = "true"
			a.Labels = labels
			a.Summary = "[ESCALATED - unresolved after " + after.String() + "] " + a.Summary
			metrics.EscalationsTotal.Inc()
			klog.Infof("escalating %s to %v (%s)", a, esc.Sinks, ruleKey)
			reg.Dispatch(ctx, a, esc.Sinks)
		}
	}
}
