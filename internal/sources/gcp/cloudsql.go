package gcp

import (
	"context"

	sqladmin "google.golang.org/api/sqladmin/v1"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceCloudSQL = "gcp-cloudsql"

// sqlLister lists Cloud SQL instances in one project. The real adapter pages
// through the SQL Admin API; tests provide a fake.
type sqlLister interface {
	List(ctx context.Context, project string) ([]*sqladmin.DatabaseInstance, error)
}

type apiSQLLister struct {
	svc *sqladmin.Service
}

func (l *apiSQLLister) List(ctx context.Context, project string) ([]*sqladmin.DatabaseInstance, error) {
	var out []*sqladmin.DatabaseInstance
	call := l.svc.Instances.List(project)
	for {
		resp, err := call.Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		out = append(out, resp.Items...)
		if resp.NextPageToken == "" {
			return out, nil
		}
		call = l.svc.Instances.List(project).PageToken(resp.NextPageToken)
	}
}

type cloudSQLSource struct {
	projects []string
	lister   sqlLister
}

func (s *cloudSQLSource) Name() string { return sourceCloudSQL }

func (s *cloudSQLSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByProject(ctx, sourceCloudSQL, s.projects, s.lister, emit, evaluateCloudSQL)
}

// evaluateCloudSQL maps a Cloud SQL instance's state onto a firing/resolve.
// FAILED/SUSPENDED are critical; PENDING_CREATE/MAINTENANCE/other are warnings;
// RUNNABLE resolves. Parallel to the AWS RDS source.
func evaluateCloudSQL(project string, in *sqladmin.DatabaseInstance, emit sources.Emit) {
	if in == nil || in.Name == "" {
		return
	}
	scope := sources.Scope(project, in.Region)
	details := map[string]string{"state": in.State, "databaseVersion": in.DatabaseVersion, "region": in.Region}
	switch in.State {
	case "RUNNABLE":
		emitResolve(emit, alert.KindCloudSQLInstance, scope, in.Name)
	case "FAILED", "SUSPENDED":
		emitFiring(emit, alert.KindCloudSQLInstance, scope, in.Name, "CloudSQLInstanceUnhealthy",
			"Cloud SQL instance "+in.Name+" state is "+in.State, alert.SeverityCritical, details)
	default:
		emitFiring(emit, alert.KindCloudSQLInstance, scope, in.Name, "CloudSQLInstanceNotRunnable",
			"Cloud SQL instance "+in.Name+" state is "+in.State, alert.SeverityWarning, details)
	}
}
