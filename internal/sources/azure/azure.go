// Package azure polls Azure APIs and emits cloud-resource alerts into the same
// pipeline as the in-cluster Kubernetes watchers. It implements one
// sources.Source per Azure service, each gated by its own config toggle:
//
//   - AKS     - managed-cluster health
//   - Monitor - fired Azure Monitor alerts (Alerts Management)
//   - VMs     - virtual-machine provisioning health
//   - Storage - storage-account availability
//   - SQL     - SQL Database health (Suspect/Offline/Inaccessible)
//   - Redis   - Azure Cache for Redis provisioning health
//
// Credentials resolve via the standard Azure chain (DefaultAzureCredential):
// AKS Workload Identity in-cluster, env/CLI locally. Azure is
// subscription-scoped, so the provider builds one client set per configured
// subscription. Each source declares a narrow interface (aksLister, ...) so it
// unit-tests against canned responses without the SDK or live credentials.
package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertsmanagement/armalertsmanagement"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/sources"
)

const provider = "azure"

// init self-registers the Azure provider (see sources.RegisterProvider).
func init() {
	sources.RegisterProvider(sources.Provider{
		Name:        provider,
		Enabled:     func(c *config.Config) bool { return c.Azure.Enabled },
		PollSeconds: func(c *config.Config) int { return c.Azure.PollSeconds },
		Build:       NewProvider,
	})
}

// drainPager collects every page of an ARM list pager into one slice. items
// extracts a page's Value slice; the per-service response types differ but all
// share this drain loop, so each arm*Lister adapter reduces to one call.
func drainPager[R, T any](ctx context.Context, pager *runtime.Pager[R], items func(R) []T) ([]T, error) {
	var out []T
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, items(page)...)
	}
	return out, nil
}

// subLister pairs a subscription id with the per-service lister scoped to it.
// Every source used to declare its own identical {subscription, lister} struct;
// they are now type aliases of this generic (see each source file), so the
// shared per-subscription fan-out (pollBySubscription) can operate over any.
type subLister[L any] struct {
	subscription string
	lister       L
}

// listerFor constrains a lister to one that returns items of type T.
type listerFor[T any] interface {
	List(ctx context.Context) ([]T, error)
}

// pollBySubscription lists items per subscription - recording a poll error and
// skipping that subscription on failure - then runs eval for each item. It
// replaces the identical list/pollErr/iterate loop every Azure source
// duplicated, so that shape lives in exactly one place.
func pollBySubscription[T any, L listerFor[T]](ctx context.Context, source string, subs []subLister[L], emit sources.Emit, eval func(subscription string, item T, emit sources.Emit)) {
	for _, sub := range subs {
		items, err := sub.lister.List(ctx)
		if err != nil {
			pollErr(source, sub.subscription, err)
			continue
		}
		for i := range items {
			eval(sub.subscription, items[i], emit)
		}
	}
}

// aksLister lists managed clusters in one subscription. The real adapter drains
// the SDK pager; tests provide a fake returning a canned slice.
type aksLister interface {
	List(ctx context.Context) ([]armcontainerservice.ManagedCluster, error)
}

// armAKSLister adapts the SDK ManagedClustersClient to aksLister by draining
// its List pager into a slice.
type armAKSLister struct {
	client *armcontainerservice.ManagedClustersClient
}

func (l *armAKSLister) List(ctx context.Context) ([]armcontainerservice.ManagedCluster, error) {
	ptrs, err := drainPager(ctx, l.client.NewListPager(nil),
		func(r armcontainerservice.ManagedClustersClientListResponse) []*armcontainerservice.ManagedCluster {
			return r.Value
		})
	if err != nil {
		return nil, err
	}
	out := make([]armcontainerservice.ManagedCluster, 0, len(ptrs))
	for _, c := range ptrs {
		if c != nil {
			out = append(out, *c)
		}
	}
	return out, nil
}

// NewProvider builds the enabled Azure sources, one client set per configured
// subscription. It returns an error if credentials cannot be resolved; the
// caller logs it and continues without Azure so a cloud-auth problem never
// takes down the Kubernetes watchers.
func NewProvider(ctx context.Context, cfg *config.Config) ([]sources.Source, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("azure: default credential: %w", err)
	}
	var aksSubs []aksSubscription
	var monSubs []azureMonitorSubscription
	var vmSubs []azureVMSubscription
	var storageSubs []azureStorageSubscription
	var sqlSubs []azureSQLSubscription
	var redisSubs []azureRedisSubscription
	for _, sub := range cfg.Azure.Subscriptions {
		if cfg.Azure.AKS {
			client, err := armcontainerservice.NewManagedClustersClient(sub, cred, nil)
			if err != nil {
				return nil, fmt.Errorf("azure: managed-clusters client for subscription %s: %w", sub, err)
			}
			aksSubs = append(aksSubs, aksSubscription{subscription: sub, lister: &armAKSLister{client: client}})
		}
		if cfg.Azure.Monitor {
			client, err := armalertsmanagement.NewAlertsClient(sub, cred, nil)
			if err != nil {
				return nil, fmt.Errorf("azure: alerts client for subscription %s: %w", sub, err)
			}
			monSubs = append(monSubs, azureMonitorSubscription{subscription: sub, lister: &armAlertsLister{client: client}})
		}
		if cfg.Azure.VMs {
			client, err := armcompute.NewVirtualMachinesClient(sub, cred, nil)
			if err != nil {
				return nil, fmt.Errorf("azure: virtual-machines client for subscription %s: %w", sub, err)
			}
			vmSubs = append(vmSubs, azureVMSubscription{subscription: sub, lister: &armVMLister{client: client}})
		}
		if cfg.Azure.Storage {
			client, err := armstorage.NewAccountsClient(sub, cred, nil)
			if err != nil {
				return nil, fmt.Errorf("azure: storage-accounts client for subscription %s: %w", sub, err)
			}
			storageSubs = append(storageSubs, azureStorageSubscription{subscription: sub, lister: &armStorageLister{client: client}})
		}
		if cfg.Azure.SQL {
			serversClient, err := armsql.NewServersClient(sub, cred, nil)
			if err != nil {
				return nil, fmt.Errorf("azure: sql servers client for subscription %s: %w", sub, err)
			}
			databasesClient, err := armsql.NewDatabasesClient(sub, cred, nil)
			if err != nil {
				return nil, fmt.Errorf("azure: sql databases client for subscription %s: %w", sub, err)
			}
			sqlSubs = append(sqlSubs, azureSQLSubscription{subscription: sub, lister: &armSQLLister{servers: serversClient, databases: databasesClient}})
		}
		if cfg.Azure.Redis {
			client, err := armredis.NewClient(sub, cred, nil)
			if err != nil {
				return nil, fmt.Errorf("azure: redis client for subscription %s: %w", sub, err)
			}
			redisSubs = append(redisSubs, azureRedisSubscription{subscription: sub, lister: &armRedisLister{client: client}})
		}
	}
	var srcs []sources.Source
	if len(aksSubs) > 0 {
		srcs = append(srcs, &aksSource{subs: aksSubs})
	}
	if len(monSubs) > 0 {
		srcs = append(srcs, &azureMonitorSource{subs: monSubs})
	}
	if len(vmSubs) > 0 {
		srcs = append(srcs, &azureVMSource{subs: vmSubs})
	}
	if len(storageSubs) > 0 {
		srcs = append(srcs, &azureStorageSource{subs: storageSubs})
	}
	if len(sqlSubs) > 0 {
		srcs = append(srcs, &azureSQLSource{subs: sqlSubs})
	}
	if len(redisSubs) > 0 {
		srcs = append(srcs, &azureRedisSource{subs: redisSubs})
	}
	return srcs, nil
}

// emitFiring publishes a firing cloud alert. Identity is (kind, scope, name)
// where scope is "subscription/region"; a resolve targets exactly that cluster.
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

func strVal(s *string) string {
	return sources.StrVal(s)
}
