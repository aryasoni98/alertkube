package sinks

import (
	"context"

	"k8s.io/klog/v2"

	"alertkube/internal/alert"
)

// StdoutSink prints alerts to klog - useful for local development.
type StdoutSink struct{}

func NewStdout() *StdoutSink { return &StdoutSink{} }

func (*StdoutSink) Name() string                       { return "stdout" }
func (*StdoutSink) Supports(_ alert.Severity) bool     { return true }

func (*StdoutSink) Send(_ context.Context, a *alert.Alert) error {
	klog.Infof("ALERT %s summary=%q", a, a.Summary)
	return nil
}
