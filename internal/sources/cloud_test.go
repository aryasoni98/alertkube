package sources

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"alertkube/internal/alert"
	"alertkube/internal/metrics"
)

func TestEmitFiring(t *testing.T) {
	var got *alert.Alert
	EmitFiring(func(a *alert.Alert) { got = a },
		alert.KindEC2Instance, "us-east-1", "i-123", "EC2StatusCheckFailed",
		"instance status check failed", alert.SeverityCritical,
		map[string]string{"provider": "aws", "region": "us-east-1"},
		map[string]string{"state": "impaired", "empty": ""},
	)
	if got == nil {
		t.Fatal("EmitFiring did not emit")
	}
	if got.Kind != alert.KindEC2Instance || got.Namespace != "us-east-1" || got.Name != "i-123" {
		t.Fatalf("identity wrong: %+v", got)
	}
	if got.Reason != "EC2StatusCheckFailed" || got.Severity != alert.SeverityCritical {
		t.Fatalf("reason/severity wrong: %+v", got)
	}
	if got.Labels["provider"] != "aws" || got.Labels["region"] != "us-east-1" {
		t.Fatalf("labels not attached: %v", got.Labels)
	}
	if got.Details["state"] != "impaired" {
		t.Fatalf("detail missing: %v", got.Details)
	}
	if _, ok := got.Details["empty"]; ok {
		t.Fatalf("empty detail value must be dropped: %v", got.Details)
	}
	if got.Resolved {
		t.Fatal("firing alert must not be Resolved")
	}
}

func TestEmitResolve(t *testing.T) {
	var got *alert.Alert
	EmitResolve(func(a *alert.Alert) { got = a }, alert.KindRDSInstance, "eu-west-1", "db-1")
	if got == nil || !got.Resolved {
		t.Fatalf("EmitResolve must emit a Resolved alert, got %+v", got)
	}
	if got.Kind != alert.KindRDSInstance || got.Namespace != "eu-west-1" || got.Name != "db-1" {
		t.Fatalf("resolve identity wrong: %+v", got)
	}
}

func TestPollErr(t *testing.T) {
	const src = "sources-test-pollerr"
	before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(src))
	PollErr(src, "scope-1", errTest{})
	if after := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(src)); after != before+1 {
		t.Fatalf("CloudPollErrors not incremented: before=%v after=%v", before, after)
	}
}

type errTest struct{}

func (errTest) Error() string { return "boom" }

func TestStrVal(t *testing.T) {
	if StrVal(nil) != "" {
		t.Error("nil should be empty string")
	}
	s := "x"
	if StrVal(&s) != "x" {
		t.Error("deref wrong")
	}
}
