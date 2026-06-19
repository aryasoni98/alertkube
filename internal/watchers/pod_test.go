package watchers

import (
	"context"
	"sync"
	"testing"
	"time"

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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := podTestConfig()
			cfg.Behavior.IgnoreRestartsWithExitCodeZero = tc.ignoreExitCodeZero
			w := NewPod(fake.NewSimpleClientset(), cfg)

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
		})
	}
}

func TestTerminationCause(t *testing.T) {
	term := func(sig, code int32, reason string) v1.ContainerStatus {
		return v1.ContainerStatus{LastTerminationState: v1.ContainerState{
			Terminated: &v1.ContainerStateTerminated{Signal: sig, ExitCode: code, Reason: reason},
		}}
	}
	cases := []struct {
		name string
		st   v1.ContainerStatus
		want string
	}{
		{"no termination", v1.ContainerStatus{}, ""},
		{"sigkill by signal", term(9, 137, ""), "SIGKILL (exit 137)"},
		{"sigterm by signal", term(15, 143, ""), "SIGTERM (exit 143)"},
		{"sigkill by exit code", term(0, 137, "Error"), "SIGKILL (exit 137)"},
		{"sigterm by exit code", term(0, 143, "Error"), "SIGTERM (exit 143)"},
		{"plain error exit", term(0, 1, "Error"), "Error (exit 1)"},
		{"bare exit code", term(0, 2, ""), "exit 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminationCause(tc.st); got != tc.want {
				t.Errorf("terminationCause: got %q, want %q", got, tc.want)
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

func TestDrainWaitsForInflightEnrichment(t *testing.T) {
	cfg := podTestConfig()
	w := NewPod(fake.NewSimpleClientset(), cfg)

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
	w := NewPod(fake.NewSimpleClientset(), podTestConfig())

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
