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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/util/homedir"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/metrics"
	"alertkube/internal/router"
	"alertkube/internal/sinks"
	"alertkube/internal/watchers"
)

const (
	sweepInterval = 30 * time.Second
	appName       = "alertkube"
)

// Flags shared between leader-election bootstrap and the controller body.
type runtimeFlags struct {
	kubeconfig            string
	configPath            string
	leaderElect           bool
	leaderElectionNS      string
	leaderElectionLeaseID string
}

func main() {
	flags := parseFlags()

	cfg, err := config.Load(flags.configPath)
	if err != nil {
		klog.Fatalf("config: %v", err)
	}

	clientset := buildClient(flags.kubeconfig)

	// Metrics + health start outside the leader-election gate so the pod
	// can still be scraped and probed when it is a hot-standby follower.
	srv := metrics.Serve(cfg.MetricsAddr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		waitForSignal()
		klog.Infof("%s received shutdown signal", appName)
		cancel()
	}()

	if flags.leaderElect {
		runWithLeaderElection(ctx, clientset, cfg, flags)
	} else {
		runController(ctx, clientset, cfg)
	}

	if srv != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			klog.Warningf("metrics server shutdown: %v", err)
		}
	}
}

// runController wires the watchers, sweeper, and dispatch path.
// Returns when ctx is cancelled (signal received OR leader election lost).
func runController(ctx context.Context, clientset kubernetes.Interface, cfg *config.Config) {
	reg := buildSinks(cfg)
	r := router.New(cfg.Routing, cfg.Inhibitions, cfg.Silences, []string{"slack"})

	store := alert.NewStore(
		time.Duration(cfg.Behavior.MuteSeconds)*time.Second,
		time.Duration(cfg.Behavior.ResolveTTLSeconds)*time.Second,
		func(a *alert.Alert) { reg.Dispatch(ctx, a, r.Route(a)) },
	)
	store.SetOnChange(func(n int) { metrics.ActiveAlerts.Set(float64(n)) })

	emit := makeEmitter(ctx, store, r, reg)

	factory := informers.NewSharedInformerFactory(clientset, 0)
	for _, w := range buildWatchers(clientset, cfg) {
		w.Setup(ctx, factory, emit)
	}

	factory.Start(ctx.Done())
	for kind, synced := range factory.WaitForCacheSync(ctx.Done()) {
		if !synced {
			klog.Fatalf("informer cache for %v did not sync (check RBAC)", kind)
		}
	}
	metrics.MarkReady()
	klog.Infof("%s started", appName)

	var wg sync.WaitGroup
	wg.Add(1)
	go runSweeper(ctx, &wg, store)

	<-ctx.Done()
	klog.Infof("%s shutting down", appName)
	wg.Wait()
	metrics.MarkNotReady()
}

// runWithLeaderElection blocks until the process either wins the lease and
// finishes, or is asked to exit. Only the leader runs the controller body;
// followers wait while serving /healthz + /metrics.
func runWithLeaderElection(ctx context.Context, clientset kubernetes.Interface, cfg *config.Config, flags runtimeFlags) {
	id, _ := os.Hostname()
	if flags.leaderElectionLeaseID != "" {
		id = flags.leaderElectionLeaseID
	}
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      appName,
			Namespace: flags.leaderElectionNS,
		},
		Client: clientset.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: id,
		},
	}
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:            lock,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(leadCtx context.Context) {
				klog.Infof("%s acquired leadership (id=%s)", appName, id)
				runController(leadCtx, clientset, cfg)
			},
			OnStoppedLeading: func() {
				klog.Warningf("%s lost leadership (id=%s)", appName, id)
				metrics.MarkNotReady()
			},
			OnNewLeader: func(leader string) {
				if leader != id {
					klog.Infof("standing by; current leader is %s", leader)
				}
			},
		},
	})
}

func parseFlags() runtimeFlags {
	var f runtimeFlags
	if home := homedir.HomeDir(); home != "" {
		flag.StringVar(&f.kubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "kubeconfig path")
	} else {
		flag.StringVar(&f.kubeconfig, "kubeconfig", "", "kubeconfig path")
	}
	flag.StringVar(&f.configPath, "config", os.Getenv("ALERTKUBE_CONFIG"), "YAML config path")
	flag.BoolVar(&f.leaderElect, "leader-elect", envBool("LEADER_ELECT", false), "enable leader election via a Lease (required when replicas > 1)")
	flag.StringVar(&f.leaderElectionNS, "leader-election-namespace", envOr("LEADER_ELECTION_NAMESPACE", "kube-system"), "namespace holding the Lease object")
	flag.StringVar(&f.leaderElectionLeaseID, "leader-election-id", os.Getenv("POD_NAME"), "lease holder identity (defaults to POD_NAME or hostname)")
	flag.Parse()
	return f
}

func envBool(key string, def bool) bool {
	switch os.Getenv(key) {
	case "1", "true", "TRUE", "yes":
		return true
	case "0", "false", "FALSE", "no":
		return false
	}
	return def
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
	return reg
}

func buildWatchers(c kubernetes.Interface, cfg *config.Config) []watchers.Watcher {
	return []watchers.Watcher{
		watchers.NewPod(c, cfg),
		watchers.NewNode(c),
		watchers.NewDeployment(),
		watchers.NewPVC(),
		watchers.NewJob(),
	}
}

func makeEmitter(ctx context.Context, store *alert.Store, r *router.Router, reg *sinks.Registry) watchers.Emit {
	return func(a *alert.Alert) {
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
}

func runSweeper(ctx context.Context, wg *sync.WaitGroup, store *alert.Store) {
	defer wg.Done()
	ticker := time.NewTicker(sweepInterval)
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
}

func waitForSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
}

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
