package group

import (
	"strings"
	"sync"
	"testing"
	"time"

	"alertkube/internal/alert"
)

func podAlert(name string) *alert.Alert {
	return alert.New(alert.KindPod, "ns", name, "CrashLoopBackOff", alert.SeverityCritical)
}

type sink struct {
	mu  sync.Mutex
	got []*alert.Alert
}

func (s *sink) flush(a *alert.Alert) {
	s.mu.Lock()
	s.got = append(s.got, a)
	s.mu.Unlock()
}

func (s *sink) alerts() []*alert.Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*alert.Alert(nil), s.got...)
}

func TestFirstPassesRestAbsorbed(t *testing.T) {
	s := &sink{}
	g := New(time.Hour, nil, s.flush)

	if !g.Offer(podAlert("p-0")) {
		t.Fatalf("first alert must pass through")
	}
	for i := 1; i < 5; i++ {
		if g.Offer(podAlert("p-" + string(rune('0'+i)))) {
			t.Fatalf("alert %d must be absorbed", i)
		}
	}
	if len(s.alerts()) != 0 {
		t.Fatalf("no summary before window closes")
	}

	g.FlushAll()
	sums := s.alerts()
	if len(sums) != 1 {
		t.Fatalf("want 1 summary, got %d", len(sums))
	}
	sum := sums[0]
	if !strings.Contains(sum.Summary, "4 more") {
		t.Fatalf("summary text: %q", sum.Summary)
	}
	if sum.Labels["alertkube-grouped"] != "true" {
		t.Fatalf("summary must carry the grouped label")
	}
	if sum.Severity != alert.SeverityCritical || sum.Kind != alert.KindPod {
		t.Fatalf("summary must inherit group identity")
	}
}

func TestDifferentGroupsDoNotFold(t *testing.T) {
	s := &sink{}
	g := New(time.Hour, nil, s.flush)
	if !g.Offer(podAlert("p-0")) {
		t.Fatalf("first must pass")
	}
	other := alert.New(alert.KindPod, "ns", "p-1", "OOMKilled", alert.SeverityCritical)
	if !g.Offer(other) {
		t.Fatalf("different reason = different group, must pass")
	}
	g.FlushAll()
	if len(s.alerts()) != 0 {
		t.Fatalf("buckets without absorptions must not produce summaries")
	}
}

func TestResolvedGroupsSeparately(t *testing.T) {
	s := &sink{}
	g := New(time.Hour, nil, s.flush)
	g.Offer(podAlert("p-0"))

	res := podAlert("p-1")
	res.Resolved = true
	if !g.Offer(res) {
		t.Fatalf("first resolve must pass even while trigger window open")
	}
	res2 := podAlert("p-2")
	res2.Resolved = true
	if g.Offer(res2) {
		t.Fatalf("second resolve must fold into the resolve window")
	}

	g.FlushAll()
	sums := s.alerts()
	if len(sums) != 1 {
		t.Fatalf("want 1 resolve summary, got %d", len(sums))
	}
	if !sums[0].Resolved {
		t.Fatalf("resolve summary must be marked Resolved")
	}
}

func TestExpiredWindowFlushesInlineAndReopens(t *testing.T) {
	s := &sink{}
	g := New(10*time.Millisecond, nil, s.flush)
	g.Offer(podAlert("p-0"))
	if g.Offer(podAlert("p-1")) {
		t.Fatalf("second within window must absorb")
	}
	time.Sleep(20 * time.Millisecond)
	// Window expired; next offer flushes the old bucket and passes.
	if !g.Offer(podAlert("p-2")) {
		t.Fatalf("offer after window expiry must pass through")
	}
	sums := s.alerts()
	if len(sums) != 1 || !strings.Contains(sums[0].Summary, "1 more") {
		t.Fatalf("expired bucket must flush inline: %v", sums)
	}
}

func TestMemberListCapped(t *testing.T) {
	s := &sink{}
	g := New(time.Hour, nil, s.flush)
	g.Offer(podAlert("lead"))
	for i := 0; i < 30; i++ {
		g.Offer(podAlert("member"))
	}
	g.FlushAll()
	sum := s.alerts()[0]
	if !strings.Contains(sum.Summary, "+20 more") {
		t.Fatalf("text list must cap at %d: %q", memberListCap, sum.Summary)
	}
	if got := strings.Count(sum.Details["Grouped Resources"], "\n") + 1; got != 30 {
		t.Fatalf("details list = %d lines, want 30", got)
	}
}
