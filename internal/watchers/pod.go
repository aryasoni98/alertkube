package watchers

import (
	"context"
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/collectors"
	"alertkube/internal/config"
	"alertkube/internal/filter"
	"alertkube/internal/metrics"
)

// enrichWorkers bounds concurrent enrichment API calls (events, logs).
// Enrichment runs off the informer handler so a slow API server cannot
// stall event processing; past this bound alerts go out skinny instead
// of queueing.
const enrichWorkers = 4

// PodWatcher reacts to restart, crashloop, oom, imagepull, pending events.
type PodWatcher struct {
	clientset     kubernetes.Interface
	cfg           *config.Config
	watchedNS     *filter.Set
	ignoredNS     *filter.Set
	watchedPrefix *filter.Set
	ignoredPrefix *filter.Set
	enrichSem     chan struct{}
	enrichWG      sync.WaitGroup
}

func NewPod(c kubernetes.Interface, cfg *config.Config) *PodWatcher {
	return &PodWatcher{
		clientset:     c,
		cfg:           cfg,
		watchedNS:     filter.New(cfg.Filters.WatchedNamespaces),
		ignoredNS:     filter.New(cfg.Filters.IgnoredNamespaces),
		watchedPrefix: filter.New(cfg.Filters.WatchedPodNamePrefixes),
		ignoredPrefix: filter.New(cfg.Filters.IgnoredPodNamePrefixes),
		enrichSem:     make(chan struct{}, enrichWorkers),
	}
}

func (p *PodWatcher) Name() string { return "pod" }

// Drain blocks until in-flight enrichment goroutines finish or ctx expires.
// Enrichment (events/logs collection) runs off the informer handler in a
// bounded pool; without draining, a shutdown abandons those goroutines
// mid-flight and the alerts they were enriching are never emitted.
func (p *PodWatcher) Drain(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		p.enrichWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		klog.Warning("pod enrichment drain timed out; abandoning in-flight enrichment")
	}
}

func (p *PodWatcher) Setup(ctx context.Context, f informers.SharedInformerFactory, emit Emit) {
	// On Add (initial sync) oldPod is nil, so evaluate only emits on
	// terminal/waiting conditions - no restart delta exists yet. On Delete
	// the pod's crashloop/oom/imagepull alert resolves now instead of at
	// resolveTTL; pod names are unique, so a rollout's replacement gets a
	// fresh fingerprint and is unaffected.
	register("pod", f.Core().V1().Pods().Informer(),
		handleDiff("pod", alert.KindPod, emit, p.shouldHandle, true, func(old, cur *v1.Pod) {
			p.evaluate(ctx, old, cur, emit)
		}))
}

// shouldHandle returns true when a pod passes namespace + name include/exclude filters.
func (p *PodWatcher) shouldHandle(pod *v1.Pod) bool {
	if !p.watchedNS.Matches(pod.Namespace) || p.ignoredNS.Blocks(pod.Namespace) {
		return false
	}
	if !p.watchedPrefix.Matches(pod.Name) || p.ignoredPrefix.Blocks(pod.Name) {
		return false
	}
	return true
}

func (p *PodWatcher) evaluate(ctx context.Context, oldPod, newPod *v1.Pod, emit Emit) {
	newCount := totalRestarts(newPod)
	oldCount := totalRestarts(oldPod)

	// Detect crashloop/oom/imagepull waiting reasons regardless of restart count.
	for _, st := range newPod.Status.ContainerStatuses {
		if st.State.Waiting != nil {
			reason := st.State.Waiting.Reason
			switch reason {
			case "CrashLoopBackOff":
				p.emitContainerAlert(ctx, newPod, st, reason, alert.SeverityCritical, emit)
				return
			case "ImagePullBackOff", "ErrImagePull":
				p.emitContainerAlert(ctx, newPod, st, reason, alert.SeverityWarning, emit)
				return
			}
		}
		if term := st.LastTerminationState.Terminated; term != nil {
			if term.Reason == "OOMKilled" {
				p.emitContainerAlert(ctx, newPod, st, "OOMKilled", alert.SeverityCritical, emit)
				return
			}
			// Non-OOM SIGKILL (exit 137 / signal 9) on a pod that is NOT being
			// deleted: the container was force-killed while it was meant to be
			// running - liveness-probe escalation, terminationGracePeriod
			// exceeded mid-run, or a runtime kill. A SIGKILL during normal pod
			// teardown (rollout, scale-down, eviction) sets DeletionTimestamp,
			// so guarding on it keeps graceful shutdowns silent.
			if (term.ExitCode == 137 || term.Signal == 9) && newPod.DeletionTimestamp == nil {
				p.emitContainerAlert(ctx, newPod, st, "ContainerKilled", alert.SeverityWarning, emit)
				return
			}
		}
	}

	// Per-restart alerts stop once a pod is chronically restarting
	// (ignoreRestartCount); CrashLoopBackOff detection above still covers it.
	if newCount > oldCount && newCount <= p.cfg.Behavior.IgnoreRestartCount {
		for _, st := range newPod.Status.ContainerStatuses {
			if st.RestartCount == 0 {
				continue
			}
			if p.cfg.Behavior.IgnoreRestartsWithExitCodeZero &&
				st.LastTerminationState.Terminated != nil &&
				st.LastTerminationState.Terminated.ExitCode == 0 {
				continue
			}
			p.emitContainerAlert(ctx, newPod, st, "ContainerRestart", alert.SeverityWarning, emit)
			klog.Infof("pod %s/%s restartCount %d->%d", newPod.Namespace, newPod.Name, oldCount, newCount)
			return
		}
	}
}

func (p *PodWatcher) emitContainerAlert(ctx context.Context, pod *v1.Pod, st v1.ContainerStatus, reason string, sev alert.Severity, emit Emit) {
	a := alert.New(alert.KindPod, pod.Namespace, pod.Name, reason, sev)
	a.NodeName = pod.Spec.NodeName
	a.Labels["container"] = st.Name
	a.Summary = fmt.Sprintf("container %q in pod %s/%s entered %s", st.Name, pod.Namespace, pod.Name, reason)
	if cause := terminationCause(st); cause != "" {
		a.Summary += " — last termination: " + cause
	}
	a.Annotations = mergeAnnotations(pod)

	// Local enrichment (no API calls) stays on the handler path.
	if podInfo, err := collectors.PrintPod(pod); err == nil {
		a.Details["Pod Status"] = podInfo
	}
	if cstate, err := collectors.DescribeContainerState(st); err == nil {
		a.Details["Container State"] = cstate
	}
	for _, c := range pod.Spec.Containers {
		if c.Name == st.Name {
			if r, err := collectors.GetContainerResource(c); err == nil {
				a.Details["Resource Spec"] = r
			}
		}
	}

	// API-backed enrichment (events, previous logs) moves to a bounded
	// pool so a slow apiserver cannot stall the informer handler. When
	// the pool is saturated (alert storm) the alert ships skinny - a
	// timely page without logs beats a late one with them.
	select {
	case p.enrichSem <- struct{}{}:
		p.enrichWG.Add(1)
		go func() {
			defer p.enrichWG.Done()
			defer func() { <-p.enrichSem }()
			func() {
				defer recoverHandler("pod.enrich")
				p.enrich(ctx, pod, st, reason, a)
			}()
			emit(a)
		}()
	default:
		metrics.EnrichmentSaturated.Inc()
		klog.Warningf("enrichment pool saturated; emitting %s without events/logs", a)
		emit(a)
	}
}

// enrich fills the Details that require apiserver round-trips.
func (p *PodWatcher) enrich(ctx context.Context, pod *v1.Pod, st v1.ContainerStatus, reason string, a *alert.Alert) {
	if events, err := collectors.PodEvents(ctx, p.clientset, pod.Namespace, pod.Name); err == nil && events != "" {
		a.Details["Pod Events"] = events
	}
	if !p.cfg.Behavior.DisableLogCollection && reason != "ImagePullBackOff" && reason != "ErrImagePull" {
		if logs, err := collectors.PreviousContainerLogs(ctx, p.clientset, pod, st.Name); err == nil && logs != "" {
			a.Details["Pod Logs Before Restart"] = logs
		}
	}
	if pod.Spec.NodeName != "" {
		if nodeEvents, err := collectors.NodeEvents(ctx, p.clientset, pod.Spec.NodeName); err == nil && nodeEvents != "" {
			a.Details["Node Events"] = nodeEvents
		}
	}
}

// terminationCause renders a container's last-termination signal/exit code
// in human form ("SIGKILL (exit 137)", "SIGTERM (exit 143)", "exit 1") for
// the alert summary, so operators see WHY a container died without opening
// the Container State block. Returns "" when there is no terminated state.
func terminationCause(st v1.ContainerStatus) string {
	t := st.LastTerminationState.Terminated
	if t == nil {
		return ""
	}
	if name := signalName(t.Signal); name != "" {
		return fmt.Sprintf("%s (exit %d)", name, t.ExitCode)
	}
	switch t.ExitCode {
	case 137:
		return "SIGKILL (exit 137)"
	case 143:
		return "SIGTERM (exit 143)"
	}
	if t.Reason != "" {
		return fmt.Sprintf("%s (exit %d)", t.Reason, t.ExitCode)
	}
	return fmt.Sprintf("exit %d", t.ExitCode)
}

// signalName maps the common termination signals to names; "" for the rest
// (the exit code still conveys those).
func signalName(sig int32) string {
	switch sig {
	case 2:
		return "SIGINT"
	case 6:
		return "SIGABRT"
	case 9:
		return "SIGKILL"
	case 11:
		return "SIGSEGV"
	case 15:
		return "SIGTERM"
	}
	return ""
}

func totalRestarts(pod *v1.Pod) int {
	if pod == nil {
		return 0
	}
	r := 0
	for _, s := range pod.Status.ContainerStatuses {
		r += int(s.RestartCount)
	}
	return r
}

// controlAnnotationKeys are annotation keys that change alertkube behavior
// (silencing, channel routing, rendered links). Labels must never populate
// these: labels are typically writable by lower-privilege automation than
// annotations, and back-filling them would let a label-writer silence their
// own alerts or inject runbook links.
var controlAnnotationKeys = map[string]struct{}{
	alert.AnnotationSilenceUntil: {},
	alert.AnnotationSlackChannel: {},
	alert.AnnotationRunbookURL:   {},
}

func mergeAnnotations(pod *v1.Pod) map[string]string {
	annotations, labels := pod.GetAnnotations(), pod.GetLabels()
	out := make(map[string]string, len(annotations)+len(labels))
	for k, v := range annotations {
		out[k] = v
	}
	for k, v := range labels {
		if _, control := controlAnnotationKeys[k]; control {
			continue
		}
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out
}
