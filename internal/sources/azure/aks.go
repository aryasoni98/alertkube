package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const sourceAKS = "azure-aks"

// armAKSLister adapts the SDK ManagedClustersClient to a per-subscription list
// function by draining its List pager into a slice.
type armAKSLister struct {
	client *armcontainerservice.ManagedClustersClient
}

func (l *armAKSLister) List(ctx context.Context) ([]*armcontainerservice.ManagedCluster, error) {
	return drainPager(ctx, sourceAKS, l.client.NewListPager(nil),
		func(r armcontainerservice.ManagedClustersClientListResponse) []*armcontainerservice.ManagedCluster {
			return r.Value
		})
}

// newAKSSource discovers AKS managed clusters per subscription and alerts on
// their control-plane health. ProvisioningState Failed/Canceled is critical; a
// Stopped power state is a warning; transient provisioning states
// (Creating/Updating/Deleting/...) are warnings; Succeeded + Running resolves.
// Each embedded agent pool is evaluated the same way as its own alert.
func newAKSSource(subs []sources.Scoped[*armcontainerservice.ManagedCluster]) sources.Source {
	return sources.NewListSource(sourceAKS, subs, evaluateAKS)
}

// evaluateAKS runs both levels of AKS evaluation for one cluster. Node pools
// are embedded in the ManagedCluster, so they come from the same list call.
func evaluateAKS(subscription string, c *armcontainerservice.ManagedCluster, emit sources.Emit) {
	evaluateAKSCluster(subscription, c, emit)
	evaluateAKSNodePools(subscription, c, emit)
}

// aksHealth is the provisioning + power-state decision table shared by clusters
// and node pools. An empty state (not reported) and Succeeded resolve unless
// the power state is Stopped (warning); Failed/Canceled is critical; any other
// state is transient (warning). suffix completes the caller's reason prefix.
func aksHealth(state, power string) (suffix string, sev alert.Severity, firing bool) {
	switch state {
	case "":
		return "", "", false
	case "Succeeded":
		if power == string(armcontainerservice.CodeStopped) {
			return "Stopped", alert.SeverityWarning, true
		}
		return "", "", false
	case "Failed", "Canceled":
		return "ProvisioningFailed", alert.SeverityCritical, true
	default:
		return "NotReady", alert.SeverityWarning, true
	}
}

// aksSummary renders the summary for a firing aksHealth suffix; noun is
// "AKS cluster" or "AKS node pool" and id the cluster or cluster/pool name.
func aksSummary(noun, id, state, suffix string) string {
	switch suffix {
	case "Stopped":
		return noun + " " + id + " is stopped"
	case "ProvisioningFailed":
		return noun + " " + id + " provisioning state is " + state
	default:
		return noun + " " + id + " is not ready (provisioning state " + state + ")"
	}
}

// aksPower returns a power state's code, or "" when it is not reported.
func aksPower(ps *armcontainerservice.PowerState) string {
	if ps == nil || ps.Code == nil {
		return ""
	}
	return string(*ps.Code)
}

// evaluateAKSCluster maps one cluster's provisioning + power state onto a single
// firing-or-resolve decision so the resolve stays surgical.
func evaluateAKSCluster(subscription string, c *armcontainerservice.ManagedCluster, emit sources.Emit) {
	if c == nil {
		return
	}
	name := strVal(c.Name)
	if name == "" {
		return
	}
	region := strVal(c.Location)
	scope := sources.Scope(subscription, region)

	var state, power string
	if c.Properties != nil {
		state = strVal(c.Properties.ProvisioningState)
		power = aksPower(c.Properties.PowerState)
	}
	suffix, sev, firing := aksHealth(state, power)
	if !firing {
		emitResolve(emit, alert.KindAKSCluster, scope, name)
		return
	}
	emitFiring(emit, alert.KindAKSCluster, scope, name, "AKSCluster"+suffix,
		aksSummary("AKS cluster", name, state, suffix), sev,
		map[string]string{"provisioningState": state, "powerState": power, "location": region})
}

// evaluateAKSNodePools alerts on each agent pool (node pool) of a cluster. The
// pool profiles are embedded in the ManagedCluster, so no extra API call is
// needed. Identity is cluster/pool. ProvisioningState Failed/Canceled is
// critical; a Stopped power state is a warning; transient states are warnings;
// Succeeded + running resolves.
func evaluateAKSNodePools(subscription string, c *armcontainerservice.ManagedCluster, emit sources.Emit) {
	if c == nil {
		return
	}
	cluster := strVal(c.Name)
	if cluster == "" || c.Properties == nil {
		return
	}
	scope := sources.Scope(subscription, strVal(c.Location))
	for _, p := range c.Properties.AgentPoolProfiles {
		if p == nil {
			continue
		}
		pool := strVal(p.Name)
		if pool == "" {
			continue
		}
		id := cluster + "/" + pool
		state := strVal(p.ProvisioningState)
		power := aksPower(p.PowerState)
		suffix, sev, firing := aksHealth(state, power)
		if !firing {
			emitResolve(emit, alert.KindAKSNodePool, scope, id)
			continue
		}
		emitFiring(emit, alert.KindAKSNodePool, scope, id, "AKSNodePool"+suffix,
			aksSummary("AKS node pool", id, state, suffix), sev,
			map[string]string{"cluster": cluster, "provisioningState": state, "powerState": power})
	}
}
