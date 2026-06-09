package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/metrics"
	"alertkube/internal/router"
	"alertkube/internal/sinks"
	"alertkube/internal/watchers"
)

func main() {
	var kubeconfig, configPath string
	if home := homedir.HomeDir(); home != "" {
		flag.StringVar(&kubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "kubeconfig path")
	} else {
		flag.StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig path")
	}
	flag.StringVar(&configPath, "config", os.Getenv("ALERTKUBE_CONFIG"), "YAML config path")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		klog.Fatalf("config: %v", err)
	}

	clientset := buildClient(kubeconfig)

	// Sinks registry.
	reg := sinks.NewRegistry()
	reg.Add(sinks.NewSlack(cfg.Cluster, "alertkube", map[alert.Severity]string{
		alert.SeverityCritical: cfg.Channels.Critical,
		alert.SeverityWarning:  cfg.Channels.Warning,
		alert.SeverityInfo:     cfg.Channels.Info,
	}))
	reg.Add(sinks.NewPagerDuty())
	reg.Add(sinks.NewTeams())
	reg.Add(sinks.NewWebhook())
	reg.Add(sinks.NewStdout())

	defaultSinks := []string{"slack"}

	r := router.New(cfg.Routing, cfg.Inhibitions, cfg.Silences, defaultSinks)

	store := alert.NewStore(
		time.Duration(cfg.Behavior.MuteSeconds)*time.Second,
		time.Duration(cfg.Behavior.ResolveTTLSeconds)*time.Second,
		func(a *alert.Alert) {
			route := r.Route(a)
			reg.Dispatch(context.Background(), a, route)
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emit := func(a *alert.Alert) {
		metrics.AlertsTotal.WithLabelValues(string(a.Kind), string(a.Severity), a.Reason).Inc()
		if !store.ShouldSend(a) {
			metrics.AlertsSuppressed.WithLabelValues("muted").Inc()
			store.Touch(a.Fingerprint)
			return
		}
		route := r.Route(a)
		if route == nil {
			return
		}
		reg.Dispatch(ctx, a, route)
	}

	factory := informers.NewSharedInformerFactory(clientset, 0)
	ws := []watchers.Watcher{
		watchers.NewPod(clientset, cfg),
		watchers.NewNode(clientset),
		watchers.NewDeployment(),
		watchers.NewPVC(),
		watchers.NewJob(),
	}
	for _, w := range ws {
		w.Setup(ctx, factory, emit)
	}

	metrics.Serve(cfg.MetricsAddr)

	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())
	klog.Info("alertkube started")

	// Background sweeps.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				store.SweepResolved()
				store.CleanOldHistory()
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	klog.Info("alertkube shutting down")
	cancel()
	wg.Wait()
}

func buildClient(kubeconfig string) kubernetes.Interface {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		cfg, err = rest.InClusterConfig()
		if err != nil {
			klog.Fatalf("kube config: %v", err)
		}
	}
	c, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Fatalf("clientset: %v", err)
	}
	return c
}
