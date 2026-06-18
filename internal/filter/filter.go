package filter

import (
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
		re, err := regexp.Compile(p)
		if err != nil {
			klog.Warningf("filter: %q is not a valid regex (%v); treating it as a literal prefix", p, err)
			s.literals = append(s.literals, p)
			continue
		}
		s.patterns = append(s.patterns, re)
	}
	return s
}

func (s *Set) Empty() bool {
	return len(s.patterns) == 0 && len(s.literals) == 0
}

func (s *Set) Matches(val string) bool {
	if s.Empty() {
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
	return !s.Empty() && s.Matches(val)
}
