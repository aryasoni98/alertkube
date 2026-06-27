package collectors

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ---- RedactSecrets: additional patterns + non-leak guarantees ----

func TestRedactSecrets_MorePatterns(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		mustNot []string
		must    []string
	}{
		{"github oauth token", "tok gho_0123456789abcdefghijklmnopqrstuv end", []string{"gho_0123456789"}, []string{"[REDACTED]"}},
		{"aws secret kv", "aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCY end", []string{"wJalrXUtnFEMI"}, []string{"[REDACTED]"}},
		{"api_key kv", "api_key: abcdef123456 trailing", []string{"abcdef123456"}, []string{"[REDACTED]"}},
		{"token query", "https://h/p?api_key=zzz&x=1", []string{"zzz"}, []string{"[REDACTED]", "x=1"}},
		{"signature query", "https://h/p?signature=deadbeef&keep=2", []string{"deadbeef"}, []string{"keep=2"}},
		{"basic auth url", "redis://user:p@55w0rd@cache:6379", []string{"p@55w0rd"}, []string{"[REDACTED]"}},
		{"multiline", "line1 password=secretpw\nline2 ok", []string{"secretpw"}, []string{"line2 ok"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactSecrets(c.in)
			for _, leak := range c.mustNot {
				if strings.Contains(got, leak) {
					t.Fatalf("secret leaked: %q still in %q", leak, got)
				}
			}
			for _, want := range c.must {
				if !strings.Contains(got, want) {
					t.Fatalf("expected %q in %q", want, got)
				}
			}
		})
	}
}

func TestRedactSecrets_Empty(t *testing.T) {
	if got := RedactSecrets(""); got != "" {
		t.Fatalf("empty in should stay empty, got %q", got)
	}
}

func TestRedactSecrets_PreservesNormalLogs(t *testing.T) {
	// Realistic multi-line app log with no credentials must pass through verbatim.
	in := strings.Join([]string{
		"2024-01-02T03:04:05Z starting server on :8080",
		"GET /healthz 200 1ms",
		"reconcile loop completed, 12 objects",
		"exit code 137 (OOMKilled)",
	}, "\n")
	if got := RedactSecrets(in); got != in {
		t.Fatalf("normal log was altered:\n in=%q\nout=%q", in, got)
	}
}

// ---- PreviousContainerLogs: fake-client stream + redaction ----

func TestPreviousContainerLogs(t *testing.T) {
	// The fake clientset returns "fake logs" as the GetLogs stream body.
	c := fake.NewSimpleClientset()
	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "p"}}
	out, err := PreviousContainerLogs(context.Background(), c, pod, "app")
	if err != nil {
		t.Fatalf("PreviousContainerLogs: %v", err)
	}
	if out == "" {
		t.Fatal("expected non-empty log output from the fake stream")
	}
}

// ---- PodEvents / NodeEvents: fake-client list + formatting ----

func TestPodEvents(t *testing.T) {
	now := metav1.NewTime(time.Now())
	older := metav1.NewTime(time.Now().Add(-time.Hour))
	c := fake.NewSimpleClientset(
		&v1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "ns", Name: "e1"},
			InvolvedObject: v1.ObjectReference{Kind: "Pod", Name: "boom"},
			Reason:         "BackOff", Message: "restarting failed container", Type: "Warning",
			LastTimestamp: now,
		},
		&v1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "ns", Name: "e2"},
			InvolvedObject: v1.ObjectReference{Kind: "Pod", Name: "boom"},
			Reason:         "Pulled", Message: "pulled image", Type: "Warning",
			LastTimestamp: older,
		},
		// A different pod's event must not appear (belt-and-suspenders filter).
		&v1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "ns", Name: "e3"},
			InvolvedObject: v1.ObjectReference{Kind: "Pod", Name: "other"},
			Reason:         "BackOff", Message: "nope", Type: "Warning",
			LastTimestamp: now,
		},
	)
	out, err := PodEvents(context.Background(), c, "ns", "boom")
	if err != nil {
		t.Fatalf("PodEvents: %v", err)
	}
	if !strings.Contains(out, "BackOff") || !strings.Contains(out, "Pulled") {
		t.Fatalf("expected both boom events, got %q", out)
	}
	if strings.Contains(out, "nope") {
		t.Fatalf("event for a different object leaked: %q", out)
	}
	// Oldest first: Pulled (older) should precede BackOff (now).
	if strings.Index(out, "Pulled") > strings.Index(out, "BackOff") {
		t.Fatalf("events not sorted oldest-first: %q", out)
	}
}

func TestNodeEvents(t *testing.T) {
	c := fake.NewSimpleClientset(&v1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: "default", Name: "ne1"},
		InvolvedObject: v1.ObjectReference{Kind: "Node", Name: "node-1"},
		Reason:         "NodeNotReady", Message: "kubelet stopped posting status",
		LastTimestamp: metav1.NewTime(time.Now()),
	})
	out, err := NodeEvents(context.Background(), c, "node-1")
	if err != nil {
		t.Fatalf("NodeEvents: %v", err)
	}
	if !strings.Contains(out, "NodeNotReady") {
		t.Fatalf("expected node event, got %q", out)
	}
}

func TestFormatEventsEmpty(t *testing.T) {
	if got := formatEvents(nil, "x"); got != "" {
		t.Fatalf("no events should format to empty, got %q", got)
	}
}

// ---- describe.go: PrintPod / PrintNode / DescribeContainerState / GetContainerResource ----

func TestPrintPod_CrashLoop(t *testing.T) {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web", CreationTimestamp: metav1.NewTime(time.Now().Add(-5 * time.Minute))},
		Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "web"}}},
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
			ContainerStatuses: []v1.ContainerStatus{{
				Name:         "web",
				RestartCount: 7,
				State:        v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
		},
	}
	out, err := PrintPod(pod)
	if err != nil {
		t.Fatalf("PrintPod: %v", err)
	}
	if !strings.Contains(out, "web") || !strings.Contains(out, "CrashLoopBackOff") {
		t.Fatalf("PrintPod missing fields: %q", out)
	}
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "RESTARTS") {
		t.Fatalf("PrintPod missing header: %q", out)
	}
}

func TestPrintNode_NotReadyCordoned(t *testing.T) {
	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))},
		Spec:       v1.NodeSpec{Unschedulable: true},
		Status: v1.NodeStatus{
			Conditions: []v1.NodeCondition{{Type: v1.NodeReady, Status: v1.ConditionFalse}},
			NodeInfo:   v1.NodeSystemInfo{KubeletVersion: "v1.31.0"},
		},
	}
	out, err := PrintNode(node)
	if err != nil {
		t.Fatalf("PrintNode: %v", err)
	}
	if !strings.Contains(out, "NotReady") || !strings.Contains(out, "SchedulingDisabled") {
		t.Fatalf("PrintNode missing status: %q", out)
	}
	if !strings.Contains(out, "v1.31.0") {
		t.Fatalf("PrintNode missing version: %q", out)
	}
}

func TestDescribeContainerState_Terminated(t *testing.T) {
	st := v1.ContainerStatus{
		Name:         "app",
		Ready:        false,
		RestartCount: 3,
		State:        v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137, Signal: 9,
			StartedAt:  metav1.NewTime(time.Now().Add(-time.Minute)),
			FinishedAt: metav1.NewTime(time.Now()),
		}},
	}
	out, err := DescribeContainerState(st)
	if err != nil {
		t.Fatalf("DescribeContainerState: %v", err)
	}
	for _, want := range []string{"app", "Restart Count", "CrashLoopBackOff", "OOMKilled", "137"} {
		if !strings.Contains(out, want) {
			t.Fatalf("DescribeContainerState missing %q in %q", want, out)
		}
	}
}

func TestGetContainerResource(t *testing.T) {
	c := v1.Container{
		Name: "app",
		Resources: v1.ResourceRequirements{
			Limits:   v1.ResourceList{v1.ResourceMemory: resource.MustParse("256Mi")},
			Requests: v1.ResourceList{v1.ResourceCPU: resource.MustParse("50m")},
		},
	}
	out, err := GetContainerResource(c)
	if err != nil {
		t.Fatalf("GetContainerResource: %v", err)
	}
	if !strings.Contains(out, "Limits") || !strings.Contains(out, "256Mi") {
		t.Fatalf("limits missing: %q", out)
	}
	if !strings.Contains(out, "Requests") || !strings.Contains(out, "50m") {
		t.Fatalf("requests missing: %q", out)
	}
}

func TestPrintBoolAndTimestamp(t *testing.T) {
	if printBool(true) != "True" || printBool(false) != "False" {
		t.Fatal("printBool wrong")
	}
	if got := translateTimestampSince(metav1.Time{}); got != "<unknown>" {
		t.Fatalf("zero timestamp = %q, want <unknown>", got)
	}
}
