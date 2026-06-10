package watchers

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"alertkube/internal/alert"
	"alertkube/internal/config"
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

func TestPodEvaluate(t *testing.T) {
	tests := []struct {
		name               string
		oldPod             *v1.Pod
		newPod             *v1.Pod
		ignoreExitCodeZero bool
		wantReason         string
		wantSeverity       alert.Severity
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := podTestConfig()
			cfg.Behavior.IgnoreRestartsWithExitCodeZero = tc.ignoreExitCodeZero
			w := NewPod(fake.NewSimpleClientset(), cfg)

			var got []*alert.Alert
			emit := func(a *alert.Alert) { got = append(got, a) }

			w.evaluate(context.Background(), tc.oldPod, tc.newPod, emit)

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
		})
	}
}

func TestPodShouldHandle(t *testing.T) {
	tests := []struct {
		name      string
		watched   string
		ignored   string
		namespace string
		want      bool
	}{
		{
			name:      "ignoredNamespaces blocks matching namespace",
			ignored:   "kube-system",
			namespace: "kube-system",
			want:      false,
		},
		{
			name:      "ignoredNamespaces leaves other namespaces alone",
			ignored:   "kube-system",
			namespace: "default",
			want:      true,
		},
		{
			name:      "watchedNamespaces allows matching namespace",
			watched:   "prod",
			namespace: "prod",
			want:      true,
		},
		{
			name:      "watchedNamespaces blocks non-matching namespace",
			watched:   "prod",
			namespace: "dev",
			want:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := podTestConfig()
			cfg.Filters.WatchedNamespaces = tc.watched
			cfg.Filters.IgnoredNamespaces = tc.ignored
			w := NewPod(fake.NewSimpleClientset(), cfg)

			pod := makePod()
			pod.Namespace = tc.namespace

			if got := w.shouldHandle(pod); got != tc.want {
				t.Errorf("shouldHandle(ns=%q): got %v, want %v", tc.namespace, got, tc.want)
			}
		})
	}
}
