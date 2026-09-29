package aws

import (
	"context"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"golang.org/x/time/rate"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

// describeRate and describeBurst pace the N+1 Describe/Get calls that follow
// a list. AWS throttles each API per account and region, so every regional
// source builds its own limiter per region per poll, and the global S3 and
// Route53 sources build one per poll. One source's fan-out therefore never
// spends another's budget. 10/s with a burst of 20 leaves a small inventory
// unpaced and keeps a large one from bursting into the throttling quota it
// shares with the account's other clients. Pacing makes a large poll slower:
// N describes take about N/10 seconds, and a poll that cannot finish them
// before its deadline records a poll error.
const (
	describeRate  = rate.Limit(10)
	describeBurst = 20
)

func newDescribeLimiter() *rate.Limiter {
	return rate.NewLimiter(describeRate, describeBurst)
}

// waitDescribe blocks until lim allows the next describe call. It fails when
// ctx is done, and also at once when the next token is due after ctx's
// deadline, while ctx.Err() is still nil. Callers return the error from their
// page callback so forEachPage stops listing and records it; ending the page
// quietly would skip the rest of the inventory with no metric, and the
// skipped resources' alerts would TTL-resolve.
func waitDescribe(ctx context.Context, lim *rate.Limiter) error {
	if err := lim.Wait(ctx); err != nil {
		return fmt.Errorf("describe rate limit: %w", err)
	}
	return nil
}

// regionClient pairs an AWS region with the service client scoped to it. Every
// regional source used to declare its own identical {region, client} struct;
// they are now type aliases of this generic (see each source file), so the
// shared Poll fan-out (pollByRegion) can operate over any of them.
type regionClient[C any] struct {
	region string
	client C
}

// pollByRegion runs pollOne once per region client. It replaces the identical
// "for _, rc := range s.regions { s.pollRegion(ctx, rc, emit) }" loop that every
// regional AWS source used to duplicate, so the per-region fan-out lives in
// exactly one place. Passing the whole regionClient lets each source keep its
// existing pollRegion(ctx, rc, emit) method unchanged.
//
// Regions are polled in turn under one poll deadline, so a region that uses
// it up leaves the later ones unpolled. Each of those is recorded as a poll
// error naming the region; returning quietly would leave them blind on every
// poll with nothing in the logs. pollErr ignores cancellation, so a shutdown
// records nothing.
func pollByRegion[C any](ctx context.Context, source string, regions []regionClient[C], emit sources.Emit, pollOne func(ctx context.Context, rc regionClient[C], emit sources.Emit)) {
	for _, rc := range regions {
		if err := ctx.Err(); err != nil {
			pollErr(source, rc.region, fmt.Errorf("region not polled: %w", err))
			continue
		}
		pollOne(ctx, rc, emit)
	}
}

// regionConfig pairs a configured region with the SDK config resolved for it.
// buildSources resolves credentials once per region and every service client
// for that region is minted from the same config.
type regionConfig struct {
	region string
	cfg    awssdk.Config
}

// regionalSource builds one client per configured region for an enabled service
// and wraps them in that service's Source. It returns nil when the service is
// toggled off or no region is configured; buildSources filters those out with
// sources.Compact. This replaces the declare-slice / append-under-toggle /
// append-source-if-non-empty trio that each of the regional services repeated,
// so wiring a new service is one line that cannot reference the wrong slice.
func regionalSource[C any](
	enabled bool,
	regions []regionConfig,
	newClient func(awssdk.Config) C,
	newSource func([]regionClient[C]) sources.Source,
) sources.Source {
	if !enabled || len(regions) == 0 {
		return nil
	}
	clients := make([]regionClient[C], 0, len(regions))
	for _, rc := range regions {
		clients = append(clients, regionClient[C]{region: rc.region, client: newClient(rc.cfg)})
	}
	return newSource(clients)
}

// globalScope is the pseudo-region carried by alerts from the account-wide S3
// and Route53 sources. Neither has a real AWS region, so their alerts share one
// constant scope; a resolve still targets exactly one resource via
// kind+namespace+name.
const globalScope = "global"

// globalSource builds a Source for an account-wide service from the first
// region's config. S3 and Route53 list the whole account regardless of the
// client's region, so building one per region would re-alert every bucket /
// health check once per configured region.
func globalSource(enabled bool, regions []regionConfig, newSource func(awssdk.Config) sources.Source) sources.Source {
	if !enabled || len(regions) == 0 {
		return nil
	}
	return newSource(regions[0].cfg)
}

// forEachPage drives a token-paginated AWS list call for one region: page is
// invoked with the current pagination token (nil on the first call) and returns
// the next one; iteration ends when the token is exhausted. An error from page
// (an API error or a describe-limiter wait) or a done context is recorded via
// pollErr and stops the loop, skipping the rest of the region for this poll. A
// token that does not advance, or the runaway guard, stops it as a truncation.
// Every regional AWS source shares this loop so the termination rules live in
// exactly one place.
func forEachPage(ctx context.Context, source, region string, page func(ctx context.Context, token *string) (next *string, err error)) {
	if err := walkPages(ctx, source, region, page); err != nil {
		pollErr(source, region, err)
	}
}

// walkPages is forEachPage without the poll-error record: it returns the error
// that stopped the loop instead. A nested list (CloudTrail's per-event lookup,
// EKS node groups) uses it to decide whether to record the error itself or to
// hand it up to the enclosing loop, so one failure is recorded once.
func walkPages(ctx context.Context, source, region string, page func(ctx context.Context, token *string) (next *string, err error)) error {
	var token *string
	for range sources.MaxPages {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, err := page(ctx, token)
		if err != nil {
			return err
		}
		if next == nil || *next == "" {
			return nil
		}
		if token != nil && *token == *next {
			sources.PollTruncated(source, region, "pagination token did not advance")
			return nil
		}
		token = next
	}
	sources.PollTruncated(source, region, fmt.Sprintf("hit the %d-page runaway guard", sources.MaxPages))
	return nil
}

// dbStatusSeverity classifies a free-form RDS/Aurora status string against the
// caller's set of known-critical states. "stopped" is a warning for both (a
// stopped production database is usually unintended); everything else -
// "available" plus transient operational states like backing-up / modifying /
// rebooting - is healthy, so routine maintenance never pages. The bool reports
// whether the status is firing (true) or should resolve (false).
func dbStatusSeverity(status string, critical map[string]bool) (alert.Severity, bool) {
	switch {
	case critical[status]:
		return alert.SeverityCritical, true
	case status == "stopped":
		return alert.SeverityWarning, true
	default:
		return alert.SeverityInfo, false
	}
}
