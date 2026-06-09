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

func TestBlocks(t *testing.T) {
	s := New("debug-,test-")
	if !s.Blocks("debug-tools") {
		t.Fatalf("debug- prefix must block debug-tools")
	}
	if s.Blocks("prod-api") {
		t.Fatalf("prod-api should not be blocked")
	}
}
