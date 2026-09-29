package watchers

import (
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
	"github.com/aryasoni98/alertkube/v2/internal/config"
)

// newPVC fires on Lost, and on Pending once a claim is older than threshold
// (behavior.pvcPendingSeconds); a non-positive threshold falls back to 5m.
func newPVC(filters config.Filters, threshold time.Duration) *simple[*v1.PersistentVolumeClaim] {
	if threshold <= 0 {
		threshold = 5 * time.Minute
	}
	return newSimple("pvc", alert.KindPVC, filters,
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

func init() {
	Register(func(o Opts) Watcher {
		return newPVC(o.Config.Filters, time.Duration(o.Config.Behavior.PVCPendingSeconds)*time.Second)
	})
}
