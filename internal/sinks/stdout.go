package sinks

import (
	"context"

	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/internal/alert"
)

// stdoutSink prints alerts to klog - useful for local development.
type stdoutSink struct{}

func init() { Register("stdout", func(SinkConfig) Sink { return NewStdout() }) }

func NewStdout() Sink { return &stdoutSink{} }

func (*stdoutSink) Name() string                   { return "stdout" }
func (*stdoutSink) Supports(_ alert.Severity) bool { return true }

func (*stdoutSink) Send(_ context.Context, a *alert.Alert) error {
	klog.Infof("ALERT %s summary=%q", a, a.Summary)
	return nil
}
