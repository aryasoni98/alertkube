package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
		set, err := decodeConfig(raw, c)
		if err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
		c.applyEnvDefaults(set)
	} else {
		c.applyEnvDefaults(nil)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseAndValidate parses YAML config bytes and runs the same validation as
// Load, without touching the filesystem. POST /api/v1/config/validate uses it
// to check candidate configuration before it is committed to Git/ConfigMap.
// Env defaults are applied so the verdict matches a real Load.
func ParseAndValidate(raw []byte) error {
	c := &Config{}
	set, err := decodeConfig(raw, c)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	c.applyEnvDefaults(set)
	return c.Validate()
}

// decodeConfig rejects keys that are not struct fields and reports which
// mapping paths were present. A typo used to load as a zero value and the
// controller booted healthy with the feature off. An explicit 0 or false is
// present; an omitted key is not, so env fallbacks apply only to omitted keys.
func decodeConfig(raw []byte, c *Config) (map[string]bool, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	err := dec.Decode(c)
	if errors.Is(err, io.EOF) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("config must contain exactly one YAML document")
	}
	// Decode mappings through YAML's own alias/merge handling so presence and
	// values agree, including explicit zero values inherited from anchors.
	var fields map[string]any
	if err := yaml.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	walkFields("", fields, set)
	return set, nil
}

// walkFields records mapping paths such as "behavior.muteSeconds". Sequence
// items are present as a whole; their children are not separate keys.
func walkFields(prefix string, fields map[string]any, set map[string]bool) {
	for key, value := range fields {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		set[path] = true
		if children, ok := value.(map[string]any); ok {
			walkFields(path, children, set)
		}
	}
}

func (c *Config) applyEnvDefaults(set map[string]bool) {
	absent := func(path string) bool { return !set[path] }
	// intDefault fills dst from key, or def, only when path was left out of
	// the YAML. TestPresentKeysBeatEnv pins every path passed here.
	intDefault := func(path, key string, dst *int, def int) {
		if absent(path) {
			*dst = env.IntOr(key, def)
		}
	}
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
	intDefault("behavior.muteSeconds", "MUTE_SECONDS", &c.Behavior.MuteSeconds, 600)
	intDefault("behavior.ignoreRestartCount", "IGNORE_RESTART_COUNT", &c.Behavior.IgnoreRestartCount, 30)
	if absent("behavior.ignoreRestartsWithExitCodeZero") {
		c.Behavior.IgnoreRestartsWithExitCodeZero = os.Getenv("IGNORE_RESTARTS_WITH_EXIT_CODE_ZERO") == "true"
	}
	intDefault("behavior.resolveTTLSeconds", "RESOLVE_TTL_SECONDS", &c.Behavior.ResolveTTLSeconds, 600)
	intDefault("behavior.startupGraceSeconds", "STARTUP_GRACE_SECONDS", &c.Behavior.StartupGraceSeconds, 0)
	intDefault("behavior.pvcPendingSeconds", "PVC_PENDING_SECONDS", &c.Behavior.PVCPendingSeconds, 300)
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
	if absent("grouping.windowSeconds") {
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
		intDefault("aws.pollSeconds", "AWS_POLL_SECONDS", &c.AWS.PollSeconds, 60)
	}
	if c.Azure.Enabled {
		intDefault("azure.pollSeconds", "AZURE_POLL_SECONDS", &c.Azure.PollSeconds, 60)
	}
	if c.GCP.Enabled {
		intDefault("gcp.pollSeconds", "GCP_POLL_SECONDS", &c.GCP.PollSeconds, 60)
	}
}
