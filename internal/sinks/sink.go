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

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/metrics"
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
	breakers map[string]*breaker
}

func NewRegistry() *Registry {
	return &Registry{
		sinks:    map[string]Sink{},
		limiters: map[string]*rate.Limiter{},
		breakers: map[string]*breaker{},
	}
}

func (r *Registry) Add(s Sink) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sinks[s.Name()] = s
	if _, ok := r.limiters[s.Name()]; !ok {
		r.limiters[s.Name()] = rate.NewLimiter(defaultSinkRate, defaultSinkBurst)
	}
	if _, ok := r.breakers[s.Name()]; !ok {
		r.breakers[s.Name()] = newBreaker()
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
//
// Return contract (callers roll back dedupe state on false so the next
// firing retries delivery):
//   - at least one sink attempted: true iff at least one attempt succeeded.
//   - nothing attempted because every routed sink was short-circuited by an
//     OPEN circuit breaker (firing alert): false. Reporting success here
//     would mute the alert for the whole mute window while it reached no
//     sink at all - a silent loss, the worst failure mode for an alerting
//     system. False makes the caller retry once the breaker recovers.
//   - nothing attempted because the route was genuinely empty (no matching
//     sinks, or none supported the severity): true. There is nothing to
//     deliver, and retrying forever would churn.
func (r *Registry) Dispatch(ctx context.Context, a *alert.Alert, names []string) bool {
	var (
		wg        sync.WaitGroup
		succeeded atomic.Int32
		attempted int
		// breakerSkipped counts sinks skipped solely because their breaker
		// was open. Incremented on the caller goroutine (before fan-out), so
		// a plain int is safe. It disambiguates "nothing to deliver" from
		// "everything short-circuited" in the return value below.
		breakerSkipped int
	)
	for _, name := range names {
		r.mu.RLock()
		s, ok := r.sinks[name]
		limiter := r.limiters[name]
		brk := r.breakers[name]
		r.mu.RUnlock()
		if !ok || (!a.Resolved && !s.Supports(a.Severity)) {
			continue
		}
		// Circuit breaker: when a sink has failed repeatedly its breaker opens
		// and short-circuits sends until a cooldown elapses, so a dead endpoint
		// stops burning the per-sink timeout on every alert. Resolves bypass the
		// breaker: a resolve must always be attempted so a recovering incident
		// sink can close its incident even while the breaker is open.
		if brk != nil && !a.Resolved && !brk.Allow() {
			breakerSkipped++
			metrics.AlertsSuppressed.WithLabelValues("circuit_open").Inc()
			metrics.SinkBreakerOpen.WithLabelValues(name).Set(1)
			klog.Warningf("sink %q circuit open: skipping %s (endpoint failing)", name, a)
			continue
		}
		attempted++
		wg.Add(1)
		go func(name string, s Sink, limiter *rate.Limiter, brk *breaker) {
			defer wg.Done()
			metrics.DispatchInflight.WithLabelValues(name).Inc()
			defer metrics.DispatchInflight.WithLabelValues(name).Dec()
			defer func() {
				if rec := recover(); rec != nil {
					metrics.SinkErrors.WithLabelValues(name).Inc()
					if brk != nil {
						brk.Record(false)
						setBreakerGauge(name, brk)
					}
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
					// Allow() may have consumed a half-open probe; record a
					// failure so the breaker is never stranded half-open (which
					// would block every later send) when we bail before sending.
					if brk != nil {
						brk.Record(false)
						setBreakerGauge(name, brk)
					}
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
			// Feed the breaker. A resolve still records its outcome so a sink
			// that recovers via a resolve probe re-closes its breaker.
			took := time.Since(start)
			if brk != nil {
				brk.Record(err == nil)
				// A sink can also fail by being unusably slow while still
				// answering 200; that never trips the failure counter but does
				// tie up a dispatch worker for the whole call.
				brk.RecordLatency(took)
				setBreakerGauge(name, brk)
			}
			metrics.SinkSendDuration.WithLabelValues(name, result).Observe(took.Seconds())
		}(name, s, limiter, brk)
	}
	wg.Wait()
	if attempted > 0 {
		return succeeded.Load() > 0
	}
	// Nothing was attempted. If sinks were skipped only because their
	// breakers are open, report failure so the caller retries the firing
	// once the endpoint recovers instead of muting an undelivered alert. A
	// genuinely empty route (breakerSkipped == 0) returns true.
	return breakerSkipped == 0
}

// setBreakerGauge mirrors a breaker's open/closed state onto the metric.
func setBreakerGauge(name string, b *breaker) {
	v := 0.0
	if b.Open() {
		v = 1
	}
	metrics.SinkBreakerOpen.WithLabelValues(name).Set(v)
}
