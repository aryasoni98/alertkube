package app

import (
	"context"
	"os"
	"time"

	"golang.org/x/time/rate"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/env"
	"alertkube/internal/sinks"
	"alertkube/internal/watchers"
)

// kubeconfigRetryBudget bounds how long buildClient retries transient
// failures (e.g. control-plane unavailable at pod start) before giving up.
const kubeconfigRetryBudget = 30 * time.Second

// Default client-go QPS/burst for the controller's REST client. client-go's
// library defaults (5 QPS / 10 burst) throttle the initial list/watch sync and
// the on-demand event/log enrichment calls on large clusters, so we raise them.
// Both are overridable via ALERTKUBE_CLIENT_QPS / ALERTKUBE_CLIENT_BURST (the
// chart surfaces them as client.qps / client.burst) for API-server-constrained
// clusters that need to dial them back.
const (
	defaultClientQPS   = 50
	defaultClientBurst = 100
)

func buildClient(ctx context.Context, kubeconfig string) kubernetes.Interface {
	deadline := time.Now().Add(kubeconfigRetryBudget)
	backoff := 500 * time.Millisecond
	var lastErr error
	for {
		cfg, err := buildConfig(kubeconfig)
		if err == nil {
			applyClientThrottle(cfg)
			c, nerr := kubernetes.NewForConfig(cfg)
			if nerr == nil {
				return c
			}
			lastErr = nerr
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			klog.Fatalf("kube client init after %s: %v", kubeconfigRetryBudget, lastErr)
		}
		klog.Warningf("kube client init failed: %v (retry in %s)", lastErr, backoff)
		// Cancellable backoff: a SIGTERM during startup (apiserver down at
		// boot) must exit promptly instead of waiting out the retry budget
		// with the signal handler unable to interrupt a bare time.Sleep.
		select {
		case <-ctx.Done():
			klog.Infof("shutdown signal during kube client init; aborting startup")
			os.Exit(0)
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// buildConfig resolves a *rest.Config, honoring an explicit kubeconfig only
// when its file actually exists. Inside a pod the --kubeconfig default
// (~/.kube/config) does not exist, so the in-cluster config is used rather
// than risk binding to a stray kubeconfig that happens to be mounted; local
// development that points --kubeconfig at a real file still wins.
func buildConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		if _, err := os.Stat(kubeconfig); err == nil {
			return clientcmd.BuildConfigFromFlags("", kubeconfig)
		}
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	// Neither an explicit file nor an in-cluster environment: fall back to
	// client-go's default loading rules (KUBECONFIG env, etc.) and let it
	// surface a useful error if nothing is usable.
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

// buildDynamicClient resolves a dynamic client from the same rest.Config the
// typed client uses (honoring QPS/burst throttling), for watching AlertKube
// CRDs via a dynamic informer. Unlike buildClient it does not retry: it is
// called after buildClient has already proven the apiserver reachable, and a
// failure here is reported to the caller so the controller continues without
// CRD watching rather than failing startup.
func buildDynamicClient(kubeconfig string) (dynamic.Interface, error) {
	cfg, err := buildConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	applyClientThrottle(cfg)
	return dynamic.NewForConfig(cfg)
}

// applyClientThrottle raises the REST client's QPS/burst above client-go's
// conservative library defaults so a large-cluster initial sync and the
// on-demand enrichment calls are not rate-limited client-side. Values are
// read from ALERTKUBE_CLIENT_QPS / ALERTKUBE_CLIENT_BURST; non-positive or
// unset values keep the tuned defaults.
func applyClientThrottle(cfg *rest.Config) {
	qps := env.IntOr("ALERTKUBE_CLIENT_QPS", defaultClientQPS)
	burst := env.IntOr("ALERTKUBE_CLIENT_BURST", defaultClientBurst)
	if qps > 0 {
		cfg.QPS = float32(qps)
	}
	if burst > 0 {
		cfg.Burst = burst
	}
}

// buildSinks constructs the sink registry from the self-registered sink
// factories (each sink registers itself in its own init; see sinks.Register),
// then applies per-sink rate overrides. Adding a sink is therefore a single
// self-contained file - no edit here - with config.KnownSinks pinned to the
// registry by a guard test so routing validation cannot drift.
func buildSinks(cfg *config.Config) *sinks.Registry {
	reg := sinks.BuildDefault(sinks.SinkConfig{
		Cluster: cfg.Cluster,
		Channels: map[alert.Severity]string{
			alert.SeverityCritical: cfg.Channels.Critical,
			alert.SeverityWarning:  cfg.Channels.Warning,
			alert.SeverityInfo:     cfg.Channels.Info,
		},
	})
	for name, sr := range cfg.SinkRates {
		reg.SetRate(name, rate.Limit(sr.PerSecond), sr.Burst)
	}
	return reg
}

func buildWatchers(c kubernetes.Interface, cfg *config.Config, watchNamespace string) []watchers.Watcher {
	ws := []watchers.Watcher{
		watchers.NewPod(c, cfg),
		watchers.NewDeployment(cfg),
		watchers.NewPVC(cfg),
		watchers.NewJob(cfg),
		watchers.NewDaemonSet(cfg),
		watchers.NewStatefulSet(cfg),
		watchers.NewCronJob(cfg),
		watchers.NewHPA(cfg),
	}
	// Nodes are cluster-scoped: a namespace-scoped factory cannot sync a
	// node informer and a namespace Role cannot grant it.
	if watchNamespace == "" {
		ws = append(ws, watchers.NewNode())
	}
	return ws
}
