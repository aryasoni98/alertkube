package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertsmanagement/armalertsmanagement"

	"alertkube/internal/alert"
)

type fakeAlertsLister struct {
	alerts []*armalertsmanagement.Alert
	err    error
}

func (f *fakeAlertsLister) List(context.Context) ([]*armalertsmanagement.Alert, error) {
	return f.alerts, f.err
}

func azAlert(name, condition, sev, rule, target string) *armalertsmanagement.Alert {
	mc := armalertsmanagement.MonitorCondition(condition)
	sv := armalertsmanagement.Severity(sev)
	return &armalertsmanagement.Alert{
		Name: sp(name),
		Properties: &armalertsmanagement.AlertProperties{
			Essentials: &armalertsmanagement.Essentials{
				MonitorCondition:   &mc,
				Severity:           &sv,
				AlertRule:          sp(rule),
				TargetResourceName: sp(target),
			},
		},
	}
}

func TestEvaluateAzureAlert(t *testing.T) {
	cases := []struct {
		name         string
		alert        *armalertsmanagement.Alert
		wantEmit     bool
		wantResolved bool
		wantSeverity alert.Severity
	}{
		{"fired sev1 critical", azAlert("a1", "Fired", "Sev1", "cpu-rule", "vm-1"), true, false, alert.SeverityCritical},
		{"fired sev3 warning", azAlert("a2", "Fired", "Sev3", "mem-rule", "vm-2"), true, false, alert.SeverityWarning},
		{"fired sev4 info", azAlert("a3", "Fired", "Sev4", "info-rule", "vm-3"), true, false, alert.SeverityInfo},
		{"resolved resolves", azAlert("a4", "Resolved", "Sev1", "cpu-rule", "vm-1"), true, true, ""},
		{"empty name skipped", azAlert("", "Fired", "Sev1", "r", "t"), false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateAzureAlert("sub-1", tc.alert, emit)
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
			if a.Kind != alert.KindAzureMonitorAlert {
				t.Errorf("kind = %s, want AzureMonitorAlert", a.Kind)
			}
			if a.Namespace != "sub-1" {
				t.Errorf("scope = %s, want sub-1", a.Namespace)
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

func TestAzureMonitorSourcePoll(t *testing.T) {
	fake := &fakeAlertsLister{alerts: []*armalertsmanagement.Alert{
		azAlert("fired-1", "Fired", "Sev1", "cpu-rule", "vm-1"),
		azAlert("resolved-1", "Resolved", "Sev2", "mem-rule", "vm-2"),
	}}
	src := &azureMonitorSource{subs: []azureMonitorSubscription{{subscription: "sub-1", lister: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
	var firing, resolved int
	for _, a := range *got {
		if a.Resolved {
			resolved++
		} else {
			firing++
			if a.Severity != alert.SeverityCritical {
				t.Errorf("fired Sev1 should be critical, got %q", a.Severity)
			}
		}
	}
	if firing != 1 || resolved != 1 {
		t.Fatalf("firing=%d resolved=%d, want 1 and 1", firing, resolved)
	}
}
