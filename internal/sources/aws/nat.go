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

type natRegion struct {
	region string
	client natAPI
}

// natSource alerts on NAT gateways in the Failed state - a failed NAT gateway
// silently breaks outbound connectivity for its subnet. Pending / deleting /
// deleted are transient lifecycle states (not failures) and resolve, so routine
// teardown never pages.
type natSource struct {
	regions []natRegion
}

func (s *natSource) Name() string { return sourceNAT }

func (s *natSource) Poll(ctx context.Context, emit sources.Emit) {
	for _, rc := range s.regions {
		s.pollRegion(ctx, rc, emit)
	}
}

func (s *natSource) pollRegion(ctx context.Context, rc natRegion, emit sources.Emit) {
	var token *string
	for {
		out, err := rc.client.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{NextToken: token})
		if err != nil {
			pollErr(sourceNAT, rc.region, err)
			return
		}
		for i := range out.NatGateways {
			evaluateNatGateway(rc.region, out.NatGateways[i], emit)
		}
		if out.NextToken == nil || *out.NextToken == "" {
			return
		}
		token = out.NextToken
	}
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
