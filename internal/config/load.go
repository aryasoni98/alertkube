package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/aryasoni98/alertkube/internal/env"
)

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
		c.Persistence.ConfigMapName = DefaultStateConfigMap
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
