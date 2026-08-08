package azure

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"

	"github.com/aryasoni98/alertkube/internal/alert"
)

type fakeRedisLister struct {
	caches []*armredis.ResourceInfo
	err    error
}

func (f *fakeRedisLister) List(context.Context) ([]*armredis.ResourceInfo, error) {
	return f.caches, f.err
}

func redisCache(name, location string, state armredis.ProvisioningState) *armredis.ResourceInfo {
	return &armredis.ResourceInfo{
		Name:       sp(name),
		Location:   sp(location),
		Properties: &armredis.Properties{ProvisioningState: &state},
	}
}

func TestEvaluateRedisCache(t *testing.T) {
	cases := []struct {
		name         string
		cache        *armredis.ResourceInfo
		wantEmit     bool
		wantResolved bool
		wantSeverity alert.Severity
	}{
		{"failed critical", redisCache("r", "eastus", armredis.ProvisioningStateFailed), true, false, alert.SeverityCritical},
		{"recovering warning", redisCache("r", "eastus", armredis.ProvisioningStateRecoveringScaleFailure), true, false, alert.SeverityWarning},
		{"succeeded resolves", redisCache("r", "eastus", armredis.ProvisioningStateSucceeded), true, true, ""},
		{"creating resolves", redisCache("r", "eastus", armredis.ProvisioningStateCreating), true, true, ""},
		{"nil properties resolves", &armredis.ResourceInfo{Name: sp("r"), Location: sp("eastus")}, true, true, ""},
		{"nil name skipped", &armredis.ResourceInfo{Location: sp("eastus")}, false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateRedisCache("sub-1", tc.cache, emit)
			if !tc.wantEmit {
				if len(*got) != 0 {
					t.Fatalf("expected no emit, got %d", len(*got))
				}
				return
			}
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindAzureRedis || a.Namespace != "sub-1/eastus" {
				t.Errorf("identity: kind=%s ns=%s", a.Kind, a.Namespace)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && a.Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", a.Severity, tc.wantSeverity)
			}
		})
	}
}

func TestAzureRedisSourcePoll(t *testing.T) {
	fake := &fakeRedisLister{caches: []*armredis.ResourceInfo{
		redisCache("good", "eastus", armredis.ProvisioningStateSucceeded),
		redisCache("bad", "westus", armredis.ProvisioningStateFailed),
	}}
	src := &azureRedisSource{subs: []azureRedisSubscription{{subscription: "sub-1", lister: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}
