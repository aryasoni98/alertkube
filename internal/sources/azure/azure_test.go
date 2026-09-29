package azure

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/sources"
)

// testPage is a stand-in for an ARM list response: its page number and the
// one item it carries.
type testPage struct {
	n    int
	item int
}

// TestDrainPager drives drainPager with an in-memory runtime.Pager. A pager
// that ends is drained whole; one that never ends stops at the shared runaway
// guard and keeps the pages it fetched; a fetch error or a cancelled context
// discards the partial list so the caller records a poll error instead.
func TestDrainPager(t *testing.T) {
	errFetch := errors.New("throttled")
	cases := []struct {
		name          string
		lastPage      int // 0 means the pager never ends
		failOn        int // page whose fetch fails; 0 means none
		cancelAfter   int // cancel the context after fetching this page; 0 means never
		wantItems     int
		wantFetches   int
		wantTruncated float64
		wantErr       error
	}{
		{name: "drains every page", lastPage: 3, wantItems: 3, wantFetches: 3},
		{name: "runaway pager stops at the guard", wantItems: sources.MaxPages, wantFetches: sources.MaxPages, wantTruncated: 1},
		{name: "fetch error", lastPage: 3, failOn: 2, wantFetches: 2, wantErr: errFetch},
		{name: "cancelled context", lastPage: 3, cancelAfter: 1, wantFetches: 1, wantErr: context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const source = "azure-test-drain"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fetches := 0
			pager := runtime.NewPager(runtime.PagingHandler[testPage]{
				More: func(p testPage) bool { return tc.lastPage == 0 || p.n < tc.lastPage },
				Fetcher: func(_ context.Context, cur *testPage) (testPage, error) {
					fetches++
					n := 1
					if cur != nil {
						n = cur.n + 1
					}
					if n == tc.failOn {
						return testPage{}, errFetch
					}
					if n == tc.cancelAfter {
						cancel()
					}
					return testPage{n: n, item: n}, nil
				},
			})

			before := testutil.ToFloat64(metrics.CloudPollTruncated.WithLabelValues(source))
			got, err := drainPager(ctx, source, pager, func(p testPage) []int { return []int{p.item} })

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(got) != tc.wantItems {
				t.Fatalf("items = %d, want %d", len(got), tc.wantItems)
			}
			if fetches != tc.wantFetches {
				t.Fatalf("fetches = %d, want %d", fetches, tc.wantFetches)
			}
			if d := testutil.ToFloat64(metrics.CloudPollTruncated.WithLabelValues(source)) - before; d != tc.wantTruncated {
				t.Fatalf("CloudPollTruncated delta = %v, want %v", d, tc.wantTruncated)
			}
		})
	}
}
