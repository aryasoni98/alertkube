package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"

	"alertkube/internal/alert"
)

type fakeASG struct {
	pages []*autoscaling.DescribeAutoScalingGroupsOutput
	idx   int
	err   error
}

func (f *fakeASG) DescribeAutoScalingGroups(_ context.Context, _ *autoscaling.DescribeAutoScalingGroupsInput, _ ...func(*autoscaling.Options)) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.pages[f.idx]
	if f.idx < len(f.pages)-1 {
		f.idx++
	}
	return out, nil
}

func asgInstance(health string, state astypes.LifecycleState) astypes.Instance {
	return astypes.Instance{HealthStatus: awssdk.String(health), LifecycleState: state}
}

func asg(name string, desired int32, ins ...astypes.Instance) astypes.AutoScalingGroup {
	return astypes.AutoScalingGroup{
		AutoScalingGroupName: awssdk.String(name),
		DesiredCapacity:      awssdk.Int32(desired),
		Instances:            ins,
	}
}

func TestEvaluateASG(t *testing.T) {
	healthy := asgInstance("Healthy", astypes.LifecycleStateInService)
	unhealthy := asgInstance("Unhealthy", astypes.LifecycleStateInService)
	cases := []struct {
		name         string
		group        astypes.AutoScalingGroup
		wantEmit     bool
		wantResolved bool
		wantReason   string
		wantSeverity alert.Severity
	}{
		{"all healthy resolves", asg("a", 2, healthy, healthy), true, true, "", ""},
		{"none healthy critical", asg("a", 2, unhealthy, unhealthy), true, false, "ASGNoHealthyCapacity", alert.SeverityCritical},
		{"shortfall warning", asg("a", 2, healthy), true, false, "ASGCapacityShortfall", alert.SeverityWarning},
		{"empty name skipped", asg("", 1, healthy), false, false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateASG("us-east-1", tc.group, emit)
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
			if a.Kind != alert.KindASG {
				t.Errorf("kind = %s, want AutoScalingGroup", a.Kind)
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

func TestASGSourcePoll(t *testing.T) {
	fake := &fakeASG{pages: []*autoscaling.DescribeAutoScalingGroupsOutput{{
		AutoScalingGroups: []astypes.AutoScalingGroup{
			asg("good", 1, asgInstance("Healthy", astypes.LifecycleStateInService)),
			asg("bad", 1, asgInstance("Unhealthy", astypes.LifecycleStateInService)),
		},
	}}}
	src := &asgSource{regions: []asgRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}
