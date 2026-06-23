package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"alertkube/internal/alert"
)

type fakeNAT struct {
	pages []*ec2.DescribeNatGatewaysOutput
	idx   int
	err   error
}

func (f *fakeNAT) DescribeNatGateways(_ context.Context, _ *ec2.DescribeNatGatewaysInput, _ ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.pages[f.idx]
	if f.idx < len(f.pages)-1 {
		f.idx++
	}
	return out, nil
}

func natGW(id string, state ec2types.NatGatewayState) ec2types.NatGateway {
	return ec2types.NatGateway{
		NatGatewayId: awssdk.String(id),
		State:        state,
		VpcId:        awssdk.String("vpc-1"),
		SubnetId:     awssdk.String("subnet-1"),
	}
}

func TestEvaluateNatGateway(t *testing.T) {
	cases := []struct {
		name         string
		gw           ec2types.NatGateway
		wantEmit     bool
		wantResolved bool
	}{
		{"failed critical", natGW("n", ec2types.NatGatewayStateFailed), true, false},
		{"available resolves", natGW("n", ec2types.NatGatewayStateAvailable), true, true},
		{"pending resolves", natGW("n", ec2types.NatGatewayStatePending), true, true},
		{"deleting resolves", natGW("n", ec2types.NatGatewayStateDeleting), true, true},
		{"empty id skipped", natGW("", ec2types.NatGatewayStateFailed), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateNatGateway("us-east-1", tc.gw, emit)
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
			if a.Kind != alert.KindNATGateway {
				t.Errorf("kind = %s, want NATGateway", a.Kind)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && a.Severity != alert.SeverityCritical {
				t.Errorf("severity = %q, want critical", a.Severity)
			}
		})
	}
}

func TestNATSourcePoll(t *testing.T) {
	fake := &fakeNAT{pages: []*ec2.DescribeNatGatewaysOutput{{
		NatGateways: []ec2types.NatGateway{
			natGW("good", ec2types.NatGatewayStateAvailable),
			natGW("bad", ec2types.NatGatewayStateFailed),
		},
	}}}
	src := &natSource{regions: []natRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}
