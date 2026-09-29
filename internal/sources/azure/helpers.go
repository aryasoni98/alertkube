package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"

	"github.com/aryasoni98/alertkube/internal/sources"
)

// drainPager collects every page of an ARM list pager into one slice. items
// extracts a page's Value slice; the per-service response types differ but all
// share this drain loop, so each arm*Lister adapter reduces to one call. A
// pager that never ends stops at the shared sources.MaxPages runaway guard and
// returns the pages it fetched, so those resources are still evaluated.
func drainPager[R, T any](ctx context.Context, source string, pager *runtime.Pager[R], items func(R) []T) ([]T, error) {
	var out []T
	for pageN := 0; pager.More(); pageN++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if pageN >= sources.MaxPages {
			sources.PollTruncated(source, "", fmt.Sprintf("hit the %d-page runaway guard", sources.MaxPages))
			return out, nil
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, items(page)...)
	}
	return out, nil
}

// buildSub returns a builder that constructs one lister per configured
// subscription for an enabled service and wraps them in that service's Source.
// A disabled service builds nil, which sources.Compact drops. It replaces the
// declare-slice / append-under-toggle / append-source-if-non-empty trio every
// service repeated, so wiring a new one is a single table entry.
func buildSub[T any](
	enabled bool,
	subs []string,
	newLister func(subscription string) (func(context.Context) ([]T, error), error),
	newSource func([]sources.Scoped[T]) sources.Source,
) func() (sources.Source, error) {
	return func() (sources.Source, error) {
		if !enabled || len(subs) == 0 {
			return nil, nil
		}
		scopes := make([]sources.Scoped[T], 0, len(subs))
		for _, sub := range subs {
			list, err := newLister(sub)
			if err != nil {
				return nil, err
			}
			scopes = append(scopes, sources.Scoped[T]{Scope: sub, List: list})
		}
		return newSource(scopes), nil
	}
}

// clientErr wraps an ARM client-construction failure with the service and
// subscription it was for, so a misconfigured subscription is identifiable in
// the single line the controller logs before continuing without Azure.
func clientErr(service, subscription string, err error) error {
	return fmt.Errorf("azure: %s client for subscription %s: %w", service, subscription, err)
}
