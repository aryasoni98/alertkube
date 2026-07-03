package aws

import (
	"context"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

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
func pollByRegion[C any](ctx context.Context, regions []regionClient[C], emit sources.Emit, pollOne func(ctx context.Context, rc regionClient[C], emit sources.Emit)) {
	for _, rc := range regions {
		pollOne(ctx, rc, emit)
	}
}

// forEachPage drives a token-paginated AWS list call for one region: page is
// invoked with the current pagination token (nil on the first call) and returns
// the next one; iteration ends when the token is exhausted. An API error is
// recorded via pollErr and stops the loop, skipping the rest of the region for
// this poll. Every regional AWS source shares this loop so the termination
// rules (nil or empty token) live in exactly one place.
func forEachPage(ctx context.Context, source, region string, page func(ctx context.Context, token *string) (next *string, err error)) {
	var token *string
	for {
		next, err := page(ctx, token)
		if err != nil {
			pollErr(source, region, err)
			return
		}
		if next == nil || *next == "" {
			return
		}
		token = next
	}
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
