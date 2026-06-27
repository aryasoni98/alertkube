package metrics

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEventsHandlerUnavailableUntilInstalled(t *testing.T) {
	ClearEventsAuth()
	rec := httptest.NewRecorder()
	eventsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("uninstalled events: got %d, want 503", rec.Code)
	}
}

func TestEventsHandlerTokenGate(t *testing.T) {
	SetEventsAuth(func(r *http.Request) bool {
		return r.Header.Get("Authorization") == "Bearer good"
	})
	t.Cleanup(ClearEventsAuth)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	eventsHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", rec.Code)
	}
}

func TestEventsHandlerStreamsChange(t *testing.T) {
	SetEventsAuth(func(*http.Request) bool { return true })
	t.Cleanup(ClearEventsAuth)

	srv := httptest.NewServer(eventsHandler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	r := bufio.NewReader(res.Body)
	// First line should be the hello event.
	line, err := r.ReadString('\n')
	if err != nil || !strings.Contains(line, "event: hello") {
		t.Fatalf("expected hello event first, got %q (err %v)", line, err)
	}

	// Publish a change and expect a "change" frame to arrive.
	go func() {
		time.Sleep(50 * time.Millisecond)
		PublishChange()
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, err = r.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		if strings.Contains(line, "event: change") {
			return // success
		}
	}
	t.Fatal("did not receive a change event within the deadline")
}

func TestHubSubscribeUnsubscribe(t *testing.T) {
	ch, unsub := hub.subscribe()
	PublishChange()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the change ping")
	}
	unsub()
	// After unsubscribe the channel is no longer in the set; a publish must not
	// panic and the subscriber gets nothing more.
	PublishChange()
	select {
	case <-ch:
		t.Fatal("unsubscribed channel should not receive pings")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPublishChangeCoalesces(t *testing.T) {
	ch, unsub := hub.subscribe()
	t.Cleanup(unsub)
	// Many publishes with no reader collapse into a single pending ping (cap 1).
	for i := 0; i < 100; i++ {
		PublishChange()
	}
	got := 0
	for {
		select {
		case <-ch:
			got++
			continue
		default:
		}
		break
	}
	if got != 1 {
		t.Fatalf("coalesced pings = %d, want 1", got)
	}
}
