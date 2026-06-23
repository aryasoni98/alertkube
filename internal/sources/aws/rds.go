package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceRDS = "aws-rds"

type rdsRegion struct {
	region string
	client rdsAPI
}

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
	for _, rc := range s.regions {
		s.pollRegion(ctx, rc, emit)
	}
}

func (s *rdsSource) pollRegion(ctx context.Context, rc rdsRegion, emit sources.Emit) {
	var marker *string
	for {
		out, err := rc.client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{Marker: marker})
		if err != nil {
			pollErr(sourceRDS, rc.region, err)
			return
		}
		for i := range out.DBInstances {
			evaluateDBInstance(rc.region, out.DBInstances[i], emit)
		}
		if out.Marker == nil || *out.Marker == "" {
			return
		}
		marker = out.Marker
	}
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

// rdsSeverity classifies an RDS status string. The bool reports whether the
// status is firing (true) or healthy/benign-transient (false → resolve).
func rdsSeverity(status string) (alert.Severity, bool) {
	switch status {
	case "failed",
		"storage-full",
		"incompatible-restore",
		"incompatible-network",
		"incompatible-parameters",
		"incompatible-option-group",
		"restore-error",
		"inaccessible-encryption-credentials":
		return alert.SeverityCritical, true
	case "stopped":
		return alert.SeverityWarning, true
	default:
		// "available" plus transient operational states (backing-up,
		// modifying, rebooting, starting, stopping, maintenance, upgrading,
		// creating, ...) are treated as not-firing.
		return alert.SeverityInfo, false
	}
}
