package gcp

import (
	"cloud.google.com/go/container/apiv1/containerpb"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const sourceGKE = "gcp-gke"

// gkeSource discovers GKE clusters per project (all locations) and alerts on
// their health. ERROR/DEGRADED is critical; transient states (PROVISIONING/
// RECONCILING/STOPPING) are warnings; RUNNING resolves. This is the brief's GKE
// "cluster discovery + cluster health monitoring".
type gkeSource = projectSource[*containerpb.Cluster, gkeLister]

func newGKESource(projects []string, lister gkeLister) *gkeSource {
	return newProjectSource(sourceGKE, projects, lister, evaluateGKE)
}

// evaluateGKE runs both levels of GKE evaluation for one cluster. Node pools
// are embedded in the Cluster object, so both come off the same list call.
func evaluateGKE(project string, c *containerpb.Cluster, emit sources.Emit) {
	evaluateGKECluster(project, c, emit)
	evaluateGKENodePools(project, c, emit)
}

func evaluateGKECluster(project string, c *containerpb.Cluster, emit sources.Emit) {
	if c == nil || c.GetName() == "" {
		return
	}
	name := c.GetName()
	location := c.GetLocation()
	scope := sources.Scope(project, location)
	status := c.GetStatus()
	details := map[string]string{"status": status.String(), "location": location}

	switch status {
	case containerpb.Cluster_RUNNING:
		emitResolve(emit, alert.KindGKECluster, scope, name)
	case containerpb.Cluster_ERROR, containerpb.Cluster_DEGRADED:
		emitFiring(emit, alert.KindGKECluster, scope, name, "GKEClusterUnhealthy",
			"GKE cluster "+name+" status is "+status.String(), alert.SeverityCritical, details)
	default:
		// PROVISIONING / RECONCILING / STOPPING / STATUS_UNSPECIFIED: transient.
		emitFiring(emit, alert.KindGKECluster, scope, name, "GKEClusterNotRunning",
			"GKE cluster "+name+" is not running (status "+status.String()+")", alert.SeverityWarning, details)
	}
}

// evaluateGKENodePools alerts on each node pool of a cluster. Node pools are
// embedded in the Cluster object, so no extra API call is needed. ERROR/
// DEGRADED/RUNNING_WITH_ERROR is critical; transient states are warnings;
// RUNNING resolves. Identity is cluster/pool.
func evaluateGKENodePools(project string, c *containerpb.Cluster, emit sources.Emit) {
	if c == nil || c.GetName() == "" {
		return
	}
	cluster := c.GetName()
	scope := sources.Scope(project, c.GetLocation())
	for _, np := range c.GetNodePools() {
		if np == nil || np.GetName() == "" {
			continue
		}
		id := cluster + "/" + np.GetName()
		status := np.GetStatus()
		details := map[string]string{"cluster": cluster, "status": status.String()}
		switch status {
		case containerpb.NodePool_RUNNING:
			emitResolve(emit, alert.KindGKENodePool, scope, id)
		case containerpb.NodePool_ERROR, containerpb.NodePool_RUNNING_WITH_ERROR:
			emitFiring(emit, alert.KindGKENodePool, scope, id, "GKENodePoolUnhealthy",
				"GKE node pool "+id+" status is "+status.String(), alert.SeverityCritical, details)
		default:
			// PROVISIONING / RECONCILING / STOPPING / STATUS_UNSPECIFIED: transient.
			emitFiring(emit, alert.KindGKENodePool, scope, id, "GKENodePoolNotRunning",
				"GKE node pool "+id+" is not running (status "+status.String()+")", alert.SeverityWarning, details)
		}
	}
}
