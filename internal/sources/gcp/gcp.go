// Package gcp polls Google Cloud APIs and emits cloud-resource alerts into the
// same pipeline as the in-cluster Kubernetes watchers. It implements one
// sources.Source per GCP service, each gated by its own config toggle:
//
//   - GKE        - cluster health
//   - Monitoring - alert-policy posture (alerts when a policy is disabled;
//     GCP's Go SDK exposes no fired-incident listing)
//   - Compute    - Compute Engine instance health (REPAIRING)
//   - CloudSQL   - Cloud SQL instance state
//
// Credentials resolve via Application Default Credentials (GKE Workload
// Identity in-cluster, gcloud/service-account locally). GCP is project-scoped.
// Each source declares a narrow interface (gkeLister, ...) so it unit-tests
// against canned responses without the SDK or live credentials.
package gcp

import (
	"context"
	"fmt"

	container "cloud.google.com/go/container/apiv1"
	"cloud.google.com/go/container/apiv1/containerpb"
	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	compute "google.golang.org/api/compute/v1"
	sqladmin "google.golang.org/api/sqladmin/v1"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/sources"
)

const provider = "gcp"

// gkeLister lists clusters in one project across all locations. The real
// adapter calls the Cluster Manager API; tests provide a fake.
type gkeLister interface {
	List(ctx context.Context, project string) ([]*containerpb.Cluster, error)
}

// apiGKELister adapts the Cluster Manager client to gkeLister.
type apiGKELister struct {
	client *container.ClusterManagerClient
}

func (l *apiGKELister) List(ctx context.Context, project string) ([]*containerpb.Cluster, error) {
	resp, err := l.client.ListClusters(ctx, &containerpb.ListClustersRequest{
		Parent: fmt.Sprintf("projects/%s/locations/-", project),
	})
	if err != nil {
		return nil, err
	}
	return resp.GetClusters(), nil
}

// NewProvider builds the enabled GCP sources. It returns an error if the API
// client (and thus Application Default Credentials) cannot be initialized; the
// caller logs it and continues without GCP so a cloud-auth problem never takes
// down the Kubernetes watchers.
func NewProvider(ctx context.Context, cfg *config.Config) ([]sources.Source, error) {
	var srcs []sources.Source
	if cfg.GCP.GKE {
		client, err := container.NewClusterManagerClient(ctx)
		if err != nil {
			return nil, fmt.Errorf("gcp: cluster manager client: %w", err)
		}
		srcs = append(srcs, &gkeSource{projects: cfg.GCP.Projects, lister: &apiGKELister{client: client}})
	}
	if cfg.GCP.Monitoring {
		client, err := monitoring.NewAlertPolicyClient(ctx)
		if err != nil {
			return nil, fmt.Errorf("gcp: alert policy client: %w", err)
		}
		srcs = append(srcs, &gcpMonitoringSource{projects: cfg.GCP.Projects, lister: &apiPolicyLister{client: client}})
	}
	if cfg.GCP.Compute {
		svc, err := compute.NewService(ctx)
		if err != nil {
			return nil, fmt.Errorf("gcp: compute service: %w", err)
		}
		srcs = append(srcs, &gceSource{projects: cfg.GCP.Projects, lister: &apiGCELister{svc: svc}})
	}
	if cfg.GCP.CloudSQL {
		svc, err := sqladmin.NewService(ctx)
		if err != nil {
			return nil, fmt.Errorf("gcp: sqladmin service: %w", err)
		}
		srcs = append(srcs, &cloudSQLSource{projects: cfg.GCP.Projects, lister: &apiSQLLister{svc: svc}})
	}
	return srcs, nil
}

// emitFiring publishes a firing cloud alert. Identity is (kind, scope, name)
// where scope is "project/location"; a resolve targets exactly that cluster.
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
