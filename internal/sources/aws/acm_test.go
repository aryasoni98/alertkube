package aws

import (
	"context"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	"alertkube/internal/alert"
)

type fakeACM struct {
	pages []*acm.ListCertificatesOutput
	idx   int
	err   error
}

func (f *fakeACM) ListCertificates(_ context.Context, _ *acm.ListCertificatesInput, _ ...func(*acm.Options)) (*acm.ListCertificatesOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := f.pages[f.idx]
	if f.idx < len(f.pages)-1 {
		f.idx++
	}
	return out, nil
}

func cert(arn string, status acmtypes.CertificateStatus, notAfter *time.Time) acmtypes.CertificateSummary {
	return acmtypes.CertificateSummary{
		CertificateArn: awssdk.String(arn),
		DomainName:     awssdk.String("example.com"),
		Status:         status,
		NotAfter:       notAfter,
	}
}

func TestEvaluateCertificate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { tt := now.Add(d); return &tt }
	day := 24 * time.Hour
	cases := []struct {
		name         string
		summary      acmtypes.CertificateSummary
		wantEmit     bool
		wantResolved bool
		wantSeverity alert.Severity
	}{
		{"expired critical", cert("a", acmtypes.CertificateStatusExpired, at(-day)), true, false, alert.SeverityCritical},
		{"revoked critical", cert("a", acmtypes.CertificateStatusRevoked, nil), true, false, alert.SeverityCritical},
		{"failed critical", cert("a", acmtypes.CertificateStatusFailed, nil), true, false, alert.SeverityCritical},
		{"validation-timeout critical", cert("a", acmtypes.CertificateStatusValidationTimedOut, nil), true, false, alert.SeverityCritical},
		{"issued expiring-soon warning", cert("a", acmtypes.CertificateStatusIssued, at(10*day)), true, false, alert.SeverityWarning},
		{"issued healthy resolves", cert("a", acmtypes.CertificateStatusIssued, at(60*day)), true, true, ""},
		{"issued no-expiry resolves", cert("a", acmtypes.CertificateStatusIssued, nil), true, true, ""},
		{"pending resolves", cert("a", acmtypes.CertificateStatusPendingValidation, nil), true, true, ""},
		{"empty arn skipped", cert("", acmtypes.CertificateStatusExpired, nil), false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateCertificate("us-east-1", now, tc.summary, emit)
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
			if a.Kind != alert.KindACMCertificate {
				t.Errorf("kind = %s, want ACMCertificate", a.Kind)
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

func TestACMSourcePoll(t *testing.T) {
	now := time.Now()
	fake := &fakeACM{pages: []*acm.ListCertificatesOutput{{
		CertificateSummaryList: []acmtypes.CertificateSummary{
			cert("good", acmtypes.CertificateStatusIssued, awssdk.Time(now.Add(90*24*time.Hour))),
			cert("bad", acmtypes.CertificateStatusExpired, awssdk.Time(now.Add(-24*time.Hour))),
		},
	}}}
	src := &acmSource{regions: []acmRegion{{region: "us-east-1", client: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}
