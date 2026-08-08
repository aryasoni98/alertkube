// Package trace provides opt-in OpenTelemetry tracing for the alert pipeline.
//
// The question this exists to answer is the one operators actually ask: "I know
// the pod crashed - why didn't I get paged?" Today answering it means
// correlating six metrics by hand (was it suppressed? which reason? did routing
// match? did the sink 500? is a breaker open?). A trace spanning
// classify -> dedupe -> suppress -> route -> group -> enqueue -> dispatch ->
// sink HTTP answers it in one view, with the terminal span naming the stage
// that dropped the alert.
//
// Disabled by default. A controller whose job is to page you must not acquire a
// hard dependency on a collector being reachable, so when tracing is off every
// function here is a no-op backed by OpenTelemetry's own no-op tracer - no nil
// checks at the call sites, no allocation, no branch beyond the one the SDK
// already does.
package trace

import (
	"context"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
	"k8s.io/klog/v2"
)

// tracerName identifies this instrumentation scope in the exported spans.
const tracerName = "github.com/aryasoni98/alertkube"

// initTimeout bounds exporter construction so an unreachable collector cannot
// stall controller startup. Tracing is an observability nicety; alerting is the
// job.
const initTimeout = 5 * time.Second

// Enabled reports whether tracing was turned on. Callers do not need to check
// it - the no-op tracer handles that - but it is useful for skipping expensive
// attribute construction.
func Enabled() bool { return enabled }

var enabled bool

// Tracer returns the tracer for the alert pipeline. Safe before Init: the
// global provider defaults to a no-op, so an uninitialised call records
// nothing rather than panicking.
func Tracer() oteltrace.Tracer { return otel.Tracer(tracerName) }

// Init wires the OTLP exporter when ALERTKUBE_TRACING_ENABLED is true and
// returns a shutdown function to flush pending spans. When tracing is off (the
// default) it returns a no-op shutdown and leaves the global provider alone.
//
// Endpoint and headers come from the standard OTEL_EXPORTER_OTLP_* environment
// variables rather than bespoke config keys, so this behaves like every other
// OTLP producer an operator already runs.
//
// An exporter that cannot be built is logged and tracing stays off; it is never
// fatal. Losing traces must not stop the controller from alerting.
func Init(ctx context.Context, serviceVersion string) func(context.Context) error {
	noop := func(context.Context) error { return nil }
	if !strings.EqualFold(os.Getenv("ALERTKUBE_TRACING_ENABLED"), "true") {
		return noop
	}

	initCtx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	exp, err := otlptracehttp.New(initCtx)
	if err != nil {
		klog.Warningf("tracing enabled but the OTLP exporter could not be built (continuing without tracing): %v", err)
		return noop
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(newResource(serviceVersion)),
		// Parent-based sampling with an always-on root: an alert's trace starts
		// here, and a half-sampled alert pipeline is worse than none - the
		// missing span is indistinguishable from a dropped alert, which is
		// exactly the confusion this is meant to remove. Operators who need
		// less volume set OTEL_TRACES_SAMPLER.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)
	otel.SetTracerProvider(tp)
	// W3C trace context so a trace started by an upstream Alertmanager POST
	// continues into this pipeline rather than starting a disconnected one.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	enabled = true
	klog.Infof("tracing enabled: exporting spans via OTLP (configure with OTEL_EXPORTER_OTLP_ENDPOINT)")
	return tp.Shutdown
}

// Detach strips cancellation from ctx while keeping its span linkage.
//
// This is what lets a delivery enqueued by an informer handler stay attached to
// the trace that produced it. The handler's context is cancelled as soon as it
// returns, long before a worker performs the HTTP send, so carrying the context
// itself would either cancel the send or force the queue to hold a live
// cancellation tree. Carrying only the span context keeps the parent link and
// nothing else.
func Detach(ctx context.Context) context.Context {
	return oteltrace.ContextWithSpanContext(context.Background(), oteltrace.SpanContextFromContext(ctx))
}

// AlertAttrs is the attribute set every pipeline span carries, so a trace can
// be found by the alert identity an operator already knows.
func AlertAttrs(kind, namespace, name, reason, fingerprint string) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("alertkube.kind", kind),
		attribute.String("alertkube.namespace", namespace),
		attribute.String("alertkube.name", name),
		attribute.String("alertkube.reason", reason),
		attribute.String("alertkube.fingerprint", fingerprint),
	}
}

// Dropped marks the active span as the point the alert stopped, naming the
// stage and why. This is the single most useful thing in the trace: the last
// span of a trace that never reached a sink says exactly which gate closed.
func Dropped(span oteltrace.Span, stage, reason string) {
	span.SetAttributes(
		attribute.String("alertkube.dropped_at", stage),
		attribute.String("alertkube.drop_reason", reason),
	)
}
