# alertkube System Design

## Overview

**Problem.** Operators need fast, contextual notifications when Kubernetes workloads degrade (CrashLoopBackOff, OOMKill, ImagePullBackOff, Node NotReady, PVC stuck Pending, Job Failed, Deployment unhealthy). Stock Prometheus alerting requires authoring and tuning rules; kube-events spam lacks dedupe and routing. alertkube is a single-binary controller that watches the apiserver, builds rich `Alert` objects with on-demand context (events + previous container logs), and fans them out to Slack / PagerDuty / Teams / generic webhook / stdout with dedupe, mute, inhibit, silence, and resolve semantics.

**Scope.**
- Watch Pod, Node, Deployment, PVC, Job via shared informers.
- Emit one logical alert per `kind|namespace|name|reason` fingerprint (`internal/alert/alert.go:54`).
- Apply mute window, cross-kind inhibitions, label-matched silences, severity-based routing.
- Deliver via pluggable `Sink` interface (`internal/sinks/sink.go`).
- Ship Helm chart with ConfigMap, Secret, RBAC, optional ServiceMonitor.

**Non-goals.**
- No persistent state. Mute / inhibit / silence-fired timestamps are in-memory only (verified in audit; finding `prod_readiness #4`).
- No HA — `replicas: 1`, no leader election (`helm/templates/deployment.yaml:7`).
- No long-term storage of alert history.
- Not a replacement for Prometheus Alertmanager; complements it for Kubernetes-native conditions.
- Not multi-tenant safe today — pod-annotation-driven channel override / silence (`internal/sinks/slack.go:40`, `internal/router/router.go:56`) trusts the workload owner.

## Architecture Diagram

```
                         +-------------------------------------+
                         |        Kubernetes API Server        |
                         +------------------+------------------+
                                            |
                                List + Watch | (ClusterRole get/list/watch)
                                            v
                +-----------------------------------------------------------+
                |        client-go SharedInformerFactory (resync=0)         |
                |  pod | node | deployment | pvc | job  (main.go:59)         |
                +--+--------+--------+--------+--------+--------------------+
                   |        |        |        |        |
            UpdateFunc / (AddFunc missing for all - code_quality #9)
                   v        v        v        v        v
                +-----------------------------------------------------------+
                |  internal/watchers/*.go   (one struct per kind)            |
                |  - evaluate(oldObj,newObj,emit)                            |
                |  - calls collectors.{PodEvents,NodeEvents,Logs} inline     |
                +-----------------------------+-----------------------------+
                                              |
                                              v  *alert.Alert (built via alert.New)
                +-----------------------------------------------------------+
                |  emit(a)   (main.go:114-128, the makeEmitter closure)      |
                |    metrics.AlertsTotal.Inc()                               |
                |    if !store.ShouldSend(a) { suppressed=muted; return }    |
                |    sinks := router.Route(a)                                |
                |    if sinks==nil { suppressed=silenced/inhibited; return } |
                |    reg.Dispatch(ctx, a, sinks)                             |
                +-----------------------------+-----------------------------+
                                              |
                +-----------------------------v-----------------------------+
                |  internal/sinks/Registry.Dispatch (sequential loop)       |
                |  for _, name := range sinks { s.Send(ctx, a) }            |
                +--+---------------+----------+-----------+-----------+----+
                   |               |          |           |           |
                   v               v          v           v           v
                +-----+        +------+   +------+   +--------+   +------+
                |Slack|        |PagDy |   |Teams |   |Webhook |   |Stdout|
                |Block|        |Events|   |MsgCrd|   |JSON    |   |klog  |
                |Kit  |        |v2    |   |      |   |        |   |      |
                +-----+        +------+   +------+   +--------+   +------+

  ---------------------------- Resolve sweep (parallel) ----------------------------

                +-----------------------------------------------------------+
                |  goroutine runSweeper (main.go:130-143, 30s tick)         |
                |    store.SweepResolved(now)                               |
                |      for fp,a in active: if EndsAt set && now>=EndsAt:    |
                |        a.Resolved=true                                    |
                |        onResolved(a)  -- reg.Dispatch(ctx,a,r.Route(a))   |
                |    store.CleanOldHistory()  (cutoff hardcoded 1h)         |
                +-----------------------------------------------------------+

  ---------------------------- Side channels --------------------------------

   /metrics  /healthz  /readyz   <-- internal/metrics/metrics.go:42-52 (own goroutine)
```

## Component Responsibilities

### Watchers (`internal/watchers/`)

- **Purpose.** Translate informer Update events into `*alert.Alert` via per-kind logic.
- **Key types.** `PodWatcher` (`pod.go:42`), `NodeWatcher` (`node.go:27`), `DeploymentWatcher` (`deployment.go:23`), `PVCWatcher` (`pvc.go:23`), `JobWatcher` (`job.go:23`). All share the `Watcher` interface declared in `main.go` (Setup signature).
- **Inputs.** `informers.SharedInformerFactory`, `kubernetes.Interface`, `filter.Set`, `emit func(*alert.Alert)`.
- **Outputs.** Calls `emit(a)` per state transition; `a.Details["Pod Logs Before Restart"]`, `a.Details["Recent Events"]`, `a.Details["Deployment Status"]` populated via inline collector calls (`pod.go:117-152`).
- **Concurrency.** Runs on the informer's single processor goroutine. No goroutine offload, no panic recovery (`reliability #9`). `DeploymentWatcher` derefs `*dep.Spec.Replicas` without nil guard (`deployment.go:32`; `code_quality #6`).

### Collectors (`internal/collectors/`)

- **Purpose.** Best-effort context enrichment for alerts.
- **Key types.** `PodEvents` (`events.go:14`), `NodeEvents` (`events.go:24` — scoped to `metav1.NamespaceDefault` which is wrong on most clusters; `prod_readiness #19`), `PreviousContainerLogs` (`logs.go:14`, 50-line tail via `pointer.Int64Ptr(50)`), `DescribePod`, `DescribeDeployment`.
- **Inputs.** `kubernetes.Interface`, target name/namespace, `ctx`.
- **Outputs.** Strings appended to `a.Details`.
- **Concurrency.** Blocking REST calls executed inline from informer handlers (`reliability #17`). No QPS guard; lists all events in namespace and filters client-side.

### Alert / Store (`internal/alert/`)

- **Purpose.** Canonical alert object plus dedupe/mute/resolve state.
- **Key types.** `Alert` (`alert.go:54-93`), `Severity` enum, `Kind` enum, `Store` (`store.go:10`), `Store.ShouldSend` (`store.go:30`), `Store.SweepResolved` (`store.go:53`), `Store.CleanOldHistory` (`store.go:73`), `MatchLabels` (`alert.go:118`).
- **Inputs.** Fingerprint (`sha1(kind|ns|name|reason)[:12]`), mute window, resolve TTL, `onResolved` callback.
- **Outputs.** Bool from `ShouldSend`; resolved-alert dispatch from sweep.
- **Concurrency.** `sync.Mutex` over `lastSent` / `active` maps. `active` grows monotonically because `EndsAt` is never set on first store (`reliability #11`). `CleanOldHistory` cutoff hardcoded `1h` decoupled from `muteWindow` (`reliability #10`, `code_quality #12`).

### Router (`internal/router/`)

- **Purpose.** Decide sink names per alert and apply silence + inhibit policy.
- **Key types.** `Router` (`router.go:21`), `Router.Route` (`router.go:35`), `silenced` (`router.go:54-61`), `inhibited` (`router.go:63-87`), `maybeArmInhibition` (`router.go:89-98`).
- **Inputs.** `*alert.Alert`, routing rules, inhibition rules, silence rules.
- **Outputs.** `[]string` sink names, or `nil` when suppressed.
- **Concurrency.** Own `sync.Mutex` over `activeInhibits`. Map leaks (no expiry sweep — `reliability #3`, `code_quality #10`). First-match-wins routing semantics are undocumented and contradict README intuition (`code_quality #8`).

### Sinks / Registry (`internal/sinks/`)

- **Purpose.** Pluggable delivery to external systems.
- **Key types.** `Sink` interface (`sink.go`), `Registry` (`sink.go`), `Registry.Dispatch` (`sink.go:41-56`), `SlackSink` (`slack.go:22`), `PagerDutySink` (`pagerduty.go:18`), `TeamsSink` (`teams.go:15`), `WebhookSink` (`webhook.go:14`), `StdoutSink` (`stdout.go`).
- **Inputs.** `ctx`, `*alert.Alert`, sink names.
- **Outputs.** Outbound HTTP / klog write; metrics observations.
- **Concurrency.** Strictly sequential loop in `Dispatch`. Slack discards `ctx` and uses `http.DefaultClient` with no timeout (`slack.go:34,54`; `reliability #1` — blocker). No retry / backoff (`reliability #12`). Sinks read env vars at construction so secret rotation requires restart (`code_quality #7`).

### Filters (`internal/filter/`)

- **Purpose.** Pre-filter informer events by namespace and pod-name patterns.
- **Key types.** `Set` (`filter.go:13`), `Set.Matches` (`filter.go:38`), `Set.Blocks`.
- **Inputs.** Comma-separated tokens from `WATCHED_NAMESPACES`, `IGNORED_NAMESPACES`, `WATCHED_POD_NAME_PREFIXES`, `IGNORED_POD_NAME_PREFIXES`.
- **Outputs.** Bool.
- **Concurrency.** Read-only after construction. Each token is stored as BOTH literal prefix and compiled regex (`filter.go:25-30`; `code_quality #4`), creating surprising over-match.

### Templates (`internal/templates/`)

- **Purpose.** Format `*alert.Alert` into sink-specific payloads.
- **Key types.** `Build` (Slack Block Kit, `blockkit.go:53`), `truncate(val, 2800)` (`blockkit.go:41`).
- **Inputs.** `*alert.Alert`.
- **Outputs.** `[]slack.Block`, MessageCard struct, PagerDuty `V2Event`.
- **Concurrency.** Pure functions, no shared state.

### Metrics (`internal/metrics/`)

- **Purpose.** Prometheus instrumentation + health endpoints.
- **Key types.** `AlertsTotal` (counter), `AlertsSuppressed` (counter `reason` label), `SinkSendSeconds` (histogram `sink` label), `SinkErrors` (counter `sink` label), `ActiveAlerts` (gauge — registered but never set, `metrics.go:28-30`; `prod_readiness #8`, `code_quality #11`). `Serve(addr)` (`metrics.go:42-52`).
- **Inputs.** Stack-wide.
- **Outputs.** `/metrics`, `/healthz`, `/readyz`. `/readyz` always returns 200 even before cache sync (`prod_readiness #2`).
- **Concurrency.** Server goroutine; uses `http.ListenAndServe` with no `ReadHeaderTimeout` or graceful `Shutdown` (`security #12`, `prod_readiness #22`).

### Config (`internal/config/`)

- **Purpose.** YAML-first configuration with env-var fallback for legacy v1 keys.
- **Key types.** `Config` (`config.go`), `Load` (`config.go:71-127`), `Inhibition.DurationParsed` (`config.go:57-63`).
- **Inputs.** `--config` path, environment.
- **Outputs.** Typed `Config` struct passed to `Store`, `Router`, watchers, sinks.
- **Concurrency.** Loaded once at startup; sinks bind to env at the same time so reload requires `kubectl rollout restart` (`prod_readiness #17`). `os.ReadFile` errors silently swallowed when `--config` path is set (`config.go:73-79`; `code_quality #25`).

## Data Model

**`Alert` struct** (`internal/alert/alert.go:54-93`):

| Field | Type | Set By | Notes |
|---|---|---|---|
| `Fingerprint` | `string` | `alert.New` | `sha1(kind|ns|name|reason)[:12]` |
| `Kind` | `Kind` | watcher | `Pod`/`Node`/`Deployment`/`PVC`/`Job` |
| `Namespace`, `Name` | `string` | watcher | identifies the K8s object |
| `Reason` | `string` | watcher | `CrashLoopBackOff`, `OOMKilled`, ... |
| `Severity` | `Severity` | watcher | `critical`, `warning`, `info` |
| `Cluster` | `string` | mutated by `SlackSink.Send` (`slack.go:38`; race risk per `reliability #29`) | env-injected |
| `Summary` | `string` | watcher | human-readable single line |
| `NodeName` | `string` | pod watcher | for routing/inhibit `equal: [node]` |
| `Details` | `map[string]string` | watcher + collectors | injected verbatim into Slack/Teams body |
| `Labels` | `map[string]string` | watcher | also queried by `FieldValue` (`alert.go:111`) |
| `Annotations` | `map[string]string` | `mergeAnnotations(pod)` (`pod.go:162-173`) — merges pod labels into annotations (`security #15`, `code_quality #13`) |
| `StartsAt`, `EndsAt`, `Resolved` | timestamps + bool | store/sweep | `EndsAt` zero until explicitly set |

**`Severity`** (`internal/alert/alert.go`): `SeverityCritical`, `SeverityWarning`, `SeverityInfo`.
**`Kind`** (`internal/alert/alert.go`): typed-string enum for the five watched resources.

**Fingerprint rule.** `Fingerprint = hex(sha1(kind + "|" + namespace + "|" + name + "|" + reason))[:12]`. Stable across restarts for the same logical incident, used as `Store.lastSent`/`active` key and as PagerDuty `DedupKey`.

## Control Flow

### Hot path emit
1. Informer delivers `UpdateFunc(oldObj, newObj)` (`pod.go:42-60`, etc.).
2. Watcher applies `filter.Set.Matches`/`Blocks` (`filter.go:38`).
3. Watcher builds `*alert.Alert` via `alert.New` + watcher-specific reason inference.
4. Watcher invokes inline collectors (`PodEvents`, `PreviousContainerLogs`, `NodeEvents`) — synchronous REST calls.
5. Watcher calls `emit(a)` (`main.go:114-128`).
6. `emit` does `metrics.AlertsTotal.Inc()`, then `store.ShouldSend(a)`.
7. If muted → `metrics.AlertsSuppressed{reason=muted}.Inc()`, `store.Touch(a)`, return.
8. Else `sinks := router.Route(a)`; if nil → suppressed with reason `silenced` or `inhibited`, return.
9. Else `reg.Dispatch(ctx, a, sinks)` — sequential loop over sink names, each `Sink.Send(ctx, a)`.

### Mute
1. `Store.ShouldSend` reads `s.lastSent[fp]` under mutex.
2. If `time.Since(last) < s.muteWindow` → return false.
3. Else `s.lastSent[fp] = now`, `s.active[fp] = a`, return true.
4. Note: `EndsAt` is not set here (`reliability #11`) — `active` map leaks for first-fire-only alerts.

### Resolve sweep
1. `runSweeper` goroutine (`main.go:130-143`) ticks every `sweepInterval = 30s` (hardcoded — `code_quality #19`).
2. Each tick: `store.SweepResolved(now)` walks `s.active`; for entries where `!a.EndsAt.IsZero() && now.After(a.EndsAt)`:
   - Set `a.Resolved = true`.
   - Invoke `onResolved(a)` = `reg.Dispatch(ctx, a, r.Route(a))` (`main.go:51-55`).
   - Delete from `s.active`.
3. Then `store.CleanOldHistory()` evicts `lastSent` older than 1h (cutoff hardcoded — `reliability #10`).
4. Resolved dispatch bypasses `ShouldSend` and `AlertsTotal` (`prod_readiness #9`).
5. Resolved dispatch reuses captured top-level ctx so a sweep after `cancel()` is no-op (`reliability #2`).

### Inhibit
1. `Router.Route` calls `r.silenced(a)` then `r.inhibited(a)`.
2. `r.inhibited`: for each inhibition rule, compute `inhibitKey` from `a` plus the rule's `equal: [...]` keys; if `now.Before(r.activeInhibits[key])` → return true.
3. Independently, `r.maybeArmInhibition(a)` writes `r.activeInhibits[key] = now.Add(duration)` for any rule whose `source:` matchers match `a`. This runs unconditionally after silence/inhibit checks (`reliability #4`) — silenced sources still arm targets.
4. `activeInhibits` is never pruned (`reliability #3`).

### Silence
1. `r.silenced(a)` iterates configured silence rules; if `MatchLabels(matcher)` → suppress.
2. Additionally honors pod-annotation `alert-silence-until: <RFC3339>` from `a.Annotations` (`router.go:56-61`).
3. Because `mergeAnnotations(pod)` (`pod.go:162-173`) also merges pod **labels** into the annotation map, a workload owner can silence its own alerts by setting either a pod label or annotation (`security #2`, `security #15`).

## Configuration Surface

### YAML schema (file at `--config` or default)

```yaml
muteSeconds: 600                       # alert.Store.muteWindow
resolveTTLSeconds: 900                 # alert.Store.resolveTTL
cluster: prod-eu-1                     # injected as a.Cluster
watchedNamespaces:  ""                 # filter.Set tokens (comma-separated)
ignoredNamespaces:  "kube-system"
watchedPodNamePrefixes: ""
ignoredPodNamePrefixes: ""

routing:                               # router.Route, first-match-wins (code_quality #8)
  - name: critical
    match: { severity: critical }
    sinks: [pagerduty, slack]
  - name: warning-prod
    match: { severity: warning, namespace: "^prod-.*" }
    sinks: [slack]

silences:                              # router.silenced
  - match: { namespace: kube-system }

inhibitions:                           # router.inhibited
  - source: { kind: Node, reason: NotReady }
    target: { kind: Pod }
    equal: [node]
    duration: 10m                      # Inhibition.DurationParsed default 10m
```

### Environment fallback (legacy v1 keys, `config.go:85-127`)

| Env var | Maps to |
|---|---|
| `MUTE_SECONDS` | `muteSeconds` |
| `RESOLVE_TTL_SECONDS` | `resolveTTLSeconds` |
| `WATCHED_NAMESPACES` / `IGNORED_NAMESPACES` | filter tokens |
| `WATCHED_POD_NAME_PREFIXES` / `IGNORED_POD_NAME_PREFIXES` | filter tokens |
| `CLUSTER_NAME` | `cluster` |
| `SLACK_WEBHOOK_URL`, `SLACK_CHANNEL` | `SlackSink` (env-only) |
| `PAGERDUTY_ROUTING_KEY` | `PagerDutySink` |
| `TEAMS_WEBHOOK_URL` | `TeamsSink` |
| `GENERIC_WEBHOOK_URL` | `WebhookSink` |
| `METRICS_ADDR` | `metrics.Serve` |

### Per-resource annotations (pod metadata)

| Annotation | Consumer | Risk |
|---|---|---|
| `alert-slack-channel` | `SlackSink.Send` (`slack.go:40`) | `security #2` blocker — tenant-controlled channel override |
| `alert-silence-until` | `router.silenced` (`router.go:56`) | `security #3` high — tenant self-silencing |
| `runbook-url` | `templates.Build` (`blockkit.go:53`) | `security #4` high — phishing URL injection |

Pod **labels** are also merged in (`mergeAnnotations`, `pod.go:162-173`) — `security #15`.

## Deployment Topology

| Helm template | Purpose | Notes |
|---|---|---|
| `helm/templates/deployment.yaml` | Single Pod (`replicas: 1`, hardcoded — `prod_readiness #11`) | No `securityContext` hardening, no `terminationGracePeriodSeconds` (`security #5`) |
| `helm/templates/rbac.yaml` | ClusterRole `get/list/watch` on `nodes, pods, pods/log, events, persistentvolumeclaims, persistentvolumes` + apps/batch/autoscaling | `pods/log` cluster-wide → exfil channel via sinks (`security #7`) |
| `helm/templates/secret.yaml` | Slack URL only (`secret.yaml:1-10`) | PagerDuty / Teams / generic webhook URLs ship as plaintext `value:` env in Deployment (`security #1` blocker) |
| `helm/templates/configmap.yaml` | Mounts `config.yaml` | |
| `helm/templates/service.yaml` | ClusterIP for `:9090` metrics/health | |
| `helm/templates/servicemonitor.yaml` | Optional Prom Operator scrape config | Missing `release:` selector by default (`prod_readiness #7`) |
| Missing | PDB, NetworkPolicy (`security #17`, `prod_readiness #6`) | |

**Secret flow.** Slack URL pattern: either inline `slack.webhookUrl` (b64 into Secret) or external `slack.webhookUrlSecretKeyRef.{name,key}`. PagerDuty / Teams / generic webhook do NOT follow this pattern — flagged as `security #1` blocker.

**ServiceMonitor.** Default labels come from `alertkube.labels`; operators must override `metrics.serviceMonitor.labels` to add their Prom Operator selector (typically `release: kube-prometheus-stack`).

## Observability

### Metrics (`internal/metrics/metrics.go:11-31`)

| Metric | Type | Labels | Set By | Notes |
|---|---|---|---|---|
| `alertkube_alerts_total` | counter | `kind`, `severity`, `reason` | `emit` (`main.go:115`) | Not incremented for resolved-path dispatch (`prod_readiness #9`) |
| `alertkube_alerts_suppressed_total` | counter | `reason` (`muted`, `silenced`, `inhibited`) | `emit` | No `ratelimited` reason today (`prod_readiness #5`) |
| `alertkube_sink_send_seconds` | histogram | `sink` | `Registry.Dispatch` | |
| `alertkube_sink_errors_total` | counter | `sink` | `Registry.Dispatch` | No retry classification (`prod_readiness #28`) |
| `alertkube_active_alerts` | gauge | — | (none — bug) | Registered, never set (`prod_readiness #8`, `code_quality #11`) |

### Health endpoints (`internal/metrics/metrics.go:42-46`)

| Endpoint | Behavior |
|---|---|
| `/healthz` | Always 200 (liveness). |
| `/readyz` | Always 200 — even before `factory.WaitForCacheSync` returns (`prod_readiness #2`). |

### Log strategy
- All logs via `klog.Infof`/`Warningf`/`Errorf` (printf style), no structured fields, no JSON option (`prod_readiness #14`).
- `klog.Fatalf` used at startup for config-load and kube-client errors (`main.go:40,156,161`; `code_quality #5`).
- Webhook URLs may appear in `httpx.PostJSON` error strings (`prod_readiness #23`).

## Failure Modes & Recovery

| Failure | Symptom | Recovery |
|---|---|---|
| **Process restart** | All `lastSent`, `active`, `activeInhibits` lost. Every still-firing condition re-fires immediately (`prod_readiness #4`). | None today. Must be persisted to ConfigMap/Lease in future release. |
| **Sink outage (Slack hung)** | `SlackSink.Send` blocks forever (no timeout, no ctx — `reliability #1`). Sequential `Registry.Dispatch` blocks all subsequent sinks for this alert AND stalls the informer worker. | Manual: bounce pod. Code fix: switch to `slack.PostWebhookCustomHTTPContext` with `&http.Client{Timeout: 10s}`. |
| **Sink transient error** | `metrics.SinkErrors{sink}.Inc()`, alert dropped (`reliability #12`). | No retry/backoff; operator-visible only via dashboard. |
| **Informer disconnect / cache resync failure** | `factory.WaitForCacheSync` result discarded (`reliability #18`); `/readyz` still 200 (`prod_readiness #2`). | None. Operator must notice missing alerts. |
| **Slow sink head-of-line** | One slow sink delays others for same alert AND blocks informer (`reliability #7`). | Code fix: fan-out per sink with errgroup + per-sink timeout. |
| **Watcher panic** | `nil` deref (e.g. `dep.Spec.Replicas`, `deployment.go:32`) → no `defer recover()` → process exits via `runtime.HandleCrash` (`reliability #5,9`). | Restart loop. Code fix: nil guards + handler-level recovery. |
| **Inhibit map leak** | `Router.activeInhibits` grows unbounded (`reliability #3`). | Restart. Code fix: periodic expiry sweep. |
| **`active` map leak** | First-fire alerts that never repeat stay in `Store.active` forever (`reliability #11`). | Restart. Code fix: set `EndsAt = now + resolveTTL` on store. |

## Extension Points

### Adding a watcher
1. Create `internal/watchers/<kind>.go` implementing `Setup(factory, clientset, filter, emit)`.
2. Inside `Setup`, register `AddFunc` AND `UpdateFunc` (`code_quality #9` highlights missing AddFunc as a launch issue).
3. In `evaluate`, build `*alert.Alert` via `alert.New(kind, ns, name, reason, severity)`, populate `a.Details`, `a.Labels`, `a.Annotations`, `a.NodeName`, `a.Summary`.
4. Wire the new watcher into `buildWatchers` in `main.go`.
5. Wrap event-handler bodies in `defer recover()` until `reliability #9` is addressed globally.

### Adding a sink
1. Create `internal/sinks/<name>.go` implementing the `Sink` interface (`Name() string`, `Supports(Severity) bool`, `Send(ctx, *alert.Alert) error`).
2. Register in `main.go` via `reg.Register(newSink)`.
3. Use `httpx.PostJSON` for HTTP (`internal/httpx/httpx.go:13`) but supply a sink-specific `http.Client` if you need a timeout (note: `defaultClient` has `Timeout: 10s` but no `CheckRedirect` — `security #8`).
4. Add a Prometheus label value for `alertkube_sink_send_seconds{sink}`.
5. If the sink takes a credential, mirror the Slack secret pattern in `helm/templates/secret.yaml` (and fix the plaintext-env regression for existing sinks per `security #1`).

### Custom router rules
1. YAML `routing:` entries are evaluated first-match-wins (`router.go:46-51`); document this for users.
2. `match:` keys recognized by `Alert.FieldValue` (`alert.go:101-113`): `kind`, `severity`, `node`, `name`, `namespace`, `reason`, otherwise `Labels[key]`. Note `mergeAnnotations` puts pod labels into `Annotations`, so user-pod labels are NOT visible via `FieldValue` default branch (`code_quality #36`).
3. `namespace` and `reason` go through `matchOrRegex` (`alert.go:134-139`) which strips `.*` and falls back to `strings.Contains` — broken; treat your patterns as substrings until `code_quality #3` is fixed.

## Workflow / Sequence

End-to-end: a Pod enters CrashLoopBackOff → Slack #alerts-critical + PagerDuty incident.

```
 t   Actor               Action                                                       File:line
 --  ------------------  -----------------------------------------------------------  -------------------------
 1   kubelet             container exits non-zero; sets ContainerStatuses[i]
                         .State.Waiting.Reason = "CrashLoopBackOff"; apiserver
                         broadcasts Pod UPDATE event.

 2   client-go informer  Delivers UpdateFunc(oldPod, newPod) on processor goroutine.   pod.go:42

 3   PodWatcher.evaluate filter.Set.Matches(newPod.Namespace) -> true                   filter.go:38
                         filter.Set.Blocks(newPod.Name) -> false

 4   PodWatcher          Detects transition into Waiting:CrashLoopBackOff for a        pod.go:70-115
                         container; calls emitContainerAlert(newPod, st, emit).

 5   emitContainerAlert  a := alert.New(Pod, ns, name, "CrashLoopBackOff",              alert.go:54
                         SeverityCritical)
                         a.Summary = "Pod ... is in CrashLoopBackOff"
                         a.NodeName = newPod.Spec.NodeName
                         a.Labels["container"] = st.Name
                         a.Annotations = mergeAnnotations(newPod)                       pod.go:162

 6   collectors          a.Details["Recent Events"]   = PodEvents(ctx, ...)            events.go:14
                         a.Details["Pod Logs Before Restart"] =
                                       PreviousContainerLogs(ctx, ...)                 logs.go:14
                         a.Details["Node Events"]     = NodeEvents(ctx, ...)           events.go:24

 7   emit(a)             metrics.AlertsTotal{kind=Pod, severity=critical,               main.go:115
                                              reason=CrashLoopBackOff}.Inc()

 8   Store.ShouldSend    fp := a.Fingerprint
                         s.mu.Lock()
                         last, ok := s.lastSent[fp]
                         if !ok || time.Since(last) >= muteWindow:                     store.go:30
                            s.lastSent[fp] = now
                            s.active[fp]   = a
                            return true                                                 (returns true here)

 9   Router.Route        r.silenced(a)  -> false  (no matching silence,                router.go:54
                                                   no alert-silence-until annot.)
                         r.inhibited(a) -> false  (no NodeNotReady arming key)         router.go:63
                         For each route entry: a.MatchLabels(route.Match)              router.go:46
                            -> first match {severity: critical} -> [pagerduty, slack]
                         r.maybeArmInhibition(a)  (no source rule matches Pod here)    router.go:89

 10  Registry.Dispatch   for name in ["pagerduty","slack"]:                            sink.go:41
                            s := r.byName[name]; if !s.Supports(a.Severity) continue
                            start := time.Now()
                            err := s.Send(ctx, a)
                            metrics.SinkSendSeconds{sink=name}.Observe(elapsed)
                            if err != nil:
                               metrics.SinkErrors{sink=name}.Inc()

 11  PagerDutySink.Send  pd.ManageEventWithContext(ctx, &pd.V2Event{                   pagerduty.go:18
                            RoutingKey: routingKey,
                            Action:     "trigger",
                            DedupKey:   a.Fingerprint,
                            Payload:    &pd.V2Payload{
                              Summary:   a.Summary,
                              Severity:  "critical",      (hardcoded - code_quality #21)
                              Source:    a.NodeName,
                              Component: a.Name,
                              Class:     string(a.Kind),
                              CustomDetails: a.Details,
                            },
                         })

 12  SlackSink.Send      a.Cluster = s.cluster   (mutates shared *Alert -                slack.go:38
                                                  reliability #29)
                         channel := s.routeChannel(a)
                         if override := a.Annotations["alert-slack-channel"];           slack.go:40
                            override != "" { channel = override }
                         msg := &slack.WebhookMessage{
                            Channel: channel,
                            Blocks:  templates.Build(a),                                 blockkit.go:53
                         }
                         slack.PostWebhook(s.webhookURL, msg)                            slack.go:54
                         (ignores ctx; uses http.DefaultClient w/o timeout -            reliability #1
                          BLOCKER)

 13  Later: container    apiserver fires another UPDATE; PodWatcher's evaluate
     enters Running       no longer matches the CrashLoopBackOff reason.
                         No explicit "resolved" emit from the watcher; resolve
                         comes from the sweep ONLY if a.EndsAt was set, which it
                         is not (reliability #11).

 14  runSweeper (30s)    store.SweepResolved(now)                                        main.go:130
                         For active entries with EndsAt set + elapsed:
                            a.Resolved = true
                            onResolved(a) -> reg.Dispatch(ctx, a, r.Route(a))           main.go:51-55
                         store.CleanOldHistory()  (cutoff 1h - reliability #10)
                         (Note: AlertsTotal not incremented on resolve path -           prod_readiness #9
                          dashboards undercount.)
```

