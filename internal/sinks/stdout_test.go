package sinks

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/internal/alert"
)

type klogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *klogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *klogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureKlog sends klog output to a buffer until the test ends.
func captureKlog(t *testing.T) *klogBuffer {
	t.Helper()
	b := &klogBuffer{}
	klog.LogToStderr(false)
	klog.SetOutput(b)
	t.Cleanup(func() {
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	})
	return b
}

// The e2e resolve test greps the stdout line for resolved=true. Alert.String
// already carries the marker, so the sink must not append a second one.
func TestStdoutLogsResolvedMarkerOnce(t *testing.T) {
	cases := []struct {
		name     string
		resolved bool
		want     string
	}{
		{name: "firing", want: "resolved=false"},
		{name: "resolved", resolved: true, want: "resolved=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureKlog(t)
			a := alert.New(alert.KindPod, "ns", "stdout-"+tc.name, "X", alert.SeverityWarning)
			a.Resolved = tc.resolved
			a.Summary = "s"
			if err := newStdout().Send(context.Background(), a); err != nil {
				t.Fatalf("send: %v", err)
			}
			klog.Flush()
			var line string
			for l := range strings.SplitSeq(logs.String(), "\n") {
				if strings.Contains(l, a.Fingerprint) {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("no ALERT line for %s in %q", a.Fingerprint, logs.String())
			}
			if n := strings.Count(line, "resolved="); n != 1 || !strings.Contains(line, tc.want) {
				t.Fatalf("line has %d resolved= markers, want one %s: %q", n, tc.want, line)
			}
		})
	}
}
