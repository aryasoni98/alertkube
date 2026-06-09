package config

import (
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
		GroupWaitSeconds               int  `yaml:"groupWaitSeconds"`
		ResolveTTLSeconds              int  `yaml:"resolveTTLSeconds"`
	} `yaml:"behavior"`

	Channels struct {
		Critical string `yaml:"critical"`
		Warning  string `yaml:"warning"`
		Info     string `yaml:"info"`
	} `yaml:"channels"`

	Routing []Route `yaml:"routing"`

	Inhibitions []Inhibition `yaml:"inhibitions"`

	Silences []Silence `yaml:"silences"`

	MetricsAddr string `yaml:"metricsAddr"`
}

type Route struct {
	Match map[string]string `yaml:"match"`
	Sinks []string          `yaml:"sinks"`
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

// Load reads YAML from path, then layers env-var fallbacks for legacy v1 keys.
func Load(path string) (*Config, error) {
	c := &Config{}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err == nil {
			if err := yaml.Unmarshal(raw, c); err != nil {
				return nil, err
			}
		}
	}
	c.applyEnvDefaults()
	return c, nil
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
	if c.Behavior.GroupWaitSeconds == 0 {
		c.Behavior.GroupWaitSeconds = atoiOr("GROUP_WAIT_SECONDS", 30)
	}
	if c.Behavior.ResolveTTLSeconds == 0 {
		c.Behavior.ResolveTTLSeconds = atoiOr("RESOLVE_TTL_SECONDS", 600)
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
