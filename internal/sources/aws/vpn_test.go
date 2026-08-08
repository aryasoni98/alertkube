package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/aryasoni98/alertkube/internal/alert"
)

type fakeVPN struct {
	out *ec2.DescribeVpnConnectionsOutput
	err error
}

func (f *fakeVPN) DescribeVpnConnections(_ context.Context, _ *ec2.DescribeVpnConnectionsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpnConnectionsOutput, error) {
	return f.out, f.err
}

func vpnConn(id string, state ec2types.VpnState, tunnels ...ec2types.TelemetryStatus) ec2types.VpnConnection {
	vc := ec2types.VpnConnection{VpnConnectionId: awssdk.String(id), State: state}
	for _, s := range tunnels {
		vc.VgwTelemetry = append(vc.VgwTelemetry, ec2types.VgwTelemetry{Status: s})
	}
	return vc
}

func TestEvaluateVpnConnection(t *testing.T) {
	up, down := ec2types.TelemetryStatusUp, ec2types.TelemetryStatusDown
	avail := ec2types.VpnStateAvailable
	cases := []struct {
		name         string
		conn         ec2types.VpnConnection
		wantEmit     bool
		wantResolved bool
		wantSeverity alert.Severity
	}{
		{"both up resolves", vpnConn("v", avail, up, up), true, true, ""},
		{"both down critical", vpnConn("v", avail, down, down), true, false, alert.SeverityCritical},
		{"one down warning", vpnConn("v", avail, up, down), true, false, alert.SeverityWarning},
		{"no telemetry resolves", vpnConn("v", avail), true, true, ""},
		{"deleting resolves", vpnConn("v", ec2types.VpnStateDeleting, down, down), true, true, ""},
		{"empty id skipped", vpnConn("", avail, down, down), false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateVpnConnection("us-east-1", tc.conn, emit)
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
			if a.Kind != alert.KindVPNConnection {
				t.Errorf("kind = %s, want VPNConnection", a.Kind)
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

func TestVPNSourcePoll(t *testing.T) {
	up, down := ec2types.TelemetryStatusUp, ec2types.TelemetryStatusDown
	fake := &fakeVPN{out: &ec2.DescribeVpnConnectionsOutput{
		VpnConnections: []ec2types.VpnConnection{
			vpnConn("good", ec2types.VpnStateAvailable, up, up),
			vpnConn("bad", ec2types.VpnStateAvailable, down, down),
		},
	}}
	src := &vpnSource{regions: []vpnRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}
