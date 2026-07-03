package azure

import (
	"context"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceAzureSQL = "azure-sql"

// sqlDatabase is the SDK-agnostic view the source evaluates. The two-step
// enumeration (servers -> databases per server) is folded into the adapter so
// the source logic and its tests stay simple.
type sqlDatabase struct {
	server   string
	name     string
	location string
	status   string
}

// sqlLister lists every SQL database across all servers in one subscription.
// The real adapter drains both pagers; tests provide a fake returning a slice.
type sqlLister interface {
	List(ctx context.Context) ([]sqlDatabase, error)
}

// armSQLLister drains the servers pager, then the databases pager per server,
// resolving each server's resource group from its ARM resource ID.
type armSQLLister struct {
	servers   *armsql.ServersClient
	databases *armsql.DatabasesClient
}

func (l *armSQLLister) List(ctx context.Context) ([]sqlDatabase, error) {
	servers, err := drainPager(ctx, l.servers.NewListPager(nil),
		func(r armsql.ServersClientListResponse) []*armsql.Server { return r.Value })
	if err != nil {
		return nil, err
	}
	var dbs []sqlDatabase
	for _, srv := range servers {
		if srv == nil || srv.Name == nil || srv.ID == nil {
			continue
		}
		rg := resourceGroupFromID(*srv.ID)
		if rg == "" {
			continue
		}
		loc := strVal(srv.Location)
		serverDBs, err := drainPager(ctx, l.databases.NewListByServerPager(rg, *srv.Name, nil),
			func(r armsql.DatabasesClientListByServerResponse) []*armsql.Database { return r.Value })
		if err != nil {
			return nil, err
		}
		for _, db := range serverDBs {
			if db == nil || db.Name == nil {
				continue
			}
			status := ""
			if db.Properties != nil && db.Properties.Status != nil {
				status = string(*db.Properties.Status)
			}
			dbs = append(dbs, sqlDatabase{server: *srv.Name, name: *db.Name, location: loc, status: status})
		}
	}
	return dbs, nil
}

// resourceGroupFromID extracts the resourceGroups segment from an ARM resource
// ID (case-insensitively). Returns "" if the ID is not in the expected shape.
func resourceGroupFromID(id string) string {
	parts := strings.Split(id, "/")
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			return parts[i+1]
		}
	}
	return ""
}

type azureSQLSubscription = subLister[sqlLister]

// azureSQLSource alerts on Azure SQL databases in an unhealthy status - the
// Azure analog of the AWS RDS source. Suspect / Offline / Inaccessible /
// EmergencyMode / Shutdown are critical; Online plus transient states
// (Restoring, Recovering, Resuming, Scaling, Pausing, Creating, Copying) and the
// deliberately-Paused serverless state resolve, so routine operations and
// by-design auto-pause never page.
type azureSQLSource struct {
	subs []azureSQLSubscription
}

func (s *azureSQLSource) Name() string { return sourceAzureSQL }

func (s *azureSQLSource) Poll(ctx context.Context, emit sources.Emit) {
	pollBySubscription(ctx, sourceAzureSQL, s.subs, emit, evaluateSQLDatabase)
}

func evaluateSQLDatabase(subscription string, db sqlDatabase, emit sources.Emit) {
	if db.name == "" {
		return
	}
	scope := sources.Scope(subscription, db.location)
	id := db.server + "/" + db.name
	if !sqlStatusCritical(db.status) {
		emitResolve(emit, alert.KindAzureSQLDatabase, scope, id)
		return
	}
	emitFiring(emit, alert.KindAzureSQLDatabase, scope, id, "AzureSQLDatabaseUnhealthy",
		"Azure SQL database "+id+" status is "+db.status, alert.SeverityCritical,
		map[string]string{"status": db.status, "server": db.server})
}

// sqlStatusCritical reports whether a database status is a hard failure. Paused
// is intentional for serverless databases (auto-pause) and is treated as healthy
// to avoid paging on by-design idle.
func sqlStatusCritical(status string) bool {
	switch status {
	case string(armsql.DatabaseStatusSuspect),
		string(armsql.DatabaseStatusOffline),
		string(armsql.DatabaseStatusInaccessible),
		string(armsql.DatabaseStatusEmergencyMode),
		string(armsql.DatabaseStatusShutdown):
		return true
	default:
		return false
	}
}
