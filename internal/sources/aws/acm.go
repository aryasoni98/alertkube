package aws

import (
	"context"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	"alertkube/internal/alert"
	"alertkube/internal/sources"
)

const sourceACM = "aws-acm"

// acmExpiryWindow is how far before NotAfter an ISSUED certificate starts firing
// a warning. 30 days is a sane renewal lead time; it is a code-level constant
// rather than a config knob to keep the source surface small.
const acmExpiryWindow = 30 * 24 * time.Hour

type acmRegion = regionClient[acmAPI]

// acmSource alerts on ACM certificates that are unusable or near expiry. EXPIRED
// / REVOKED / FAILED / VALIDATION_TIMED_OUT are critical (the cert cannot serve
// traffic); an ISSUED cert whose NotAfter is within acmExpiryWindow is a warning
// (renew soon); PENDING_VALIDATION / INACTIVE and a healthy ISSUED cert resolve,
// so in-progress issuance never pages. Certs paginate with NextToken.
type acmSource struct {
	regions []acmRegion
}

func (s *acmSource) Name() string { return sourceACM }

func (s *acmSource) Poll(ctx context.Context, emit sources.Emit) {
	now := time.Now()
	pollByRegion(ctx, s.regions, emit, func(ctx context.Context, rc acmRegion, emit sources.Emit) {
		s.pollRegion(ctx, rc, now, emit)
	})
}

func (s *acmSource) pollRegion(ctx context.Context, rc acmRegion, now time.Time, emit sources.Emit) {
	forEachPage(ctx, sourceACM, rc.region, func(ctx context.Context, token *string) (*string, error) {
		out, err := rc.client.ListCertificates(ctx, &acm.ListCertificatesInput{NextToken: token})
		if err != nil {
			return nil, err
		}
		for i := range out.CertificateSummaryList {
			evaluateCertificate(rc.region, now, out.CertificateSummaryList[i], emit)
		}
		return out.NextToken, nil
	})
}

func evaluateCertificate(region string, now time.Time, c acmtypes.CertificateSummary, emit sources.Emit) {
	arn := awssdk.ToString(c.CertificateArn)
	if arn == "" {
		return
	}
	domain := awssdk.ToString(c.DomainName)
	details := map[string]string{"domain": domain, "status": string(c.Status)}
	switch c.Status {
	case acmtypes.CertificateStatusExpired,
		acmtypes.CertificateStatusRevoked,
		acmtypes.CertificateStatusFailed,
		acmtypes.CertificateStatusValidationTimedOut:
		emitFiring(emit, alert.KindACMCertificate, region, arn, "ACMCertificateUnusable",
			"ACM certificate "+domain+" is "+string(c.Status), alert.SeverityCritical, details)
		return
	case acmtypes.CertificateStatusIssued:
		if c.NotAfter != nil && c.NotAfter.Sub(now) < acmExpiryWindow {
			details["notAfter"] = c.NotAfter.UTC().Format(time.RFC3339)
			emitFiring(emit, alert.KindACMCertificate, region, arn, "ACMCertificateExpiringSoon",
				"ACM certificate "+domain+" expires "+c.NotAfter.UTC().Format(time.RFC3339),
				alert.SeverityWarning, details)
			return
		}
	}
	emitResolve(emit, alert.KindACMCertificate, region, arn)
}
