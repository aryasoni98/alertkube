package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceAurora = "aws-aurora"

type auroraRegion = regionClient[auroraAPI]

// auroraSource alerts on Aurora DB clusters whose status is not healthy. It is
// distinct from rdsSource (which watches individual DB instances via
// DescribeDBInstances): a cluster can be unhealthy - failed, storage-failure,
// inaccessible-encryption-credentials - while its member instances report fine,
// and vice versa, so the cluster status is a separate signal. auroraSeverity
// classifies the known bad cluster states as critical, a deliberately "stopped"
// cluster as a warning, and "available" plus transient operational states
// (backing-up, modifying, failing-over, migrating, ...) as healthy so routine
// operations never page. Clusters paginate with Marker like RDS instances.
type auroraSource struct {
	regions []auroraRegion
}

func (s *auroraSource) Name() string { return sourceAurora }

func (s *auroraSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByRegion(ctx, s.regions, emit, s.pollRegion)
}

func (s *auroraSource) pollRegion(ctx context.Context, rc auroraRegion, emit sources.Emit) {
	forEachPage(ctx, sourceAurora, rc.region, func(ctx context.Context, marker *string) (*string, error) {
		out, err := rc.client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{Marker: marker})
		if err != nil {
			return nil, err
		}
		for i := range out.DBClusters {
			evaluateDBCluster(rc.region, out.DBClusters[i], emit)
		}
		return out.Marker, nil
	})
}

func evaluateDBCluster(region string, c rdstypes.DBCluster, emit sources.Emit) {
	id := awssdk.ToString(c.DBClusterIdentifier)
	if id == "" {
		return
	}
	status := awssdk.ToString(c.Status)
	sev, firing := auroraSeverity(status)
	if !firing {
		emitResolve(emit, alert.KindAuroraCluster, region, id)
		return
	}
	emitFiring(emit, alert.KindAuroraCluster, region, id, "AuroraClusterUnhealthy",
		"Aurora cluster "+id+" status is "+status, sev,
		map[string]string{"status": status, "engine": awssdk.ToString(c.Engine)})
}

// auroraCriticalStatuses are the cluster states that page immediately; see
// dbStatusSeverity for the shared stopped/healthy classification.
var auroraCriticalStatuses = map[string]bool{
	"failed":                              true,
	"storage-failure":                     true,
	"inaccessible-encryption-credentials": true,
	"incompatible-parameters":             true,
	"incompatible-network":                true,
	"migration-failed":                    true,
}

// auroraSeverity classifies an Aurora cluster status string. The bool reports
// whether the status is firing (true) or healthy/benign-transient (false).
func auroraSeverity(status string) (alert.Severity, bool) {
	return dbStatusSeverity(status, auroraCriticalStatuses)
}
