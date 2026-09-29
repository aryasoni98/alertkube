package gcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/api/option"
	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/metrics"
	"github.com/aryasoni98/alertkube/v2/internal/sources"
)

func sqlInstance(name, region, state string) *sqladmin.DatabaseInstance {
	return &sqladmin.DatabaseInstance{Name: name, Region: region, State: state, DatabaseVersion: "POSTGRES_15"}
}

func TestEvaluateCloudSQL(t *testing.T) {
	cases := []struct {
		name         string
		state        string
		wantResolved bool
		wantSeverity alert.Severity
	}{
		{"runnable resolves", "RUNNABLE", true, ""},
		{"failed critical", "FAILED", false, alert.SeverityCritical},
		{"suspended critical", "SUSPENDED", false, alert.SeverityCritical},
		{"maintenance warning", "MAINTENANCE", false, alert.SeverityWarning},
		{"pending-create warning", "PENDING_CREATE", false, alert.SeverityWarning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateCloudSQL("proj-1", sqlInstance("db", "us-central1", tc.state), emit)
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindCloudSQLInstance || a.Namespace != "proj-1/us-central1" {
				t.Errorf("identity: kind=%s ns=%s", a.Kind, a.Namespace)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && a.Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", a.Severity, tc.wantSeverity)
			}
		})
	}
}

func TestEvaluateCloudSQLEmptyNameSkipped(t *testing.T) {
	emit, got := collect()
	evaluateCloudSQL("proj-1", sqlInstance("", "us-central1", "FAILED"), emit)
	if len(*got) != 0 {
		t.Fatalf("expected no emit for empty name, got %d", len(*got))
	}
}

func TestCloudSQLSourcePoll(t *testing.T) {
	fake := fakeLister(map[string][]*sqladmin.DatabaseInstance{
		"proj-1": {
			sqlInstance("good", "us-central1", "RUNNABLE"),
			sqlInstance("bad", "us-east1", "FAILED"),
		},
	}, nil)
	src := newCloudSQLSource([]string{"proj-1"}, fake)
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}

// TestAPISQLListerPagination drives the real Cloud SQL adapter against canned
// REST pages. Each page carries one FAILED instance. A list that ends is read
// whole; a page token that does not advance, or a pager that never ends, stops
// the list, records the truncation, and still evaluates every instance fetched
// so far. Failing the project instead would let their alerts TTL-resolve.
func TestAPISQLListerPagination(t *testing.T) {
	cases := []struct {
		name          string
		next          func(req int, token string) string // next page token for request req
		wantRequests  int
		wantTruncated float64
	}{
		{"multi-page list", func(_ int, token string) string {
			if token == "" {
				return "t1"
			}
			return ""
		}, 2, 0},
		{"stuck page token", func(int, string) string { return "t1" }, 2, 1},
		{"runaway pager", func(req int, _ string) string { return fmt.Sprintf("t%d", req) }, sources.MaxPages, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/projects/proj-1/instances" {
					http.NotFound(w, r)
					return
				}
				req := int(requests.Add(1))
				if req > sources.MaxPages+5 {
					// Ends an unbounded loop so a missing guard fails instead of hanging.
					http.Error(w, `{"error":{"code":403,"message":"test page budget exhausted"}}`, http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"items":[{"name":"db-%d","region":"us-central1","state":"FAILED"}],"nextPageToken":%q}`,
					req, tc.next(req, r.URL.Query().Get("pageToken")))
			}))
			defer srv.Close()
			svc, err := sqladmin.NewService(context.Background(),
				option.WithEndpoint(srv.URL+"/"), option.WithHTTPClient(srv.Client()))
			if err != nil {
				t.Fatalf("service: %v", err)
			}

			truncBefore := testutil.ToFloat64(metrics.CloudPollTruncated.WithLabelValues(sourceCloudSQL))
			errsBefore := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceCloudSQL))
			emit, got := collect()
			newCloudSQLSource([]string{"proj-1"}, (&apiSQLLister{svc: svc}).List).Poll(context.Background(), emit)

			if len(*got) != tc.wantRequests {
				t.Fatalf("every fetched instance must be evaluated: got %d alerts, want %d", len(*got), tc.wantRequests)
			}
			for _, a := range *got {
				if a.Resolved {
					t.Fatalf("a FAILED instance resolved: %+v", a)
				}
			}
			if n := int(requests.Load()); n != tc.wantRequests {
				t.Fatalf("requests = %d, want %d", n, tc.wantRequests)
			}
			if d := testutil.ToFloat64(metrics.CloudPollTruncated.WithLabelValues(sourceCloudSQL)) - truncBefore; d != tc.wantTruncated {
				t.Fatalf("CloudPollTruncated delta = %v, want %v", d, tc.wantTruncated)
			}
			if d := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceCloudSQL)) - errsBefore; d != 0 {
				t.Fatalf("a truncated list is not a poll error: CloudPollErrors delta = %v", d)
			}
		})
	}
}
