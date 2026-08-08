package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/aryasoni98/alertkube/internal/alert"
)

type fakeEC2 struct {
	pages []*ec2.DescribeInstanceStatusOutput
	idx   int
	err   error
}

func (f *fakeEC2) DescribeInstanceStatus(_ context.Context, _ *ec2.DescribeInstanceStatusInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstanceStatusOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.pages[f.idx]
	if f.idx < len(f.pages)-1 {
		f.idx++
	}
	return out, nil
}

func instStatus(id string, sys, inst ec2types.SummaryStatus) ec2types.InstanceStatus {
	return ec2types.InstanceStatus{
		InstanceId:       awssdk.String(id),
		AvailabilityZone: awssdk.String("us-east-1a"),
		SystemStatus:     &ec2types.InstanceStatusSummary{Status: sys},
		InstanceStatus:   &ec2types.InstanceStatusSummary{Status: inst},
	}
}

func TestEvaluateInstance(t *testing.T) {
	cases := []struct {
		name         string
		status       ec2types.InstanceStatus
		wantResolved bool
	}{
		{"system impaired fires", instStatus("i-1", ec2types.SummaryStatusImpaired, ec2types.SummaryStatusOk), false},
		{"instance impaired fires", instStatus("i-2", ec2types.SummaryStatusOk, ec2types.SummaryStatusImpaired), false},
		{"both ok resolves", instStatus("i-3", ec2types.SummaryStatusOk, ec2types.SummaryStatusOk), true},
		{"initializing resolves", instStatus("i-4", ec2types.SummaryStatusInitializing, ec2types.SummaryStatusInitializing), true},
		{"nil summaries resolve", ec2types.InstanceStatus{InstanceId: awssdk.String("i-5")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateInstance("us-east-1", tc.status, emit)
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindEC2Instance {
				t.Errorf("kind = %s, want EC2Instance", a.Kind)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved {
				if a.Severity != alert.SeverityCritical {
					t.Errorf("severity = %q, want critical", a.Severity)
				}
				if a.Reason != "EC2StatusCheckFailed" {
					t.Errorf("reason = %q, want EC2StatusCheckFailed", a.Reason)
				}
			}
		})
	}
}

func TestEvaluateInstanceEmptyIDSkipped(t *testing.T) {
	emit, got := collect()
	evaluateInstance("us-east-1", instStatus("", ec2types.SummaryStatusImpaired, ec2types.SummaryStatusOk), emit)
	if len(*got) != 0 {
		t.Fatalf("expected no alert for empty instance id, got %d", len(*got))
	}
}

func TestEC2SourcePollPaginates(t *testing.T) {
	page1 := &ec2.DescribeInstanceStatusOutput{
		InstanceStatuses: []ec2types.InstanceStatus{instStatus("i-bad", ec2types.SummaryStatusImpaired, ec2types.SummaryStatusOk)},
		NextToken:        awssdk.String("more"),
	}
	page2 := &ec2.DescribeInstanceStatusOutput{
		InstanceStatuses: []ec2types.InstanceStatus{instStatus("i-good", ec2types.SummaryStatusOk, ec2types.SummaryStatusOk)},
	}
	fake := &fakeEC2{pages: []*ec2.DescribeInstanceStatusOutput{page1, page2}}
	src := &ec2Source{regions: []ec2Region{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts across 2 pages, got %d", len(*got))
	}
	for _, a := range *got {
		switch a.Name {
		case "i-bad":
			if a.Resolved {
				t.Error("i-bad should be firing")
			}
		case "i-good":
			if !a.Resolved {
				t.Error("i-good should be resolved")
			}
		default:
			t.Errorf("unexpected instance %q", a.Name)
		}
	}
}
