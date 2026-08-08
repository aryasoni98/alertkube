// Package persist stores the alert Store's state in a ConfigMap so a
// controller restart does not lose pending resolves (dangling PagerDuty
// incidents) or re-page muted standing conditions.
package persist

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/metrics"
)

// dataKey is the legacy ConfigMap key that held the uncompressed JSON snapshot
// (in .data). Still read on Load so an upgrade from a pre-compression build
// migrates its state; Save no longer writes it and clears it on update.
const dataKey = "snapshot.json"

// gzKey is the ConfigMap BinaryData key holding the gzip-compressed JSON
// snapshot. Compression is the current format: alert state is highly
// repetitive JSON that typically shrinks 5-9x, so the effective state capacity
// under the ConfigMap object limit is several times what raw JSON allowed.
const gzKey = "snapshot.json.gz"

// maxSnapshotBytes guards the ConfigMap ~1MiB object limit, applied to the
// STORED (compressed) size. A snapshot past this is not saved; losing one save
// beats wedging every subsequent update with apiserver rejections.
const maxSnapshotBytes = 900 * 1024

// ConfigMapStore loads and saves alert.Snapshot blobs in a ConfigMap.
type ConfigMapStore struct {
	client    kubernetes.Interface
	namespace string
	name      string
}

func NewConfigMapStore(c kubernetes.Interface, namespace, name string) *ConfigMapStore {
	return &ConfigMapStore{client: c, namespace: namespace, name: name}
}

// Load returns the stored snapshot, or (nil, nil) when none exists yet.
// A corrupt snapshot is reported as an error so the caller can log it;
// the controller then starts cold, which is the pre-persistence behavior.
func (p *ConfigMapStore) Load(ctx context.Context) (*alert.Snapshot, error) {
	cm, err := p.client.CoreV1().ConfigMaps(p.namespace).Get(ctx, p.name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get state configmap %s/%s: %w", p.namespace, p.name, err)
	}
	// Prefer the compressed snapshot (current format).
	if gz, ok := cm.BinaryData[gzKey]; ok && len(gz) > 0 {
		raw, err := gunzip(gz)
		if err != nil {
			return nil, fmt.Errorf("decompress state configmap %s/%s: %w", p.namespace, p.name, err)
		}
		snap := &alert.Snapshot{}
		if err := json.Unmarshal(raw, snap); err != nil {
			return nil, fmt.Errorf("parse state configmap %s/%s: %w", p.namespace, p.name, err)
		}
		return snap, nil
	}
	// Legacy uncompressed snapshot (pre-compression build): read it so state
	// migrates on upgrade. The next Save rewrites it as compressed BinaryData.
	raw, ok := cm.Data[dataKey]
	if !ok || raw == "" {
		return nil, nil
	}
	snap := &alert.Snapshot{}
	if err := json.Unmarshal([]byte(raw), snap); err != nil {
		return nil, fmt.Errorf("parse state configmap %s/%s: %w", p.namespace, p.name, err)
	}
	return snap, nil
}

// gzipBytes compresses b with gzip.
func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// gunzip decompresses a gzip blob.
func gunzip(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// Save writes the snapshot, creating the ConfigMap on first use.
//
// The read-modify-write is retried on conflict: during a leader handoff the
// outgoing and incoming leaders can briefly both write, and a bare
// Get-then-Update would let one silently clobber the other's snapshot
// (last-write-wins). RetryOnConflict re-reads and re-applies on a 409; the
// AlreadyExists case from a lost create race is funnelled into the same retry
// so the second iteration finds the object and Updates it.
func (p *ConfigMapStore) Save(ctx context.Context, snap *alert.Snapshot) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	gz, err := gzipBytes(body)
	if err != nil {
		return fmt.Errorf("compress snapshot: %w", err)
	}
	// The guard and the metric track the STORED (compressed) size, since that
	// is what counts against the ConfigMap object limit.
	metrics.StateSnapshotBytes.Set(float64(len(gz)))
	if len(gz) > maxSnapshotBytes {
		metrics.StateSaveSkipped.Inc()
		return fmt.Errorf("compressed snapshot is %d bytes (limit %d); skipping save", len(gz), maxSnapshotBytes)
	}
	cms := p.client.CoreV1().ConfigMaps(p.namespace)
	retryable := func(err error) bool {
		return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err)
	}
	err = retry.OnError(retry.DefaultRetry, retryable, func() error {
		cm, err := cms.Get(ctx, p.name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = cms.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      p.name,
					Namespace: p.namespace,
					Labels:    map[string]string{"app.kubernetes.io/managed-by": "alertkube"},
				},
				BinaryData: map[string][]byte{gzKey: gz},
			}, metav1.CreateOptions{})
			// AlreadyExists (concurrent first writer won the create race) is
			// retryable: the next iteration Gets the now-existing object and
			// Updates it instead of dropping our snapshot.
			return err
		}
		if err != nil {
			return err
		}
		if cm.BinaryData == nil {
			cm.BinaryData = map[string][]byte{}
		}
		cm.BinaryData[gzKey] = gz
		// Drop any legacy uncompressed blob so it does not linger (stale) or
		// double-count against the object limit after migration.
		delete(cm.Data, dataKey)
		_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("save state configmap %s/%s: %w", p.namespace, p.name, err)
	}
	return nil
}
