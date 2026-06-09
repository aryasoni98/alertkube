package collectors

import (
	"bytes"
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/pointer"
)

// PreviousContainerLogs fetches up to 50 lines of the prior container instance.
func PreviousContainerLogs(ctx context.Context, c kubernetes.Interface, pod *v1.Pod, container string) (string, error) {
	opts := &v1.PodLogOptions{
		Container:  container,
		Previous:   true,
		Timestamps: true,
		TailLines:  pointer.Int64Ptr(50),
	}
	rc, err := c.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opts).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("stream logs: %w", err)
	}
	defer rc.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(rc); err != nil {
		return "", err
	}
	return buf.String(), nil
}
