package trace

import (
	"context"
	"testing"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// Tracing is off by default and must stay a no-op that costs nothing and
// panics on nothing. A controller whose job is to page must not acquire a hard
// dependency on a collector being reachable.
func TestDisabledByDefault(t *testing.T) {
	t.Setenv("ALERTKUBE_TRACING_ENABLED", "")
	shutdown := Init(context.Background(), "test")
	if Enabled() {
		t.Fatal("tracing must be off unless explicitly enabled")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown returned %v", err)
	}
	// The global no-op tracer must still be usable, so call sites need no guard.
	_, span := Tracer().Start(context.Background(), "noop")
	span.End()
}

// The key propagation property: a job queued by an informer handler must keep
// its trace linkage after the handler's context is cancelled, or every delivery
// span would be orphaned (or the send cancelled outright).
func TestDetachKeepsSpanLinkageAndDropsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx, span := Tracer().Start(ctx, "producer")
	defer span.End()

	detached := Detach(ctx)
	cancel() // the producer goroutine returns

	if err := detached.Err(); err != nil {
		t.Fatalf("detached context inherited cancellation: %v; queued deliveries would be cancelled before they are sent", err)
	}
	want := oteltrace.SpanContextFromContext(ctx)
	if got := oteltrace.SpanContextFromContext(detached); got.TraceID() != want.TraceID() {
		t.Fatal("detached context lost its trace linkage; delivery spans would not join the producing trace")
	}
}

// Alert attributes must carry the identity an operator already has in hand,
// otherwise a trace cannot be found from a page.
func TestAlertAttrsCarryIdentity(t *testing.T) {
	attrs := AlertAttrs("Pod", "ns", "web-1", "CrashLoopBackOff", "abc123")
	got := map[string]string{}
	for _, a := range attrs {
		got[string(a.Key)] = a.Value.AsString()
	}
	for k, want := range map[string]string{
		"alertkube.kind":        "Pod",
		"alertkube.namespace":   "ns",
		"alertkube.name":        "web-1",
		"alertkube.reason":      "CrashLoopBackOff",
		"alertkube.fingerprint": "abc123",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}
