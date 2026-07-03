package gcp

import (
	"context"
	"strings"

	compute "google.golang.org/api/compute/v1"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceGCE = "gcp-compute"

// gceLister lists Compute Engine instances across all zones of one project.
// The real adapter drains the aggregated-list pages; tests provide a fake.
type gceLister interface {
	List(ctx context.Context, project string) ([]*compute.Instance, error)
}

type apiGCELister struct {
	svc *compute.Service
}

func (l *apiGCELister) List(ctx context.Context, project string) ([]*compute.Instance, error) {
	var out []*compute.Instance
	err := l.svc.Instances.AggregatedList(project).Pages(ctx, func(page *compute.InstanceAggregatedList) error {
		for _, scoped := range page.Items {
			out = append(out, scoped.Instances...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type gceSource struct {
	projects []string
	lister   gceLister
}

func (s *gceSource) Name() string { return sourceGCE }

func (s *gceSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByProject(ctx, sourceGCE, s.projects, s.lister, emit, evaluateGCEInstance)
}

// evaluateGCEInstance alerts on instances in REPAIRING (critical). Other states
// (TERMINATED/STOPPING/SUSPENDED/...) are usually intentional and not alerted,
// mirroring the AWS EC2 source. Any non-REPAIRING state resolves.
func evaluateGCEInstance(project string, in *compute.Instance, emit sources.Emit) {
	if in == nil || in.Name == "" {
		return
	}
	zone := shortZone(in.Zone)
	scope := sources.Scope(project, zone)
	if in.Status == "REPAIRING" {
		emitFiring(emit, alert.KindGCEInstance, scope, in.Name, "GCEInstanceRepairing",
			"Compute Engine instance "+in.Name+" is REPAIRING", alert.SeverityCritical,
			map[string]string{"status": in.Status, "zone": zone})
		return
	}
	emitResolve(emit, alert.KindGCEInstance, scope, in.Name)
}

// shortZone trims a full zone URL (".../zones/us-central1-a") to the bare name.
func shortZone(zoneURL string) string {
	if i := strings.LastIndex(zoneURL, "/"); i >= 0 {
		return zoneURL[i+1:]
	}
	return zoneURL
}
