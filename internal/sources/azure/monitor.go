package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertsmanagement/armalertsmanagement"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceAzureMonitor = "azure-monitor"

// alertsLister lists fired Azure Monitor alerts for one subscription. The real
// adapter drains the GetAll pager; tests provide a fake returning a slice.
type alertsLister interface {
	List(ctx context.Context) ([]*armalertsmanagement.Alert, error)
}

type armAlertsLister struct {
	client *armalertsmanagement.AlertsClient
}

func (l *armAlertsLister) List(ctx context.Context) ([]*armalertsmanagement.Alert, error) {
	return drainPager(ctx, l.client.NewGetAllPager(nil),
		func(r armalertsmanagement.AlertsClientGetAllResponse) []*armalertsmanagement.Alert { return r.Value })
}

type azureMonitorSubscription = subLister[alertsLister]

// azureMonitorSource ingests fired Azure Monitor alerts (Alerts Management).
// An alert whose monitorCondition is Fired pages (severity mapped from
// Sev0-Sev4); Resolved resolves. This is the Azure analog of the AWS
// CloudWatch-alarm source: one source covering every metric/log/activity-log
// alert configured in the subscription.
type azureMonitorSource struct {
	subs []azureMonitorSubscription
}

func (s *azureMonitorSource) Name() string { return sourceAzureMonitor }

func (s *azureMonitorSource) Poll(ctx context.Context, emit sources.Emit) {
	pollBySubscription(ctx, sourceAzureMonitor, s.subs, emit, evaluateAzureAlert)
}

func evaluateAzureAlert(subscription string, al *armalertsmanagement.Alert, emit sources.Emit) {
	if al == nil {
		return
	}
	name := strVal(al.Name)
	if name == "" {
		return
	}
	var condition, rule, target string
	sev := alert.SeverityWarning
	if al.Properties != nil && al.Properties.Essentials != nil {
		e := al.Properties.Essentials
		if e.MonitorCondition != nil {
			condition = string(*e.MonitorCondition)
		}
		if e.Severity != nil {
			sev = azureSeverity(string(*e.Severity))
		}
		rule = strVal(e.AlertRule)
		target = strVal(e.TargetResourceName)
	}
	if condition == string(armalertsmanagement.MonitorConditionResolved) {
		emitResolve(emit, alert.KindAzureMonitorAlert, subscription, name)
		return
	}
	emitFiring(emit, alert.KindAzureMonitorAlert, subscription, name, "AzureMonitorAlert",
		azureAlertSummary(rule, target), sev,
		map[string]string{"alertRule": rule, "targetResource": target, "monitorCondition": condition})
}

// azureSeverity maps Azure's Sev0-Sev4 onto alertkube severities: Sev0/Sev1
// critical, Sev4 info, the rest warning.
func azureSeverity(sev string) alert.Severity {
	switch sev {
	case "Sev0", "Sev1":
		return alert.SeverityCritical
	case "Sev4":
		return alert.SeverityInfo
	default:
		return alert.SeverityWarning
	}
}

func azureAlertSummary(rule, target string) string {
	s := "Azure Monitor alert"
	if rule != "" {
		s += " " + rule
	}
	if target != "" {
		s += " on " + target
	}
	return s
}
