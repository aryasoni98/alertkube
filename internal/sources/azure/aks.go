package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceAKS = "azure-aks"

type aksSubscription = subLister[aksLister]

// aksSource discovers AKS managed clusters per subscription and alerts on their
// control-plane health. ProvisioningState Failed/Canceled is critical; a
// Stopped power state is a warning; transient provisioning states
// (Creating/Updating/Deleting/...) are warnings; Succeeded + Running resolves.
// This is the brief's AKS "cluster discovery + cluster health monitoring".
type aksSource struct {
	subs []aksSubscription
}

func (s *aksSource) Name() string { return sourceAKS }

func (s *aksSource) Poll(ctx context.Context, emit sources.Emit) {
	pollBySubscription(ctx, sourceAKS, s.subs, emit, func(subscription string, c armcontainerservice.ManagedCluster, emit sources.Emit) {
		evaluateAKSCluster(subscription, c, emit)
		evaluateAKSNodePools(subscription, c, emit)
	})
}

// evaluateAKSCluster maps one cluster's provisioning + power state onto a single
// firing-or-resolve decision so the resolve stays surgical.
func evaluateAKSCluster(subscription string, c armcontainerservice.ManagedCluster, emit sources.Emit) {
	name := strVal(c.Name)
	if name == "" {
		return
	}
	region := strVal(c.Location)
	scope := sources.Scope(subscription, region)

	var state, power string
	if c.Properties != nil {
		state = strVal(c.Properties.ProvisioningState)
		if c.Properties.PowerState != nil && c.Properties.PowerState.Code != nil {
			power = string(*c.Properties.PowerState.Code)
		}
	}
	details := map[string]string{"provisioningState": state, "powerState": power, "location": region}

	switch state {
	case "Succeeded":
		// Healthy provisioning; fall through to the power-state check.
	case "Failed", "Canceled":
		emitFiring(emit, alert.KindAKSCluster, scope, name, "AKSClusterProvisioningFailed",
			"AKS cluster "+name+" provisioning state is "+state, alert.SeverityCritical, details)
		return
	default:
		emitFiring(emit, alert.KindAKSCluster, scope, name, "AKSClusterNotReady",
			"AKS cluster "+name+" is not ready (provisioning state "+state+")", alert.SeverityWarning, details)
		return
	}
	if power == string(armcontainerservice.CodeStopped) {
		emitFiring(emit, alert.KindAKSCluster, scope, name, "AKSClusterStopped",
			"AKS cluster "+name+" is stopped", alert.SeverityWarning, details)
		return
	}
	emitResolve(emit, alert.KindAKSCluster, scope, name)
}

// evaluateAKSNodePools alerts on each agent pool (node pool) of a cluster. The
// pool profiles are embedded in the ManagedCluster, so no extra API call is
// needed. Identity is cluster/pool. ProvisioningState Failed/Canceled is
// critical; a Stopped power state is a warning; transient states are warnings;
// Succeeded + running resolves.
func evaluateAKSNodePools(subscription string, c armcontainerservice.ManagedCluster, emit sources.Emit) {
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
		power := ""
		if p.PowerState != nil && p.PowerState.Code != nil {
			power = string(*p.PowerState.Code)
		}
		details := map[string]string{"cluster": cluster, "provisioningState": state, "powerState": power}
		switch state {
		case "Succeeded":
			if power == string(armcontainerservice.CodeStopped) {
				emitFiring(emit, alert.KindAKSNodePool, scope, id, "AKSNodePoolStopped",
					"AKS node pool "+id+" is stopped", alert.SeverityWarning, details)
				continue
			}
			emitResolve(emit, alert.KindAKSNodePool, scope, id)
		case "Failed", "Canceled":
			emitFiring(emit, alert.KindAKSNodePool, scope, id, "AKSNodePoolProvisioningFailed",
				"AKS node pool "+id+" provisioning state is "+state, alert.SeverityCritical, details)
		default:
			emitFiring(emit, alert.KindAKSNodePool, scope, id, "AKSNodePoolNotReady",
				"AKS node pool "+id+" is not ready (provisioning state "+state+")", alert.SeverityWarning, details)
		}
	}
}
