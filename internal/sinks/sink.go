package sinks

import (
	"context"
	"runtime/debug"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/metrics"
)

// perSinkTimeout caps each individual sink send so a stalled endpoint
// cannot delay other sinks on the same route. Histogram observations
// reflect this ceiling.
const perSinkTimeout = 15 * time.Second

// defaultSinkRate is the per-sink rate limit applied when no explicit
// rate is configured. Slack's published limit is ~1 msg/sec/channel and
// the other sinks have similar shapes — start conservative.
var defaultSinkRate = rate.Limit(1)
var defaultSinkBurst = 5

// Sink is the interface every alert delivery target implements.
type Sink interface {
	Name() string
	Send(ctx context.Context, a *alert.Alert) error
	Supports(sev alert.Severity) bool
}

// Registry tracks sinks by name plus a per-sink rate limiter.
type Registry struct {
	mu       sync.RWMutex
	sinks    map[string]Sink
	limiters map[string]*rate.Limiter
}

func NewRegistry() *Registry {
	return &Registry{
		sinks:    map[string]Sink{},
		limiters: map[string]*rate.Limiter{},
	}
}

func (r *Registry) Add(s Sink) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sinks[s.Name()] = s
	if _, ok := r.limiters[s.Name()]; !ok {
		r.limiters[s.Name()] = rate.NewLimiter(defaultSinkRate, defaultSinkBurst)
	}
}

// SetRate overrides the per-second rate and burst for a single sink. Use
// from main to honor user config.
func (r *Registry) SetRate(name string, limit rate.Limit, burst int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limiters[name] = rate.NewLimiter(limit, burst)
}

func (r *Registry) Get(name string) (Sink, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sinks[name]
	return s, ok
}

func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.sinks))
	for n := range r.sinks {
		out = append(out, n)
	}
	return out
}

// Dispatch fans an alert to the named sinks concurrently with a
// per-sink timeout, rate limiter, and panic safety.
func (r *Registry) Dispatch(ctx context.Context, a *alert.Alert, names []string) {
	var wg sync.WaitGroup
	for _, name := range names {
		r.mu.RLock()
		s, ok := r.sinks[name]
		limiter := r.limiters[name]
		r.mu.RUnlock()
		if !ok || !s.Supports(a.Severity) {
			continue
		}
		wg.Add(1)
		go func(name string, s Sink, limiter *rate.Limiter) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					metrics.SinkErrors.WithLabelValues(name).Inc()
					klog.Errorf("sink %q panic: %v\n%s", name, rec, debug.Stack())
				}
			}()
			sendCtx, cancel := context.WithTimeout(ctx, perSinkTimeout)
			defer cancel()

			if limiter != nil {
				if err := limiter.Wait(sendCtx); err != nil {
					metrics.AlertsSuppressed.WithLabelValues("ratelimited").Inc()
					klog.V(2).Infof("sink %q rate-limited: %v", name, err)
					return
				}
			}

			start := time.Now()
			err := s.Send(sendCtx, a)
			result := "ok"
			if err != nil {
				result = "error"
				metrics.SinkErrors.WithLabelValues(name).Inc()
				klog.Warningf("sink %q send failed: %v", name, err)
			}
			metrics.SinkSendDuration.WithLabelValues(name, result).Observe(time.Since(start).Seconds())
		}(name, s, limiter)
	}
	wg.Wait()
}
