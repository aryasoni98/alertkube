package azure

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/sources"
)

// TestSourceNames pins the Name() of every Azure source so a rename that would
// break metric labels / docs is caught.
func TestSourceNames(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{(&aksSource{}).Name(), sourceAKS},
		{(&azureMonitorSource{}).Name(), sourceAzureMonitor},
		{(&azureVMSource{}).Name(), sourceAzureVM},
		{(&azureStorageSource{}).Name(), sourceAzureStorage},
		{(&azureSQLSource{}).Name(), sourceAzureSQL},
		{(&azureRedisSource{}).Name(), sourceAzureRedis},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("source name %q != %q", c.got, c.want)
		}
	}
}

// TestPollErrIncrementsMetric asserts a poll failure is observable on the shared
// metric and never panics.
func TestPollErrIncrementsMetric(t *testing.T) {
	const src = "azure-test-pollerr"
	before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(src))
	pollErr(src, "sub-1", errors.New("AuthorizationFailed"))
	if after := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(src)); after != before+1 {
		t.Fatalf("CloudPollErrors not incremented: before=%v after=%v", before, after)
	}
}

func TestStrVal(t *testing.T) {
	if strVal(nil) != "" {
		t.Error("nil pointer should be empty string")
	}
	s := "hi"
	if strVal(&s) != "hi" {
		t.Error("pointer deref wrong")
	}
}

// TestSourcesRecordListErrors drives every source's Poll with a lister that
// errors, asserting it records the failure (CloudPollErrors increments), emits
// nothing, and does not panic - the "a blinded cloud source must not take down
// the watchers" contract.
func TestSourcesRecordListErrors(t *testing.T) {
	boom := errors.New("ListFailed")
	subs := []sources.Source{
		&aksSource{subs: []aksSubscription{{subscription: "s", lister: &fakeAKSLister{err: boom}}}},
		&azureMonitorSource{subs: []azureMonitorSubscription{{subscription: "s", lister: &fakeAlertsLister{err: boom}}}},
		&azureVMSource{subs: []azureVMSubscription{{subscription: "s", lister: &fakeVMLister{err: boom}}}},
		&azureStorageSource{subs: []azureStorageSubscription{{subscription: "s", lister: &fakeStorageLister{err: boom}}}},
		&azureSQLSource{subs: []azureSQLSubscription{{subscription: "s", lister: &fakeSQLLister{err: boom}}}},
		&azureRedisSource{subs: []azureRedisSubscription{{subscription: "s", lister: &fakeRedisLister{err: boom}}}},
	}
	for _, s := range subs {
		t.Run(s.Name(), func(t *testing.T) {
			before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(s.Name()))
			emit, got := collect()
			s.Poll(context.Background(), emit) // must not panic
			if len(*got) != 0 {
				t.Fatalf("list error must emit nothing, got %d", len(*got))
			}
			if after := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(s.Name())); after != before+1 {
				t.Fatalf("CloudPollErrors[%s] not incremented: %v -> %v", s.Name(), before, after)
			}
		})
	}
}
