package config

import (
	"fmt"
	"regexp"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
)

// SelectiveMatchers rejects a matcher map that would match every alert:
// an empty map, an empty value on a label key (alert.IsFieldKey is false),
// or a namespace/reason pattern that matches every probe in matchAllProbes.
// An empty value on a field key is accepted: `node: ""` selects only alerts
// with no node name, such as unscheduled pods and Deployment, CronJob or cloud
// alerts.
// It also rejects a namespace/reason pattern that does not compile: MatchLabels
// would treat it as a literal that no real value equals, so the entry would
// silently never match. The key rules come from package alert, so validation
// cannot drift from MatchLabels.
func SelectiveMatchers(field string, m map[string]string) error {
	if len(m) == 0 {
		return fmt.Errorf("%s: empty matchers match every alert", field)
	}
	for k, v := range m {
		if v == "" && !alert.IsFieldKey(k) {
			return fmt.Errorf("%s: %q is empty; an empty value on an unknown key matches every alert missing that label", field, k)
		}
		if !alert.IsPatternKey(k) {
			continue
		}
		re, err := compileMatcherPattern(field, k, v)
		if err != nil {
			return err
		}
		if matchesEveryString(re) {
			return fmt.Errorf("%s: %s=%q matches every alert", field, k, v)
		}
	}
	return nil
}

// matchAllProbes are realistic namespace and reason values: short, hyphenated,
// mixed case, and with digits and punctuation. A pattern that matches all of
// them is taken to match every alert. The probes are not empty, so `.+` is
// caught although it misses "": no alert has an empty reason. This is a
// ponytail: these probes are a heuristic, not a proof; use regex-language
// analysis if exact match-all detection becomes a requirement. `[a-z0-9-]+`
// matches every valid namespace yet passes because it misses "CrashLoopBackOff".
var matchAllProbes = []string{"x", "kube-system", "CrashLoopBackOff", "Prod_1.a-z"}

func matchesEveryString(re *regexp.Regexp) bool {
	for _, p := range matchAllProbes {
		if !re.MatchString(p) {
			return false
		}
	}
	return true
}

func checkMatcherPatterns(field string, m map[string]string) error {
	for k, v := range m {
		if !alert.IsPatternKey(k) {
			continue
		}
		if _, err := compileMatcherPattern(field, k, v); err != nil {
			return err
		}
	}
	return nil
}

// compileMatcherPattern compiles a namespace/reason value exactly as
// MatchLabels does, naming the field and key in the error.
func compileMatcherPattern(field, key, pattern string) (*regexp.Regexp, error) {
	re, err := alert.CompilePattern(pattern)
	if err != nil {
		return nil, fmt.Errorf("%s: %s pattern %q: %w", field, key, pattern, err)
	}
	return re, nil
}
