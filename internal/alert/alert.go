package alert

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"maps"
	"net/url"
	"regexp"
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

// knownKinds is filled by kind() at each declaration. Valid is a map lookup,
// so a new kind cannot be named without being accepted on snapshot restore.
var knownKinds = map[Kind]struct{}{}

func kind(name string) Kind {
	k := Kind(name)
	if _, dup := knownKinds[k]; dup {
		panic("alert: duplicate kind " + name)
	}
	knownKinds[k] = struct{}{}
	return k
}

var (
	KindPod         = kind("Pod")
	KindNode        = kind("Node")
	KindDeployment  = kind("Deployment")
	KindReplicaSet  = kind("ReplicaSet")
	KindPVC         = kind("PersistentVolumeClaim")
	KindJob         = kind("Job")
	KindDaemonSet   = kind("DaemonSet")
	KindStatefulSet = kind("StatefulSet")
	KindCronJob     = kind("CronJob")
	KindHPA         = kind("HorizontalPodAutoscaler")
	KindService     = kind("Service")
	// AWS cloud-source kinds. Unlike the workload kinds above (produced by
	// informer-driven watchers), these are produced by the polled AWS
	// sources in internal/sources/aws. Namespace carries the AWS region and
	// Name carries the resource identifier (cluster name, alarm name,
	// instance id), so a resolve targets exactly one cloud resource.
	KindEKSCluster         = kind("EKSCluster")
	KindCloudWatchAlarm    = kind("CloudWatchAlarm")
	KindEC2Instance        = kind("EC2Instance")
	KindLoadBalancer       = kind("LoadBalancer")
	KindTargetGroup        = kind("TargetGroup")
	KindRDSInstance        = kind("RDSInstance")
	KindDynamoDBTable      = kind("DynamoDBTable")
	KindElastiCacheCluster = kind("ElastiCacheCluster")
	KindS3Bucket           = kind("S3Bucket")
	KindCloudTrailEvent    = kind("CloudTrailEvent")
	KindAKSCluster         = kind("AKSCluster")
	KindGKECluster         = kind("GKECluster")
	KindAzureMonitorAlert  = kind("AzureMonitorAlert")
	KindGCPAlertPolicy     = kind("GCPAlertPolicy")
	KindEKSNodegroup       = kind("EKSNodegroup")
	KindAKSNodePool        = kind("AKSNodePool")
	KindGKENodePool        = kind("GKENodePool")
	KindAzureVM            = kind("AzureVM")
	KindGCEInstance        = kind("GCEInstance")
	KindCloudSQLInstance   = kind("CloudSQLInstance")
	KindASG                = kind("AutoScalingGroup")
	KindKMSKey             = kind("KMSKey")
	KindAzureStorage       = kind("AzureStorageAccount")
	KindEBSVolume          = kind("EBSVolume")
	KindAuroraCluster      = kind("AuroraCluster")
	KindNATGateway         = kind("NATGateway")
	KindEFSFileSystem      = kind("EFSFileSystem")
	KindRoute53HealthCheck = kind("Route53HealthCheck")
	KindACMCertificate     = kind("ACMCertificate")
	KindVPNConnection      = kind("VPNConnection")
	KindAzureSQLDatabase   = kind("AzureSQLDatabase")
	KindAzureRedis         = kind("AzureRedisCache")
	// KindDerived marks an alert produced by a user-authored rule
	// (internal/rules) correlating other alerts, not by a watcher or source.
	KindDerived = kind("Derived")
	// KindExternal marks alerts ingested through the Alertmanager
	// webhook receiver rather than produced by a watcher.
	KindExternal = kind("External")
)

// valid reports whether k is a known kind. Used to reject untrusted values
// (e.g. a poisoned persisted snapshot) before they enter the store.
func (k Kind) valid() bool {
	_, ok := knownKinds[k]
	return ok
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
	// Event marks an ephemeral, point-in-time alert (e.g. a CloudTrail
	// management event) rather than a standing condition. The emitter dedupes
	// it by Fingerprint and dispatches once, but never adds it to the active
	// set and never emits a synthetic resolve for it - a "security group was
	// modified" notification has nothing to resolve.
	Event bool
	// Correlation is derived context attached by the correlation engine; nil
	// when correlation is disabled or the alert is standalone. Not persisted.
	Correlation *Correlation
}

// Clone returns a deep copy whose Labels, Annotations, and Details maps are
// independent of the receiver's. The store hands copies outside its lock (to
// the /api/alerts JSON encoder, to in-flight sink goroutines, to the recent
// ring); a shallow `cp := *a` shares those maps with the live alert, so a
// reader could race a writer mutating them. Clone severs that sharing.
func (a *Alert) Clone() *Alert {
	cp := *a
	cp.Labels = maps.Clone(a.Labels)
	cp.Annotations = maps.Clone(a.Annotations)
	cp.Details = maps.Clone(a.Details)
	cp.Correlation = a.Correlation.clone()
	return &cp
}

// CloneWithoutDetails is Clone with the enrichment Details dropped. Details
// (logs, events) only matter for the trigger message, so the copies that are
// kept around - the recent ring, snapshot entries and outbox records - discard
// them before cloning to bound their size.
func (a *Alert) CloneWithoutDetails() *Alert {
	cp := *a
	cp.Details = nil
	return cp.Clone()
}

// fingerprintLen is the hex truncation. Widened from 12 in the same change
// that length-prefixed the preimage; both invalidate persisted fingerprints.
const fingerprintLen = 16

// ComputeFingerprint hashes the identity tuple so equivalent alerts dedupe.
// Each field is length-prefixed, so a "|" inside a name cannot be rearranged
// into a different field and produce the same hash. Truncated for log and
// footer readability. Changing this function changes every fingerprint:
// persisted snapshots then fail to match live alerts, and standing conditions
// re-page once after upgrade. It does not bump SnapshotVersion, which gates the
// wire shape: restored alerts keep their old fingerprints and resolve when
// their TTL lapses.
func ComputeFingerprint(kind Kind, ns, name, reason string) string {
	h := sha256.New()
	for _, field := range []string{string(kind), ns, name, reason} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(field))) //nolint:gosec // G115: identity fields are object names and reasons, never near 4 GiB
		h.Write(n[:])
		h.Write([]byte(field))
	}
	return hex.EncodeToString(h.Sum(nil))[:fingerprintLen]
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

// fieldKeys are the matcher keys FieldValue resolves against an alert field
// instead of Labels. Keep it in step with the switch below:
// TestFieldKeysResolveToFields fails if a listed key falls through to Labels.
var fieldKeys = map[string]bool{
	"severity": true, "kind": true, "namespace": true,
	"node": true, "reason": true, "name": true,
}

// IsFieldKey reports whether key names an alert field rather than a label.
// An empty matcher value on a label key matches every alert that lacks the
// label; on a field key it matches only alerts whose field is empty.
func IsFieldKey(key string) bool {
	return fieldKeys[key]
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

// IsPatternKey reports whether a matcher value for key is a regular expression
// (compiled by CompilePattern) rather than an exact string.
func IsPatternKey(key string) bool {
	return key == "namespace" || key == "reason"
}

// CompilePattern compiles a namespace/reason matcher value the way MatchLabels
// evaluates it: anchored with ^ and $ unless the pattern already starts or
// ends with them, so `prod-.*` does not match `dev-prod-tools`.
func CompilePattern(pattern string) (*regexp.Regexp, error) {
	anchored := pattern
	if !strings.HasPrefix(anchored, "^") {
		anchored = "^" + anchored
	}
	if !strings.HasSuffix(anchored, "$") {
		anchored += "$"
	}
	return regexp.Compile(anchored)
}

// MatchLabels reports whether a label-equality map matches.
// `namespace` and `reason` accept a regular expression (anchored automatically
// at both ends so `prod-.*` does not match `dev-prod-tools`). All other keys
// use exact-string equality.
func (a *Alert) MatchLabels(want map[string]string) bool {
	for k, v := range want {
		got := a.FieldValue(k)
		if IsPatternKey(k) {
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
// invalid patterns) so MatchLabels does not recompile per alert. Patterns come
// from config (routing, inhibition, escalation, severity-override matchers), so
// the key set is normally bounded by config size, not by untrusted input. As a
// defense-in-depth guard against any future caller that feeds alert-supplied
// values here, the cache is hard-capped at regexCacheMax entries: once full it
// stops memoizing new patterns (compiling them per call) rather than growing
// without bound.
const regexCacheMax = 4096

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
		compiled, err := CompilePattern(pattern)
		if err != nil {
			// Invalid pattern: fall back to literal equality. Only memoize the
			// nil sentinel while there is room, so a flood of distinct invalid
			// patterns cannot grow the cache without bound.
			cacheRegex(pattern, nil)
			return false
		}
		cacheRegex(pattern, compiled)
		re = compiled
	}
	if re == nil {
		return false
	}
	return re.MatchString(s)
}

// cacheRegex memoizes a compiled pattern (or a nil sentinel) under the cache
// cap. Once regexCache holds regexCacheMax entries it stops admitting new ones,
// so an unexpected flood of distinct patterns recompiles per call instead of
// growing memory without bound.
func cacheRegex(pattern string, re *regexp.Regexp) {
	regexCacheMu.Lock()
	defer regexCacheMu.Unlock()
	if _, exists := regexCache[pattern]; !exists && len(regexCache) >= regexCacheMax {
		return
	}
	regexCache[pattern] = re
}

// GroupKey includes field names and escapes values so swapped fields or
// embedded separators cannot merge unrelated alerts. Encoding sorts the keys,
// keeping group identity independent of the order of `by` in config.
func (a *Alert) GroupKey(by []string) string {
	fields := make(url.Values, len(by))
	for _, k := range by {
		fields.Set(k, a.FieldValue(k))
	}
	return fields.Encode()
}

func (a *Alert) String() string {
	return fmt.Sprintf("[%s] %s %q/%q reason=%q fp=%s resolved=%t", a.Severity, a.Kind, a.Namespace, a.Name, a.Reason, a.Fingerprint, a.Resolved)
}
