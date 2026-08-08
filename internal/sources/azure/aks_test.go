package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

func collect() (sources.Emit, *[]*alert.Alert) {
	var got []*alert.Alert
	return func(a *alert.Alert) { got = append(got, a) }, &got
}

func sp(s string) *string { return &s }

func codePtr(c armcontainerservice.Code) *armcontainerservice.Code { return &c }

type fakeAKSLister struct {
	clusters []armcontainerservice.ManagedCluster
	err      error
}

func (f *fakeAKSLister) List(context.Context) ([]armcontainerservice.ManagedCluster, error) {
	return f.clusters, f.err
}

func aksCluster(name, location, provState string, power *armcontainerservice.Code) armcontainerservice.ManagedCluster {
	props := &armcontainerservice.ManagedClusterProperties{ProvisioningState: sp(provState)}
	if power != nil {
		props.PowerState = &armcontainerservice.PowerState{Code: power}
	}
	return armcontainerservice.ManagedCluster{Name: sp(name), Location: sp(location), Properties: props}
}

func TestEvaluateAKSCluster(t *testing.T) {
	running := codePtr(armcontainerservice.CodeRunning)
	stopped := codePtr(armcontainerservice.CodeStopped)
	cases := []struct {
		name         string
		cluster      armcontainerservice.ManagedCluster
		wantEmit     bool
		wantResolved bool
		wantReason   string
		wantSeverity alert.Severity
	}{
		{"succeeded running resolves", aksCluster("c", "eastus", "Succeeded", running), true, true, "", ""},
		{"succeeded stopped warns", aksCluster("c", "eastus", "Succeeded", stopped), true, false, "AKSClusterStopped", alert.SeverityWarning},
		{"failed critical", aksCluster("c", "eastus", "Failed", nil), true, false, "AKSClusterProvisioningFailed", alert.SeverityCritical},
		{"canceled critical", aksCluster("c", "eastus", "Canceled", nil), true, false, "AKSClusterProvisioningFailed", alert.SeverityCritical},
		{"creating warns", aksCluster("c", "eastus", "Creating", nil), true, false, "AKSClusterNotReady", alert.SeverityWarning},
		{"empty name skipped", aksCluster("", "eastus", "Failed", nil), false, false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateAKSCluster("sub-1", tc.cluster, emit)
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
			if a.Kind != alert.KindAKSCluster {
				t.Errorf("kind = %s, want AKSCluster", a.Kind)
			}
			if a.Namespace != "sub-1/eastus" {
				t.Errorf("scope = %s, want sub-1/eastus", a.Namespace)
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

func TestAKSSourcePoll(t *testing.T) {
	running := codePtr(armcontainerservice.CodeRunning)
	fake := &fakeAKSLister{clusters: []armcontainerservice.ManagedCluster{
		aksCluster("healthy", "eastus", "Succeeded", running),
		aksCluster("broken", "westus", "Failed", nil),
	}}
	src := &aksSource{subs: []aksSubscription{{subscription: "sub-1", lister: fake}}}
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
