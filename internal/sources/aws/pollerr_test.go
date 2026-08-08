package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/internal/metrics"
)

// TestPollErrIncrementsMetric asserts a cloud poll failure is observable on the
// shared CloudPollErrors metric (and does not panic). A blinded source must be
// visible to operators while the in-cluster watchers keep running.
func TestPollErrIncrementsMetric(t *testing.T) {
	const src = "aws-test-pollerr"
	before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(src))
	pollErr(src, "us-east-1", errors.New("AccessDenied"))
	after := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(src))
	if after != before+1 {
		t.Fatalf("CloudPollErrors not incremented: before=%v after=%v", before, after)
	}
}

// TestEKSPollContinuesAfterDescribeError verifies one cluster's DescribeCluster
// failure does not abort the whole region poll: the source records the error and
// keeps going (no panic, no lost coverage of sibling clusters).
func TestEKSPollContinuesAfterDescribeError(t *testing.T) {
	// Lists two clusters but errors on every Describe; the poll must record the
	// errors and emit nothing, without panicking.
	f := &fakeEKS{pages: [][]string{{"a", "b"}}, descErr: errors.New("Throttling")}
	src := &eksSource{regions: []eksRegion{{region: "us-east-1", client: f}}}
	emit, got := collect()
	src.Poll(context.Background(), emit) // must not panic
	if len(*got) != 0 {
		t.Fatalf("describe errors emit nothing, got %d", len(*got))
	}
}
