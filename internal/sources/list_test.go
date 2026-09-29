package sources

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/internal/metrics"
)

// A scope whose list fails is recorded under the source's own name and
// skipped; the remaining scopes still poll, each item tagged with its scope.
func TestListSourceSkipsFailedScope(t *testing.T) {
	const name = "sources-test-list"
	boom := errors.New("ListFailed")
	type seen struct{ scope, item string }
	var got []seen
	src := NewListSource(name, []Scoped[string]{
		{Scope: "a", List: func(context.Context) ([]string, error) { return []string{"a-1", "a-2"}, nil }},
		{Scope: "bad", List: func(context.Context) ([]string, error) { return nil, boom }},
		{Scope: "c", List: func(context.Context) ([]string, error) { return []string{"c-1"}, nil }},
	}, func(scope, item string, _ Emit) { got = append(got, seen{scope, item}) })

	if src.Name() != name {
		t.Fatalf("Name() = %q, want %q", src.Name(), name)
	}
	before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(name))
	src.Poll(context.Background(), discard)

	want := []seen{{"a", "a-1"}, {"a", "a-2"}, {"c", "c-1"}}
	if len(got) != len(want) {
		t.Fatalf("evaluated %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if after := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(name)); after-before != 1 {
		t.Fatalf("CloudPollErrors[%s] delta = %v, want 1", name, after-before)
	}
}

// A cancelled poll stops before listing the next scope and before evaluating
// the next item.
func TestListSourceStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listed, evaluated := 0, 0
	list := func(context.Context) ([]int, error) { listed++; return []int{1, 2}, nil }
	src := NewListSource("sources-test-cancel", []Scoped[int]{{Scope: "a", List: list}, {Scope: "b", List: list}},
		func(string, int, Emit) { evaluated++; cancel() })

	src.Poll(ctx, discard)
	if listed != 1 || evaluated != 1 {
		t.Fatalf("listed %d scopes and evaluated %d items after cancel, want 1 and 1", listed, evaluated)
	}
}

func TestBuildAll(t *testing.T) {
	a := funcSource{name: "a", poll: func(context.Context, Emit) {}}
	ok := func() (Source, error) { return a, nil }
	disabled := func() (Source, error) { return nil, nil }

	got, err := BuildAll(disabled, ok, disabled)
	if err != nil || len(got) != 1 || got[0].Name() != "a" {
		t.Fatalf("BuildAll = %v, %v; want the one enabled source with disabled ones dropped", got, err)
	}

	boom := errors.New("bad credential")
	never := func() (Source, error) { t.Fatal("builders after a failure must not run"); return nil, nil }
	got, err = BuildAll(ok, func() (Source, error) { return nil, boom }, never)
	if !errors.Is(err, boom) || got != nil {
		t.Fatalf("BuildAll = %v, %v; want nil and the construction error", got, err)
	}
}
