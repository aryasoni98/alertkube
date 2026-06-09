package filter

import (
	"regexp"
	"strings"
)

// Set evaluates whether a string matches any of the include patterns
// (comma-separated literals or regex). Empty set => match all.
type Set struct {
	patterns []*regexp.Regexp
	literals []string
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
		re, err := regexp.Compile(p)
		if err == nil {
			s.patterns = append(s.patterns, re)
		}
		s.literals = append(s.literals, p)
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

// MatchesAny returns true if any include set matches the value.
func MatchesAny(val string, sets ...*Set) bool {
	for _, s := range sets {
		if s.Matches(val) {
			return true
		}
	}
	return false
}
