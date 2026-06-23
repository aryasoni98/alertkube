package sources

import (
	"context"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// Run polls every source on its own goroutine until ctx is cancelled, then
// blocks until all in-flight polls return. Add it to the controller
// WaitGroup so the shutdown sequence waits for a clean drain.
//
// Each source gets its own ticker so a slow provider API cannot delay the
// others. time.Ticker coalesces ticks (its channel buffers at most one), so a
// poll that overruns the interval simply skips the missed ticks instead of
// piling up a backlog of overlapping polls. The first poll runs immediately so
// startup does not wait a full interval for the first cloud alert.
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

// runOne drives a single source: an immediate poll, then one poll per tick
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
