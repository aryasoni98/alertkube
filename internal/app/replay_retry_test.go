package app

import (
	"errors"
	"testing"
	"time"

	"github.com/aryasoni98/alertkube/internal/alert"
)

// The regression D10 exists for: a replayed FIRING alert used to carry no
// onFail, so it was dead-lettered on its first delivery failure - while a fresh
// firing rolls dedupe back and retries on the next watch event. That made the
// outbox reduce an alert's durability, which is the opposite of its purpose.
func TestReplayedFiringRollsBackDedupeOnFailure(t *testing.T) {
	s := &dispatchStub{name: "a", err: errors.New("down")}
	d, _ := dispatcherWith(t, s, 2, 64)
	defer d.Shutdown()

	var dead int32
	d.SetDeadLetter(func(*alert.Alert) { dead++ })

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
}

// A replayed RESOLVE must keep the bounded resolve-retry path, not the dedupe
// rollback: a resolve has no dedupe entry, and losing it dangles an incident.
func TestReplayedResolveKeepsRetryPathNotRollback(t *testing.T) {
	old := resolveRetryDelay
	resolveRetryDelay = time.Millisecond
	t.Cleanup(func() { resolveRetryDelay = old })

	s := &dispatchStub{name: "a", err: errors.New("down")}
	d, _ := dispatcherWith(t, s, 2, 64)
	defer d.Shutdown()

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
