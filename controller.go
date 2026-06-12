package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/group"
	"alertkube/internal/metrics"
	"alertkube/internal/persist"
	"alertkube/internal/receiver"
	"alertkube/internal/router"
	"alertkube/internal/sinks"
	"alertkube/internal/watchers"
)

// runController wires the watchers, sweeper, and dispatch path.
// Returns when ctx is cancelled (signal received OR leader election lost).
// A non-empty watchNamespace scopes every informer to that namespace and
// disables the node watcher (nodes are cluster-scoped), so the controller
// runs under a namespace Role instead of a ClusterRole.
func runController(ctx context.Context, clientset kubernetes.Interface, cfg *config.Config, watchNamespace string) {
	reg := buildSinks(cfg)
	r := router.New(cfg.Routing, cfg.Inhibitions, cfg.Silences, []string{"slack"})
	r.SetDisableAnnotationSilences(cfg.Behavior.DisableAnnotationSilences)

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
			// Detached ctx so the shutdown drain can still deliver.
			fctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			reg.Dispatch(fctx, s, route)
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
		reg.Dispatch(ctx, a, route)
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
			klog.Infof("restored state: %d active alerts, %d mute records (saved %s)",
				len(snap.Active), len(snap.LastSent), snap.SavedAt.Format(time.RFC3339))
		}
	}

	emit := makeEmitter(ctx, store, r, reg, cfg, grouper)

	// Read-only view of the active set + recent history for dashboards
	// and debugging. Reachable on the metrics address; gate with the
	// chart's NetworkPolicy ingressFrom when the cluster is multi-tenant.
	metrics.SetAlertsHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": store.ActiveList(),
			"recent": store.Recent(),
		})
	}))

	if cfg.Receiver.Enabled {
		receiverToken := os.Getenv("ALERTKUBE_RECEIVER_TOKEN")
		if receiverToken == "" {
			klog.Warningf("receiver enabled WITHOUT a bearer token: POST /api/v1/alerts on %s accepts unauthenticated alert injection - set ALERTKUBE_RECEIVER_TOKEN (helm: receiver.token) or restrict the port with a NetworkPolicy", cfg.MetricsAddr)
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

	var factoryOpts []informers.SharedInformerOption
	if watchNamespace != "" {
		klog.Infof("watching single namespace %q (node alerts disabled)", watchNamespace)
		factoryOpts = append(factoryOpts, informers.WithNamespace(watchNamespace))
	}
	factory := informers.NewSharedInformerFactoryWithOptions(clientset, 0, factoryOpts...)
	for _, w := range buildWatchers(clientset, cfg, watchNamespace) {
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
	go runSweeper(ctx, &wg, store, persister, reg, cfg)
	if grouper != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			grouper.Run(ctx) // drains open windows on shutdown
		}()
	}

	<-ctx.Done()
	klog.Infof("%s shutting down", appName)
	wg.Wait()
	if persister != nil {
		// ctx is already cancelled; the final save gets its own deadline.
		saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := persister.Save(saveCtx, store.Export()); err != nil {
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

func makeEmitter(ctx context.Context, store *alert.Store, r *router.Router, reg *sinks.Registry, cfg *config.Config, grouper *group.Grouper) watchers.Emit {
	controllerStart := time.Now()
	grace := time.Duration(cfg.Behavior.StartupGraceSeconds) * time.Second
	return func(a *alert.Alert) {
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
		if !reg.Dispatch(ctx, &cp, route) {
			store.MarkFailed(a.Fingerprint)
		}
	}
}
