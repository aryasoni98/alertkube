package shard

import (
	"fmt"

	"github.com/aryasoni98/alertkube/internal/env"
)

// Env var names for the static shard assignment. They live beside the ownership
// model they configure because the shard identity is consumed at three points
// that are wired at different moments in startup - the leader Lease name, the
// persisted-state ConfigMap name, and the emit-path ownership gate - and a
// single parse is what keeps those three from disagreeing. Two of them
// (Lease, ConfigMap) are resolved before the controller body runs, which is why
// this cannot stay buried in the controller.
const (
	EnvTotal = "ALERTKUBE_SHARD_TOTAL"
	EnvIndex = "ALERTKUBE_SHARD_INDEX"
)

// FromEnv builds the Sharder from ALERTKUBE_SHARD_TOTAL / ALERTKUBE_SHARD_INDEX.
// The defaults (total 1) disable sharding, so an unset environment yields a
// replica that owns everything - the unchanged single-replica behavior.
//
// An out-of-range index returns an error rather than a silently-degraded
// Sharder: a replica that owns nothing looks perfectly healthy (informers sync,
// /readyz is green, no errors are logged) while paging for its whole share of
// the cluster stops. That must fail at startup, not in production.
func FromEnv() (*Sharder, error) {
	total := env.IntOr(EnvTotal, 1)
	index := env.IntOr(EnvIndex, 0)
	s, ok := New(index, total)
	if !ok {
		return nil, fmt.Errorf("invalid sharding config: %s=%d must be in [0,%d)", EnvIndex, index, total)
	}
	return s, nil
}
