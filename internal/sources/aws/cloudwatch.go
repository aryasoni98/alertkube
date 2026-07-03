package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceCloudWatch = "aws-cloudwatch"

type cwRegion = regionClient[cloudwatchAPI]

// cloudWatchSource turns CloudWatch alarms into alerts. An alarm in ALARM
// state fires; OK or INSUFFICIENT_DATA resolves. Because CloudWatch alarms
// front EC2, ALB/NLB, RDS, and custom metrics alike, this single source covers
// the bulk of AWS infrastructure threshold alerting (CPU/memory/disk/target-
// group health/etc.) without a bespoke collector per service. Per-alarm
// severity and routing are expressed with the existing severityOverrides and
// routing config, which match on the alarm name and region.
type cloudWatchSource struct {
	regions []cwRegion
}

func (s *cloudWatchSource) Name() string { return sourceCloudWatch }

func (s *cloudWatchSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByRegion(ctx, s.regions, emit, s.pollRegion)
}

func (s *cloudWatchSource) pollRegion(ctx context.Context, rc cwRegion, emit sources.Emit) {
	forEachPage(ctx, sourceCloudWatch, rc.region, func(ctx context.Context, token *string) (*string, error) {
		out, err := rc.client.DescribeAlarms(ctx, &cloudwatch.DescribeAlarmsInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for i := range out.MetricAlarms {
			a := out.MetricAlarms[i]
			evaluateAlarm(rc.region, awssdk.ToString(a.AlarmName), a.StateValue, awssdk.ToString(a.StateReason),
				map[string]string{
					"type":       "metric",
					"metric":     awssdk.ToString(a.MetricName),
					"awsService": awssdk.ToString(a.Namespace),
				}, emit)
		}
		for i := range out.CompositeAlarms {
			a := out.CompositeAlarms[i]
			evaluateAlarm(rc.region, awssdk.ToString(a.AlarmName), a.StateValue, awssdk.ToString(a.StateReason),
				map[string]string{"type": "composite"}, emit)
		}
		return out.NextToken, nil
	})
}

// evaluateAlarm fires on ALARM and resolves on OK / INSUFFICIENT_DATA. Severity
// defaults to warning; operators raise specific alarms to critical via
// severityOverrides (match on name/region) rather than guessing here.
func evaluateAlarm(region, name string, state cwtypes.StateValue, stateReason string, details map[string]string, emit sources.Emit) {
	if name == "" {
		return
	}
	if state == cwtypes.StateValueAlarm {
		details["stateReason"] = stateReason
		emitFiring(emit, alert.KindCloudWatchAlarm, region, name, "CloudWatchAlarm",
			"CloudWatch alarm "+name+" is in ALARM: "+stateReason, alert.SeverityWarning, details)
		return
	}
	emitResolve(emit, alert.KindCloudWatchAlarm, region, name)
}
