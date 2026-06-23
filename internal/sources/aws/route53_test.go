package aws

import (
	"context"
	"errors"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"

	"alertkube/internal/alert"
)

type fakeRoute53 struct {
	checks    []string
	obs       map[string][]string
	listErr   error
	statusErr error
}

func (f *fakeRoute53) ListHealthChecks(_ context.Context, _ *route53.ListHealthChecksInput, _ ...func(*route53.Options)) (*route53.ListHealthChecksOutput, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	hcs := make([]r53types.HealthCheck, 0, len(f.checks))
	for _, id := range f.checks {
		hcs = append(hcs, r53types.HealthCheck{Id: awssdk.String(id)})
	}
	return &route53.ListHealthChecksOutput{HealthChecks: hcs, IsTruncated: false}, nil
}

func (f *fakeRoute53) GetHealthCheckStatus(_ context.Context, in *route53.GetHealthCheckStatusInput, _ ...func(*route53.Options)) (*route53.GetHealthCheckStatusOutput, error) {
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	var obs []r53types.HealthCheckObservation
	for _, s := range f.obs[awssdk.ToString(in.HealthCheckId)] {
		obs = append(obs, r53types.HealthCheckObservation{StatusReport: &r53types.StatusReport{Status: awssdk.String(s)}})
	}
	return &route53.GetHealthCheckStatusOutput{HealthCheckObservations: obs}, nil
}

func runR53(t *testing.T, statuses []string) *alert.Alert {
	t.Helper()
	fake := &fakeRoute53{checks: []string{"hc-1"}, obs: map[string][]string{"hc-1": statuses}}
	src := &route53Source{client: fake}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(*got))
	}
	return (*got)[0]
}

func TestRoute53Aggregation(t *testing.T) {
	cases := []struct {
		name         string
		statuses     []string
		wantResolved bool
	}{
		{"majority failure fires", []string{"Failure: timeout", "Failure: 503", "Success: 200"}, false},
		{"minority failure resolves", []string{"Failure: timeout", "Success: 200", "Success: 200"}, true},
		{"tie resolves", []string{"Failure: timeout", "Success: 200"}, true},
		{"all success resolves", []string{"Success: 200", "Success: 200"}, true},
		{"no observations resolves", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := runR53(t, tc.statuses)
			if a.Kind != alert.KindRoute53HealthCheck {
				t.Errorf("kind = %s, want Route53HealthCheck", a.Kind)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v (statuses=%v)", a.Resolved, tc.wantResolved, tc.statuses)
			}
			if !tc.wantResolved && a.Severity != alert.SeverityCritical {
				t.Errorf("severity = %q, want critical", a.Severity)
			}
		})
	}
}

func TestRoute53SourcePoll(t *testing.T) {
	fake := &fakeRoute53{
		checks: []string{"healthy", "down"},
		obs: map[string][]string{
			"healthy": {"Success: 200", "Success: 200"},
			"down":    {"Failure: timeout", "Failure: timeout"},
		},
	}
	src := &route53Source{client: fake}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}

func TestRoute53ListErrorNoEmit(t *testing.T) {
	fake := &fakeRoute53{listErr: errors.New("boom")}
	src := &route53Source{client: fake}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 0 {
		t.Fatalf("expected no alerts on list error, got %d", len(*got))
	}
}
