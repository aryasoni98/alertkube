package sources

import (
	"context"

	"alertkube/internal/config"
)

// Provider describes a cloud provider's source set (AWS, Azure, GCP, ...). Each
// provider package registers one in its init via RegisterProvider, so wiring a
// new cloud is a self-contained package - the controller iterates the registry
// instead of hardcoding each provider (mirrors the sink self-registration).
type Provider struct {
	// Name identifies the provider in logs (e.g. "aws").
	Name string
	// Enabled reports whether the provider is turned on in config.
	Enabled func(*config.Config) bool
	// PollSeconds is the provider's configured poll interval.
	PollSeconds func(*config.Config) int
	// Build constructs the enabled sources for the provider. A construction
	// error (bad credentials/config) is logged by the caller and the provider
	// skipped, so a cloud-auth problem never takes down the Kubernetes watchers.
	Build func(context.Context, *config.Config) ([]Source, error)
}

// providers holds every registered cloud provider, populated by the provider
// packages' init functions at load.
var providers []Provider

// RegisterProvider adds a cloud provider to the registry. Called from each
// provider package's init.
func RegisterProvider(p Provider) { providers = append(providers, p) }

// Providers returns the registered cloud providers.
func Providers() []Provider { return providers }
