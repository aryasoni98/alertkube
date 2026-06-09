package watchers

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"alertkube/internal/alert"
)

// JobWatcher fires on Failed jobs (backoffLimit hit).
type JobWatcher struct{}

func NewJob() *JobWatcher { return &JobWatcher{} }

func (*JobWatcher) Name() string { return "job" }

func (j *JobWatcher) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	inf := f.Batch().V1().Jobs().Informer()
	handler := func(obj interface{}) {
		job, ok := obj.(*batchv1.Job)
		if !ok {
			return
		}
		j.evaluate(job, emit)
	}
	register("job", inf, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			defer recoverHandler("job.Add")
			handler(obj)
		},
		UpdateFunc: func(_, cur interface{}) {
			defer recoverHandler("job.Update")
			handler(cur)
		},
	})
}

func (j *JobWatcher) evaluate(job *batchv1.Job, emit Emit) {
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
