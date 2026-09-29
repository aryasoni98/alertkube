package gcp

import (
	"context"
	"fmt"

	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/sources"
)

const sourceCloudSQL = "gcp-cloudsql"

// apiSQLLister lists Cloud SQL instances in one project by paging through the
// SQL Admin API; tests provide a fake.
type apiSQLLister struct {
	svc *sqladmin.Service
}

// List pages through one project's instances. The token loop is hand-rolled,
// so it carries the same guards as the AWS and Azure pagers: a page token that
// does not advance or the sources.MaxPages runaway guard stops it, records the
// truncation, and returns the instances fetched so far so they are still
// evaluated.
func (l *apiSQLLister) List(ctx context.Context, project string) ([]*sqladmin.DatabaseInstance, error) {
	var out []*sqladmin.DatabaseInstance
	call := l.svc.Instances.List(project)
	token := ""
	for range sources.MaxPages {
		resp, err := call.Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		out = append(out, resp.Items...)
		if resp.NextPageToken == "" {
			return out, nil
		}
		if resp.NextPageToken == token {
			sources.PollTruncated(sourceCloudSQL, project, "page token did not advance")
			return out, nil
		}
		token = resp.NextPageToken
		call = l.svc.Instances.List(project).PageToken(token)
	}
	sources.PollTruncated(sourceCloudSQL, project, fmt.Sprintf("hit the %d-page runaway guard", sources.MaxPages))
	return out, nil
}

// newCloudSQLSource alerts on Cloud SQL instance state across every
// configured project.
func newCloudSQLSource(projects []string, list func(ctx context.Context, project string) ([]*sqladmin.DatabaseInstance, error)) sources.Source {
	return sources.NewListSource(sourceCloudSQL, perProject(projects, list), evaluateCloudSQL)
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
