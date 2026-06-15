// Package httpx provides shared HTTP helpers for webhook-style sinks.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// defaultClient bounds every webhook POST in time. The 10s budget covers
// dial + TLS + body upload and is well within Slack/Teams rate-limit hints.
var defaultClient = &http.Client{Timeout: 10 * time.Second}

// RetryPolicy controls how PostJSON retries transient failures.
type RetryPolicy struct {
	MaxAttempts int           // total attempts including the first call
	BaseDelay   time.Duration // initial backoff, doubled per attempt
	MaxDelay    time.Duration // ceiling on backoff
}

// DefaultRetry is applied when PostJSON is called via the variadic overload
// with no explicit policy. Three attempts with exponential backoff and full
// jitter, capped at one second.
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
	if dest == "" {
		return nil
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
	jitter := time.Duration(rand.Int63n(int64(d/2 + 1)))
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
