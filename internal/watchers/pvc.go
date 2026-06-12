package watchers

import (
	"context"
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

// PVCWatcher fires on Pending (after behavior.pvcPendingSeconds) and Lost.
type PVCWatcher struct {
	ns               nsFilter
	pendingThreshold time.Duration
}

func NewPVC(cfg *config.Config) *PVCWatcher {
	threshold := time.Duration(cfg.Behavior.PVCPendingSeconds) * time.Second
	if threshold <= 0 {
		threshold = 5 * time.Minute
	}
	return &PVCWatcher{
		ns:               newNSFilter(cfg),
		pendingThreshold: threshold,
	}
}

func (*PVCWatcher) Name() string { return "pvc" }

func (p *PVCWatcher) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	register("pvc", f.Core().V1().PersistentVolumeClaims().Informer(),
		handleCurrent("pvc", p.ns, func(pvc *v1.PersistentVolumeClaim) { p.evaluate(pvc, emit) }))
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
		if time.Since(pvc.CreationTimestamp.Time) < p.pendingThreshold {
			return
		}
		a := alert.New(alert.KindPVC, pvc.Namespace, pvc.Name, "PVCPending", alert.SeverityWarning)
		a.Summary = fmt.Sprintf("PVC %s/%s pending for over %s", pvc.Namespace, pvc.Name, p.pendingThreshold)
		emit(a)
	}
}
