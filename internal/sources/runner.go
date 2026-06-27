package sources

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// maxStartupJitter caps the random delay before a source's first poll. Spreading
// the first poll keeps many sources (regions x services) from hitting the
// provider API in the same instant, which would otherwise risk throttling and
// a synchronized error spike. Bounded so startup still surfaces cloud alerts
// quickly.
const maxStartupJitter = 5 * time.Second

// Run polls every source on its own goroutine until ctx is cancelled, then
// blocks until all in-flight polls return. Add it to the controller
// WaitGroup so the shutdown sequence waits for a clean drain.
//
// Each source gets its own ticker so a slow provider API cannot delay the
// others. time.Ticker coalesces ticks (its channel buffers at most one), so a
// poll that overruns the interval simply skips the missed ticks instead of
// piling up a backlog of overlapping polls. The first poll runs after a small
// bounded jitter so many sources do not stampede the provider API at once.
func Run(ctx context.Context, interval time.Duration, emit Emit, srcs ...Source) {
	if len(srcs) == 0 || interval <= 0 {
		return
	}
	var wg sync.WaitGroup
	for _, s := range srcs {
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			runOne(ctx, interval, emit, s)
		}(s)
	}
	wg.Wait()
}

// runOne drives a single source: a jittered first poll, then one poll per tick
// until ctx is cancelled.
func runOne(ctx context.Context, interval time.Duration, emit Emit, s Source) {
	poll := func() {
		// A panic in one provider's poll (e.g. an unexpected nil deep in an
		// SDK response shape) must not take down the controller and silence
		// the Kubernetes watchers. Mirrors watchers.recoverHandler.
		defer func() {
			if r := recover(); r != nil {
				klog.Errorf("source %s panicked in Poll (recovered): %v", s.Name(), r)
			}
		}()
		s.Poll(ctx, emit)
	}
	// Stagger the first poll by a bounded random delay so concurrent sources do
	// not all call the provider API at the same instant. Cap at the interval so
	// a sub-jitter interval is not overshot.
	if jitter := startupJitter(interval); jitter > 0 {
		t := time.NewTimer(jitter)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
	if ctx.Err() == nil {
		poll()
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			poll()
		}
	}
}

// startupJitter returns a random delay in [0, min(interval, maxStartupJitter)).
func startupJitter(interval time.Duration) time.Duration {
	limit := maxStartupJitter
	if interval < limit {
		limit = interval
	}
	if limit <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(limit)))
}
