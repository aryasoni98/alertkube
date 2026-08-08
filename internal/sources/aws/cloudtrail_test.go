package aws

import (
	"context"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cloudtrailtypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/metrics"
)

// alwaysPagingCloudTrail always returns a next token, so the lookup never
// terminates on its own - used to exercise the page-cap truncation guard.
type alwaysPagingCloudTrail struct{ calls int }

func (f *alwaysPagingCloudTrail) LookupEvents(_ context.Context, _ *cloudtrail.LookupEventsInput, _ ...func(*cloudtrail.Options)) (*cloudtrail.LookupEventsOutput, error) {
	f.calls++
	return &cloudtrail.LookupEventsOutput{NextToken: awssdk.String("more")}, nil
}

func TestCloudTrailTruncationIsObservable(t *testing.T) {
	before := testutil.ToFloat64(metrics.CloudPollTruncated.WithLabelValues(sourceCloudTrail))
	fake := &alwaysPagingCloudTrail{}
	src := &cloudTrailSource{
		regions:  []cloudTrailRegion{{region: "us-east-1", client: fake}},
		events:   []string{"CreateUser"},
		lookback: time.Minute,
	}
	emit, _ := collect()
	src.Poll(context.Background(), emit)

	if fake.calls != maxCloudTrailPages {
		t.Fatalf("lookup should stop at the %d-page cap, made %d calls", maxCloudTrailPages, fake.calls)
	}
	after := testutil.ToFloat64(metrics.CloudPollTruncated.WithLabelValues(sourceCloudTrail))
	if after != before+1 {
		t.Fatalf("truncation must increment CloudPollTruncated: before=%v after=%v", before, after)
	}
}

type fakeCloudTrail struct {
	byEvent map[string][]cloudtrailtypes.Event
	err     error
	calls   int
}

func (f *fakeCloudTrail) LookupEvents(_ context.Context, in *cloudtrail.LookupEventsInput, _ ...func(*cloudtrail.Options)) (*cloudtrail.LookupEventsOutput, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	name := awssdk.ToString(in.LookupAttributes[0].AttributeValue)
	return &cloudtrail.LookupEventsOutput{Events: f.byEvent[name]}, nil
}

func ctEvent(id, name, user, resource string) cloudtrailtypes.Event {
	e := cloudtrailtypes.Event{
		EventId:     awssdk.String(id),
		EventName:   awssdk.String(name),
		Username:    awssdk.String(user),
		EventSource: awssdk.String("ec2.amazonaws.com"),
	}
	if resource != "" {
		e.Resources = []cloudtrailtypes.Resource{{ResourceName: awssdk.String(resource)}}
	}
	return e
}

func TestEventToAlert(t *testing.T) {
	a := eventToAlert("us-east-1", ctEvent("evt-1", "AuthorizeSecurityGroupIngress", "alice", "sg-123"))
	if a == nil {
		t.Fatal("expected an alert")
	}
	if a.Kind != alert.KindCloudTrailEvent {
		t.Errorf("kind = %s, want CloudTrailEvent", a.Kind)
	}
	if !a.Event {
		t.Error("Event flag must be set so the emitter uses the ephemeral path")
	}
	if a.Fingerprint != "evt-1" {
		t.Errorf("fingerprint = %q, want the unique EventId evt-1", a.Fingerprint)
	}
	if a.Reason != "AuthorizeSecurityGroupIngress" {
		t.Errorf("reason = %q", a.Reason)
	}
	if a.Name != "sg-123" {
		t.Errorf("name = %q, want resource sg-123", a.Name)
	}
	if a.Severity != alert.SeverityWarning {
		t.Errorf("severity = %q, want warning", a.Severity)
	}
	if a.Labels["provider"] != "aws" {
		t.Errorf("provider label = %q", a.Labels["provider"])
	}

	if eventToAlert("r", ctEvent("", "X", "u", "res")) != nil {
		t.Error("an event with no EventId must be skipped (cannot dedupe)")
	}
	if b := eventToAlert("r", ctEvent("e2", "CreateUser", "bob", "")); b == nil || b.Name != "bob" {
		t.Errorf("name should fall back to the username when no resource, got %+v", b)
	}
}

func TestCloudTrailSourcePoll(t *testing.T) {
	fake := &fakeCloudTrail{byEvent: map[string][]cloudtrailtypes.Event{
		"CreateSecurityGroup": {
			ctEvent("e1", "CreateSecurityGroup", "alice", "sg-1"),
			ctEvent("e2", "CreateSecurityGroup", "bob", "sg-2"),
		},
		"DeleteSecurityGroup": {ctEvent("e3", "DeleteSecurityGroup", "alice", "sg-3")},
	}}
	src := &cloudTrailSource{
		regions:  []cloudTrailRegion{{region: "us-east-1", client: fake}},
		events:   []string{"CreateSecurityGroup", "DeleteSecurityGroup"},
		lookback: time.Minute,
	}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if len(*got) != 3 {
		t.Fatalf("expected 3 event alerts, got %d", len(*got))
	}
	if fake.calls != 2 {
		t.Fatalf("expected one LookupEvents per event name (2), got %d", fake.calls)
	}
	for _, a := range *got {
		if !a.Event || a.Kind != alert.KindCloudTrailEvent || a.Resolved {
			t.Errorf("bad event alert: %+v", a)
		}
	}
}

func TestNewCloudTrailSourceDefaults(t *testing.T) {
	cfg := &config.Config{}
	cfg.AWS.PollSeconds = 60

	src := newCloudTrailSource(nil, cfg)
	if len(src.events) == 0 {
		t.Fatal("empty cloudtrailEvents should fall back to the curated default set")
	}
	if src.lookback != 120*time.Second {
		t.Fatalf("lookback = %v, want 2x poll interval (120s)", src.lookback)
	}

	cfg.AWS.CloudTrailEvents = []string{"CreateUser"}
	if src2 := newCloudTrailSource(nil, cfg); len(src2.events) != 1 || src2.events[0] != "CreateUser" {
		t.Errorf("explicit cloudtrailEvents override not honored: %v", src2.events)
	}
}
