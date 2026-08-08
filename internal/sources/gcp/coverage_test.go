package gcp

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/sources"
)

// TestSourceNames pins the Name() of every GCP source so a rename that would
// break metric labels / docs is caught.
func TestSourceNames(t *testing.T) {
	cases := []struct{ got, want string }{
		{newGKESource(nil, nil).Name(), sourceGKE},
		{newGCESource(nil, nil).Name(), sourceGCE},
		{newCloudSQLSource(nil, nil).Name(), sourceCloudSQL},
		{newMonitoringSource(nil, nil).Name(), sourceGCPMonitoring},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("source name %q != %q", c.got, c.want)
		}
	}
}

func TestPollErrIncrementsMetric(t *testing.T) {
	const src = "gcp-test-pollerr"
	before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(src))
	pollErr(src, "proj-1", errors.New("PermissionDenied"))
	if after := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(src)); after != before+1 {
		t.Fatalf("CloudPollErrors not incremented: before=%v after=%v", before, after)
	}
}

// TestSourcesRecordListErrors drives every GCP source's Poll with a lister that
// errors, asserting it records the failure, emits nothing, and does not panic.
func TestSourcesRecordListErrors(t *testing.T) {
	boom := errors.New("ListFailed")
	srcs := []sources.Source{
		newGKESource([]string{"p"}, &fakeGKELister{err: boom}),
		newGCESource([]string{"p"}, &fakeGCELister{err: boom}),
		newCloudSQLSource([]string{"p"}, &fakeSQLLister{err: boom}),
		newMonitoringSource([]string{"p"}, &fakePolicyLister{err: boom}),
	}
	for _, s := range srcs {
		t.Run(s.Name(), func(t *testing.T) {
			before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(s.Name()))
			emit, got := collect()
			s.Poll(context.Background(), emit) // must not panic
			if len(*got) != 0 {
				t.Fatalf("list error must emit nothing, got %d", len(*got))
			}
			if after := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(s.Name())); after != before+1 {
				t.Fatalf("CloudPollErrors[%s] not incremented: %v -> %v", s.Name(), before, after)
			}
		})
	}
}
