package aws

import (
	"context"
	"errors"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceS3 = "aws-s3"

// s3Scope is the pseudo-namespace for S3 alerts. S3 is a global service, so
// rather than a real AWS region the alerts carry a constant scope; a resolve
// still targets exactly one bucket via kind+namespace+name.
const s3Scope = "global"

type s3Source struct {
	client s3API
}

func (s *s3Source) Name() string { return sourceS3 }

// Poll lists every bucket and alerts on those that are publicly exposed
// (public bucket policy → critical) or not fully protected by a public-access
// block (→ warning). It is the brief's "S3 Public Access Alerts".
func (s *s3Source) Poll(ctx context.Context, emit sources.Emit) {
	out, err := s.client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		pollErr(sourceS3, s3Scope, err)
		return
	}
	for _, b := range out.Buckets {
		name := awssdk.ToString(b.Name)
		if name == "" {
			continue
		}
		s.evaluateBucket(ctx, name, emit)
	}
}

func (s *s3Source) evaluateBucket(ctx context.Context, name string, emit sources.Emit) {
	isPublic, ok := s.bucketIsPublic(ctx, name)
	if !ok {
		return // a real API error was already recorded; skip rather than flap
	}
	blocked, ok := s.publicAccessBlocked(ctx, name)
	if !ok {
		return
	}
	switch {
	case isPublic:
		emitFiring(emit, alert.KindS3Bucket, s3Scope, name, "S3BucketPublic",
			"S3 bucket "+name+" is publicly accessible via its bucket policy", alert.SeverityCritical,
			map[string]string{"publicAccessBlock": boolStr(blocked)})
	case !blocked:
		emitFiring(emit, alert.KindS3Bucket, s3Scope, name, "S3BucketPublicAccessNotBlocked",
			"S3 bucket "+name+" does not fully enable the public-access block", alert.SeverityWarning,
			map[string]string{"publicAccessBlock": "false"})
	default:
		emitResolve(emit, alert.KindS3Bucket, s3Scope, name)
	}
}

// bucketIsPublic reports whether the bucket policy makes the bucket public. A
// missing policy (NoSuchBucketPolicy) means "not public via policy". ok is
// false only on an unexpected API error (already recorded), so the caller
// skips the bucket rather than flapping its alert.
func (s *s3Source) bucketIsPublic(ctx context.Context, bucket string) (public, ok bool) {
	out, err := s.client.GetBucketPolicyStatus(ctx, &s3.GetBucketPolicyStatusInput{Bucket: awssdk.String(bucket)})
	if err != nil {
		if isAPIErrCode(err, "NoSuchBucketPolicy") {
			return false, true
		}
		pollErr(sourceS3, s3Scope, err)
		return false, false
	}
	if out.PolicyStatus == nil {
		return false, true
	}
	return awssdk.ToBool(out.PolicyStatus.IsPublic), true
}

// publicAccessBlocked reports whether all four public-access-block settings are
// enabled. A missing configuration (NoSuchPublicAccessBlockConfiguration) means
// not blocked.
func (s *s3Source) publicAccessBlocked(ctx context.Context, bucket string) (blocked, ok bool) {
	out, err := s.client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: awssdk.String(bucket)})
	if err != nil {
		if isAPIErrCode(err, "NoSuchPublicAccessBlockConfiguration") {
			return false, true
		}
		pollErr(sourceS3, s3Scope, err)
		return false, false
	}
	c := out.PublicAccessBlockConfiguration
	if c == nil {
		return false, true
	}
	return awssdk.ToBool(c.BlockPublicAcls) && awssdk.ToBool(c.IgnorePublicAcls) &&
		awssdk.ToBool(c.BlockPublicPolicy) && awssdk.ToBool(c.RestrictPublicBuckets), true
}

// isAPIErrCode reports whether err is a smithy API error carrying the given
// AWS error code (e.g. "NoSuchBucketPolicy").
func isAPIErrCode(err error, code string) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && ae.ErrorCode() == code
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
