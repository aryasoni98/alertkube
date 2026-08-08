package gcp

import (
	"context"
	"testing"

	"cloud.google.com/go/container/apiv1/containerpb"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

func collect() (sources.Emit, *[]*alert.Alert) {
	var got []*alert.Alert
	return func(a *alert.Alert) { got = append(got, a) }, &got
}

type fakeGKELister struct {
	byProject map[string][]*containerpb.Cluster
	err       error
}

func (f *fakeGKELister) List(_ context.Context, project string) ([]*containerpb.Cluster, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byProject[project], nil
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
	fake := &fakeGKELister{byProject: map[string][]*containerpb.Cluster{
		"proj-1": {
			gkeCluster("healthy", "us-central1", containerpb.Cluster_RUNNING),
			gkeCluster("broken", "us-east1", containerpb.Cluster_ERROR),
		},
	}}
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
