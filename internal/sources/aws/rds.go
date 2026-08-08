package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const sourceRDS = "aws-rds"

type rdsRegion = regionClient[rdsAPI]

// rdsSource alerts on RDS DB instances whose status is not healthy. RDS reports
// status as a free-form string, so rdsSeverity classifies the known bad states
// (failed / storage-full / incompatible-* / restore-error / inaccessible-
// encryption-credentials) as critical, "stopped" as a warning (a stopped
// production DB is usually unintended), and everything else - "available" plus
// transient operational states like backing-up / modifying / rebooting - as
// healthy, so routine maintenance never pages. RDS paginates with Marker on
// both the request and the response.
type rdsSource struct {
	regions []rdsRegion
}

func (s *rdsSource) Name() string { return sourceRDS }

func (s *rdsSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByRegion(ctx, s.regions, emit, s.pollRegion)
}

func (s *rdsSource) pollRegion(ctx context.Context, rc rdsRegion, emit sources.Emit) {
	forEachPage(ctx, sourceRDS, rc.region, func(ctx context.Context, marker *string) (*string, error) {
		out, err := rc.client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{Marker: marker})
		if err != nil {
			return nil, err
		}
		for i := range out.DBInstances {
			evaluateDBInstance(rc.region, out.DBInstances[i], emit)
		}
		return out.Marker, nil
	})
}

func evaluateDBInstance(region string, db rdstypes.DBInstance, emit sources.Emit) {
	id := awssdk.ToString(db.DBInstanceIdentifier)
	if id == "" {
		return
	}
	status := awssdk.ToString(db.DBInstanceStatus)
	sev, firing := rdsSeverity(status)
	if !firing {
		emitResolve(emit, alert.KindRDSInstance, region, id)
		return
	}
	emitFiring(emit, alert.KindRDSInstance, region, id, "RDSInstanceUnhealthy",
		"RDS instance "+id+" status is "+status, sev,
		map[string]string{"status": status, "engine": awssdk.ToString(db.Engine)})
}

// rdsCriticalStatuses are the DB-instance states that page immediately; see
// dbStatusSeverity for the shared stopped/healthy classification.
var rdsCriticalStatuses = map[string]bool{
	"failed":                              true,
	"storage-full":                        true,
	"incompatible-restore":                true,
	"incompatible-network":                true,
	"incompatible-parameters":             true,
	"incompatible-option-group":           true,
	"restore-error":                       true,
	"inaccessible-encryption-credentials": true,
}

// rdsSeverity classifies an RDS status string. The bool reports whether the
// status is firing (true) or healthy/benign-transient (false → resolve).
func rdsSeverity(status string) (alert.Severity, bool) {
	return dbStatusSeverity(status, rdsCriticalStatuses)
}
