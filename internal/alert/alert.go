package alert

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

func (s Severity) Color() string {
	switch s {
	case SeverityCritical:
		return "#E01E5A"
	case SeverityWarning:
		return "#ECB22E"
	default:
		return "#4599DF"
	}
}

func (s Severity) Emoji() string {
	switch s {
	case SeverityCritical:
		return ":rotating_light:"
	case SeverityWarning:
		return ":warning:"
	default:
		return ":information_source:"
	}
}

// Kind identifies the resource type that produced the alert.
type Kind string

const (
	KindPod        Kind = "Pod"
	KindNode       Kind = "Node"
	KindDeployment Kind = "Deployment"
	KindPVC        Kind = "PersistentVolumeClaim"
	KindJob        Kind = "Job"
)

// Alert is the canonical event flowing through the pipeline.
type Alert struct {
	Fingerprint string
	Kind        Kind
	Namespace   string
	Name        string
	NodeName    string
	Severity    Severity
	Reason      string
	Summary     string
	Cluster     string
	Details     map[string]string
	Labels      map[string]string
	Annotations map[string]string
	StartsAt    time.Time
	EndsAt      time.Time
	Resolved    bool
}

// ComputeFingerprint hashes the identity tuple so equivalent alerts dedupe.
func ComputeFingerprint(kind Kind, ns, name, reason string) string {
	h := sha1.New()
	h.Write([]byte(string(kind) + "|" + ns + "|" + name + "|" + reason))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// New constructs an Alert and fills the fingerprint.
func New(kind Kind, ns, name, reason string, sev Severity) *Alert {
	return &Alert{
		Fingerprint: ComputeFingerprint(kind, ns, name, reason),
		Kind:        kind,
		Namespace:   ns,
		Name:        name,
		Reason:      reason,
		Severity:    sev,
		Details:     map[string]string{},
		Labels:      map[string]string{},
		Annotations: map[string]string{},
		StartsAt:    time.Now(),
	}
}

// FieldValue resolves a label-style key against the alert's well-known fields,
// falling back to Labels[key]. Shared by MatchLabels, GroupKey, and routing keys.
func (a *Alert) FieldValue(key string) string {
	switch key {
	case "severity":
		return string(a.Severity)
	case "kind":
		return string(a.Kind)
	case "namespace":
		return a.Namespace
	case "node":
		return a.NodeName
	case "reason":
		return a.Reason
	case "name":
		return a.Name
	default:
		return a.Labels[key]
	}
}

// MatchLabels reports whether a label-equality map matches.
// `namespace` and `reason` accept a regular expression (anchored automatically
// at both ends so `prod-.*` does not match `dev-prod-tools`). All other keys
// use exact-string equality.
func (a *Alert) MatchLabels(want map[string]string) bool {
	for k, v := range want {
		got := a.FieldValue(k)
		if k == "namespace" || k == "reason" {
			if !matchOrRegex(got, v) {
				return false
			}
			continue
		}
		if got != v {
			return false
		}
	}
	return true
}

var (
	regexCacheMu sync.RWMutex
	regexCache   = map[string]*regexp.Regexp{}
)

// matchOrRegex returns true when s exactly equals pattern OR when pattern
// compiles as a regex and fully matches s. Patterns are anchored with ^…$
// when absent so substring matches do not leak. Invalid regexes fall back
// to literal equality (never substring).
func matchOrRegex(s, pattern string) bool {
	if s == pattern {
		return true
	}
	regexCacheMu.RLock()
	re, ok := regexCache[pattern]
	regexCacheMu.RUnlock()
	if !ok {
		anchored := pattern
		if !strings.HasPrefix(anchored, "^") {
			anchored = "^" + anchored
		}
		if !strings.HasSuffix(anchored, "$") {
			anchored = anchored + "$"
		}
		compiled, err := regexp.Compile(anchored)
		if err != nil {
			// Cache a sentinel so we don't recompile on every call.
			regexCacheMu.Lock()
			regexCache[pattern] = nil
			regexCacheMu.Unlock()
			return false
		}
		regexCacheMu.Lock()
		regexCache[pattern] = compiled
		regexCacheMu.Unlock()
		re = compiled
	}
	if re == nil {
		return false
	}
	return re.MatchString(s)
}

// GroupKey builds a stable key for grouping alerts together.
func (a *Alert) GroupKey(by []string) string {
	parts := make([]string, 0, len(by))
	for _, k := range by {
		parts = append(parts, a.FieldValue(k))
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func (a *Alert) String() string {
	return fmt.Sprintf("[%s] %s %s/%s reason=%s fp=%s", a.Severity, a.Kind, a.Namespace, a.Name, a.Reason, a.Fingerprint)
}
