package gcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	container "cloud.google.com/go/container/apiv1"
	"cloud.google.com/go/container/apiv1/containerpb"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/api/option"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/metrics"
	"github.com/aryasoni98/alertkube/v2/internal/sources"
)

func collect() (sources.Emit, *[]*alert.Alert) {
	var got []*alert.Alert
	return func(a *alert.Alert) { got = append(got, a) }, &got
}

// fakeLister serves canned per-project list results, or err for every
// project - the same shape as a real adapter's List method value.
func fakeLister[T any](byProject map[string][]T, err error) func(context.Context, string) ([]T, error) {
	return func(_ context.Context, project string) ([]T, error) {
		if err != nil {
			return nil, err
		}
		return byProject[project], nil
	}
}

func gkeCluster(name, location string, status containerpb.Cluster_Status) *containerpb.Cluster {
	return &containerpb.Cluster{Name: name, Location: location, Status: status}
}

func TestEvaluateGKECluster(t *testing.T) {
	cases := []struct {
		name         string
		cluster      *containerpb.Cluster
		wantEmit     bool
		wantResolved bool
		wantReason   string
		wantSeverity alert.Severity
	}{
		{"running resolves", gkeCluster("c", "us-central1", containerpb.Cluster_RUNNING), true, true, "", ""},
		{"error critical", gkeCluster("c", "us-central1", containerpb.Cluster_ERROR), true, false, "GKEClusterUnhealthy", alert.SeverityCritical},
		{"degraded critical", gkeCluster("c", "us-central1", containerpb.Cluster_DEGRADED), true, false, "GKEClusterUnhealthy", alert.SeverityCritical},
		{"provisioning warns", gkeCluster("c", "us-central1", containerpb.Cluster_PROVISIONING), true, false, "GKEClusterNotRunning", alert.SeverityWarning},
		{"empty name skipped", gkeCluster("", "us-central1", containerpb.Cluster_ERROR), false, false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateGKECluster("proj-1", tc.cluster, emit)
			if !tc.wantEmit {
				if len(*got) != 0 {
					t.Fatalf("expected no emit, got %d", len(*got))
				}
				return
			}
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindGKECluster {
				t.Errorf("kind = %s, want GKECluster", a.Kind)
			}
			if a.Namespace != "proj-1/us-central1" {
				t.Errorf("scope = %s, want proj-1/us-central1", a.Namespace)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && (a.Reason != tc.wantReason || a.Severity != tc.wantSeverity) {
				t.Errorf("reason/sev = %q/%q, want %q/%q", a.Reason, a.Severity, tc.wantReason, tc.wantSeverity)
			}
		})
	}
}

func TestGKESourcePoll(t *testing.T) {
	fake := fakeLister(map[string][]*containerpb.Cluster{
		"proj-1": {
			gkeCluster("healthy", "us-central1", containerpb.Cluster_RUNNING),
			gkeCluster("broken", "us-east1", containerpb.Cluster_ERROR),
		},
	}, nil)
	src := newGKESource([]string{"proj-1"}, fake)
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
	for _, a := range *got {
		switch a.Name {
		case "healthy":
			if !a.Resolved {
				t.Errorf("healthy should resolve: %+v", a)
			}
		case "broken":
			if a.Resolved || a.Severity != alert.SeverityCritical {
				t.Errorf("broken should be critical firing: %+v", a)
			}
		default:
			t.Errorf("unexpected cluster %q", a.Name)
		}
	}
}

// TestAPIGKEListerKeepsPartialList drives the real Cluster Manager adapter
// against a canned REST response. A list with missing zones must still return
// the clusters it did get, so their alerts keep re-firing and resolving, and
// record the gap as a poll error. Failing the whole project instead would let
// every reachable cluster's alert TTL-resolve during a zonal outage.
func TestAPIGKEListerKeepsPartialList(t *testing.T) {
	const cluster = `{"name":"broken","location":"us-central1-a","status":"ERROR"}`
	cases := []struct {
		name     string
		body     string
		wantErrs float64
	}{
		{"complete list", `{"clusters":[` + cluster + `]}`, 0},
		{"missing zones", `{"clusters":[` + cluster + `],"missingZones":["us-east1-b"]}`, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/projects/proj-1/locations/-/clusters" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			client, err := container.NewClusterManagerRESTClient(context.Background(),
				option.WithEndpoint(srv.URL), option.WithHTTPClient(srv.Client()))
			if err != nil {
				t.Fatalf("client: %v", err)
			}
			defer func() { _ = client.Close() }()

			before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceGKE))
			emit, got := collect()
			newGKESource([]string{"proj-1"}, (&apiGKELister{client: client}).List).Poll(context.Background(), emit)

			if len(*got) != 1 || (*got)[0].Name != "broken" || (*got)[0].Resolved {
				t.Fatalf("returned cluster must be evaluated as firing, got %d alerts: %+v", len(*got), *got)
			}
			if after := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceGKE)); after-before != tc.wantErrs {
				t.Fatalf("CloudPollErrors[%s] delta = %v, want %v", sourceGKE, after-before, tc.wantErrs)
			}
		})
	}
}
