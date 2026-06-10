package collectors

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/kubectl/pkg/describe"
)

// PrintPod renders kubectl-style pod status table.
func PrintPod(pod *v1.Pod) (string, error) {
	restarts := 0
	totalContainers := len(pod.Spec.Containers)
	readyContainers := 0
	lastRestartDate := metav1.NewTime(time.Time{})

	reason := string(pod.Status.Phase)
	if pod.Status.Reason != "" {
		reason = pod.Status.Reason
	}

	initializing := false
	for i := range pod.Status.InitContainerStatuses {
		container := pod.Status.InitContainerStatuses[i]
		restarts += int(container.RestartCount)
		if container.LastTerminationState.Terminated != nil {
			terminatedDate := container.LastTerminationState.Terminated.FinishedAt
			if lastRestartDate.Before(&terminatedDate) {
				lastRestartDate = terminatedDate
			}
		}
		switch {
		case container.State.Terminated != nil && container.State.Terminated.ExitCode == 0:
			continue
		case container.State.Terminated != nil:
			if len(container.State.Terminated.Reason) == 0 {
				if container.State.Terminated.Signal != 0 {
					reason = fmt.Sprintf("Init:Signal:%d", container.State.Terminated.Signal)
				} else {
					reason = fmt.Sprintf("Init:ExitCode:%d", container.State.Terminated.ExitCode)
				}
			} else {
				reason = "Init:" + container.State.Terminated.Reason
			}
			initializing = true
		case container.State.Waiting != nil && len(container.State.Waiting.Reason) > 0 && container.State.Waiting.Reason != "PodInitializing":
			reason = "Init:" + container.State.Waiting.Reason
			initializing = true
		default:
			reason = fmt.Sprintf("Init:%d/%d", i, len(pod.Spec.InitContainers))
			initializing = true
		}
		break
	}
	if !initializing {
		restarts = 0
		hasRunning := false
		for i := len(pod.Status.ContainerStatuses) - 1; i >= 0; i-- {
			container := pod.Status.ContainerStatuses[i]
			restarts += int(container.RestartCount)
			if container.LastTerminationState.Terminated != nil {
				terminatedDate := container.LastTerminationState.Terminated.FinishedAt
				if lastRestartDate.Before(&terminatedDate) {
					lastRestartDate = terminatedDate
				}
			}
			if container.State.Waiting != nil && container.State.Waiting.Reason != "" {
				reason = container.State.Waiting.Reason
			} else if container.State.Terminated != nil && container.State.Terminated.Reason != "" {
				reason = container.State.Terminated.Reason
			} else if container.State.Terminated != nil && container.State.Terminated.Reason == "" {
				if container.State.Terminated.Signal != 0 {
					reason = fmt.Sprintf("Signal:%d", container.State.Terminated.Signal)
				} else {
					reason = fmt.Sprintf("ExitCode:%d", container.State.Terminated.ExitCode)
				}
			} else if container.Ready && container.State.Running != nil {
				hasRunning = true
				readyContainers++
			}
		}
		if reason == "Completed" && hasRunning {
			if hasPodReadyCondition(pod.Status.Conditions) {
				reason = "Running"
			} else {
				reason = "NotReady"
			}
		}
	}

	if pod.DeletionTimestamp != nil && pod.Status.Reason == "NodeLost" {
		reason = "Unknown"
	} else if pod.DeletionTimestamp != nil {
		reason = "Terminating"
	}

	restartsStr := strconv.Itoa(restarts)
	if !lastRestartDate.IsZero() {
		restartsStr = fmt.Sprintf("%d (%s ago)", restarts, translateTimestampSince(lastRestartDate))
	}

	return tabbedString(func(out io.Writer) error {
		w := describe.NewPrefixWriter(out)
		w.Write(describe.LEVEL_0, "NAME\tREADY\tSTATUS\tRESTARTS\tAGE\n")
		w.Write(describe.LEVEL_0, "%s\t%d/%d\t%s\t%s\t%s\n", pod.Name, readyContainers, totalContainers, reason, restartsStr, translateTimestampSince(pod.CreationTimestamp))
		return nil
	})
}

func PrintNode(obj *v1.Node) (string, error) {
	conditionMap := make(map[v1.NodeConditionType]*v1.NodeCondition)
	for i := range obj.Status.Conditions {
		cond := obj.Status.Conditions[i]
		conditionMap[cond.Type] = &cond
	}
	var status []string
	for _, validCondition := range []v1.NodeConditionType{v1.NodeReady} {
		if condition, ok := conditionMap[validCondition]; ok {
			if condition.Status == v1.ConditionTrue {
				status = append(status, string(condition.Type))
			} else {
				status = append(status, "Not"+string(condition.Type))
			}
		}
	}
	if len(status) == 0 {
		status = append(status, "Unknown")
	}
	if obj.Spec.Unschedulable {
		status = append(status, "SchedulingDisabled")
	}
	return tabbedString(func(out io.Writer) error {
		w := describe.NewPrefixWriter(out)
		w.Write(describe.LEVEL_0, "NAME\tSTATUS\tAGE\tVERSION\n")
		w.Write(describe.LEVEL_0, "%s\t%s\t%s\t%s\n", obj.Name, strings.Join(status, ","), translateTimestampSince(obj.CreationTimestamp), obj.Status.NodeInfo.KubeletVersion)
		return nil
	})
}

func DescribeContainerState(status v1.ContainerStatus) (string, error) {
	return tabbedString(func(out io.Writer) error {
		w := describe.NewPrefixWriter(out)
		w.Write(describe.LEVEL_0, "%v:\n", status.Name)
		w.Write(describe.LEVEL_1, "Ready:\t%v\n", printBool(status.Ready))
		w.Write(describe.LEVEL_1, "Restart Count:\t%d\n", status.RestartCount)
		describeStatus("State", status.State, w)
		if status.LastTerminationState.Terminated != nil {
			describeStatus("Last State", status.LastTerminationState, w)
		}
		return nil
	})
}

func describeStatus(stateName string, state v1.ContainerState, w describe.PrefixWriter) {
	switch {
	case state.Running != nil:
		w.Write(describe.LEVEL_1, "%s:\tRunning\n", stateName)
		w.Write(describe.LEVEL_2, "Started:\t%v\n", state.Running.StartedAt.Format(time.RFC1123Z))
	case state.Waiting != nil:
		w.Write(describe.LEVEL_1, "%s:\tWaiting\n", stateName)
		if state.Waiting.Reason != "" {
			w.Write(describe.LEVEL_2, "Reason:\t%s\n", state.Waiting.Reason)
		}
	case state.Terminated != nil:
		w.Write(describe.LEVEL_1, "%s:\tTerminated\n", stateName)
		if state.Terminated.Reason != "" {
			w.Write(describe.LEVEL_2, "Reason:\t%s\n", state.Terminated.Reason)
		}
		if state.Terminated.Message != "" {
			w.Write(describe.LEVEL_2, "Message:\t%s\n", state.Terminated.Message)
		}
		w.Write(describe.LEVEL_2, "Exit Code:\t%d\n", state.Terminated.ExitCode)
		if state.Terminated.Signal > 0 {
			w.Write(describe.LEVEL_2, "Signal:\t%d\n", state.Terminated.Signal)
		}
		w.Write(describe.LEVEL_2, "Started:\t%s\n", state.Terminated.StartedAt.Format(time.RFC1123Z))
		w.Write(describe.LEVEL_2, "Finished:\t%s\n", state.Terminated.FinishedAt.Format(time.RFC1123Z))
	default:
		w.Write(describe.LEVEL_1, "%s:\tWaiting\n", stateName)
	}
}

func GetContainerResource(container v1.Container) (string, error) {
	return tabbedString(func(out io.Writer) error {
		w := describe.NewPrefixWriter(out)
		resources := container.Resources
		if len(resources.Limits) > 0 {
			w.Write(describe.LEVEL_1, "Limits:\n")
		}
		for _, name := range sortedResourceNames(resources.Limits) {
			quantity := resources.Limits[name]
			w.Write(describe.LEVEL_2, "%s:\t%s\n", name, quantity.String())
		}
		if len(resources.Requests) > 0 {
			w.Write(describe.LEVEL_1, "Requests:\n")
		}
		for _, name := range sortedResourceNames(resources.Requests) {
			quantity := resources.Requests[name]
			w.Write(describe.LEVEL_2, "%s:\t%s\n", name, quantity.String())
		}
		return nil
	})
}

func tabbedString(f func(out io.Writer) error) (string, error) {
	out := new(tabwriter.Writer)
	buf := &bytes.Buffer{}
	out.Init(buf, 0, 8, 2, ' ', 0)
	if err := f(out); err != nil {
		return "", err
	}
	if err := out.Flush(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func printBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

func hasPodReadyCondition(conditions []v1.PodCondition) bool {
	for _, c := range conditions {
		if c.Type == v1.PodReady && c.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

func translateTimestampSince(timestamp metav1.Time) string {
	if timestamp.IsZero() {
		return "<unknown>"
	}
	return duration.HumanDuration(time.Since(timestamp.Time))
}

func sortedResourceNames(list v1.ResourceList) []v1.ResourceName {
	out := make([]v1.ResourceName, 0, len(list))
	for r := range list {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
