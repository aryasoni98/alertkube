package config

import "testing"

func shardedCfg(name string) *Config {
	c := &Config{}
	c.Persistence.Enabled = true
	c.Persistence.ConfigMapName = name
	return c
}

// An unsharded replica must keep the plain default: scoping it would orphan the
// state of every existing single-replica install on upgrade.
func TestApplyShardScopeNoOpWhenUnsharded(t *testing.T) {
	c := shardedCfg(DefaultStateConfigMap)
	if err := c.ApplyShardScope(0, false); err != nil {
		t.Fatalf("ApplyShardScope: %v", err)
	}
	if c.Persistence.ConfigMapName != DefaultStateConfigMap {
		t.Fatalf("ConfigMapName = %q, want the unscoped default", c.Persistence.ConfigMapName)
	}
}

// Persistence off means there is no object to collide over, so the name is
// irrelevant and an explicit shared name must not be rejected.
func TestApplyShardScopeNoOpWhenPersistenceDisabled(t *testing.T) {
	c := &Config{}
	c.Persistence.Enabled = false
	c.Persistence.ConfigMapName = "shared-state"
	if err := c.ApplyShardScope(2, true); err != nil {
		t.Fatalf("ApplyShardScope must not reject a name it will never write: %v", err)
	}
}

// The default name is rewritten per shard, so turning sharding on is safe
// without the operator also having to remember to re-scope state.
func TestApplyShardScopeScopesDefaultName(t *testing.T) {
	c := shardedCfg(DefaultStateConfigMap)
	if err := c.ApplyShardScope(2, true); err != nil {
		t.Fatalf("ApplyShardScope: %v", err)
	}
	if want := DefaultStateConfigMap + "-2"; c.Persistence.ConfigMapName != want {
		t.Fatalf("ConfigMapName = %q, want %q", c.Persistence.ConfigMapName, want)
	}
}

// The regression this whole change exists for: a shared, explicitly-set name
// under sharding means every replica's Save() overwrites the others' mute
// history and outbox. It must fail at startup, not in production.
func TestApplyShardScopeRejectsSharedExplicitName(t *testing.T) {
	c := shardedCfg("alertkube-prod-state")
	err := c.ApplyShardScope(1, true)
	if err == nil {
		t.Fatal("a shared state ConfigMap under sharding must be rejected: each shard would clobber the others")
	}
	// The message has to tell the operator what to type, not just that they are wrong.
	if got := err.Error(); !contains(got, "alertkube-prod-state-1") {
		t.Fatalf("error must suggest the shard-scoped name, got: %s", got)
	}
}

// An explicit name that already carries this shard's index is the operator
// having done the right thing by hand; accept it verbatim rather than
// rewriting it out from under their RBAC/NetworkPolicy.
func TestApplyShardScopeAcceptsExplicitShardScopedName(t *testing.T) {
	c := shardedCfg("alertkube-prod-state-3")
	if err := c.ApplyShardScope(3, true); err != nil {
		t.Fatalf("an already shard-scoped name must be accepted: %v", err)
	}
	if c.Persistence.ConfigMapName != "alertkube-prod-state-3" {
		t.Fatalf("ConfigMapName = %q, want it left untouched", c.Persistence.ConfigMapName)
	}
}

// A name scoped to the WRONG shard is the subtlest form of the collision (two
// shards pointed at one object), so it must be rejected too.
func TestApplyShardScopeRejectsWrongShardSuffix(t *testing.T) {
	c := shardedCfg("alertkube-state-0")
	if err := c.ApplyShardScope(1, true); err == nil {
		t.Fatal("a name scoped to another shard must be rejected")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
