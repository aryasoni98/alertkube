package trace

import (
	"os"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// newResource describes this controller instance on every exported span.
//
// The pod name and namespace matter more here than in a typical service: with
// leader election only one replica is producing spans at a time, and with
// sharding several are producing disjoint ones. Without these attributes a
// trace cannot be attributed to the replica that handled it, which is the first
// question during a failover or rebalance investigation.
func newResource(serviceVersion string) *resource.Resource {
	attrs := []attribute.KeyValue{
		semconv.ServiceName("alertkube"),
		semconv.ServiceVersion(serviceVersion),
	}
	if pod := os.Getenv("POD_NAME"); pod != "" {
		attrs = append(attrs, semconv.K8SPodName(pod))
	}
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		attrs = append(attrs, semconv.K8SNamespaceName(ns))
	}
	if idx := os.Getenv("ALERTKUBE_SHARD_INDEX"); idx != "" {
		attrs = append(attrs, attribute.String("alertkube.shard_index", idx))
	}
	// resource.Merge with the default resource keeps the SDK's own telemetry
	// attributes (sdk name/version/language) alongside ours.
	r, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL, attrs...))
	if err != nil {
		// A merge conflict means a schema-URL mismatch; ours is still usable.
		return resource.NewWithAttributes(semconv.SchemaURL, attrs...)
	}
	return r
}
