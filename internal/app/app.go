package app

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	// Register client-go auth plugins (gcp, azure, oidc, exec) so a local run
	// against a kubeconfig that uses one of them can authenticate.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/homedir"
	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/internal/config"
	"github.com/aryasoni98/alertkube/internal/env"
	"github.com/aryasoni98/alertkube/internal/metrics"
	"github.com/aryasoni98/alertkube/internal/shard"
	"github.com/aryasoni98/alertkube/internal/trace"
)

const (
	sweepInterval = 30 * time.Second
	appName       = "alertkube"
)

// version is overridden at build time via
// -ldflags "-X github.com/aryasoni98/alertkube/internal/app.version=...".
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
	// watchSilenceCRD enables watching the alertkube.io Silence CRD via a
	// dynamic informer (opt-in; requires the CRD installed + RBAC).
	watchSilenceCRD bool
}

// Run is the controller entrypoint: dispatch CLI subcommands, parse flags, load
// config, then start the controller (optionally under leader election). Invoked
// by cmd/alertkube. It may call os.Exit for subcommands and fatal config errors.
func Run() {
	// Subcommands (version, validate) run without a cluster connection and must
	// be dispatched before flag.Parse so `alertkube validate --config x` is not
	// mistaken for controller flags. They own their own flag sets.
	if handled, code := dispatchSubcommand(os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}

	flags := parseFlags()
	klog.Infof("%s %s starting", appName, version)

	cfg, err := config.Load(flags.configPath)
	if err != nil {
		klog.Fatalf("config: %v", err)
	}

	// The shard identity is resolved here, before anything that depends on it:
	// the leader Lease name, the persisted-state ConfigMap name, and the
	// emit-path ownership gate all derive from it, and the first two are needed
	// before the controller body starts. A bad shard set is fatal - a replica
	// that owns nothing looks healthy while silently paging for nothing.
	sharder, err := shard.FromEnv()
	if err != nil {
		klog.Fatalf("%v", err)
	}
	if err := cfg.ApplyShardScope(sharder.Index(), sharder.Enabled()); err != nil {
		klog.Fatalf("config: %v", err)
	}
	if sharder.Enabled() {
		klog.Infof("sharding enabled: shard %d of %d (this replica owns objects where hash(kind/ns/name) mod %d == %d); leader Lease %q, state ConfigMap %q; scaling requires a rollout of %s",
			sharder.Index(), sharder.Total(), sharder.Total(), sharder.Index(),
			leaseName(sharder), cfg.Persistence.ConfigMapName, shard.EnvTotal)
	}

	// Install the shutdown signal handler before any blocking startup work:
	// buildClient can retry for up to kubeconfigRetryBudget when the
	// apiserver is unavailable at boot, and a SIGTERM during that window
	// must be honored promptly rather than waiting out the budget.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		waitForSignal()
		klog.Infof("%s received shutdown signal", appName)
		cancel()
	}()

	// Opt-in tracing. Started before the controller so producer spans exist from
	// the first watch event, and shut down last so queued spans are flushed.
	// A failure here is never fatal: losing traces must not stop alerting.
	traceShutdown := trace.Init(ctx, version)
	defer func() {
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := traceShutdown(flushCtx); err != nil {
			klog.Warningf("tracing shutdown: %v", err)
		}
		flushCancel()
	}()

	clientset := buildClient(ctx, flags.kubeconfig)

	// Optional Silence CRD watch via a dynamic client (opt-in). Built here so
	// both the leader and the direct path share one client. A build failure is
	// non-fatal: log and continue without CRD watching, mirroring cloud sources.
	var dynClient dynamic.Interface
	if flags.watchSilenceCRD {
		dc, derr := buildDynamicClient(flags.kubeconfig)
		if derr != nil {
			klog.Errorf("silence CRD watch requested but dynamic client init failed (continuing without it): %v", derr)
		} else {
			dynClient = dc
		}
	}

	// Metrics + health start outside the leader-election gate so the pod
	// can still be scraped and probed when it is a hot-standby follower. When
	// cfg.APIAddr is set, the sensitive data plane is served on its own
	// listener so the metrics/probe port can be exposed independently.
	srvs := metrics.Serve(cfg.MetricsAddr, cfg.APIAddr)

	if flags.leaderElect {
		runWithLeaderElection(ctx, clientset, dynClient, cfg, flags, sharder)
	} else {
		runController(ctx, clientset, dynClient, cfg, flags.watchNamespace, sharder)
	}

	for _, srv := range srvs {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := srv.Shutdown(shutdownCtx); err != nil {
			klog.Warningf("http server shutdown: %v", err)
		}
		shutdownCancel()
	}
}

func parseFlags() runtimeFlags {
	var f runtimeFlags
	if home := homedir.HomeDir(); home != "" {
		flag.StringVar(&f.kubeconfig, "kubeconfig", filepath.Join(home, ".kube", "config"), "kubeconfig path")
	} else {
		flag.StringVar(&f.kubeconfig, "kubeconfig", "", "kubeconfig path")
	}
	flag.StringVar(&f.configPath, "config", envConfigPath(), "YAML config path")
	flag.StringVar(&f.watchNamespace, "watch-namespace", os.Getenv("WATCH_NAMESPACE"), "restrict informers to one namespace (disables node alerts; required for namespace-scoped RBAC)")
	flag.BoolVar(&f.leaderElect, "leader-elect", env.Bool("LEADER_ELECT", false), "enable leader election via a Lease (required when replicas > 1)")
	flag.StringVar(&f.leaderElectionNS, "leader-election-namespace", env.Or("LEADER_ELECTION_NAMESPACE", "kube-system"), "namespace holding the Lease object")
	flag.StringVar(&f.leaderElectionLeaseID, "leader-election-id", os.Getenv("POD_NAME"), "lease holder identity (defaults to POD_NAME or hostname)")
	flag.BoolVar(&f.watchSilenceCRD, "watch-silence-crd", env.Bool("ALERTKUBE_WATCH_SILENCE_CRD", false), "watch the alertkube.io Silence CRD via a dynamic informer (opt-in; requires the CRD + RBAC)")
	flag.Parse()
	return f
}

func waitForSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
}

// envConfigPath is the config path from the ALERTKUBE_CONFIG env var. Shared by
// the --config flag default and the `validate` subcommand so both resolve the
// same fallback.
func envConfigPath() string { return os.Getenv("ALERTKUBE_CONFIG") }
