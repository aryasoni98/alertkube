package sources

import (
	"context"
	"errors"

	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/metrics"
)

// Shared helpers for the polled cloud providers (AWS/Azure/GCP). Each provider
// package wraps EmitFiring to attach its own provider label; its emitResolve and
// pollErr are plain pass-throughs. The emit/resolve/error shape lives in exactly
// one place.
//
// Identity convention: the provider scope (AWS region, Azure subscription, GCP
// "project/location") rides in the alert Namespace, and the resource id in Name,
// so a resolve - which the store matches on kind+namespace+name - targets
// exactly one cloud resource and never clears an unrelated one.

// EmitFiring publishes a firing cloud alert. labels are attached verbatim (e.g.
// {"provider":"aws","region":"us-east-1"}); empty detail values are dropped so
// sinks never render blank rows.
func EmitFiring(emit Emit, k alert.Kind, scope, name, reason, summary string, sev alert.Severity, labels, details map[string]string) {
	a := alert.New(k, scope, name, reason, sev)
	a.Summary = summary
	for key, v := range labels {
		a.Labels[key] = v
	}
	for key, v := range details {
		if v != "" {
			a.Details[key] = v
		}
	}
	emit(a)
}

// EmitResolve clears any active alert for one cloud resource. Identity only,
// Resolved=true, no reason/severity - the store resolves every active alert for
// kind+scope+name. A resolve for a resource with no active alert is a no-op, so
// callers may emit it for every healthy resource each poll without producing
// spurious "resolved" notifications.
func EmitResolve(emit Emit, k alert.Kind, scope, name string) {
	emit(&alert.Alert{Kind: k, Namespace: scope, Name: name, Resolved: true})
}

// PollErr records a per-source poll failure on the shared metric and logs it,
// so a blinded cloud source is observable without crashing the controller.
func PollErr(source, scope string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	metrics.CloudPollErrors.WithLabelValues(source).Inc()
	klog.Warningf("%s poll failed (%s): %v", source, scope, err)
}

// MaxPages is the runaway guard for one paginated cloud list, not a routine
// truncation limit: at SDK default page sizes it is tens of thousands of
// resources, far past a real account. It stops a pager whose token keeps
// changing but never ends; the AWS and Cloud SQL loops also stop sooner on a
// token that does not advance.
const MaxPages = 1000

// PollTruncated records a paginated list that stopped before its last page on
// the shared metric and logs why. Resources on the unfetched pages are absent
// from the list, so they are neither re-fired nor resolved this poll. scope may
// be empty when the caller does not know it (the Azure pagers).
func PollTruncated(source, scope, why string) {
	metrics.CloudPollTruncated.WithLabelValues(source).Inc()
	where := ""
	if scope != "" {
		where = " (" + scope + ")"
	}
	klog.Warningf("%s poll truncated%s: %s; remaining pages were not fetched", source, where, why)
}

// Compact drops the nil entries a disabled source toggle leaves behind, so a
// provider's Build can list every candidate source unconditionally - one line
// per service, in a fixed order - and filter once at the end instead of
// guarding each append.
func Compact(srcs []Source) []Source {
	out := make([]Source, 0, len(srcs))
	for _, s := range srcs {
		if s != nil {
			out = append(out, s)
		}
	}
	return out
}

// BuildAll runs a provider's deferred source builders in order and compacts the
// result. The first construction error aborts the provider, so a misconfigured
// client is reported once instead of leaving a partly built provider. A builder
// for a disabled service returns (nil, nil), which Compact drops.
func BuildAll(builders ...func() (Source, error)) ([]Source, error) {
	srcs := make([]Source, 0, len(builders))
	for _, build := range builders {
		s, err := build()
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, s)
	}
	return Compact(srcs), nil
}

// Scope joins a provider parent scope (Azure subscription, GCP project) with a
// location qualifier (region/zone) into the alert-identity scope, omitting the
// separator when the location is unknown so identities stay stable.
func Scope(parent, location string) string {
	if location == "" {
		return parent
	}
	return parent + "/" + location
}
