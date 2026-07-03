package app

import (
	"fmt"
	"testing"

	"alertkube/internal/alert"
	"alertkube/internal/shard"
)

func TestShardGateForwardsOnlyOwned(t *testing.T) {
	s, _ := shard.New(0, 3) // this replica is shard 0 of 3
	var got int
	gated := shardGate(func(*alert.Alert) { got++ }, s)

	owned := 0
	const n = 90
	for i := 0; i < n; i++ {
		a := alert.New(alert.KindPod, "ns", fmt.Sprintf("p%d", i), "X", alert.SeverityInfo)
		if s.Owns(shardKey(a)) {
			owned++
		}
		gated(a)
	}
	if got != owned {
		t.Fatalf("gate forwarded %d alerts, want exactly the %d owned", got, owned)
	}
	if owned == 0 || owned == n {
		t.Fatalf("expected a partition of the %d keys, got %d owned (sharding not partitioning)", n, owned)
	}
}

func TestShardGateNoopWhenDisabled(t *testing.T) {
	s, _ := shard.New(0, 1) // disabled
	got := 0
	gated := shardGate(func(*alert.Alert) { got++ }, s)
	for i := 0; i < 10; i++ {
		gated(alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo))
	}
	if got != 10 {
		t.Fatalf("disabled sharding must forward every alert, got %d/10", got)
	}
}

func TestShardKeyIsObjectIdentity(t *testing.T) {
	// Two alerts on the same object with different reasons must share a key so
	// they are owned by the same replica (the key is identity, not fingerprint).
	a := alert.New(alert.KindPod, "ns", "web", "CrashLoopBackOff", alert.SeverityCritical)
	b := alert.New(alert.KindPod, "ns", "web", "OOMKilled", alert.SeverityCritical)
	if shardKey(a) != shardKey(b) {
		t.Fatalf("same object must map to one shard key: %q != %q", shardKey(a), shardKey(b))
	}
}
