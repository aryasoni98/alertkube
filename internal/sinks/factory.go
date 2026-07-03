package sinks

import (
	"sort"

	"alertkube/internal/alert"
)

// SinkConfig carries the non-secret, process-wide wiring a sink factory needs
// at construction. Per-sink credentials (webhook URLs, tokens, routing keys)
// are deliberately NOT here: they are read from the environment on each Send so
// a Secret rotation is honored without a restart. Only settings that are fixed
// for the process lifetime (the cluster label, the Slack per-severity channels)
// live here.
type SinkConfig struct {
	Cluster  string
	Channels map[alert.Severity]string
}

// Factory constructs a sink from the process wiring. Each sink registers one
// via Register in its own init, so adding a sink is a single self-contained
// file - no edits to a central build list.
type Factory func(SinkConfig) Sink

// factories holds every registered sink factory, keyed by sink name. Populated
// by the sinks' init functions at package load, so it is fully built before
// BuildDefault or RegisteredNames is ever called.
var factories = map[string]Factory{}

// Register adds a sink factory under name. It panics on a duplicate name: two
// sinks answering to the same routing name is a programming error that must
// surface at startup, not silently shadow one another.
func Register(name string, f Factory) {
	if _, dup := factories[name]; dup {
		panic("sinks: duplicate registration for sink " + name)
	}
	factories[name] = f
}

// BuildDefault constructs a Registry containing every registered sink. It is
// the single source of truth for "which sinks exist"; buildSinks (in the app
// package) applies per-sink rate overrides on top.
func BuildDefault(cfg SinkConfig) *Registry {
	reg := NewRegistry()
	for _, f := range factories {
		reg.Add(f(cfg))
	}
	return reg
}

// RegisteredNames returns the registered sink names, sorted. config validation
// pins this against its KnownSinks set (guard test) so the two cannot drift.
func RegisteredNames() []string {
	out := make([]string, 0, len(factories))
	for name := range factories {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
