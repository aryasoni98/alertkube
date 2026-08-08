package persist

import (
	"context"

	"github.com/aryasoni98/alertkube/internal/alert"
)

// Store is the contract a state backend must satisfy. The controller holds one
// to survive a restart without losing pending resolves (which would dangle a
// stateful incident) or mute history (whose loss re-pages every standing
// condition).
//
// It is an interface rather than the concrete ConfigMapStore because the
// default backend has a hard ceiling: a ConfigMap object is capped near 1 MiB,
// which after gzip is roughly 8k-15k active alerts (see maxSnapshotBytes). Past
// that, saves are skipped and persisted state silently goes stale. Growing past
// that ceiling means a different backend, not a bigger ConfigMap.
//
// Contract for an implementation:
//
//   - Load returns (nil, nil) when no state has been stored yet. That is the
//     cold-start path and must not be an error.
//   - Load returns an error for stored-but-unreadable state (corrupt, wrong
//     format). The caller logs it and starts cold rather than failing startup.
//   - Save must be safe against concurrent writers. During a leader handoff the
//     outgoing and incoming leaders can both write, and a naive
//     read-modify-write silently drops one of the two snapshots.
//   - Save may refuse a snapshot that exceeds the backend's size limit,
//     reporting an error. Skipping one save is preferable to wedging every
//     subsequent update.
//
// Implementations must be safe for concurrent use: the sweeper saves on its own
// goroutine while shutdown may issue a final save.
type Store interface {
	// Load returns the stored snapshot, or (nil, nil) when none exists.
	Load(ctx context.Context) (*alert.Snapshot, error)
	// Save writes the snapshot, replacing any previous one.
	Save(ctx context.Context, snap *alert.Snapshot) error
}

// ConfigMapStore is the default Store: it keeps state in a single
// gzip-compressed ConfigMap in the controller's own namespace (ADR-0003 - no
// external dependency to operate a Kubernetes controller).
var _ Store = (*ConfigMapStore)(nil)
