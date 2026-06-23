package aws

import (
	"context"
	"strconv"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	astypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceASG = "aws-asg"

type asgRegion struct {
	region string
	client autoscalingAPI
}

// asgSource alerts on Auto Scaling Groups whose healthy in-service capacity is
// below the desired count: zero healthy is critical (no serving capacity), a
// partial shortfall is a warning. Meeting desired capacity resolves.
type asgSource struct {
	regions []asgRegion
}

func (s *asgSource) Name() string { return sourceASG }

func (s *asgSource) Poll(ctx context.Context, emit sources.Emit) {
	for _, rc := range s.regions {
		s.pollRegion(ctx, rc, emit)
	}
}

func (s *asgSource) pollRegion(ctx context.Context, rc asgRegion, emit sources.Emit) {
	var token *string
	for {
		out, err := rc.client.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{NextToken: token})
		if err != nil {
			pollErr(sourceASG, rc.region, err)
			return
		}
		for i := range out.AutoScalingGroups {
			evaluateASG(rc.region, out.AutoScalingGroups[i], emit)
		}
		if out.NextToken == nil || *out.NextToken == "" {
			return
		}
		token = out.NextToken
	}
}

func evaluateASG(region string, g astypes.AutoScalingGroup, emit sources.Emit) {
	name := awssdk.ToString(g.AutoScalingGroupName)
	if name == "" {
		return
	}
	desired := int(awssdk.ToInt32(g.DesiredCapacity))
	healthy := 0
	for _, in := range g.Instances {
		if awssdk.ToString(in.HealthStatus) == "Healthy" && in.LifecycleState == astypes.LifecycleStateInService {
			healthy++
		}
	}
	details := map[string]string{
		"desired":          strconv.Itoa(desired),
		"healthyInService": strconv.Itoa(healthy),
	}
	switch {
	case desired > 0 && healthy == 0:
		emitFiring(emit, alert.KindASG, region, name, "ASGNoHealthyCapacity",
			"Auto Scaling group "+name+" has no healthy in-service instances", alert.SeverityCritical, details)
	case healthy < desired:
		emitFiring(emit, alert.KindASG, region, name, "ASGCapacityShortfall",
			"Auto Scaling group "+name+" has "+strconv.Itoa(healthy)+" healthy of "+strconv.Itoa(desired)+" desired", alert.SeverityWarning, details)
	default:
		emitResolve(emit, alert.KindASG, region, name)
	}
}
