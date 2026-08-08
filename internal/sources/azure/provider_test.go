package azure

import (
	"context"
	"errors"
	"testing"

	"github.com/aryasoni98/alertkube/internal/sources"
)

// fakeSource is a minimal Source used to assert what buildSub returned.
type fakeSource struct{ name string }

func (s *fakeSource) Name() string                       { return s.name }
func (s *fakeSource) Poll(context.Context, sources.Emit) {}

func TestBuildSubBuildsOneListerPerSubscription(t *testing.T) {
	var got []subLister[string]
	build := buildSub(true, []string{"sub-a", "sub-b"},
		func(sub string) (string, error) { return "lister-" + sub, nil },
		func(ls []subLister[string]) sources.Source {
			got = ls
			return &fakeSource{name: "svc"}
		})

	src, err := build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if src == nil {
		t.Fatal("enabled service must build a Source")
	}
	if len(got) != 2 || got[0].subscription != "sub-a" || got[0].lister != "lister-sub-a" {
		t.Fatalf("subscription listers = %+v, want each subscription paired with its own lister", got)
	}
}

func TestBuildSubSkipsDisabledAndEmpty(t *testing.T) {
	never := func(string) (string, error) { t.Fatal("lister constructor must not run"); return "", nil }
	wrap := func([]subLister[string]) sources.Source { return &fakeSource{name: "svc"} }

	for _, tc := range []struct {
		name    string
		enabled bool
		subs    []string
	}{
		{"disabled", false, []string{"sub-a"}},
		{"no subscriptions", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, err := buildSub(tc.enabled, tc.subs, never, wrap)()
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
// silently yielding a Source that polls a subset of the subscriptions.
func TestBuildSubPropagatesClientError(t *testing.T) {
	boom := errors.New("bad credential")
	src, err := buildSub(true, []string{"sub-a"},
		func(string) (string, error) { return "", boom },
		func([]subLister[string]) sources.Source { t.Fatal("Source must not be built"); return nil })()
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the client-construction error", err)
	}
	if src != nil {
		t.Fatalf("src = %v, want nil on error", src)
	}
}
