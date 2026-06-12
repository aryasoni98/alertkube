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

	"alertkube/internal/alert"
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
func (p *ConfigMapStore) Save(ctx context.Context, snap *alert.Snapshot) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	if len(body) > maxSnapshotBytes {
		return fmt.Errorf("snapshot is %d bytes (limit %d); skipping save", len(body), maxSnapshotBytes)
	}
	cms := p.client.CoreV1().ConfigMaps(p.namespace)
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
		if apierrors.IsAlreadyExists(err) {
			// Lost a create race (e.g. brief dual-leader window); the other
			// writer's state is as good as ours - next sweep retries.
			return nil
		}
		if err != nil {
			return fmt.Errorf("create state configmap %s/%s: %w", p.namespace, p.name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get state configmap %s/%s: %w", p.namespace, p.name, err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[dataKey] = string(body)
	if _, err := cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update state configmap %s/%s: %w", p.namespace, p.name, err)
	}
	return nil
}
