package aws

import (
	"context"
	"strconv"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"golang.org/x/time/rate"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/sources"
)

const sourceRoute53 = "aws-route53"

// route53API is the subset of the Route53 client the health-check source uses.
type route53API interface {
	ListHealthChecks(context.Context, *route53.ListHealthChecksInput, ...func(*route53.Options)) (*route53.ListHealthChecksOutput, error)
	GetHealthCheckStatus(context.Context, *route53.GetHealthCheckStatusInput, ...func(*route53.Options)) (*route53.GetHealthCheckStatusOutput, error)
}

// route53Source alerts on Route53 health checks that a majority of AWS health
// checkers report as failing. Route53 is a global service, so the source is
// built once (not per region), and alerts carry globalScope as the region.
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
	lim := newDescribeLimiter()
	forEachPage(ctx, sourceRoute53, globalScope, func(ctx context.Context, marker *string) (*string, error) {
		out, err := s.client.ListHealthChecks(ctx, &route53.ListHealthChecksInput{Marker: marker})
		if err != nil {
			return nil, err
		}
		for i := range out.HealthChecks {
			if err := s.evaluate(ctx, lim, awssdk.ToString(out.HealthChecks[i].Id), emit); err != nil {
				return nil, err
			}
		}
		if !out.IsTruncated {
			return nil, nil
		}
		return out.NextMarker, nil
	})
}

// evaluate fetches and classifies one health check. It returns only the
// describe-limiter error, which must stop the listing; an API error is
// recorded here and skips just this check.
func (s *route53Source) evaluate(ctx context.Context, lim *rate.Limiter, id string, emit sources.Emit) error {
	if id == "" {
		return nil
	}
	if err := waitDescribe(ctx, lim); err != nil {
		return err
	}
	st, err := s.client.GetHealthCheckStatus(ctx, &route53.GetHealthCheckStatusInput{HealthCheckId: awssdk.String(id)})
	if err != nil {
		pollErr(sourceRoute53, globalScope, err)
		return nil
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
		emitFiring(emit, alert.KindRoute53HealthCheck, globalScope, id, "Route53HealthCheckFailing",
			"Route53 health check "+id+" failing from a majority of health checkers", alert.SeverityCritical,
			map[string]string{"failingCheckers": strconv.Itoa(failing), "totalCheckers": strconv.Itoa(total)})
		return nil
	}
	emitResolve(emit, alert.KindRoute53HealthCheck, globalScope, id)
	return nil
}
