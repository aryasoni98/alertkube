package gcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/option"

	"github.com/aryasoni98/alertkube/internal/alert"
)

func gceInstance(name, zone, status string) *compute.Instance {
	return &compute.Instance{
		Name:   name,
		Zone:   "https://www.googleapis.com/compute/v1/projects/p/zones/" + zone,
		Status: status,
	}
}

func TestEvaluateGCEInstance(t *testing.T) {
	cases := []struct {
		name         string
		instance     *compute.Instance
		wantEmit     bool
		wantResolved bool
	}{
		{"repairing critical", gceInstance("i", "us-central1-a", "REPAIRING"), true, false},
		{"running resolves", gceInstance("i", "us-central1-a", "RUNNING"), true, true},
		{"terminated resolves", gceInstance("i", "us-central1-a", "TERMINATED"), true, true},
		{"empty name skipped", gceInstance("", "us-central1-a", "REPAIRING"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateGCEInstance("proj-1", tc.instance, emit)
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
			if a.Kind != alert.KindGCEInstance {
				t.Errorf("kind = %s, want GCEInstance", a.Kind)
			}
			if a.Namespace != "proj-1/us-central1-a" {
				t.Errorf("scope = %s, want proj-1/us-central1-a (zone trimmed)", a.Namespace)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && a.Severity != alert.SeverityCritical {
				t.Errorf("severity = %q, want critical", a.Severity)
			}
		})
	}
}

func TestGCESourcePoll(t *testing.T) {
	fake := fakeLister(map[string][]*compute.Instance{
		"proj-1": {
			gceInstance("good", "us-central1-a", "RUNNING"),
			gceInstance("bad", "us-east1-b", "REPAIRING"),
		},
	}, nil)
	src := newGCESource([]string{"proj-1"}, fake)
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}

// TestAPIGCEListerKeepsReturnedInstances drives the real aggregated-list
// adapter against a canned REST page. The instances a page returns are always
// evaluated; an Unreachables entry must not discard the whole project.
func TestAPIGCEListerKeepsReturnedInstances(t *testing.T) {
	const instances = `"items":{"zones/us-central1-a":{"instances":[` +
		`{"name":"bad","zone":"https://www.googleapis.com/compute/v1/projects/proj-1/zones/us-central1-a","status":"REPAIRING"}]}}`
	cases := []struct {
		name string
		body string
	}{
		{"complete list", `{` + instances + `}`},
		{"unreachable zone", `{` + instances + `,"unreachables":["zones/us-east1-b"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/projects/proj-1/aggregated/instances" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			svc, err := compute.NewService(context.Background(),
				option.WithEndpoint(srv.URL+"/"), option.WithHTTPClient(srv.Client()))
			if err != nil {
				t.Fatalf("service: %v", err)
			}

			emit, got := collect()
			newGCESource([]string{"proj-1"}, (&apiGCELister{svc: svc}).List).Poll(context.Background(), emit)

			if len(*got) != 1 || (*got)[0].Name != "bad" || (*got)[0].Resolved {
				t.Fatalf("returned instance must be evaluated as firing, got %d alerts: %+v", len(*got), *got)
			}
		})
	}
}
