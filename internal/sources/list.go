package sources

import "context"

// Scoped pairs one provider scope (an Azure subscription, a GCP project) with
// the function that lists that scope's items. The scope is passed to eval as
// the parent of the alert identity and labels the poll-error log line.
type Scoped[T any] struct {
	Scope string
	List  func(ctx context.Context) ([]T, error)
}

// listSource is a Source whose poll is "list items for every scope, evaluate
// each item". Every Azure and GCP service has exactly that shape, so this owns
// the struct / Name / Poll triple each of them used to repeat, and a service
// supplies only its per-scope listers and its evaluate function - the same
// collapse watchers.simple performs for the Kubernetes watchers.
type listSource[T any] struct {
	name   string
	scopes []Scoped[T]
	eval   func(scope string, item T, emit Emit)
}

// NewListSource binds one service's name, per-scope listers, and evaluator.
// Binding the name once means Name and the poll-error metric label can never
// disagree. A scope whose list fails is recorded via PollErr and skipped, so
// its resources are neither re-fired nor resolved this poll; the other scopes
// still poll.
func NewListSource[T any](name string, scopes []Scoped[T], eval func(scope string, item T, emit Emit)) Source {
	return &listSource[T]{name: name, scopes: scopes, eval: eval}
}

func (s *listSource[T]) Name() string { return s.name }

func (s *listSource[T]) Poll(ctx context.Context, emit Emit) {
	for _, sc := range s.scopes {
		if ctx.Err() != nil {
			return
		}
		items, err := sc.List(ctx)
		if err != nil {
			PollErr(s.name, sc.Scope, err)
			continue
		}
		for i := range items {
			if ctx.Err() != nil {
				return
			}
			s.eval(sc.Scope, items[i], emit)
		}
	}
}
