package sinks

import (
	"context"
	"runtime/debug"
	"sync"
	"sync/atomic"
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

// Dispatch fans an alert to the named sinks concurrently with a
// per-sink timeout, rate limiter, and panic safety. Resolved alerts skip
// the Supports severity gate so a resolve always follows its trigger
// (PagerDuty drops resolves for unknown dedup keys, so extras are harmless).
// Returns false only when at least one sink was attempted and every
// attempt failed — callers use that to roll back dedupe state so the next
// firing retries delivery.
func (r *Registry) Dispatch(ctx context.Context, a *alert.Alert, names []string) bool {
	var (
		wg        sync.WaitGroup
		succeeded atomic.Int32
		attempted int
	)
	for _, name := range names {
		r.mu.RLock()
		s, ok := r.sinks[name]
		limiter := r.limiters[name]
		r.mu.RUnlock()
		if !ok || (!a.Resolved && !s.Supports(a.Severity)) {
			continue
		}
		attempted++
		wg.Add(1)
		go func(name string, s Sink, limiter *rate.Limiter) {
			defer wg.Done()
			metrics.DispatchInflight.WithLabelValues(name).Inc()
			defer metrics.DispatchInflight.WithLabelValues(name).Dec()
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
					// A drop here means the alert never reaches this sink —
					// surface which one, loudly, instead of a V(2) whisper.
					metrics.AlertsSuppressed.WithLabelValues("ratelimited").Inc()
					klog.Warningf("sink %q dropped %s: rate limit not acquired within %s", name, a, perSinkTimeout)
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
			} else {
				succeeded.Add(1)
			}
			metrics.SinkSendDuration.WithLabelValues(name, result).Observe(time.Since(start).Seconds())
		}(name, s, limiter)
	}
	wg.Wait()
	return attempted == 0 || succeeded.Load() > 0
}
