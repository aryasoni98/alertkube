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

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertsmanagement/armalertsmanagement"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/metrics"
	"alertkube/internal/sources"
)

const provider = "azure"

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
	var out []armcontainerservice.ManagedCluster
	pager := l.client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, c := range page.Value {
			if c != nil {
				out = append(out, *c)
			}
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
	a := alert.New(k, scope, name, reason, sev)
	a.Summary = summary
	a.Labels["provider"] = provider
	for key, v := range details {
		if v != "" {
			a.Details[key] = v
		}
	}
	emit(a)
}

func emitResolve(emit sources.Emit, k alert.Kind, scope, name string) {
	emit(&alert.Alert{Kind: k, Namespace: scope, Name: name, Resolved: true})
}

func pollErr(source, scope string, err error) {
	metrics.CloudPollErrors.WithLabelValues(source).Inc()
	klog.Warningf("%s poll failed (%s): %v", source, scope, err)
}

func strVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
