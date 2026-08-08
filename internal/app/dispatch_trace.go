package app

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/trace"
)

// Tracing for the delivery half of the pipeline.
//
// The producing side (classify -> dedupe -> suppress -> route) runs on the
// informer goroutine and ends at enqueue; delivery happens later, on a worker.
// Those are two spans in one trace, joined by the span context the job carries
// (see dispatchJob.traceCtx). Without that join, the most common question -
// "the alert was produced, so where did it go?" - has its answer split across
// two unrelated traces.

// enqueueSpan opens the span covering an alert's admission to the queue, and
// returns the detached context to store on the job so the eventual delivery
// span can parent to it. The returned span is ended by the caller once the job
// is queued: enqueue is near-instant by design, and a span that stayed open
// until delivery would hide queue latency inside it rather than showing the
// wait as the gap between the two spans.
func enqueueSpan(ctx context.Context, a *alert.Alert, route []string) (context.Context, oteltrace.Span) {
	ctx, span := trace.Tracer().Start(ctx, "alertkube.enqueue")
	span.SetAttributes(trace.AlertAttrs(string(a.Kind), a.Namespace, a.Name, a.Reason, a.Fingerprint)...)
	span.SetAttributes(
		attribute.StringSlice("alertkube.route", route),
		attribute.Bool("alertkube.resolved", a.Resolved),
		attribute.String("alertkube.severity", string(a.Severity)),
	)
	return ctx, span
}

// startDeliverySpan opens the span covering one delivery attempt, parented to
// the enqueue span via the job's carried linkage. A job with no linkage (a
// replayed outbox record, whose producing trace ended in a previous process)
// starts a fresh root - correct, since there is nothing to attach to.
func startDeliverySpan(job dispatchJob) oteltrace.Span {
	parent := job.traceCtx
	if parent == nil {
		parent = context.Background()
	}
	_, span := trace.Tracer().Start(parent, "alertkube.dispatch")
	span.SetAttributes(trace.AlertAttrs(
		string(job.a.Kind), job.a.Namespace, job.a.Name, job.a.Reason, job.a.Fingerprint)...)
	span.SetAttributes(
		attribute.StringSlice("alertkube.route", job.route),
		attribute.Bool("alertkube.resolved", job.a.Resolved),
		attribute.Int("alertkube.retries", job.retries),
	)
	return span
}

// endDeliverySpan closes a delivery span, marking it an error when no sink on
// the route accepted the alert. That error status is the signal that makes the
// trace worth having: a red terminal span means the alert reached nobody.
func endDeliverySpan(span oteltrace.Span, job dispatchJob, delivered bool) {
	if delivered {
		span.SetStatus(codes.Ok, "")
	} else {
		span.SetStatus(codes.Error, "every routed sink failed")
		trace.Dropped(span, "dispatch", "all sinks failed")
	}
	span.End()
}
