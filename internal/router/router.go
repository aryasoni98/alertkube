package router

import (
	"strings"
	"sync"
	"time"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/silence"
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

	// runtimeSilences holds UI-created, time-boxed silences. Nil when the
	// runtime control plane is not wired (e.g. unit tests), in which case only
	// annotation and config silences apply. Its own lock guards concurrent
	// reads against API writes.
	runtimeSilences *silence.Store

	// maintenance holds recurring daily suppression windows (backup/patch
	// windows). Like config silences they are operator-controlled (from Git).
	maintenance []config.MaintenanceWindow

	// crdSilences, when set, returns Silence CRs as config.Silence values. It is
	// a func (not a slice) so the router reads the live cached set on every
	// decision without the router importing the crd package. Nil when CRD
	// watching is disabled.
	crdSilences func() []config.Silence

	// mu is an RWMutex: inhibited() runs on every firing alert and only reads
	// activeInhibits, so it takes a shared read lock and concurrent routing
	// decisions do not serialize on it. maybeArmInhibition (the write path)
	// takes the exclusive lock and also prunes expired keys, so pruning stays
	// off the hot read path.
	mu             sync.RWMutex
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
		if r.inMaintenance(a, time.Now()) {
			metrics.AlertsSuppressed.WithLabelValues("maintenance").Inc()
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

// SetRuntimeSilences wires the runtime (UI-created) silence store. Call before
// the first Route.
func (r *Router) SetRuntimeSilences(s *silence.Store) {
	r.runtimeSilences = s
}

// SetMaintenance wires recurring maintenance windows. Call before the first
// Route. Windows are operator-controlled (config/Git) like config silences.
func (r *Router) SetMaintenance(w []config.MaintenanceWindow) {
	r.maintenance = w
}

// SetCRDSilences wires a provider of Silence-CRD-backed silences. The provider
// returns the live cached set (matchers + RFC3339 until); the router applies the
// same expiry/matching it uses for file silences. Call before the first Route.
func (r *Router) SetCRDSilences(provider func() []config.Silence) {
	r.crdSilences = provider
}

func (r *Router) silenced(a *alert.Alert) bool {
	now := time.Now()
	// Annotation-based silence: `alert-silence-until: RFC3339`
	if until, ok := a.Annotations[alert.AnnotationSilenceUntil]; ok && !r.disableAnnotationSilences {
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
	// Silence CRs (kubectl/GitOps-managed). Same shape and matching as file
	// silences; the CRD's etcd is their source of truth. Nil provider when CRD
	// watching is disabled.
	if r.crdSilences != nil {
		for _, s := range r.crdSilences() {
			if !a.MatchLabels(s.Matchers) {
				continue
			}
			if t, err := time.Parse(time.RFC3339, s.Until); err == nil && now.Before(t) {
				return true
			}
		}
	}
	// Runtime (UI-created) silences: time-boxed mutes added without a redeploy.
	if r.runtimeSilences != nil {
		for _, s := range r.runtimeSilences.Active(now) {
			if a.MatchLabels(s.Matchers) {
				return true
			}
		}
	}
	return false
}

// inMaintenance reports whether a recurring maintenance window currently
// suppresses the alert. Kept separate from silenced() so the suppression metric
// can attribute it to "maintenance" rather than "silenced".
func (r *Router) inMaintenance(a *alert.Alert, now time.Time) bool {
	for _, w := range r.maintenance {
		if a.MatchLabels(w.Matchers) && w.Active(now) {
			return true
		}
	}
	return false
}

func (r *Router) inhibited(a *alert.Alert) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	// Read-only: expired keys are ignored by the now.Before(exp) check and
	// reclaimed later by maybeArmInhibition, so no pruning (a write) happens
	// on this hot path.
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
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	// Arm is the write path and the only place the map grows, so reclaim
	// expired keys here rather than on the hot inhibited() read path.
	r.pruneExpiredLocked(now)
	for _, inh := range r.inhibitions {
		if !a.MatchLabels(inh.Source) {
			continue
		}
		r.activeInhibits[inhibitKey(inh, a)] = now.Add(inh.DurationParsed())
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
