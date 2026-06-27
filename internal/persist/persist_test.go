package persist

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"alertkube/internal/alert"
	"alertkube/internal/metrics"
)

func TestLoadMissingIsNil(t *testing.T) {
	p := NewConfigMapStore(fake.NewSimpleClientset(), "ns", "alertkube-state")
	snap, err := p.Load(context.Background())
	if err != nil || snap != nil {
		t.Fatalf("missing configmap: snap=%v err=%v, want nil,nil", snap, err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := NewConfigMapStore(fake.NewSimpleClientset(), "ns", "alertkube-state")
	ctx := context.Background()

	a := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	in := &alert.Snapshot{
		Version:  alert.SnapshotVersion,
		SavedAt:  time.Now(),
		Active:   []*alert.Alert{a},
		LastSent: map[string]time.Time{a.Fingerprint: time.Now()},
	}
	if err := p.Save(ctx, in); err != nil {
		t.Fatalf("first save (create): %v", err)
	}
	// Second save exercises the update path.
	if err := p.Save(ctx, in); err != nil {
		t.Fatalf("second save (update): %v", err)
	}

	out, err := p.Load(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out == nil || len(out.Active) != 1 || out.Active[0].Fingerprint != a.Fingerprint {
		t.Fatalf("round trip lost data: %+v", out)
	}
	if len(out.LastSent) != 1 {
		t.Fatalf("lastSent lost: %+v", out.LastSent)
	}
}

func TestSaveRejectsOversizedSnapshot(t *testing.T) {
	p := NewConfigMapStore(fake.NewSimpleClientset(), "ns", "alertkube-state")
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)
	a.Summary = strings.Repeat("x", maxSnapshotBytes)
	err := p.Save(context.Background(), &alert.Snapshot{Version: alert.SnapshotVersion, Active: []*alert.Alert{a}})
	if err == nil {
		t.Fatalf("oversized snapshot must be rejected, not sent to the apiserver")
	}
}

func TestLoadCorruptSnapshotErrors(t *testing.T) {
	client := fake.NewSimpleClientset()
	p := NewConfigMapStore(client, "ns", "alertkube-state")
	ctx := context.Background()
	if err := p.Save(ctx, &alert.Snapshot{Version: alert.SnapshotVersion}); err != nil {
		t.Fatalf("save: %v", err)
	}
	cm, _ := client.CoreV1().ConfigMaps("ns").Get(ctx, "alertkube-state", metav1.GetOptions{})
	cm.Data[dataKey] = "{not json"
	if _, err := client.CoreV1().ConfigMaps("ns").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("seed corrupt data: %v", err)
	}
	if _, err := p.Load(ctx); err == nil {
		t.Fatalf("corrupt snapshot must surface an error")
	}
}

func TestSaveSkipsOversizeSnapshotAndRecordsMetric(t *testing.T) {
	p := NewConfigMapStore(fake.NewSimpleClientset(), "ns", "alertkube-state")
	ctx := context.Background()

	before := testutil.ToFloat64(metrics.StateSaveSkipped)

	// Build an oversize snapshot: many active alerts with large summaries so the
	// serialized JSON exceeds maxSnapshotBytes (900 KiB).
	snap := &alert.Snapshot{Version: alert.SnapshotVersion, SavedAt: time.Now(),
		LastSent: map[string]time.Time{}}
	big := strings.Repeat("x", 2048)
	for i := 0; i < 600; i++ {
		a := alert.New(alert.KindPod, "ns", "p", big+string(rune(i)), alert.SeverityWarning)
		a.Summary = big
		snap.Active = append(snap.Active, a)
	}

	err := p.Save(ctx, snap)
	if err == nil || !strings.Contains(err.Error(), "skipping save") {
		t.Fatalf("oversize snapshot should be skipped, got err=%v", err)
	}
	after := testutil.ToFloat64(metrics.StateSaveSkipped)
	if after != before+1 {
		t.Fatalf("StateSaveSkipped not incremented: before=%v after=%v", before, after)
	}
	if b := testutil.ToFloat64(metrics.StateSnapshotBytes); b <= 0 {
		t.Fatalf("StateSnapshotBytes should be set, got %v", b)
	}
}
