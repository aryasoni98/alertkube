package filter

import "testing"

func TestEmptyMatchesAll(t *testing.T) {
	s := New("")
	if !s.Matches("anything") {
		t.Fatalf("empty set should match all")
	}
	if s.Blocks("anything") {
		t.Fatalf("empty set should never block")
	}
}

func TestLiteralPrefix(t *testing.T) {
	s := New("prod-,staging-")
	if !s.Matches("prod-api") {
		t.Fatalf("prod-api should match prefix prod-")
	}
	if !s.Matches("staging-worker") {
		t.Fatalf("staging-worker should match prefix staging-")
	}
	if s.Matches("dev-thing") {
		t.Fatalf("dev-thing should not match prod-/staging-")
	}
}

func TestRegex(t *testing.T) {
	s := New("^kube-.*$")
	if !s.Matches("kube-system") {
		t.Fatalf("regex ^kube-.*$ should match kube-system")
	}
	if s.Matches("my-kube-thing") {
		t.Fatalf("anchored regex must not match my-kube-thing")
	}
}

func TestLiteralPrefixIsNotUnanchoredSubstring(t *testing.T) {
	// "prod-" is a prefix, not a substring: a name that merely contains it
	// elsewhere must not match, or the filter silently widens.
	s := New("prod-")
	if s.Matches("xprod-api") {
		t.Fatalf("xprod-api must not match prefix prod-")
	}
	if !s.Matches("prod-api") {
		t.Fatalf("prod-api should still match prefix prod-")
	}
}

func TestInvalidRegexFallsBackToPrefix(t *testing.T) {
	// An unclosed group is not a valid regex; it must degrade to a literal
	// prefix instead of being silently dropped.
	s := New("broken(")
	if !s.Matches("broken(thing") {
		t.Fatalf("invalid regex should match as a literal prefix")
	}
}

func TestBlocks(t *testing.T) {
	s := New("debug-,test-")
	if !s.Blocks("debug-tools") {
		t.Fatalf("debug- prefix must block debug-tools")
	}
	if s.Blocks("prod-api") {
		t.Fatalf("prod-api should not be blocked")
	}
}
