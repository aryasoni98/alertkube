package watchers

import (
	"context"
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/collectors"
	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/filter"
	"github.com/aryasoni98/alertkube/internal/metrics"
)

// enrichWorkers bounds concurrent enrichment API calls (events, logs).
// Enrichment runs off the informer handler so a slow API server cannot
// stall event processing; past this bound alerts go out skinny instead
// of queueing.
const enrichWorkers = 4

// podWatcher reacts to container restart, crashloop, OOM, image-pull, and
// unexpected-kill transitions.
type podWatcher struct {
	clientset     kubernetes.Interface
	behavior      config.Behavior
	ns            nsFilter
	watchedPrefix *filter.Set
	ignoredPrefix *filter.Set
	enrichSem     chan struct{}
	enrichWG      sync.WaitGroup
}

func newPod(c kubernetes.Interface, cfg *config.Config) *podWatcher {
	return &podWatcher{
		clientset:     c,
		behavior:      cfg.Behavior,
		ns:            newNSFilter(cfg.Filters),
		watchedPrefix: filter.New(cfg.Filters.WatchedPodNamePrefixes),
		ignoredPrefix: filter.New(cfg.Filters.IgnoredPodNamePrefixes),
		enrichSem:     make(chan struct{}, enrichWorkers),
	}
}

func (p *podWatcher) Name() string { return "pod" }

// Drain blocks until in-flight enrichment goroutines finish or ctx expires.
// Enrichment (events/logs collection) runs off the informer handler in a
// bounded pool; without draining, a shutdown abandons those goroutines
// mid-flight and the alerts they were enriching are never emitted.
func (p *podWatcher) Drain(ctx context.Context) {
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

func (p *podWatcher) Setup(ctx context.Context, f informers.SharedInformerFactory, emit Emit) {
	// On Add (initial sync) oldPod is nil, so evaluate only emits on
	// terminal/waiting conditions - no restart delta exists yet. On Delete
	// the pod's crashloop/oom/imagepull alert resolves now instead of at
	// resolveTTL; pod names are unique, so a rollout's replacement gets a
	// fresh fingerprint and is unaffected.
	addHandler("pod", f.Core().V1().Pods().Informer(),
		handleDiff("pod", alert.KindPod, emit, p.shouldHandle, true, func(old, cur *v1.Pod) {
			p.evaluate(ctx, old, cur, emit)
		}))
}

// shouldHandle returns true when a pod passes namespace + name include/exclude filters.
func (p *podWatcher) shouldHandle(pod *v1.Pod) bool {
	if !p.ns.allows(pod.Namespace) {
		return false
	}
	if !p.watchedPrefix.Matches(pod.Name) || p.ignoredPrefix.Blocks(pod.Name) {
		return false
	}
	return true
}

func (p *podWatcher) evaluate(ctx context.Context, oldPod, newPod *v1.Pod, emit Emit) {
	// Walk every container and keep the highest-severity finding. Returning on
	// the first match let a sidecar's stale record hide a live crashloop.
	var best *v1.ContainerStatus
	var bestTerm *v1.ContainerStateTerminated
	var bestReason string
	var bestSev alert.Severity
	consider := func(st v1.ContainerStatus, term *v1.ContainerStateTerminated, reason string, sev alert.Severity) {
		if best != nil && severityRank(sev) <= severityRank(bestSev) {
			return
		}
		cp := st
		best, bestTerm, bestReason, bestSev = &cp, term, reason, sev
	}
	for _, st := range newPod.Status.ContainerStatuses {
		// A container that just died holds that exit in State.Terminated.
		// Under restartPolicy Never it stays there until the pod is deleted,
		// so its alert re-asserts on every resync. LastTerminationState
		// survives the next successful run, so it counts only while the
		// container is not running.
		term := st.State.Terminated
		if term == nil && st.State.Running == nil {
			term = st.LastTerminationState.Terminated
		}
		if st.State.Waiting != nil {
			switch st.State.Waiting.Reason {
			case "CrashLoopBackOff":
				consider(st, term, "CrashLoopBackOff", alert.SeverityCritical)
			case "ImagePullBackOff", "ErrImagePull":
				consider(st, term, st.State.Waiting.Reason, alert.SeverityWarning)
			}
		}
		reason, sev := killReason(newPod, term)
		if reason == "ContainerKilled" && st.State.Terminated != nil && kubeletTeardown(newPod, term) {
			reason = ""
		}
		if reason != "" {
			consider(st, term, reason, sev)
		}
	}

	// An add has no previous pod. Historical restarts are not a delta.
	if oldPod != nil {
		oldRestarts := map[string]int32{}
		for _, st := range oldPod.Status.ContainerStatuses {
			oldRestarts[st.Name] = st.RestartCount
		}
		for _, st := range newPod.Status.ContainerStatuses {
			prev := oldRestarts[st.Name]
			if st.RestartCount <= prev {
				continue
			}
			// The container is usually running again by the time the restart
			// is observed, so the kill that caused it is in
			// LastTerminationState. ignoreRestartCount and the exit-zero
			// filter apply only to plain restarts.
			last := st.LastTerminationState.Terminated
			reason, sev := killReason(newPod, last)
			if reason == "" {
				if int(st.RestartCount) > p.behavior.IgnoreRestartCount ||
					(p.behavior.IgnoreRestartsWithExitCodeZero && last != nil && last.ExitCode == 0) {
					continue
				}
				reason, sev = "ContainerRestart", alert.SeverityWarning
			}
			consider(st, last, reason, sev)
			klog.V(2).Infof("pod %s/%s container %s restartCount %d->%d", newPod.Namespace, newPod.Name, st.Name, prev, st.RestartCount)
		}
	}
	if best != nil {
		p.emitContainerAlert(ctx, newPod, *best, bestTerm, bestReason, bestSev, emit)
	}
}

// killReason classifies a termination as an OOM kill (critical) or an
// unexpected SIGKILL (warning), and returns "" for any other exit. A SIGKILL
// during API-initiated teardown (rollout, scale-down, drain eviction) sets
// DeletionTimestamp, so graceful shutdowns stay silent; one without it means
// a liveness-probe escalation, terminationGracePeriod exceeded mid-run, or a
// runtime kill. Kubelet-initiated teardown does not set DeletionTimestamp;
// kubeletTeardown covers it.
func killReason(pod *v1.Pod, t *v1.ContainerStateTerminated) (string, alert.Severity) {
	switch {
	case t == nil:
		return "", ""
	case t.Reason == "OOMKilled":
		return "OOMKilled", alert.SeverityCritical
	case (t.ExitCode == 137 || t.Signal == 9) && pod.DeletionTimestamp == nil:
		return "ContainerKilled", alert.SeverityWarning
	}
	return "", ""
}

// kubeletTeardown reports whether a current termination came from the kubelet
// ending the pod rather than from a kill while it was meant to run. Node-
// pressure eviction, graceful node shutdown and activeDeadlineSeconds set phase
// Failed with a pod-level reason but no DeletionTimestamp, and the pod stays
// until pod GC, so a SIGKILL there would re-fire on every resync.
// ContainerStatusUnknown means the kubelet lost track of the container, not
// that anything killed it.
func kubeletTeardown(pod *v1.Pod, t *v1.ContainerStateTerminated) bool {
	if t.Reason == "ContainerStatusUnknown" {
		return true
	}
	return pod.Status.Phase == v1.PodFailed && pod.Status.Reason != ""
}

func severityRank(s alert.Severity) int {
	switch s {
	case alert.SeverityCritical:
		return 3
	case alert.SeverityWarning:
		return 2
	default:
		return 1
	}
}

func (p *podWatcher) emitContainerAlert(ctx context.Context, pod *v1.Pod, st v1.ContainerStatus, term *v1.ContainerStateTerminated, reason string, sev alert.Severity, emit Emit) {
	a := alert.New(alert.KindPod, pod.Namespace, pod.Name, reason, sev)
	a.NodeName = pod.Spec.NodeName
	a.Labels["container"] = st.Name
	a.Summary = fmt.Sprintf("container %q in pod %s/%s entered %s", st.Name, pod.Namespace, pod.Name, reason)
	if cause := terminationCause(term); cause != "" {
		a.Summary += " - last termination: " + cause
	}
	a.Annotations = mergeAnnotations(pod)

	// Local enrichment (no API calls) stays on the handler path.
	a.Details["Pod Status"] = collectors.PrintPod(pod)
	a.Details["Container State"] = collectors.DescribeContainerState(st)
	for _, c := range pod.Spec.Containers {
		if c.Name == st.Name {
			a.Details["Resource Spec"] = collectors.GetContainerResource(c)
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
			// emit runs in its own recover, separate from enrich: an enrich
			// panic must still ship the skinny alert, and a panic in the
			// dispatch path (store/router/sink) from this detached goroutine
			// must not crash the controller - every informer-handler path is
			// recovered, so this one must be too.
			func() {
				defer recoverHandler("pod.emit")
				emit(a)
			}()
		}()
	default:
		metrics.EnrichmentSaturated.Inc()
		klog.Warningf("enrichment pool saturated; emitting %s without events/logs", a)
		emit(a)
	}
}

// enrich fills the Details that require apiserver round-trips.
func (p *podWatcher) enrich(ctx context.Context, pod *v1.Pod, st v1.ContainerStatus, reason string, a *alert.Alert) {
	// Collector failures (typically a missing events or pods/log grant) are
	// logged, not fatal: the alert still ships without that section.
	if events, err := collectors.PodEvents(ctx, p.clientset, pod.Namespace, pod.Name); err != nil {
		klog.V(2).Infof("pod %s/%s: collect events: %v", pod.Namespace, pod.Name, err)
	} else if events != "" {
		a.Details["Pod Events"] = events
	}
	if !p.behavior.DisableLogCollection && reason != "ImagePullBackOff" && reason != "ErrImagePull" {
		if logs, err := collectors.PreviousContainerLogs(ctx, p.clientset, pod, st.Name); err != nil {
			klog.V(2).Infof("pod %s/%s: collect previous logs of container %q: %v", pod.Namespace, pod.Name, st.Name, err)
		} else if logs != "" {
			a.Details["Pod Logs Before Restart"] = logs
		}
	}
	if pod.Spec.NodeName != "" {
		if nodeEvents, err := collectors.NodeEvents(ctx, p.clientset, pod.Spec.NodeName); err != nil {
			klog.V(2).Infof("node %s: collect events: %v", pod.Spec.NodeName, err)
		} else if nodeEvents != "" {
			a.Details["Node Events"] = nodeEvents
		}
	}
}

// terminationCause renders the termination an alert was classified from in
// human form ("OOMKilled (exit 137)", "SIGKILL (exit 137)", "SIGTERM (exit
// 143)", "exit 1") for the alert summary, so operators see WHY a container
// died without opening the Container State block. Returns "" for nil.
func terminationCause(t *v1.ContainerStateTerminated) string {
	if t == nil {
		return ""
	}
	// An informative reason beats the exit code: the OOM killer's SIGKILL
	// arrives as exit 137 with signal 0 on containerd and would otherwise
	// read as a plain SIGKILL.
	switch t.Reason {
	case "", "Error", "Completed":
	default:
		return fmt.Sprintf("%s (exit %d)", t.Reason, t.ExitCode)
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

// Registered here so adding a resource kind is one self-contained file.
func init() { Register(func(o Opts) Watcher { return newPod(o.Client, o.Config) }) }
