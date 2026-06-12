package watchers

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

// CronJobWatcher fires when a schedule tick passes without a successful
// run. Individual failed runs already alert via the Job watcher
// (JobFailed); this catches the chronic case - a CronJob whose runs keep
// failing or never complete - without parsing cron expressions: each new
// LastScheduleTime is an Update event, and at that moment we can check
// whether the previous tick ever succeeded.
type CronJobWatcher struct {
	ns nsFilter
}

func NewCronJob(cfg *config.Config) *CronJobWatcher {
	return &CronJobWatcher{ns: newNSFilter(cfg)}
}

func (*CronJobWatcher) Name() string { return "cronjob" }

func (c *CronJobWatcher) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	inf := f.Batch().V1().CronJobs().Informer()
	register("cronjob", inf, cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(old, cur interface{}) {
			defer recoverHandler("cronjob.Update")
			oldCJ, _ := old.(*batchv1.CronJob)
			newCJ, ok := cur.(*batchv1.CronJob)
			if !ok || oldCJ == nil || !c.ns.allows(newCJ.Namespace) {
				return
			}
			c.evaluate(oldCJ, newCJ, emit)
		},
	})
}

func (c *CronJobWatcher) evaluate(oldCJ, newCJ *batchv1.CronJob, emit Emit) {
	// Suspend transition is operator-relevant but not an incident.
	if newCJ.Spec.Suspend != nil && *newCJ.Spec.Suspend &&
		(oldCJ.Spec.Suspend == nil || !*oldCJ.Spec.Suspend) {
		a := alert.New(alert.KindCronJob, newCJ.Namespace, newCJ.Name, "CronJobSuspended", alert.SeverityInfo)
		a.Summary = fmt.Sprintf("cronjob %s/%s was suspended", newCJ.Namespace, newCJ.Name)
		emit(a)
	}

	// A new schedule tick arrived. If the PREVIOUS tick never produced a
	// success, a full interval passed without one.
	oldSched, newSched := oldCJ.Status.LastScheduleTime, newCJ.Status.LastScheduleTime
	if newSched == nil || oldSched == nil || newSched.Equal(oldSched) {
		return
	}
	success := newCJ.Status.LastSuccessfulTime
	if success == nil || success.Before(oldSched) {
		a := alert.New(alert.KindCronJob, newCJ.Namespace, newCJ.Name, "CronJobMissingSuccess", alert.SeverityWarning)
		last := "never"
		if success != nil {
			last = success.String()
		}
		a.Summary = fmt.Sprintf("cronjob %s/%s: a full schedule interval passed without a successful run (last success: %s)",
			newCJ.Namespace, newCJ.Name, last)
		a.Details["CronJob Status"] = fmt.Sprintf("Last schedule: %s\nLast success: %s\nActive jobs: %d",
			newSched, last, len(newCJ.Status.Active))
		emit(a)
	}
}
