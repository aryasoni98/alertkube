package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/aryasoni98/alertkube/internal/alert"
)

type fakeDynamo struct {
	pages    [][]string
	idx      int
	statuses map[string]dynamodbtypes.TableStatus
	listErr  error
}

func (f *fakeDynamo) ListTables(_ context.Context, _ *dynamodb.ListTablesInput, _ ...func(*dynamodb.Options)) (*dynamodb.ListTablesOutput, error) {
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
