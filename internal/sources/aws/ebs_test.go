package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/aryasoni98/alertkube/internal/alert"
)

type fakeEBS struct {
	pages []*ec2.DescribeVolumeStatusOutput
	idx   int
	err   error
}

func (f *fakeEBS) DescribeVolumeStatus(_ context.Context, _ *ec2.DescribeVolumeStatusInput, _ ...func(*ec2.Options)) (*ec2.DescribeVolumeStatusOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.pages[f.idx]
	if f.idx < len(f.pages)-1 {
		f.idx++
	}
	return out, nil
}

func volStatus(id string, status ec2types.VolumeStatusInfoStatus) ec2types.VolumeStatusItem {
	return ec2types.VolumeStatusItem{
		VolumeId:         awssdk.String(id),
		AvailabilityZone: awssdk.String("us-east-1a"),
		VolumeStatus:     &ec2types.VolumeStatusInfo{Status: status},
	}
}

func TestEvaluateVolume(t *testing.T) {
	cases := []struct {
		name         string
		item         ec2types.VolumeStatusItem
		wantEmit     bool
		wantResolved bool
		wantSeverity alert.Severity
	}{
		{"impaired critical", volStatus("v", ec2types.VolumeStatusInfoStatusImpaired), true, false, alert.SeverityCritical},
		{"insufficient-data warning", volStatus("v", ec2types.VolumeStatusInfoStatusInsufficientData), true, false, alert.SeverityWarning},
		{"ok resolves", volStatus("v", ec2types.VolumeStatusInfoStatusOk), true, true, ""},
		{"nil status resolves", ec2types.VolumeStatusItem{VolumeId: awssdk.String("v")}, true, true, ""},
		{"empty id skipped", volStatus("", ec2types.VolumeStatusInfoStatusImpaired), false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateVolume("us-east-1", tc.item, emit)
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
			if a.Kind != alert.KindEBSVolume {
				t.Errorf("kind = %s, want EBSVolume", a.Kind)
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

func TestEBSSourcePoll(t *testing.T) {
	fake := &fakeEBS{pages: []*ec2.DescribeVolumeStatusOutput{{
		VolumeStatuses: []ec2types.VolumeStatusItem{
			volStatus("good", ec2types.VolumeStatusInfoStatusOk),
			volStatus("bad", ec2types.VolumeStatusInfoStatusImpaired),
		},
	}}}
	src := &ebsSource{regions: []ebsRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}
