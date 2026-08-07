package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/authz"
	"alertkube/internal/config"
	"alertkube/internal/crd"
	"alertkube/internal/env"
	"alertkube/internal/group"
	"alertkube/internal/metrics"
	"alertkube/internal/persist"
	"alertkube/internal/receiver"
	"alertkube/internal/router"
	"alertkube/internal/rules"
	"alertkube/internal/shard"
	"alertkube/internal/silence"
	"alertkube/internal/sources"
	"alertkube/internal/watchers"

	// Cloud providers self-register into the sources registry via init; the
	// blank imports pull them in so startCloudSources can iterate the registry.
	_ "alertkube/internal/sources/aws"
	_ "alertkube/internal/sources/azure"
	_ "alertkube/internal/sources/gcp"
)

// informerResyncPeriod is how often cached objects are re-delivered as
// synthetic Update events. Kept below the resolveTTL/mute windows so a
// still-firing standing condition is re-touched before its TTL elapses;
// config.Validate rejects configs that violate that relationship, and the
// resync rationale is at the factory construction site. Sourced from the
// config package so the validation and the runtime use one number.
const informerResyncPeriod = config.InformerResyncSeconds * time.Second

// runController wires the watchers, sweeper, and dispatch path.
// Returns when ctx is cancelled (signal received OR leader election lost).
// A non-empty watchNamespace scopes every informer to that namespace and
// disables the node watcher (nodes are cluster-scoped), so the controller
// runs under a namespace Role instead of a ClusterRole.
// controllerRuns counts entries into runController. With leader election a
// single process can win, lose, and re-win the lease without exiting, so this
// body runs more than once per process. Each run rebuilds the informer
// factory, store, and grouper from scratch and re-applies the startup grace
// window to the fresh informer sync; the counter makes that re-entrancy
// observable in the logs (e.g. when diagnosing leader flap).
var controllerRuns atomic.Uint64

func runController(ctx context.Context, clientset kubernetes.Interface, dynClient dynamic.Interface, cfg *config.Config, watchNamespace string) {
	if n := controllerRuns.Add(1); n > 1 {
		klog.Infof("controller starting (leadership acquisition #%d): rebuilding informers/store/grouper; startup grace re-applies to this sync", n)
	}
	reg := buildSinks(cfg)
	r := router.New(cfg.Routing, cfg.Inhibitions, cfg.Silences, []string{"slack"})
	r.SetDisableAnnotationSilences(cfg.Behavior.DisableAnnotationSilences)
	r.SetMaintenance(cfg.Maintenance)

	// Delivery runs on a bounded worker pool, decoupled from the informer,
	// receiver, source, and sweeper goroutines that produce alerts: they
	// enqueue near-instantly while workers perform the blocking sink fan-out,
	// so a slow sink can never stall Kubernetes event processing.
	disp := newDispatcher(reg, dispatchWorkers(), dispatchQueueSize())
	// Capture permanently-abandoned deliveries (exhausted resolves, failed
	// fire-once alerts) so they surface on /api/deadletter + the metric instead
	// of vanishing into a log line.
	deadLetter := newDeadLetterLog()
	disp.SetDeadLetter(deadLetter.Record)
	disp.Start()

	crdSyncer := setupCRDSilences(dynClient, watchNamespace, r)

	// Runtime silences: time-boxed mutes created from the console without a
	// redeploy. Persisted into the state ConfigMap (below) so they survive a
	// leader failover, and consulted by the router alongside config silences.
	silStore := silence.NewStore()
	r.SetRuntimeSilences(silStore)

	grouper := buildGrouper(cfg, r, disp.enqueue)

	// dispatchResolved handles both the store's TTL-based synthetic
	// resolves and resolves ingested by the webhook receiver.
	dispatchResolved := func(a *alert.Alert) {
		route := r.Route(a)
		if route == nil {
			return
		}
		if grouper != nil && !grouper.Offer(a) {
			metrics.AlertsSuppressed.WithLabelValues("grouped").Inc()
			// Absorbed resolves still must close their incidents:
			// stateful sinks key on the member fingerprint.
			route = keepStateful(route)
			if len(route) == 0 {
				return
			}
		}
		disp.enqueue(a, route, nil)
	}

	store := alert.NewStore(
		time.Duration(cfg.Behavior.MuteSeconds)*time.Second,
		time.Duration(cfg.Behavior.ResolveTTLSeconds)*time.Second,
		dispatchResolved,
	)
	store.SetOnChange(func(n int) {
		metrics.ActiveAlerts.Set(float64(n))
	})

	persister := restoreState(ctx, clientset, cfg, store, silStore, disp)

	// The rule engine observes the firing stream and emits derived alerts back
	// through emit. ruleEngine is captured by emit's observe callback (assigned
	// just below), and derived alerts are tagged KindDerived so Observe ignores
	// them - no feedback loop.
	var ruleEngine *rules.Engine
	emit := makeEmitter(store, r, disp.enqueue, cfg, grouper, func(a *alert.Alert) {
		if ruleEngine != nil {
			ruleEngine.Observe(a)
		}
	})
	ruleEngine = rules.New(cfg.Rules, emit)

	installConsoleHandlers(buildConsoleDeps(clientset, cfg, store, silStore, reg, deadLetter))

	setupReceiver(cfg, store, emit, dispatchResolved)

	// Static hash-based sharding (A2): with ALERTKUBE_SHARD_TOTAL > 1, this
	// replica only acts on objects it owns. The gate wraps the high-volume
	// producer paths (watchers + cloud sources); the receiver and rule engine
	// keep the ungated emit (received alerts are handled by whichever replica
	// gets them, and derived alerts are low volume). Default (total=1) = no-op.
	sharder := buildSharder()
	shardedEmit := shardGate(emit, sharder)

	ws := startInformers(ctx, clientset, cfg, watchNamespace, shardedEmit)

	var wg sync.WaitGroup
	wg.Add(1)
	go runSweeper(ctx, &wg, store, silStore, persister, disp, cfg)

	// The grouper runs on its own cancel (not the controller ctx) so the
	// shutdown sequence can finish in-flight enrichment - which may still
	// Offer alerts into open windows - BEFORE those windows are flushed.
	// Tying it to ctx would race the FlushAll against the enrichment drain.
	grouperCtx, grouperStop := context.WithCancel(context.Background())
	defer grouperStop()
	if grouper != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			grouper.Run(grouperCtx) // FlushAll on grouperStop drains open windows
		}()
	}

	startCloudSources(ctx, &wg, cfg, shardedEmit)

	if ruleEngine.Enabled() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ruleEngine.Run(ctx) // evaluates Absent (heartbeat) rules on a timer
		}()
		klog.Infof("rule engine enabled: %d rule(s)", len(cfg.Rules))
	}
	if crdSyncer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A sync failure (CRD not installed / missing RBAC) is logged once;
			// the controller keeps running without CRD-backed silences.
			if err := crdSyncer.Run(ctx); err != nil {
				klog.Errorf("silence CRD watch disabled (continuing without it): %v", err)
			}
		}()
		klog.Infof("silence CRD watch enabled")
	}

	<-ctx.Done()
	klog.Infof("%s shutting down", appName)
	shutdown(ws, grouperStop, &wg, disp, persister, store, silStore)
}

// buildSharder configures static hash-based sharding from the environment.
// ALERTKUBE_SHARD_TOTAL <= 1 (default) disables it (this replica owns
// everything, unchanged single-replica behavior). When enabled,
// ALERTKUBE_SHARD_INDEX must be in [0, total); an invalid pair is fatal so a
// misconfigured shard set fails fast rather than silently owning nothing/all.
func buildSharder() *shard.Sharder {
	total := env.IntOr("ALERTKUBE_SHARD_TOTAL", 1)
	index := env.IntOr("ALERTKUBE_SHARD_INDEX", 0)
	s, ok := shard.New(index, total)
	if !ok {
		klog.Fatalf("invalid sharding config: ALERTKUBE_SHARD_INDEX=%d must be in [0,%d)", index, total)
	}
	if s.Enabled() {
		klog.Infof("sharding enabled: shard %d of %d (this replica owns objects where hash(kind/ns/name) mod %d == %d); scaling requires a rollout of ALERTKUBE_SHARD_TOTAL", s.Index(), s.Total(), s.Total(), s.Index())
	}
	return s
}

// shardGate wraps emit so only alerts for objects this replica owns proceed;
// foreign-shard alerts are dropped (another replica owns them). Applied to the
// watcher and cloud-source paths only. Returns emit unchanged when sharding is
// disabled, so a single replica has zero overhead.
func shardGate(emit watchers.Emit, s *shard.Sharder) watchers.Emit {
	if !s.Enabled() {
		return emit
	}
	return func(a *alert.Alert) {
		if s.Owns(shardKey(a)) {
			emit(a)
			return
		}
		metrics.AlertsSuppressed.WithLabelValues("foreign_shard").Inc()
	}
}

// shardKey is the per-object ownership key: identity only (kind/namespace/name),
// NOT the fingerprint - so every reason on an object, and its delete-resolve,
// are owned by the same replica (a fingerprint includes the reason and would
// split one object's alerts across shards).
func shardKey(a *alert.Alert) string {
	return string(a.Kind) + "/" + a.Namespace + "/" + a.Name
}

// setupCRDSilences wires the optional Silence CRD watch: a dynamic informer
// keeps an in-memory store of Silence CRs the router consults like file
// silences. The CRD's etcd is its source of truth (no ConfigMap persistence).
// Returns nil when the feature is off (nil dynClient); the caller starts the
// syncer on the shutdown WaitGroup so the drain sequence covers it.
func setupCRDSilences(dynClient dynamic.Interface, watchNamespace string, r *router.Router) *crd.Syncer {
	if dynClient == nil {
		return nil
	}
	crdStore := crd.NewSilenceStore()
	r.SetCRDSilences(crdStore.List)
	return crd.NewSyncer(dynClient, crdStore, watchNamespace)
}

// buildGrouper constructs the optional alert grouper, or nil when grouping is
// disabled. Flushed summaries route like alerts but never go to stateful
// incident sinks: those dedupe storms by fingerprint themselves, and a summary
// incident has no resolve to close it.
func buildGrouper(cfg *config.Config, r *router.Router, enqueue enqueueFunc) *group.Grouper {
	if !cfg.Grouping.Enabled {
		return nil
	}
	window := time.Duration(cfg.Grouping.WindowSeconds) * time.Second
	return group.New(window, cfg.Grouping.By, func(s *alert.Alert) {
		route := dropStateful(r.Route(s))
		if len(route) == 0 {
			return
		}
		enqueue(s, route, nil)
	})
}

// restoreState loads the persisted snapshot into the alert store and
// runtime-silence store before the informers start, so the initial sync sees
// the prior mute history and pending resolves survive the restart instead of
// leaving PagerDuty incidents dangling. Returns the persister for the sweeper
// and the final shutdown save, or nil when persistence is disabled. A load
// failure starts cold rather than blocking startup.
func restoreState(ctx context.Context, clientset kubernetes.Interface, cfg *config.Config, store *alert.Store, silStore *silence.Store, disp *dispatcher) *persist.ConfigMapStore {
	if !cfg.Persistence.Enabled {
		return nil
	}
	persister := persist.NewConfigMapStore(clientset, cfg.Persistence.Namespace, cfg.Persistence.ConfigMapName)
	loadCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snap, err := persister.Load(loadCtx)
	switch {
	case err != nil:
		klog.Warningf("state restore failed (starting cold): %v", err)
	case snap != nil:
		store.Restore(snap)
		silStore.Replace(snap.RuntimeSilences)
		// Replay the durable outbox so deliveries that were enqueued but not
		// acknowledged before the restart resume instead of being lost.
		replayed := disp.ReplayPending(snap.Pending)
		klog.Infof("restored state: %d active alerts, %d mute records, %d runtime silences, %d pending deliveries replayed (saved %s)",
			len(snap.Active), len(snap.LastSent), len(snap.RuntimeSilences), replayed, snap.SavedAt.Format(time.RFC3339))
	}
	return persister
}

// startCloudSources builds each enabled cloud provider (AWS/Azure/GCP) and
// starts its polling loop beside the informers on the same emit pipeline. A
// provider whose construction fails (credentials/config) is logged and
// skipped - a cloud-auth problem must never take down the Kubernetes watchers.
func startCloudSources(ctx context.Context, wg *sync.WaitGroup, cfg *config.Config, emit watchers.Emit) {
	for _, p := range sources.Providers() {
		if !p.Enabled(cfg) {
			continue
		}
		srcs, err := p.Build(ctx, cfg)
		if err != nil {
			klog.Errorf("%s sources disabled (continuing without them): %v", p.Name, err)
			continue
		}
		if len(srcs) == 0 {
			continue
		}
		poll := p.PollSeconds(cfg)
		wg.Add(1)
		go func(name string, srcs []sources.Source, poll int) {
			defer wg.Done()
			sources.Run(ctx, time.Duration(poll)*time.Second, emit, srcs...)
		}(p.Name, srcs, poll)
		klog.Infof("%s sources enabled: %d source(s), polling every %ds", p.Name, len(srcs), poll)
	}
}

// writeAuthorized gates a control-plane mutation. It fails closed: an empty
// write token means runtime mutation is disabled entirely (403), so a default
// install never exposes a write path. With a token set, the request must carry
// it as a bearer (constant-time compared). It writes the rejection response and
// returns false when not authorized.
func writeAuthorized(req *http.Request, writeToken string, w http.ResponseWriter) bool {
	if writeToken == "" {
		httpErr(w, http.StatusForbidden, "runtime mutation is disabled: set ALERTKUBE_API_WRITE_TOKEN (helm: api.writeToken)")
		return false
	}
	if !authz.BearerEqual(req.Header.Get("Authorization"), writeToken) {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

// httpErr writes a small JSON error body with the given status.
func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// sanitizeField strips control characters (defeating log injection from the
// comment / user header, which are echoed into klog) and bounds the length.
func sanitizeField(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n', r == '\r', r == '\t':
			return ' '
		case r < 0x20:
			return -1
		default:
			return r
		}
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.TrimSpace(s)
}

// exportState builds the durable snapshot: alert-store state, the runtime
// silences (which live outside the store but share the state ConfigMap so a
// runtime mute survives a leader failover), and the dispatcher's outbox
// (undelivered deliveries, replayed on restart).
func exportState(store *alert.Store, sil *silence.Store, disp *dispatcher) *alert.Snapshot {
	snap := store.Export()
	snap.RuntimeSilences = sil.List()
	snap.Pending = disp.PendingSnapshot()
	return snap
}

// setupReceiver wires the optional Alertmanager-compatible webhook receiver
// onto the metrics server. Fails closed when enabled without a bearer token
// unless receiver.allowAnonymous is set.
func setupReceiver(cfg *config.Config, store *alert.Store, emit watchers.Emit, dispatchResolved func(*alert.Alert)) {
	if !cfg.Receiver.Enabled {
		return
	}
	receiverToken := os.Getenv("ALERTKUBE_RECEIVER_TOKEN")
	switch {
	case receiverToken == "" && !cfg.Receiver.AllowAnonymous:
		// Fail closed: an open POST /api/v1/alerts lets anyone with
		// network reach inject arbitrary alerts (and resolves that close
		// real incidents). Require an explicit opt-in to run without auth.
		klog.Fatalf("receiver.enabled but no bearer token: POST /api/v1/alerts on %s would accept unauthenticated alert injection. Set ALERTKUBE_RECEIVER_TOKEN (helm: receiver.token), or set receiver.allowAnonymous: true if the port is restricted by a NetworkPolicy", cfg.MetricsAddr)
	case receiverToken == "":
		klog.Warningf("receiver enabled with receiver.allowAnonymous: POST /api/v1/alerts on %s accepts UNAUTHENTICATED alert injection - ensure the port is restricted by a NetworkPolicy", cfg.MetricsAddr)
	}
	metrics.SetReceiverHandler(receiver.New(
		receiverToken,
		func(a *alert.Alert) { emit(a) },
		func(a *alert.Alert) {
			// Upstream already told the world it resolved; forget our
			// copy so the TTL sweep does not emit a duplicate resolve.
			store.Forget(a.Fingerprint)
			dispatchResolved(a)
		},
	))
	klog.Infof("alertmanager-compatible receiver enabled on %s/api/v1/alerts", cfg.MetricsAddr)
}

// startInformers builds the shared informer factory, wires every watcher,
// starts the informers, and blocks until their caches sync (fatal on failure,
// which is almost always missing RBAC). Returns the watchers so the caller can
// drain their background work at shutdown.
func startInformers(ctx context.Context, clientset kubernetes.Interface, cfg *config.Config, watchNamespace string, emit watchers.Emit) []watchers.Watcher {
	var factoryOpts []informers.SharedInformerOption
	if watchNamespace != "" {
		klog.Infof("watching single namespace %q (node alerts disabled)", watchNamespace)
		factoryOpts = append(factoryOpts, informers.WithNamespace(watchNamespace))
	}
	// A non-zero resync re-delivers every cached object as a synthetic
	// Update on a fixed period. This re-evaluates standing conditions so a
	// stuck-but-stable problem (e.g. a Deployment with unavailable replicas
	// whose status stops changing) keeps its alert alive via store.Touch
	// instead of false-resolving when its resolveTTL elapses with no real
	// watch event. Kept under the default resolveTTL/mute window (600s) so
	// the re-fire lands before expiry; resync re-fires are muted, not paged.
	factory := informers.NewSharedInformerFactoryWithOptions(clientset, informerResyncPeriod, factoryOpts...)
	ws := buildWatchers(clientset, cfg, watchNamespace)
	for _, w := range ws {
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
	return ws
}

// shutdown runs the controller's drain sequence in the one order that does
// not drop alerts: finish in-flight pod enrichment first (those alerts must
// reach the store and grouper), then stop the grouper so it flushes open
// windows, wait for the sweeper + grouper goroutines, drain the dispatch queue
// so every enqueued alert is actually delivered, save final state on a fresh
// deadline (ctx is already cancelled), and finally mark not-ready.
//
// The dispatcher is drained after wg.Wait so every producer (enrichment,
// grouper flush, sweeper escalations, cloud sources, rules) has stopped
// enqueuing before the queue is closed, and before the final save so a
// delivery failure's dedupe rollback is reflected in the saved snapshot.
func shutdown(ws []watchers.Watcher, grouperStop func(), wg *sync.WaitGroup, disp *dispatcher, persister *persist.ConfigMapStore, store *alert.Store, silStore *silence.Store) {
	// Stop serving the leader-scoped routes first. The HTTP server outlives
	// leader election, so a demoted leader would otherwise keep accepting
	// receiver POSTs (202) into the store we are about to abandon - silently
	// dropping them - and keep dumping a stale active set on /api/alerts.
	// 503 makes both fail loudly until the next leader reinstalls them.
	metrics.ClearReceiverHandler()
	metrics.ClearAlertsHandler()
	metrics.ClearConfigHandler()
	metrics.ClearValidateHandler()
	metrics.ClearSilencesHandler()
	metrics.ClearChannelsHandler()
	metrics.ClearDeadLetterHandler()
	metrics.ClearPprofHandler()
	drainWatchers(ws, enrichDrainTimeout)
	grouperStop()
	wg.Wait()
	// All producers have stopped; drain the queued deliveries before the final
	// save so nothing enqueued during the drain is abandoned.
	disp.Shutdown()
	if persister != nil {
		saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := persister.Save(saveCtx, exportState(store, silStore, disp)); err != nil {
			klog.Warningf("final state save: %v", err)
		}
		saveCancel()
	}
	metrics.MarkNotReady()
}
