package router

import (
	"strings"
	"sync"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/metrics"
)

// Router decides which sinks an alert goes to, applies inhibitions and silences.
type Router struct {
	routes       []config.Route
	inhibitions  []config.Inhibition
	silences     []config.Silence
	defaultSinks []string
	// disableAnnotationSilences ignores `alert-silence-until` annotations
	// so workload authors cannot self-silence. Config-file silences still
	// apply (those are operator-controlled).
	disableAnnotationSilences bool

	mu             sync.Mutex
	activeInhibits map[string]time.Time // equal-key -> expiry
}

func New(routes []config.Route, inhibitions []config.Inhibition, silences []config.Silence, defaultSinks []string) *Router {
	return &Router{
		routes:         routes,
		inhibitions:    inhibitions,
		silences:       silences,
		defaultSinks:   defaultSinks,
		activeInhibits: map[string]time.Time{},
	}
}

// Route returns the sinks an alert should fan out to (nil = drop).
// Resolved alerts skip silences and inhibitions entirely: a resolve must
// reach the sinks that saw the trigger (else PagerDuty incidents dangle),
// must not arm inhibitions, and must not count as suppressed.
func (r *Router) Route(a *alert.Alert) []string {
	if !a.Resolved {
		if r.silenced(a) {
			metrics.AlertsSuppressed.WithLabelValues("silenced").Inc()
			return nil
		}
		if r.inhibited(a) {
			metrics.AlertsSuppressed.WithLabelValues("inhibited").Inc()
			return nil
		}
		r.maybeArmInhibition(a)
	}

	for _, route := range r.routes {
		if a.MatchLabels(route.Match) {
			return route.Sinks
		}
	}
	return r.defaultSinks
}

// ArmInhibitions refreshes inhibition expiries for a firing source alert
// without running the full routing decision. Callers use this for muted
// re-fires: a NodeNotReady that keeps firing inside its mute window must
// keep its pod inhibitions armed, otherwise they expire while the node is
// still down and the pod alert storm leaks through.
func (r *Router) ArmInhibitions(a *alert.Alert) {
	if a.Resolved {
		return
	}
	r.maybeArmInhibition(a)
}

// SetDisableAnnotationSilences toggles whether `alert-silence-until`
// annotations are honored. Call before the first Route.
func (r *Router) SetDisableAnnotationSilences(disabled bool) {
	r.disableAnnotationSilences = disabled
}

func (r *Router) silenced(a *alert.Alert) bool {
	now := time.Now()
	// Annotation-based silence: `alert-silence-until: RFC3339`
	if until, ok := a.Annotations["alert-silence-until"]; ok && !r.disableAnnotationSilences {
		if t, err := time.Parse(time.RFC3339, until); err == nil && now.Before(t) {
			return true
		}
	}
	for _, s := range r.silences {
		if !a.MatchLabels(s.Matchers) {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s.Until); err == nil && now.Before(t) {
			return true
		}
	}
	return false
}

func (r *Router) inhibited(a *alert.Alert) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.pruneExpiredLocked(now)
	for _, inh := range r.inhibitions {
		if !a.MatchLabels(inh.Target) {
			continue
		}
		key := inhibitKey(inh, a)
		if exp, ok := r.activeInhibits[key]; ok && now.Before(exp) {
			return true
		}
	}
	return false
}

// pruneExpiredLocked drops inhibition keys past their expiry. Caller holds r.mu.
func (r *Router) pruneExpiredLocked(now time.Time) {
	for key, exp := range r.activeInhibits {
		if !now.Before(exp) {
			delete(r.activeInhibits, key)
		}
	}
}

func (r *Router) maybeArmInhibition(a *alert.Alert) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, inh := range r.inhibitions {
		if !a.MatchLabels(inh.Source) {
			continue
		}
		r.activeInhibits[inhibitKey(inh, a)] = time.Now().Add(inh.DurationParsed())
	}
}

func inhibitKey(inh config.Inhibition, a *alert.Alert) string {
	var b strings.Builder
	for _, k := range inh.Equal {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(a.FieldValue(k))
		b.WriteByte('|')
	}
	return b.String()
}
