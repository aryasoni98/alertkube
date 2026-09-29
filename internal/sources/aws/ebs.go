package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const sourceEBS = "aws-ebs"

// ebsAPI is the subset of the EC2 client the EBS volume-status source uses.
type ebsAPI interface {
	DescribeVolumeStatus(context.Context, *ec2.DescribeVolumeStatusInput, ...func(*ec2.Options)) (*ec2.DescribeVolumeStatusOutput, error)
}

type ebsRegion = regionClient[ebsAPI]

// ebsSource alerts on EBS volumes whose status check is impaired - the storage
// analog of the EC2 status-check source. DescribeVolumeStatus reports a rolled-
// up VolumeStatus.Status of ok / impaired / insufficient-data: "impaired" is a
// definite failure (critical), "insufficient-data" means the volume is
// unreachable for status reporting (warning), and "ok" resolves. A volume the
// API has not yet evaluated (nil status) is treated as ok so freshly-created
// volumes do not page.
type ebsSource struct {
	regions []ebsRegion
}

func (s *ebsSource) Name() string { return sourceEBS }

func (s *ebsSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByRegion(ctx, sourceEBS, s.regions, emit, s.pollRegion)
}

func (s *ebsSource) pollRegion(ctx context.Context, rc ebsRegion, emit sources.Emit) {
	forEachPage(ctx, sourceEBS, rc.region, func(ctx context.Context, token *string) (*string, error) {
		out, err := rc.client.DescribeVolumeStatus(ctx, &ec2.DescribeVolumeStatusInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for i := range out.VolumeStatuses {
			evaluateVolume(rc.region, out.VolumeStatuses[i], emit)
		}
		return out.NextToken, nil
	})
}

func evaluateVolume(region string, vs ec2types.VolumeStatusItem, emit sources.Emit) {
	id := awssdk.ToString(vs.VolumeId)
	if id == "" {
		return
	}
	status := ec2types.VolumeStatusInfoStatusOk
	if vs.VolumeStatus != nil {
		status = vs.VolumeStatus.Status
	}
	details := map[string]string{"az": awssdk.ToString(vs.AvailabilityZone), "status": string(status)}
	switch status {
	case ec2types.VolumeStatusInfoStatusImpaired:
		emitFiring(emit, alert.KindEBSVolume, region, id, "EBSVolumeImpaired",
			"EBS volume "+id+" status check impaired", alert.SeverityCritical, details)
	case ec2types.VolumeStatusInfoStatusInsufficientData:
		emitFiring(emit, alert.KindEBSVolume, region, id, "EBSVolumeStatusUnknown",
			"EBS volume "+id+" status is insufficient-data", alert.SeverityWarning, details)
	default:
		emitResolve(emit, alert.KindEBSVolume, region, id)
	}
}
