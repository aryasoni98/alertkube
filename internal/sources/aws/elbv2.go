package aws

import (
	"context"
	"fmt"
	"strconv"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const sourceELBV2 = "aws-elbv2"

type elbv2Region = regionClient[elbv2API]

// elbv2Source alerts on Application/Network Load Balancers and their target
// groups - two failure classes the brief calls out separately: load-balancer
// availability (an LB not in the active state) and target-group health
// (registered backends failing their health checks). LB and TG are distinct
// alert kinds so a routing rule can page differently for "the load balancer is
// down" versus "some backends are unhealthy". ELBv2 paginates with Marker /
// NextMarker rather than NextToken.
type elbv2Source struct {
	regions []elbv2Region
}

func (s *elbv2Source) Name() string { return sourceELBV2 }

func (s *elbv2Source) Poll(ctx context.Context, emit sources.Emit) {
	pollByRegion(ctx, s.regions, emit, func(ctx context.Context, rc elbv2Region, emit sources.Emit) {
		s.pollLoadBalancers(ctx, rc, emit)
		s.pollTargetGroups(ctx, rc, emit)
	})
}

func (s *elbv2Source) pollLoadBalancers(ctx context.Context, rc elbv2Region, emit sources.Emit) {
	forEachPage(ctx, sourceELBV2, rc.region, func(ctx context.Context, marker *string) (*string, error) {
		out, err := rc.client.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{Marker: marker})
		if err != nil {
			return nil, err
		}
		for i := range out.LoadBalancers {
			evaluateLoadBalancer(rc.region, out.LoadBalancers[i], emit)
		}
		return out.NextMarker, nil
	})
}

// evaluateLoadBalancer maps one LB's provisioning state onto a single
// firing-or-resolve decision. active resolves; failed/active_impaired are
// critical; anything else (provisioning, or a future state) is a warning.
func evaluateLoadBalancer(region string, lb elbv2types.LoadBalancer, emit sources.Emit) {
	name := awssdk.ToString(lb.LoadBalancerName)
	if name == "" {
		return
	}
	code := elbv2types.LoadBalancerStateEnumActive
	reason := ""
	if lb.State != nil {
		code = lb.State.Code
		reason = awssdk.ToString(lb.State.Reason)
	}
	details := map[string]string{"lbType": string(lb.Type), "stateReason": reason}
	switch code {
	case elbv2types.LoadBalancerStateEnumActive:
		emitResolve(emit, alert.KindLoadBalancer, region, name)
	case elbv2types.LoadBalancerStateEnumFailed:
		emitFiring(emit, alert.KindLoadBalancer, region, name, "LoadBalancerFailed",
			"Load balancer "+name+" is in FAILED state", alert.SeverityCritical, details)
	case elbv2types.LoadBalancerStateEnumActiveImpaired:
		emitFiring(emit, alert.KindLoadBalancer, region, name, "LoadBalancerImpaired",
			"Load balancer "+name+" is active_impaired (some zones not functioning)", alert.SeverityCritical, details)
	default:
		emitFiring(emit, alert.KindLoadBalancer, region, name, "LoadBalancerNotActive",
			"Load balancer "+name+" is not active (state "+string(code)+")", alert.SeverityWarning, details)
	}
}

func (s *elbv2Source) pollTargetGroups(ctx context.Context, rc elbv2Region, emit sources.Emit) {
	forEachPage(ctx, sourceELBV2, rc.region, func(ctx context.Context, marker *string) (*string, error) {
		out, err := rc.client.DescribeTargetGroups(ctx, &elbv2.DescribeTargetGroupsInput{Marker: marker})
		if err != nil {
			return nil, err
		}
		for i := range out.TargetGroups {
			tg := out.TargetGroups[i]
			name := awssdk.ToString(tg.TargetGroupName)
			if name == "" {
				continue
			}
			h, err := rc.client.DescribeTargetHealth(ctx, &elbv2.DescribeTargetHealthInput{TargetGroupArn: tg.TargetGroupArn})
			if err != nil {
				pollErr(sourceELBV2, rc.region, err)
				continue
			}
			evaluateTargetGroup(rc.region, name, h.TargetHealthDescriptions, emit)
		}
		return out.NextMarker, nil
	})
}

// evaluateTargetGroup classifies a target group by its registered-target
// health: zero healthy with unhealthy present is critical (no backends
// serving), a partial shortfall is a warning, and otherwise it resolves. An
// empty target group (no registered targets) resolves rather than alarming -
// an empty group is usually intentional, not an outage.
func evaluateTargetGroup(region, name string, descs []elbv2types.TargetHealthDescription, emit sources.Emit) {
	var healthy, unhealthy int
	for _, d := range descs {
		if d.TargetHealth == nil {
			continue
		}
		switch d.TargetHealth.State {
		case elbv2types.TargetHealthStateEnumHealthy:
			healthy++
		case elbv2types.TargetHealthStateEnumUnhealthy:
			unhealthy++
		}
	}
	details := map[string]string{
		"healthy":   strconv.Itoa(healthy),
		"unhealthy": strconv.Itoa(unhealthy),
		"total":     strconv.Itoa(len(descs)),
	}
	switch {
	case unhealthy > 0 && healthy == 0:
		emitFiring(emit, alert.KindTargetGroup, region, name, "TargetGroupNoHealthyTargets",
			fmt.Sprintf("Target group %s has no healthy targets (%d unhealthy)", name, unhealthy),
			alert.SeverityCritical, details)
	case unhealthy > 0:
		emitFiring(emit, alert.KindTargetGroup, region, name, "TargetGroupDegraded",
			fmt.Sprintf("Target group %s has %d unhealthy of %d targets", name, unhealthy, len(descs)),
			alert.SeverityWarning, details)
	default:
		emitResolve(emit, alert.KindTargetGroup, region, name)
	}
}
