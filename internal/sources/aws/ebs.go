package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceEBS = "aws-ebs"

type ebsRegion struct {
	region string
	client ebsAPI
}

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
	for _, rc := range s.regions {
		s.pollRegion(ctx, rc, emit)
	}
}

func (s *ebsSource) pollRegion(ctx context.Context, rc ebsRegion, emit sources.Emit) {
	var token *string
	for {
		out, err := rc.client.DescribeVolumeStatus(ctx, &ec2.DescribeVolumeStatusInput{NextToken: token})
		if err != nil {
			pollErr(sourceEBS, rc.region, err)
			return
		}
		for i := range out.VolumeStatuses {
			evaluateVolume(rc.region, out.VolumeStatuses[i], emit)
		}
		if out.NextToken == nil || *out.NextToken == "" {
			return
		}
		token = out.NextToken
	}
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
