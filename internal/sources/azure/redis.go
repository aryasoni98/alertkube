package azure

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const sourceAzureRedis = "azure-redis"

// armRedisLister lists Redis caches in one subscription by draining the SDK
// pager; tests provide a fake returning a canned slice.
type armRedisLister struct {
	client *armredis.Client
}

func (l *armRedisLister) List(ctx context.Context) ([]*armredis.ResourceInfo, error) {
	return drainPager(ctx, sourceAzureRedis, l.client.NewListBySubscriptionPager(nil),
		func(r armredis.ClientListBySubscriptionResponse) []*armredis.ResourceInfo { return r.Value })
}

// newAzureRedisSource alerts on Azure Cache for Redis instances in a bad
// provisioning state - the Azure analog of the AWS ElastiCache source. Failed
// is critical; RecoveringScaleFailure is a warning (a scale op failed but is
// self-healing); Succeeded plus transient states (Creating, Updating, Scaling,
// Deleting, ...) resolve.
func newAzureRedisSource(subs []sources.Scoped[*armredis.ResourceInfo]) sources.Source {
	return sources.NewListSource(sourceAzureRedis, subs, evaluateRedisCache)
}

func evaluateRedisCache(subscription string, c *armredis.ResourceInfo, emit sources.Emit) {
	if c == nil || c.Name == nil {
		return
	}
	name := *c.Name
	scope := sources.Scope(subscription, strVal(c.Location))
	state := ""
	if c.Properties != nil && c.Properties.ProvisioningState != nil {
		state = string(*c.Properties.ProvisioningState)
	}
	switch state {
	case string(armredis.ProvisioningStateFailed):
		emitFiring(emit, alert.KindAzureRedis, scope, name, "AzureRedisFailed",
			"Azure Redis cache "+name+" provisioning state is Failed", alert.SeverityCritical,
			map[string]string{"provisioningState": state})
	case string(armredis.ProvisioningStateRecoveringScaleFailure):
		emitFiring(emit, alert.KindAzureRedis, scope, name, "AzureRedisScaleRecovering",
			"Azure Redis cache "+name+" is recovering from a scale failure", alert.SeverityWarning,
			map[string]string{"provisioningState": state})
	default:
		emitResolve(emit, alert.KindAzureRedis, scope, name)
	}
}
