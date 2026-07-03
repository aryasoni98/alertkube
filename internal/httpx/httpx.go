// Package httpx provides shared HTTP helpers for webhook-style sinks.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultTimeout bounds every webhook POST in time. The 10s budget covers
// dial + TLS + body upload and is well within Slack/Teams rate-limit hints.
// Exported so sinks that must inject their own *http.Client (e.g. slack-go)
// share the same per-request ceiling instead of redefining the constant.
// This bounds one HTTP attempt; the surrounding per-sink and per-route
// budgets are documented at dispatchTimeout in controller.go.
const DefaultTimeout = 10 * time.Second

var defaultClient = &http.Client{Timeout: DefaultTimeout, Transport: guardedTransport()}

// guardedTransport clones the default transport and installs a dialer Control
// hook that re-validates the IP actually being connected to. guardDest checks
// the destination up front, but the HTTP client resolves the hostname again at
// dial time - a DNS-rebind between the two would let a name that first resolved
// to a public IP be reconnected to a blocked one (e.g. the 169.254.169.254
// metadata endpoint). Control runs after resolution and before connect with the
// resolved ip:port, so a blocked address aborts the dial and closes that window.
func guardedTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip != nil {
				return ipBlocked(ip, strictEgress())
			}
			return nil
		},
	}
	t.DialContext = dialer.DialContext
	return t
}

// strictEgress reports whether loopback/private destinations are additionally
// blocked (see strictEgressEnv).
func strictEgress() bool { return strings.EqualFold(os.Getenv(strictEgressEnv), "true") }

// RetryPolicy controls how PostJSON retries transient failures.
type RetryPolicy struct {
	MaxAttempts int           // total attempts including the first call
	BaseDelay   time.Duration // initial backoff, doubled per attempt
	MaxDelay    time.Duration // ceiling on backoff
}

// DefaultRetry is applied when PostJSON is called via the variadic overload
// with no explicit policy. Three attempts with exponential backoff and full
// jitter, capped at one second. The whole retry loop (attempts + backoff
// sleeps) runs inside the caller's context, so the per-sink timeout caps it;
// see the timeout budget at dispatchTimeout in controller.go.
var DefaultRetry = RetryPolicy{MaxAttempts: 3, BaseDelay: 200 * time.Millisecond, MaxDelay: time.Second}

// PostJSON marshals payload, POSTs it to url, and returns an error on
// transport failure or HTTP status >= 400. Empty url is a no-op.
// Transient failures (network errors, 408, 425, 429, 5xx) are retried with
// exponential backoff bounded by DefaultRetry. A `Retry-After` header is
// honored when present.
func PostJSON(ctx context.Context, dest string, payload any) error {
	return PostJSONWithRetry(ctx, dest, payload, DefaultRetry)
}

// PostJSONWithRetry is the explicit-policy form of PostJSON.
func PostJSONWithRetry(ctx context.Context, dest string, payload any, policy RetryPolicy) error {
	return PostJSONWithHeaders(ctx, dest, payload, policy, nil)
}

// HeaderFunc sets per-attempt request headers on the JSON POST. It receives
// the marshaled body so signatures (e.g. HMAC) can be computed over it, and
// runs on every retry so time-sensitive headers (timestamps, signatures) stay
// fresh within the receiver's replay window.
type HeaderFunc func(req *http.Request, body []byte)

// PostJSONWithHeaders is PostJSONWithRetry plus a hook to add per-request
// headers (auth tokens, HMAC signatures). It owns the marshal + retry +
// status-handling loop so sinks that need custom headers do not re-implement
// it. Empty dest is a no-op; header may be nil.
func PostJSONWithHeaders(ctx context.Context, dest string, payload any, policy RetryPolicy, header HeaderFunc) error {
	if dest == "" {
		return nil
	}
	if err := guardDest(ctx, dest); err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return Retry(ctx, policy, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, dest, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if header != nil {
			header(req, body)
		}
		resp, err := defaultClient.Do(req)
		if err != nil {
			return err
		}
		// Drain + close so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 400 {
			return nil
		}
		return NewStatusError(resp.StatusCode, dest, resp.Header.Get("Retry-After"))
	})
}

// Retry runs fn under the policy's exponential backoff until it succeeds,
// returns a non-retriable error, or attempts are exhausted. fn owns request
// construction so bodies are rebuilt per attempt. Sinks whose HTTP calls go
// through third-party clients (Slack, PagerDuty) wrap their sends with this.
func Retry(ctx context.Context, policy RetryPolicy, fn func(ctx context.Context) error) error {
	if policy.MaxAttempts < 1 {
		policy.MaxAttempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepWithCtx(ctx, backoffDelay(policy, attempt, lastErr)); err != nil {
				return err
			}
		}
		err := fn(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		if !Retriable(err) {
			return err
		}
	}
	return fmt.Errorf("after %d attempts: %w", policy.MaxAttempts, lastErr)
}

// NewStatusError builds an HTTP-status failure carrying an optional
// Retry-After hint; Retry honors both when scheduling the next attempt.
// rawURL is sanitized so webhook secrets never reach logs.
func NewStatusError(status int, rawURL, retryAfterHeader string) error {
	return &statusError{status: status, url: sanitizeURL(rawURL), retryAfter: parseRetryAfter(retryAfterHeader)}
}

// Retriable reports whether err is worth retrying: retriable HTTP statuses
// (via statusError or any error exposing HTTPStatusCode, e.g. slack-go's
// StatusCodeError) and transport errors qualify; context cancellation and
// fatal 4xx do not.
func Retriable(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return isRetriableStatus(se.status)
	}
	var sc interface{ HTTPStatusCode() int }
	if errors.As(err, &sc) {
		return isRetriableStatus(sc.HTTPStatusCode())
	}
	return isRetriableErr(err)
}

// statusError surfaces the HTTP status separately from the URL so callers
// can distinguish retriable vs fatal codes, and to keep the URL out of
// log strings via sanitizeURL.
type statusError struct {
	status     int
	url        string
	retryAfter time.Duration
}

func (e *statusError) Error() string {
	return fmt.Sprintf("POST %s returned %d", e.url, e.status)
}

// strictEgressEnv, when set to "true", extends guardDest to also reject
// loopback and private (RFC-1918 / ULA) destinations - for clusters that
// forbid host-local and in-cluster webhook targets entirely.
const strictEgressEnv = "ALERTKUBE_STRICT_WEBHOOK_EGRESS"

// guardDest is a defense-in-depth SSRF check on operator-configured webhook
// destinations (generic webhook, Opsgenie, Telegram). Link-local addresses
// (169.254.0.0/16, fe80::/10) - which include the cloud metadata endpoint
// 169.254.169.254 - are blocked unconditionally: no legitimate notification
// endpoint lives there, and allowing them turns a settable webhook URL into
// an SSRF that can read instance credentials. Loopback and private ranges are
// additionally blocked when strictEgressEnv is set. The destinations come
// from operator-controlled env/Secrets, so this guards against misconfig and
// a compromised config source, not untrusted request input. DNS resolution
// uses ctx so a slow resolver cannot hang the send beyond its sink timeout.
func guardDest(ctx context.Context, dest string) error {
	u, err := url.Parse(dest)
	if err != nil {
		return fmt.Errorf("invalid destination URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("destination scheme %q not allowed (http/https only)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("destination URL has no host")
	}
	strict := strictEgress()
	if ip := net.ParseIP(host); ip != nil {
		return ipBlocked(ip, strict)
	}
	// Hostname: resolve and reject if any resolved address is in a blocked
	// range (catches names like metadata.google.internal). A resolution
	// failure is not fatal - the real dial will surface it with context. The
	// dialer Control hook re-checks the finally-connected IP, so a rebind
	// after this pre-check is still caught.
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil //nolint:nilerr // resolution failure is non-fatal; the real dial surfaces it with context
	}
	for _, a := range addrs {
		if cerr := ipBlocked(a.IP, strict); cerr != nil {
			return cerr
		}
	}
	return nil
}

// ipBlocked reports why an IP is an unsafe webhook destination, or nil when it
// is allowed. Link-local (incl. the cloud metadata endpoint) is always blocked;
// loopback/private/unspecified are additionally blocked under strict egress.
// Shared by the up-front guardDest check and the dial-time Control hook so both
// enforce exactly the same policy.
func ipBlocked(ip net.IP, strict bool) error {
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("destination %s is link-local (blocked: SSRF/cloud-metadata risk)", ip)
	}
	if strict && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()) {
		return fmt.Errorf("destination %s is loopback/private (blocked by %s)", ip, strictEgressEnv)
	}
	return nil
}

// sanitizeURL strips path + query so secrets in webhook URLs (Slack /
// Teams tokens, PagerDuty signatures) never leak through error logs.
func sanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[invalid-url]"
	}
	return u.Scheme + "://" + u.Host + "/[REDACTED]"
}

func isRetriableErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

func isRetriableStatus(code int) bool {
	switch code {
	case 408, 425, 429, 500, 502, 503, 504:
		return true
	}
	return false
}

func backoffDelay(p RetryPolicy, attempt int, lastErr error) time.Duration {
	var se *statusError
	if errors.As(lastErr, &se) && se.retryAfter > 0 {
		return capDuration(se.retryAfter, p.MaxDelay)
	}
	base := p.BaseDelay
	if base <= 0 {
		base = 200 * time.Millisecond
	}
	d := time.Duration(float64(base) * float64(int(1)<<uint(attempt-1)))
	d = capDuration(d, p.MaxDelay)
	jitter := time.Duration(rand.Int64N(int64(d/2 + 1)))
	return d/2 + jitter
}

func capDuration(d, ceiling time.Duration) time.Duration {
	if ceiling <= 0 || d <= ceiling {
		return d
	}
	return ceiling
}

func sleepWithCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
