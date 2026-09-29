package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertsmanagement/armalertsmanagement"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const sourceAzureMonitor = "azure-monitor"

// armAlertsLister lists fired Azure Monitor alerts for one subscription by
// draining the GetAll pager; tests provide a fake returning a slice.
type armAlertsLister struct {
	client *armalertsmanagement.AlertsClient
}

func (l *armAlertsLister) List(ctx context.Context) ([]*armalertsmanagement.Alert, error) {
	pageCount := int64(250)
	window := armalertsmanagement.TimeRangeThirtyD
	return drainPager(ctx, sourceAzureMonitor, l.client.NewGetAllPager(&armalertsmanagement.AlertsClientGetAllOptions{
		PageCount: &pageCount,
		TimeRange: &window,
	}),
		func(r armalertsmanagement.AlertsClientGetAllResponse) []*armalertsmanagement.Alert { return r.Value })
}

// newAzureMonitorSource ingests fired Azure Monitor alerts (Alerts
// Management). An alert whose monitorCondition is Fired pages (severity mapped
// from Sev0-Sev4); Resolved resolves. This is the Azure analog of the AWS
// CloudWatch-alarm source: one source covering every metric/log/activity-log
// alert configured in the subscription.
func newAzureMonitorSource(subs []sources.Scoped[*armalertsmanagement.Alert]) sources.Source {
	return sources.NewListSource(sourceAzureMonitor, subs, evaluateAzureAlert)
}

func evaluateAzureAlert(subscription string, al *armalertsmanagement.Alert, emit sources.Emit) {
	if al == nil {
		return
	}
	name := strVal(al.Name)
	if name == "" {
		return
	}
	if al.Properties == nil || al.Properties.Essentials == nil || al.Properties.Essentials.MonitorCondition == nil {
		return
	}
	e := al.Properties.Essentials
	condition := string(*e.MonitorCondition)
	sev := alert.SeverityWarning
	if e.Severity != nil {
		sev = azureSeverity(string(*e.Severity))
	}
	rule, target := strVal(e.AlertRule), strVal(e.TargetResourceName)
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
