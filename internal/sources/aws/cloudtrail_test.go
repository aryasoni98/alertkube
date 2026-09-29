package aws

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cloudtrailtypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/time/rate"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/sources"
)

// alwaysPagingCloudTrail always returns a next token, so the lookup never
// terminates on its own - used to exercise the page-cap truncation guard.
type alwaysPagingCloudTrail struct{ calls int }

func (f *alwaysPagingCloudTrail) LookupEvents(_ context.Context, _ *cloudtrail.LookupEventsInput, _ ...func(*cloudtrail.Options)) (*cloudtrail.LookupEventsOutput, error) {
	f.calls++
	// A fresh token each page, so the loop stops on the page guard rather than
	// the non-advancing-token check.
	tok := awssdk.String("page-" + strconv.Itoa(f.calls))
	return &cloudtrail.LookupEventsOutput{NextToken: tok}, nil
}

func TestCloudTrailTruncationIsObservable(t *testing.T) {
	before := testutil.ToFloat64(metrics.CloudPollTruncated.WithLabelValues(sourceCloudTrail))
	fake := &alwaysPagingCloudTrail{}
	src := &cloudTrailSource{
		regions:    []cloudTrailRegion{{region: "us-east-1", client: fake}},
		events:     []string{"CreateUser"},
		lookback:   time.Minute,
		lookupRate: rate.Inf, // 1000 paced pages would take minutes
	}
	emit, _ := collect()
	src.Poll(context.Background(), emit)

	if fake.calls != sources.MaxPages {
		t.Fatalf("lookup should stop at the %d-page guard, made %d calls", sources.MaxPages, fake.calls)
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
		regions:    []cloudTrailRegion{{region: "us-east-1", client: fake}},
		events:     []string{"CreateSecurityGroup", "DeleteSecurityGroup"},
		lookback:   time.Minute,
		lookupRate: rate.Inf,
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

// syncCollect is collect for a Poll that emits from several goroutines: the
// CloudTrail source polls its regions concurrently. Read the slice only after
// Poll returns.
func syncCollect() (sources.Emit, *[]*alert.Alert) {
	var mu sync.Mutex
	var got []*alert.Alert
	return func(a *alert.Alert) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, a)
	}, &got
}

// pagingCloudTrail answers every event name with pages pages of one event each
// (pages < 0: the lookup never ends) and records each call's time and event
// name. Use one per region: it is not safe for concurrent use.
type pagingCloudTrail struct {
	pages int
	calls []time.Time
	names []string
}

func (f *pagingCloudTrail) LookupEvents(_ context.Context, in *cloudtrail.LookupEventsInput, _ ...func(*cloudtrail.Options)) (*cloudtrail.LookupEventsOutput, error) {
	f.calls = append(f.calls, time.Now())
	name := awssdk.ToString(in.LookupAttributes[0].AttributeValue)
	f.names = append(f.names, name)
	page := 0
	if in.NextToken != nil {
		page, _ = strconv.Atoi(*in.NextToken)
	}
	out := &cloudtrail.LookupEventsOutput{
		Events: []cloudtrailtypes.Event{ctEvent(name+"-"+strconv.Itoa(page), name, "alice", "")},
	}
	if f.pages < 0 || page+1 < f.pages {
		out.NextToken = awssdk.String(strconv.Itoa(page + 1))
	}
	return out, nil
}

// The LookupEvents quota is per region, so regions are polled side by side.
// Polled one after another, the second region would get only part of its
// lookups in before the poll deadline, and the rest would be skipped silently.
func TestCloudTrailPollsRegionsConcurrently(t *testing.T) {
	before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceCloudTrail))
	events := []string{"A", "B", "C", "D", "E", "F"}
	east, west := &pagingCloudTrail{pages: 1}, &pagingCloudTrail{pages: 1}
	src := &cloudTrailSource{
		regions:  []cloudTrailRegion{{region: "us-east-1", client: east}, {region: "us-west-2", client: west}},
		events:   events,
		lookback: time.Minute,
		// Six lookups at 10/s with a burst of 1 take 500ms per region: two
		// regions fit an 800ms deadline side by side, not one after the other.
		lookupRate: 10,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	emit, got := syncCollect()
	src.Poll(ctx, emit)

	for _, r := range []struct {
		region string
		fake   *pagingCloudTrail
	}{{"us-east-1", east}, {"us-west-2", west}} {
		if n := len(r.fake.calls); n != len(events) {
			t.Errorf("%s made %d of %d lookups before the deadline", r.region, n, len(events))
		}
	}
	if len(*got) != 2*len(events) {
		t.Errorf("got %d alerts, want %d", len(*got), 2*len(events))
	}
	if d := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceCloudTrail)) - before; d != 0 {
		t.Errorf("CloudPollErrors delta = %v, want 0", d)
	}
}

// The quota counts every LookupEvents call, so a follow-up page waits on the
// region's limiter too, not only the first call for each event name.
func TestCloudTrailPacesEveryPage(t *testing.T) {
	fake := &pagingCloudTrail{pages: 3}
	src := &cloudTrailSource{
		regions:    []cloudTrailRegion{{region: "us-east-1", client: fake}},
		events:     []string{"CreateUser"},
		lookback:   time.Minute,
		lookupRate: 20, // one call per 50ms
	}
	emit, got := syncCollect()
	src.Poll(context.Background(), emit)

	if len(fake.calls) != 3 {
		t.Fatalf("made %d calls, want 3 pages", len(fake.calls))
	}
	for i := 1; i < len(fake.calls); i++ {
		if gap := fake.calls[i].Sub(fake.calls[i-1]); gap < 40*time.Millisecond {
			t.Errorf("page %d came %v after page %d, want paced about 50ms apart", i+1, gap, i)
		}
	}
	if len(*got) != 3 {
		t.Errorf("got %d alerts, want one per page (3)", len(*got))
	}
}

// A lookup the limiter cannot fit before the deadline is a poll error, not a
// silent stop. It is recorded once per region: the region stops there, since
// every later wait in it would fail the same way.
func TestCloudTrailLimiterExhaustionIsRecordedOncePerRegion(t *testing.T) {
	before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceCloudTrail))
	east, west := &pagingCloudTrail{pages: -1}, &pagingCloudTrail{pages: -1}
	src := &cloudTrailSource{
		regions:    []cloudTrailRegion{{region: "us-east-1", client: east}, {region: "us-west-2", client: west}},
		events:     []string{"A", "B"},
		lookback:   time.Minute,
		lookupRate: 10,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	emit, _ := syncCollect()
	src.Poll(ctx, emit)

	for _, r := range []struct {
		region string
		fake   *pagingCloudTrail
	}{{"us-east-1", east}, {"us-west-2", west}} {
		// About three calls fit in 250ms at 10/s.
		if n := len(r.fake.calls); n == 0 || n > 4 {
			t.Errorf("%s made %d calls in 250ms at 10/s, want 1-4", r.region, n)
		}
		for _, name := range r.fake.names {
			if name != "A" {
				t.Errorf("%s looked up %q after its limiter ran out; the region should stop", r.region, name)
				break
			}
		}
	}
	if d := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceCloudTrail)) - before; d != 2 {
		t.Errorf("CloudPollErrors delta = %v, want one per region (2)", d)
	}
}

// funcCloudTrail answers every LookupEvents call with fn and records the
// event names looked up.
type funcCloudTrail struct {
	fn    func(ctx context.Context) (*cloudtrail.LookupEventsOutput, error)
	names []string
}

func (f *funcCloudTrail) LookupEvents(ctx context.Context, in *cloudtrail.LookupEventsInput, _ ...func(*cloudtrail.Options)) (*cloudtrail.LookupEventsOutput, error) {
	f.names = append(f.names, awssdk.ToString(in.LookupAttributes[0].AttributeValue))
	return f.fn(ctx)
}

// An API error skips only its event name. Running out of time stops the
// region and is recorded once, whether a call was cut off or the deadline
// passed between two lookups.
func TestCloudTrailLookupFailures(t *testing.T) {
	cases := []struct {
		name      string
		fn        func(ctx context.Context) (*cloudtrail.LookupEventsOutput, error)
		deadline  time.Duration
		wantNames []string
		wantErrs  float64
	}{
		{
			name: "api error skips only that event name",
			fn: func(context.Context) (*cloudtrail.LookupEventsOutput, error) {
				return nil, errors.New("AccessDeniedException")
			},
			wantNames: []string{"A", "B", "C"},
			wantErrs:  3,
		},
		{
			name: "call cut off by the deadline stops the region",
			fn: func(ctx context.Context) (*cloudtrail.LookupEventsOutput, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
			deadline:  50 * time.Millisecond,
			wantNames: []string{"A"},
			wantErrs:  1,
		},
		{
			name: "deadline passing after a lookup stops the region",
			fn: func(ctx context.Context) (*cloudtrail.LookupEventsOutput, error) {
				<-ctx.Done()
				return &cloudtrail.LookupEventsOutput{}, nil
			},
			deadline:  50 * time.Millisecond,
			wantNames: []string{"A"},
			wantErrs:  1,
		},
		{
			name: "deadline passing during a lookup with more pages stops the region",
			fn: func(ctx context.Context) (*cloudtrail.LookupEventsOutput, error) {
				<-ctx.Done()
				return &cloudtrail.LookupEventsOutput{NextToken: awssdk.String("next")}, nil
			},
			deadline:  50 * time.Millisecond,
			wantNames: []string{"A"},
			wantErrs:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceCloudTrail))
			fake := &funcCloudTrail{fn: tc.fn}
			src := &cloudTrailSource{
				regions:    []cloudTrailRegion{{region: "us-east-1", client: fake}},
				events:     []string{"A", "B", "C"},
				lookback:   time.Minute,
				lookupRate: rate.Inf,
			}
			ctx := context.Background()
			if tc.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.deadline)
				defer cancel()
			}
			emit, _ := syncCollect()
			src.Poll(ctx, emit)

			if !slices.Equal(fake.names, tc.wantNames) {
				t.Errorf("looked up %v, want %v", fake.names, tc.wantNames)
			}
			if d := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceCloudTrail)) - before; d != tc.wantErrs {
				t.Errorf("CloudPollErrors delta = %v, want %v", d, tc.wantErrs)
			}
		})
	}
}

// windowCloudTrail records the time window of every LookupEvents call.
type windowCloudTrail struct {
	starts, ends []time.Time
}

func (f *windowCloudTrail) LookupEvents(_ context.Context, in *cloudtrail.LookupEventsInput, _ ...func(*cloudtrail.Options)) (*cloudtrail.LookupEventsOutput, error) {
	f.starts = append(f.starts, awssdk.ToTime(in.StartTime))
	f.ends = append(f.ends, awssdk.ToTime(in.EndTime))
	return &cloudtrail.LookupEventsOutput{}, nil
}

// A poll that runs to its deadline returns only after its in-flight lookups
// are torn down, so the next poll starts a little late. Its lookback alone
// would leave the events stamped during that teardown in neither window, so
// each window starts no later than where the previous poll's window ended.
// A zero lookback stands in for a teardown longer than the overlap.
func TestCloudTrailWindowsMeet(t *testing.T) {
	fake := &windowCloudTrail{}
	src := &cloudTrailSource{
		regions:    []cloudTrailRegion{{region: "us-east-1", client: fake}},
		events:     []string{"A"},
		lookupRate: rate.Inf,
	}
	emit, _ := syncCollect()
	src.Poll(context.Background(), emit)
	time.Sleep(5 * time.Millisecond)
	src.Poll(context.Background(), emit)

	if len(fake.starts) != 2 {
		t.Fatalf("made %d lookups, want 2", len(fake.starts))
	}
	if !fake.starts[1].Equal(fake.ends[0]) {
		t.Errorf("second window starts at %v, want the first window's end %v", fake.starts[1], fake.ends[0])
	}
	if !fake.starts[0].Equal(fake.ends[0]) {
		t.Errorf("first window starts at %v, want its end %v with a zero lookback", fake.starts[0], fake.ends[0])
	}
}

type panickingCloudTrail struct{}

func (panickingCloudTrail) LookupEvents(context.Context, *cloudtrail.LookupEventsInput, ...func(*cloudtrail.Options)) (*cloudtrail.LookupEventsOutput, error) {
	panic("unexpected LookupEvents response shape")
}

// Each region runs on its own goroutine, out of reach of the runner's
// per-source recover, so a region recovers its own panic. Otherwise one bad
// SDK response would take down the controller.
func TestCloudTrailRegionPanicIsContained(t *testing.T) {
	west := &pagingCloudTrail{pages: 1}
	src := &cloudTrailSource{
		regions:    []cloudTrailRegion{{region: "us-east-1", client: panickingCloudTrail{}}, {region: "us-west-2", client: west}},
		events:     []string{"CreateUser"},
		lookback:   time.Minute,
		lookupRate: rate.Inf,
	}
	emit, got := syncCollect()
	src.Poll(context.Background(), emit)

	if len(*got) != 1 {
		t.Fatalf("the healthy region should still emit its event, got %d alerts", len(*got))
	}
}

func TestNewCloudTrailSourceDefaults(t *testing.T) {
	cfg := config.AWS{}
	cfg.PollSeconds = 60

	src := newCloudTrailSource(nil, cfg)
	if len(src.events) == 0 {
		t.Fatal("empty cloudtrailEvents should fall back to the curated default set")
	}
	if src.lookback != 120*time.Second {
		t.Fatalf("lookback = %v, want 2x poll interval (120s)", src.lookback)
	}
	if src.lookupRate != cloudTrailRate {
		t.Fatalf("lookupRate = %v, want %v (under the 2 req/s LookupEvents quota)", src.lookupRate, cloudTrailRate)
	}

	cfg.CloudTrailEvents = []string{"CreateUser"}
	if src2 := newCloudTrailSource(nil, cfg); len(src2.events) != 1 || src2.events[0] != "CreateUser" {
		t.Errorf("explicit cloudtrailEvents override not honored: %v", src2.events)
	}
}
