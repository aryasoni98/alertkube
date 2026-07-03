package persist

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"alertkube/internal/alert"
	"alertkube/internal/metrics"
)

// incompressible returns a base64 string of n random bytes; random data does
// not gzip down, so it exercises the compressed-size guard (unlike repeated
// characters, which now compress to almost nothing).
func incompressible(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

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

func TestSaveLoadRoundTripsPendingOutbox(t *testing.T) {
	p := NewConfigMapStore(fake.NewSimpleClientset(), "ns", "alertkube-state")
	ctx := context.Background()
	a := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	in := &alert.Snapshot{
		Version:  alert.SnapshotVersion,
		SavedAt:  time.Now(),
		LastSent: map[string]time.Time{},
		Pending: []alert.PendingDelivery{
			{ID: 42, Alert: a, Route: []string{"slack", "pagerduty"}},
		},
	}
	if err := p.Save(ctx, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := p.Load(ctx)
	if err != nil || out == nil {
		t.Fatalf("load: out=%v err=%v", out, err)
	}
	if len(out.Pending) != 1 {
		t.Fatalf("pending outbox lost: %+v", out.Pending)
	}
	got := out.Pending[0]
	if got.ID != 42 || got.Alert == nil || got.Alert.Fingerprint != a.Fingerprint || len(got.Route) != 2 {
		t.Fatalf("pending entry not round-tripped: %+v", got)
	}
}

func TestSaveRejectsOversizedSnapshot(t *testing.T) {
	p := NewConfigMapStore(fake.NewSimpleClientset(), "ns", "alertkube-state")
	a := alert.New(alert.KindPod, "ns", "p", "X", alert.SeverityInfo)
	// Incompressible so it exceeds the guard even after gzip.
	a.Summary = incompressible(t, 1500*1024)
	err := p.Save(context.Background(), &alert.Snapshot{Version: alert.SnapshotVersion, Active: []*alert.Alert{a}})
	if err == nil {
		t.Fatalf("oversized snapshot must be rejected, not sent to the apiserver")
	}
}

func TestSaveCompressesHighlyCompressibleSnapshot(t *testing.T) {
	// A snapshot whose raw JSON far exceeds the old raw-byte limit but
	// compresses well must now save successfully (the ceiling is on the
	// compressed size).
	client := fake.NewSimpleClientset()
	p := NewConfigMapStore(client, "ns", "alertkube-state")
	ctx := context.Background()
	a := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	a.Summary = strings.Repeat("x", 5*maxSnapshotBytes) // ~4.5MB raw, tiny gzipped
	if err := p.Save(ctx, &alert.Snapshot{Version: alert.SnapshotVersion, Active: []*alert.Alert{a}}); err != nil {
		t.Fatalf("highly compressible snapshot should save, got %v", err)
	}
	out, err := p.Load(ctx)
	if err != nil || out == nil || len(out.Active) != 1 {
		t.Fatalf("round trip failed: out=%v err=%v", out, err)
	}
	if out.Active[0].Summary != a.Summary {
		t.Fatalf("summary not preserved through compression")
	}
	// Stored as compressed BinaryData, not plaintext Data.
	cm, _ := client.CoreV1().ConfigMaps("ns").Get(ctx, "alertkube-state", metav1.GetOptions{})
	if len(cm.BinaryData[gzKey]) == 0 {
		t.Fatalf("snapshot should be stored gzip-compressed in BinaryData[%q]", gzKey)
	}
	if _, ok := cm.Data[dataKey]; ok {
		t.Fatalf("compressed save must not leave a legacy plaintext blob")
	}
}

func TestLoadLegacyPlaintextMigrates(t *testing.T) {
	// A ConfigMap written by a pre-compression build (plaintext in .data) must
	// still load, and the next Save must rewrite it as compressed BinaryData
	// and drop the legacy key.
	client := fake.NewSimpleClientset()
	ctx := context.Background()
	a := alert.New(alert.KindPod, "ns", "p", "CrashLoopBackOff", alert.SeverityCritical)
	legacy := &alert.Snapshot{Version: alert.SnapshotVersion, Active: []*alert.Alert{a}, LastSent: map[string]time.Time{}}
	body := mustJSON(t, legacy)
	if _, err := client.CoreV1().ConfigMaps("ns").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "alertkube-state", Namespace: "ns"},
		Data:       map[string]string{dataKey: string(body)},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed legacy configmap: %v", err)
	}
	p := NewConfigMapStore(client, "ns", "alertkube-state")
	out, err := p.Load(ctx)
	if err != nil || out == nil || len(out.Active) != 1 {
		t.Fatalf("legacy load failed: out=%v err=%v", out, err)
	}
	if err := p.Save(ctx, out); err != nil {
		t.Fatalf("re-save after migration: %v", err)
	}
	cm, _ := client.CoreV1().ConfigMaps("ns").Get(ctx, "alertkube-state", metav1.GetOptions{})
	if len(cm.BinaryData[gzKey]) == 0 {
		t.Fatalf("migration should write compressed BinaryData")
	}
	if _, ok := cm.Data[dataKey]; ok {
		t.Fatalf("migration should drop the legacy plaintext key")
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
	// Corrupt the compressed blob (the current storage path).
	cm.BinaryData[gzKey] = []byte("not a gzip stream")
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

	// Build an oversize snapshot from incompressible data so it exceeds
	// maxSnapshotBytes (900 KiB) even after gzip.
	snap := &alert.Snapshot{Version: alert.SnapshotVersion, SavedAt: time.Now(),
		LastSent: map[string]time.Time{}}
	a := alert.New(alert.KindPod, "ns", "p", "OversizeState", alert.SeverityWarning)
	a.Summary = incompressible(t, 1500*1024)
	snap.Active = append(snap.Active, a)

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
