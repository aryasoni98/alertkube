package sinks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aryasoni98/alertkube/internal/alert"
)

func TestWebhookEmptyURLNoop(t *testing.T) {
	t.Setenv("GENERIC_WEBHOOK_URL", "")
	w := NewWebhook()
	if err := w.Send(context.Background(), alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)); err != nil {
		t.Fatalf("empty URL must be no-op, got %v", err)
	}
}

func TestWebhookPostsJSONWithoutSignatureWhenSecretMissing(t *testing.T) {
	var gotSig, gotTs, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Alertkube-Signature")
		gotTs = r.Header.Get("X-Alertkube-Timestamp")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("GENERIC_WEBHOOK_URL", srv.URL)
	t.Setenv("GENERIC_WEBHOOK_SECRET", "")
	w := NewWebhook()
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)
	a.Summary = "test"
	if err := w.Send(context.Background(), a); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotSig != "" {
		t.Fatalf("must not sign without secret, got %q", gotSig)
	}
	if gotTs == "" {
		t.Fatalf("timestamp header should still be set")
	}
	if !strings.Contains(gotBody, `"Reason":"X"`) {
		t.Fatalf("body missing alert payload: %s", gotBody)
	}
}

func TestWebhookHMACSignatureVerifies(t *testing.T) {
	secret := "supersecret-rotate-me"
	var verified bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts := r.Header.Get("X-Alertkube-Timestamp")
		sig := r.Header.Get("X-Alertkube-Signature")
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(ts))
		mac.Write([]byte{'.'})
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if hmac.Equal([]byte(sig), []byte(want)) {
			verified = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	t.Setenv("GENERIC_WEBHOOK_URL", srv.URL)
	t.Setenv("GENERIC_WEBHOOK_SECRET", secret)
	w := NewWebhook()
	if err := w.Send(context.Background(), alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !verified {
		t.Fatalf("receiver did not verify signature")
	}
}

func TestWebhookRotationPickedUpPerSend(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("GENERIC_WEBHOOK_URL", "")
	w := NewWebhook()
	if err := w.Send(context.Background(), alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)); err != nil {
		t.Fatalf("first send (empty URL): %v", err)
	}
	if hits != 0 {
		t.Fatalf("expected no hit while URL empty, got %d", hits)
	}
	t.Setenv("GENERIC_WEBHOOK_URL", srv.URL)
	if err := w.Send(context.Background(), alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)); err != nil {
		t.Fatalf("second send: %v", err)
	}
	if hits != 1 {
		t.Fatalf("rotation not picked up, hits=%d", hits)
	}
}
