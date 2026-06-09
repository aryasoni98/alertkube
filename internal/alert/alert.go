package alert

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
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

// MatchLabels reports whether a label-equality map matches.
func (a *Alert) MatchLabels(want map[string]string) bool {
	for k, v := range want {
		switch k {
		case "severity":
			if string(a.Severity) != v {
				return false
			}
		case "kind":
			if string(a.Kind) != v {
				return false
			}
		case "namespace":
			if !matchOrRegex(a.Namespace, v) {
				return false
			}
		case "reason":
			if !matchOrRegex(a.Reason, v) {
				return false
			}
		default:
			if a.Labels[k] != v {
				return false
			}
		}
	}
	return true
}

func matchOrRegex(s, pattern string) bool {
	if s == pattern {
		return true
	}
	return strings.Contains(s, strings.TrimSuffix(strings.TrimPrefix(pattern, ".*"), ".*"))
}

// GroupKey builds a stable key for grouping alerts together.
func (a *Alert) GroupKey(by []string) string {
	parts := []string{}
	for _, k := range by {
		switch k {
		case "severity":
			parts = append(parts, string(a.Severity))
		case "kind":
			parts = append(parts, string(a.Kind))
		case "namespace":
			parts = append(parts, a.Namespace)
		case "node":
			parts = append(parts, a.NodeName)
		case "reason":
			parts = append(parts, a.Reason)
		default:
			parts = append(parts, a.Labels[k])
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func (a *Alert) String() string {
	return fmt.Sprintf("[%s] %s %s/%s reason=%s fp=%s", a.Severity, a.Kind, a.Namespace, a.Name, a.Reason, a.Fingerprint)
}
