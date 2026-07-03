package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceNAT = "aws-nat"

type natRegion = regionClient[natAPI]

// natSource alerts on NAT gateways in the Failed state - a failed NAT gateway
// silently breaks outbound connectivity for its subnet. Pending / deleting /
// deleted are transient lifecycle states (not failures) and resolve, so routine
// teardown never pages.
type natSource struct {
	regions []natRegion
}

func (s *natSource) Name() string { return sourceNAT }

func (s *natSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByRegion(ctx, s.regions, emit, s.pollRegion)
}

func (s *natSource) pollRegion(ctx context.Context, rc natRegion, emit sources.Emit) {
	forEachPage(ctx, sourceNAT, rc.region, func(ctx context.Context, token *string) (*string, error) {
		out, err := rc.client.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for i := range out.NatGateways {
			evaluateNatGateway(rc.region, out.NatGateways[i], emit)
		}
		return out.NextToken, nil
	})
}

func evaluateNatGateway(region string, ng ec2types.NatGateway, emit sources.Emit) {
	id := awssdk.ToString(ng.NatGatewayId)
	if id == "" {
		return
	}
	if ng.State == ec2types.NatGatewayStateFailed {
		emitFiring(emit, alert.KindNATGateway, region, id, "NATGatewayFailed",
			"NAT gateway "+id+" is in failed state", alert.SeverityCritical,
			map[string]string{
				"state":          string(ng.State),
				"vpcId":          awssdk.ToString(ng.VpcId),
				"subnetId":       awssdk.ToString(ng.SubnetId),
				"failureCode":    awssdk.ToString(ng.FailureCode),
				"failureMessage": awssdk.ToString(ng.FailureMessage),
			})
		return
	}
	emitResolve(emit, alert.KindNATGateway, region, id)
}
