package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"

	"github.com/aryasoni98/alertkube/internal/sources"
)

// fakeSource is a minimal Source used to assert what the builders returned.
type fakeSource struct{ name string }

func (s *fakeSource) Name() string                       { return s.name }
func (s *fakeSource) Poll(context.Context, sources.Emit) {}

func testRegions() []regionConfig {
	return []regionConfig{
		{region: "us-east-1", cfg: awssdk.Config{Region: "us-east-1"}},
		{region: "eu-west-1", cfg: awssdk.Config{Region: "eu-west-1"}},
	}
}

func TestRegionalSourceBuildsOneClientPerRegion(t *testing.T) {
	var built []string
	var got []regionClient[string]
	src := regionalSource(true, testRegions(),
		func(c awssdk.Config) string {
			built = append(built, c.Region)
			return "client-" + c.Region
		},
		func(rs []regionClient[string]) sources.Source {
			got = rs
			return &fakeSource{name: "svc"}
		})

	if src == nil {
		t.Fatal("enabled service must build a Source")
	}
	if len(built) != 2 || built[0] != "us-east-1" || built[1] != "eu-west-1" {
		t.Fatalf("client constructor called with %v, want both regions in order", built)
	}
	if len(got) != 2 || got[0].region != "us-east-1" || got[0].client != "client-us-east-1" {
		t.Fatalf("region clients = %+v, want each region paired with its own client", got)
	}
}

func TestRegionalSourceSkipsDisabledAndEmpty(t *testing.T) {
	never := func(awssdk.Config) string { t.Fatal("client constructor must not run"); return "" }
	wrap := func([]regionClient[string]) sources.Source { return &fakeSource{name: "svc"} }

	if src := regionalSource(false, testRegions(), never, wrap); src != nil {
		t.Fatal("disabled service must build nil so Compact drops it")
	}
	if src := regionalSource(true, nil, never, wrap); src != nil {
		t.Fatal("no configured regions must build nil")
	}
}

func TestGlobalSourceUsesFirstRegionOnly(t *testing.T) {
	calls := 0
	src := globalSource(true, testRegions(), func(c awssdk.Config) sources.Source {
		calls++
		return &fakeSource{name: c.Region}
	})
	if calls != 1 {
		t.Fatalf("global source built %d times, want 1 (an account-wide service must not re-alert per region)", calls)
	}
	if src.Name() != "us-east-1" {
		t.Fatalf("global source built from %q, want the first configured region", src.Name())
	}
	if globalSource(false, testRegions(), func(awssdk.Config) sources.Source { return nil }) != nil {
		t.Fatal("disabled global service must build nil")
	}
}
