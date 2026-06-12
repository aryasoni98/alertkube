package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
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
	// the same dedupe/grouping/routing/sink pipeline. Optional bearer
	// auth via the ALERTKUBE_RECEIVER_TOKEN env var.
	Receiver struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"receiver"`

	MetricsAddr string `yaml:"metricsAddr"`

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
}

type Route struct {
	Match map[string]string `yaml:"match"`
	Sinks []string          `yaml:"sinks"`
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

// KnownSinks lists the sink names registered at startup; routing rules may
// only reference these. Kept here so Validate can fail fast on typos
// instead of dispatch silently skipping an unknown name.
var KnownSinks = map[string]bool{
	"slack":     true,
	"pagerduty": true,
	"teams":     true,
	"webhook":   true,
	"stdout":    true,
	"discord":   true,
	"telegram":  true,
	"opsgenie":  true,
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
	if c.Behavior.MuteSeconds <= 0 {
		return fmt.Errorf("behavior.muteSeconds must be positive, got %d", c.Behavior.MuteSeconds)
	}
	if c.Behavior.ResolveTTLSeconds <= 0 {
		return fmt.Errorf("behavior.resolveTTLSeconds must be positive, got %d", c.Behavior.ResolveTTLSeconds)
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
		c.Behavior.MuteSeconds = atoiOr("MUTE_SECONDS", 600)
	}
	if c.Behavior.IgnoreRestartCount == 0 {
		c.Behavior.IgnoreRestartCount = atoiOr("IGNORE_RESTART_COUNT", 30)
	}
	if !c.Behavior.IgnoreRestartsWithExitCodeZero {
		c.Behavior.IgnoreRestartsWithExitCodeZero = os.Getenv("IGNORE_RESTARTS_WITH_EXIT_CODE_ZERO") == "true"
	}
	if c.Behavior.ResolveTTLSeconds == 0 {
		c.Behavior.ResolveTTLSeconds = atoiOr("RESOLVE_TTL_SECONDS", 600)
	}
	if c.Behavior.StartupGraceSeconds == 0 {
		c.Behavior.StartupGraceSeconds = atoiOr("STARTUP_GRACE_SECONDS", 0)
	}
	if c.Behavior.PVCPendingSeconds == 0 {
		c.Behavior.PVCPendingSeconds = atoiOr("PVC_PENDING_SECONDS", 300)
	}
	if c.Channels.Critical == "" {
		c.Channels.Critical = envOr("SLACK_CHANNEL_CRITICAL", "alerts-critical")
	}
	if c.Channels.Warning == "" {
		c.Channels.Warning = envOr("SLACK_CHANNEL_WARNING", envOr("SLACK_CHANNEL", "alerts-warning"))
	}
	if c.Channels.Info == "" {
		c.Channels.Info = envOr("SLACK_CHANNEL_INFO", "alerts-info")
	}
	if c.MetricsAddr == "" {
		c.MetricsAddr = envOr("METRICS_ADDR", ":9090")
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
}

func atoiOr(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
