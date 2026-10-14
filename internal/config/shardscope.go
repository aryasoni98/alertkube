package config

import (
	"fmt"
	"strings"
)

// DefaultStateConfigMap is the persisted-state ConfigMap name used when the
// config does not name one. Exported because ApplyShardScope treats a name
// equal to it as not chosen and rewrites it per shard. applyEnvDefaults has
// already filled an empty name with it, so an explicit `configMapName:
// alertkube-state` cannot be told apart and is rewritten too.
const DefaultStateConfigMap = "alertkube-state"

// ApplyShardScope binds the persisted-state ConfigMap to this replica's shard.
//
// Sharded replicas MUST NOT share a state ConfigMap. Each replica's Export()
// contains only the alerts it owns, so a shared object means every Save()
// overwrites the other shards' mute history and delivery outbox
// (RetryOnConflict makes this last-write-wins, not a merge). The visible
// symptoms are a re-paging storm after any restart - because the mute records
// for N-1 shards were erased - and an outbox replay that delivers another
// shard's backlog.
//
// Two cases:
//
//   - Name equal to the default, whether omitted or written out: rewrite it
//     to "<default>-<index>", so enabling sharding is safe without also
//     having to remember this.
//   - Any other name: refuse unless it carries this replica's shard index
//     as a suffix. Silently rewriting an operator's chosen name would be worse
//     (it would not match the RBAC/NetworkPolicy they wrote around it), and
//     silently accepting it reintroduces the clobbering.
//
// A no-op when sharding is off or persistence is disabled, so the
// single-replica path is unchanged.
func (c *Config) ApplyShardScope(index int, sharded bool) error {
	if !sharded || !c.Persistence.Enabled {
		return nil
	}
	suffix := fmt.Sprintf("-%d", index)
	if c.Persistence.ConfigMapName == DefaultStateConfigMap {
		c.Persistence.ConfigMapName = DefaultStateConfigMap + suffix
		return nil
	}
	if !strings.HasSuffix(c.Persistence.ConfigMapName, suffix) {
		return fmt.Errorf(
			"persistence.configMapName %q is shared across shards: with sharding enabled every replica would overwrite the others' mute history and delivery outbox on each save. "+
				"Give each shard its own object by ending the name with its shard index (e.g. %q for shard %d), or leave persistence.configMapName empty to have it scoped automatically",
			c.Persistence.ConfigMapName, c.Persistence.ConfigMapName+suffix, index)
	}
	return nil
}
