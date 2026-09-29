package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/filter"
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

// PollDeadlineFactor is the multiple of pollSeconds after which the cloud
// source runner cancels a poll (sources.runOne). A poll that overruns its
// interval delays the next one, so a firing cloud alert can go up to
// PollDeadlineFactor intervals without being re-fired. Validate only requires
// pollSeconds below the resolve TTL, as earlier releases did, so existing
// configs keep loading; PollDeadlineWarnings reports configs where a slow
// poll can outlast the TTL.
const PollDeadlineFactor = 2

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

// Validate runs each section validator in a fixed order and returns the first
// failure, so the same bad config always reports the same error. The order is
// close to the Config field order but not the same: behavior runs after
// silences, for one. Do not reorder the slice; that changes which error a
// config with several defects reports.
func (c *Config) Validate() error {
	sections := []func() error{
		c.validateFilters,
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

// validateSeverity rejects anything outside alert's three-level vocabulary,
// shared by severity overrides and rules.
func validateSeverity(field, severity string) error {
	if alert.Severity(severity).Valid() {
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

// PollDeadlineWarnings returns one message per enabled cloud provider whose
// poll deadline (PollDeadlineFactor x pollSeconds) is not below the resolve
// TTL. Such a config is valid, but a poll that runs toward its deadline can
// leave a firing alert un-refreshed past the TTL, so it false-resolves and
// re-pages. The caller logs these at startup.
func (c *Config) PollDeadlineWarnings() []string {
	var out []string
	for _, p := range []struct {
		name    string
		enabled bool
		poll    int
	}{
		{"aws", c.AWS.Enabled, c.AWS.PollSeconds},
		{"azure", c.Azure.Enabled, c.Azure.PollSeconds},
		{"gcp", c.GCP.Enabled, c.GCP.PollSeconds},
	} {
		if p.enabled && PollDeadlineFactor*p.poll >= c.Behavior.ResolveTTLSeconds {
			out = append(out, fmt.Sprintf("%s.pollSeconds (%d) x %d is not below behavior.resolveTTLSeconds (%d): a poll that runs toward its deadline can let a firing alert false-resolve and re-page; lower pollSeconds or raise the TTL", p.name, p.poll, PollDeadlineFactor, c.Behavior.ResolveTTLSeconds))
		}
	}
	return out
}

// --- sections ---------------------------------------------------------------

func (c *Config) validateFilters() error {
	fields := []struct{ name, raw string }{
		{"filters.watchedNamespaces", c.Filters.WatchedNamespaces},
		{"filters.ignoredNamespaces", c.Filters.IgnoredNamespaces},
		{"filters.watchedPodNamePrefixes", c.Filters.WatchedPodNamePrefixes},
		{"filters.ignoredPodNamePrefixes", c.Filters.IgnoredPodNamePrefixes},
	}
	for _, f := range fields {
		if err := filter.Validate(f.name, f.raw); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateRouting() error {
	for i, r := range c.Routing {
		field := fmt.Sprintf("routing[%d].match", i)
		switch {
		case len(r.Match) == 0:
			if i != len(c.Routing)-1 {
				return fmt.Errorf("%s: empty match is a catch-all and must be the final route (routing[%d] would be unreachable)", field, i+1)
			}
		case i == len(c.Routing)-1:
			// The final route may be a catch-all, as match: {} is: it
			// shadows no later route and suppresses nothing. Only require
			// its patterns to compile.
			if err := checkMatcherPatterns(field, r.Match); err != nil {
				return err
			}
		default:
			if err := SelectiveMatchers(field, r.Match); err != nil {
				return err
			}
		}
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
		if err := SelectiveMatchers(fmt.Sprintf("inhibitions[%d].source", i), inh.Source); err != nil {
			return err
		}
		if err := SelectiveMatchers(fmt.Sprintf("inhibitions[%d].target", i), inh.Target); err != nil {
			return err
		}
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
		if err := SelectiveMatchers(fmt.Sprintf("silences[%d].matchers", i), s.Matchers); err != nil {
			return err
		}
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
		if err := SelectiveMatchers(field+".match", esc.Match); err != nil {
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
	names := map[string]int{}
	for i, ru := range c.Rules {
		if ru.Name == "" {
			return fmt.Errorf("rules[%d]: name is required", i)
		}
		if prev, ok := names[ru.Name]; ok {
			return fmt.Errorf("rules[%d]: duplicate name %q (also rules[%d])", i, ru.Name, prev)
		}
		names[ru.Name] = i
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
		if err := checkMatcherPatterns(field+".count.match", ru.Count.Match); err != nil {
			return err
		}
		if err := requirePositive(field+": count.threshold", ru.Count.Threshold); err != nil {
			return err
		}
		return requirePositive(field+": windowSeconds (count rule)", ru.WindowSeconds)
	case len(ru.All) > 0:
		for i, m := range ru.All {
			if err := checkMatcherPatterns(fmt.Sprintf("%s.all[%d]", field, i), m); err != nil {
				return err
			}
		}
		return requirePositive(field+": windowSeconds (all rule)", ru.WindowSeconds)
	default:
		if err := checkMatcherPatterns(field+".absent.match", ru.Absent.Match); err != nil {
			return err
		}
		return requirePositive(field+": absent.forSeconds", ru.Absent.ForSeconds)
	}
}

// validateMaintenance defers to MaintenanceWindow.validate, which also owns
// the SelectiveMatchers check.
func (c *Config) validateMaintenance() error {
	for i, w := range c.Maintenance {
		if err := w.validate(); err != nil {
			return fmt.Errorf("maintenance[%d]: %w", i, err)
		}
	}
	return nil
}

// validateCorrelation rejects correlation.enabled: true and checks nothing
// else. Nothing reads intervalSeconds, maxHops or blastRadiusCap, so they are
// parsed but not range-checked.
func (c *Config) validateCorrelation() error {
	if !c.Correlation.Enabled {
		return nil
	}
	// The topology package exists; the engine that would consume it does not.
	// Accepting enabled: true would boot a controller that silently ignores the knob.
	return errors.New("correlation.enabled is not implemented (see docs/design/2026-07-10-correlation-engine-design.md); leave it false")
}
