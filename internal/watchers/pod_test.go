package watchers

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/config"
)

func podTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Behavior.IgnoreRestartCount = 30
	return cfg
}

func makePod(statuses ...v1.ContainerStatus) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "app-1"},
		Spec:       v1.PodSpec{NodeName: "node-1"},
		Status:     v1.PodStatus{ContainerStatuses: statuses},
	}
}

// kubeletFailedPod is a restartPolicy Never pod in phase Failed whose only
// container holds term. reason is the pod-level status reason.
func kubeletFailedPod(reason string, term v1.ContainerStateTerminated) *v1.Pod {
	p := makePod(v1.ContainerStatus{
		Name:  "app",
		State: v1.ContainerState{Terminated: &term},
	})
	p.Spec.RestartPolicy = v1.RestartPolicyNever
	p.Status.Phase = v1.PodFailed
	p.Status.Reason = reason
	return p
}

func TestPodEvaluate(t *testing.T) {
	tests := []struct {
		name               string
		oldPod             *v1.Pod
		newPod             *v1.Pod
		ignoreExitCodeZero bool
		wantReason         string
		wantSeverity       alert.Severity
		wantCause          string
		wantNone           bool
	}{
		{
			// Regression test for the inverted-gate bug: crashloop detection
			// must fire even when total restarts exceed ignoreRestartCount.
			name:   "crashloopbackoff above ignoreRestartCount still fires critical",
			oldPod: nil,
			newPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 50,
				State:        v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}),
			wantReason:   "CrashLoopBackOff",
			wantSeverity: alert.SeverityCritical,
		},
		{
			name:   "add with historical restarts is not a restart delta",
			oldPod: nil,
			newPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 5,
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{ExitCode: 1},
				},
			}),
			wantNone: true,
		},
		{
			name:   "imagepullbackoff fires warning",
			oldPod: nil,
			newPod: makePod(v1.ContainerStatus{
				Name:  "app",
				State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
			}),
			wantReason:   "ImagePullBackOff",
			wantSeverity: alert.SeverityWarning,
		},
		{
			name:   "errimagepull fires warning",
			oldPod: nil,
			newPod: makePod(v1.ContainerStatus{
				Name:  "app",
				State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "ErrImagePull"}},
			}),
			wantReason:   "ErrImagePull",
			wantSeverity: alert.SeverityWarning,
		},
		{
			name:   "oomkilled in last termination state fires critical",
			oldPod: nil,
			newPod: makePod(v1.ContainerStatus{
				Name: "app",
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
				},
			}),
			wantReason:   "OOMKilled",
			wantSeverity: alert.SeverityCritical,
		},
		{
			name:   "non-OOM SIGKILL (exit 137) on a running pod fires ContainerKilled warning",
			oldPod: nil,
			newPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 1,
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{Reason: "Error", ExitCode: 137},
				},
			}),
			wantReason:   "ContainerKilled",
			wantSeverity: alert.SeverityWarning,
		},
		{
			name:   "SIGKILL during pod deletion stays silent (graceful teardown)",
			oldPod: nil,
			newPod: func() *v1.Pod {
				p := makePod(v1.ContainerStatus{
					Name:         "app",
					RestartCount: 0,
					LastTerminationState: v1.ContainerState{
						Terminated: &v1.ContainerStateTerminated{ExitCode: 137},
					},
				})
				now := metav1.Now()
				p.DeletionTimestamp = &now
				return p
			}(),
			wantNone: true,
		},
		{
			name: "restart delta fires ContainerRestart warning",
			oldPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 1,
			}),
			newPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 2,
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{Reason: "Error", ExitCode: 1},
				},
			}),
			wantReason:   "ContainerRestart",
			wantSeverity: alert.SeverityWarning,
		},
		{
			name: "restart delta above ignoreRestartCount is suppressed",
			oldPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 31,
			}),
			newPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 32,
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{Reason: "Error", ExitCode: 1},
				},
			}),
			wantNone: true,
		},
		{
			name:               "exit code zero restart ignored when configured",
			ignoreExitCodeZero: true,
			oldPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 1,
			}),
			newPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 2,
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{Reason: "Completed", ExitCode: 0},
				},
			}),
			wantNone: true,
		},
		{
			name:   "current OOM kill fires critical before the restart",
			oldPod: nil,
			newPod: makePod(v1.ContainerStatus{
				Name:  "app",
				State: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
			}),
			wantReason:   "OOMKilled",
			wantSeverity: alert.SeverityCritical,
			wantCause:    "OOMKilled (exit 137)",
		},
		{
			name:   "current SIGKILL fires ContainerKilled warning",
			oldPod: nil,
			newPod: makePod(v1.ContainerStatus{
				Name:  "app",
				State: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{Reason: "Error", ExitCode: 137}},
			}),
			wantReason:   "ContainerKilled",
			wantSeverity: alert.SeverityWarning,
			wantCause:    "SIGKILL (exit 137)",
		},
		{
			// Kubelet node-pressure eviction sets phase Failed without a
			// deletionTimestamp, and the pod lingers until pod GC.
			name:     "evicted pod with an unknown container status is silent",
			oldPod:   nil,
			newPod:   kubeletFailedPod("Evicted", v1.ContainerStateTerminated{Reason: "ContainerStatusUnknown", ExitCode: 137}),
			wantNone: true,
		},
		{
			name:     "evicted pod with a SIGKILLed container is silent",
			oldPod:   nil,
			newPod:   kubeletFailedPod("Evicted", v1.ContainerStateTerminated{Reason: "Error", ExitCode: 137}),
			wantNone: true,
		},
		{
			name:     "pod killed by graceful node shutdown is silent",
			oldPod:   nil,
			newPod:   kubeletFailedPod("Terminated", v1.ContainerStateTerminated{Reason: "Error", ExitCode: 137}),
			wantNone: true,
		},
		{
			name:     "pod past activeDeadlineSeconds is silent",
			oldPod:   nil,
			newPod:   kubeletFailedPod("DeadlineExceeded", v1.ContainerStateTerminated{Reason: "Error", Signal: 9}),
			wantNone: true,
		},
		{
			name:     "current unknown container status is not a kill",
			oldPod:   nil,
			newPod:   kubeletFailedPod("", v1.ContainerStateTerminated{Reason: "ContainerStatusUnknown", ExitCode: 137}),
			wantNone: true,
		},
		{
			name:         "evicted pod still reports a current OOM kill",
			oldPod:       nil,
			newPod:       kubeletFailedPod("Evicted", v1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}),
			wantReason:   "OOMKilled",
			wantSeverity: alert.SeverityCritical,
			wantCause:    "OOMKilled (exit 137)",
		},
		{
			name:         "failed pod without a kubelet reason still reports a SIGKILL",
			oldPod:       nil,
			newPod:       kubeletFailedPod("", v1.ContainerStateTerminated{Reason: "Error", ExitCode: 137}),
			wantReason:   "ContainerKilled",
			wantSeverity: alert.SeverityWarning,
			wantCause:    "SIGKILL (exit 137)",
		},
		{
			name:   "summary describes the current termination, not the previous run",
			oldPod: nil,
			newPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 1,
				State:        v1.ContainerState{Terminated: &v1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{Reason: "Error", ExitCode: 1},
				},
			}),
			wantReason:   "OOMKilled",
			wantSeverity: alert.SeverityCritical,
			wantCause:    "OOMKilled (exit 137)",
		},
		{
			name:   "current failure does not re-report the previous run's OOM kill",
			oldPod: nil,
			newPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 1,
				State:        v1.ContainerState{Terminated: &v1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
				},
			}),
			wantNone: true,
		},
		{
			name: "restartPolicy Never OOM kill fires critical",
			oldPod: func() *v1.Pod {
				p := makePod(v1.ContainerStatus{
					Name:  "app",
					State: v1.ContainerState{Running: &v1.ContainerStateRunning{}},
				})
				p.Spec.RestartPolicy = v1.RestartPolicyNever
				return p
			}(),
			newPod: func() *v1.Pod {
				p := makePod(v1.ContainerStatus{
					Name:  "app",
					State: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
				})
				p.Spec.RestartPolicy = v1.RestartPolicyNever
				return p
			}(),
			wantReason:   "OOMKilled",
			wantSeverity: alert.SeverityCritical,
			wantCause:    "OOMKilled (exit 137)",
		},
		{
			name: "restart after an OOM kill fires OOMKilled critical",
			oldPod: makePod(v1.ContainerStatus{
				Name:  "app",
				State: v1.ContainerState{Running: &v1.ContainerStateRunning{}},
			}),
			newPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 1,
				State:        v1.ContainerState{Running: &v1.ContainerStateRunning{}},
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
				},
			}),
			wantReason:   "OOMKilled",
			wantSeverity: alert.SeverityCritical,
			wantCause:    "OOMKilled (exit 137)",
		},
		{
			name: "restart after an OOM kill above ignoreRestartCount still fires",
			oldPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 31,
				State:        v1.ContainerState{Running: &v1.ContainerStateRunning{}},
			}),
			newPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 32,
				State:        v1.ContainerState{Running: &v1.ContainerStateRunning{}},
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
				},
			}),
			wantReason:   "OOMKilled",
			wantSeverity: alert.SeverityCritical,
		},
		{
			name: "restart after a SIGKILL fires ContainerKilled warning",
			oldPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 1,
				State:        v1.ContainerState{Running: &v1.ContainerStateRunning{}},
			}),
			newPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 2,
				State:        v1.ContainerState{Running: &v1.ContainerStateRunning{}},
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{Reason: "Error", ExitCode: 137},
				},
			}),
			wantReason:   "ContainerKilled",
			wantSeverity: alert.SeverityWarning,
			wantCause:    "SIGKILL (exit 137)",
		},
		{
			// The reason stays CrashLoopBackOff (checked first, equal rank);
			// the summary carries the OOM kill.
			name:   "OOM crash loop names the OOM kill in the summary",
			oldPod: nil,
			newPod: makePod(v1.ContainerStatus{
				Name:         "app",
				RestartCount: 3,
				State:        v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: v1.ContainerState{
					Terminated: &v1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
				},
			}),
			wantReason:   "CrashLoopBackOff",
			wantSeverity: alert.SeverityCritical,
			wantCause:    "OOMKilled (exit 137)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := podTestConfig()
			cfg.Behavior.IgnoreRestartsWithExitCodeZero = tc.ignoreExitCodeZero
			w := newPod(fake.NewSimpleClientset(), cfg)

			var mu sync.Mutex
			var got []*alert.Alert
			emit := func(a *alert.Alert) {
				mu.Lock()
				got = append(got, a)
				mu.Unlock()
			}

			w.evaluate(context.Background(), tc.oldPod, tc.newPod, emit)
			w.enrichWG.Wait() // emit runs async after enrichment

			if tc.wantNone {
				if len(got) != 0 {
					t.Fatalf("expected no alerts, got %d: %v", len(got), got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(got))
			}
			a := got[0]
			if a.Reason != tc.wantReason {
				t.Errorf("reason: got %q, want %q", a.Reason, tc.wantReason)
			}
			if a.Severity != tc.wantSeverity {
				t.Errorf("severity: got %q, want %q", a.Severity, tc.wantSeverity)
			}
			if a.Kind != alert.KindPod {
				t.Errorf("kind: got %q, want %q", a.Kind, alert.KindPod)
			}
			if a.Namespace != "default" || a.Name != "app-1" {
				t.Errorf("identity: got %s/%s, want default/app-1", a.Namespace, a.Name)
			}
			if tc.wantCause != "" && !strings.HasSuffix(a.Summary, " - last termination: "+tc.wantCause) {
				t.Errorf("summary: got %q, want cause %q", a.Summary, tc.wantCause)
			}
		})
	}
}

func TestPodLifecyclePicksTheLiveContainer(t *testing.T) {
	cfg := podTestConfig()
	w := newPod(fake.NewSimpleClientset(), cfg)
	var got []*alert.Alert
	emit := func(a *alert.Alert) { got = append(got, a) }

	running := makePod(v1.ContainerStatus{
		Name:  "app",
		State: v1.ContainerState{Running: &v1.ContainerStateRunning{}},
	})
	runningOOM := makePod(v1.ContainerStatus{
		Name:         "app",
		RestartCount: 1,
		State:        v1.ContainerState{Running: &v1.ContainerStateRunning{}},
		LastTerminationState: v1.ContainerState{
			Terminated: &v1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
		},
	})
	w.evaluate(context.Background(), nil, runningOOM, emit)
	w.enrichWG.Wait()
	if len(got) != 0 {
		t.Fatalf("stale OOM on a running container fired: %v", got)
	}

	// The restart that follows an OOM kill is the OOM kill.
	got = nil
	w.evaluate(context.Background(), running, runningOOM, emit)
	w.enrichWG.Wait()
	if len(got) != 1 || got[0].Reason != "OOMKilled" || got[0].Severity != alert.SeverityCritical {
		t.Fatalf("restart after OOM was not classified as OOMKilled: %+v", got)
	}

	// A resync repeats the object with no restart delta.
	got = nil
	w.evaluate(context.Background(), runningOOM, runningOOM, emit)
	w.enrichWG.Wait()
	if len(got) != 0 {
		t.Fatalf("resync re-fired a past OOM: %v", got)
	}

	// A warning on one container must not hide an OOM restart on another.
	got = nil
	oldPair := makePod(
		v1.ContainerStatus{Name: "sidecar"},
		v1.ContainerStatus{Name: "app", State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}},
	)
	newPair := makePod(
		v1.ContainerStatus{
			Name:  "sidecar",
			State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
		},
		runningOOM.Status.ContainerStatuses[0],
	)
	w.evaluate(context.Background(), oldPair, newPair, emit)
	w.enrichWG.Wait()
	if len(got) != 1 || got[0].Reason != "OOMKilled" || got[0].Labels["container"] != "app" {
		t.Fatalf("OOM restart was masked by a sidecar warning: %+v", got)
	}

	got = nil
	both := makePod(
		v1.ContainerStatus{
			Name:  "sidecar",
			State: v1.ContainerState{Running: &v1.ContainerStateRunning{}},
			LastTerminationState: v1.ContainerState{
				Terminated: &v1.ContainerStateTerminated{Reason: "OOMKilled"},
			},
		},
		v1.ContainerStatus{
			Name:  "app",
			State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		},
	)
	w.evaluate(context.Background(), nil, both, emit)
	w.enrichWG.Wait()
	if len(got) != 1 || got[0].Reason != "CrashLoopBackOff" || got[0].Labels["container"] != "app" {
		t.Fatalf("live crashloop was masked: %+v", got)
	}

	got = nil
	oldP := makePod(
		v1.ContainerStatus{Name: "stable", RestartCount: 5},
		v1.ContainerStatus{Name: "app", RestartCount: 0},
	)
	newP := makePod(
		v1.ContainerStatus{Name: "stable", RestartCount: 5},
		v1.ContainerStatus{Name: "app", RestartCount: 1, LastTerminationState: v1.ContainerState{
			Terminated: &v1.ContainerStateTerminated{ExitCode: 1},
		}},
	)
	w.evaluate(context.Background(), oldP, newP, emit)
	w.enrichWG.Wait()
	if len(got) != 1 || got[0].Reason != "ContainerRestart" || got[0].Labels["container"] != "app" {
		t.Fatalf("restart attributed to the wrong container: %+v", got)
	}
}

func TestTerminationCause(t *testing.T) {
	term := func(sig, code int32, reason string) *v1.ContainerStateTerminated {
		return &v1.ContainerStateTerminated{Signal: sig, ExitCode: code, Reason: reason}
	}
	cases := []struct {
		name string
		term *v1.ContainerStateTerminated
		want string
	}{
		{"no termination", nil, ""},
		{"sigkill by signal", term(9, 137, ""), "SIGKILL (exit 137)"},
		{"sigterm by signal", term(15, 143, ""), "SIGTERM (exit 143)"},
		{"sigkill by exit code", term(0, 137, "Error"), "SIGKILL (exit 137)"},
		{"sigterm by exit code", term(0, 143, "Error"), "SIGTERM (exit 143)"},
		{"plain error exit", term(0, 1, "Error"), "Error (exit 1)"},
		{"bare exit code", term(0, 2, ""), "exit 2"},
		{"oom kill by reason", term(0, 137, "OOMKilled"), "OOMKilled (exit 137)"},
		{"oom kill with signal", term(9, 137, "OOMKilled"), "OOMKilled (exit 137)"},
		{"informative reason on exit 137", term(0, 137, "ContainerStatusUnknown"), "ContainerStatusUnknown (exit 137)"},
		{"completed", term(0, 0, "Completed"), "Completed (exit 0)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminationCause(tc.term); got != tc.want {
				t.Errorf("terminationCause: got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPodShouldHandle covers the pod-name prefix filters that shouldHandle
// layers on top of the shared namespace filter (see TestNSFilterAllows).
func TestPodShouldHandle(t *testing.T) {
	tests := []struct {
		name          string
		watchedPrefix string
		ignoredPrefix string
		ignoredNS     string
		podName       string
		want          bool
	}{
		{
			name:    "no prefix filters allow every pod",
			podName: "app-1",
			want:    true,
		},
		{
			name:          "watchedPodNamePrefixes allows matching pod",
			watchedPrefix: "app-,api-",
			podName:       "api-7f9c",
			want:          true,
		},
		{
			name:          "watchedPodNamePrefixes blocks non-matching pod",
			watchedPrefix: "app-",
			podName:       "batch-1",
			want:          false,
		},
		{
			name:          "ignoredPodNamePrefixes blocks matching pod",
			ignoredPrefix: "debug-",
			podName:       "debug-shell",
			want:          false,
		},
		{
			name:          "ignoredPodNamePrefixes leaves other pods alone",
			ignoredPrefix: "debug-",
			podName:       "app-1",
			want:          true,
		},
		{
			name:          "ignoredPodNamePrefixes wins over watchedPodNamePrefixes",
			watchedPrefix: "app-",
			ignoredPrefix: "app-canary",
			podName:       "app-canary-1",
			want:          false,
		},
		{
			name:          "namespace filter still applies to a matching pod name",
			watchedPrefix: "app-",
			ignoredNS:     "kube-system",
			podName:       "app-1",
			want:          false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := podTestConfig()
			cfg.Filters.WatchedPodNamePrefixes = tc.watchedPrefix
			cfg.Filters.IgnoredPodNamePrefixes = tc.ignoredPrefix
			cfg.Filters.IgnoredNamespaces = tc.ignoredNS
			w := newPod(fake.NewSimpleClientset(), cfg)

			pod := makePod()
			pod.Name = tc.podName
			if tc.ignoredNS != "" {
				pod.Namespace = tc.ignoredNS
			}

			if got := w.shouldHandle(pod); got != tc.want {
				t.Errorf("shouldHandle(name=%q): got %v, want %v", tc.podName, got, tc.want)
			}
		})
	}
}

func TestDrainWaitsForInflightEnrichment(t *testing.T) {
	cfg := podTestConfig()
	w := newPod(fake.NewSimpleClientset(), cfg)

	var mu sync.Mutex
	var got []*alert.Alert
	emit := func(a *alert.Alert) {
		mu.Lock()
		got = append(got, a)
		mu.Unlock()
	}

	crash := makePod(v1.ContainerStatus{
		Name:  "c",
		State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
	})
	w.evaluate(context.Background(), nil, crash, emit)

	// Drain must block until the async enrichment goroutine has emitted.
	w.Drain(context.Background())

	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("Drain returned before enrichment emitted: got %d alerts, want 1", n)
	}
}

func TestDrainRespectsTimeout(t *testing.T) {
	w := newPod(fake.NewSimpleClientset(), podTestConfig())

	// Simulate a stuck enrichment goroutine so Drain cannot complete on its
	// own; Drain must still return when ctx expires instead of hanging.
	w.enrichWG.Add(1)
	defer w.enrichWG.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Drain(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Drain ignored ctx timeout and hung on a stuck enrichment goroutine")
	}
}

func TestMergeAnnotationsExcludesControlKeysFromLabels(t *testing.T) {
	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{"runbook-url": "https://from-annotation"},
		Labels: map[string]string{
			"alert-silence-until": "2099-01-01T00:00:00Z",
			"alert-slack-channel": "#attacker",
			"runbook-url":         "https://from-label",
			"team":                "payments",
		},
	}}
	got := mergeAnnotations(pod)
	if got["runbook-url"] != "https://from-annotation" {
		t.Fatalf("annotation should win: %q", got["runbook-url"])
	}
	if _, ok := got["alert-silence-until"]; ok {
		t.Fatal("label must not populate alert-silence-until")
	}
	if _, ok := got["alert-slack-channel"]; ok {
		t.Fatal("label must not populate alert-slack-channel")
	}
	if got["team"] != "payments" {
		t.Fatalf("non-control labels should still merge: %q", got["team"])
	}
}
