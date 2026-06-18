package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/client-go/util/homedir"
	"k8s.io/klog/v2"

	"alertkube/internal/config"
	"alertkube/internal/env"
	"alertkube/internal/metrics"
)

const (
	sweepInterval = 30 * time.Second
	appName       = "alertkube"
)

// version is overridden at build time via -ldflags "-X main.version=...".
// Logged at startup so the running image version is observable in pod logs
// without exec-ing into the container.
var version = "dev"

// Flags shared between leader-election bootstrap and the controller body.
type runtimeFlags struct {
	kubeconfig            string
	configPath            string
	watchNamespace        string
	leaderElect           bool
	leaderElectionNS      string
	leaderElectionLeaseID string
}

func main() {
	flags := parseFlags()
	klog.Infof("%s %s starting", appName, version)

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
		runController(ctx, clientset, cfg, flags.watchNamespace)
	}

	if srv != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			klog.Warningf("metrics server shutdown: %v", err)
		}
	}
}

func parseFlags() runtimeFlags {
	var f runtimeFlags
	if home := homedir.HomeDir(); home != "" {
		flag.StringVar(&f.kubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "kubeconfig path")
	} else {
		flag.StringVar(&f.kubeconfig, "kubeconfig", "", "kubeconfig path")
	}
	flag.StringVar(&f.configPath, "config", os.Getenv("ALERTKUBE_CONFIG"), "YAML config path")
	flag.StringVar(&f.watchNamespace, "watch-namespace", os.Getenv("WATCH_NAMESPACE"), "restrict informers to one namespace (disables node alerts; required for namespace-scoped RBAC)")
	flag.BoolVar(&f.leaderElect, "leader-elect", env.Bool("LEADER_ELECT", false), "enable leader election via a Lease (required when replicas > 1)")
	flag.StringVar(&f.leaderElectionNS, "leader-election-namespace", env.Or("LEADER_ELECTION_NAMESPACE", "kube-system"), "namespace holding the Lease object")
	flag.StringVar(&f.leaderElectionLeaseID, "leader-election-id", os.Getenv("POD_NAME"), "lease holder identity (defaults to POD_NAME or hostname)")
	flag.Parse()
	return f
}

func waitForSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
}
