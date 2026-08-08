//go:build integration

// Package integration runs tests against a real API server (envtest: a real
// kube-apiserver + etcd, no kubelet, no scheduler).
//
// It exists because the sharding defects found in the v1.2 audit were invisible
// to both existing test tiers. Unit tests use fake.NewSimpleClientset, which
// does not model resource-version conflicts, so it cannot show two shards
// clobbering one ConfigMap. E2E (chainsaw) runs one deployment and does not
// exercise concurrent writers or Lease contention at all. The failures live in
// the gap between them.
//
// These are excluded from `go test ./...` by the `integration` build tag. Run:
//
//	export KUBEBUILDER_ASSETS=$(setup-envtest use 1.34.1 -p path)
//	go test -tags integration ./test/integration/...
//
// NOTE ON DEPENDENCIES: sigs.k8s.io/controller-runtime appears in go.mod solely
// for envtest's process management (certs, apiserver/etcd lifecycle). It is a
// test-only import - no production package imports it - and ADR-0001's choice
// of client-go for the controller is unaffected. Hand-rolling apiserver
// bootstrap was the alternative and is not worth the maintenance.
package integration

import (
	"fmt"
	"os"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	restCfg   *rest.Config
	clientset kubernetes.Interface
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		fmt.Fprintln(os.Stderr, "KUBEBUILDER_ASSETS is unset; run: export KUBEBUILDER_ASSETS=$(setup-envtest use 1.34.1 -p path)")
		os.Exit(1)
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		os.Exit(1)
	}
	restCfg = cfg
	clientset, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build clientset: %v\n", err)
		_ = env.Stop()
		os.Exit(1)
	}
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
	}
	os.Exit(code)
}
