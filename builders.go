package main

import (
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

func buildClient(kubeconfig string) kubernetes.Interface {
	deadline := time.Now().Add(kubeconfigRetryBudget)
	backoff := 500 * time.Millisecond
	var lastErr error
	for {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			cfg, err = rest.InClusterConfig()
		}
		if err == nil {
			c, err := kubernetes.NewForConfig(cfg)
			if err == nil {
				return c
			}
			lastErr = err
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			klog.Fatalf("kube client init after %s: %v", kubeconfigRetryBudget, lastErr)
		}
		klog.Warningf("kube client init failed: %v (retry in %s)", lastErr, backoff)
		time.Sleep(backoff)
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
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
