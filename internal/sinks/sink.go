package sinks

import (
	"context"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/metrics"
)

// Sink is the interface every alert delivery target implements.
type Sink interface {
	Name() string
	Send(ctx context.Context, a *alert.Alert) error
	Supports(sev alert.Severity) bool
}

// Registry tracks sinks by name.
type Registry struct {
	sinks map[string]Sink
}

func NewRegistry() *Registry { return &Registry{sinks: map[string]Sink{}} }

func (r *Registry) Add(s Sink) { r.sinks[s.Name()] = s }

func (r *Registry) Get(name string) (Sink, bool) {
	s, ok := r.sinks[name]
	return s, ok
}

func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.sinks))
	for n := range r.sinks {
		out = append(out, n)
	}
	return out
}

// Dispatch fans an alert to the named sinks, recording metrics.
func (r *Registry) Dispatch(ctx context.Context, a *alert.Alert, names []string) {
	for _, name := range names {
		s, ok := r.sinks[name]
		if !ok || !s.Supports(a.Severity) {
			continue
		}
		start := time.Now()
		err := s.Send(ctx, a)
		result := "ok"
		if err != nil {
			result = "error"
			metrics.SinkErrors.WithLabelValues(name).Inc()
		}
		metrics.SinkSendDuration.WithLabelValues(name, result).Observe(time.Since(start).Seconds())
	}
}
