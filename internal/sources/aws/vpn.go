package aws

import (
	"context"
	"strconv"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceVPN = "aws-vpn"

type vpnRegion struct {
	region string
	client vpnAPI
}

// vpnSource alerts on Site-to-Site VPN connections with degraded tunnel
// telemetry. A connection normally has two tunnels: all tunnels DOWN is critical
// (connectivity lost), some-but-not-all DOWN is a warning (redundancy lost), and
// all UP resolves. Connections not in the "available" state (pending / deleting
// / deleted) are lifecycle transitions, not failures, so they resolve.
// DescribeVpnConnections returns every connection in one call (no pagination).
type vpnSource struct {
	regions []vpnRegion
}

func (s *vpnSource) Name() string { return sourceVPN }

func (s *vpnSource) Poll(ctx context.Context, emit sources.Emit) {
	for _, rc := range s.regions {
		s.pollRegion(ctx, rc, emit)
	}
}

func (s *vpnSource) pollRegion(ctx context.Context, rc vpnRegion, emit sources.Emit) {
	out, err := rc.client.DescribeVpnConnections(ctx, &ec2.DescribeVpnConnectionsInput{})
	if err != nil {
		pollErr(sourceVPN, rc.region, err)
		return
	}
	for i := range out.VpnConnections {
		evaluateVpnConnection(rc.region, out.VpnConnections[i], emit)
	}
}

func evaluateVpnConnection(region string, vc ec2types.VpnConnection, emit sources.Emit) {
	id := awssdk.ToString(vc.VpnConnectionId)
	if id == "" {
		return
	}
	// Only an available connection has meaningful tunnel telemetry; lifecycle
	// states resolve so routine create/teardown never pages.
	total := len(vc.VgwTelemetry)
	down := 0
	for _, t := range vc.VgwTelemetry {
		if t.Status == ec2types.TelemetryStatusDown {
			down++
		}
	}
	if vc.State != ec2types.VpnStateAvailable || total == 0 || down == 0 {
		emitResolve(emit, alert.KindVPNConnection, region, id)
		return
	}
	details := map[string]string{
		"state":        string(vc.State),
		"tunnelsDown":  strconv.Itoa(down),
		"tunnelsTotal": strconv.Itoa(total),
	}
	if down == total {
		emitFiring(emit, alert.KindVPNConnection, region, id, "VPNConnectionDown",
			"VPN connection "+id+" has all "+strconv.Itoa(total)+" tunnels down",
			alert.SeverityCritical, details)
		return
	}
	emitFiring(emit, alert.KindVPNConnection, region, id, "VPNTunnelDegraded",
		"VPN connection "+id+" has "+strconv.Itoa(down)+" of "+strconv.Itoa(total)+" tunnels down",
		alert.SeverityWarning, details)
}
