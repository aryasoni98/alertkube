package watchers

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"alertkube/internal/alert"
)

// PVCWatcher fires on Pending (after 5m) and Lost.
type PVCWatcher struct{}

func NewPVC() *PVCWatcher { return &PVCWatcher{} }

func (*PVCWatcher) Name() string { return "pvc" }

func (*PVCWatcher) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	inf := f.Core().V1().PersistentVolumeClaims().Informer()
	inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, new interface{}) {
			pvc, ok := new.(*v1.PersistentVolumeClaim)
			if !ok {
				return
			}
			switch pvc.Status.Phase {
			case v1.ClaimLost:
				a := alert.New(alert.KindPVC, pvc.Namespace, pvc.Name, "PVCLost", alert.SeverityCritical)
				a.Summary = fmt.Sprintf("PVC %s/%s is Lost", pvc.Namespace, pvc.Name)
				emit(a)
			case v1.ClaimPending:
				// Only fire if pending >5m
				if pvc.CreationTimestamp.Time.IsZero() {
					return
				}
				a := alert.New(alert.KindPVC, pvc.Namespace, pvc.Name, "PVCPending", alert.SeverityWarning)
				a.Summary = fmt.Sprintf("PVC %s/%s pending", pvc.Namespace, pvc.Name)
				emit(a)
			}
		},
	})
}
