package alert

import (
	"crypto/sha256"
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

// ResolvedColorHex is the swatch chat sinks use for a resolved alert (green),
// kept beside the firing palette in Color() so the "resolved is green"
// decision lives in one place rather than being re-encoded per sink.
const ResolvedColorHex = "#2EB67D"

// Emoji returns a literal Unicode emoji (not a :shortcode:) so it renders in
// a Slack `header` block, which only converts shortcodes when emoji:true is
// set on the text object. Colors mirror Color(): red/amber/blue circles.
func (s Severity) Emoji() string {
	switch s {
	case SeverityCritical:
		return "🔴"
	case SeverityWarning:
		return "🟡"
	default:
		return "🔵"
	}
}

// Valid reports whether s is a known severity. Used to reject untrusted
// values (e.g. a poisoned persisted snapshot) before they enter the store.
func (s Severity) Valid() bool {
	switch s {
	case SeverityCritical, SeverityWarning, SeverityInfo:
		return true
	}
	return false
}

// Kind identifies the resource type that produced the alert.
type Kind string

const (
	KindPod         Kind = "Pod"
	KindNode        Kind = "Node"
	KindDeployment  Kind = "Deployment"
	KindPVC         Kind = "PersistentVolumeClaim"
	KindJob         Kind = "Job"
	KindDaemonSet   Kind = "DaemonSet"
	KindStatefulSet Kind = "StatefulSet"
	KindCronJob     Kind = "CronJob"
	KindHPA         Kind = "HorizontalPodAutoscaler"
	// KindExternal marks alerts ingested through the Alertmanager
	// webhook receiver rather than produced by a watcher.
	KindExternal Kind = "External"
)

// Valid reports whether k is a known kind. Used to reject untrusted values
// (e.g. a poisoned persisted snapshot) before they enter the store.
func (k Kind) Valid() bool {
	switch k {
	case KindPod, KindNode, KindDeployment, KindPVC, KindJob, KindDaemonSet,
		KindStatefulSet, KindCronJob, KindHPA, KindExternal:
		return true
	}
	return false
}

// Control annotation keys change alertkube behavior (silencing, channel
// routing, rendered links). They are defined here, in one place, so the pod
// watcher's allow-list - which blocks lower-privilege labels from back-filling
// them - and every consumer below cannot drift apart. A mismatch would be a
// privilege bug: a label-writer could silence their own alerts or inject a
// runbook link the allow-list was meant to reject.
const (
	// AnnotationSilenceUntil suppresses an alert until the RFC3339 time it holds.
	AnnotationSilenceUntil = "alert-silence-until"
	// AnnotationSlackChannel overrides the Slack channel for an alert.
	AnnotationSlackChannel = "alert-slack-channel"
	// AnnotationRunbookURL is the runbook link rendered by chat sinks.
	AnnotationRunbookURL = "runbook-url"
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
// sha256 rather than sha1: collision resistance is irrelevant here, but it
// keeps security scanners quiet and costs nothing. Truncated to 12 hex
// chars for log/footer readability. NOTE: changing this function changes
// every fingerprint - persisted snapshots from older versions then fail to
// match live alerts, so re-fires inside the mute window re-page once after
// the upgrade. Bump SnapshotVersion if the change must invalidate state.
func ComputeFingerprint(kind Kind, ns, name, reason string) string {
	h := sha256.New()
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

// regexCache memoizes compiled namespace/reason matchers (nil sentinels for
// invalid patterns) so MatchLabels does not recompile per alert. It is
// intentionally unbounded: patterns only ever come from config (routing,
// inhibition, escalation, severity-override matchers), so the key set is
// bounded by config size, not by untrusted input. If alert-supplied label
// values ever feed these matchers, add a size cap here.
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

// GroupKey builds a stable key for grouping alerts together. The values are
// sorted (not joined in `by` order) on purpose: it makes the key independent
// of how the `by` list is ordered in config, so reordering `by` does not
// silently split previously-grouped alerts. Do not "simplify" the sort away.
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
