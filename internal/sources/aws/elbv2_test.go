package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/aryasoni98/alertkube/internal/alert"
)

type fakeELBV2 struct {
	lbPages   []*elbv2.DescribeLoadBalancersOutput
	lbIdx     int
	tgPages   []*elbv2.DescribeTargetGroupsOutput
	tgIdx     int
	health    map[string]*elbv2.DescribeTargetHealthOutput
	lbErr     error
	tgErr     error
	healthErr error
}

func (f *fakeELBV2) DescribeLoadBalancers(_ context.Context, _ *elbv2.DescribeLoadBalancersInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error) {
	if f.lbErr != nil {
		return nil, f.lbErr
	}
	out := f.lbPages[f.lbIdx]
	if f.lbIdx < len(f.lbPages)-1 {
		f.lbIdx++
	}
	return out, nil
}

func (f *fakeELBV2) DescribeTargetGroups(_ context.Context, _ *elbv2.DescribeTargetGroupsInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetGroupsOutput, error) {
	if f.tgErr != nil {
		return nil, f.tgErr
	}
	out := f.tgPages[f.tgIdx]
	if f.tgIdx < len(f.tgPages)-1 {
		f.tgIdx++
	}
	return out, nil
}

func (f *fakeELBV2) DescribeTargetHealth(_ context.Context, in *elbv2.DescribeTargetHealthInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeTargetHealthOutput, error) {
	if f.healthErr != nil {
		return nil, f.healthErr
	}
	return f.health[awssdk.ToString(in.TargetGroupArn)], nil
}

func lbState(name string, code elbv2types.LoadBalancerStateEnum) elbv2types.LoadBalancer {
	return elbv2types.LoadBalancer{
		LoadBalancerName: awssdk.String(name),
		Type:             elbv2types.LoadBalancerTypeEnumApplication,
		State:            &elbv2types.LoadBalancerState{Code: code},
	}
}

func thd(states ...elbv2types.TargetHealthStateEnum) []elbv2types.TargetHealthDescription {
	out := make([]elbv2types.TargetHealthDescription, 0, len(states))
	for _, st := range states {
		out = append(out, elbv2types.TargetHealthDescription{TargetHealth: &elbv2types.TargetHealth{State: st}})
	}
	return out
}

func TestEvaluateLoadBalancer(t *testing.T) {
	cases := []struct {
		name         string
		lb           elbv2types.LoadBalancer
		wantResolved bool
		wantReason   string
		wantSeverity alert.Severity
	}{
		{"active resolves", lbState("web", elbv2types.LoadBalancerStateEnumActive), true, "", ""},
		{"failed critical", lbState("web", elbv2types.LoadBalancerStateEnumFailed), false, "LoadBalancerFailed", alert.SeverityCritical},
		{"impaired critical", lbState("web", elbv2types.LoadBalancerStateEnumActiveImpaired), false, "LoadBalancerImpaired", alert.SeverityCritical},
		{"provisioning warning", lbState("web", elbv2types.LoadBalancerStateEnumProvisioning), false, "LoadBalancerNotActive", alert.SeverityWarning},
		{"nil state treated active", elbv2types.LoadBalancer{LoadBalancerName: awssdk.String("web")}, true, "", ""},
		{"empty name skipped", lbState("", elbv2types.LoadBalancerStateEnumFailed), true, "", ""}, // skipped: no emit
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateLoadBalancer("us-east-1", tc.lb, emit)
			if tc.name == "empty name skipped" {
				if len(*got) != 0 {
					t.Fatalf("expected no emit for empty name, got %d", len(*got))
				}
				return
			}
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindLoadBalancer {
				t.Errorf("kind = %s, want LoadBalancer", a.Kind)
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

func TestEvaluateTargetGroup(t *testing.T) {
	cases := []struct {
		name         string
		descs        []elbv2types.TargetHealthDescription
		wantResolved bool
		wantReason   string
		wantSeverity alert.Severity
	}{
		{"all healthy resolves", thd(elbv2types.TargetHealthStateEnumHealthy, elbv2types.TargetHealthStateEnumHealthy), true, "", ""},
		{"partial unhealthy degraded", thd(elbv2types.TargetHealthStateEnumHealthy, elbv2types.TargetHealthStateEnumUnhealthy), false, "TargetGroupDegraded", alert.SeverityWarning},
		{"none healthy critical", thd(elbv2types.TargetHealthStateEnumUnhealthy, elbv2types.TargetHealthStateEnumUnhealthy), false, "TargetGroupNoHealthyTargets", alert.SeverityCritical},
		{"empty resolves", nil, true, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateTargetGroup("us-east-1", "tg", tc.descs, emit)
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindTargetGroup {
				t.Errorf("kind = %s, want TargetGroup", a.Kind)
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

func TestELBV2SourcePoll(t *testing.T) {
	fake := &fakeELBV2{
		lbPages: []*elbv2.DescribeLoadBalancersOutput{{
			LoadBalancers: []elbv2types.LoadBalancer{lbState("broken-lb", elbv2types.LoadBalancerStateEnumFailed)},
		}},
		tgPages: []*elbv2.DescribeTargetGroupsOutput{{
			TargetGroups: []elbv2types.TargetGroup{
				{TargetGroupName: awssdk.String("good-tg"), TargetGroupArn: awssdk.String("arn:good")},
				{TargetGroupName: awssdk.String("bad-tg"), TargetGroupArn: awssdk.String("arn:bad")},
			},
		}},
		health: map[string]*elbv2.DescribeTargetHealthOutput{
			"arn:good": {TargetHealthDescriptions: thd(elbv2types.TargetHealthStateEnumHealthy)},
			"arn:bad":  {TargetHealthDescriptions: thd(elbv2types.TargetHealthStateEnumUnhealthy)},
		},
	}
	src := &elbv2Source{regions: []elbv2Region{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if len(*got) != 3 {
		t.Fatalf("expected 3 alerts (LB failed, good-tg resolve, bad-tg critical), got %d", len(*got))
	}
	byName := map[string]*alert.Alert{}
	for _, a := range *got {
		byName[a.Name] = a
	}
	if a := byName["broken-lb"]; a == nil || a.Resolved || a.Kind != alert.KindLoadBalancer {
		t.Errorf("broken-lb should be a firing LoadBalancer alert: %+v", a)
	}
	if a := byName["good-tg"]; a == nil || !a.Resolved {
		t.Errorf("good-tg should resolve: %+v", a)
	}
	if a := byName["bad-tg"]; a == nil || a.Resolved || a.Severity != alert.SeverityCritical {
		t.Errorf("bad-tg should be critical firing: %+v", a)
	}
}
