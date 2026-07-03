package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceKMS = "aws-kms"

type kmsRegion = regionClient[kmsAPI]

// kmsSource alerts on customer-managed KMS keys in a risky state:
// PendingDeletion (scheduled destruction → potential data loss) and
// Unavailable are critical; Disabled is a warning; Enabled resolves.
// AWS-managed keys are skipped - their lifecycle is not the operator's concern.
type kmsSource struct {
	regions []kmsRegion
}

func (s *kmsSource) Name() string { return sourceKMS }

func (s *kmsSource) Poll(ctx context.Context, emit sources.Emit) {
	pollByRegion(ctx, s.regions, emit, s.pollRegion)
}

func (s *kmsSource) pollRegion(ctx context.Context, rc kmsRegion, emit sources.Emit) {
	forEachPage(ctx, sourceKMS, rc.region, func(ctx context.Context, marker *string) (*string, error) {
		out, err := rc.client.ListKeys(ctx, &kms.ListKeysInput{Marker: marker})
		if err != nil {
			return nil, err
		}
		for _, k := range out.Keys {
			id := awssdk.ToString(k.KeyId)
			if id == "" {
				continue
			}
			desc, err := rc.client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: k.KeyId})
			if err != nil {
				pollErr(sourceKMS, rc.region, err)
				continue
			}
			if desc == nil || desc.KeyMetadata == nil {
				continue
			}
			evaluateKMSKey(rc.region, desc.KeyMetadata, emit)
		}
		return out.NextMarker, nil
	})
}

func evaluateKMSKey(region string, md *kmstypes.KeyMetadata, emit sources.Emit) {
	id := awssdk.ToString(md.KeyId)
	if id == "" || md.KeyManager != kmstypes.KeyManagerTypeCustomer {
		return
	}
	details := map[string]string{"keyState": string(md.KeyState)}
	switch md.KeyState {
	case kmstypes.KeyStateEnabled:
		emitResolve(emit, alert.KindKMSKey, region, id)
	case kmstypes.KeyStatePendingDeletion, kmstypes.KeyStatePendingReplicaDeletion:
		emitFiring(emit, alert.KindKMSKey, region, id, "KMSKeyPendingDeletion",
			"KMS key "+id+" is scheduled for deletion", alert.SeverityCritical, details)
	case kmstypes.KeyStateUnavailable:
		emitFiring(emit, alert.KindKMSKey, region, id, "KMSKeyUnavailable",
			"KMS key "+id+" is unavailable", alert.SeverityCritical, details)
	case kmstypes.KeyStateDisabled:
		emitFiring(emit, alert.KindKMSKey, region, id, "KMSKeyDisabled",
			"KMS key "+id+" is disabled", alert.SeverityWarning, details)
	default:
		// PendingImport / Creating / Updating: transient.
		emitResolve(emit, alert.KindKMSKey, region, id)
	}
}
