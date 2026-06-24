package sinks

import (
	"context"
	"fmt"
	"runtime/debug"
	"sort"
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
// reflect this ceiling. It is the hard ceiling on all retries for one sink;
// see the delivery-path timeout budget at dispatchTimeout (controller.go)
// for how it nests inside dispatchTimeout and around DefaultRetry.
const perSinkTimeout = 15 * time.Second

// defaultSinkRate is the per-sink rate limit applied when no explicit
// rate is configured. Slack's published limit is ~1 msg/sec/channel and
// the other sinks have similar shapes - start conservative.
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

// Has reports whether a sink is registered under name.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.sinks[name]
	return ok
}

// Names returns the registered sink names, sorted. The console lists these so an
// operator can pick a channel to test-fire.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.sinks))
	for n := range r.sinks {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestSend delivers one alert to a single named sink synchronously and returns
// the real send error, bypassing the Dispatch fan-out, severity gate, and rate
// limiter so a one-off test reports the actual outcome instead of queueing
// behind a storm. Panics in a sink are converted to an error. The console's
// channel test-fire uses this; it reuses the sink's already-loaded credentials,
// so no Secret read is involved.
func (r *Registry) TestSend(ctx context.Context, name string, a *alert.Alert) (err error) {
	r.mu.RLock()
	s, ok := r.sinks[name]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown sink %q", name)
	}
	defer func() {
		if rec := recover(); rec != nil {
			metrics.SinkErrors.WithLabelValues(name).Inc()
			err = fmt.Errorf("sink %q panicked: %v", name, rec)
		}
	}()
	sendCtx, cancel := context.WithTimeout(ctx, perSinkTimeout)
	defer cancel()
	metrics.DispatchInflight.WithLabelValues(name).Inc()
	defer metrics.DispatchInflight.WithLabelValues(name).Dec()
	start := time.Now()
	err = s.Send(sendCtx, a)
	result := "ok"
	if err != nil {
		result = "error"
		metrics.SinkErrors.WithLabelValues(name).Inc()
	}
	metrics.SinkSendDuration.WithLabelValues(name, result).Observe(time.Since(start).Seconds())
	return err
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
// attempt failed - callers use that to roll back dedupe state so the next
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
					// A drop here means the alert never reaches this sink -
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
