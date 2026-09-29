package sources

import (
	"context"

	"github.com/aryasoni98/alertkube/internal/config"
)

// Bound is one provider resolved against a config. Build closes over that
// provider's own section (AWS, Azure, or GCP), not the rest of Config.
type Bound struct {
	Enabled     bool
	PollSeconds int
	// Build constructs the enabled sources. A construction error is logged
	// by the caller and the provider skipped, so a cloud-auth problem never
	// takes down the Kubernetes watchers.
	Build func(context.Context) ([]Source, error)
}

// Provider describes a cloud provider's source set (AWS, Azure, GCP, ...). Each
// provider package registers one in its init via RegisterProvider, so wiring a
// new cloud is a self-contained package - the controller iterates the registry
// instead of hardcoding each provider (mirrors the sink self-registration).
type Provider struct {
	// Name identifies the provider in logs (e.g. "aws").
	Name string
	// Bind reads this provider's section out of cfg.
	Bind func(*config.Config) Bound
}

// providers holds every registered cloud provider, populated by the provider
// packages' init functions at load.
var providers []Provider

// RegisterProvider adds a cloud provider to the registry. Called from each
// provider package's init.
func RegisterProvider(p Provider) { providers = append(providers, p) }

// Providers returns the registered cloud providers.
func Providers() []Provider { return providers }
