package sinks

import (
	"slices"
	"sort"
	"testing"
)

// BuildDefault is the single source of truth for which sinks exist, so it must
// construct exactly the set of self-registered factories - no factory silently
// skipped (its sink would be unreachable by routing) and none invented. Names
// must also come back sorted, since callers (the console channel list, the
// KnownSinks guard) compare it positionally.
func TestBuildDefaultConstructsEveryRegisteredFactory(t *testing.T) {
	if len(factories) == 0 {
		t.Fatal("no sinks registered; each sink should self-register in its init")
	}
	got := BuildDefault(SinkConfig{Cluster: "test"}).Names()
	if !sort.StringsAreSorted(got) {
		t.Fatalf("Names not sorted: %v", got)
	}
	if len(got) != len(factories) {
		t.Fatalf("BuildDefault built %d sinks, %d factories registered", len(got), len(factories))
	}
	for _, name := range got {
		if _, ok := factories[name]; !ok {
			t.Errorf("BuildDefault built %q, which no factory registered", name)
		}
	}
	for name := range factories {
		if !slices.Contains(got, name) {
			t.Errorf("factory %q registered but BuildDefault did not construct it", name)
		}
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	// "slack" is already registered by slack.go's init; re-registering must
	// panic (before mutating the map) rather than silently shadowing it.
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate sink registration must panic")
		}
	}()
	Register("slack", func(SinkConfig) Sink { return nil })
}
