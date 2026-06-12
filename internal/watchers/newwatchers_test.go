package watchers

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

func collect() (Emit, *[]*alert.Alert) {
	got := &[]*alert.Alert{}
	return func(a *alert.Alert) { *got = append(*got, a) }, got
}

func TestDaemonSetUnavailableFires(t *testing.T) {
	w := NewDaemonSet(&config.Config{})
	emit, got := collect()
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "logger"},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 5, NumberReady: 3, NumberUnavailable: 2},
	}
	w.evaluate(ds, emit)
	if len(*got) != 1 || (*got)[0].Reason != "DaemonSetUnavailable" {
		t.Fatalf("got %v", *got)
	}
	ds.Status.NumberUnavailable = 0
	w.evaluate(ds, emit)
	if len(*got) != 1 {
		t.Fatalf("healthy daemonset must not fire")
	}
}

func TestStatefulSetShortfall(t *testing.T) {
	w := NewStatefulSet(&config.Config{})
	emit, got := collect()
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "db", Generation: 2},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr.To(int32(3))},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1, ObservedGeneration: 2},
	}
	w.evaluate(sts, emit)
	if len(*got) != 1 || (*got)[0].Reason != "StatefulSetReplicasUnavailable" {
		t.Fatalf("got %v", *got)
	}

	// Stale generation must not fire.
	sts.Status.ObservedGeneration = 1
	w.evaluate(sts, emit)
	if len(*got) != 1 {
		t.Fatalf("unobserved generation must not fire")
	}

	// Fully ready must not fire.
	sts.Status.ObservedGeneration = 2
	sts.Status.ReadyReplicas = 3
	w.evaluate(sts, emit)
	if len(*got) != 1 {
		t.Fatalf("ready statefulset must not fire")
	}
}

func TestCronJobMissingSuccess(t *testing.T) {
	w := NewCronJob(&config.Config{})
	emit, got := collect()

	t0 := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	t1 := metav1.NewTime(time.Now().Add(-time.Hour))
	cj := func(sched, success *metav1.Time) *batchv1.CronJob {
		return &batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "backup"},
			Status:     batchv1.CronJobStatus{LastScheduleTime: sched, LastSuccessfulTime: success},
		}
	}

	// New tick, previous tick never succeeded -> fire.
	w.evaluate(cj(&t0, nil), cj(&t1, nil), emit)
	if len(*got) != 1 || (*got)[0].Reason != "CronJobMissingSuccess" {
		t.Fatalf("got %v", *got)
	}

	// New tick, previous tick succeeded -> quiet.
	succ := metav1.NewTime(t0.Add(time.Minute))
	w.evaluate(cj(&t0, &succ), cj(&t1, &succ), emit)
	if len(*got) != 1 {
		t.Fatalf("successful previous tick must not fire")
	}

	// No new tick -> quiet.
	w.evaluate(cj(&t1, nil), cj(&t1, nil), emit)
	if len(*got) != 1 {
		t.Fatalf("same schedule time must not fire")
	}
}

func TestCronJobSuspendTransition(t *testing.T) {
	w := NewCronJob(&config.Config{})
	emit, got := collect()
	off := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "b"},
		Spec: batchv1.CronJobSpec{Suspend: ptr.To(false)}}
	on := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "b"},
		Spec: batchv1.CronJobSpec{Suspend: ptr.To(true)}}
	w.evaluate(off, on, emit)
	if len(*got) != 1 || (*got)[0].Reason != "CronJobSuspended" {
		t.Fatalf("got %v", *got)
	}
	// Already suspended -> no re-fire.
	w.evaluate(on, on, emit)
	if len(*got) != 1 {
		t.Fatalf("steady suspended state must not re-fire")
	}
}

func TestHPAMaxedOut(t *testing.T) {
	w := NewHPA(&config.Config{})
	emit, got := collect()
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "api"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MaxReplicas:    10,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "api"},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			CurrentReplicas: 10,
			DesiredReplicas: 10,
			Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{{
				Type: autoscalingv2.ScalingLimited, Status: v1.ConditionTrue, Reason: "TooManyReplicas",
				Message: "the desired replica count is more than the maximum replica count",
			}},
		},
	}
	w.evaluate(hpa, emit)
	if len(*got) != 1 || (*got)[0].Reason != "HPAMaxedOut" {
		t.Fatalf("got %v", *got)
	}

	// At max but not scaling-limited -> quiet.
	hpa.Status.Conditions[0].Status = v1.ConditionFalse
	w.evaluate(hpa, emit)
	if len(*got) != 1 {
		t.Fatalf("unlimited HPA at max must not fire")
	}
}
