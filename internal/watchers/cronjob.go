package watchers

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
)

// cronJobWatcher fires when a schedule tick passes without a successful
// run. Individual failed runs already alert via the Job watcher
// (JobFailed); this catches the chronic case - a CronJob whose runs keep
// failing or never complete - without parsing cron expressions: each new
// LastScheduleTime is an Update event, and at that moment we can check
// whether the previous tick ever succeeded.
type cronJobWatcher struct {
	ns nsFilter
}

func newCronJob(cfg *config.Config) *cronJobWatcher {
	return &cronJobWatcher{ns: newNSFilter(cfg.Filters)}
}

func (*cronJobWatcher) Name() string { return "cronjob" }

func (c *cronJobWatcher) Setup(_ context.Context, f informers.SharedInformerFactory, emit Emit) {
	// includeAdd is false: a missed-schedule diff needs a prior tick, which
	// only exists on Update. Delete resolves CronJobSuspended /
	// CronJobMissingSuccess alerts instead of waiting out resolveTTL.
	addHandler("cronjob", f.Batch().V1().CronJobs().Informer(),
		handleDiff("cronjob", alert.KindCronJob, emit,
			func(cj *batchv1.CronJob) bool { return c.ns.allows(cj.Namespace) },
			false,
			func(old, cur *batchv1.CronJob) { c.evaluate(old, cur, emit) }))
}

func (c *cronJobWatcher) evaluate(oldCJ, newCJ *batchv1.CronJob, emit Emit) {
	// Update events always carry a prior object; guard defensively so a
	// missing old never nil-derefs the diff below.
	if oldCJ == nil {
		return
	}
	suspended := newCJ.Spec.Suspend != nil && *newCJ.Spec.Suspend
	// Suspend transition is operator-relevant but not an incident. A resync
	// (old == cur) is never a transition, so it does not re-assert: Add is not
	// evaluated either, and a job suspended before startup must not alert on
	// the first resync. The info alert resolves after resolveTTL.
	if suspended && (oldCJ.Spec.Suspend == nil || !*oldCJ.Spec.Suspend) {
		a := alert.New(alert.KindCronJob, newCJ.Namespace, newCJ.Name, "CronJobSuspended", alert.SeverityInfo)
		a.Summary = fmt.Sprintf("cronjob %s/%s was suspended", newCJ.Namespace, newCJ.Name)
		emit(a)
	}
	if isResync(oldCJ, newCJ) {
		// Re-assert MissingSuccess so a still-failing job does not
		// false-resolve. With no active run, a resync re-asserts once the
		// latest run ended without success, before the next tick the
		// transition rule below waits for. An in-flight run has no outcome
		// yet, so it is skipped; a transition alert can therefore lapse after
		// resolveTTL while a long run is active. A suspended job schedules
		// no ticks, so it is left alone.
		if !suspended && len(newCJ.Status.Active) == 0 {
			c.emitMissingSuccess(newCJ, newCJ.Status.LastScheduleTime, emit)
		}
		return
	}

	// A new schedule tick arrived. If the PREVIOUS tick never produced a
	// success, a full interval passed without one.
	oldSched, newSched := oldCJ.Status.LastScheduleTime, newCJ.Status.LastScheduleTime
	if newSched == nil || oldSched == nil || newSched.Equal(oldSched) {
		return
	}
	if success := newCJ.Status.LastSuccessfulTime; success == nil || success.Before(oldSched) {
		c.emitMissingSuccess(newCJ, newSched, emit)
	}
}

func (c *cronJobWatcher) emitMissingSuccess(cj *batchv1.CronJob, sched *metav1.Time, emit Emit) {
	if sched == nil {
		return
	}
	success := cj.Status.LastSuccessfulTime
	if success != nil && !success.Before(sched) {
		return
	}
	a := alert.New(alert.KindCronJob, cj.Namespace, cj.Name, "CronJobMissingSuccess", alert.SeverityWarning)
	last := "never"
	if success != nil {
		last = success.String()
	}
	a.Summary = fmt.Sprintf("cronjob %s/%s: a full schedule interval passed without a successful run (last success: %s)",
		cj.Namespace, cj.Name, last)
	a.Details["CronJob Status"] = fmt.Sprintf("Last schedule: %s\nLast success: %s\nActive jobs: %d",
		sched, last, len(cj.Status.Active))
	emit(a)
}

func init() { Register(func(o Opts) Watcher { return newCronJob(o.Config) }) }
