package filter

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/klog/v2"
)

// Set evaluates whether a string matches any of the include patterns
// (comma-separated literals or regex). Empty set => match all.
type Set struct {
	patterns []*regexp.Regexp
	literals []string
}

// isRegex reports whether a pattern uses regex syntax. A pattern with no
// metacharacters is a literal prefix (the documented pod-name-prefix
// behavior); one with metacharacters is a regex (the documented namespace
// behavior). The old code applied a pattern as BOTH a regex and an
// unanchored prefix, so the broader of the two won - e.g. the prefix-intended
// "prod-" also compiled to an unanchored regex that matched "xprod-api",
// silently widening an operator's filter.
func isRegex(p string) bool {
	return strings.ContainsAny(p, `^$.*+?()[]{}|\`)
}

// anchor pins a regex to the start of the value only, so it has the same
// prefix semantics as a literal: prod-.* matches prod-api and not
// staging-prod-tools, and my.app- still matches my.app-7f9c. The group
// anchors every alternative. A trailing $ in p asks for an exact match.
// A \Q with no \E quotes to the end of p, so it is closed first or the
// group's ")" would be quoted. A stray \E is an error, so only an open quote
// gets one.
func anchor(p string) string {
	if openQuote(p) {
		p += `\E`
	}
	return "^(?:" + p + ")"
}

// openQuote reports whether p ends inside a \Q quote. Like RE2, it skips the
// character after each backslash and ends a quote at the first \E.
func openQuote(p string) bool {
	for i := 0; i < len(p)-1; i++ {
		if p[i] != '\\' {
			continue
		}
		if p[i+1] != 'Q' {
			i++
			continue
		}
		end := strings.Index(p[i+2:], `\E`)
		if end < 0 {
			return true
		}
		i += 2 + end + 1
	}
	return false
}

// compile parses p on its own before anchoring it. Otherwise a pattern such
// as "x)|(.*" would close the anchor group early and compile to an unanchored
// match-everything regex.
func compile(p string) (*regexp.Regexp, error) {
	if _, err := regexp.Compile(p); err != nil {
		return nil, err
	}
	return regexp.Compile(anchor(p))
}

// Validate rejects a filter string whose regex patterns do not compile.
// New degrades those to literal prefixes; failing here keeps a typo from
// silently changing which namespaces are watched.
func Validate(field, raw string) error {
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" || !isRegex(p) {
			continue
		}
		if _, err := compile(p); err != nil {
			return fmt.Errorf("%s: %q is not a valid regex: %w", field, p, err)
		}
	}
	return nil
}

func New(raw string) *Set {
	s := &Set{}
	if raw == "" {
		return s
	}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !isRegex(p) {
			s.literals = append(s.literals, p)
			continue
		}
		re, err := compile(p)
		if err != nil {
			klog.Warningf("filter: %q is not a valid regex (%v); treating it as a literal prefix", p, err)
			s.literals = append(s.literals, p)
			continue
		}
		s.patterns = append(s.patterns, re)
	}
	return s
}

func (s *Set) empty() bool {
	return len(s.patterns) == 0 && len(s.literals) == 0
}

func (s *Set) Matches(val string) bool {
	if s.empty() {
		return true
	}
	for _, lit := range s.literals {
		if strings.HasPrefix(val, lit) || val == lit {
			return true
		}
	}
	for _, re := range s.patterns {
		if re.MatchString(val) {
			return true
		}
	}
	return false
}

// Blocks reports whether val is matched by an exclusion set.
// Empty set never blocks (caller treats empty as "no exclusions").
func (s *Set) Blocks(val string) bool {
	return !s.empty() && s.Matches(val)
}
