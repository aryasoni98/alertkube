package aws

import (
	"context"
	"errors"
	"strconv"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"golang.org/x/time/rate"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/sources"
)

const sourceS3 = "aws-s3"

// s3API is the subset of the S3 client the public-access source uses. S3 is a
// global service: ListBuckets returns the whole account regardless of the
// client's region.
type s3API interface {
	ListBuckets(context.Context, *s3.ListBucketsInput, ...func(*s3.Options)) (*s3.ListBucketsOutput, error)
	GetBucketPolicyStatus(context.Context, *s3.GetBucketPolicyStatusInput, ...func(*s3.Options)) (*s3.GetBucketPolicyStatusOutput, error)
	GetPublicAccessBlock(context.Context, *s3.GetPublicAccessBlockInput, ...func(*s3.Options)) (*s3.GetPublicAccessBlockOutput, error)
}

type s3Source struct {
	client s3API
}

func (s *s3Source) Name() string { return sourceS3 }

// Poll lists every bucket and alerts on those that are publicly exposed
// (public bucket policy → critical) or not fully protected by a public-access
// block (→ warning). It is the brief's "S3 Public Access Alerts".
//
// ListBuckets is paginated: an account with more than one page of buckets
// (ListBuckets caps a page, historically at 1000) would otherwise leave later
// buckets unchecked - so a publicly-exposed bucket beyond the first page would
// be silently missed. forEachPage walks every page via the continuation token.
func (s *s3Source) Poll(ctx context.Context, emit sources.Emit) {
	lim := newDescribeLimiter()
	forEachPage(ctx, sourceS3, globalScope, func(ctx context.Context, token *string) (*string, error) {
		out, err := s.client.ListBuckets(ctx, &s3.ListBucketsInput{ContinuationToken: token})
		if err != nil {
			return nil, err
		}
		for _, b := range out.Buckets {
			name := awssdk.ToString(b.Name)
			if name == "" {
				continue
			}
			if err := s.evaluateBucket(ctx, lim, name, awssdk.ToString(b.BucketRegion), emit); err != nil {
				return nil, err
			}
		}
		return out.ContinuationToken, nil
	})
}

// evaluateBucket runs the two per-bucket checks and classifies the bucket. It
// returns only the describe-limiter error, which must stop the listing; an API
// error is recorded by the check and skips just this bucket.
func (s *s3Source) evaluateBucket(ctx context.Context, lim *rate.Limiter, name, region string, emit sources.Emit) error {
	if err := waitDescribe(ctx, lim); err != nil {
		return err
	}
	isPublic, ok := s.bucketIsPublic(ctx, name, region)
	if !ok {
		return nil // a real API error was already recorded; skip rather than flap
	}
	if err := waitDescribe(ctx, lim); err != nil {
		return err
	}
	blocked, ok := s.publicAccessBlocked(ctx, name, region)
	if !ok {
		return nil
	}
	switch {
	case isPublic:
		emitFiring(emit, alert.KindS3Bucket, globalScope, name, "S3BucketPublic",
			"S3 bucket "+name+" is publicly accessible via its bucket policy", alert.SeverityCritical,
			map[string]string{"publicAccessBlock": strconv.FormatBool(blocked)})
	case !blocked:
		emitFiring(emit, alert.KindS3Bucket, globalScope, name, "S3BucketPublicAccessNotBlocked",
			"S3 bucket "+name+" does not fully enable the public-access block", alert.SeverityWarning,
			map[string]string{"publicAccessBlock": "false"})
	default:
		emitResolve(emit, alert.KindS3Bucket, globalScope, name)
	}
	return nil
}

// bucketIsPublic reports whether the bucket policy makes the bucket public. A
// missing policy (NoSuchBucketPolicy) means "not public via policy". ok is
// false only on an unexpected API error (already recorded), so the caller
// skips the bucket rather than flapping its alert.
func (s *s3Source) bucketIsPublic(ctx context.Context, bucket, region string) (public, ok bool) {
	out, err := s.client.GetBucketPolicyStatus(ctx, &s3.GetBucketPolicyStatusInput{Bucket: awssdk.String(bucket)}, s3Region(region))
	if err != nil {
		if isAPIErrCode(err, "NoSuchBucketPolicy") {
			return false, true
		}
		pollErr(sourceS3, globalScope, err)
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
func (s *s3Source) publicAccessBlocked(ctx context.Context, bucket, region string) (blocked, ok bool) {
	out, err := s.client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: awssdk.String(bucket)}, s3Region(region))
	if err != nil {
		if isAPIErrCode(err, "NoSuchPublicAccessBlockConfiguration") {
			return false, true
		}
		pollErr(sourceS3, globalScope, err)
		return false, false
	}
	c := out.PublicAccessBlockConfiguration
	if c == nil {
		return false, true
	}
	return awssdk.ToBool(c.BlockPublicAcls) && awssdk.ToBool(c.IgnorePublicAcls) &&
		awssdk.ToBool(c.BlockPublicPolicy) && awssdk.ToBool(c.RestrictPublicBuckets), true
}

// s3Region routes the request to the bucket's home region when it is known.
func s3Region(region string) func(*s3.Options) {
	return func(o *s3.Options) {
		if region != "" {
			o.Region = region
		}
	}
}

// isAPIErrCode reports whether err is a smithy API error carrying the given
// AWS error code (e.g. "NoSuchBucketPolicy").
func isAPIErrCode(err error, code string) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && ae.ErrorCode() == code
}
