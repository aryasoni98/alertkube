package aws

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cloudtrailtypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"golang.org/x/time/rate"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/sources"
)

const sourceCloudTrail = "aws-cloudtrail"

// cloudTrailRate paces LookupEvents in each region. The API is limited to two
// requests per second per account per region; 1.5 leaves headroom for the
// account's other callers.
const cloudTrailRate = rate.Limit(1.5)

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

// cloudTrailAPI is the subset of the CloudTrail client the change-event source
// uses.
type cloudTrailAPI interface {
	LookupEvents(context.Context, *cloudtrail.LookupEventsInput, ...func(*cloudtrail.Options)) (*cloudtrail.LookupEventsOutput, error)
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
	regions    []cloudTrailRegion
	events     []string
	lookback   time.Duration
	lookupRate rate.Limit // LookupEvents calls per second per region
	// prevEnd is where the previous poll's window ended. The runner calls
	// Poll from one goroutine, one poll at a time, so it needs no lock.
	prevEnd time.Time
}

// newCloudTrailSource resolves the event-name set (curated default when empty)
// and a lookback of 2x the poll interval so an event landing between polls is
// still caught; EventId dedupe absorbs the resulting overlap.
func newCloudTrailSource(regions []cloudTrailRegion, cfg config.AWS) *cloudTrailSource {
	events := cfg.CloudTrailEvents
	if len(events) == 0 {
		events = defaultCloudTrailEvents
	}
	return &cloudTrailSource{
		regions:    regions,
		events:     events,
		lookback:   time.Duration(2*cfg.PollSeconds) * time.Second,
		lookupRate: cloudTrailRate,
	}
}

func (s *cloudTrailSource) Name() string { return sourceCloudTrail }

// Poll looks up every event name in every region. The LookupEvents quota is
// per account per region, so each region runs on its own goroutine with its
// own limiter. Polled one after another, a long region list would outrun the
// poll deadline and the later regions would never be polled. Poll returns
// once every region has.
//
// The window reaches back at least to where the previous one ended. A poll
// cut off at its deadline returns only after its lookups are torn down, so
// the next poll can start more than the lookback after the previous window's
// end; the lookback alone would then leave a gap that neither window covers.
// EventId dedupe absorbs any overlap.
func (s *cloudTrailSource) Poll(ctx context.Context, emit sources.Emit) {
	end := time.Now()
	start := end.Add(-s.lookback)
	if !s.prevEnd.IsZero() && s.prevEnd.Before(start) {
		start = s.prevEnd
	}
	s.prevEnd = end
	var wg sync.WaitGroup
	for _, rc := range s.regions {
		wg.Go(func() {
			// The runner recovers panics only on the Poll goroutine, so each
			// region recovers its own instead of taking down the controller.
			defer func() {
				if r := recover(); r != nil {
					klog.Errorf("source %s panicked polling region %s (recovered): %v\n%s", sourceCloudTrail, rc.region, r, debug.Stack())
				}
			}()
			s.pollRegion(ctx, rc, start, end, emit)
		})
	}
	wg.Wait()
}

// pollRegion looks up each event name in one region, pacing every call on one
// limiter. Once the region is out of time it stops, so the failure is recorded
// once rather than once per remaining event name: lookupEvent records a
// lookup that ran out, and the ctx check here records a deadline that passed
// after a lookup's last page.
func (s *cloudTrailSource) pollRegion(ctx context.Context, rc cloudTrailRegion, start, end time.Time, emit sources.Emit) {
	lim := rate.NewLimiter(s.lookupRate, 1)
	for _, name := range s.events {
		if err := ctx.Err(); err != nil {
			pollErr(sourceCloudTrail, rc.region, err)
			return
		}
		if err := s.lookupEvent(ctx, rc, lim, name, start, end, emit); err != nil {
			return
		}
	}
}

// lookupEvent pages through one event name's matches in one region. It waits
// on lim before every LookupEvents call, follow-up pages included, because the
// quota counts every call. It records any failure. The returned error is
// non-nil only when the region is out of time: the limiter's next slot is past
// the deadline, or ctx is done, whether a call failed or the deadline passed
// between two pages. Any other API error returns nil so the remaining event
// names are still looked up.
func (s *cloudTrailSource) lookupEvent(ctx context.Context, rc cloudTrailRegion, lim *rate.Limiter, eventName string, start, end time.Time, emit sources.Emit) error {
	limited := false
	err := walkPages(ctx, sourceCloudTrail, rc.region, func(ctx context.Context, token *string) (*string, error) {
		if err := lim.Wait(ctx); err != nil {
			limited = true
			return nil, fmt.Errorf("lookup rate limit: %w", err)
		}
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
			return nil, err
		}
		for i := range out.Events {
			if a := eventToAlert(rc.region, out.Events[i]); a != nil {
				emit(a)
			}
		}
		return out.NextToken, nil
	})
	if err == nil {
		return nil
	}
	pollErr(sourceCloudTrail, rc.region, err)
	if limited || ctx.Err() != nil {
		return err
	}
	return nil
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
