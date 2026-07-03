// Package shard implements static, hash-based ownership so alert evaluation and
// delivery can be distributed across N active replicas (A2 in the design doc).
//
// Model: every replica watches the whole cluster (informer caches are cheap
// relative to delivery), but a replica only *acts* on an object it owns, where
// ownership is a pure function of a stable object key:
//
//	owns(key) == (fnv32a(key) mod total == index)
//
// Because ownership is deterministic and depends only on the key, at any instant
// exactly one replica owns a given object - so no two replicas page for it. This
// is the same sharding model Prometheus/Thanos use. Rebalancing happens by
// changing total/index (a StatefulSet rollout), which is simpler and safer than
// a dynamic coordinator; the trade-off is that scaling requires a rollout.
//
// Sharding is disabled (own everything) unless total > 1, so a default single
// replica behaves exactly as before.
package shard

import "hash/fnv"

// Sharder decides whether the local replica owns a key. The zero value and nil
// both mean "own everything" (sharding disabled).
type Sharder struct {
	index int
	total int
}

// New returns a Sharder for replica index of total. total <= 1 disables
// sharding (owns everything) and the index is ignored. ok is false only when
// sharding is requested (total > 1) but index is out of range, so the caller
// can fail fast on a misconfigured shard set.
func New(index, total int) (s *Sharder, ok bool) {
	if total <= 1 {
		return &Sharder{index: 0, total: 1}, true
	}
	if index < 0 || index >= total {
		return nil, false
	}
	return &Sharder{index: index, total: total}, true
}

// Enabled reports whether sharding is active (total > 1).
func (s *Sharder) Enabled() bool { return s != nil && s.total > 1 }

// Index and Total expose the configuration for logging/metrics.
func (s *Sharder) Index() int { return s.shardOr().index }
func (s *Sharder) Total() int { return s.shardOr().total }

// Owns reports whether the local replica is responsible for key. When sharding
// is disabled it always returns true, so the ownership gate is a no-op on a
// single replica.
func (s *Sharder) Owns(key string) bool {
	if !s.Enabled() {
		return true
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	// Compute the modulo in uint64 to avoid a narrowing int->uint32 conversion
	// of s.total. total is a small, positive, operator-set value, but the wider
	// arithmetic is bounds-safe regardless (CWE-190/681).
	return int(uint64(h.Sum32())%uint64(s.total)) == s.index
}

func (s *Sharder) shardOr() *Sharder {
	if s == nil {
		return &Sharder{index: 0, total: 1}
	}
	return s
}
