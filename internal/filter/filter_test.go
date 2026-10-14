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

func TestUnanchoredRegexDoesNotMatchASubstring(t *testing.T) {
	s := New("prod-.*")
	if !s.Matches("prod-api") {
		t.Fatal("prod-.* should match prod-api")
	}
	if s.Matches("staging-prod-tools") {
		t.Fatal("prod-.* must not match staging-prod-tools")
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

func TestRegexIsStartAnchoredPrefix(t *testing.T) {
	// A regex is anchored at the start only, so it has the same prefix
	// semantics as a literal in the same field.
	tests := []struct {
		name    string
		pattern string
		val     string
		want    bool
	}{
		{"dotted pod-name prefix", "my.app-", "my.app-7f9c-xyz", true},
		{"alternation prefix", "(api|web)-", "api-7f9c", true},
		{"alternation prefix second branch", "(api|web)-", "web-1", true},
		{"alternation prefix is not a substring", "(api|web)-", "xapi-7f9c", false},
		{"explicit caret prefix", "^kube-", "kube-system", true},
		{"explicit caret prefix is not a substring", "^kube-", "my-kube-thing", false},
		{"wildcard is not a substring", "prod-.*", "staging-prod-tools", false},
		{"bare alternation matches by prefix", "kube-system|default", "default-x", true},
		{"every alternative is start-anchored", "kube-system|default", "my-default", false},
		{"trailing dollar makes an exact match", "kube-system$", "kube-system-x", false},
		{"trailing dollar still matches the exact name", "kube-system$", "kube-system", true},
		{"unterminated quote is a quoted prefix", `\Qmy.app-`, "my.app-7f9c", true},
		{"unterminated quote is not a substring", `\Qmy.app-`, "xmy.app-7f9c", false},
		{"unterminated quote dot is literal", `\Qmy.app-`, "myxapp-7f9c", false},
		{"unterminated quote keeps its parens literal", `\Qa)|(.*`, "anything", false},
		{"terminated quote is a prefix", `\Qmy.app\E-`, "my.app-7f9c", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := New(tt.pattern).Matches(tt.val); got != tt.want {
				t.Fatalf("New(%q).Matches(%q) = %v, want %v", tt.pattern, tt.val, got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"empty", "", false},
		{"literals", "prod-,staging-", false},
		{"valid regex", "my.app-,(api|web)-", false},
		{"unclosed group", "broken(", true},
		// Valid only once wrapped in the anchor group, which it would close
		// early and leave (.*) unanchored.
		{"pattern that escapes the anchor group", "x)|(.*", true},
		// RE2 quotes to the end of the pattern when \E is missing.
		{"unterminated quote", `\Qmy.app-`, false},
		{"terminated quote", `a\Qb.\Ec`, false},
		{"escaped backslash before Q", `a\\Q`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate("filters.test", tt.raw); (err != nil) != tt.wantErr {
				t.Fatalf("Validate(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
		})
	}
}

func TestPatternThatEscapesTheAnchorGroupFallsBackToPrefix(t *testing.T) {
	s := New("x)|(.*")
	if s.Matches("anything") {
		t.Fatal(`"x)|(.*" must not compile to a match-everything regex`)
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
