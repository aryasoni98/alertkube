package config

import "time"

// Config is the YAML-driven runtime configuration.
type Config struct {
	Cluster string `yaml:"cluster"`

	Filters Filters `yaml:"filters"`

	Behavior Behavior `yaml:"behavior"`

	Channels Channels `yaml:"channels"`

	Routing []Route `yaml:"routing"`

	// SeverityOverrides remap an alert's severity before dedupe and
	// routing. First match wins. Watchers hardcode sensible defaults
	// (ImagePullBackOff=warning, JobFailed=critical, ...) but every org
	// disagrees somewhere - this is the escape hatch.
	SeverityOverrides []SeverityOverride `yaml:"severityOverrides"`

	// SinkRates overrides the per-sink send rate limiter. Unlisted sinks
	// keep the conservative default (1/sec, burst 5 - Slack's published
	// webhook limit).
	SinkRates map[string]SinkRate `yaml:"sinkRates"`

	Inhibitions []Inhibition `yaml:"inhibitions"`

	Silences []Silence `yaml:"silences"`

	// Escalations re-dispatch still-unresolved matching alerts to extra
	// sinks after a delay. Each rule fires at most once per alert
	// lifetime. Match semantics are the same as routing rules.
	Escalations []Escalation `yaml:"escalations"`

	// Receiver exposes POST /api/v1/receiver/alerts on the API address,
	// accepting Alertmanager webhook payloads and running them through
	// the same dedupe/grouping/routing/sink pipeline. Bearer auth via the
	// ALERTKUBE_RECEIVER_TOKEN env var; without a token the endpoint accepts
	// unauthenticated alert injection, so an empty token is a fatal error
	// unless AllowAnonymous is set (e.g. the port is locked down by a
	// NetworkPolicy).
	Receiver Receiver `yaml:"receiver"`

	MetricsAddr string `yaml:"metricsAddr"`

	// APIAddr optionally serves the sensitive data plane (the control API
	// and Alertmanager receiver) on a separate listen address from
	// MetricsAddr, which then serves only /metrics + the health probes. This
	// lets an operator expose the metrics/probe port for scraping and kubelet
	// probes while firewalling the data/receiver port with a
	// NetworkPolicy. Empty (default) co-locates everything on MetricsAddr, the
	// original single-port behavior.
	APIAddr string `yaml:"apiAddr"`

	// Grouping folds alert storms: the first alert of a group dispatches
	// immediately, later same-group alerts within the window collapse
	// into one summary. Stateful incident sinks (pagerduty, opsgenie)
	// still receive every resolve so incidents close, and never receive
	// summaries.
	Grouping Grouping `yaml:"grouping"`

	// Persistence snapshots active-alert and mute state to a ConfigMap so
	// a restart does not lose pending resolves or re-page muted standing
	// conditions. Requires get/create/update on the named ConfigMap.
	Persistence Persistence `yaml:"persistence"`

	// AWS enables polling AWS APIs for cloud-resource alerts alongside the
	// in-cluster Kubernetes watchers. Unlike watchers (informer-driven),
	// these sources are polled every PollSeconds. Credentials resolve via
	// the standard AWS chain; in-cluster the recommended setup is IAM Roles
	// for Service Accounts (IRSA). Disabled by default - alertkube stays a
	// pure Kubernetes controller unless this is turned on.
	AWS AWS `yaml:"aws"`

	// Azure enables polling Azure APIs for cloud-resource alerts. Credentials
	// resolve via the standard Azure chain (DefaultAzureCredential); in-cluster
	// the recommended setup is AKS Workload Identity. Subscription-scoped (not
	// region). Disabled by default.
	Azure Azure `yaml:"azure"`

	// GCP enables polling Google Cloud APIs for cloud-resource alerts.
	// Credentials resolve via Application Default Credentials; in-cluster the
	// recommended setup is GKE Workload Identity. Project-scoped. Disabled by
	// default.
	GCP GCP `yaml:"gcp"`

	// Rules are user-authored correlation rules evaluated against the live
	// alert stream by internal/rules. Each fires a derived alert (kind
	// Derived) through the same dedupe/route/group/sink pipeline.
	Rules []Rule `yaml:"rules"`

	// Correlation reserves configuration for the planned topology-aware engine.
	// It is not wired into the controller yet; retained for config compatibility.
	Correlation Correlation `yaml:"correlation"`

	// Maintenance windows suppress matching alerts on a recurring daily
	// schedule (e.g. a nightly backup window or a weekly patch window),
	// complementing the one-shot `silences` (which expire at a single RFC3339
	// instant). Evaluated on every routing decision.
	Maintenance []MaintenanceWindow `yaml:"maintenance"`
}

// Filters selects the namespaces and pod names watched by the controller.
type Filters struct {
	WatchedNamespaces      string `yaml:"watchedNamespaces"`
	IgnoredNamespaces      string `yaml:"ignoredNamespaces"`
	WatchedPodNamePrefixes string `yaml:"watchedPodNamePrefixes"`
	IgnoredPodNamePrefixes string `yaml:"ignoredPodNamePrefixes"`
}

// Behavior controls alert lifecycle and pod enrichment.
type Behavior struct {
	MuteSeconds                    int  `yaml:"muteSeconds"`
	IgnoreRestartCount             int  `yaml:"ignoreRestartCount"`
	IgnoreRestartsWithExitCodeZero bool `yaml:"ignoreRestartsWithExitCodeZero"`
	ResolveTTLSeconds              int  `yaml:"resolveTTLSeconds"`
	// StartupGraceSeconds suppresses alerts fired during the first N
	// seconds after start (informer initial sync re-fires standing
	// conditions on every restart). 0 disables the window.
	StartupGraceSeconds int `yaml:"startupGraceSeconds"`
	// PVCPendingSeconds is how long a claim may stay Pending before
	// alerting (provisioners legitimately take a while).
	PVCPendingSeconds int `yaml:"pvcPendingSeconds"`
	// DisableLogCollection stops the pod watcher from fetching
	// previous-container logs for alert enrichment. Logs are redacted
	// before forwarding, but redaction is pattern-based and
	// best-effort - strict environments should turn collection off
	// entirely rather than trust it.
	DisableLogCollection bool `yaml:"disableLogCollection"`
	// DisableAnnotationSilences ignores the `alert-silence-until`
	// pod annotation. Anyone with patch on a workload can otherwise
	// silence its alerts; environments where workload authors must
	// not control alerting set this.
	DisableAnnotationSilences bool `yaml:"disableAnnotationSilences"`
}

// Channels provides the default Slack channel for each severity.
type Channels struct {
	Critical string `yaml:"critical"`
	Warning  string `yaml:"warning"`
	Info     string `yaml:"info"`
}

// Receiver controls the Alertmanager-compatible webhook endpoint.
type Receiver struct {
	Enabled        bool `yaml:"enabled"`
	AllowAnonymous bool `yaml:"allowAnonymous"`
}

// Grouping controls how alert storms collapse into summaries.
type Grouping struct {
	Enabled       bool `yaml:"enabled"`
	WindowSeconds int  `yaml:"windowSeconds"`
	// By lists the alert fields forming the group identity.
	// Defaults to kind, namespace, reason, severity.
	By []string `yaml:"by"`
}

// Persistence configures the ConfigMap holding alert and delivery state.
type Persistence struct {
	Enabled bool `yaml:"enabled"`
	// ConfigMapName defaults to DefaultStateConfigMap, and to
	// "<default>-<shardIndex>" when sharding is enabled - see
	// ApplyShardScope for why sharded replicas must not share one object.
	ConfigMapName string `yaml:"configMapName"`
	// Namespace defaults to the POD_NAMESPACE env var (set via the
	// Downward API in the Helm chart).
	Namespace string `yaml:"namespace"`
}

type Route struct {
	Match map[string]string `yaml:"match"`
	Sinks []string          `yaml:"sinks"`
}

// Rule is a user-authored correlation rule. Exactly one of Count, All, or
// Absent must be set. It observes the firing alert stream (watchers + cloud
// sources) and emits a derived alert when its condition holds.
type Rule struct {
	Name     string `yaml:"name"`
	Severity string `yaml:"severity"`
	Summary  string `yaml:"summary"`
	// WindowSeconds is the look-back window for Count/All conditions.
	WindowSeconds int `yaml:"windowSeconds"`
	// Count fires when >= Threshold alerts matching Match occurred in the window.
	Count *RuleCount `yaml:"count"`
	// All fires when every matcher in the list had >=1 match in the window
	// (composite AND / multi-condition).
	All []map[string]string `yaml:"all"`
	// Absent fires when NO alert matching Match was seen for ForSeconds
	// (heartbeat / dead-man's-switch; evaluated on a timer).
	Absent *RuleAbsent `yaml:"absent"`
}

type RuleCount struct {
	Match     map[string]string `yaml:"match"`
	Threshold int               `yaml:"threshold"`
}

type RuleAbsent struct {
	Match      map[string]string `yaml:"match"`
	ForSeconds int               `yaml:"forSeconds"`
}

// Correlation configures the topology-aware alert correlation engine
// (internal/correlate). Disabled by default. Zero numeric values mean "use the
// engine default". Enabling requires the extra list/watch RBAC in the chart; see
// docs/superpowers/specs/2026-07-10-correlation-engine-design.md.
type Correlation struct {
	Enabled         bool `yaml:"enabled"`
	IntervalSeconds int  `yaml:"intervalSeconds"`
	MaxHops         int  `yaml:"maxHops"`
	BlastRadiusCap  int  `yaml:"blastRadiusCap"`
}

// SinkRate is a per-sink token-bucket override.
type SinkRate struct {
	PerSecond float64 `yaml:"perSecond"`
	Burst     int     `yaml:"burst"`
}

// SeverityOverride remaps matching alerts to a different severity.
// Match uses the same semantics as routing rules: exact equality on all
// keys except namespace/reason, which accept anchored regexes.
type SeverityOverride struct {
	Match    map[string]string `yaml:"match"`
	Severity string            `yaml:"severity"`
}

type Inhibition struct {
	Source   map[string]string `yaml:"source"`
	Target   map[string]string `yaml:"target"`
	Equal    []string          `yaml:"equal"`
	Duration string            `yaml:"duration"`
}

func (i Inhibition) DurationParsed() time.Duration {
	d, err := time.ParseDuration(i.Duration)
	if err != nil {
		return 10 * time.Minute
	}
	return d
}

type Silence struct {
	Matchers map[string]string `yaml:"matchers"`
	Until    string            `yaml:"until"`
}

// Escalation re-dispatches an alert that is still active after
// AfterMinutes to the listed sinks.
type Escalation struct {
	Match        map[string]string `yaml:"match"`
	AfterMinutes int               `yaml:"afterMinutes"`
	Sinks        []string          `yaml:"sinks"`
}
