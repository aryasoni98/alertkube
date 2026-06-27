package alert

import (
	"encoding/json"
	"testing"
	"time"
)

// FuzzComputeFingerprint asserts the fingerprint invariants hold for arbitrary
// inputs: it is always 12 lowercase-hex characters and is deterministic. A
// regression here (e.g. a panic on odd bytes, or a length change) would silently
// break dedup, suppression, grouping, and persisted-state matching.
func FuzzComputeFingerprint(f *testing.F) {
	f.Add("Pod", "default", "web-0", "CrashLoopBackOff")
	f.Add("Node", "kube-system", "node|with|pipes", "NodeNotReady")
	f.Add("", "", "", "")
	f.Add("Pod", "ns", "name", "résumé-OOMKilled-💥")

	f.Fuzz(func(t *testing.T, kind, ns, name, reason string) {
		got := ComputeFingerprint(Kind(kind), ns, name, reason)
		if len(got) != 12 {
			t.Fatalf("fingerprint length = %d, want 12 (inputs %q/%q/%q/%q -> %q)",
				len(got), kind, ns, name, reason, got)
		}
		for i, c := range got {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				t.Fatalf("non-hex byte %q at %d in fingerprint %q", c, i, got)
			}
		}
		if again := ComputeFingerprint(Kind(kind), ns, name, reason); again != got {
			t.Fatalf("non-deterministic fingerprint: %q != %q", got, again)
		}
	})
}

// FuzzMatchOrRegex exercises the anchored-regex matcher with arbitrary patterns.
// The contract: never panic, and an invalid pattern must fall back to literal
// equality (never a substring match). matchOrRegex caches compiled patterns, so
// this also fuzzes the cache path.
func FuzzMatchOrRegex(f *testing.F) {
	f.Add("prod-api", "prod-.*")
	f.Add("dev-prod-tools", "prod-.*") // must NOT match (anchored)
	f.Add("anything", "(")             // invalid regex -> literal fallback
	f.Add("x", "x")

	f.Fuzz(func(t *testing.T, s, pattern string) {
		got := matchOrRegex(s, pattern) // must not panic

		// Invariant: exact-equal strings always match.
		if s == pattern && !got {
			t.Fatalf("matchOrRegex(%q, %q) = false, want true for equal strings", s, pattern)
		}
		// Invariant: result is stable across calls (cache must not change it).
		if again := matchOrRegex(s, pattern); again != got {
			t.Fatalf("matchOrRegex(%q, %q) unstable: %v then %v", s, pattern, got, again)
		}
	})
}

// FuzzRestorePoisonedSnapshot feeds arbitrary JSON to Restore to confirm the
// poisoned-snapshot defenses hold: Restore must never panic, must never admit
// an active alert with an unknown Kind/Severity (which the sweep would later
// emit as a synthetic resolve), and must never honor a future-dated mute that
// would suppress a fingerprint forever.
func FuzzRestorePoisonedSnapshot(f *testing.F) {
	f.Add([]byte(`{"version":1,"active":[{"Fingerprint":"abc","Kind":"Pod","Severity":"critical"}]}`))
	f.Add([]byte(`{"version":1,"active":[{"Fingerprint":"x","Kind":"Bogus","Severity":"nope"}]}`))
	f.Add([]byte(`{"version":1,"lastSent":{"fp":"2999-01-01T00:00:00Z"}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		var snap Snapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			return // not a valid snapshot shape; nothing to restore
		}
		s := NewStore(time.Minute, time.Minute, nil)
		s.Restore(&snap) // must never panic

		// Every restored active alert must have a known Kind and Severity.
		s.mu.Lock()
		for fp, a := range s.active {
			if !a.Kind.Valid() || !a.Severity.Valid() {
				s.mu.Unlock()
				t.Fatalf("restored invalid alert fp=%s kind=%q sev=%q", fp, a.Kind, a.Severity)
			}
		}
		// No future-dated mute may have been admitted.
		now := time.Now()
		for fp, ts := range s.lastSent {
			if ts.After(now) {
				s.mu.Unlock()
				t.Fatalf("restored future-dated mute fp=%s ts=%s", fp, ts)
			}
		}
		s.mu.Unlock()
	})
}
