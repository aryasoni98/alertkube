package gcp

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/container/apiv1/containerpb"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"github.com/prometheus/client_golang/prometheus/testutil"
	compute "google.golang.org/api/compute/v1"
	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/sources"
)

// TestSourceNames pins the Name() of every GCP source to its literal value.
// The names are CloudPollErrors label values, so a rename that would break
// dashboards and docs is caught.
func TestSourceNames(t *testing.T) {
	cases := []struct{ got, want string }{
		{newGKESource(nil, nil).Name(), "gcp-gke"},
		{newGCESource(nil, nil).Name(), "gcp-compute"},
		{newCloudSQLSource(nil, nil).Name(), "gcp-cloudsql"},
		{newMonitoringSource(nil, nil).Name(), "gcp-monitoring"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("source name %q != %q", c.got, c.want)
		}
	}
}

// TestSourcesRecordListErrors drives every GCP source's Poll with a lister that
// errors, asserting it records the failure, emits nothing, and does not panic.
func TestSourcesRecordListErrors(t *testing.T) {
	boom := errors.New("ListFailed")
	srcs := []sources.Source{
		newGKESource([]string{"p"}, fakeLister[*containerpb.Cluster](nil, boom)),
		newGCESource([]string{"p"}, fakeLister[*compute.Instance](nil, boom)),
		newCloudSQLSource([]string{"p"}, fakeLister[*sqladmin.DatabaseInstance](nil, boom)),
		newMonitoringSource([]string{"p"}, fakeLister[*monitoringpb.AlertPolicy](nil, boom)),
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
