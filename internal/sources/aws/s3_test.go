package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"alertkube/internal/alert"
)

type fakeS3 struct {
	buckets   []string
	policy    map[string]*s3.GetBucketPolicyStatusOutput
	policyErr map[string]error
	pab       map[string]*s3.GetPublicAccessBlockOutput
	pabErr    map[string]error
	listErr   error
}

func (f *fakeS3) ListBuckets(_ context.Context, _ *s3.ListBucketsInput, _ ...func(*s3.Options)) (*s3.ListBucketsOutput, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var bs []s3types.Bucket
	for _, n := range f.buckets {
		bs = append(bs, s3types.Bucket{Name: awssdk.String(n)})
	}
	return &s3.ListBucketsOutput{Buckets: bs}, nil
}

func (f *fakeS3) GetBucketPolicyStatus(_ context.Context, in *s3.GetBucketPolicyStatusInput, _ ...func(*s3.Options)) (*s3.GetBucketPolicyStatusOutput, error) {
	b := awssdk.ToString(in.Bucket)
	if e := f.policyErr[b]; e != nil {
		return nil, e
	}
	return f.policy[b], nil
}

func (f *fakeS3) GetPublicAccessBlock(_ context.Context, in *s3.GetPublicAccessBlockInput, _ ...func(*s3.Options)) (*s3.GetPublicAccessBlockOutput, error) {
	b := awssdk.ToString(in.Bucket)
	if e := f.pabErr[b]; e != nil {
		return nil, e
	}
	return f.pab[b], nil
}

func policyStatus(isPublic bool) *s3.GetBucketPolicyStatusOutput {
	return &s3.GetBucketPolicyStatusOutput{PolicyStatus: &s3types.PolicyStatus{IsPublic: awssdk.Bool(isPublic)}}
}

func pabAll(on bool) *s3.GetPublicAccessBlockOutput {
	return &s3.GetPublicAccessBlockOutput{PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{
		BlockPublicAcls:       awssdk.Bool(on),
		IgnorePublicAcls:      awssdk.Bool(on),
		BlockPublicPolicy:     awssdk.Bool(on),
		RestrictPublicBuckets: awssdk.Bool(on),
	}}
}

func apiErr(code string) error { return &smithy.GenericAPIError{Code: code, Message: code} }

func TestIsAPIErrCode(t *testing.T) {
	if !isAPIErrCode(apiErr("NoSuchBucketPolicy"), "NoSuchBucketPolicy") {
		t.Error("expected code match")
	}
	if isAPIErrCode(apiErr("AccessDenied"), "NoSuchBucketPolicy") {
		t.Error("unexpected code match")
	}
	if isAPIErrCode(context.Canceled, "NoSuchBucketPolicy") {
		t.Error("non-API error should not match")
	}
}

func TestS3SourcePoll(t *testing.T) {
	fake := &fakeS3{
		buckets: []string{"pub-policy", "safe", "unblocked", "denied"},
		policy: map[string]*s3.GetBucketPolicyStatusOutput{
			"pub-policy": policyStatus(true),
			"safe":       policyStatus(false),
		},
		policyErr: map[string]error{
			"unblocked": apiErr("NoSuchBucketPolicy"),
			"denied":    apiErr("AccessDenied"),
		},
		pab: map[string]*s3.GetPublicAccessBlockOutput{
			"safe": pabAll(true),
		},
		pabErr: map[string]error{
			"pub-policy": apiErr("NoSuchPublicAccessBlockConfiguration"),
			"unblocked":  apiErr("NoSuchPublicAccessBlockConfiguration"),
		},
	}
	src := &s3Source{client: fake}
	emit, got := collect()
	src.Poll(context.Background(), emit)

	byName := map[string]*alert.Alert{}
	for _, a := range *got {
		byName[a.Name] = a
		if a.Kind != alert.KindS3Bucket || a.Namespace != s3Scope {
			t.Errorf("bad identity for %s: kind=%s ns=%s", a.Name, a.Kind, a.Namespace)
		}
	}
	// pub-policy: public bucket policy -> critical.
	if a := byName["pub-policy"]; a == nil || a.Resolved || a.Severity != alert.SeverityCritical || a.Reason != "S3BucketPublic" {
		t.Errorf("pub-policy should be critical S3BucketPublic: %+v", a)
	}
	// safe: not public + PAB fully on -> resolve.
	if a := byName["safe"]; a == nil || !a.Resolved {
		t.Errorf("safe should resolve: %+v", a)
	}
	// unblocked: no policy + no PAB -> warning.
	if a := byName["unblocked"]; a == nil || a.Resolved || a.Severity != alert.SeverityWarning || a.Reason != "S3BucketPublicAccessNotBlocked" {
		t.Errorf("unblocked should be warning S3BucketPublicAccessNotBlocked: %+v", a)
	}
	// denied: AccessDenied on policy -> skipped (no emit).
	if _, ok := byName["denied"]; ok {
		t.Errorf("denied bucket should be skipped, got %+v", byName["denied"])
	}
	if len(*got) != 3 {
		t.Fatalf("expected 3 alerts (denied skipped), got %d", len(*got))
	}
}
