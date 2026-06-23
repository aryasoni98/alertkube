package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"

	"alertkube/internal/alert"
)

type fakeElastiCache struct {
	pages []*elasticache.DescribeCacheClustersOutput
	idx   int
	err   error
}

func (f *fakeElastiCache) DescribeCacheClusters(_ context.Context, _ *elasticache.DescribeCacheClustersInput, _ ...func(*elasticache.Options)) (*elasticache.DescribeCacheClustersOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.pages[f.idx]
	if f.idx < len(f.pages)-1 {
		f.idx++
	}
	return out, nil
}

func cacheCluster(id, status string) ectypes.CacheCluster {
	return ectypes.CacheCluster{
		CacheClusterId:     awssdk.String(id),
		CacheClusterStatus: awssdk.String(status),
		Engine:             awssdk.String("redis"),
	}
}

func TestEvaluateCacheCluster(t *testing.T) {
	cases := []struct {
		name         string
		id           string
		status       string
		wantEmit     bool
		wantResolved bool
	}{
		{"incompatible-network critical", "c1", "incompatible-network", true, false},
		{"restore-failed critical", "c2", "restore-failed", true, false},
		{"available resolves", "c3", "available", true, true},
		{"modifying resolves", "c4", "modifying", true, true},
		{"empty id skipped", "", "restore-failed", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateCacheCluster("us-west-2", tc.id, tc.status, "redis", emit)
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
			if a.Kind != alert.KindElastiCacheCluster {
				t.Errorf("kind = %s, want ElastiCacheCluster", a.Kind)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && a.Severity != alert.SeverityCritical {
				t.Errorf("severity = %q, want critical", a.Severity)
			}
		})
	}
}

func TestElastiCacheSourcePollPaginates(t *testing.T) {
	page1 := &elasticache.DescribeCacheClustersOutput{
		CacheClusters: []ectypes.CacheCluster{cacheCluster("c-bad", "restore-failed")},
		Marker:        awssdk.String("more"),
	}
	page2 := &elasticache.DescribeCacheClustersOutput{
		CacheClusters: []ectypes.CacheCluster{cacheCluster("c-good", "available")},
	}
	fake := &fakeElastiCache{pages: []*elasticache.DescribeCacheClustersOutput{page1, page2}}
	src := &elastiCacheSource{regions: []ecRegion{{region: "us-west-2", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts across 2 pages, got %d", len(*got))
	}
	for _, a := range *got {
		switch a.Name {
		case "c-bad":
			if a.Resolved || a.Severity != alert.SeverityCritical {
				t.Errorf("c-bad should be critical firing: %+v", a)
			}
		case "c-good":
			if !a.Resolved {
				t.Errorf("c-good should resolve: %+v", a)
			}
		default:
			t.Errorf("unexpected cluster %q", a.Name)
		}
	}
}
