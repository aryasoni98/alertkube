package azure

import (
	"context"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertsmanagement/armalertsmanagement"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/sources"
)

// TestSourceNames pins the Name() of every Azure source to its literal value.
// The names are CloudPollErrors label values, so a rename that would break
// dashboards and docs is caught.
func TestSourceNames(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{newAKSSource(nil).Name(), "azure-aks"},
		{newAzureMonitorSource(nil).Name(), "azure-monitor"},
		{newAzureVMSource(nil).Name(), "azure-vm"},
		{newAzureStorageSource(nil).Name(), "azure-storage"},
		{newAzureSQLSource(nil).Name(), "azure-sql"},
		{newAzureRedisSource(nil).Name(), "azure-redis"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("source name %q != %q", c.got, c.want)
		}
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
		newAKSSource(fakeLister[*armcontainerservice.ManagedCluster]("s", nil, boom)),
		newAzureMonitorSource(fakeLister[*armalertsmanagement.Alert]("s", nil, boom)),
		newAzureVMSource(fakeLister[*armcompute.VirtualMachine]("s", nil, boom)),
		newAzureStorageSource(fakeLister[*armstorage.Account]("s", nil, boom)),
		newAzureSQLSource(fakeLister[sqlDatabase]("s", nil, boom)),
		newAzureRedisSource(fakeLister[*armredis.ResourceInfo]("s", nil, boom)),
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
