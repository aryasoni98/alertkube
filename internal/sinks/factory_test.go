package sinks

import (
	"sort"
	"testing"
)

func TestRegisteredNamesMatchBuildDefault(t *testing.T) {
	names := RegisteredNames()
	if len(names) == 0 {
		t.Fatal("no sinks registered; each sink should self-register in its init")
	}
	// RegisteredNames must be sorted.
	if !sort.StringsAreSorted(names) {
		t.Fatalf("RegisteredNames not sorted: %v", names)
	}
	// BuildDefault must construct exactly the registered set.
	reg := BuildDefault(SinkConfig{Cluster: "test"})
	got := reg.Names()
	if len(got) != len(names) {
		t.Fatalf("BuildDefault built %d sinks, %d registered", len(got), len(names))
	}
	for i := range names {
		if got[i] != names[i] {
			t.Fatalf("BuildDefault[%d]=%q != registered %q", i, got[i], names[i])
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
