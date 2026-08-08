package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"

	"github.com/aryasoni98/alertkube/internal/alert"
)

func agentPool(name, provState string, power *armcontainerservice.Code) *armcontainerservice.ManagedClusterAgentPoolProfile {
	p := &armcontainerservice.ManagedClusterAgentPoolProfile{Name: sp(name), ProvisioningState: sp(provState)}
	if power != nil {
		p.PowerState = &armcontainerservice.PowerState{Code: power}
	}
	return p
}

func aksClusterWithPools(name, location string, pools ...*armcontainerservice.ManagedClusterAgentPoolProfile) armcontainerservice.ManagedCluster {
	c := aksCluster(name, location, "Succeeded", codePtr(armcontainerservice.CodeRunning))
	c.Properties.AgentPoolProfiles = pools
	return c
}

func TestEvaluateAKSNodePools(t *testing.T) {
	c := aksClusterWithPools("cl", "eastus",
		agentPool("good", "Succeeded", codePtr(armcontainerservice.CodeRunning)),
		agentPool("stopped", "Succeeded", codePtr(armcontainerservice.CodeStopped)),
		agentPool("bad", "Failed", nil),
	)
	emit, got := collect()
	evaluateAKSNodePools("sub-1", c, emit)

	if len(*got) != 3 {
		t.Fatalf("expected 3 node-pool alerts, got %d", len(*got))
	}
	byName := map[string]*alert.Alert{}
	for _, a := range *got {
		byName[a.Name] = a
		if a.Kind != alert.KindAKSNodePool || a.Namespace != "sub-1/eastus" {
			t.Errorf("bad identity: kind=%s ns=%s name=%s", a.Kind, a.Namespace, a.Name)
		}
	}
	if a := byName["cl/good"]; a == nil || !a.Resolved {
		t.Errorf("cl/good should resolve: %+v", a)
	}
	if a := byName["cl/stopped"]; a == nil || a.Resolved || a.Reason != "AKSNodePoolStopped" || a.Severity != alert.SeverityWarning {
		t.Errorf("cl/stopped should warn (stopped): %+v", a)
	}
	if a := byName["cl/bad"]; a == nil || a.Resolved || a.Severity != alert.SeverityCritical {
		t.Errorf("cl/bad should be critical: %+v", a)
	}
}

func TestAKSSourcePollIncludesNodePools(t *testing.T) {
	fake := &fakeAKSLister{clusters: []armcontainerservice.ManagedCluster{
		aksClusterWithPools("cl", "eastus", agentPool("np", "Failed", nil)),
	}}
	src := &aksSource{subs: []aksSubscription{{subscription: "sub-1", lister: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	// Cluster Succeeded+Running -> resolve; node pool Failed -> critical.
	var clusterResolve, poolCritical bool
	for _, a := range *got {
		switch a.Kind {
		case alert.KindAKSCluster:
			clusterResolve = a.Resolved
		case alert.KindAKSNodePool:
			poolCritical = !a.Resolved && a.Severity == alert.SeverityCritical && a.Name == "cl/np"
		}
	}
	if !clusterResolve || !poolCritical {
		t.Fatalf("expected cluster resolve + node pool critical; clusterResolve=%v poolCritical=%v", clusterResolve, poolCritical)
	}
}
