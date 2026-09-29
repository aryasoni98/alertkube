package aws

import (
	"context"
	"fmt"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/metrics"
)

type fakeDynamo struct {
	pages     [][]string
	idx       int
	statuses  map[string]dynamodbtypes.TableStatus
	listErr   error
	listCalls int
}

func (f *fakeDynamo) ListTables(_ context.Context, _ *dynamodb.ListTablesInput, _ ...func(*dynamodb.Options)) (*dynamodb.ListTablesOutput, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := &dynamodb.ListTablesOutput{TableNames: f.pages[f.idx]}
	if f.idx < len(f.pages)-1 {
		f.idx++
		out.LastEvaluatedTableName = awssdk.String("next")
	}
	return out, nil
}

func (f *fakeDynamo) DescribeTable(_ context.Context, in *dynamodb.DescribeTableInput, _ ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	return &dynamodb.DescribeTableOutput{
		Table: &dynamodbtypes.TableDescription{TableStatus: f.statuses[awssdk.ToString(in.TableName)]},
	}, nil
}

func TestEvaluateDynamoTable(t *testing.T) {
	cases := []struct {
		name         string
		table        string
		status       dynamodbtypes.TableStatus
		wantEmit     bool
		wantResolved bool
		wantSeverity alert.Severity
	}{
		{"inaccessible critical", "t", dynamodbtypes.TableStatusInaccessibleEncryptionCredentials, true, false, alert.SeverityCritical},
		{"archived warning", "t", dynamodbtypes.TableStatusArchived, true, false, alert.SeverityWarning},
		{"active resolves", "t", dynamodbtypes.TableStatusActive, true, true, ""},
		{"creating resolves", "t", dynamodbtypes.TableStatusCreating, true, true, ""},
		{"empty name skipped", "", dynamodbtypes.TableStatusArchived, false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateDynamoTable("us-east-1", tc.table, tc.status, emit)
			if !tc.wantEmit {
				if len(*got) != 0 {
					t.Fatalf("expected no emit, got %d", len(*got))
				}
				return
			}
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindDynamoDBTable {
				t.Errorf("kind = %s, want DynamoDBTable", a.Kind)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && a.Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", a.Severity, tc.wantSeverity)
			}
		})
	}
}

func TestDynamoSourcePollPaginates(t *testing.T) {
	fake := &fakeDynamo{
		pages: [][]string{{"t-bad"}, {"t-ok"}},
		statuses: map[string]dynamodbtypes.TableStatus{
			"t-bad": dynamodbtypes.TableStatusInaccessibleEncryptionCredentials,
			"t-ok":  dynamodbtypes.TableStatusActive,
		},
	}
	src := &dynamoDBSource{regions: []dynRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts across 2 pages, got %d", len(*got))
	}
	for _, a := range *got {
		switch a.Name {
		case "t-bad":
			if a.Resolved || a.Severity != alert.SeverityCritical {
				t.Errorf("t-bad should be critical firing: %+v", a)
			}
		case "t-ok":
			if !a.Resolved {
				t.Errorf("t-ok should resolve: %+v", a)
			}
		default:
			t.Errorf("unexpected table %q", a.Name)
		}
	}
}

// TestDynamoSourcePollStopsWhenDescribeBudgetRunsOut drains the describe
// limiter under a poll deadline shorter than its refill interval. The limiter
// then fails the next wait at once while ctx.Err() is still nil. The region
// must record that as a poll error and stop listing. Treating it as the last
// page would skip the rest of the inventory with no metric, and the skipped
// tables' alerts would TTL-resolve.
func TestDynamoSourcePollStopsWhenDescribeBudgetRunsOut(t *testing.T) {
	page := make([]string, describeBurst+5)
	for i := range page {
		page[i] = fmt.Sprintf("t-%02d", i)
	}
	fake := &fakeDynamo{pages: [][]string{page, {"t-late"}}}
	src := &dynamoDBSource{regions: []dynRegion{{region: "us-east-1", client: fake}}}
	before := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceDynamoDB))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	emit, got := collect()
	src.Poll(ctx, emit)

	if d := testutil.ToFloat64(metrics.CloudPollErrors.WithLabelValues(sourceDynamoDB)) - before; d != 1 {
		t.Errorf("CloudPollErrors delta = %v, want 1: a poll that ran out of describe budget must be observable", d)
	}
	if fake.listCalls != 1 {
		t.Errorf("ListTables calls = %d, want 1: listing must stop once the describe budget runs out", fake.listCalls)
	}
	if len(*got) >= len(page) {
		t.Errorf("evaluated %d of %d tables; the describe budget should have run out first", len(*got), len(page))
	}
}
