// Package persist stores the alert Store's state in a ConfigMap so a
// controller restart does not lose pending resolves (dangling PagerDuty
// incidents) or re-page muted standing conditions.
package persist

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"alertkube/internal/alert"
	"alertkube/internal/metrics"
)

// dataKey is the ConfigMap key holding the JSON snapshot.
const dataKey = "snapshot.json"

// maxSnapshotBytes guards the ConfigMap 1MiB object limit. A snapshot
// past this size is not saved; losing one save beats wedging every
// subsequent update with apiserver rejections.
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
	metrics.StateSnapshotBytes.Set(float64(len(body)))
	if len(body) > maxSnapshotBytes {
		metrics.StateSaveSkipped.Inc()
		return fmt.Errorf("snapshot is %d bytes (limit %d); skipping save", len(body), maxSnapshotBytes)
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
				Data: map[string]string{dataKey: string(body)},
			}, metav1.CreateOptions{})
			// AlreadyExists (concurrent first writer won the create race) is
			// retryable: the next iteration Gets the now-existing object and
			// Updates it instead of dropping our snapshot.
			return err
		}
		if err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[dataKey] = string(body)
		_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("save state configmap %s/%s: %w", p.namespace, p.name, err)
	}
	return nil
}
