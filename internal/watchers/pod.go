package watchers

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/collectors"
	"alertkube/internal/config"
	"alertkube/internal/filter"
)

// PodWatcher reacts to restart, crashloop, oom, imagepull, pending events.
type PodWatcher struct {
	clientset     kubernetes.Interface
	cfg           *config.Config
	watchedNS     *filter.Set
	ignoredNS     *filter.Set
	watchedPrefix *filter.Set
	ignoredPrefix *filter.Set
}

func NewPod(c kubernetes.Interface, cfg *config.Config) *PodWatcher {
	return &PodWatcher{
		clientset:     c,
		cfg:           cfg,
		watchedNS:     filter.New(cfg.Filters.WatchedNamespaces),
		ignoredNS:     filter.New(cfg.Filters.IgnoredNamespaces),
		watchedPrefix: filter.New(cfg.Filters.WatchedPodNamePrefixes),
		ignoredPrefix: filter.New(cfg.Filters.IgnoredPodNamePrefixes),
	}
}

func (p *PodWatcher) Name() string { return "pod" }

func (p *PodWatcher) Setup(ctx context.Context, f informers.SharedInformerFactory, emit Emit) {
	inf := f.Core().V1().Pods().Informer()
	register("pod", inf, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			defer recoverHandler("pod.Add")
			newPod, ok := obj.(*v1.Pod)
			if !ok || !p.shouldHandle(newPod) {
				return
			}
			// Initial-sync: only emit on terminal/waiting conditions; no
			// restart delta exists yet so ContainerRestart is skipped.
			p.evaluate(ctx, nil, newPod, emit)
		},
		UpdateFunc: func(oldObj, curObj interface{}) {
			defer recoverHandler("pod.Update")
			oldPod, _ := oldObj.(*v1.Pod)
			newPod, ok := curObj.(*v1.Pod)
			if !ok || !p.shouldHandle(newPod) {
				return
			}
			p.evaluate(ctx, oldPod, newPod, emit)
		},
	})
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
		if st.LastTerminationState.Terminated != nil && st.LastTerminationState.Terminated.Reason == "OOMKilled" {
			p.emitContainerAlert(ctx, newPod, st, "OOMKilled", alert.SeverityCritical, emit)
			return
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
	a.Annotations = mergeAnnotations(pod)

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
	if events, err := collectors.PodEvents(ctx, p.clientset, pod.Namespace, pod.Name); err == nil && events != "" {
		a.Details["Pod Events"] = events
	}
	if reason != "ImagePullBackOff" && reason != "ErrImagePull" {
		if logs, err := collectors.PreviousContainerLogs(ctx, p.clientset, pod, st.Name); err == nil && logs != "" {
			a.Details["Pod Logs Before Restart"] = logs
		}
	}
	if pod.Spec.NodeName != "" {
		if nodeEvents, err := collectors.NodeEvents(ctx, p.clientset, pod.Spec.NodeName); err == nil && nodeEvents != "" {
			a.Details["Node Events"] = nodeEvents
		}
	}

	emit(a)
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

func mergeAnnotations(pod *v1.Pod) map[string]string {
	out := map[string]string{}
	for k, v := range pod.GetAnnotations() {
		out[k] = v
	}
	for k, v := range pod.GetLabels() {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out
}
