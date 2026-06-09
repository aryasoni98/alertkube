package router

import (
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
func (r *Router) Route(a *alert.Alert) []string {
	if r.silenced(a) {
		metrics.AlertsSuppressed.WithLabelValues("silenced").Inc()
		return nil
	}
	if r.inhibited(a) {
		metrics.AlertsSuppressed.WithLabelValues("inhibited").Inc()
		return nil
	}
	r.maybeArmInhibition(a)

	for _, route := range r.routes {
		if a.MatchLabels(route.Match) {
			return route.Sinks
		}
	}
	return r.defaultSinks
}

func (r *Router) silenced(a *alert.Alert) bool {
	now := time.Now()
	// Annotation-based silence: `alert-silence-until: RFC3339`
	if until, ok := a.Annotations["alert-silence-until"]; ok {
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
	key := ""
	for _, k := range inh.Equal {
		switch k {
		case "namespace":
			key += "ns:" + a.Namespace + "|"
		case "node":
			key += "node:" + a.NodeName + "|"
		case "kind":
			key += "kind:" + string(a.Kind) + "|"
		default:
			key += k + ":" + a.Labels[k] + "|"
		}
	}
	return key
}
