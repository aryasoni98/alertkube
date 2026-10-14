package sources

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/metrics"
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

func TestPollTruncated(t *testing.T) {
	const src = "sources-test-truncated"
	before := testutil.ToFloat64(metrics.CloudPollTruncated.WithLabelValues(src))
	PollTruncated(src, "scope-1", "page token did not advance")
	PollTruncated(src, "", "hit the page guard")
	if after := testutil.ToFloat64(metrics.CloudPollTruncated.WithLabelValues(src)); after != before+2 {
		t.Fatalf("CloudPollTruncated delta = %v, want 2", after-before)
	}
}

func TestPollErrIgnoresCancel(t *testing.T) {
	before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues("test-source"))
	PollErr("test-source", "scope", context.Canceled)
	PollErr("test-source", "scope", nil)
	after := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues("test-source"))
	if after != before {
		t.Fatalf("canceled poll counted as an error: before=%v after=%v", before, after)
	}
	PollErr("test-source", "scope", errors.New("throttled"))
	if got := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues("test-source")); got != before+1 {
		t.Fatalf("real poll error = %v, want %v", got, before+1)
	}
}

func TestCompactDropsNilSources(t *testing.T) {
	a := funcSource{name: "a", poll: func(context.Context, Emit) {}}
	b := funcSource{name: "b", poll: func(context.Context, Emit) {}}
	got := Compact([]Source{nil, a, nil, b, nil})
	if len(got) != 2 || got[0].Name() != "a" || got[1].Name() != "b" {
		t.Fatalf("Compact = %v, want the two non-nil sources in order", got)
	}
	if len(Compact(nil)) != 0 {
		t.Fatal("Compact(nil) must be empty, not nil-panicking")
	}
}

func TestScope(t *testing.T) {
	cases := []struct {
		parent, location, want string
	}{
		{"sub-1", "eastus", "sub-1/eastus"},
		{"proj-1", "us-central1-a", "proj-1/us-central1-a"},
		{"proj-1", "", "proj-1"}, // unknown location: no trailing separator
	}
	for _, tc := range cases {
		if got := Scope(tc.parent, tc.location); got != tc.want {
			t.Errorf("Scope(%q, %q) = %q, want %q", tc.parent, tc.location, got, tc.want)
		}
	}
}
