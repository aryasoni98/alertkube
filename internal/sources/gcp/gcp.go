// Package gcp polls Google Cloud APIs and emits cloud-resource alerts into the
// same pipeline as the in-cluster Kubernetes watchers. It implements one
// sources.Source per GCP service, each gated by its own config toggle:
//
//   - GKE        - cluster and node-pool health
//   - Monitoring - alert-policy posture (alerts when a policy is disabled;
//     GCP's Go SDK exposes no fired-incident listing)
//   - Compute    - Compute Engine instance health (REPAIRING)
//   - CloudSQL   - Cloud SQL instance state
//
// Credentials resolve via Application Default Credentials (GKE Workload
// Identity in-cluster, gcloud/service-account locally). GCP is project-scoped.
// Each source polls through a per-project list function (see sources.Scoped),
// so it unit-tests against canned responses without the SDK or live
// credentials.
package gcp

import (
	"context"

	container "cloud.google.com/go/container/apiv1"
	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	compute "google.golang.org/api/compute/v1"
	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/config"
	"github.com/aryasoni98/alertkube/v2/internal/sources"
)

const provider = "gcp"

// init self-registers the GCP provider (see sources.RegisterProvider).
func init() {
	sources.RegisterProvider(sources.Provider{
		Name: provider,
		Bind: func(c *config.Config) sources.Bound {
			section := c.GCP
			return sources.Bound{
				Enabled:     section.Enabled,
				PollSeconds: section.PollSeconds,
				Build: func(ctx context.Context) ([]sources.Source, error) {
					return buildSources(ctx, section)
				},
			}
		},
	})
}

// buildSources builds the enabled GCP sources. It returns an error if the API
// client (and thus Application Default Credentials) cannot be initialized; the
// caller logs it and continues without GCP so a cloud-auth problem never takes
// down the Kubernetes watchers.
func buildSources(ctx context.Context, cfg config.GCP) ([]sources.Source, error) {
	// One entry per service: its config toggle and the constructor for its API
	// client plus the Source that polls with it. A disabled service builds nil
	// - without touching credentials - and Compact drops it, so adding a
	// service means adding one entry here and nothing else.
	projects := cfg.Projects
	return sources.BuildAll(
		buildProject(cfg.GKE, projects, func() (sources.Source, error) {
			client, err := container.NewClusterManagerClient(ctx)
			if err != nil {
				return nil, clientErr("cluster manager", err)
			}
			return newGKESource(projects, (&apiGKELister{client: client}).List), nil
		}),

		buildProject(cfg.Monitoring, projects, func() (sources.Source, error) {
			client, err := monitoring.NewAlertPolicyClient(ctx)
			if err != nil {
				return nil, clientErr("alert policy", err)
			}
			return newMonitoringSource(projects, (&apiPolicyLister{client: client}).List), nil
		}),

		buildProject(cfg.Compute, projects, func() (sources.Source, error) {
			svc, err := compute.NewService(ctx)
			if err != nil {
				return nil, clientErr("compute", err)
			}
			return newGCESource(projects, (&apiGCELister{svc: svc}).List), nil
		}),

		buildProject(cfg.CloudSQL, projects, func() (sources.Source, error) {
			svc, err := sqladmin.NewService(ctx)
			if err != nil {
				return nil, clientErr("sqladmin", err)
			}
			return newCloudSQLSource(projects, (&apiSQLLister{svc: svc}).List), nil
		}),
	)
}

// emitFiring publishes a firing cloud alert. Identity is (kind, scope, name)
// where scope is "project/location" (Monitoring alert policies use the bare
// project); a resolve targets exactly that resource.
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
