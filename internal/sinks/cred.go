package sinks

import (
	"context"
	"os"
)

type credOverrideKey struct{}

// WithCreds returns a context carrying per-request credential overrides keyed by
// the credential's environment-variable name. It lets the console's
// Secret-reference channel test inject a credential (read from a referenced
// Secret) for a single Send, without mutating process environment - which would
// be global and racy. Sinks read their credential via cred, which prefers an
// override and otherwise falls back to the environment, so normal delivery is
// completely unaffected.
func WithCreds(ctx context.Context, overrides map[string]string) context.Context {
	return context.WithValue(ctx, credOverrideKey{}, overrides)
}

// cred returns the per-request override for env, or os.Getenv(env) when there is
// no override. An empty override is treated as absent.
func cred(ctx context.Context, env string) string {
	if ctx != nil {
		if m, ok := ctx.Value(credOverrideKey{}).(map[string]string); ok {
			if v := m[env]; v != "" {
				return v
			}
		}
	}
	return os.Getenv(env)
}
