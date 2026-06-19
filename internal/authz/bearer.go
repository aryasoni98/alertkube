// Package authz holds the small auth primitives shared by the HTTP endpoints
// served on the metrics address - the Alertmanager receiver and the read-only
// /api/alerts view - so the bearer-token check has a single source of truth
// instead of a copy per endpoint.
package authz

import (
	"crypto/subtle"
	"strings"
)

// BearerEqual reports whether the Authorization header carries the expected
// bearer token. It strips the canonical "Bearer " scheme prefix and compares
// in constant time so a wrong token cannot be recovered byte-by-byte from
// response timing. The scheme match is case-sensitive (canonical "Bearer ");
// endpoints sit behind a NetworkPolicy and treat that exact form as the
// contract.
func BearerEqual(authHeader, token string) bool {
	got := strings.TrimPrefix(authHeader, "Bearer ")
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
