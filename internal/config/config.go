package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"alertkube/internal/env"
)

// Config is the YAML-driven runtime configuration.
type Config struct {
	Cluster string `yaml:"cluster"`

	Filters struct {
		WatchedNamespaces      string `yaml:"watchedNamespaces"`
		IgnoredNamespaces      string `yaml:"ignoredNamespaces"`
		WatchedPodNamePrefixes string `yaml:"watchedPodNamePrefixes"`
		IgnoredPodNamePrefixes string `yaml:"ignoredPodNamePrefixes"`
	} `yaml:"filters"`

	Behavior struct {
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
	} `yaml:"behavior"`

	Channels struct {
		Critical string `yaml:"critical"`
		Warning  string `yaml:"warning"`
		Info     string `yaml:"info"`
	} `yaml:"channels"`

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

	// Receiver exposes POST /api/v1/alerts on the metrics address,
	// accepting Alertmanager webhook payloads and running them through
	// the same dedupe/grouping/routing/sink pipeline. Bearer auth via the
	// ALERTKUBE_RECEIVER_TOKEN env var; without a token the endpoint accepts
	// unauthenticated alert injection, so an empty token is a fatal error
	// unless AllowAnonymous is set (e.g. the port is locked down by a
	// NetworkPolicy).
	Receiver struct {
		Enabled        bool `yaml:"enabled"`
		AllowAnonymous bool `yaml:"allowAnonymous"`
	} `yaml:"receiver"`

	MetricsAddr string `yaml:"metricsAddr"`

	// APIAddr optionally serves the sensitive data plane (/api/*, the console
	// SPA, and the Alertmanager receiver) on a SEPARATE listen address from
	// MetricsAddr, which then serves only /metrics + the health probes. This
	// lets an operator expose the metrics/probe port for scraping and kubelet
	// probes while firewalling the data/console/receiver port with a
	// NetworkPolicy. Empty (default) co-locates everything on MetricsAddr, the
	// original single-port behavior.
	APIAddr string `yaml:"apiAddr"`

	// Grouping folds alert storms: the first alert of a group dispatches
	// immediately, later same-group alerts within the window collapse
	// into one summary. Stateful incident sinks (pagerduty, opsgenie)
	// still receive every resolve so incidents close, and never receive
	// summaries.
	Grouping struct {
		Enabled       bool `yaml:"enabled"`
		WindowSeconds int  `yaml:"windowSeconds"`
		// By lists the alert fields forming the group identity.
		// Defaults to kind, namespace, reason, severity.
		By []string `yaml:"by"`
	} `yaml:"grouping"`

	// Persistence snapshots active-alert and mute state to a ConfigMap so
	// a restart does not lose pending resolves or re-page muted standing
	// conditions. Requires get/create/update on the named ConfigMap.
	Persistence struct {
		Enabled bool `yaml:"enabled"`
		// ConfigMapName defaults to "alertkube-state".
		ConfigMapName string `yaml:"configMapName"`
		// Namespace defaults to the POD_NAMESPACE env var (set via the
		// Downward API in the Helm chart).
		Namespace string `yaml:"namespace"`
	} `yaml:"persistence"`

	// AWS enables polling AWS APIs for cloud-resource alerts alongside the
	// in-cluster Kubernetes watchers. Unlike watchers (informer-driven),
	// these sources are polled every PollSeconds. Credentials resolve via
	// the standard AWS chain; in-cluster the recommended setup is IAM Roles
	// for Service Accounts (IRSA). Disabled by default - alertkube stays a
	// pure Kubernetes controller unless this is turned on.
	AWS struct {
		Enabled bool `yaml:"enabled"`
		// Regions to poll. Each AWS API call is per-region, so every region
		// here multiplies the per-poll API call count.
		Regions []string `yaml:"regions"`
		// PollSeconds is the interval between polls. Must be below
		// resolveTTLSeconds or a still-firing alarm false-resolves between
		// polls (Validate enforces this, mirroring the informer-resync rule).
		PollSeconds int `yaml:"pollSeconds"`
		// Source toggles. At least one must be true when Enabled.
		EKS         bool `yaml:"eks"`
		CloudWatch  bool `yaml:"cloudwatch"`
		EC2         bool `yaml:"ec2"`
		ELBV2       bool `yaml:"elbv2"`
		RDS         bool `yaml:"rds"`
		DynamoDB    bool `yaml:"dynamodb"`
		ElastiCache bool `yaml:"elasticache"`
		S3          bool `yaml:"s3"`
		CloudTrail  bool `yaml:"cloudtrail"`
		// CloudTrailEvents overrides the management event names the CloudTrail
		// source looks up. Empty uses a curated security set (security-group,
		// S3 policy/ACL, and IAM mutating events).
		CloudTrailEvents []string `yaml:"cloudtrailEvents"`
		ASG              bool     `yaml:"asg"`
		KMS              bool     `yaml:"kms"`
		EBS              bool     `yaml:"ebs"`
		Aurora           bool     `yaml:"aurora"`
		NAT              bool     `yaml:"nat"`
		EFS              bool     `yaml:"efs"`
		Route53          bool     `yaml:"route53"`
		ACM              bool     `yaml:"acm"`
		VPN              bool     `yaml:"vpn"`
	} `yaml:"aws"`

	// Azure enables polling Azure APIs for cloud-resource alerts. Credentials
	// resolve via the standard Azure chain (DefaultAzureCredential); in-cluster
	// the recommended setup is AKS Workload Identity. Subscription-scoped (not
	// region). Disabled by default.
	Azure struct {
		Enabled       bool     `yaml:"enabled"`
		Subscriptions []string `yaml:"subscriptions"`
		PollSeconds   int      `yaml:"pollSeconds"`
		AKS           bool     `yaml:"aks"`
		// Monitor enables ingesting fired Azure Monitor alerts (Alerts
		// Management): an alert with monitorCondition Fired pages, Resolved
		// resolves.
		Monitor bool `yaml:"monitor"`
		// VMs enables Azure Virtual Machine provisioning-health alerts.
		VMs bool `yaml:"vms"`
		// Storage enables Azure Storage account availability alerts.
		Storage bool `yaml:"storage"`
		// SQL enables Azure SQL Database health alerts (Suspect/Offline/Inaccessible).
		SQL bool `yaml:"sql"`
		// Redis enables Azure Cache for Redis provisioning-health alerts.
		Redis bool `yaml:"redis"`
	} `yaml:"azure"`

	// GCP enables polling Google Cloud APIs for cloud-resource alerts.
	// Credentials resolve via Application Default Credentials; in-cluster the
	// recommended setup is GKE Workload Identity. Project-scoped. Disabled by
	// default.
	GCP struct {
		Enabled     bool     `yaml:"enabled"`
		Projects    []string `yaml:"projects"`
		PollSeconds int      `yaml:"pollSeconds"`
		GKE         bool     `yaml:"gke"`
		// Monitoring enables a Cloud Monitoring posture source: it alerts when
		// an alert policy is disabled. GCP's Go SDK exposes no fired-incident
		// listing, so this surfaces monitoring-coverage posture, not fired
		// incidents.
		Monitoring bool `yaml:"monitoring"`
		// Compute enables Compute Engine instance health (REPAIRING) alerts.
		Compute bool `yaml:"compute"`
		// CloudSQL enables Cloud SQL instance state alerts.
		CloudSQL bool `yaml:"cloudsql"`
	} `yaml:"gcp"`

	// Rules are user-authored correlation rules evaluated against the live
	// alert stream by internal/rules. Each fires a derived alert (kind
	// Derived) through the same dedupe/route/group/sink pipeline.
	Rules []Rule `yaml:"rules"`

	// Maintenance windows suppress matching alerts on a recurring daily
	// schedule (e.g. a nightly backup window or a weekly patch window),
	// complementing the one-shot `silences` (which expire at a single RFC3339
	// instant). Evaluated on every routing decision.
	Maintenance []MaintenanceWindow `yaml:"maintenance"`
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

// Load reads YAML from path, then layers env-var fallbacks for legacy v1 keys.
// A path that cannot be read is a hard error: silently booting on env
// defaults because a ConfigMap mount is wrong gives an operator a
// mis-routed controller with no signal.
func Load(path string) (*Config, error) {
	c := &Config{}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := yaml.Unmarshal(raw, c); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	c.applyEnvDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseAndValidate parses YAML config bytes and runs the same validation as
// Load, without touching the filesystem. The read-only UI's POST
// /api/config/validate uses it to give authors fast feedback on a candidate
// config before they commit the change to Git/ConfigMap (Phase 1 authoring).
// Env defaults are applied so the verdict matches a real Load.
func ParseAndValidate(raw []byte) error {
	c := &Config{}
	if err := yaml.Unmarshal(raw, c); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	c.applyEnvDefaults()
	return c.Validate()
}

// InformerResyncSeconds is the fixed informer resync period the controller
// runs with (controller.go derives informerResyncPeriod from it). A resync
// re-delivers every cached object as a synthetic Update, re-touching standing
// conditions so they do not false-resolve. The resolveTTL and mute windows
// must therefore exceed it, or a still-firing condition expires between
// resyncs and re-pages every cycle; Validate enforces that relationship.
const InformerResyncSeconds = 300

// KnownSinks lists the sink names registered at startup; routing rules may
// only reference these. Kept here so Validate can fail fast on typos
// instead of dispatch silently skipping an unknown name.
var KnownSinks = map[string]bool{
	"slack":      true,
	"pagerduty":  true,
	"teams":      true,
	"webhook":    true,
	"stdout":     true,
	"discord":    true,
	"telegram":   true,
	"opsgenie":   true,
	"googlechat": true,
	"mattermost": true,
}

// Validate rejects configurations that would otherwise fail open at
// runtime: unknown sink names, unparseable silence timestamps and
// inhibition durations, and non-positive behavior windows.
func (c *Config) Validate() error {
	for i, r := range c.Routing {
		if len(r.Sinks) == 0 {
			return fmt.Errorf("routing[%d]: sinks list is empty", i)
		}
		for _, s := range r.Sinks {
			if !KnownSinks[s] {
				return fmt.Errorf("routing[%d]: unknown sink %q", i, s)
			}
		}
	}
	for i, ov := range c.SeverityOverrides {
		if len(ov.Match) == 0 {
			return fmt.Errorf("severityOverrides[%d]: match is empty (would remap every alert)", i)
		}
		switch ov.Severity {
		case "critical", "warning", "info":
		default:
			return fmt.Errorf("severityOverrides[%d]: severity must be critical|warning|info, got %q", i, ov.Severity)
		}
	}
	for name, sr := range c.SinkRates {
		if !KnownSinks[name] {
			return fmt.Errorf("sinkRates: unknown sink %q", name)
		}
		if sr.PerSecond <= 0 {
			return fmt.Errorf("sinkRates.%s: perSecond must be positive, got %v", name, sr.PerSecond)
		}
		if sr.Burst < 1 {
			return fmt.Errorf("sinkRates.%s: burst must be >= 1, got %d", name, sr.Burst)
		}
	}
	for i, inh := range c.Inhibitions {
		if inh.Duration == "" {
			continue
		}
		if _, err := time.ParseDuration(inh.Duration); err != nil {
			return fmt.Errorf("inhibitions[%d]: duration %q: %w", i, inh.Duration, err)
		}
	}
	for i, s := range c.Silences {
		if _, err := time.Parse(time.RFC3339, s.Until); err != nil {
			return fmt.Errorf("silences[%d]: until must be RFC3339: %w", i, err)
		}
	}
	if c.Behavior.MuteSeconds <= InformerResyncSeconds {
		return fmt.Errorf("behavior.muteSeconds (%d) must exceed the informer resync period (%ds): a shorter mute lets a standing condition re-page when the mute lapses before the next resync re-fire", c.Behavior.MuteSeconds, InformerResyncSeconds)
	}
	if c.Behavior.ResolveTTLSeconds <= InformerResyncSeconds {
		return fmt.Errorf("behavior.resolveTTLSeconds (%d) must exceed the informer resync period (%ds): a shorter TTL false-resolves still-firing standing conditions between resyncs, re-paging every cycle", c.Behavior.ResolveTTLSeconds, InformerResyncSeconds)
	}
	if c.Behavior.IgnoreRestartCount < 0 {
		return fmt.Errorf("behavior.ignoreRestartCount must be >= 0, got %d", c.Behavior.IgnoreRestartCount)
	}
	if c.Behavior.StartupGraceSeconds < 0 {
		return fmt.Errorf("behavior.startupGraceSeconds must be >= 0, got %d", c.Behavior.StartupGraceSeconds)
	}
	if c.Behavior.PVCPendingSeconds <= 0 {
		return fmt.Errorf("behavior.pvcPendingSeconds must be positive, got %d", c.Behavior.PVCPendingSeconds)
	}
	if c.Persistence.Enabled && c.Persistence.Namespace == "" {
		return fmt.Errorf("persistence.enabled requires persistence.namespace or the POD_NAMESPACE env var")
	}
	for i, esc := range c.Escalations {
		if esc.AfterMinutes <= 0 {
			return fmt.Errorf("escalations[%d]: afterMinutes must be positive, got %d", i, esc.AfterMinutes)
		}
		if len(esc.Sinks) == 0 {
			return fmt.Errorf("escalations[%d]: sinks list is empty", i)
		}
		for _, s := range esc.Sinks {
			if !KnownSinks[s] {
				return fmt.Errorf("escalations[%d]: unknown sink %q", i, s)
			}
		}
	}
	if c.Grouping.Enabled {
		if c.Grouping.WindowSeconds <= 0 {
			return fmt.Errorf("grouping.windowSeconds must be positive, got %d", c.Grouping.WindowSeconds)
		}
		for i, k := range c.Grouping.By {
			if k == "" {
				return fmt.Errorf("grouping.by[%d]: empty field name", i)
			}
		}
	}
	if c.AWS.Enabled {
		if len(c.AWS.Regions) == 0 {
			return fmt.Errorf("aws.enabled requires at least one entry in aws.regions (or the AWS_REGION env var)")
		}
		if !c.AWS.EKS && !c.AWS.CloudWatch && !c.AWS.EC2 && !c.AWS.ELBV2 && !c.AWS.RDS && !c.AWS.DynamoDB && !c.AWS.ElastiCache && !c.AWS.S3 && !c.AWS.CloudTrail && !c.AWS.ASG && !c.AWS.KMS && !c.AWS.EBS && !c.AWS.Aurora && !c.AWS.NAT && !c.AWS.EFS && !c.AWS.Route53 && !c.AWS.ACM && !c.AWS.VPN {
			return fmt.Errorf("aws.enabled requires at least one source (eks, cloudwatch, ec2, elbv2, rds, dynamodb, elasticache, s3, cloudtrail, asg, kms, ebs, aurora, nat, efs, route53, acm, vpn)")
		}
		if c.AWS.PollSeconds <= 0 {
			return fmt.Errorf("aws.pollSeconds must be positive, got %d", c.AWS.PollSeconds)
		}
		if c.AWS.PollSeconds >= c.Behavior.ResolveTTLSeconds {
			return fmt.Errorf("aws.pollSeconds (%d) must be below behavior.resolveTTLSeconds (%d): a longer poll interval lets a still-firing alarm false-resolve between polls", c.AWS.PollSeconds, c.Behavior.ResolveTTLSeconds)
		}
	}
	if c.Azure.Enabled {
		if len(c.Azure.Subscriptions) == 0 {
			return fmt.Errorf("azure.enabled requires at least one azure.subscriptions entry")
		}
		if !c.Azure.AKS && !c.Azure.Monitor && !c.Azure.VMs && !c.Azure.Storage && !c.Azure.SQL && !c.Azure.Redis {
			return fmt.Errorf("azure.enabled requires at least one source: azure.aks, azure.monitor, azure.vms, azure.storage, azure.sql, or azure.redis")
		}
		if c.Azure.PollSeconds <= 0 {
			return fmt.Errorf("azure.pollSeconds must be positive, got %d", c.Azure.PollSeconds)
		}
		if c.Azure.PollSeconds >= c.Behavior.ResolveTTLSeconds {
			return fmt.Errorf("azure.pollSeconds (%d) must be below behavior.resolveTTLSeconds (%d)", c.Azure.PollSeconds, c.Behavior.ResolveTTLSeconds)
		}
	}
	if c.GCP.Enabled {
		if len(c.GCP.Projects) == 0 {
			return fmt.Errorf("gcp.enabled requires at least one gcp.projects entry")
		}
		if !c.GCP.GKE && !c.GCP.Monitoring && !c.GCP.Compute && !c.GCP.CloudSQL {
			return fmt.Errorf("gcp.enabled requires at least one source: gcp.gke, gcp.monitoring, gcp.compute, or gcp.cloudsql")
		}
		if c.GCP.PollSeconds <= 0 {
			return fmt.Errorf("gcp.pollSeconds must be positive, got %d", c.GCP.PollSeconds)
		}
		if c.GCP.PollSeconds >= c.Behavior.ResolveTTLSeconds {
			return fmt.Errorf("gcp.pollSeconds (%d) must be below behavior.resolveTTLSeconds (%d)", c.GCP.PollSeconds, c.Behavior.ResolveTTLSeconds)
		}
	}
	for i, ru := range c.Rules {
		if ru.Name == "" {
			return fmt.Errorf("rules[%d]: name is required", i)
		}
		switch ru.Severity {
		case "critical", "warning", "info":
		default:
			return fmt.Errorf("rules[%d] (%s): severity must be critical|warning|info, got %q", i, ru.Name, ru.Severity)
		}
		set := 0
		if ru.Count != nil {
			set++
		}
		if len(ru.All) > 0 {
			set++
		}
		if ru.Absent != nil {
			set++
		}
		if set != 1 {
			return fmt.Errorf("rules[%d] (%s): exactly one of count, all, or absent must be set", i, ru.Name)
		}
		if ru.Count != nil {
			if ru.Count.Threshold <= 0 {
				return fmt.Errorf("rules[%d] (%s): count.threshold must be positive", i, ru.Name)
			}
			if ru.WindowSeconds <= 0 {
				return fmt.Errorf("rules[%d] (%s): windowSeconds must be positive for a count rule", i, ru.Name)
			}
		}
		if len(ru.All) > 0 && ru.WindowSeconds <= 0 {
			return fmt.Errorf("rules[%d] (%s): windowSeconds must be positive for an all rule", i, ru.Name)
		}
		if ru.Absent != nil && ru.Absent.ForSeconds <= 0 {
			return fmt.Errorf("rules[%d] (%s): absent.forSeconds must be positive", i, ru.Name)
		}
	}
	for i, w := range c.Maintenance {
		if err := w.validate(); err != nil {
			return fmt.Errorf("maintenance[%d]: %w", i, err)
		}
	}
	return nil
}

func (c *Config) applyEnvDefaults() {
	if c.Cluster == "" {
		c.Cluster = os.Getenv("CLUSTER_NAME")
	}
	if c.Filters.WatchedNamespaces == "" {
		c.Filters.WatchedNamespaces = os.Getenv("WATCHED_NAMESPACES")
	}
	if c.Filters.IgnoredNamespaces == "" {
		c.Filters.IgnoredNamespaces = os.Getenv("IGNORED_NAMESPACES")
	}
	if c.Filters.WatchedPodNamePrefixes == "" {
		c.Filters.WatchedPodNamePrefixes = os.Getenv("WATCHED_POD_NAME_PREFIXES")
	}
	if c.Filters.IgnoredPodNamePrefixes == "" {
		c.Filters.IgnoredPodNamePrefixes = os.Getenv("IGNORED_POD_NAME_PREFIXES")
	}
	if c.Behavior.MuteSeconds == 0 {
		c.Behavior.MuteSeconds = env.IntOr("MUTE_SECONDS", 600)
	}
	if c.Behavior.IgnoreRestartCount == 0 {
		c.Behavior.IgnoreRestartCount = env.IntOr("IGNORE_RESTART_COUNT", 30)
	}
	if !c.Behavior.IgnoreRestartsWithExitCodeZero {
		c.Behavior.IgnoreRestartsWithExitCodeZero = os.Getenv("IGNORE_RESTARTS_WITH_EXIT_CODE_ZERO") == "true"
	}
	if c.Behavior.ResolveTTLSeconds == 0 {
		c.Behavior.ResolveTTLSeconds = env.IntOr("RESOLVE_TTL_SECONDS", 600)
	}
	if c.Behavior.StartupGraceSeconds == 0 {
		c.Behavior.StartupGraceSeconds = env.IntOr("STARTUP_GRACE_SECONDS", 0)
	}
	if c.Behavior.PVCPendingSeconds == 0 {
		c.Behavior.PVCPendingSeconds = env.IntOr("PVC_PENDING_SECONDS", 300)
	}
	if c.Channels.Critical == "" {
		c.Channels.Critical = env.Or("SLACK_CHANNEL_CRITICAL", "alerts-critical")
	}
	if c.Channels.Warning == "" {
		c.Channels.Warning = env.Or("SLACK_CHANNEL_WARNING", env.Or("SLACK_CHANNEL", "alerts-warning"))
	}
	if c.Channels.Info == "" {
		c.Channels.Info = env.Or("SLACK_CHANNEL_INFO", "alerts-info")
	}
	if c.MetricsAddr == "" {
		c.MetricsAddr = env.Or("METRICS_ADDR", ":9090")
	}
	if c.APIAddr == "" {
		// Empty stays empty (co-located) unless an address is supplied.
		c.APIAddr = os.Getenv("ALERTKUBE_API_ADDR")
	}
	if c.Grouping.WindowSeconds == 0 {
		c.Grouping.WindowSeconds = 30
	}
	if c.Persistence.ConfigMapName == "" {
		c.Persistence.ConfigMapName = "alertkube-state"
	}
	if c.Persistence.Namespace == "" {
		c.Persistence.Namespace = os.Getenv("POD_NAMESPACE")
	}
	if c.AWS.Enabled {
		if len(c.AWS.Regions) == 0 {
			if r := os.Getenv("AWS_REGION"); r != "" {
				c.AWS.Regions = []string{r}
			}
		}
		if c.AWS.PollSeconds == 0 {
			c.AWS.PollSeconds = env.IntOr("AWS_POLL_SECONDS", 60)
		}
	}
	if c.Azure.Enabled && c.Azure.PollSeconds == 0 {
		c.Azure.PollSeconds = env.IntOr("AZURE_POLL_SECONDS", 60)
	}
	if c.GCP.Enabled && c.GCP.PollSeconds == 0 {
		c.GCP.PollSeconds = env.IntOr("GCP_POLL_SECONDS", 60)
	}
}
