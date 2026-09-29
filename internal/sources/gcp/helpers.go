package gcp

import (
	"context"
	"fmt"

	"github.com/aryasoni98/alertkube/internal/sources"
)

// perProject binds one shared lister to every configured project, producing the
// per-scope listers sources.NewListSource polls. A GCP client is not scoped to
// the project it queries, so a single lister serves every project; each Scoped
// entry just fixes the project argument, which also becomes the alert-identity
// parent and the poll-error scope.
func perProject[T any](projects []string, list func(ctx context.Context, project string) ([]T, error)) []sources.Scoped[T] {
	scopes := make([]sources.Scoped[T], 0, len(projects))
	for _, project := range projects {
		scopes = append(scopes, sources.Scoped[T]{
			Scope: project,
			List:  func(ctx context.Context) ([]T, error) { return list(ctx, project) },
		})
	}
	return scopes
}

// buildProject returns a builder that constructs one service's Source when the
// service is enabled and at least one project is configured, and nil otherwise
// - which sources.Compact drops. It replaces the declare-slice /
// append-under-toggle pair every service repeated, so wiring a new service is a
// single table entry. Unlike AWS and Azure, a GCP client is not scoped to the
// project it queries, so one client serves every configured project and the
// builder takes no per-project constructor.
func buildProject(enabled bool, projects []string, newSource func() (sources.Source, error)) func() (sources.Source, error) {
	return func() (sources.Source, error) {
		if !enabled || len(projects) == 0 {
			return nil, nil
		}
		return newSource()
	}
}

// clientErr wraps an API client-construction failure with the service it was
// for, so a credential or permission problem is identifiable in the single line
// the controller logs before continuing without GCP.
func clientErr(service string, err error) error {
	return fmt.Errorf("gcp: %s client: %w", service, err)
}
