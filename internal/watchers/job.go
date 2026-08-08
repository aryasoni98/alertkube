package watchers

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
)

// NewJob fires on Failed jobs (backoffLimit hit).
func NewJob(cfg *config.Config) *simple[*batchv1.Job] {
	return newSimple("job", alert.KindJob, cfg,
		func(f informers.SharedInformerFactory) cache.SharedIndexInformer {
			return f.Batch().V1().Jobs().Informer()
		},
		evaluateJob)
}

func evaluateJob(job *batchv1.Job, emit Emit) {
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == v1.ConditionTrue {
			a := alert.New(alert.KindJob, job.Namespace, job.Name, "JobFailed", alert.SeverityCritical)
			a.Summary = fmt.Sprintf("job %s/%s failed: %s", job.Namespace, job.Name, cond.Message)
			a.Details["Job Status"] = fmt.Sprintf("Active: %d\nSucceeded: %d\nFailed: %d", job.Status.Active, job.Status.Succeeded, job.Status.Failed)
			emit(a)
			return
		}
	}
}

func init() { Register(func(o Opts) Watcher { return NewJob(o.Config) }) }
