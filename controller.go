package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/authz"
	"alertkube/internal/config"
	"alertkube/internal/group"
	"alertkube/internal/metrics"
	"alertkube/internal/persist"
	"alertkube/internal/receiver"
	"alertkube/internal/router"
	"alertkube/internal/rules"
	"alertkube/internal/silence"
	"alertkube/internal/sinks"
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

func runController(ctx context.Context, clientset kubernetes.Interface, cfg *config.Config, watchNamespace string) {
	if n := controllerRuns.Add(1); n > 1 {
		klog.Infof("controller starting (leadership acquisition #%d): rebuilding informers/store/grouper; startup grace re-applies to this sync", n)
	}
	reg := buildSinks(cfg)
	r := router.New(cfg.Routing, cfg.Inhibitions, cfg.Silences, []string{"slack"})
	r.SetDisableAnnotationSilences(cfg.Behavior.DisableAnnotationSilences)

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
	store.SetOnChange(func(n int) { metrics.ActiveAlerts.Set(float64(n)) })

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

	// Read-only view of the active set + recent history for dashboards and
	// debugging. It dumps alert contents (names, namespaces, summaries), so
	// an optional bearer token guards it: set ALERTKUBE_API_TOKEN and the
	// endpoint requires `Authorization: Bearer <token>`. Without a token it
	// stays open (current behavior) - lock the port down with the chart's
	// NetworkPolicy instead.
	apiToken := os.Getenv("ALERTKUBE_API_TOKEN")
	if apiToken == "" {
		klog.Warningf("/api/alerts on %s is UNAUTHENTICATED and exposes active alert contents; set ALERTKUBE_API_TOKEN (helm: api.token) or restrict the port with a NetworkPolicy", cfg.MetricsAddr)
	}
	metrics.SetAlertsHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if apiToken != "" && !authz.BearerEqual(req.Header.Get("Authorization"), apiToken) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": store.ActiveList(),
			"recent": store.Recent(),
		})
	}))

	// GET /api/config: a read-only snapshot of the loaded config for the
	// console (rules, grouping, routing, channels, silences, source toggles).
	// Re-marshalling through YAML keys the JSON by the same yaml tags the file
	// uses (so the UI and the config look identical) and yields a plain string
	// dump for the raw view. The config holds no secrets - sink credentials are
	// env/Secrets, never in the YAML - but it is gated by the same apiToken as
	// /api/alerts because it still exposes the alerting topology.
	metrics.SetConfigHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if apiToken != "" && !authz.BearerEqual(req.Header.Get("Authorization"), apiToken) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, err := yaml.Marshal(cfg)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var m map[string]any
		_ = yaml.Unmarshal(raw, &m)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"config": m,
			"yaml":   string(raw),
		})
	}))

	// POST /api/config/validate: runs the startup validator against a candidate
	// YAML body and returns {ok|error}. Nothing is applied - this is the fast
	// feedback loop for authoring a change before committing it to Git/ConfigMap
	// (Phase 1). Body is capped to bound a hostile request.
	metrics.SetValidateHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if apiToken != "" && !authz.BearerEqual(req.Header.Get("Authorization"), apiToken) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if req.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if verr := config.ParseAndValidate(body); verr != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": verr.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))

	// Write-path authorization. Two modes, selected by ALERTKUBE_AUTH_MODE:
	//   token (default) - a single shared ALERTKUBE_API_WRITE_TOKEN, fail closed:
	//     with no token set every mutation is rejected, so the default install
	//     keeps its read-only posture.
	//   rbac            - the bearer token is a real Kubernetes token, validated
	//     by TokenReview and authorized by SubjectAccessReview against the
	//     alertkube.io synthetic resources, so audit records a real username and
	//     access is managed with standard RBAC.
	// writeGate authorizes one write request, writes the failure response itself,
	// and returns the acting username (for audit) plus an ok flag.
	writeToken := os.Getenv("ALERTKUBE_API_WRITE_TOKEN")
	authMode := strings.ToLower(os.Getenv("ALERTKUBE_AUTH_MODE"))
	var rbacAuth *authz.RBACAuthorizer
	if authMode == "rbac" {
		rbacAuth = authz.NewRBACAuthorizer(clientset)
		klog.Infof("console write auth: rbac mode (TokenReview + SubjectAccessReview); writes require a Kubernetes token authorized for the alertkube.io resources - ALERTKUBE_API_WRITE_TOKEN is ignored")
	} else {
		authMode = "token"
		if writeToken == "" {
			klog.Infof("console write auth: token mode, but no ALERTKUBE_API_WRITE_TOKEN set - runtime writes are DISABLED (403). Set api.writeToken, or api.authMode=rbac.")
		} else {
			klog.Infof("console write auth: token mode (shared ALERTKUBE_API_WRITE_TOKEN)")
		}
	}
	writeGate := func(req *http.Request, attr authz.ResourceAttributes, w http.ResponseWriter) (string, bool) {
		if rbacAuth != nil {
			token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
			if token == "" {
				httpErr(w, http.StatusUnauthorized, "a Kubernetes bearer token is required")
				return "", false
			}
			user, allowed, err := rbacAuth.Authorize(req.Context(), token, attr)
			if err != nil {
				klog.Warningf("authz check failed (%s %s.%s): %v", attr.Verb, attr.Resource, attr.Group, err)
				httpErr(w, http.StatusServiceUnavailable, "authorization check failed")
				return "", false
			}
			if !allowed {
				httpErr(w, http.StatusForbidden, "not authorized to "+attr.Verb+" "+attr.Resource+"."+attr.Group)
				return user, false
			}
			return user, true
		}
		if !writeAuthorized(req, writeToken, w) {
			return "", false
		}
		// Token mode has no real identity; fall back to the best-effort header.
		if h := sanitizeField(req.Header.Get("X-Alertkube-User")); h != "" {
			return h, true
		}
		return "shared-token", true
	}

	metrics.SetSilencesHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			if apiToken != "" && !authz.BearerEqual(req.Header.Get("Authorization"), apiToken) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"runtime": silStore.List()})

		case http.MethodPost:
			user, ok := writeGate(req, authz.ResourceAttributes{Group: "alertkube.io", Resource: "silences", Verb: "create"}, w)
			if !ok {
				return
			}
			body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024))
			if err != nil {
				httpErr(w, http.StatusBadRequest, "read body")
				return
			}
			var in struct {
				Matchers map[string]string `json:"matchers"`
				Until    string            `json:"until"`
				Comment  string            `json:"comment"`
			}
			if err := json.Unmarshal(body, &in); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			if len(in.Matchers) == 0 {
				httpErr(w, http.StatusBadRequest, "at least one matcher is required")
				return
			}
			until, err := time.Parse(time.RFC3339, in.Until)
			if err != nil {
				httpErr(w, http.StatusBadRequest, "until must be an RFC3339 timestamp")
				return
			}
			if !until.After(time.Now()) {
				httpErr(w, http.StatusBadRequest, "until must be in the future")
				return
			}
			sil := silStore.Add(silence.Silence{
				Matchers:  in.Matchers,
				Until:     until,
				Comment:   sanitizeField(in.Comment),
				CreatedBy: user,
			})
			metrics.RuntimeMutations.WithLabelValues("silence_create").Inc()
			klog.Infof("runtime silence created: id=%s matchers=%v until=%s by=%q",
				sil.ID, sil.Matchers, sil.Until.Format(time.RFC3339), sil.CreatedBy)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(sil)

		case http.MethodDelete:
			user, ok := writeGate(req, authz.ResourceAttributes{Group: "alertkube.io", Resource: "silences", Verb: "delete"}, w)
			if !ok {
				return
			}
			id := strings.TrimPrefix(req.URL.Path, "/api/silences/")
			if id == "" || strings.Contains(id, "/") {
				httpErr(w, http.StatusBadRequest, "silence id required: DELETE /api/silences/{id}")
				return
			}
			if !silStore.Delete(id) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			metrics.RuntimeMutations.WithLabelValues("silence_delete").Inc()
			klog.Infof("runtime silence deleted: id=%s by=%q", id, user)
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	// Channels: GET /api/channels lists configured sinks (read token); POST
	// /api/channels/test fires one synthetic alert through a named sink so an
	// operator can confirm a channel is wired. The test-fire is outward-facing
	// (it sends a REAL notification) and gated by the write token, fail closed.
	// It reuses the sink's already-loaded credentials - no Secret read, so the
	// zero-secrets-read posture is unchanged. Adding a brand-new channel from a
	// Secret reference (which would need RBAC) is deliberately out of scope here.
	metrics.SetChannelsHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/api/channels":
			if apiToken != "" && !authz.BearerEqual(req.Header.Get("Authorization"), apiToken) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"channels": reg.Names()})

		case req.Method == http.MethodPost && req.URL.Path == "/api/channels/test":
			user, ok := writeGate(req, authz.ResourceAttributes{Group: "alertkube.io", Resource: "channels", Verb: "create"}, w)
			if !ok {
				return
			}
			body, err := io.ReadAll(io.LimitReader(req.Body, 8*1024))
			if err != nil {
				httpErr(w, http.StatusBadRequest, "read body")
				return
			}
			var in struct {
				Sink string `json:"sink"`
			}
			if err := json.Unmarshal(body, &in); err != nil {
				httpErr(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			if in.Sink == "" {
				httpErr(w, http.StatusBadRequest, "sink is required")
				return
			}
			if !reg.Has(in.Sink) {
				httpErr(w, http.StatusBadRequest, "unknown sink: "+sanitizeField(in.Sink))
				return
			}
			test := alert.New(alert.KindPod, "alertkube", "console-test", "AlertkubeConsoleTest", alert.SeverityInfo)
			test.Cluster = cfg.Cluster
			test.Summary = "Test alert from the AlertKube console - if you can read this, the channel is wired correctly."
			test.Event = true // ephemeral: dispatched once, never tracked or resolved
			testCtx, cancel := context.WithTimeout(req.Context(), 20*time.Second)
			defer cancel()
			sendErr := reg.TestSend(testCtx, in.Sink, test)
			metrics.RuntimeMutations.WithLabelValues("channel_test").Inc()
			by := user
			w.Header().Set("Content-Type", "application/json")
			if sendErr != nil {
				klog.Warningf("channel test-fire failed: sink=%s by=%q: %v", in.Sink, by, sendErr)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": sendErr.Error()})
				return
			}
			klog.Infof("channel test-fire ok: sink=%s by=%q", in.Sink, by)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

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
	metrics.ClearSilencesHandler()
	metrics.ClearChannelsHandler()
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

// statefulSinks open/close incidents keyed by alert fingerprint. They
// dedupe storms themselves, must receive every resolve (to close the
// incident), and must never receive group summaries (nothing closes them).
var statefulSinks = map[string]bool{"pagerduty": true, "opsgenie": true}

// filterRoute returns the sinks in route for which keep is true.
func filterRoute(route []string, keep func(name string) bool) []string {
	out := make([]string, 0, len(route))
	for _, s := range route {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

func dropStateful(route []string) []string {
	return filterRoute(route, func(s string) bool { return !statefulSinks[s] })
}

func keepStateful(route []string) []string {
	return filterRoute(route, func(s string) bool { return statefulSinks[s] })
}

// Delivery-path timeout budget (canonical description — perSinkTimeout in
// internal/sinks and DefaultTimeout/DefaultRetry in internal/httpx point
// here). The budgets nest, outermost first:
//
//	dispatch()           context.WithTimeout(dispatchTimeout = 20s)   // one fan-out across a route
//	  Registry.Dispatch  per-sink goroutine
//	    sendCtx          context.WithTimeout(perSinkTimeout = 15s)    // one sink, within the 20s
//	      sink.Send → httpx.Retry   (DefaultRetry: ≤3 attempts, backoff capped at 1s)
//	        each attempt http.Client{Timeout: DefaultTimeout = 10s}   // one HTTP request
//
// Every attempt AND its backoff sleep run under sendCtx, so perSinkTimeout
// (15s) is the hard ceiling on all retries for a sink: a Retry-After or a
// custom RetryPolicy that would sleep past it just aborts the retry
// (sleepWithCtx returns ctx.Err()). dispatchTimeout (20s) > perSinkTimeout
// (15s) leaves headroom for the goroutine fan-out/join.
//
// dispatchTimeout is detached from the controller ctx on purpose: a resolve
// (or an alert drained at shutdown) must still reach its sinks after ctx is
// cancelled.
const dispatchTimeout = 20 * time.Second

// enrichDrainTimeout caps how long shutdown waits for in-flight pod
// enrichment before giving up and saving state anyway.
const enrichDrainTimeout = 10 * time.Second

// dispatch fans an alert to a route on a detached, time-bounded context so
// delivery survives controller-ctx cancellation during shutdown.
func dispatch(reg *sinks.Registry, a *alert.Alert, route []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), dispatchTimeout)
	defer cancel()
	return reg.Dispatch(ctx, a, route)
}

// drainWatchers waits for watchers with background work (pod enrichment) to
// finish, bounded by timeout, so alerts mid-enrichment are delivered and
// persisted instead of abandoned on shutdown.
func drainWatchers(ws []watchers.Watcher, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for _, w := range ws {
		if d, ok := w.(watchers.Drainer); ok {
			d.Drain(ctx)
		}
	}
}

func makeEmitter(store *alert.Store, r *router.Router, reg *sinks.Registry, cfg *config.Config, grouper *group.Grouper, observe func(*alert.Alert)) watchers.Emit {
	// controllerStart is intentionally per-leadership-acquisition, not
	// process start: each runController builds fresh informers whose initial
	// sync re-fires every standing condition. The grace window must cover
	// that fresh sync - seeding the re-fires into the mute window instead of
	// paging them - every time this pod (re)acquires leadership, not only on
	// the first acquisition. See controllerRuns for the re-entrancy contract.
	controllerStart := time.Now()
	grace := time.Duration(cfg.Behavior.StartupGraceSeconds) * time.Second
	return func(a *alert.Alert) {
		// Resolve marker from a watcher Delete (see emitResolve): the object
		// is gone, so clear every active alert for it regardless of reason
		// and let the store fan synthetic resolves to the sinks. Bypasses the
		// firing pipeline (severity/grace/dedupe/route) entirely.
		if a.Resolved {
			store.ResolveObject(a.Kind, a.Namespace, a.Name)
			return
		}
		a.Cluster = cfg.Cluster
		// Severity overrides run before metrics, dedupe, and routing so
		// every downstream decision sees the remapped severity.
		for _, ov := range cfg.SeverityOverrides {
			if a.MatchLabels(ov.Match) {
				a.Severity = alert.Severity(ov.Severity)
				break
			}
		}
		metrics.AlertsTotal.WithLabelValues(string(a.Kind), string(a.Severity), a.Reason).Inc()
		// Ephemeral event alerts (e.g. CloudTrail management events) are
		// point-in-time facts, not standing conditions: dedupe by fingerprint
		// and dispatch once, but never enter the active set, never get a TTL
		// resolve, and never open a stateful incident (dropStateful) - an
		// incident with no resolve would dangle forever. They bypass the
		// startup-grace seed (a restart re-notify is already prevented by the
		// persisted lastSent map) and grouping (discrete events are not folded
		// into a condition summary).
		if a.Event {
			if !store.ShouldSendEvent(a) {
				metrics.AlertsSuppressed.WithLabelValues("muted").Inc()
				return
			}
			if observe != nil {
				observe(a)
			}
			route := dropStateful(r.Route(a))
			if len(route) == 0 {
				return
			}
			cp := *a
			dispatch(reg, &cp, route)
			return
		}
		// Startup grace: conditions that pre-date this process (informer
		// initial sync re-fires every standing CrashLoop on restart) are
		// seeded into the mute window instead of re-paging.
		if grace > 0 && time.Since(controllerStart) < grace {
			store.Seed(a.Fingerprint)
			metrics.AlertsSuppressed.WithLabelValues("startup").Inc()
			return
		}
		if !store.ShouldSend(a) {
			metrics.AlertsSuppressed.WithLabelValues("muted").Inc()
			store.Touch(a.Fingerprint)
			// A muted re-fire still proves the source condition persists:
			// keep its inhibitions armed or they expire mid-outage and the
			// dependent alert storm leaks through.
			r.ArmInhibitions(a)
			return
		}
		if observe != nil {
			observe(a)
		}
		route := r.Route(a)
		if route == nil {
			return
		}
		// Grouping runs after routing so silenced/inhibited alerts never
		// open or join a window. The first alert of a group passes; the
		// rest fold into the summary flushed at window close.
		if grouper != nil && !grouper.Offer(a) {
			metrics.AlertsSuppressed.WithLabelValues("grouped").Inc()
			return
		}
		// Dispatch a copy: the original is retained in the store and its
		// EndsAt is mutated by Touch while sink goroutines read the alert.
		cp := *a
		if !dispatch(reg, &cp, route) {
			store.MarkFailed(a.Fingerprint)
		}
	}
}
