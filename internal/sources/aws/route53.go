package aws

import (
	"context"
	"strconv"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceRoute53 = "aws-route53"

// route53Source alerts on Route53 health checks that a majority of AWS health
// checkers report as failing. Route53 is a global service, so the source is
// built once (not per region), and alerts carry "global" as the region.
// GetHealthCheckStatus returns one observation per checker; each StatusReport's
// Status string, per the API contract, begins with "Success" or "Failure", so
// the check is treated as down when a strict majority of reporting checkers see
// failure - mirroring Route53's own quorum rather than paging on a single
// checker's transient blip. A check with no observations resolves rather than
// alerting on missing data.
type route53Source struct {
	client route53API
}

func (s *route53Source) Name() string { return sourceRoute53 }

func (s *route53Source) Poll(ctx context.Context, emit sources.Emit) {
	var marker *string
	for {
		out, err := s.client.ListHealthChecks(ctx, &route53.ListHealthChecksInput{Marker: marker})
		if err != nil {
			pollErr(sourceRoute53, "global", err)
			return
		}
		for i := range out.HealthChecks {
			s.evaluate(ctx, awssdk.ToString(out.HealthChecks[i].Id), emit)
		}
		if !out.IsTruncated || out.NextMarker == nil || *out.NextMarker == "" {
			return
		}
		marker = out.NextMarker
	}
}

func (s *route53Source) evaluate(ctx context.Context, id string, emit sources.Emit) {
	if id == "" {
		return
	}
	st, err := s.client.GetHealthCheckStatus(ctx, &route53.GetHealthCheckStatusInput{HealthCheckId: awssdk.String(id)})
	if err != nil {
		pollErr(sourceRoute53, "global", err)
		return
	}
	failing, total := 0, 0
	for _, obs := range st.HealthCheckObservations {
		if obs.StatusReport == nil || obs.StatusReport.Status == nil {
			continue
		}
		total++
		if strings.HasPrefix(*obs.StatusReport.Status, "Failure") {
			failing++
		}
	}
	if total > 0 && failing*2 > total {
		emitFiring(emit, alert.KindRoute53HealthCheck, "global", id, "Route53HealthCheckFailing",
			"Route53 health check "+id+" failing from a majority of health checkers", alert.SeverityCritical,
			map[string]string{"failingCheckers": strconv.Itoa(failing), "totalCheckers": strconv.Itoa(total)})
		return
	}
	emitResolve(emit, alert.KindRoute53HealthCheck, "global", id)
}
