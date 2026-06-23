// Package rules evaluates user-authored correlation rules against the live
// alert stream (watchers + cloud sources) and emits derived alerts when a
// rule's condition holds. Derived alerts flow back through the same
// dedupe/route/group/sink pipeline as any other alert, so they get firing,
// mute-window dedupe, and TTL-based auto-resolve for free.
//
// Three rule shapes (config.Rule):
//   - Count : >= Threshold alerts matching a selector within a window (storm /
//     correlation).
//   - All   : every selector in a list had >= 1 match within a window
//     (composite AND / multi-condition).
//   - Absent: NO alert matching a selector for ForSeconds (heartbeat /
//     dead-man's-switch), evaluated on a timer.
//
// Count and All are evaluated synchronously on each Observe; Absent is
// evaluated by Run's ticker. Derived alerts (kind Derived) are never fed back
// into the engine, so a rule cannot trigger itself.
package rules

import (
	"context"
	"sync"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

// absentTick is how often Absent (heartbeat) rules are re-evaluated.
const absentTick = 30 * time.Second

// derivedReason is the reason on every derived alert; the rule name is the
// alert Name, so the fingerprint sha256(Derived||name|DerivedRule) is stable
// per rule and the mute window debounces re-fires.
const derivedReason = "DerivedRule"

// Emit publishes an alert into the controller pipeline (same shape as
// watchers.Emit / sources.Emit).
type Emit = func(*alert.Alert)

// Engine holds rule state. Observe is called from the emitter goroutine(s) and
// Run's ticker from its own goroutine; a single mutex guards the occurrence
// bookkeeping.
type Engine struct {
	mu      sync.Mutex
	rules   []config.Rule
	emit    Emit
	start   time.Time
	seen    [][]time.Time   // Count rules: ascending match timestamps
	allSeen [][][]time.Time // All rules: per-group ascending timestamps
	last    []time.Time     // Absent rules: time of last match (zero = never)
	now     func() time.Time
}

// New builds an Engine for the given rules. emit is the controller's pipeline
// entry point; the engine sends derived alerts through it.
func New(rules []config.Rule, emit Emit) *Engine {
	e := &Engine{
		rules:   rules,
		emit:    emit,
		now:     time.Now,
		start:   time.Now(),
		seen:    make([][]time.Time, len(rules)),
		allSeen: make([][][]time.Time, len(rules)),
		last:    make([]time.Time, len(rules)),
	}
	for i, r := range rules {
		if len(r.All) > 0 {
			e.allSeen[i] = make([][]time.Time, len(r.All))
		}
	}
	return e
}

// Enabled reports whether any rules are configured.
func (e *Engine) Enabled() bool { return e != nil && len(e.rules) > 0 }

// Observe feeds one firing alert to the rules. Resolved alerts are not
// occurrences, and Derived alerts are ignored so a rule cannot feed itself.
func (e *Engine) Observe(a *alert.Alert) {
	if e == nil || a == nil || a.Resolved || a.Kind == alert.KindDerived {
		return
	}
	now := e.now()
	var fired []*alert.Alert
	e.mu.Lock()
	for i := range e.rules {
		r := &e.rules[i]
		switch {
		case r.Count != nil:
			if a.MatchLabels(r.Count.Match) {
				e.seen[i] = append(e.seen[i], now)
			}
			e.seen[i] = prune(e.seen[i], now, secs(r.WindowSeconds))
			if len(e.seen[i]) >= r.Count.Threshold {
				fired = append(fired, derived(r))
			}
		case len(r.All) > 0:
			for g, m := range r.All {
				if a.MatchLabels(m) {
					e.allSeen[i][g] = append(e.allSeen[i][g], now)
				}
				e.allSeen[i][g] = prune(e.allSeen[i][g], now, secs(r.WindowSeconds))
			}
			if allGroupsActive(e.allSeen[i]) {
				fired = append(fired, derived(r))
			}
		case r.Absent != nil:
			if a.MatchLabels(r.Absent.Match) {
				e.last[i] = now
			}
		}
	}
	e.mu.Unlock()
	for _, d := range fired {
		e.emit(d)
	}
}

// Run evaluates Absent rules on a ticker until ctx is cancelled. It returns
// immediately when no Absent rules are configured (Count/All need no timer).
func (e *Engine) Run(ctx context.Context) {
	if e == nil || !e.hasAbsent() {
		return
	}
	t := time.NewTicker(absentTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.evalAbsent()
		}
	}
}

func (e *Engine) hasAbsent() bool {
	for i := range e.rules {
		if e.rules[i].Absent != nil {
			return true
		}
	}
	return false
}

// evalAbsent fires a derived alert for each Absent rule with no match within
// its window. The window is measured from the last match, or from engine start
// when nothing has matched yet, so a heartbeat gets one full window of grace
// after boot before it can fire.
func (e *Engine) evalAbsent() {
	now := e.now()
	var fired []*alert.Alert
	e.mu.Lock()
	for i := range e.rules {
		r := &e.rules[i]
		if r.Absent == nil {
			continue
		}
		ref := e.last[i]
		if ref.IsZero() {
			ref = e.start
		}
		if now.Sub(ref) > secs(r.Absent.ForSeconds) {
			fired = append(fired, derived(r))
		}
	}
	e.mu.Unlock()
	for _, d := range fired {
		e.emit(d)
	}
}

// derived builds the alert a rule emits when it fires.
func derived(r *config.Rule) *alert.Alert {
	a := alert.New(alert.KindDerived, "", r.Name, derivedReason, alert.Severity(r.Severity))
	if r.Summary != "" {
		a.Summary = r.Summary
	} else {
		a.Summary = "rule " + r.Name + " fired"
	}
	a.Labels["rule"] = r.Name
	return a
}

// prune drops timestamps older than now-d. ts is ascending (Observe appends
// now), so a single leading-trim suffices.
func prune(ts []time.Time, now time.Time, d time.Duration) []time.Time {
	cutoff := now.Add(-d)
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	return ts[i:]
}

// allGroupsActive reports whether every group has at least one live timestamp.
func allGroupsActive(groups [][]time.Time) bool {
	if len(groups) == 0 {
		return false
	}
	for _, g := range groups {
		if len(g) == 0 {
			return false
		}
	}
	return true
}

func secs(n int) time.Duration { return time.Duration(n) * time.Second }
