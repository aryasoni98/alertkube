package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const sourceEFS = "aws-efs"

type efsRegion = regionClient[efsAPI]

// efsSource alerts on EFS file systems in the "error" lifecycle state - the only
// definitively-bad LifeCycleState. "available" plus the transient states
// (creating, updating, deleting) resolve, so provisioning and teardown never
// page. EFS paginates with Marker on the request and NextMarker on the response.
type efsSource struct {
	regions []efsRegion
}

func (s *efsSource) Name() string { return sourceEFS }

func (s *efsSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByRegion(ctx, s.regions, emit, s.pollRegion)
}

func (s *efsSource) pollRegion(ctx context.Context, rc efsRegion, emit sources.Emit) {
	forEachPage(ctx, sourceEFS, rc.region, func(ctx context.Context, marker *string) (*string, error) {
		out, err := rc.client.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{Marker: marker})
		if err != nil {
			return nil, err
		}
		for i := range out.FileSystems {
			evaluateFileSystem(rc.region, out.FileSystems[i], emit)
		}
		return out.NextMarker, nil
	})
}

func evaluateFileSystem(region string, fs efstypes.FileSystemDescription, emit sources.Emit) {
	id := awssdk.ToString(fs.FileSystemId)
	if id == "" {
		return
	}
	if fs.LifeCycleState == efstypes.LifeCycleStateError {
		emitFiring(emit, alert.KindEFSFileSystem, region, id, "EFSFileSystemError",
			"EFS file system "+id+" is in error state", alert.SeverityCritical,
			map[string]string{"name": awssdk.ToString(fs.Name), "lifeCycleState": string(fs.LifeCycleState)})
		return
	}
	emitResolve(emit, alert.KindEFSFileSystem, region, id)
}
