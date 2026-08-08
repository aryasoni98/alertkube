package gcp

import (
	"context"
	"testing"

	"cloud.google.com/go/container/apiv1/containerpb"

	"github.com/aryasoni98/alertkube/internal/alert"
)

func nodePool(name string, status containerpb.NodePool_Status) *containerpb.NodePool {
	return &containerpb.NodePool{Name: name, Status: status}
}

func gkeClusterWithPools(name, location string, status containerpb.Cluster_Status, pools ...*containerpb.NodePool) *containerpb.Cluster {
	c := gkeCluster(name, location, status)
	c.NodePools = pools
	return c
}

func TestEvaluateGKENodePools(t *testing.T) {
	c := gkeClusterWithPools("cl", "us-central1", containerpb.Cluster_RUNNING,
		nodePool("good", containerpb.NodePool_RUNNING),
		nodePool("bad", containerpb.NodePool_ERROR),
		nodePool("provisioning", containerpb.NodePool_PROVISIONING),
	)
	emit, got := collect()
	evaluateGKENodePools("proj-1", c, emit)

	if len(*got) != 3 {
		t.Fatalf("expected 3 node-pool alerts, got %d", len(*got))
	}
	byName := map[string]*alert.Alert{}
	for _, a := range *got {
		byName[a.Name] = a
		if a.Kind != alert.KindGKENodePool || a.Namespace != "proj-1/us-central1" {
			t.Errorf("bad identity: kind=%s ns=%s name=%s", a.Kind, a.Namespace, a.Name)
		}
	}
	if a := byName["cl/good"]; a == nil || !a.Resolved {
		t.Errorf("cl/good should resolve: %+v", a)
	}
	if a := byName["cl/bad"]; a == nil || a.Resolved || a.Severity != alert.SeverityCritical {
		t.Errorf("cl/bad should be critical: %+v", a)
	}
	if a := byName["cl/provisioning"]; a == nil || a.Resolved || a.Severity != alert.SeverityWarning {
		t.Errorf("cl/provisioning should warn: %+v", a)
	}
}

func TestGKESourcePollIncludesNodePools(t *testing.T) {
	fake := &fakeGKELister{byProject: map[string][]*containerpb.Cluster{
		"proj-1": {gkeClusterWithPools("cl", "us-east1", containerpb.Cluster_RUNNING, nodePool("np", containerpb.NodePool_ERROR))},
	}}
	src := newGKESource([]string{"proj-1"}, fake)
	emit, got := collect()
	src.Poll(context.Background(), emit)

	var clusterResolve, poolCritical bool
	for _, a := range *got {
		switch a.Kind {
		case alert.KindGKECluster:
			clusterResolve = a.Resolved
		case alert.KindGKENodePool:
			poolCritical = !a.Resolved && a.Severity == alert.SeverityCritical && a.Name == "cl/np"
		}
	}
	if !clusterResolve || !poolCritical {
		t.Fatalf("expected cluster resolve + node pool critical; clusterResolve=%v poolCritical=%v", clusterResolve, poolCritical)
	}
}
