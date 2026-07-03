package gcp

import (
	"context"
	"errors"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/api/iterator"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceGCPMonitoring = "gcp-monitoring"

// policyLister lists Cloud Monitoring alert policies in one project. The real
// adapter drains the API iterator; tests provide a fake.
type policyLister interface {
	List(ctx context.Context, project string) ([]*monitoringpb.AlertPolicy, error)
}

type apiPolicyLister struct {
	client *monitoring.AlertPolicyClient
}

func (l *apiPolicyLister) List(ctx context.Context, project string) ([]*monitoringpb.AlertPolicy, error) {
	it := l.client.ListAlertPolicies(ctx, &monitoringpb.ListAlertPoliciesRequest{Name: "projects/" + project})
	var out []*monitoringpb.AlertPolicy
	for {
		p, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// gcpMonitoringSource surfaces Cloud Monitoring coverage posture: it alerts
// (warning) when an alert policy is disabled and resolves when it is
// re-enabled.
//
// NOTE: GCP's stable Go SDK exposes no fired-incident listing, so unlike the
// AWS CloudWatch and Azure Monitor sources this is a posture signal ("is this
// monitoring switched on?"), not a fired-alert feed. It is a deliberate,
// documented limitation rather than a faked incident stream.
type gcpMonitoringSource struct {
	projects []string
	lister   policyLister
}

func (s *gcpMonitoringSource) Name() string { return sourceGCPMonitoring }

func (s *gcpMonitoringSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByProject(ctx, sourceGCPMonitoring, s.projects, s.lister, emit, evaluateAlertPolicy)
}

func evaluateAlertPolicy(project string, p *monitoringpb.AlertPolicy, emit sources.Emit) {
	if p == nil || p.GetName() == "" {
		return
	}
	name := p.GetName()
	display := p.GetDisplayName()
	if p.GetEnabled().GetValue() {
		emitResolve(emit, alert.KindGCPAlertPolicy, project, name)
		return
	}
	emitFiring(emit, alert.KindGCPAlertPolicy, project, name, "GCPAlertPolicyDisabled",
		"Cloud Monitoring alert policy is disabled: "+display, alert.SeverityWarning,
		map[string]string{"displayName": display})
}
