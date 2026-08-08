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
	// breakerSlowThreshold is the latency above which a *successful* send still
	// counts against the sink. A failure-only breaker has a blind spot: an
	// endpoint that accepts every request but takes 20s to answer never trips,
	// yet it occupies a dispatch worker for the whole time and pushes
	// backpressure onto the informer handlers. Slow is a different failure from
	// broken, and it needs the same short-circuit.
	breakerSlowThreshold = 5 * time.Second
	// breakerSlowRun is how many consecutive slow-but-successful sends trip the
	// breaker. Kept equal to breakerThreshold so slow and broken are treated
	// with the same patience; a single slow send (a cold connection, one GC
	// pause at the far end) must not short-circuit a healthy sink.
	breakerSlowRun = 5
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
	slow      int // consecutive successful-but-slow sends; see RecordLatency
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

// RecordLatency feeds a completed send's duration to the slow-sink detector.
// Call it alongside Record, for successes as well as failures.
//
// It is separate from Record because slow and broken are independent signals
// and Record's failure semantics (praised, well-tested, and load-bearing for
// resolve retries) should not change to accommodate this. A fast send clears
// the slow run; breakerSlowRun consecutive slow ones open the breaker exactly
// as a failure run would, so a sink that is merely unusably slow stops
// occupying dispatch workers.
func (b *breaker) RecordLatency(took time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if took < breakerSlowThreshold {
		b.slow = 0
		return
	}
	b.slow++
	if b.slow >= breakerSlowRun {
		b.state = breakerOpen
		b.openUntil = b.now().Add(breakerCooldown)
		// Reset the run so the next cooldown is measured from fresh evidence
		// rather than re-opening on the first slow probe forever.
		b.slow = 0
	}
}

// Open reports whether the breaker is currently short-circuiting sends. Used to
// drive the per-sink open-gauge metric.
func (b *breaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state == breakerOpen && b.now().Before(b.openUntil)
}
