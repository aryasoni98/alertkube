package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/sinks"
)

// The regression D10 exists for: a replayed FIRING alert used to carry no
// onFail, so it was dead-lettered on its first delivery failure - while a fresh
// firing rolls dedupe back and retries on the next watch event. That made the
// outbox reduce an alert's durability, which is the opposite of its purpose.
func TestReplayedFiringRollsBackDedupeOnFailure(t *testing.T) {
	s := &testSink{name: "a", err: errors.New("down")}
	d := dispatcherWith(t, s, 2, 64)
	defer d.Shutdown(context.Background())

	var dead atomic.Int32
	d.SetDeadLetter(func(*alert.Alert) { dead.Add(1) })

	rolledBack := make(chan string, 1)
	a := alert.New(alert.KindPod, "ns", "p", "Boom", alert.SeverityCritical)
	if n := d.ReplayPending(
		[]alert.PendingDelivery{{ID: 1, Alert: a, Route: []string{"a"}}},
		nil,
		func(fp string) { rolledBack <- fp },
	); n != 1 {
		t.Fatalf("replayed %d, want 1", n)
	}

	select {
	case fp := <-rolledBack:
		if fp != a.Fingerprint {
			t.Fatalf("rolled back %q, want the alert's fingerprint %q", fp, a.Fingerprint)
		}
	case <-timeoutAfter():
		t.Fatal("a failed replayed firing did not roll back dedupe; it will stay muted for the whole mute window")
	}
	// Drain the workers first so a late dead-letter cannot race the check.
	d.Shutdown(context.Background())
	if n := dead.Load(); n != 0 {
		t.Fatalf("a failed replayed firing was dead-lettered %d times; it must only roll back dedupe", n)
	}
}

// A replayed RESOLVE must keep the bounded resolve-retry path, not the dedupe
// rollback: a resolve has no dedupe entry, and losing it dangles an incident.
func TestReplayedResolveKeepsRetryPathNotRollback(t *testing.T) {
	withRetryDelay(t, time.Millisecond)

	s := &testSink{name: "a", err: errors.New("down")}
	d := dispatcherWith(t, s, 2, 64)
	defer d.Shutdown(context.Background())

	rolledBack := make(chan string, 1)
	res := alert.New(alert.KindPod, "ns", "p", "Boom", alert.SeverityCritical)
	res.Resolved = true
	d.ReplayPending(
		[]alert.PendingDelivery{{ID: 1, Alert: res, Route: []string{"a"}}},
		nil,
		func(fp string) { rolledBack <- fp },
	)

	// It must retry (>1 send attempt) and never invoke the dedupe rollback.
	waitFor(t, 2, s.sends.Load)
	select {
	case fp := <-rolledBack:
		t.Fatalf("a resolve must not roll back dedupe (rolled back %q); it has no dedupe entry and needs the retry path", fp)
	default:
	}
}

func timeoutAfter() <-chan time.Time { return time.After(2 * time.Second) }

func TestReplayedFireOnceDoesNotRollback(t *testing.T) {
	for _, event := range []bool{true, false} {
		d := newDispatcher(sinks.NewRegistry(), 1, 8)
		a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityCritical)
		a.Event = event
		if !event {
			a.Labels["alertkube-grouped"] = "true"
		}
		d.ReplayPending([]alert.PendingDelivery{{ID: 1, Alert: a, Route: []string{"a"}}}, nil, func(string) {})
		if job := <-d.queues[0]; job.onFail != nil {
			t.Errorf("fire-once replay (event=%v) gained a dedupe rollback instead of dead-lettering", event)
		}
	}
}
