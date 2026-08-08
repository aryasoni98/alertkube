package aws

import (
	"context"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const sourceEC2 = "aws-ec2"

type ec2Region = regionClient[ec2API]

// ec2Source alerts on EC2 instances whose system or instance status check is
// impaired - the brief's "EC2 Status Check Alerts". An impaired check fires
// critical; an instance with neither check impaired resolves. DescribeInstance-
// Status returns only running instances by default, which is what we want:
// a stopped instance has no status checks to fail.
type ec2Source struct {
	regions []ec2Region
}

func (s *ec2Source) Name() string { return sourceEC2 }

func (s *ec2Source) Poll(ctx context.Context, emit sources.Emit) {
	pollByRegion(ctx, s.regions, emit, s.pollRegion)
}

func (s *ec2Source) pollRegion(ctx context.Context, rc ec2Region, emit sources.Emit) {
	forEachPage(ctx, sourceEC2, rc.region, func(ctx context.Context, token *string) (*string, error) {
		out, err := rc.client.DescribeInstanceStatus(ctx, &ec2.DescribeInstanceStatusInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for i := range out.InstanceStatuses {
			evaluateInstance(rc.region, out.InstanceStatuses[i], emit)
		}
		return out.NextToken, nil
	})
}

// evaluateInstance fires when either the system or instance status check is
// impaired, and resolves otherwise. "impaired" is the only definitively-bad
// SummaryStatus; initializing/insufficient-data/not-applicable are treated as
// not-firing to avoid paging on instances that are merely booting.
func evaluateInstance(region string, st ec2types.InstanceStatus, emit sources.Emit) {
	id := awssdk.ToString(st.InstanceId)
	if id == "" {
		return
	}
	sys := summaryStatus(st.SystemStatus)
	inst := summaryStatus(st.InstanceStatus)
	var impaired []string
	if sys == ec2types.SummaryStatusImpaired {
		impaired = append(impaired, "system")
	}
	if inst == ec2types.SummaryStatusImpaired {
		impaired = append(impaired, "instance")
	}
	if len(impaired) > 0 {
		emitFiring(emit, alert.KindEC2Instance, region, id, "EC2StatusCheckFailed",
			"EC2 instance "+id+" status check impaired: "+strings.Join(impaired, ", "),
			alert.SeverityCritical,
			map[string]string{
				"az":             awssdk.ToString(st.AvailabilityZone),
				"systemStatus":   string(sys),
				"instanceStatus": string(inst),
			})
		return
	}
	emitResolve(emit, alert.KindEC2Instance, region, id)
}

// summaryStatus reads a status-check summary, treating a missing summary as
// not-applicable (the SDK leaves it nil for instances without checks yet).
func summaryStatus(s *ec2types.InstanceStatusSummary) ec2types.SummaryStatus {
	if s == nil {
		return ec2types.SummaryStatusNotApplicable
	}
	return s.Status
}
