package sources

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"alertkube/internal/alert"
)

// funcSource adapts a function into a Source for tests.
type funcSource struct {
	name string
	poll func(context.Context, Emit)
}

func (f funcSource) Name() string                        { return f.name }
func (f funcSource) Poll(ctx context.Context, emit Emit) { f.poll(ctx, emit) }

func discard(*alert.Alert) {}

func TestRunPollsImmediatelyThenOnTick(t *testing.T) {
	var count atomic.Int32
	s := funcSource{name: "tick", poll: func(context.Context, Emit) { count.Add(1) }}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, 15*time.Millisecond, discard, s)
		close(done)
	}()

	// ~immediate poll + several ticks over 90ms.
	time.Sleep(90 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
	if got := count.Load(); got < 2 {
		t.Fatalf("expected at least the immediate poll plus one tick, got %d", got)
	}
}

func TestRunRecoversPanic(t *testing.T) {
	var polls atomic.Int32
	boom := funcSource{name: "boom", poll: func(context.Context, Emit) {
		polls.Add(1)
		panic("provider blew up")
	}}
	// Run must return cleanly when ctx expires even though every poll panics.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	Run(ctx, 10*time.Millisecond, discard, boom)
	if polls.Load() == 0 {
		t.Fatal("expected the panicking source to be polled at least once")
	}
}

func TestRunNoSourcesReturns(t *testing.T) {
	// No sources and a non-positive interval are both no-ops, not hangs.
	Run(context.Background(), time.Second, discard)
	Run(context.Background(), 0, discard, funcSource{name: "x", poll: func(context.Context, Emit) {}})
}

func TestRunStopsAllSourcesOnCancel(t *testing.T) {
	var a, b atomic.Int32
	sa := funcSource{name: "a", poll: func(context.Context, Emit) { a.Add(1) }}
	sb := funcSource{name: "b", poll: func(context.Context, Emit) { b.Add(1) }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Run(ctx, 15*time.Millisecond, discard, sa, sb); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not drain both sources on cancel")
	}
	if a.Load() == 0 || b.Load() == 0 {
		t.Fatalf("both sources should have polled: a=%d b=%d", a.Load(), b.Load())
	}
}
