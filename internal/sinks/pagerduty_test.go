package sinks

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
)

// pagerdutyServer stands in for the Events API v2 and records every request
// body verbatim. It answers with status and body.
func pagerdutyServer(t *testing.T, status int, body string) (bodies func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/enqueue" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	old := pagerdutyEventsURL
	pagerdutyEventsURL = srv.URL + "/v2/enqueue"
	t.Cleanup(func() { pagerdutyEventsURL = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// The want bodies are the exact bytes go-pagerduty v1.8.0 posted for these
// alerts before the sink moved to httpx. The wire payload must not change:
// PagerDuty dedupes and resolves by these fields.
func TestPagerDutySendWireBodyUnchanged(t *testing.T) {
	firing := func() *alert.Alert {
		a := alert.New(alert.KindPod, "shop", "api", "CrashLoopBackOff", alert.SeverityCritical)
		a.Fingerprint = "fp-trigger"
		a.Cluster = "prod-eu"
		a.Details = map[string]string{"Pod Status": `<b>Back-off</b> & "restarting"`, "Logs": "line 1\nline 2 é"}
		return a
	}
	cases := []struct {
		name  string
		alert func() *alert.Alert
		want  string
	}{
		{
			name:  "trigger",
			alert: firing,
			want:  `{"routing_key":"routing-key","event_action":"trigger","dedup_key":"fp-trigger","payload":{"summary":"shop/api: CrashLoopBackOff","source":"prod-eu","severity":"critical","component":"Pod","group":"shop","class":"CrashLoopBackOff","custom_details":{"Logs":"line 1\nline 2 é","Pod Status":"\u003cb\u003eBack-off\u003c/b\u003e \u0026 \"restarting\""}}}`,
		},
		{
			name: "resolve keeps the trigger's dedup key",
			alert: func() *alert.Alert {
				a := firing()
				a.Resolved = true
				return a
			},
			want: `{"routing_key":"routing-key","event_action":"resolve","dedup_key":"fp-trigger","payload":{"summary":"shop/api: CrashLoopBackOff","source":"prod-eu","severity":"critical","component":"Pod","group":"shop","class":"CrashLoopBackOff","custom_details":{"Logs":"line 1\nline 2 é","Pod Status":"\u003cb\u003eBack-off\u003c/b\u003e \u0026 \"restarting\""}}}`,
		},
		{
			name: "empty cluster, namespace and details",
			alert: func() *alert.Alert {
				a := alert.New(alert.KindDeployment, "", "web", "Unavailable", alert.SeverityWarning)
				a.Fingerprint = "fp-empty"
				return a
			},
			want: `{"routing_key":"routing-key","event_action":"trigger","dedup_key":"fp-empty","payload":{"summary":"/web: Unavailable","source":"","severity":"warning","component":"Deployment","class":"Unavailable","custom_details":{}}}`,
		},
		{
			name: "nil details",
			alert: func() *alert.Alert {
				a := alert.New(alert.KindNode, "", "node-1", "NotReady", alert.SeverityInfo)
				a.Fingerprint = "fp-nil"
				a.Cluster = "c"
				a.Details = nil
				return a
			},
			want: `{"routing_key":"routing-key","event_action":"trigger","dedup_key":"fp-nil","payload":{"summary":"/node-1: NotReady","source":"c","severity":"info","component":"Node","class":"NotReady","custom_details":null}}`,
		},
	}
	t.Setenv("PAGERDUTY_ROUTING_KEY", "routing-key")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bodies := pagerdutyServer(t, http.StatusAccepted, `{"status":"success","message":"Event processed","dedup_key":"k"}`)
			if err := newPagerDuty().Send(context.Background(), tc.alert()); err != nil {
				t.Fatal(err)
			}
			got := bodies()
			if len(got) != 1 {
				t.Fatalf("got %d requests, want 1", len(got))
			}
			if got[0] != tc.want {
				t.Errorf("wire body changed\n got: %s\nwant: %s", got[0], tc.want)
			}
		})
	}
}

// PagerDuty answers 400 for an event it will never accept. Retrying it only
// burns the per-sink budget, and the error must not quote the routing key.
func TestPagerDutySendDoesNotRetryRejectedEvent(t *testing.T) {
	bodies := pagerdutyServer(t, http.StatusBadRequest, `{"status":"invalid event","message":"Event object is invalid","errors":["Length of 'routing_key' is incorrect (should be 32 characters)"]}`)
	t.Setenv("PAGERDUTY_ROUTING_KEY", "ROUTINGKEYSECRET")
	a := alert.New(alert.KindPod, "shop", "api", "CrashLoopBackOff", alert.SeverityCritical)
	err := newPagerDuty().Send(context.Background(), a)
	if err == nil {
		t.Fatal("a rejected event reported success")
	}
	if n := len(bodies()); n != 1 {
		t.Errorf("rejected event sent %d times, want 1", n)
	}
	if strings.Contains(err.Error(), "ROUTINGKEYSECRET") {
		t.Errorf("error leaks the routing key: %v", err)
	}
}

// Sends go through the guarded webhook client, so the SSRF guard applies.
func TestPagerDutySendRefusesLinkLocal(t *testing.T) {
	old := pagerdutyEventsURL
	pagerdutyEventsURL = "http://169.254.169.254/v2/enqueue"
	t.Cleanup(func() { pagerdutyEventsURL = old })
	t.Setenv("PAGERDUTY_ROUTING_KEY", "routing-key")
	a := alert.New(alert.KindPod, "shop", "api", "CrashLoopBackOff", alert.SeverityCritical)
	err := newPagerDuty().Send(context.Background(), a)
	if err == nil || !strings.Contains(err.Error(), "link-local") {
		t.Fatalf("link-local destination: err = %v, want a link-local refusal", err)
	}
}
