package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Startup validation. Every rule here rejects a configuration that would
// otherwise fail *open* at runtime - an unknown sink name that dispatch would
// silently skip, a mute window shorter than the informer resync that re-pages
// every cycle, a cloud poll interval that false-resolves live alarms. Failing
// at startup turns each of those into a crash-loop with a precise message
// instead of a controller that looks healthy and mis-routes.

// InformerResyncSeconds is the fixed informer resync period the controller
// runs with (controller.go derives informerResyncPeriod from it). A resync
// re-delivers every cached object as a synthetic Update, re-touching standing
// conditions so they do not false-resolve. The resolveTTL and mute windows
// must therefore exceed it, or a still-firing condition expires between
// resyncs and re-pages every cycle; Validate enforces that relationship.
const InformerResyncSeconds = 300

// KnownSinks lists the sink names registered at startup; routing rules may
// only reference these. Kept here so Validate can fail fast on typos
// instead of dispatch silently skipping an unknown name. A guard test
// (app.TestKnownSinksMatchesRegistry) pins it against the registry buildSinks
// actually constructs, so the two cannot drift.
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

// Validate runs each section validator in config-file order and returns the
// first failure, so an operator fixing a bad config sees problems reported
// top-down rather than in an arbitrary order.
func (c *Config) Validate() error {
	sections := []func() error{
		c.validateRouting,
		c.validateSeverityOverrides,
		c.validateSinkRates,
		c.validateInhibitions,
		c.validateSilences,
		c.validateBehavior,
		c.validatePersistence,
		c.validateEscalations,
		c.validateGrouping,
		c.validateAWS,
		c.validateAzure,
		c.validateGCP,
		c.validateRules,
		c.validateMaintenance,
		c.validateCorrelation,
	}
	for _, validate := range sections {
		if err := validate(); err != nil {
			return err
		}
	}
	return nil
}

// --- shared rules -----------------------------------------------------------

// validateSinkNames rejects an empty or unknown-sink list. field names the
// config path (e.g. `routing[0]`) so the error points straight at the entry.
func validateSinkNames(field string, names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("%s: sinks list is empty", field)
	}
	for _, s := range names {
		if !KnownSinks[s] {
			return fmt.Errorf("%s: unknown sink %q", field, s)
		}
	}
	return nil
}

// validateSeverity rejects anything outside the three-level vocabulary shared
// by severity overrides and rules.
func validateSeverity(field, severity string) error {
	switch severity {
	case "critical", "warning", "info":
		return nil
	}
	return fmt.Errorf("%s: severity must be critical|warning|info, got %q", field, severity)
}

// requirePositive rejects a non-positive window/count, naming the config key.
func requirePositive(field string, v int) error {
	if v <= 0 {
		return fmt.Errorf("%s must be positive, got %d", field, v)
	}
	return nil
}

// toggle pairs a provider source's config key with whether it is enabled.
type toggle struct {
	key string
	on  bool
}

// requireAnySource rejects an enabled cloud provider with no source turned on -
// which would poll nothing and quietly do nothing at all. The error names every
// valid key, so the message can never drift from the set actually checked.
func requireAnySource(provider string, toggles []toggle) error {
	keys := make([]string, 0, len(toggles))
	for _, t := range toggles {
		if t.on {
			return nil
		}
		// Only reached while every toggle so far is off, so on the error path
		// below this holds the complete key list.
		keys = append(keys, t.key)
	}
	return fmt.Errorf("%s.enabled requires at least one source (%s)", provider, strings.Join(keys, ", "))
}

// validatePollInterval enforces the rule every polled cloud provider shares:
// the interval must be positive and strictly below the resolve TTL. At or above
// the TTL, a still-firing alarm false-resolves between polls and re-pages every
// cycle - the same relationship the informer resync has with the watchers.
func validatePollInterval(provider string, poll, resolveTTL int) error {
	if err := requirePositive(provider+".pollSeconds", poll); err != nil {
		return err
	}
	if poll >= resolveTTL {
		return fmt.Errorf("%s.pollSeconds (%d) must be below behavior.resolveTTLSeconds (%d): a longer poll interval lets a still-firing alarm false-resolve between polls", provider, poll, resolveTTL)
	}
	return nil
}

// --- sections ---------------------------------------------------------------

func (c *Config) validateRouting() error {
	for i, r := range c.Routing {
		if err := validateSinkNames(fmt.Sprintf("routing[%d]", i), r.Sinks); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateSeverityOverrides() error {
	for i, ov := range c.SeverityOverrides {
		field := fmt.Sprintf("severityOverrides[%d]", i)
		if len(ov.Match) == 0 {
			return fmt.Errorf("%s: match is empty (would remap every alert)", field)
		}
		if err := validateSeverity(field, ov.Severity); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateSinkRates() error {
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
	return nil
}

func (c *Config) validateInhibitions() error {
	for i, inh := range c.Inhibitions {
		if inh.Duration == "" {
			continue
		}
		if _, err := time.ParseDuration(inh.Duration); err != nil {
			return fmt.Errorf("inhibitions[%d]: duration %q: %w", i, inh.Duration, err)
		}
	}
	return nil
}

func (c *Config) validateSilences() error {
	for i, s := range c.Silences {
		if _, err := time.Parse(time.RFC3339, s.Until); err != nil {
			return fmt.Errorf("silences[%d]: until must be RFC3339: %w", i, err)
		}
	}
	return nil
}

func (c *Config) validateBehavior() error {
	b := c.Behavior
	if b.MuteSeconds <= InformerResyncSeconds {
		return fmt.Errorf("behavior.muteSeconds (%d) must exceed the informer resync period (%ds): a shorter mute lets a standing condition re-page when the mute lapses before the next resync re-fire", b.MuteSeconds, InformerResyncSeconds)
	}
	if b.ResolveTTLSeconds <= InformerResyncSeconds {
		return fmt.Errorf("behavior.resolveTTLSeconds (%d) must exceed the informer resync period (%ds): a shorter TTL false-resolves still-firing standing conditions between resyncs, re-paging every cycle", b.ResolveTTLSeconds, InformerResyncSeconds)
	}
	if b.IgnoreRestartCount < 0 {
		return fmt.Errorf("behavior.ignoreRestartCount must be >= 0, got %d", b.IgnoreRestartCount)
	}
	if b.StartupGraceSeconds < 0 {
		return fmt.Errorf("behavior.startupGraceSeconds must be >= 0, got %d", b.StartupGraceSeconds)
	}
	return requirePositive("behavior.pvcPendingSeconds", b.PVCPendingSeconds)
}

func (c *Config) validatePersistence() error {
	if c.Persistence.Enabled && c.Persistence.Namespace == "" {
		return errors.New("persistence.enabled requires persistence.namespace or the POD_NAMESPACE env var")
	}
	return nil
}

func (c *Config) validateEscalations() error {
	for i, esc := range c.Escalations {
		field := fmt.Sprintf("escalations[%d]", i)
		if err := requirePositive(field+": afterMinutes", esc.AfterMinutes); err != nil {
			return err
		}
		if err := validateSinkNames(field, esc.Sinks); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateGrouping() error {
	if !c.Grouping.Enabled {
		return nil
	}
	if err := requirePositive("grouping.windowSeconds", c.Grouping.WindowSeconds); err != nil {
		return err
	}
	for i, k := range c.Grouping.By {
		if k == "" {
			return fmt.Errorf("grouping.by[%d]: empty field name", i)
		}
	}
	return nil
}

func (c *Config) validateAWS() error {
	if !c.AWS.Enabled {
		return nil
	}
	if len(c.AWS.Regions) == 0 {
		return errors.New("aws.enabled requires at least one entry in aws.regions (or the AWS_REGION env var)")
	}
	a := c.AWS
	if err := requireAnySource("aws", []toggle{
		{"eks", a.EKS}, {"cloudwatch", a.CloudWatch}, {"ec2", a.EC2},
		{"elbv2", a.ELBV2}, {"rds", a.RDS}, {"dynamodb", a.DynamoDB},
		{"elasticache", a.ElastiCache}, {"s3", a.S3}, {"cloudtrail", a.CloudTrail},
		{"asg", a.ASG}, {"kms", a.KMS}, {"ebs", a.EBS}, {"aurora", a.Aurora},
		{"nat", a.NAT}, {"efs", a.EFS}, {"route53", a.Route53}, {"acm", a.ACM},
		{"vpn", a.VPN},
	}); err != nil {
		return err
	}
	return validatePollInterval("aws", a.PollSeconds, c.Behavior.ResolveTTLSeconds)
}

func (c *Config) validateAzure() error {
	if !c.Azure.Enabled {
		return nil
	}
	if len(c.Azure.Subscriptions) == 0 {
		return errors.New("azure.enabled requires at least one azure.subscriptions entry")
	}
	z := c.Azure
	if err := requireAnySource("azure", []toggle{
		{"aks", z.AKS}, {"monitor", z.Monitor}, {"vms", z.VMs},
		{"storage", z.Storage}, {"sql", z.SQL}, {"redis", z.Redis},
	}); err != nil {
		return err
	}
	return validatePollInterval("azure", z.PollSeconds, c.Behavior.ResolveTTLSeconds)
}

func (c *Config) validateGCP() error {
	if !c.GCP.Enabled {
		return nil
	}
	if len(c.GCP.Projects) == 0 {
		return errors.New("gcp.enabled requires at least one gcp.projects entry")
	}
	g := c.GCP
	if err := requireAnySource("gcp", []toggle{
		{"gke", g.GKE}, {"monitoring", g.Monitoring},
		{"compute", g.Compute}, {"cloudsql", g.CloudSQL},
	}); err != nil {
		return err
	}
	return validatePollInterval("gcp", g.PollSeconds, c.Behavior.ResolveTTLSeconds)
}

func (c *Config) validateRules() error {
	for i, ru := range c.Rules {
		if ru.Name == "" {
			return fmt.Errorf("rules[%d]: name is required", i)
		}
		field := fmt.Sprintf("rules[%d] (%s)", i, ru.Name)
		if err := validateSeverity(field, ru.Severity); err != nil {
			return err
		}
		if err := validateRuleCondition(field, ru); err != nil {
			return err
		}
	}
	return nil
}

// validateRuleCondition enforces the one-condition-per-rule contract and each
// condition's own required windows. Exactly one of count / all / absent must be
// set: zero would never fire, and two would make the rule's semantics ambiguous.
func validateRuleCondition(field string, ru Rule) error {
	set := 0
	for _, present := range []bool{ru.Count != nil, len(ru.All) > 0, ru.Absent != nil} {
		if present {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("%s: exactly one of count, all, or absent must be set", field)
	}
	switch {
	case ru.Count != nil:
		if err := requirePositive(field+": count.threshold", ru.Count.Threshold); err != nil {
			return err
		}
		return requirePositive(field+": windowSeconds (count rule)", ru.WindowSeconds)
	case len(ru.All) > 0:
		return requirePositive(field+": windowSeconds (all rule)", ru.WindowSeconds)
	default:
		return requirePositive(field+": absent.forSeconds", ru.Absent.ForSeconds)
	}
}

func (c *Config) validateMaintenance() error {
	for i, w := range c.Maintenance {
		if err := w.validate(); err != nil {
			return fmt.Errorf("maintenance[%d]: %w", i, err)
		}
	}
	return nil
}

// validateCorrelation bounds the engine's tunables. A zero means "use the
// engine default", so only explicitly-set values are range-checked, and the
// whole section is skipped while the engine is off.
func (c *Config) validateCorrelation() error {
	if !c.Correlation.Enabled {
		return nil
	}
	if v := c.Correlation.IntervalSeconds; v != 0 && v < 5 {
		return fmt.Errorf("correlation.intervalSeconds (%d) must be >= 5", v)
	}
	if v := c.Correlation.MaxHops; v != 0 && (v < 1 || v > 5) {
		return fmt.Errorf("correlation.maxHops (%d) must be in [1,5]", v)
	}
	if v := c.Correlation.BlastRadiusCap; v != 0 && (v < 1 || v > 500) {
		return fmt.Errorf("correlation.blastRadiusCap (%d) must be in [1,500]", v)
	}
	return nil
}
