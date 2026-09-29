package watchers

import (
	"testing"

	"github.com/aryasoni98/alertkube/v2/internal/config"
)

// TestNSFilterAllows covers the namespace filter shared by every
// namespace-scoped watcher.
func TestNSFilterAllows(t *testing.T) {
	tests := []struct {
		name      string
		watched   string
		ignored   string
		namespace string
		want      bool
	}{
		{
			name:      "no filters allow every namespace",
			namespace: "default",
			want:      true,
		},
		{
			name:      "ignoredNamespaces blocks matching namespace",
			ignored:   "kube-system",
			namespace: "kube-system",
			want:      false,
		},
		{
			name:      "ignoredNamespaces leaves other namespaces alone",
			ignored:   "kube-system",
			namespace: "default",
			want:      true,
		},
		{
			name:      "watchedNamespaces allows matching namespace",
			watched:   "prod",
			namespace: "prod",
			want:      true,
		},
		{
			name:      "watchedNamespaces blocks non-matching namespace",
			watched:   "prod",
			namespace: "dev",
			want:      false,
		},
		{
			name:      "ignoredNamespaces wins over watchedNamespaces",
			watched:   "prod",
			ignored:   "prod-canary",
			namespace: "prod-canary",
			want:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newNSFilter(config.Filters{
				WatchedNamespaces: tc.watched,
				IgnoredNamespaces: tc.ignored,
			})
			if got := f.allows(tc.namespace); got != tc.want {
				t.Errorf("allows(%q): got %v, want %v", tc.namespace, got, tc.want)
			}
		})
	}
}
