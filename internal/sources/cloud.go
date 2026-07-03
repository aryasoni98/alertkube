package sources

import (
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/metrics"
)

// Shared helpers for the polled cloud providers (AWS/Azure/GCP). Each provider
// package wraps these with its own provider constant so call sites stay terse
// while the emit/resolve/error shape lives in exactly one place.
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
	metrics.CloudPollErrors.WithLabelValues(source).Inc()
	klog.Warningf("%s poll failed (%s): %v", source, scope, err)
}

// StrVal dereferences a *string, returning "" for nil. Cloud SDK response
// fields are overwhelmingly *string; this is the shared nil-safe accessor.
func StrVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
