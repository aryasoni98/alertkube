package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceElastiCache = "aws-elasticache"

type ecRegion = regionClient[elastiCacheAPI]

// elastiCacheSource alerts on ElastiCache clusters whose status indicates a
// problem. incompatible-network and restore-failed are critical; everything
// else - "available" plus transient states (creating / modifying / rebooting /
// snapshotting) - resolves. ElastiCache reports status as a free-form string
// and paginates with Marker on both request and response.
type elastiCacheSource struct {
	regions []ecRegion
}

func (s *elastiCacheSource) Name() string { return sourceElastiCache }

func (s *elastiCacheSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByRegion(ctx, s.regions, emit, s.pollRegion)
}

func (s *elastiCacheSource) pollRegion(ctx context.Context, rc ecRegion, emit sources.Emit) {
	forEachPage(ctx, sourceElastiCache, rc.region, func(ctx context.Context, marker *string) (*string, error) {
		out, err := rc.client.DescribeCacheClusters(ctx, &elasticache.DescribeCacheClustersInput{Marker: marker})
		if err != nil {
			return nil, err
		}
		for i := range out.CacheClusters {
			cc := out.CacheClusters[i]
			evaluateCacheCluster(rc.region, awssdk.ToString(cc.CacheClusterId),
				awssdk.ToString(cc.CacheClusterStatus), awssdk.ToString(cc.Engine), emit)
		}
		return out.Marker, nil
	})
}

func evaluateCacheCluster(region, id, status, engine string, emit sources.Emit) {
	if id == "" {
		return
	}
	switch status {
	case "incompatible-network", "restore-failed":
		emitFiring(emit, alert.KindElastiCacheCluster, region, id, "ElastiCacheClusterUnhealthy",
			"ElastiCache cluster "+id+" status is "+status, alert.SeverityCritical,
			map[string]string{"status": status, "engine": engine})
	default:
		emitResolve(emit, alert.KindElastiCacheCluster, region, id)
	}
}
