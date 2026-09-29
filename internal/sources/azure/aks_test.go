package azure

import (
	"context"
	"strings"
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

// fakeLister binds a canned list result (or error) to one subscription - the
// same []sources.Scoped shape buildSub produces from a real ARM adapter.
func fakeLister[T any](subscription string, items []T, err error) []sources.Scoped[T] {
	return []sources.Scoped[T]{{
		Scope: subscription,
		List:  func(context.Context) ([]T, error) { return items, err },
	}}
}

func aksCluster(name, location, provState string, power *armcontainerservice.Code) *armcontainerservice.ManagedCluster {
	props := &armcontainerservice.ManagedClusterProperties{ProvisioningState: sp(provState)}
	if power != nil {
		props.PowerState = &armcontainerservice.PowerState{Code: power}
	}
	return &armcontainerservice.ManagedCluster{Name: sp(name), Location: sp(location), Properties: props}
}

func TestEvaluateAKSCluster(t *testing.T) {
	running := codePtr(armcontainerservice.CodeRunning)
	stopped := codePtr(armcontainerservice.CodeStopped)
	cases := []struct {
		name         string
		cluster      *armcontainerservice.ManagedCluster
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
		{"missing provisioning state resolves", aksCluster("c", "eastus", "", nil), true, true, "", ""},
		{"nil cluster skipped", nil, false, false, "", ""},
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
	items := []*armcontainerservice.ManagedCluster{
		aksCluster("healthy", "eastus", "Succeeded", running),
		aksCluster("broken", "westus", "Failed", nil),
	}
	src := newAKSSource(fakeLister("sub-1", items, nil))
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

// TestAKSReasonAndSummaryStrings pins every reason and summary string the
// cluster and node-pool evaluators emit, so the shared decision table cannot
// drift either level's wording. A zero reason means the case must resolve.
func TestAKSReasonAndSummaryStrings(t *testing.T) {
	running := codePtr(armcontainerservice.CodeRunning)
	stopped := codePtr(armcontainerservice.CodeStopped)
	cases := []struct {
		name        string
		state       string
		power       *armcontainerservice.Code
		wantReason  string
		wantSummary string
		wantSev     alert.Severity
	}{
		{"empty state resolves", "", stopped, "", "", ""},
		{"succeeded running resolves", "Succeeded", running, "", "", ""},
		{"succeeded no power resolves", "Succeeded", nil, "", "", ""},
		{"succeeded stopped", "Succeeded", stopped, "Stopped", "{noun} {id} is stopped", alert.SeverityWarning},
		{"failed", "Failed", running, "ProvisioningFailed", "{noun} {id} provisioning state is Failed", alert.SeverityCritical},
		{"canceled stopped", "Canceled", stopped, "ProvisioningFailed", "{noun} {id} provisioning state is Canceled", alert.SeverityCritical},
		{"updating", "Updating", stopped, "NotReady", "{noun} {id} is not ready (provisioning state Updating)", alert.SeverityWarning},
	}
	levels := []struct {
		level, prefix, noun, id string
		kind                    alert.Kind
		eval                    func(emit sources.Emit, state string, power *armcontainerservice.Code)
	}{
		{"cluster", "AKSCluster", "AKS cluster", "c", alert.KindAKSCluster,
			func(emit sources.Emit, state string, power *armcontainerservice.Code) {
				evaluateAKSCluster("sub-1", aksCluster("c", "eastus", state, power), emit)
			}},
		{"node pool", "AKSNodePool", "AKS node pool", "cl/np", alert.KindAKSNodePool,
			func(emit sources.Emit, state string, power *armcontainerservice.Code) {
				evaluateAKSNodePools("sub-1", aksClusterWithPools("cl", "eastus", agentPool("np", state, power)), emit)
			}},
	}
	for _, lv := range levels {
		for _, tc := range cases {
			t.Run(lv.level+"/"+tc.name, func(t *testing.T) {
				emit, got := collect()
				lv.eval(emit, tc.state, tc.power)
				if len(*got) != 1 {
					t.Fatalf("expected 1 alert, got %d", len(*got))
				}
				a := (*got)[0]
				if a.Kind != lv.kind || a.Namespace != "sub-1/eastus" || a.Name != lv.id {
					t.Fatalf("identity = %s %s/%s, want %s sub-1/eastus/%s", a.Kind, a.Namespace, a.Name, lv.kind, lv.id)
				}
				if tc.wantReason == "" {
					if !a.Resolved {
						t.Fatalf("want resolve, got firing %q", a.Reason)
					}
					return
				}
				wantReason := lv.prefix + tc.wantReason
				wantSummary := strings.NewReplacer("{noun}", lv.noun, "{id}", lv.id).Replace(tc.wantSummary)
				if a.Resolved || a.Reason != wantReason || a.Summary != wantSummary || a.Severity != tc.wantSev {
					t.Errorf("got resolved=%v %q %q %q, want firing %q %q %q",
						a.Resolved, a.Reason, a.Summary, a.Severity, wantReason, wantSummary, tc.wantSev)
				}
			})
		}
	}
}
