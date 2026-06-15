package alert

import "testing"

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
