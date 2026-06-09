# alertkube Launch Readiness Audit

## Executive Summary

**Recommendation: NO-GO for v0.0.1 launch.**

Four parallel audits (security, reliability, production readiness, code quality) surfaced **6 blocker-class** issues and a large tail of high-severity gaps. The single biggest theme is that user-facing alert delivery cannot tolerate a single slow Slack endpoint, and tenant-controlled annotations let any workload owner redirect or silence the operator's alerts. The chart also ships secrets in plaintext for every sink except Slack, and there are zero unit tests and no CI pipeline.

### Top 3 blockers
1. **Plaintext credentials in Deployment env.** PagerDuty routing key, Teams webhook URL, and generic webhook URL are rendered as `value:` env (only Slack uses `secretKeyRef`). Anyone with `get deployment` reads them. (`helm/templates/deployment.yaml:32-37`, `security #1`)
2. **Tenant-driven Slack channel redirection.** `internal/sinks/slack.go:40-42` honors a pod annotation `alert-slack-channel`, so any user who can write pod annotations can steer alerts to executive channels, DMs, or @mentions. (`security #2`)
3. **Slack sink ignores `ctx` and uses `http.DefaultClient` with no timeout.** Combined with sequential `Registry.Dispatch`, a hung Slack endpoint blocks every subsequent sink AND the informer worker. (`internal/sinks/slack.go:34,54`, `reliability #1`)

### Top 5 risks (beyond the blockers)
1. **No persistence across restart.** Mute / inhibit / silence-fired state is in-memory only — every restart re-floods downstreams. (`prod_readiness #4`)
2. **No leader election + `replicas: 1`** in the chart guarantees duplicate alerts on rolling updates and is a SPOF. (`prod_readiness #1`)
3. **`runbook-url` annotation → unvalidated URL injected as a Slack button.** Tenant can phish on-call. (`internal/templates/blockkit.go:53-57`, `security #4`)
4. **Cluster-wide `pods/log` access shipped to external sinks with no redaction.** Application secrets logged at startup re-emit to chat. (`helm/templates/rbac.yaml:14-16`, `security #7`, `security #13`)
5. **Routing semantics are silently wrong.** `alert.matchOrRegex` strips `.*` and falls back to `strings.Contains`, so a route `namespace: prod-.*` also matches `dev-prod-tools`. README documents regex; implementation is substring. (`internal/alert/alert.go:134-139`, `code_quality #3`)

## Findings by Dimension

### Security

#### PagerDuty key, Teams webhook URL, generic webhook URL ship as plaintext env in Deployment
- **Severity:** blocker
- **File:** `helm/templates/deployment.yaml:32-37`
- **Evidence:** env entries `PAGERDUTY_ROUTING_KEY: value: {{ .Values.pagerduty.routingKey | quote }}`, `TEAMS_WEBHOOK_URL: value: {{ .Values.teams.webhookUrl | quote }}`, `GENERIC_WEBHOOK_URL: value: {{ .Values.genericWebhook.url | quote }}` rendered as `value:` not `valueFrom: secretKeyRef`. Only Slack uses a Secret (`deployment.yaml:29-31`).
- **Recommendation:** Use the Slack `secretKeyRef` pattern for all four credentials: render to Secret when user supplies inline, otherwise reference an external Secret name/key.

#### Annotation-driven Slack channel override allows alert redirection by tenants
- **Severity:** blocker
- **File:** `internal/sinks/slack.go:40-42`
- **Evidence:** `if override, ok := a.Annotations["alert-slack-channel"]; ok && override != "" { channel = override }`. Annotations sourced via `mergeAnnotations(pod)` (`internal/watchers/pod.go:162-173`). Any pod-writer can set `alert-slack-channel: "#executive-incidents"` (or DM or `<@U123>`).
- **Recommendation:** Drop or gate behind allowlist (`cfg.Channels.AllowedOverrides`). Reject anything not matching `^#[a-z0-9._-]{1,80}$`.

#### `alert-silence-until` annotation lets tenants silence their own alerts
- **Severity:** high
- **File:** `internal/router/router.go:56-61`
- **Evidence:** `if until, ok := a.Annotations["alert-silence-until"]; ok { if t, err := time.Parse(time.RFC3339, until); err == nil && now.Before(t) { return true } }`. Annotation flows from the pod itself.
- **Recommendation:** Honor silences only via operator-controlled Config / Silence CRs, not workload annotations.

#### `runbook-url` annotation injects arbitrary URL into Slack button without validation
- **Severity:** high
- **File:** `internal/templates/blockkit.go:53-57`
- **Evidence:** `if runbook := a.Annotations["runbook-url"]; runbook != "" { ... WithURL(runbook) }`. No scheme allowlist; `javascript:`, `data:`, `file://` unblocked.
- **Recommendation:** Require `https://`, optionally pin to allowlist of hostnames or sign via admission policy.

#### Container has no hardened securityContext (runs as root by default)
- **Severity:** high
- **File:** `helm/templates/deployment.yaml:20-22` (+ `helm/values.yaml:82`)
- **Evidence:** Only `podSecurityContext: {{- toYaml .Values.podSecurityContext | nindent 8 }}`, default `{}`. No container `securityContext` at all.
- **Recommendation:** Add container `securityContext` with `runAsNonRoot: true`, `runAsUser: 65532`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile.type: RuntimeDefault`. Set sane defaults in `values.yaml`.

#### Dockerfile has no USER directive — image runs as root
- **Severity:** high
- **File:** `Dockerfile:8-12`
- **Evidence:** Final stage `FROM alpine:3.19` + `COPY --from=builder ... ENTRYPOINT`, no `USER` line.
- **Recommendation:** Switch to `gcr.io/distroless/static:nonroot` (drop apk add); add `USER 65532:65532`.

#### Cluster-wide `pods/log` access exposes every pod's logs to external sinks
- **Severity:** high
- **File:** `helm/templates/rbac.yaml:14-16`
- **Evidence:** ClusterRole grants `pods/log` on `[""]`; `internal/collectors/logs.go:14-31` streams 50 lines of previous container output into `a.Details["Pod Logs Before Restart"]`; every sink dumps `Details` verbatim.
- **Recommendation:** Make `pods/log` access optional, off by default; gate at runtime to `watchedNamespaces`; add redaction.

#### No SSRF protection / scheme allowlist in generic webhook sink
- **Severity:** high
- **File:** `internal/sinks/webhook.go:14-21`, `internal/httpx/httpx.go:13-39`
- **Evidence:** `defaultClient = &http.Client{Timeout: 10*time.Second}` with no `CheckRedirect`, no scheme allowlist. Can point at `http://169.254.169.254/...`.
- **Recommendation:** Reject non-https (or opt-in http), set `client.CheckRedirect = func(...) error { return http.ErrUseLastResponse }`, parse-and-fail-fast on startup, allowlist hostnames.

#### Caller-controlled regex in filter.New compiled without length cap
- **Severity:** medium
- **File:** `internal/filter/filter.go:25-29`
- **Evidence:** `regexp.Compile(p)` on comma-split tokens; no length cap; compile error silently appends pattern as literal anyway.
- **Recommendation:** Cap each token at e.g. 256 chars, klog.Warning on compile failure, don't double-store.

#### Go 1.21 toolchain is past upstream security support
- **Severity:** medium
- **File:** `go.mod:3`, `Dockerfile:1`
- **Evidence:** `go 1.21` + `FROM golang:1.21-alpine AS builder`. Only Go 1.22/1.23 receive security fixes.
- **Recommendation:** Bump to `go 1.23` (or current N-1), add renovate/Dependabot.

#### Mutable image tag, no digest pin
- **Severity:** medium
- **File:** `helm/values.yaml:6-8`
- **Evidence:** `image: { repository: aryasoni98/alertkube, tag: "0.0.1", pullPolicy: IfNotPresent }`. Pulled by tag.
- **Recommendation:** Pin by digest in values.yaml; document release process publishing digest; consider `pullPolicy: Always` + digest.

#### Metrics HTTP server has no ReadHeaderTimeout
- **Severity:** medium
- **File:** `internal/metrics/metrics.go:49`
- **Evidence:** `http.ListenAndServe(addr, mux)` — default infinite read/write timeouts (Slowloris).
- **Recommendation:** Use `&http.Server{ReadHeaderTimeout: 5s, ReadTimeout: 10s, WriteTimeout: 10s, IdleTimeout: 60s}` + `srv.ListenAndServe()`.

#### Pod logs and Kubernetes details ship to external sinks unredacted
- **Severity:** medium
- **File:** `internal/watchers/pod.go:141-143`, `internal/collectors/logs.go:14-31`
- **Evidence:** 50-line tail attached to `a.Details["Pod Logs Before Restart"]`; sinks dump every detail key. Env, args, resource specs also pushed.
- **Recommendation:** Redaction pass (regex `AKIA…`, `sk-…`, `ghp_…`, bearer prefixes; lines with `password=|token=|secret=`); make collection opt-in per namespace; document risk.

#### Helm Secret stores webhook URL via b64 in values, leaking into Helm history
- **Severity:** medium
- **File:** `helm/templates/secret.yaml:1-10`
- **Evidence:** Default path renders inline `slack.webhookUrl | b64enc` into Secret; plaintext lands in `helm get values --all` and any `helm template` artifacts in git.
- **Recommendation:** Flip default to require `secretKeyRef`; remove or warn on inline path; document ExternalSecrets pattern; apply same pattern for PD/Teams/webhook.

#### Pod label keys silently override annotations in mergeAnnotations
- **Severity:** low
- **File:** `internal/watchers/pod.go:162-173`
- **Evidence:** `mergeAnnotations` walks annotations then labels; pod labels named `alert-silence-until` / `alert-slack-channel` / `runbook-url` honored as annotations.
- **Recommendation:** Stop merging labels into annotation lookups; scope labels to `a.Labels` only.

#### Generic webhook sink lacks request signing / authentication
- **Severity:** low
- **File:** `internal/sinks/webhook.go:19-21`, `internal/httpx/httpx.go:25-29`
- **Evidence:** Only `Content-Type: application/json` header. No HMAC, no `Authorization`.
- **Recommendation:** Optional `GENERIC_WEBHOOK_SECRET` → `X-Alertkube-Signature: hmac-sha256(body)`.

#### No NetworkPolicy, no PodDisruptionBudget shipped
- **Severity:** low
- **File:** `helm/templates/`
- **Evidence:** Templates dir has deployment, rbac, secret, service, servicemonitor, configmap — no NetworkPolicy, no PDB.
- **Recommendation:** Templated NetworkPolicy (ingress to :9090 from configurable selector; egress to apiserver + configured webhook hosts); PDB `maxUnavailable: 0`.

#### Slack channel override accepted with no length / character validation
- **Severity:** low
- **File:** `internal/sinks/slack.go:40-42`
- **Evidence:** Override value passed directly to `slack.PostWebhook`; annotations can be 256KB.
- **Recommendation:** Validate against `^#[a-z0-9._-]{1,80}$`.

#### Routing rules do not anchor matches; loose contains semantics on namespace/reason
- **Severity:** low
- **File:** `internal/alert/alert.go:118-139`
- **Evidence:** `matchOrRegex` strips `.*` and uses `strings.Contains`; `namespace: prod` matches `nonprod` and `prod-test`.
- **Recommendation:** Use anchored regex (`^pattern$`) or strict equality; document semantics.

### Reliability

#### Slack sink ignores ctx and uses http.DefaultClient with no timeout, can block all sinks
- **Severity:** blocker
- **File:** `internal/sinks/slack.go:34,54`
- **Evidence:** `Send(_ context.Context, a *alert.Alert) error` discards ctx; `slack.PostWebhook(s.webhookURL, msg)` uses `context.Background()` and `http.DefaultClient` internally. Combined with sequential `Registry.Dispatch` (`sink.go:42-55`), a stalled Slack endpoint blocks every subsequent sink AND the informer worker (since emit runs inline from informer handlers, `main.go:114-128`, `pod.go:42-60`).
- **Recommendation:** Switch to `slack.PostWebhookCustomHTTPContext(ctx, s.webhookURL, customClient, msg)` with `&http.Client{Timeout: 10s}`. Run `Registry.Dispatch` fan-out in parallel with per-sink timeouts.

#### Resolved-alert dispatch reuses captured top-level ctx after shutdown is requested
- **Severity:** high
- **File:** `main.go:51-55, 130-143`
- **Evidence:** `onResolved=func(a){ reg.Dispatch(ctx, a, r.Route(a)) }` captures parent ctx that cancel()s on SIGTERM. A sweep after cancel returns `ctx.Err()` immediately.
- **Recommendation:** Use a separate shutdown-tolerant ctx (`context.WithTimeout(context.Background(), 30s)`) for the final resolve flush; emit a synthetic resolve sweep right after `waitForSignal` before cancel().

#### Router.activeInhibits grows unbounded — leaks per unique inhibitKey
- **Severity:** high
- **File:** `internal/router/router.go:21,89-98`
- **Evidence:** `maybeArmInhibition` writes `r.activeInhibits[key]`; `inhibited()` reads but never deletes expired keys.
- **Recommendation:** Sweep step inside `inhibited()`/`maybeArmInhibition` deleting `now.After(exp)` entries; or periodic cleaner from `runSweeper`.

#### Inhibition arms even when source alert is muted/silenced/inhibited itself
- **Severity:** high
- **File:** `main.go:114-128`, `internal/router/router.go:35-52`
- **Evidence:** `Route` calls `silenced()`, `inhibited()`, then `maybeArmInhibition` unconditionally before returning.
- **Recommendation:** Move `maybeArmInhibition(a)` to the success path AFTER silenced/inhibited short-circuits, AND only when `ShouldSend` returned true.

#### deployment watcher nil-derefs dep.Spec.Replicas
- **Severity:** high
- **File:** `internal/watchers/deployment.go:32`
- **Evidence:** `*dep.Spec.Replicas` used directly; `Spec.Replicas *int32` can be nil.
- **Recommendation:** Guard `replicas := int32(0); if dep.Spec.Replicas != nil { replicas = *dep.Spec.Replicas }`; add top-level `defer recover()` in every handler.

#### Sequential Dispatch — one slow sink delays/blocks all others for the same alert
- **Severity:** high
- **File:** `internal/sinks/sink.go:41-56`
- **Evidence:** `Dispatch` loops over names calling `s.Send(ctx, a)` inline; no goroutines, no per-sink timeout.
- **Recommendation:** Fan out per sink with errgroup + per-sink `context.WithTimeout`; at minimum `go emit(a)` from informer handler.

#### Informer handlers run inline emit → Dispatch → network IO; no goroutine offload
- **Severity:** high
- **File:** `main.go:114-128`, `internal/watchers/pod.go:42-60`
- **Evidence:** Pod `UpdateFunc` directly calls `evaluate` → `emit` → `reg.Dispatch`; `evaluate` also calls `collectors.PodEvents`/`PreviousContainerLogs` (apiserver RTTs) inline.
- **Recommendation:** Buffered channel + N workers; collectors out of the synchronous handler path.

#### No panic recovery in informer handlers / Dispatch / emit
- **Severity:** high
- **File:** `internal/watchers/{pod,node,deployment,pvc,job}.go`, `internal/sinks/sink.go:41`
- **Evidence:** Every `UpdateFunc` body runs without `recover()`; any nil deref bubbles into `runtime.HandleCrash` and the process exits.
- **Recommendation:** Wrap every `UpdateFunc`/`AddFunc` and each `s.Send` call in `defer func(){ if r := recover(); r != nil { klog.Errorf(...) } }()`.

#### Slack PostWebhook ignores ctx (duplicate of #1)
- **Severity:** high
- **File:** `internal/sinks/slack.go:34`
- **Evidence:** Same finding as `reliability #1` — flagged separately by audit prompt.
- **Recommendation:** See `reliability #1`.

#### CleanOldHistory cutoff hardcoded 1h, decoupled from configurable muteWindow
- **Severity:** medium
- **File:** `internal/alert/store.go:73-82`, `internal/config/config.go:101-103`
- **Evidence:** `cutoff := time.Now().Add(-1*time.Hour)`; `MUTE_SECONDS=7200` (2h) breaks dedup.
- **Recommendation:** `cutoff := time.Now().Add(-s.muteWindow * 2)`, clamp to ≥10m.

#### node watcher relies on cond-on-left for nil safety; misses initial NotReady via Add
- **Severity:** medium
- **File:** `internal/watchers/node.go:39,57-59`
- **Evidence:** Only registers `UpdateFunc`; initial-list NodeNotReady misses. `clientset` field unused.
- **Recommendation:** Register `AddFunc`; remove unused clientset or wire `NodeEvents`.

#### Store.lastSent / Store.active have no size cap, no panic safety
- **Severity:** medium
- **File:** `internal/alert/store.go:30-50`
- **Evidence:** `active` map grows monotonically for first-fire alerts that never repeat — `EndsAt` is zero so `SweepResolved` skips them.
- **Recommendation:** Set `a.EndsAt = time.Now().Add(s.resolveTTL)` on first store; add LRU eviction.

#### No retry/backoff on sink errors
- **Severity:** medium
- **File:** `internal/sinks/sink.go:41-56`, `internal/httpx/httpx.go:17-39`
- **Evidence:** Single `Do()` call; transient 429/500 drops alert permanently.
- **Recommendation:** Small retry loop with exponential backoff + jitter for 5xx/429; capped at 3 within ctx deadline.

#### PVC pending alert lacks the documented '>5m' gate
- **Severity:** medium
- **File:** `internal/watchers/pvc.go:21-46`
- **Evidence:** Comment says ">5m"; code only checks `CreationTimestamp.IsZero()`.
- **Recommendation:** `if time.Since(pvc.CreationTimestamp.Time) < 5*time.Minute { return }`; delayed work-queue requeue.

#### informer 0-resync — no periodic re-emit, missed transitions stay missed
- **Severity:** medium
- **File:** `main.go:59`
- **Evidence:** `NewSharedInformerFactory(clientset, 0)` plus watchers only register `UpdateFunc`. Initial-list Adds are dropped; restart mid-incident loses all signal.
- **Recommendation:** Register `AddFunc` synthesizing transition where `oldN==nil`; or set 5m resync; or startup-time sweep.

#### Multiple emitContainerAlert paths call blocking apiserver collectors from informer goroutine
- **Severity:** medium
- **File:** `internal/watchers/pod.go:117-152`, `internal/collectors/events.go:15-30`, `internal/collectors/logs.go:14-31`
- **Evidence:** `PodEvents` lists ALL events in namespace + filters in Go; called inline.
- **Recommendation:** FieldSelector on `involvedObject.Name`/`Namespace`; goroutine + channel for detail collection.

#### WaitForCacheSync result ignored
- **Severity:** medium
- **File:** `main.go:66`
- **Evidence:** Return value of `factory.WaitForCacheSync` discarded; missing RBAC on a kind silently degrades.
- **Recommendation:** Capture result; `klog.Fatalf` if not synced; reflect in `/readyz`.

#### metrics.Serve uses ListenAndServe with no shutdown, leaks goroutine on exit
- **Severity:** low
- **File:** `internal/metrics/metrics.go:47-52`
- **Evidence:** `go func(){ http.ListenAndServe(addr, mux) }()` — never wired to ctx.
- **Recommendation:** `http.Server` + `srv.Shutdown(ctx)` from main shutdown sequence.

#### metrics server failures do not signal main; ListenAndServe error is logged and swallowed
- **Severity:** low
- **File:** `internal/metrics/metrics.go:47-52`, `main.go:64`
- **Evidence:** main unconditionally proceeds.
- **Recommendation:** Fatal on bind error or propagate.

#### resolved-alert dispatch bypasses ShouldSend so muteWindow does not throttle resolves
- **Severity:** info
- **File:** `internal/alert/store.go:65-69`, `main.go:51-55`
- **Evidence:** `onResolved` directly dispatches; flapping fingerprints could spam resolves; `Route` may incorrectly arm an inhibition at resolve time.
- **Recommendation:** Document intent; skip `maybeArmInhibition` for resolved alerts; small resolve-side dedup.

#### no goroutine to mark metrics.ActiveAlerts — gauge declared but never set
- **Severity:** low
- **File:** `internal/metrics/metrics.go:28-30`
- **Evidence:** Gauge always reads 0.
- **Recommendation:** From `runSweeper`, after each sweep `metrics.ActiveAlerts.Set(float64(store.NumActive()))`.

#### Router and Store concurrent access — independent mutexes
- **Severity:** info
- **File:** `internal/alert/store.go:11`, `internal/router/router.go:21`
- **Evidence:** Independent mutexes today. No deadlock; future joining risk.
- **Recommendation:** Document lock order if joined; consider `sync.RWMutex` on Router.

#### filter.Set treats every comma token as BOTH literal AND regex
- **Severity:** low
- **File:** `internal/filter/filter.go:20-32,38-53`
- **Evidence:** Token unconditionally appended to `literals` AND compiled regex.
- **Recommendation:** Treat `/…/` tokens as regex, others exact; or document.

#### MatchLabels loose 'contains' regex shim for namespace/reason can over-match silences/inhibitions
- **Severity:** medium
- **File:** `internal/alert/alert.go:118-139`
- **Evidence:** `matchOrRegex` substring fallback; `namespace: kube` silences `tokube`, `my-kubeflow`.
- **Recommendation:** Use real regex via `regexp.MatchString` or exact equality.

#### shouldHandle does not consider deletion / phase=Succeeded
- **Severity:** low
- **File:** `internal/watchers/pod.go:54-71`
- **Evidence:** `DeletionTimestamp != nil` and `PodSucceeded` not short-circuited; brief CrashLoop during teardown emits.
- **Recommendation:** Early-return on those.

#### WaitForCacheSync may race with subsequent emit (informational)
- **Severity:** info
- **File:** `main.go:60-66`
- **Evidence:** Order is correct; `metrics.Serve` spawns goroutine and returns immediately.
- **Recommendation:** Keep `metrics.Serve` non-blocking.

#### NodeEvents collector lists only NamespaceDefault — wrong for node-scoped events
- **Severity:** low
- **File:** `internal/collectors/events.go:24-29`
- **Evidence:** Most clusters emit Node events into `kube-system` or as cluster-scoped events.
- **Recommendation:** `Events(metav1.NamespaceAll)` with `FieldSelector involvedObject.kind=Node,involvedObject.name=<name>`.

#### Slack sink mutates the shared Alert object (a.Cluster)
- **Severity:** low
- **File:** `internal/sinks/slack.go:38`
- **Evidence:** Sequential dispatch hides the race today; parallel dispatch would expose it.
- **Recommendation:** Pass cluster through Slack message build path explicitly.

#### ShouldSend stores the alert pointer in active but later mutations are not synchronized
- **Severity:** low
- **File:** `internal/alert/store.go:38-40` vs `internal/sinks/slack.go:38`
- **Evidence:** Sink layer mutates `a.Cluster`; sweep sets `a.Resolved` — latent race once Dispatch is parallel.
- **Recommendation:** Make `Alert` immutable after creation (copy-on-modify) or document lock requirement.

### Production Readiness

#### No leader election + replicas:1 = duplicate alerts on every rolling restart
- **Severity:** blocker
- **File:** `helm/templates/deployment.yaml:7`, `main.go:35-77`
- **Evidence:** Hardcoded `replicas: 1`; no `leaderelection.RunOrDie`. Dedupe state in-memory only (`internal/alert/store.go:13`), so fresh replica re-pages everything.
- **Recommendation:** Add `client-go/tools/leaderelection` gated on `--enable-leader-election`; expose `replicas` + `leaderElection.enabled` in values.

#### /readyz returns 200 before informer cache sync — Pod accepts traffic while blind
- **Severity:** high
- **File:** `internal/metrics/metrics.go:42-46`, `main.go:64-66`
- **Evidence:** `mux.HandleFunc("/readyz", ok)` always 200. `metrics.Serve` runs BEFORE `factory.Start`/`WaitForCacheSync`.
- **Recommendation:** `atomic.Bool ready` flipped after `WaitForCacheSync`; `/readyz` returns 503 until then. Split `/livez` (always-200) from `/readyz`.

#### No graceful shutdown — SIGTERM cancels ctx while sinks are mid-flight
- **Severity:** high
- **File:** `main.go:73-77`, `internal/sinks/sink.go:41-56`, `internal/httpx/httpx.go:13-39`
- **Evidence:** `cancel()` then `wg.Wait()` only awaits sweeper. In-flight POSTs see `ctx.Done()` and abort with `io.EOF`. No `srv.Shutdown`.
- **Recommendation:** Derived shutdown ctx with timeout (grace - 5s); outstanding-sink WaitGroup that Dispatch wg.Add/Done's; `srv.Shutdown` on metrics; `terminationGracePeriodSeconds: 30`.

#### All mute/dedupe/inhibit/silence state is in-memory — every restart re-floods downstreams
- **Severity:** high
- **File:** `internal/alert/store.go:10-41`, `internal/router/router.go:20-32`
- **Evidence:** `lastSent`, `active`, `activeInhibits` all RAM-only.
- **Recommendation:** Persist to ConfigMap or Lease + ConfigMap snapshot every N seconds; hydrate before `WaitForCacheSync` returns. Or ship a much longer default mute and document the limitation.

#### No rate limit / flood protection between watchers and sinks
- **Severity:** high
- **File:** `internal/sinks/sink.go:41-56`, `main.go:114-128`
- **Evidence:** 200 CrashLooping pods → 200 alerts in <1s. Slack ~1 msg/s; PD has its own.
- **Recommendation:** `golang.org/x/time/rate` per-sink limiter; global `behavior.maxAlertsPerMinute` circuit breaker emitting summary alert; new label `reason="ratelimited"`.

#### No PodDisruptionBudget, NetworkPolicy, or securityContext hardening
- **Severity:** high
- **File:** `helm/templates/` (missing pdb.yaml / networkpolicy.yaml), `helm/templates/deployment.yaml:15-25`, `helm/values.yaml:82`
- **Evidence:** No PDB; no NetworkPolicy; `podSecurityContext: {}` and no container `securityContext`.
- **Recommendation:** Templated `pdb.yaml` (minAvailable: 1 conditional on replicas>1), templated `networkpolicy.yaml`, container `securityContext` defaults. Set `USER 65532` in Dockerfile.

#### ServiceMonitor lacks namespace / release labels expected by kube-prometheus-stack
- **Severity:** medium
- **File:** `helm/templates/servicemonitor.yaml:1-17`, `helm/values.yaml:51`
- **Evidence:** Only `include "alertkube.labels"` + free-form labels; many kube-prom-stack installs require `release: <name>`.
- **Recommendation:** Default `metrics.serviceMonitor.labels: {release: kube-prometheus-stack}`; document discovery; add `scrapeTimeout`.

#### alertkube_active_alerts gauge registered but NEVER written
- **Severity:** medium
- **File:** `internal/metrics/metrics.go:28-30`
- **Evidence:** No `Set/Inc/Dec` call exists anywhere.
- **Recommendation:** From `ShouldSend` + `SweepResolved` (under mutex) call `metrics.ActiveAlerts.Set(float64(len(s.active)))`; or expose `Store.ActiveCount()`.

#### Resolved-alert path skips AlertsTotal counter + dedupe / mute / suppression metrics
- **Severity:** medium
- **File:** `main.go:51-55, 114-128`, `internal/alert/store.go:53-69`
- **Evidence:** `onResolved` bypasses `makeEmitter`.
- **Recommendation:** Route resolved alerts through the same `emit()` pipeline (with `a.Resolved=true`, skip mute); add `alertkube_alerts_resolved_total`.

#### No per-watcher activity metric, no informer queue depth, no event-handler latency histogram
- **Severity:** medium
- **File:** `internal/metrics/metrics.go:11-31`, `internal/watchers/*.go`
- **Evidence:** Only 5 metrics. If Pod watcher `UpdateFunc` panics, only signal is absence of alerts.
- **Recommendation:** Add `alertkube_events_received_total{kind}`, `alertkube_event_processing_seconds{kind}`, `alertkube_watcher_running{kind}`.

#### Helm replicas hardcoded — no values.yaml knob to scale
- **Severity:** medium
- **File:** `helm/templates/deployment.yaml:7`, `helm/values.yaml`
- **Evidence:** Literal `replicas: 1`; no `strategy:` block (default RollingUpdate maxSurge=25%).
- **Recommendation:** `replicas: {{ .Values.replicaCount | default 1 }}`; `strategy: { type: Recreate }` (or RollingUpdate maxSurge:0 with leader election on); expose both in values.

#### image.tag pinned to 0.0.1 but repository points to non-published image
- **Severity:** medium
- **File:** `helm/values.yaml:5-8`, `helm/Chart.yaml:13`, `README.md:62`
- **Evidence:** `helm/values.yaml:6` → `repository: aryasoni98/alertkube`, `helm/Chart.yaml:13` → `home: https://github.com/aryasoni98/alertkube`, `docs/README.md:3` → `aryasoni98/alertkube`. Repository owner is consistent, but no published image exists yet (no CI workflow to build/push).
- **Recommendation:** Decide canonical repo; fix Chart.yaml home; add GitHub Actions build+push (`ghcr.io/<owner>/alertkube`); allow `image.digest` override.

#### Resource limits 200m/256Mi may starve informer cache on large clusters
- **Severity:** medium
- **File:** `helm/values.yaml:70-76`
- **Evidence:** ~10k Pods → ~250 MiB Pod cache; defaults will OOMKill.
- **Recommendation:** Bump defaults to `requests: 256Mi` / `limits: 1Gi`; document memory model; add `--namespace-scope`; field selectors / transforms to drop heavy fields.

#### Unstructured logging via klog.Infof, no log levels honored, no JSON output
- **Severity:** medium
- **File:** `main.go:67,74`, `internal/sinks/stdout.go:20`, `internal/watchers/pod.go:111`, `internal/metrics/metrics.go:48-50`
- **Evidence:** All `klog.Infof`/`Warning`/`Errorf` printf style; no `klog.V(2).Infof`, no `--v=` surfaced in values.
- **Recommendation:** Switch to `klog.InfoS`/`ErrorS` or `log/slog` with JSON; expose `logging.format` + `logging.verbosity`.

#### No runbook docs, no operations guide, no troubleshooting section
- **Severity:** medium
- **File:** `README.md`, missing `docs/operations.md`/`docs/troubleshooting.md`
- **Evidence:** README has install + config but no SLO, no PromQL alerts for alertkube itself, no failure-mode playbook.
- **Recommendation:** Add `docs/operations.md`, `docs/troubleshooting.md`; example `prometheusrule.yaml` gated by feature flag.

#### No documented migration path from k8s-pod-restart-info-collector
- **Severity:** medium
- **File:** `README.md`, `CHANGELOG.md`, missing `docs/migration-…`
- **Evidence:** No mention of predecessor; env-var compatibility code in `config.go:85-127` undocumented.
- **Recommendation:** `docs/migration-from-pod-restart-info-collector.md` with env-var → YAML table and in-place upgrade procedure.

#### Slack webhookURL captured at process start — config changes require restart
- **Severity:** medium
- **File:** `internal/sinks/slack.go:22-28`, `pagerduty.go:18-20`, `teams.go:15`, `webhook.go:14`
- **Evidence:** All read `os.Getenv(...)` at construction.
- **Recommendation:** Read on every Send (cheap), or SIGHUP reload handler; document rotation.

#### PVC `pending >5m` semantic comment is a lie — alert fires immediately
- **Severity:** medium
- **File:** `internal/watchers/pvc.go:14, 34-42`
- **Evidence:** Comment promises 5m gate; code only checks `CreationTimestamp.IsZero()`.
- **Recommendation:** Implement gate; expose `behavior.pvcPendingThreshold: 5m`.

#### NodeEvents collector lists events only from `default` namespace
- **Severity:** medium
- **File:** `internal/collectors/events.go:24-29`
- **Evidence:** Most clusters route Node events elsewhere.
- **Recommendation:** `Events(metav1.NamespaceAll)` + `FieldSelector involvedObject.kind=Node,involvedObject.name=<name>`.

#### PodEvents and NodeEvents list events on EVERY alert — API server abuse during flood
- **Severity:** medium
- **File:** `internal/collectors/events.go:14-29`, `internal/watchers/pod.go:137-148`
- **Evidence:** 200-pod CrashLoop → 400 List calls in <1s; client-go QPS=5/burst=10.
- **Recommendation:** Add Event informer to factory; cache per-resource event slices; raise `rest.Config.QPS/Burst`; combine with rate-limit.

#### metrics HTTP server uses ListenAndServe — cannot shutdown gracefully
- **Severity:** medium
- **File:** `internal/metrics/metrics.go:47-52`
- **Evidence:** Returns only on error; no `srv.Shutdown(ctx)`.
- **Recommendation:** `*http.Server` + `srv.Shutdown(shutdownCtx)`; `ReadHeaderTimeout: 5s`.

#### Routing match semantic mixes prefix and substring — silently matches unintended alerts
- **Severity:** medium
- **File:** `internal/alert/alert.go:118-139`
- **Evidence:** `matchOrRegex` substring fallback; `namespace: prod-.*` matches `dev-prod-tools`.
- **Recommendation:** `regexp.MustCompile` cached at config-load; anchor `^…$`; reject invalid at load.

#### config.Load silently ignores read errors — typo in --config means defaults, not failure
- **Severity:** low
- **File:** `internal/config/config.go:71-83`
- **Evidence:** `os.ReadFile` error dropped on explicit path.
- **Recommendation:** Fatal when path explicit and read fails.

#### Slack/Teams/PagerDuty webhook URLs may leak via httpx error logs
- **Severity:** low
- **File:** `internal/sinks/slack.go:25`, `teams.go:15`, `webhook.go:14`, `internal/httpx/httpx.go:36`
- **Evidence:** `POST <url> returned <status>` error string includes full URL with token.
- **Recommendation:** `sanitizeURL(url)` — keep scheme://host/first-segment/REDACTED.

#### Dockerfile uses Alpine 3.19 + CA-certs but no USER, no HEALTHCHECK, no labels, golang 1.21 EOL
- **Severity:** low
- **File:** `Dockerfile:1-13`
- **Evidence:** No `USER`, no `HEALTHCHECK`, no OCI labels; Go 1.21 EOL; Alpine 3.19 EOL Nov 2025.
- **Recommendation:** Bump to `golang:1.23-alpine` + `alpine:3.20+` (or distroless); add user, healthcheck, OCI labels; multi-arch buildx.

#### main waits for cache sync without ctx-cancel handling
- **Severity:** low
- **File:** `main.go:66`
- **Evidence:** `WaitForCacheSync(ctx.Done())` blocks; SIGTERM not wired yet; livenessProbe times out.
- **Recommendation:** Signal handler before `WaitForCacheSync`; startup timeout; `alertkube_cache_sync_seconds` metric.

#### ClusterRoleBinding grants cluster-wide list/watch — no scope-narrowing escape hatch
- **Severity:** low
- **File:** `helm/templates/rbac.yaml:13-25`
- **Evidence:** No `RoleBinding` mode for single-namespace install.
- **Recommendation:** Helm flag `rbac.scope: cluster|namespace`; pass `--namespace-scope`.

#### Sweeper interval hardcoded 30s — not tunable
- **Severity:** low
- **File:** `main.go:31, 71`, `internal/alert/store.go:53-69`
- **Evidence:** `sweepInterval = 30 * time.Second` const; `CleanOldHistory` cutoff hardcoded 1h.
- **Recommendation:** `Behavior.SweepIntervalSeconds` + `Behavior.HistoryRetentionSeconds` in config + values; jitter for HA.

#### Generic webhook + Teams have no retry, no backoff, no DLQ
- **Severity:** low
- **File:** `internal/httpx/httpx.go:13-39`, `internal/sinks/webhook.go`, `teams.go`
- **Evidence:** Single Do(); status≥400 increments error counter and drops payload.
- **Recommendation:** Retry loop (e.g. cenkalti/backoff/v4, 3 attempts) on 5xx/429; label `reason="retriable|fatal"`.

#### PodWatcher relies only on UpdateFunc — initial CrashLoop on startup is missed
- **Severity:** low
- **File:** `internal/watchers/pod.go:44-60`
- **Evidence:** No `AddFunc` registered; pods crashlooping before startup get no signal until next Update.
- **Recommendation:** Add `AddFunc` calling `evaluate(nil, newPod, emit)`.

#### Inhibition arms based on the alert being routed, not on detection
- **Severity:** low
- **File:** `internal/router/router.go:35-52, 89-98`
- **Evidence:** Silenced source skips arming, defeating operator intent.
- **Recommendation:** Move `maybeArmInhibition` before `silenced()` check.

#### No CI workflow visible — no tests, no lint, no image build automation
- **Severity:** low
- **File:** `.github/CODEOWNERS` (deleted), no `.github/workflows/`
- **Evidence:** No `_test.go` files; no workflows directory.
- **Recommendation:** `.github/workflows/{ci.yaml,release.yaml}`: `go test -race`, `golangci-lint`, `helm lint`, `kubeval`. Release: buildx multi-arch + cosign + helm package.

### Code Quality

#### Zero unit tests across the entire codebase
- **Severity:** blocker
- **File:** (repo-wide)
- **Evidence:** `find . -name '*_test.go'` returns 0 matches.
- **Recommendation:** Table-driven tests for `alert.MatchLabels`/`GroupKey`/`FieldValue`, `filter.Set`, `config.Load`, `Store.ShouldSend`/`SweepResolved`/`Touch`, `Router.Route`/`silenced`/`inhibited`/`maybeArmInhibition`, fake-clientset per watcher; target ≥60% coverage in `internal/{alert,router,filter,config}`.

#### No CI workflow — .github/workflows directory does not exist
- **Severity:** blocker
- **File:** `.github/workflows/` (absent)
- **Evidence:** Directory does not exist; `.github/CODEOWNERS` deleted.
- **Recommendation:** Add `ci.yml` (vet, test -race, lint, docker build, helm lint); restore CODEOWNERS; pin Go version to Dockerfile.

#### alert.matchOrRegex silently strips '.*' and falls back to substring
- **Severity:** high
- **File:** `internal/alert/alert.go:134-139`
- **Evidence:** `^prod-.*` becomes substring search for literal `^prod-`; `kube-system|prometheus` becomes substring for the pipe-delimited literal. README documents regex; behavior is substring.
- **Recommendation:** Replace with `regexp.MatchString` (or precompile at config load). Commit to one semantic (exact-equality or full-regex); test corner cases.

#### filter.Set double-counts every pattern as both literal and regex with conflicting semantics
- **Severity:** high
- **File:** `internal/filter/filter.go:25-30, 38-53`
- **Evidence:** Every token goes into BOTH `literals` (`HasPrefix`/equal) and `patterns` (regex). Strictly broader than either alone.
- **Recommendation:** Decide per-token via `regexp.QuoteMeta(p) == p` (literal) vs regex; tests for `^prod-.*`, `prod-`, etc.

#### klog.Fatalf at startup hides the underlying cause and prevents graceful exit
- **Severity:** high
- **File:** `main.go:40, 156, 161`
- **Evidence:** Three direct `klog.Fatalf` calls; falls back from out-of-cluster to in-cluster only on error.
- **Recommendation:** Return errors up to main; wrap with `fmt.Errorf("context: %w", err)`; probe in-cluster first when kubeconfig flag empty.

#### DeploymentWatcher dereferences possibly-nil *dep.Spec.Replicas
- **Severity:** high
- **File:** `internal/watchers/deployment.go:32`
- **Evidence:** `*dep.Spec.Replicas` direct deref.
- **Recommendation:** Guard via `k8s.io/utils/ptr.Deref`; add fake-informer test.

#### Sinks read environment variables at constructor time — cannot reload at runtime
- **Severity:** high
- **File:** `internal/sinks/webhook.go:14`, `teams.go:15`, `slack.go:23`, `pagerduty.go:19`
- **Evidence:** Env captured at New; rotation requires restart (and per `prod_readiness #4`, restart re-floods).
- **Recommendation:** Pass through `config.Config` so reload rebuilds sinks; or per-Send `os.Getenv`.

#### Routing matches first rule only — undocumented & differs from typical alert-routing semantics
- **Severity:** high
- **File:** `internal/router/router.go:46-51`
- **Evidence:** First match wins; default sinks hard-coded `[]string{"slack"}` in `main.go:46`.
- **Recommendation:** Document first-match-wins; make default sinks configurable via YAML.

#### Watchers register only UpdateFunc — initial state and AddFunc events never trigger alerts
- **Severity:** high
- **File:** `internal/watchers/{pod,node,deployment,pvc,job}.go`
- **Evidence:** All five watchers only set `UpdateFunc`.
- **Recommendation:** Add `AddFunc` for each watcher (nil-old path); test that a Failed Job seen at sync time emits.

#### Router.activeInhibits map grows unbounded
- **Severity:** high
- **File:** `internal/router/router.go:30, 89-98`
- **Evidence:** No expiry GC.
- **Recommendation:** `cleanExpiredInhibitions` from sweeper; test fills/expires/sweeps/asserts shrink.

#### ActiveAlerts gauge declared but never set
- **Severity:** high
- **File:** `internal/metrics/metrics.go:28-30`
- **Evidence:** Zero callers.
- **Recommendation:** Set under mutex in `ShouldSend`/`SweepResolved`.

#### Resolved fingerprints stay muted up to 1h after expiry
- **Severity:** high
- **File:** `internal/alert/store.go:73-82, 19-27`
- **Evidence:** `CleanOldHistory` hardcoded `-1h`; `SweepResolved` does not delete from `lastSent`. Next occurrence silently muted.
- **Recommendation:** Parameterize cutoff to `2 * muteWindow`; delete from `lastSent` on resolve; test resolve→re-fire path.

#### PodWatcher.mergeAnnotations stuffs labels into Annotations — semantic confusion
- **Severity:** medium
- **File:** `internal/watchers/pod.go:162-173`
- **Evidence:** Pod labels back-filled into `a.Annotations` map; downstream silence/runbook keys honored from labels.
- **Recommendation:** Copy annotations into `a.Annotations`, labels into `a.Labels` separately.

#### PVCWatcher comment lies about 5m pending threshold (duplicate of reliability)
- **Severity:** medium
- **File:** `internal/watchers/pvc.go:34-41`
- **Evidence:** See `reliability #14`.
- **Recommendation:** See `reliability #14`.

#### Resolved emitter in main.go bypasses Store.ShouldSend / Router silence checks
- **Severity:** medium
- **File:** `main.go:51-55`, `internal/alert/store.go:53-70`
- **Evidence:** `onResolved` runs `Route` but skips `ShouldSend` and active-store re-write.
- **Recommendation:** Document or route resolved events through same `makeEmitter` path; test it.

#### NodeEvents queries the default namespace only (duplicate of prod_readiness)
- **Severity:** medium
- **File:** `internal/collectors/events.go:24-30`
- **Evidence:** See `prod_readiness #19`.
- **Recommendation:** `Events("")` + field selector on `involvedObject.name`.

#### JobWatcher compares cond.Status to literal "True" instead of corev1.ConditionTrue
- **Severity:** low
- **File:** `internal/watchers/job.go:30`
- **Evidence:** Literal `"True"` vs `corev1.ConditionTrue` used in `node.go`.
- **Recommendation:** Use the typed constant for grep-ability.

#### Hard-coded magic numbers throughout
- **Severity:** low
- **File:** `main.go:31`, `internal/alert/store.go:76`, `internal/templates/blockkit.go:41`, `internal/collectors/logs.go:19`, `internal/config/config.go:60`, `internal/httpx/httpx.go:13`
- **Evidence:** `sweepInterval = 30s`, cutoff `1h`, `truncate(val, 2800)`, `TailLines: 50`, inhibit `10m`, http client `10s`.
- **Recommendation:** Named constants with rationale comment; surface sweep interval + http timeout via config.

#### PagerDuty sink hard-codes Severity="critical" regardless of input
- **Severity:** low
- **File:** `internal/sinks/pagerduty.go:44`
- **Evidence:** `Severity: "critical"` ignores `alert.Severity`.
- **Recommendation:** Map `alert.Severity → PD severity`.

#### Stdout sink: extra spacing in function header
- **Severity:** info
- **File:** `internal/sinks/stdout.go:17`
- **Evidence:** Spaces in `Supports(_ alert.Severity) bool     { return true }`.
- **Recommendation:** `gofmt -s -w ./...`.

#### store.go onResolved field tab-aligned oddly
- **Severity:** info
- **File:** `internal/alert/store.go:11-17`
- **Evidence:** Mixed tab/space alignment.
- **Recommendation:** `gofmt -s -w ./...` (pre-commit hook).

#### alert.Alert exposes mutable maps with no constructor for Cluster/Summary
- **Severity:** low
- **File:** `internal/alert/alert.go:54-93`
- **Evidence:** Mixed builder vs direct-field patterns; Fingerprint overwriteable.
- **Recommendation:** Adopt one style (fluent builder OR struct-literal+EnsureFingerprint); document in package comment.

#### config.Inhibition.DurationParsed silently swallows parse errors and defaults to 10m
- **Severity:** low
- **File:** `internal/config/config.go:57-63`
- **Evidence:** `if err != nil { return 10*time.Minute }` — no warning.
- **Recommendation:** Validate at Load; klog.Warningf on fallback; store parsed `time.Duration` on struct.

#### config.Load swallows os.ReadFile errors and proceeds with empty config (duplicate of prod_readiness)
- **Severity:** low
- **File:** `internal/config/config.go:71-83`
- **Evidence:** See `prod_readiness #22`.
- **Recommendation:** Return error when path non-empty.

#### Routing default sinks hard-coded in main.go
- **Severity:** low
- **File:** `main.go:46`
- **Evidence:** `[]string{"slack"}` hard-coded.
- **Recommendation:** Add `DefaultSinks []string` to `config.Config`.

#### SlackSink ignores ctx (duplicate of reliability blocker)
- **Severity:** low
- **File:** `internal/sinks/slack.go:34, 54`
- **Evidence:** See `reliability #1`.
- **Recommendation:** `slack.PostWebhookContext(ctx, ...)`.

#### PreviousContainerLogs uses deprecated k8s.io/utils/pointer.Int64Ptr
- **Severity:** low
- **File:** `internal/collectors/logs.go:19`
- **Evidence:** `pointer.Int64Ptr(50)`; superseded by `k8s.io/utils/ptr.To`.
- **Recommendation:** Migrate to `ptr.To(int64(50))`; lift `50` to const `logTailLines`.

#### CONTRIBUTION.md is a non-conventional filename
- **Severity:** medium
- **File:** `CONTRIBUTION.md`
- **Evidence:** GitHub special-cases `CONTRIBUTING.md` for in-UI prompts. File now rewritten with DCO + GitHub-issue flow (2026-06-09); rename remaining.
- **Recommendation:** Rename to `CONTRIBUTING.md`; add PR template under `.github/PULL_REQUEST_TEMPLATE.md`; restore CODEOWNERS; add CODE_OF_CONDUCT.md.

#### NOTICE file template placeholder (resolved)
- **Severity:** resolved
- **File:** `NOTICE:1`
- **Evidence:** Was `Copyright [2022] [aryasoni98 (Hong Kong) Limited]` (literal brackets, stale year, sed artifact). Fixed 2026-06-09 to `Copyright 2026 aryasoni98`.
- **Recommendation:** none — closed.

#### LICENSE filename is non-conventional
- **Severity:** low
- **File:** `LICENSE-2.0.txt`
- **Evidence:** GitHub detects `LICENSE`/`LICENSE.md`; current name may flag as unlicensed by scanners. `README.md:130` references the unconventional name.
- **Recommendation:** Rename to `LICENSE`; update README link.

#### CHANGELOG entries verified — accurate
- **Severity:** info
- **File:** `CHANGELOG.md:29`, `helm/templates/servicemonitor.yaml`
- **Evidence:** Each CHANGELOG bullet maps to real code (Block Kit, PD v2, per-severity channels, YAML config, fingerprint dedupe, resolve TTL, inhibitions, silences, metrics, healthz/readyz, runbook-url, filters).
- **Recommendation:** Keep CHANGELOG honest; add CI helm-lint to keep chart honest.

#### MatchLabels regex shim asymmetry across keys
- **Severity:** low
- **File:** `internal/alert/alert.go:118-132`
- **Evidence:** Only `namespace` and `reason` get the (broken) regex shim; `kind`, `severity`, `node`, `name`, `Labels[...]` are exact-equal-only.
- **Recommendation:** Apply uniform matching strategy; document.

#### shell-version-for-poc/ removal — confirmed in git status
- **Severity:** info
- **File:** `shell-version-for-poc/` (deleted)
- **Evidence:** `git status` D entries; no Go imports.
- **Recommendation:** Commit deletion; verify README/CHANGELOG never referenced.

#### FieldValue default branch reads Labels but mergeAnnotations writes user-pod labels into Annotations
- **Severity:** low
- **File:** `internal/alert/alert.go:111-113`, `internal/watchers/pod.go:122-123`
- **Evidence:** Routing rule `match: {app: payments}` won't match because `app` was copied into Annotations.
- **Recommendation:** Decide if routing reads Labels, Annotations, or both with precedence; update `FieldValue` + `mergeAnnotations`; test.

## Aggregated Risk Matrix

| Severity | Security | Reliability | Production Readiness | Code Quality | Total |
|---|---:|---:|---:|---:|---:|
| Blocker | 2 | 1 | 1 | 2 | **6** |
| High | 6 | 8 | 6 | 9 | **29** |
| Medium | 6 | 9 | 18 | 5 | **38** |
| Low | 5 | 10 | 7 | 12 | **34** |
| Info | 0 | 3 | 0 | 4 | **7** |
| **Dimension total** | **19** | **31** | **32** | **32** | **114** |

## Pre-Launch Checklist

### Must-fix (block v0.0.1)
- **Move PD / Teams / generic webhook secrets to `secretKeyRef`** — `helm/templates/deployment.yaml:32-37`, mirror Slack pattern in `helm/templates/secret.yaml` (`security #1`).
- **Remove or allowlist `alert-slack-channel` annotation override** — `internal/sinks/slack.go:40-42` (`security #2`).
- **Fix Slack sink: pass ctx + custom `http.Client{Timeout: 10s}` + parallel Dispatch** — `internal/sinks/slack.go:34,54`, `internal/sinks/sink.go:41-56` (`reliability #1`, `reliability #7`).
- **Nil-guard `*dep.Spec.Replicas`** — `internal/watchers/deployment.go:32` (`reliability #5`, `code_quality #6`).
- **Add `defer recover()` to all informer handlers + Dispatch** — `internal/watchers/*.go`, `internal/sinks/sink.go:41` (`reliability #9`).
- **Container `securityContext` + Dockerfile `USER 65532`** — `helm/templates/deployment.yaml`, `Dockerfile:8-12` (`security #5`, `security #6`).
- **Validate `runbook-url` scheme allowlist** — `internal/templates/blockkit.go:53-57` (`security #4`).
- **Honor or drop `alert-silence-until` only via operator config** — `internal/router/router.go:56-61` (`security #3`).
- **Stop merging pod labels into `a.Annotations`** — `internal/watchers/pod.go:162-173` (`security #15`, `code_quality #13`).
- **Make `/readyz` reflect cache sync** — `internal/metrics/metrics.go:42-46`, `main.go:64-66` (`prod_readiness #2`).
- **Graceful shutdown: derived ctx + outstanding-sink WaitGroup + `srv.Shutdown`** — `main.go:73-77`, `internal/metrics/metrics.go:47-52`, `internal/sinks/sink.go:41-56` (`prod_readiness #3`).
- **Bound `Router.activeInhibits` via expiry sweep** — `internal/router/router.go:21,89-98` (`reliability #3`, `code_quality #10`).
- **Set `metrics.ActiveAlerts` from sweeper** — `internal/metrics/metrics.go:28-30` (`prod_readiness #8`, `code_quality #11`).
- **CI workflow + table-driven unit tests** — missing `.github/workflows/`, missing `_test.go` (`prod_readiness #29`, `code_quality #1`, `code_quality #2`).
- **Fix `alert.matchOrRegex` semantics** — `internal/alert/alert.go:134-139` (`code_quality #3`).
- **Fix `filter.Set` double-counting** — `internal/filter/filter.go:25-30` (`code_quality #4`).
- **Rate-limit per sink + circuit breaker on flood** — `internal/sinks/sink.go:41-56` (`prod_readiness #5`).

### Should-fix (before broad adoption)
- Persist `lastSent` / `active` / `activeInhibits` to ConfigMap or Lease (`prod_readiness #4`).
- Leader election + `replicas` configurable + `strategy: Recreate` default (`prod_readiness #1`, `prod_readiness #11`).
- PDB, NetworkPolicy templates (`security #17`, `prod_readiness #6`).
- ServiceMonitor `release:` selector default + `scrapeTimeout` (`prod_readiness #7`).
- Resolved-alert path: increment `AlertsTotal`, skip `maybeArmInhibition` for `Resolved=true`, dedup resolves (`prod_readiness #9`, `reliability #4`, `reliability #20`).
- Implement PVC `>5m` gate as the comment claims — `internal/watchers/pvc.go:34-41` (`reliability #14`, `code_quality #15`).
- Register `AddFunc` on every watcher + startup state sweep (`reliability #16`, `code_quality #9`).
- Fix `NodeEvents` namespace + field selector — `internal/collectors/events.go:24-29` (`prod_readiness #19`, `reliability #28`).
- Replace `klog.Fatalf` with error returns + `klog.Exitf` at main (`code_quality #5`).
- Bump Go to 1.23, Alpine to 3.20+ (or distroless), `golang:1.23-alpine` in Dockerfile (`security #10`, `prod_readiness #24`).
- Add retry/backoff to `httpx.PostJSON` and Teams/webhook sinks (`reliability #12`, `prod_readiness #28`).
- Set `EndsAt = time.Now().Add(s.resolveTTL)` on first store in `Store.ShouldSend` (`reliability #11`).
- Couple `CleanOldHistory` cutoff to `muteWindow`; delete from `lastSent` on resolve (`reliability #10`, `code_quality #12`).
- Replace `pointer.Int64Ptr` with `ptr.To` (`code_quality #28`).
- Operations docs (`docs/operations.md`, `docs/troubleshooting.md`) and migration guide from `k8s-pod-restart-info-collector` (`prod_readiness #15`, `prod_readiness #16`).
- Rename `CONTRIBUTION.md` → `CONTRIBUTING.md`, fix NOTICE placeholder, rename `LICENSE-2.0.txt` → `LICENSE` (`code_quality #29`, `code_quality #30`, `code_quality #31`).
- Image: decide canonical repo, pin by digest, CI build+push, multi-arch (`security #11`, `prod_readiness #12`).
- Helm `replicas` + `strategy` configurable (`prod_readiness #11`).
- Helm `resources` defaults raised + `--namespace-scope` flag (`prod_readiness #13`).
- Structured logging (`klog.InfoS` or `slog`) + JSON option (`prod_readiness #14`).
- SSRF guard + scheme allowlist + `CheckRedirect` on generic webhook (`security #8`).
- Length-cap filter regex tokens + surface compile errors (`security #9`).
- `ReadHeaderTimeout` on metrics server (`security #12`, `prod_readiness #22`).
- Don't mutate shared `*Alert` from Slack sink (`reliability #29`).
- Pre-Dispatch wg.Add per outstanding send so shutdown waits for completion (`prod_readiness #3`).
- Surface `--v=` and verbosity in Helm values (`prod_readiness #14`).
- Capture `WaitForCacheSync` result + reflect in `/readyz` (`reliability #18`).
- Sweep interval + history retention configurable, jittered (`prod_readiness #27`).
- Webhook URL sanitization in error messages (`prod_readiness #23`).

### Nice-to-have
- HMAC signing on generic webhook (`security #16`).
- ExternalSecrets pattern documentation (`security #14`).
- `gofmt -s -w` pre-commit hook (`code_quality #22`, `code_quality #23`).
- Map PagerDuty severity from `alert.Severity` (`code_quality #21`).
- `corev1.ConditionTrue` consistency in `JobWatcher` (`code_quality #18`).
- Hard-coded magic numbers → named constants (`code_quality #19`).
- Default sinks in YAML, not `main.go` (`code_quality #26`).
- Inhibition arming semantics: arm on detection rather than on send (`prod_readiness #30`).
- `alert.Alert` constructor / builder consolidation (`code_quality #24`).
- Validate inhibition duration at config load with explicit error (`code_quality #25`).
- Anchor route matchers; reject invalid regex at load (`security #18`, `prod_readiness #21`, `code_quality #35`).
- `rbac.scope: cluster|namespace` flag (`prod_readiness #26`).

## Post-Launch Watch Items (first 7 days)

- **`alertkube_sink_errors_total{sink}` rate** — non-zero indicates a sink misconfiguration or downstream incident; if Slack stays >0, investigate the no-timeout regression (`reliability #1`).
- **Process restarts** — `kube_pod_container_status_restarts_total{pod=~"alertkube-.*"}`. Every restart re-floods (`prod_readiness #4`); alert if ≥1 in 24h.
- **`alertkube_alerts_suppressed_total{reason}`** — sudden jump in `muted` after a flap, in `silenced` (possible tenant abuse), or in `inhibited` (verify intent).
- **`alertkube_alerts_total` vs. apiserver event rate** — sustained divergence may indicate informer disconnect or handler panic (`reliability #9`); cross-check with `alertkube_event_processing_seconds` once it exists.
- **`alertkube_sink_send_seconds` p99 per sink** — a tail past 5s on Slack indicates an upcoming head-of-line block (`reliability #7`).
- **Memory RSS over 24h** — leaks in `Store.active` (`reliability #11`) and `Router.activeInhibits` (`reliability #3`) manifest as slow upward drift.
- **`/readyz` history during rolling updates** — confirm new replica only flips Ready after cache sync once `prod_readiness #2` is fixed; until then, monitor for alert gaps during upgrades.
- **Slack channel anomalies** — look for unexpected `alert-slack-channel` overrides in audit logs and Slack message metadata until `security #2` is gated.
- **`pods/log` blast radius** — sample 10 alerts/day and grep for secret patterns (`AKIA`, `sk-`, `ghp_`, `Bearer `) in `Pod Logs Before Restart`; until redaction lands, treat any sighting as a P1 incident.
- **Helm release values** — `helm get values --all alertkube` should never contain plaintext PD / Teams / webhook tokens; verify after `security #1` lands.

## Suggested Next Releases

### v2.1 — Reliability and Operability hardening
- Per-sink parallel dispatch with errgroup + per-sink timeout (`reliability #1`, `reliability #7`).
- Persistent dedupe state in ConfigMap or Lease (`prod_readiness #4`).
- Leader election + configurable `replicaCount` and update strategy (`prod_readiness #1`, `prod_readiness #11`).
- Retry / backoff for `httpx.PostJSON` and Teams/Slack/PagerDuty/webhook (`reliability #12`, `prod_readiness #28`).
- AddFunc + startup sweep for every watcher (`reliability #16`, `code_quality #9`).
- Event informer + apiserver QPS uplift instead of per-alert List (`prod_readiness #20`).
- Structured JSON logging via `log/slog` (`prod_readiness #14`).
- Rate-limit per sink + global circuit breaker with `reason="ratelimited"` (`prod_readiness #5`).
- Operations + troubleshooting + migration docs (`prod_readiness #15`, `prod_readiness #16`).

### v2.2 — Security and Extensibility
- HMAC signing on generic webhook + `Authorization` header support (`security #16`).
- SSRF guard + scheme allowlist + `CheckRedirect` (`security #8`).
- Pod-log redaction pipeline + opt-in namespace allowlist (`security #13`, `security #7`).
- Silence CR (CRD) replacing pod-annotation silence (`security #3`).
- Per-sink credential rotation without restart (`code_quality #7`, `prod_readiness #17`).
- `rbac.scope: cluster|namespace` and `--namespace-scope` flag (`prod_readiness #26`).
- NetworkPolicy + PodDisruptionBudget templates (`security #17`, `prod_readiness #6`).
- Configurable default sinks and YAML default routing (`code_quality #8`, `code_quality #26`).
- Map PagerDuty severity from `alert.Severity`; document Block Kit / MessageCard / Events v2 contracts (`code_quality #21`).
- Builder API for `Alert` with immutability post-dispatch (`code_quality #24`, `reliability #29`, `reliability #30`).
