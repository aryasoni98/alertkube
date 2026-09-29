// Package azure polls Azure APIs and emits cloud-resource alerts into the same
// pipeline as the in-cluster Kubernetes watchers. It implements one
// sources.Source per Azure service, each gated by its own config toggle:
//
//   - AKS     - managed-cluster and node-pool health
//   - Monitor - fired Azure Monitor alerts (Alerts Management)
//   - VMs     - virtual-machine provisioning health
//   - Storage - storage-account availability
//   - SQL     - SQL Database health (Suspect/Offline/Inaccessible/
//     EmergencyMode/Shutdown)
//   - Redis   - Azure Cache for Redis provisioning health (Failed; scale-failure
//     recovery is a warning)
//
// Credentials resolve via the standard Azure chain (DefaultAzureCredential):
// AKS Workload Identity in-cluster, env/CLI locally. Azure is
// subscription-scoped, so the provider builds one client set per configured
// subscription. Each source polls through a per-subscription list function
// (see sources.Scoped), so it unit-tests against canned responses without the
// SDK or live credentials.
package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertsmanagement/armalertsmanagement"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const provider = "azure"

// init self-registers the Azure provider (see sources.RegisterProvider).
func init() {
	sources.RegisterProvider(sources.Provider{
		Name: provider,
		Bind: func(c *config.Config) sources.Bound {
			section := c.Azure
			return sources.Bound{
				Enabled:     section.Enabled,
				PollSeconds: section.PollSeconds,
				Build: func(ctx context.Context) ([]sources.Source, error) {
					return buildSources(ctx, section)
				},
			}
		},
	})
}

// buildSources builds the enabled Azure sources, one client set per configured
// subscription. It returns an error only if the credential chain or a client
// cannot be constructed, which is rare (DefaultAzureCredential defers most
// failures, e.g. an invalid AZURE_TOKEN_CREDENTIALS is one that is not); the
// caller logs it and continues without Azure so a cloud-auth problem never
// takes down the Kubernetes watchers. Missing or invalid credentials normally
// surface at the first token request, as alertkube_cloud_poll_errors_total on
// every poll.
func buildSources(ctx context.Context, cfg config.Azure) ([]sources.Source, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("azure: default credential: %w", err)
	}
	// One entry per service: its config toggle, the per-subscription lister
	// constructor, and the Source that owns the resulting listers. A disabled
	// service builds nil and Compact drops it, so adding a service means adding
	// one entry here and nothing else.
	subs := cfg.Subscriptions
	return sources.BuildAll(
		buildSub(cfg.AKS, subs, func(sub string) (func(context.Context) ([]*armcontainerservice.ManagedCluster, error), error) {
			client, err := armcontainerservice.NewManagedClustersClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("managed-clusters", sub, err)
			}
			return (&armAKSLister{client: client}).List, nil
		}, newAKSSource),

		buildSub(cfg.Monitor, subs, func(sub string) (func(context.Context) ([]*armalertsmanagement.Alert, error), error) {
			client, err := armalertsmanagement.NewAlertsClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("alerts", sub, err)
			}
			return (&armAlertsLister{client: client}).List, nil
		}, newAzureMonitorSource),

		buildSub(cfg.VMs, subs, func(sub string) (func(context.Context) ([]*armcompute.VirtualMachine, error), error) {
			client, err := armcompute.NewVirtualMachinesClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("virtual-machines", sub, err)
			}
			return (&armVMLister{client: client}).List, nil
		}, newAzureVMSource),

		buildSub(cfg.Storage, subs, func(sub string) (func(context.Context) ([]*armstorage.Account, error), error) {
			client, err := armstorage.NewAccountsClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("storage-accounts", sub, err)
			}
			return (&armStorageLister{client: client}).List, nil
		}, newAzureStorageSource),

		buildSub(cfg.SQL, subs, func(sub string) (func(context.Context) ([]sqlDatabase, error), error) {
			servers, err := armsql.NewServersClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("sql servers", sub, err)
			}
			databases, err := armsql.NewDatabasesClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("sql databases", sub, err)
			}
			return (&armSQLLister{servers: servers, databases: databases}).List, nil
		}, newAzureSQLSource),

		buildSub(cfg.Redis, subs, func(sub string) (func(context.Context) ([]*armredis.ResourceInfo, error), error) {
			client, err := armredis.NewClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("redis", sub, err)
			}
			return (&armRedisLister{client: client}).List, nil
		}, newAzureRedisSource),
	)
}

// emitFiring publishes a firing cloud alert. Identity is (kind, scope, name)
// where scope is "subscription/region" (Monitor alerts use the bare
// subscription); a resolve targets exactly that resource.
func emitFiring(emit sources.Emit, k alert.Kind, scope, name, reason, summary string, sev alert.Severity, details map[string]string) {
	sources.EmitFiring(emit, k, scope, name, reason, summary, sev,
		map[string]string{"provider": provider}, details)
}

func emitResolve(emit sources.Emit, k alert.Kind, scope, name string) {
	sources.EmitResolve(emit, k, scope, name)
}

func pollErr(source, scope string, err error) {
	sources.PollErr(source, scope, err)
}

// strVal dereferences a *string, returning "" for nil. ARM response fields are
// overwhelmingly *string.
func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
