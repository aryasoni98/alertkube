package templates

import (
	"strings"
	"testing"

	"alertkube/internal/alert"
)

func TestBuildResolvedHeader(t *testing.T) {
	a := alert.New(alert.KindPod, "ns", "pod-1", "CrashLoopBackOff", alert.SeverityCritical)
	a.Resolved = true
	blocks := Build(a)
	if len(blocks) == 0 {
		t.Fatal("no blocks built")
	}
}

func TestBuildBlockCount(t *testing.T) {
	a := alert.New(alert.KindPod, "ns", "pod-1", "OOMKilled", alert.SeverityCritical)
	a.Summary = "container oom"
	for _, k := range orderedDetailKeys() {
		a.Details[k] = strings.Repeat("x", 5000)
	}
	a.Annotations["runbook-url"] = "https://wiki/runbooks/oom"
	blocks := Build(a)
	if len(blocks) > 50 {
		t.Fatalf("%d blocks exceeds Slack's 50-block limit", len(blocks))
	}
}
