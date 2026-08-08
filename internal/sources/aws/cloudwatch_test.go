package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/aryasoni98/alertkube/internal/alert"
)

type fakeCW struct {
	pages []*cloudwatch.DescribeAlarmsOutput
	idx   int
	err   error
}

func (f *fakeCW) DescribeAlarms(_ context.Context, _ *cloudwatch.DescribeAlarmsInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.DescribeAlarmsOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.pages[f.idx]
	if f.idx < len(f.pages)-1 {
		f.idx++
	}
	return out, nil
}

func metricAlarm(name string, state cwtypes.StateValue) cwtypes.MetricAlarm {
	return cwtypes.MetricAlarm{
		AlarmName:   awssdk.String(name),
		StateValue:  state,
		StateReason: awssdk.String("threshold crossed"),
		MetricName:  awssdk.String("CPUUtilization"),
		Namespace:   awssdk.String("AWS/EC2"),
	}
}

func TestEvaluateAlarm(t *testing.T) {
	cases := []struct {
		name         string
		state        cwtypes.StateValue
		wantResolved bool
	}{
		{"alarm fires", cwtypes.StateValueAlarm, false},
		{"ok resolves", cwtypes.StateValueOk, true},
		{"insufficient-data resolves", cwtypes.StateValueInsufficientData, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateAlarm("us-east-1", "cpu-high", tc.state, "reason", map[string]string{"type": "metric"}, emit)
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindCloudWatchAlarm || a.Name != "cpu-high" || a.Namespace != "us-east-1" {
				t.Errorf("identity wrong: kind=%s name=%s ns=%s", a.Kind, a.Name, a.Namespace)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved {
				if a.Severity != alert.SeverityWarning {
					t.Errorf("severity = %q, want warning", a.Severity)
				}
				if a.Reason != "CloudWatchAlarm" {
					t.Errorf("reason = %q, want CloudWatchAlarm", a.Reason)
				}
				if a.Details["stateReason"] != "reason" {
					t.Errorf("stateReason detail = %q", a.Details["stateReason"])
				}
			}
		})
	}
}

func TestCloudWatchSourcePollPaginatedAndComposite(t *testing.T) {
	page1 := &cloudwatch.DescribeAlarmsOutput{
		MetricAlarms: []cwtypes.MetricAlarm{
			metricAlarm("cpu-high", cwtypes.StateValueAlarm),
			metricAlarm("cpu-ok", cwtypes.StateValueOk),
		},
		NextToken: awssdk.String("more"),
	}
	page2 := &cloudwatch.DescribeAlarmsOutput{
		CompositeAlarms: []cwtypes.CompositeAlarm{
			{AlarmName: awssdk.String("svc-down"), StateValue: cwtypes.StateValueAlarm, StateReason: awssdk.String("children alarming")},
		},
	}
	fake := &fakeCW{pages: []*cloudwatch.DescribeAlarmsOutput{page1, page2}}
	src := &cloudWatchSource{regions: []cwRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if len(*got) != 3 {
		t.Fatalf("expected 3 alerts across 2 pages, got %d", len(*got))
	}
	firing := map[string]bool{}
	resolved := map[string]bool{}
	for _, a := range *got {
		if a.Resolved {
			resolved[a.Name] = true
		} else {
			firing[a.Name] = true
		}
	}
	if !firing["cpu-high"] || !firing["svc-down"] {
		t.Errorf("expected cpu-high and svc-down firing, got %v", firing)
	}
	if !resolved["cpu-ok"] {
		t.Errorf("expected cpu-ok resolved, got %v", resolved)
	}
}
