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

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const provider = "gcp"

// init self-registers the GCP provider (see sources.RegisterProvider).
func init() {
	sources.RegisterProvider(sources.Provider{
		Name:        provider,
		Enabled:     func(c *config.Config) bool { return c.GCP.Enabled },
		PollSeconds: func(c *config.Config) int { return c.GCP.PollSeconds },
		Build:       NewProvider,
	})
}

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
	// One entry per service: its config toggle and the constructor for its API
	// client plus the Source that polls with it. A disabled service builds nil
	// - without touching credentials - and Compact drops it, so adding a
	// service means adding one entry here and nothing else.
	projects, g := cfg.GCP.Projects, cfg.GCP
	builders := []sourceBuilder{
		buildProject(g.GKE, projects, func() (sources.Source, error) {
			client, err := container.NewClusterManagerClient(ctx)
			if err != nil {
				return nil, clientErr("cluster manager", err)
			}
			return newGKESource(projects, &apiGKELister{client: client}), nil
		}),

		buildProject(g.Monitoring, projects, func() (sources.Source, error) {
			client, err := monitoring.NewAlertPolicyClient(ctx)
			if err != nil {
				return nil, clientErr("alert policy", err)
			}
			return newMonitoringSource(projects, &apiPolicyLister{client: client}), nil
		}),

		buildProject(g.Compute, projects, func() (sources.Source, error) {
			svc, err := compute.NewService(ctx)
			if err != nil {
				return nil, clientErr("compute", err)
			}
			return newGCESource(projects, &apiGCELister{svc: svc}), nil
		}),

		buildProject(g.CloudSQL, projects, func() (sources.Source, error) {
			svc, err := sqladmin.NewService(ctx)
			if err != nil {
				return nil, clientErr("sqladmin", err)
			}
			return newCloudSQLSource(projects, &apiSQLLister{svc: svc}), nil
		}),
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
