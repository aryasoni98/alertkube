package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sinks"
)

// testSink is the one fake Sink for app tests. It records a copy of every
// alert it is sent, counts sends, and returns fn's result when fn is set, else
// err. It is safe for the concurrent fan-out in Registry.Dispatch.
type testSink struct {
	name  string
	err   error
	fn    func(*alert.Alert) error
	sends atomic.Int32
	mu    sync.Mutex
	got   []*alert.Alert
}

func (s *testSink) Name() string                 { return s.name }
func (s *testSink) Supports(alert.Severity) bool { return true }
func (s *testSink) Send(_ context.Context, a *alert.Alert) error {
	s.sends.Add(1)
	cp := *a
	s.mu.Lock()
	s.got = append(s.got, &cp)
	s.mu.Unlock()
	if s.fn != nil {
		return s.fn(a)
	}
	return s.err
}

func (s *testSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

// derived counts the derived-rule fires and resolves the sink received.
func (s *testSink) derived() (fires, resolves int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.got {
		switch {
		case a.Kind != alert.KindDerived:
		case a.Resolved:
			resolves++
		default:
			fires++
		}
	}
	return fires, resolves
}

// syncEnqueue delivers inline so tests can assert delivery synchronously;
// production uses the async dispatch worker pool with the same signature.
func syncEnqueue(reg *sinks.Registry) enqueueFunc {
	return func(a *alert.Alert, route []string, onFail func()) {
		if !dispatch(reg, a, route) && onFail != nil {
			onFail()
		}
	}
}

// withRetryDelay shortens resolveRetryDelay for the rest of the test.
func withRetryDelay(t *testing.T, d time.Duration) {
	t.Helper()
	old := resolveRetryDelay
	resolveRetryDelay = d
	t.Cleanup(func() { resolveRetryDelay = old })
}

// dispatcherWith starts a dispatcher delivering to s alone. The caller owns
// Shutdown, since several tests call it mid-test to assert on the drain.
func dispatcherWith(t *testing.T, s sinks.Sink, workers, queue int) *dispatcher {
	t.Helper()
	reg := sinks.NewRegistry()
	reg.Add(s)
	reg.SetRate(s.Name(), rate.Limit(100000), 100000) // never wait on the limiter
	d := newDispatcher(reg, workers, queue)
	d.Start()
	return d
}
