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

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/sources"
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

// sourceBuilder defers one service's construction so NewProvider can list every
// service in a single table and run them all through one error check. buildSub
// binds the generic lister type at the call site, leaving a monomorphic closure.
type sourceBuilder func() (sources.Source, error)

// buildSub returns a builder that constructs one lister per configured
// subscription for an enabled service and wraps them in that service's Source.
// A disabled service builds nil, which sources.Compact drops. It replaces the
// declare-slice / append-under-toggle / append-source-if-non-empty trio every
// service repeated, so wiring a new one is a single table entry.
func buildSub[L any](
	enabled bool,
	subs []string,
	newLister func(subscription string) (L, error),
	newSource func([]subLister[L]) sources.Source,
) sourceBuilder {
	return func() (sources.Source, error) {
		if !enabled || len(subs) == 0 {
			return nil, nil
		}
		listers := make([]subLister[L], 0, len(subs))
		for _, sub := range subs {
			l, err := newLister(sub)
			if err != nil {
				return nil, err
			}
			listers = append(listers, subLister[L]{subscription: sub, lister: l})
		}
		return newSource(listers), nil
	}
}

// clientErr wraps an ARM client-construction failure with the service and
// subscription it was for, so a misconfigured subscription is identifiable in
// the single line the controller logs before continuing without Azure.
func clientErr(service, subscription string, err error) error {
	return fmt.Errorf("azure: %s client for subscription %s: %w", service, subscription, err)
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
	// One entry per service: its config toggle, the per-subscription lister
	// constructor, and the Source that owns the resulting listers. A disabled
	// service builds nil and Compact drops it, so adding a service means adding
	// one entry here and nothing else.
	subs, z := cfg.Azure.Subscriptions, cfg.Azure
	builders := []sourceBuilder{
		buildSub(z.AKS, subs, func(sub string) (aksLister, error) {
			client, err := armcontainerservice.NewManagedClustersClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("managed-clusters", sub, err)
			}
			return &armAKSLister{client: client}, nil
		}, func(ls []aksSubscription) sources.Source { return &aksSource{subs: ls} }),

		buildSub(z.Monitor, subs, func(sub string) (alertsLister, error) {
			client, err := armalertsmanagement.NewAlertsClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("alerts", sub, err)
			}
			return &armAlertsLister{client: client}, nil
		}, func(ls []azureMonitorSubscription) sources.Source { return &azureMonitorSource{subs: ls} }),

		buildSub(z.VMs, subs, func(sub string) (vmLister, error) {
			client, err := armcompute.NewVirtualMachinesClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("virtual-machines", sub, err)
			}
			return &armVMLister{client: client}, nil
		}, func(ls []azureVMSubscription) sources.Source { return &azureVMSource{subs: ls} }),

		buildSub(z.Storage, subs, func(sub string) (storageLister, error) {
			client, err := armstorage.NewAccountsClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("storage-accounts", sub, err)
			}
			return &armStorageLister{client: client}, nil
		}, func(ls []azureStorageSubscription) sources.Source { return &azureStorageSource{subs: ls} }),

		buildSub(z.SQL, subs, func(sub string) (sqlLister, error) {
			servers, err := armsql.NewServersClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("sql servers", sub, err)
			}
			databases, err := armsql.NewDatabasesClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("sql databases", sub, err)
			}
			return &armSQLLister{servers: servers, databases: databases}, nil
		}, func(ls []azureSQLSubscription) sources.Source { return &azureSQLSource{subs: ls} }),

		buildSub(z.Redis, subs, func(sub string) (redisLister, error) {
			client, err := armredis.NewClient(sub, cred, nil)
			if err != nil {
				return nil, clientErr("redis", sub, err)
			}
			return &armRedisLister{client: client}, nil
		}, func(ls []azureRedisSubscription) sources.Source { return &azureRedisSource{subs: ls} }),
	}

	srcs := make([]sources.Source, 0, len(builders))
	for _, build := range builders {
		s, err := build()
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, s)
	}
	return sources.Compact(srcs), nil
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
