package aws

import (
	"context"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cloudtrailtypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const sourceCloudTrail = "aws-cloudtrail"

// maxCloudTrailPages bounds LookupEvents pagination per (region, event) per
// poll so a misconfigured huge lookback cannot make one poll run unbounded.
const maxCloudTrailPages = 20

// defaultCloudTrailEvents is the curated security-relevant management-event set
// the source looks up when aws.cloudtrailEvents is empty: security-group
// mutations (the brief's "Security Group Change Alerts"), S3 bucket
// policy/ACL/public-access changes, and IAM principal/permission changes.
var defaultCloudTrailEvents = []string{
	// Security groups
	"AuthorizeSecurityGroupIngress", "AuthorizeSecurityGroupEgress",
	"RevokeSecurityGroupIngress", "RevokeSecurityGroupEgress",
	"CreateSecurityGroup", "DeleteSecurityGroup", "ModifySecurityGroupRules",
	// S3 exposure
	"PutBucketPolicy", "DeleteBucketPolicy", "PutBucketAcl",
	"PutBucketPublicAccessBlock", "DeletePublicAccessBlock",
	// IAM
	"CreateUser", "DeleteUser", "CreateAccessKey", "DeleteAccessKey",
	"AttachUserPolicy", "AttachRolePolicy", "PutUserPolicy", "PutRolePolicy",
	"CreateRole", "DeleteRole",
}

type cloudTrailRegion = regionClient[cloudTrailAPI]

// cloudTrailSource emits a fire-once event alert for each CloudTrail management
// event matching the configured event-name set within a lookback window each
// poll. CloudTrail LookupEvents allows only one attribute filter per call, so
// the source issues one LookupEvents per event name per region. The alerts are
// ephemeral (alert.Event=true): the emitter dedupes them by the unique
// CloudTrail EventId and dispatches once, with no resolve - a "security group
// was modified" notification is a fact, not a standing condition.
type cloudTrailSource struct {
	regions  []cloudTrailRegion
	events   []string
	lookback time.Duration
}

// newCloudTrailSource resolves the event-name set (curated default when empty)
// and a lookback of 2x the poll interval so an event landing between polls is
// still caught; EventId dedupe absorbs the resulting overlap.
func newCloudTrailSource(regions []cloudTrailRegion, cfg *config.Config) *cloudTrailSource {
	events := cfg.AWS.CloudTrailEvents
	if len(events) == 0 {
		events = defaultCloudTrailEvents
	}
	return &cloudTrailSource{
		regions:  regions,
		events:   events,
		lookback: time.Duration(2*cfg.AWS.PollSeconds) * time.Second,
	}
}

func (s *cloudTrailSource) Name() string { return sourceCloudTrail }

func (s *cloudTrailSource) Poll(ctx context.Context, emit sources.Emit) {
	end := time.Now()
	start := end.Add(-s.lookback)
	for _, rc := range s.regions {
		for _, name := range s.events {
			s.lookupEvent(ctx, rc, name, start, end, emit)
		}
	}
}

func (s *cloudTrailSource) lookupEvent(ctx context.Context, rc cloudTrailRegion, eventName string, start, end time.Time, emit sources.Emit) {
	var token *string
	for page := 0; page < maxCloudTrailPages; page++ {
		out, err := rc.client.LookupEvents(ctx, &cloudtrail.LookupEventsInput{
			StartTime: awssdk.Time(start),
			EndTime:   awssdk.Time(end),
			NextToken: token,
			LookupAttributes: []cloudtrailtypes.LookupAttribute{{
				AttributeKey:   cloudtrailtypes.LookupAttributeKeyEventName,
				AttributeValue: awssdk.String(eventName),
			}},
		})
		if err != nil {
			pollErr(sourceCloudTrail, rc.region, err)
			return
		}
		for i := range out.Events {
			if a := eventToAlert(rc.region, out.Events[i]); a != nil {
				emit(a)
			}
		}
		if out.NextToken == nil || *out.NextToken == "" {
			return
		}
		token = out.NextToken
	}
	// Fell out of the loop with a token still pending: the page cap was hit and
	// the remaining matching events were NOT fetched this poll. They are not
	// recovered next poll (the lookback window has moved on), so surface the
	// truncation loudly instead of dropping events silently.
	metrics.CloudPollTruncated.WithLabelValues(sourceCloudTrail).Inc()
	klog.Warningf("%s: %q in %s hit the %d-page lookup cap; remaining matching events were dropped this poll - raise the cap or narrow aws.cloudtrailEvents if this persists",
		sourceCloudTrail, eventName, rc.region, maxCloudTrailPages)
}

// eventToAlert builds an ephemeral event alert from a CloudTrail event. The
// fingerprint is the unique EventId so the emitter dedupes per event (two
// distinct ingress authorizations on the same group both page); an event with
// no EventId is skipped because it cannot be deduplicated.
func eventToAlert(region string, e cloudtrailtypes.Event) *alert.Alert {
	id := awssdk.ToString(e.EventId)
	if id == "" {
		return nil
	}
	eventName := awssdk.ToString(e.EventName)
	user := awssdk.ToString(e.Username)
	resource := firstResourceName(e)
	name := resource
	if name == "" {
		name = user
	}
	if name == "" {
		name = eventName
	}
	a := alert.New(alert.KindCloudTrailEvent, region, name, eventName, alert.SeverityWarning)
	a.Fingerprint = id // unique per event → dedupe per occurrence, not per group
	a.Event = true
	a.Summary = cloudTrailSummary(eventName, user, resource)
	a.Labels["provider"] = provider
	a.Labels["region"] = region
	a.Labels["eventSource"] = awssdk.ToString(e.EventSource)
	a.Details["eventId"] = id
	a.Details["eventName"] = eventName
	if user != "" {
		a.Details["user"] = user
	}
	if resource != "" {
		a.Details["resource"] = resource
	}
	if e.EventTime != nil {
		a.Details["eventTime"] = e.EventTime.UTC().Format(time.RFC3339)
		a.StartsAt = *e.EventTime
	}
	return a
}

func cloudTrailSummary(eventName, user, resource string) string {
	s := eventName
	if user != "" {
		s += " by " + user
	}
	if resource != "" {
		s += " on " + resource
	}
	return s
}

// firstResourceName returns the first named resource on the event, or "".
func firstResourceName(e cloudtrailtypes.Event) string {
	for _, r := range e.Resources {
		if n := awssdk.ToString(r.ResourceName); n != "" {
			return n
		}
	}
	return ""
}
