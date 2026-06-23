package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"

	"alertkube/internal/alert"
)

type fakeKMS struct {
	keys []string
	meta map[string]*kmstypes.KeyMetadata
	err  error
}

func (f *fakeKMS) ListKeys(_ context.Context, _ *kms.ListKeysInput, _ ...func(*kms.Options)) (*kms.ListKeysOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	entries := make([]kmstypes.KeyListEntry, 0, len(f.keys))
	for _, id := range f.keys {
		entries = append(entries, kmstypes.KeyListEntry{KeyId: awssdk.String(id)})
	}
	return &kms.ListKeysOutput{Keys: entries}, nil
}

func (f *fakeKMS) DescribeKey(_ context.Context, in *kms.DescribeKeyInput, _ ...func(*kms.Options)) (*kms.DescribeKeyOutput, error) {
	return &kms.DescribeKeyOutput{KeyMetadata: f.meta[awssdk.ToString(in.KeyId)]}, nil
}

func keyMeta(id string, mgr kmstypes.KeyManagerType, state kmstypes.KeyState) *kmstypes.KeyMetadata {
	return &kmstypes.KeyMetadata{KeyId: awssdk.String(id), KeyManager: mgr, KeyState: state}
}

func TestEvaluateKMSKey(t *testing.T) {
	cust := kmstypes.KeyManagerTypeCustomer
	cases := []struct {
		name         string
		meta         *kmstypes.KeyMetadata
		wantEmit     bool
		wantResolved bool
		wantSeverity alert.Severity
	}{
		{"enabled customer resolves", keyMeta("k", cust, kmstypes.KeyStateEnabled), true, true, ""},
		{"pending-deletion critical", keyMeta("k", cust, kmstypes.KeyStatePendingDeletion), true, false, alert.SeverityCritical},
		{"unavailable critical", keyMeta("k", cust, kmstypes.KeyStateUnavailable), true, false, alert.SeverityCritical},
		{"disabled warning", keyMeta("k", cust, kmstypes.KeyStateDisabled), true, false, alert.SeverityWarning},
		{"aws-managed skipped", keyMeta("k", kmstypes.KeyManagerTypeAws, kmstypes.KeyStateDisabled), false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateKMSKey("us-east-1", tc.meta, emit)
			if !tc.wantEmit {
				if len(*got) != 0 {
					t.Fatalf("expected no emit (aws-managed key), got %d", len(*got))
				}
				return
			}
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindKMSKey {
				t.Errorf("kind = %s, want KMSKey", a.Kind)
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

func TestKMSSourcePoll(t *testing.T) {
	cust := kmstypes.KeyManagerTypeCustomer
	fake := &fakeKMS{
		keys: []string{"good", "bad", "awsmanaged"},
		meta: map[string]*kmstypes.KeyMetadata{
			"good":       keyMeta("good", cust, kmstypes.KeyStateEnabled),
			"bad":        keyMeta("bad", cust, kmstypes.KeyStatePendingDeletion),
			"awsmanaged": keyMeta("awsmanaged", kmstypes.KeyManagerTypeAws, kmstypes.KeyStateDisabled),
		},
	}
	src := &kmsSource{regions: []kmsRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	// good -> resolve, bad -> critical, aws-managed -> skipped = 2 alerts.
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts (aws-managed skipped), got %d", len(*got))
	}
}
