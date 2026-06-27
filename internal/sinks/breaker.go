package sinks

import (
	"sync"
	"time"
)

// Circuit-breaker defaults. A sink that fails consecutiveFailures times in a row
// is considered down; the breaker opens and short-circuits its sends for
// openCooldown so a persistently-broken endpoint stops burning the per-sink
// timeout budget (and log noise) on every alert. After the cooldown one trial
// send is allowed (half-open); success closes the breaker, another failure
// re-opens it. These are conservative defaults sized to recover quickly while
// still cutting a sustained outage's wasted work.
var (
	breakerThreshold = 5
	breakerCooldown  = 30 * time.Second
)

// breakerState is the closed/open/half-open tri-state of one sink's breaker.
type breakerState int

const (
	breakerClosed   breakerState = iota // healthy: sends proceed
	breakerOpen                         // tripped: sends short-circuit until cooldown elapses
	breakerHalfOpen                     // probing: a single trial send is allowed
)

// breaker is a per-sink circuit breaker. It is safe for concurrent use: the
// Dispatch fan-out calls Allow before a send and Record after it, possibly from
// several goroutines for the same sink under a storm.
type breaker struct {
	mu        sync.Mutex
	failures  int
	state     breakerState
	openUntil time.Time
	now       func() time.Time // injectable clock for tests
}

func newBreaker() *breaker { return &breaker{now: time.Now} }

// Allow reports whether a send may proceed. When the breaker is open it stays
// closed-to-traffic until the cooldown elapses, then admits exactly one trial
// (half-open) so a recovered endpoint can re-close the breaker without releasing
// a storm at it.
func (b *breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerOpen:
		if b.now().Before(b.openUntil) {
			return false
		}
		// Cooldown elapsed: allow a single probe.
		b.state = breakerHalfOpen
		return true
	case breakerHalfOpen:
		// A probe is already in flight; hold other sends back until it resolves.
		return false
	default:
		return true
	}
}

// Record updates the breaker with the outcome of a send. A success closes it and
// clears the failure run; a failure increments the run and trips the breaker
// once it reaches the threshold (or immediately re-opens a half-open probe).
func (b *breaker) Record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		b.failures = 0
		b.state = breakerClosed
		return
	}
	b.failures++
	if b.state == breakerHalfOpen || b.failures >= breakerThreshold {
		b.state = breakerOpen
		b.openUntil = b.now().Add(breakerCooldown)
	}
}

// Open reports whether the breaker is currently short-circuiting sends. Used to
// drive the per-sink open-gauge metric.
func (b *breaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state == breakerOpen && b.now().Before(b.openUntil)
}
