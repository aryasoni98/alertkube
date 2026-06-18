package watchers

import (
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

// NewPVC fires on Pending (after behavior.pvcPendingSeconds) and Lost.
func NewPVC(cfg *config.Config) *simple[*v1.PersistentVolumeClaim] {
	threshold := time.Duration(cfg.Behavior.PVCPendingSeconds) * time.Second
	if threshold <= 0 {
		threshold = 5 * time.Minute
	}
	return newSimple("pvc", alert.KindPVC, cfg,
		func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Core().V1().PersistentVolumeClaims().Informer()
		},
		func(pvc *v1.PersistentVolumeClaim, emit Emit) { evaluatePVC(pvc, threshold, emit) })
}

func evaluatePVC(pvc *v1.PersistentVolumeClaim, pendingThreshold time.Duration, emit Emit) {
	switch pvc.Status.Phase {
	case v1.ClaimLost:
		a := alert.New(alert.KindPVC, pvc.Namespace, pvc.Name, "PVCLost", alert.SeverityCritical)
		a.Summary = fmt.Sprintf("PVC %s/%s is Lost", pvc.Namespace, pvc.Name)
		emit(a)
	case v1.ClaimPending:
		if pvc.CreationTimestamp.Time.IsZero() {
			return
		}
		if time.Since(pvc.CreationTimestamp.Time) < pendingThreshold {
			return
		}
		a := alert.New(alert.KindPVC, pvc.Namespace, pvc.Name, "PVCPending", alert.SeverityWarning)
		a.Summary = fmt.Sprintf("PVC %s/%s pending for over %s", pvc.Namespace, pvc.Name, pendingThreshold)
		emit(a)
	}
}
