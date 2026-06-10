package watchers

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

// pvcPendingThreshold matches the documented behavior: only alert once a
// claim has stayed Pending for at least this long.
const pvcPendingThreshold = 5 * time.Minute

// PVCWatcher fires on Pending (after pvcPendingThreshold) and Lost.
type PVCWatcher struct {
	ns nsFilter
}

func NewPVC(cfg *config.Config) *PVCWatcher {
	return &PVCWatcher{ns: newNSFilter(cfg)}
}

func (*PVCWatcher) Name() string { return "pvc" }

func (p *PVCWatcher) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	inf := f.Core().V1().PersistentVolumeClaims().Informer()
	handler := func(obj interface{}) {
		pvc, ok := obj.(*v1.PersistentVolumeClaim)
		if !ok || !p.ns.allows(pvc.Namespace) {
			return
		}
		p.evaluate(pvc, emit)
	}
	register("pvc", inf, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			defer recoverHandler("pvc.Add")
			handler(obj)
		},
		UpdateFunc: func(_, cur interface{}) {
			defer recoverHandler("pvc.Update")
			handler(cur)
		},
	})
}

func (p *PVCWatcher) evaluate(pvc *v1.PersistentVolumeClaim, emit Emit) {
	switch pvc.Status.Phase {
	case v1.ClaimLost:
		a := alert.New(alert.KindPVC, pvc.Namespace, pvc.Name, "PVCLost", alert.SeverityCritical)
		a.Summary = fmt.Sprintf("PVC %s/%s is Lost", pvc.Namespace, pvc.Name)
		emit(a)
	case v1.ClaimPending:
		if pvc.CreationTimestamp.Time.IsZero() {
			return
		}
		if time.Since(pvc.CreationTimestamp.Time) < pvcPendingThreshold {
			return
		}
		a := alert.New(alert.KindPVC, pvc.Namespace, pvc.Name, "PVCPending", alert.SeverityWarning)
		a.Summary = fmt.Sprintf("PVC %s/%s pending for over %s", pvc.Namespace, pvc.Name, pvcPendingThreshold)
		emit(a)
	}
}
