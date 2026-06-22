package main

import (
	"context"
	"os"
	"time"

	"golang.org/x/time/rate"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/sinks"
	"alertkube/internal/watchers"
)

// kubeconfigRetryBudget bounds how long buildClient retries transient
// failures (e.g. control-plane unavailable at pod start) before giving up.
const kubeconfigRetryBudget = 30 * time.Second

func buildClient(ctx context.Context, kubeconfig string) kubernetes.Interface {
	deadline := time.Now().Add(kubeconfigRetryBudget)
	backoff := 500 * time.Millisecond
	var lastErr error
	for {
		cfg, err := buildConfig(kubeconfig)
		if err == nil {
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

func buildSinks(cfg *config.Config) *sinks.Registry {
	reg := sinks.NewRegistry()
	reg.Add(sinks.NewSlack(cfg.Cluster, appName, map[alert.Severity]string{
		alert.SeverityCritical: cfg.Channels.Critical,
		alert.SeverityWarning:  cfg.Channels.Warning,
		alert.SeverityInfo:     cfg.Channels.Info,
	}))
	reg.Add(sinks.NewPagerDuty())
	reg.Add(sinks.NewTeams())
	reg.Add(sinks.NewWebhook())
	reg.Add(sinks.NewStdout())
	reg.Add(sinks.NewDiscord())
	reg.Add(sinks.NewTelegram())
	reg.Add(sinks.NewOpsgenie())
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
		ws = append(ws, watchers.NewNode(c))
	}
	return ws
}
