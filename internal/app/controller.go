package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/authz"
	"alertkube/internal/config"
	"alertkube/internal/crd"
	"alertkube/internal/group"
	"alertkube/internal/metrics"
	"alertkube/internal/persist"
	"alertkube/internal/receiver"
	"alertkube/internal/router"
	"alertkube/internal/rules"
	"alertkube/internal/silence"
	"alertkube/internal/sources"
	awssource "alertkube/internal/sources/aws"
	azuresource "alertkube/internal/sources/azure"
	gcpsource "alertkube/internal/sources/gcp"
	"alertkube/internal/watchers"
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

	// Optional Silence CRD watch: a dynamic informer keeps an in-memory store of
	// Silence CRs the router consults like file silences. The CRD's etcd is its
	// source of truth (no ConfigMap persistence). Started below on the wg so the
	// shutdown sequence drains it; a sync failure (missing CRD/RBAC) is logged
	// and the controller continues without it. nil dynClient = feature off.
	var crdSyncer *crd.Syncer
	if dynClient != nil {
		crdStore := crd.NewSilenceStore()
		crdSyncer = crd.NewSyncer(dynClient, crdStore, watchNamespace)
		r.SetCRDSilences(crdStore.List)
	}

	// Runtime silences: time-boxed mutes created from the console without a
	// redeploy. Persisted into the state ConfigMap (below) so they survive a
	// leader failover, and consulted by the router alongside config silences.
	silStore := silence.NewStore()
	r.SetRuntimeSilences(silStore)

	var grouper *group.Grouper
	if cfg.Grouping.Enabled {
		window := time.Duration(cfg.Grouping.WindowSeconds) * time.Second
		grouper = group.New(window, cfg.Grouping.By, func(s *alert.Alert) {
			// Summaries never go to stateful incident sinks: those dedupe
			// storms by fingerprint themselves, and a summary incident has
			// no resolve to close it.
			route := dropStateful(r.Route(s))
			if len(route) == 0 {
				return
			}
			dispatch(reg, s, route)
		})
	}

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
		dispatch(reg, a, route)
	}

	store := alert.NewStore(
		time.Duration(cfg.Behavior.MuteSeconds)*time.Second,
		time.Duration(cfg.Behavior.ResolveTTLSeconds)*time.Second,
		dispatchResolved,
	)
	store.SetOnChange(func(n int) {
		metrics.ActiveAlerts.Set(float64(n))
		// Notify any connected consoles (SSE) so they refresh live instead of
		// waiting for their poll interval.
		metrics.PublishChange()
	})

	// Restore persisted state before the informers start so the initial
	// sync sees the prior mute history and pending resolves survive the
	// restart instead of leaving PagerDuty incidents dangling.
	var persister *persist.ConfigMapStore
	if cfg.Persistence.Enabled {
		persister = persist.NewConfigMapStore(clientset, cfg.Persistence.Namespace, cfg.Persistence.ConfigMapName)
		loadCtx, loadCancel := context.WithTimeout(ctx, 10*time.Second)
		snap, err := persister.Load(loadCtx)
		loadCancel()
		switch {
		case err != nil:
			klog.Warningf("state restore failed (starting cold): %v", err)
		case snap != nil:
			store.Restore(snap)
			silStore.Replace(snap.RuntimeSilences)
			klog.Infof("restored state: %d active alerts, %d mute records, %d runtime silences (saved %s)",
				len(snap.Active), len(snap.LastSent), len(snap.RuntimeSilences), snap.SavedAt.Format(time.RFC3339))
		}
	}

	// The rule engine observes the firing stream and emits derived alerts back
	// through emit. ruleEngine is captured by emit's observe callback (assigned
	// just below), and derived alerts are tagged KindDerived so Observe ignores
	// them - no feedback loop.
	var ruleEngine *rules.Engine
	emit := makeEmitter(store, r, reg, cfg, grouper, func(a *alert.Alert) {
		if ruleEngine != nil {
			ruleEngine.Observe(a)
		}
	})
	ruleEngine = rules.New(cfg.Rules, emit)

	// Console + control-plane HTTP handlers. The read token guards reads; the
	// write path is fail-closed (see newWriteGate). ALERTKUBE_AUTH_MODE selects
	// token mode (shared ALERTKUBE_API_WRITE_TOKEN, default) or rbac mode
	// (per-request Kubernetes TokenReview + SubjectAccessReview). Handlers live
	// in console.go so they are unit-testable.
	apiToken := os.Getenv("ALERTKUBE_API_TOKEN")
	if apiToken == "" {
		klog.Warningf("/api/alerts on %s is UNAUTHENTICATED and exposes active alert contents; set ALERTKUBE_API_TOKEN (helm: api.token) or restrict the port with a NetworkPolicy", cfg.MetricsAddr)
	}
	writeToken := os.Getenv("ALERTKUBE_API_WRITE_TOKEN")
	var rbacAuth *authz.RBACAuthorizer
	if strings.ToLower(os.Getenv("ALERTKUBE_AUTH_MODE")) == "rbac" {
		rbacAuth = authz.NewRBACAuthorizer(clientset)
		klog.Infof("console write auth: rbac mode (TokenReview + SubjectAccessReview); writes require a Kubernetes token authorized for the alertkube.io resources - ALERTKUBE_API_WRITE_TOKEN is ignored")
	} else if writeToken == "" {
		klog.Infof("console write auth: token mode, but no ALERTKUBE_API_WRITE_TOKEN set - runtime writes are DISABLED (403). Set api.writeToken, or api.authMode=rbac.")
	} else {
		klog.Infof("console write auth: token mode (shared ALERTKUBE_API_WRITE_TOKEN)")
	}
	// Phase 2b (opt-in): Secret-reference channel testing. Off unless
	// ALERTKUBE_ALLOW_SECRET_READ=true, which (via the chart) also grants the
	// controller secrets:get in its own namespace. The reader is namespace- and
	// key-scoped and never returns the value to a client.
	secretRead := strings.EqualFold(os.Getenv("ALERTKUBE_ALLOW_SECRET_READ"), "true")
	var secretReader func(context.Context, string, string) (string, error)
	if secretRead {
		ns := os.Getenv("POD_NAMESPACE")
		if ns == "" {
			klog.Warningf("ALERTKUBE_ALLOW_SECRET_READ is set but POD_NAMESPACE is empty; Secret-reference channel testing stays disabled")
			secretRead = false
		} else {
			secretReader = func(ctx context.Context, name, key string) (string, error) {
				s, err := clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					return "", err
				}
				b, ok := s.Data[key]
				if !ok {
					return "", fmt.Errorf("key %q not in secret %q", key, name)
				}
				return string(b), nil
			}
			klog.Infof("Secret-reference channel testing ENABLED (api.allowSecretRead): the controller may read Secrets in namespace %q to test channel credentials", ns)
		}
	}

	installConsoleHandlers(consoleDeps{
		apiToken:     apiToken,
		writeGate:    newWriteGate(writeToken, rbacAuth),
		cfg:          cfg,
		store:        store,
		silStore:     silStore,
		reg:          reg,
		secretRead:   secretRead,
		secretReader: secretReader,
	})

	setupReceiver(cfg, store, emit, dispatchResolved)

	ws := startInformers(ctx, clientset, cfg, watchNamespace, emit)

	var wg sync.WaitGroup
	wg.Add(1)
	go runSweeper(ctx, &wg, store, silStore, persister, reg, cfg)

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

	// Optional cloud sources (AWS/Azure/GCP) run beside the informers on the
	// same emit pipeline. Each provider's construction can fail (credentials/
	// config), but that must never take down the Kubernetes watchers: log and
	// continue without that provider.
	startCloud := func(name string, pollSeconds int, build func(context.Context) ([]sources.Source, error)) {
		srcs, err := build(ctx)
		if err != nil {
			klog.Errorf("%s sources disabled (continuing without them): %v", name, err)
			return
		}
		if len(srcs) == 0 {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sources.Run(ctx, time.Duration(pollSeconds)*time.Second, emit, srcs...)
		}()
		klog.Infof("%s sources enabled: %d source(s), polling every %ds", name, len(srcs), pollSeconds)
	}
	if cfg.AWS.Enabled {
		startCloud("aws", cfg.AWS.PollSeconds, func(c context.Context) ([]sources.Source, error) { return awssource.NewProvider(c, cfg) })
	}
	if cfg.Azure.Enabled {
		startCloud("azure", cfg.Azure.PollSeconds, func(c context.Context) ([]sources.Source, error) { return azuresource.NewProvider(c, cfg) })
	}
	if cfg.GCP.Enabled {
		startCloud("gcp", cfg.GCP.PollSeconds, func(c context.Context) ([]sources.Source, error) { return gcpsource.NewProvider(c, cfg) })
	}
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
	shutdown(ws, grouperStop, &wg, persister, store, silStore)
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

// exportState builds the durable snapshot: alert-store state plus the runtime
// silences, which live outside the store but share the same state ConfigMap so
// a UI-created mute survives a leader failover.
func exportState(store *alert.Store, sil *silence.Store) *alert.Snapshot {
	snap := store.Export()
	snap.RuntimeSilences = sil.List()
	return snap
}

// overlayConfig renders a candidate config: it parses a JSON patch of the
// form-editable sections (rules, routing, grouping) and overlays them onto a
// copy of the live config, returning the full YAML. Only provided sections are
// replaced; every other field is preserved, so form-based authoring never drops
// config the UI does not model. Nothing is applied - the result is for the
// operator to review, diff, and commit to Git (the GitOps source of truth).
func overlayConfig(base *config.Config, patch []byte) ([]byte, error) {
	var in struct {
		Rules    *[]config.Rule  `json:"rules"`
		Routing  *[]config.Route `json:"routing"`
		Grouping *struct {
			Enabled       bool     `json:"enabled"`
			WindowSeconds int      `json:"windowSeconds"`
			By            []string `json:"by"`
		} `json:"grouping"`
	}
	if err := json.Unmarshal(patch, &in); err != nil {
		return nil, err
	}
	out := *base // shallow copy; section assignments below replace slice/struct headers, never mutating base
	if in.Rules != nil {
		out.Rules = *in.Rules
	}
	if in.Routing != nil {
		out.Routing = *in.Routing
	}
	if in.Grouping != nil {
		out.Grouping.Enabled = in.Grouping.Enabled
		out.Grouping.WindowSeconds = in.Grouping.WindowSeconds
		out.Grouping.By = in.Grouping.By
	}
	return yaml.Marshal(&out)
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
// windows, wait for the sweeper + grouper goroutines, save final state on a
// fresh deadline (ctx is already cancelled), and finally mark not-ready.
func shutdown(ws []watchers.Watcher, grouperStop func(), wg *sync.WaitGroup, persister *persist.ConfigMapStore, store *alert.Store, silStore *silence.Store) {
	// Stop serving the leader-scoped routes first. The HTTP server outlives
	// leader election, so a demoted leader would otherwise keep accepting
	// receiver POSTs (202) into the store we are about to abandon - silently
	// dropping them - and keep dumping a stale active set on /api/alerts.
	// 503 makes both fail loudly until the next leader reinstalls them.
	metrics.ClearReceiverHandler()
	metrics.ClearAlertsHandler()
	metrics.ClearConfigHandler()
	metrics.ClearValidateHandler()
	metrics.ClearRenderHandler()
	metrics.ClearSilencesHandler()
	metrics.ClearChannelsHandler()
	metrics.ClearEventsAuth()
	drainWatchers(ws, enrichDrainTimeout)
	grouperStop()
	wg.Wait()
	if persister != nil {
		saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := persister.Save(saveCtx, exportState(store, silStore)); err != nil {
			klog.Warningf("final state save: %v", err)
		}
		saveCancel()
	}
	metrics.MarkNotReady()
}
