package gcp

import (
	"context"
	"errors"
	"testing"

	"github.com/aryasoni98/alertkube/internal/sources"
)

// fakeSource is a minimal Source used to assert what buildProject returned.
type fakeSource struct{ name string }

func (s *fakeSource) Name() string                       { return s.name }
func (s *fakeSource) Poll(context.Context, sources.Emit) {}

func TestBuildProjectBuildsEnabledService(t *testing.T) {
	calls := 0
	src, err := buildProject(true, []string{"proj-a", "proj-b"}, func() (sources.Source, error) {
		calls++
		return &fakeSource{name: "svc"}, nil
	})()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if src == nil {
		t.Fatal("enabled service must build a Source")
	}
	// One GCP client serves every configured project, so it must be built
	// exactly once no matter how many projects are configured.
	if calls != 1 {
		t.Fatalf("client constructor ran %d times, want 1", calls)
	}
}

func TestBuildProjectSkipsDisabledAndEmpty(t *testing.T) {
	never := func() (sources.Source, error) {
		t.Fatal("client constructor must not run")
		return nil, nil
	}
	for _, tc := range []struct {
		name     string
		enabled  bool
		projects []string
	}{
		{"disabled", false, []string{"proj-a"}},
		{"no projects", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, err := buildProject(tc.enabled, tc.projects, never)()
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if src != nil {
				t.Fatal("must build nil so Compact drops it")
			}
		})
	}
}

// A client-construction failure must abort the provider build rather than
// silently yielding a provider that polls a subset of its configured services.
func TestBuildProjectPropagatesClientError(t *testing.T) {
	boom := errors.New("bad credential")
	src, err := buildProject(true, []string{"proj-a"}, func() (sources.Source, error) {
		return nil, clientErr("cluster manager", boom)
	})()
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the client-construction error", err)
	}
	if src != nil {
		t.Fatalf("src = %v, want nil on error", src)
	}
}

// projectSource must poll every configured project through one lister and hand
// each listed item to the service's evaluator, tagged with its own project.
func TestProjectSourcePollsEveryProject(t *testing.T) {
	type seen struct{ project, item string }
	var got []seen
	src := newProjectSource("gcp-test", []string{"proj-a", "proj-b"},
		projectListerFunc(func(_ context.Context, project string) ([]string, error) {
			return []string{project + "-1", project + "-2"}, nil
		}),
		func(project string, item string, _ sources.Emit) {
			got = append(got, seen{project, item})
		})

	if src.Name() != "gcp-test" {
		t.Fatalf("Name() = %q, want the name it was built with", src.Name())
	}
	emit, _ := collect()
	src.Poll(context.Background(), emit)

	want := []seen{
		{"proj-a", "proj-a-1"}, {"proj-a", "proj-a-2"},
		{"proj-b", "proj-b-1"}, {"proj-b", "proj-b-2"},
	}
	if len(got) != len(want) {
		t.Fatalf("evaluated %d items, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// projectListerFunc adapts a function to projectLister so the generic can be
// driven without a service-specific fake.
type projectListerFunc func(ctx context.Context, project string) ([]string, error)

func (f projectListerFunc) List(ctx context.Context, project string) ([]string, error) {
	return f(ctx, project)
}
