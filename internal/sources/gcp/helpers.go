package gcp

import (
	"context"
	"fmt"

	"github.com/aryasoni98/alertkube/internal/sources"
)

// projectLister lists items of type T for one project. Every GCP source's
// lister satisfies this shape, so pollByProject can drive any of them.
type projectLister[T any] interface {
	List(ctx context.Context, project string) ([]T, error)
}

// pollByProject lists items per project - recording a poll error and skipping
// that project on failure - then runs eval for each item. It replaces the
// identical projects/list/pollErr/iterate loop every GCP source duplicated, so
// that shape lives in exactly one place.
func pollByProject[T any, L projectLister[T]](ctx context.Context, source string, projects []string, lister L, emit sources.Emit, eval func(project string, item T, emit sources.Emit)) {
	for _, project := range projects {
		items, err := lister.List(ctx, project)
		if err != nil {
			pollErr(source, project, err)
			continue
		}
		for i := range items {
			eval(project, items[i], emit)
		}
	}
}

// projectSource is a sources.Source whose poll is "list items for every
// configured project, evaluate each item". Every GCP service has exactly that
// shape, so this owns the struct / Name / Poll triple each of them used to
// repeat and a service supplies only its lister and its evaluate function -
// the same collapse watchers.simple performs for the Kubernetes watchers. Each
// service declares a type alias of one instantiation (see its own file) so its
// concrete item and lister types stay named at the point of use.
type projectSource[T any, L projectLister[T]] struct {
	name     string
	projects []string
	lister   L
	eval     func(project string, item T, emit sources.Emit)
}

// newProjectSource binds one service's name, projects, lister, and evaluator.
// Constructing through it rather than a struct literal means a service can
// never be wired with an unset name - which would mislabel its poll-error
// metric - or a nil eval, which would panic on the first listed item.
func newProjectSource[T any, L projectLister[T]](
	name string,
	projects []string,
	lister L,
	eval func(project string, item T, emit sources.Emit),
) *projectSource[T, L] {
	return &projectSource[T, L]{name: name, projects: projects, lister: lister, eval: eval}
}

func (s *projectSource[T, L]) Name() string { return s.name }

func (s *projectSource[T, L]) Poll(ctx context.Context, emit sources.Emit) {
	pollByProject(ctx, s.name, s.projects, s.lister, emit, s.eval)
}

// sourceBuilder defers one service's construction so NewProvider can list every
// service in a single table and run them all through one error check. Deferring
// also means a disabled service never constructs its API client, so its
// credentials are never resolved.
type sourceBuilder func() (sources.Source, error)

// buildProject returns a builder that constructs one service's Source when the
// service is enabled and at least one project is configured, and nil otherwise
// - which sources.Compact drops. It replaces the declare-slice /
// append-under-toggle pair every service repeated, so wiring a new service is a
// single table entry. Unlike AWS and Azure, a GCP client is not scoped to the
// project it queries, so one client serves every configured project and the
// builder takes no per-project constructor.
func buildProject(enabled bool, projects []string, newSource func() (sources.Source, error)) sourceBuilder {
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
