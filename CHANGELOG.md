# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

* **sources:** experimental multi-cloud alert sources that poll provider
  control planes and flow through the same dedupe → route → group → sink
  pipeline as the Kubernetes watchers. Each service is an independent,
  off-by-default toggle:
  * **AWS** (18 sources): EKS, CloudWatch, EC2, ELBv2, RDS, DynamoDB,
    ElastiCache, S3, CloudTrail, ASG, KMS, EBS, Aurora, NAT, EFS, Route53,
    ACM, VPN. Credentials via the standard AWS chain (IRSA in-cluster).
  * **Azure** (6 sources): AKS, Monitor (Alerts Management), VMs, Storage,
    SQL, Redis. Credentials via DefaultAzureCredential (AKS Workload Identity).
  * **GCP** (4 sources): GKE, Monitoring (alert-policy posture), Compute,
    Cloud SQL. Credentials via Application Default Credentials (GKE Workload
    Identity).
* **rules:** user-authored correlation rules engine (`internal/rules`)
  evaluated against the live alert stream. Three shapes — `Count` (storm /
  N-within-window), `All` (composite AND), `Absent` (heartbeat /
  dead-man's-switch). Derived alerts run through the standard pipeline and
  cannot trigger themselves.

> **Stability:** cloud sources and the rules engine are **experimental**. They
> are covered by unit tests against recorded SDK responses but have not yet
> been validated against live cloud accounts at scale. The Kubernetes watchers
> remain the stable, production-proven core.

## [0.2.4](https://github.com/aryasoni98/alertkube/compare/v0.2.3...v0.2.4) (2026-06-19)


### Added

* **watchers:** alert on non-OOM SIGKILL (ContainerKilled) + termination cause ([#17](https://github.com/aryasoni98/alertkube/issues/17)) ([b7da111](https://github.com/aryasoni98/alertkube/commit/b7da111274dd59df3158a0742bd33cbf3a63fa6e))

## [0.2.3](https://github.com/aryasoni98/alertkube/compare/v0.2.2...v0.2.3) (2026-06-18)


### Added

* **website:** SEO, performance & a11y upgrade for landing + docs site ([41be82e](https://github.com/aryasoni98/alertkube/commit/41be82e0dd88444d8b098b3f733e057606417dea))
* **website:** SEO, performance, and a11y upgrade for landing + docs site ([ab1d058](https://github.com/aryasoni98/alertkube/commit/ab1d058ae6b7c8f50d40d6d89bb36aae02b9949b))


### Fixed

* harden controller shutdown, filtering, receiver, and delete handling ([8ce30ef](https://github.com/aryasoni98/alertkube/commit/8ce30ef6038d63c7bd2a3764faecbe003c0926c1))


### Changed

* extract reusable helpers, render all alert details, drop dead code ([5efb9ea](https://github.com/aryasoni98/alertkube/commit/5efb9ead8c66860d1beb7b7d7bc880bd82077935))
* **sinks:** share severity-tier mapping; drop dead WLogo primitive ([09eab83](https://github.com/aryasoni98/alertkube/commit/09eab83354e975d883b3b59e8ae44b33794b3ee1))
* **sinks:** share severity-tier mapping; drop dead WLogo primitive ([833d7d7](https://github.com/aryasoni98/alertkube/commit/833d7d724be85b403a114325030ca02992bcc782))

## [v0.2.2] - 2026-06-15

Project-maturity and CNCF-readiness work (no controller behavior change). See
[`docs/ROADMAP.md`](docs/ROADMAP.md) Phases 0–2.

### Added

- **Governance & community**: `GOVERNANCE.md` (roles, lazy-consensus decisions,
  contribution ladder, neutrality), `MAINTAINERS.md`, `ADOPTERS.md`, ADRs under
  `docs/decisions/` (client-go vs controller-runtime, MkDocs choice, ConfigMap
  state backend). `CODE_OF_CONDUCT.md` replaced with the CNCF Community Code of
  Conduct. `CONTRIBUTING.md` expanded with DCO mechanics, Conventional Commits,
  and a response-time SLA.
- **Contributor experience**: issue forms (`.github/ISSUE_TEMPLATE/`), a DCO
  sign-off CI check (`dco.yml`), a label set + sync workflow, and a curated
  [good first issues](docs/good-first-issues.md) backlog.
- **Security & supply chain**: OpenSSF Scorecard workflow, `SECURITY-INSIGHTS.yml`,
  a branch-protection setup script, and an OpenSSF best-practices tracker.
- **Documentation site**: MkDocs Material site under `docs-site/` organized by
  Diátaxis (15 pages — tutorials, how-to, reference, explanation) plus an
  architecture overview; built with `--strict` link checking in CI (`docs.yml`).
- **Release engineering**: release-please workflow + config for automated
  versioning/changelog from Conventional Commits.
- **Testing**: native fuzz targets (`FuzzComputeFingerprint`, `FuzzMatchOrRegex`,
  `FuzzLoad`), a CI coverage gate, a fuzz-smoke CI job, and `docs/TESTING.md`.
- **Performance**: Go benchmarks for fingerprint/matching/routing, a load-test
  harness (`test/load/`), and `docs/PERFORMANCE.md`.
- **E2E**: kind-based smoke + HA leader-election workflow across a Kubernetes
  version matrix (`e2e.yml`), with a chainsaw scaffold under `test/e2e/`.
- **Chart**: optional self-health `PrometheusRule` (`prometheusRule.enabled`),
  Artifact Hub metadata (`Chart.yaml` annotations, `artifacthub-repo.yml`), a
  chart `README.md`, and chart-testing (`ct`) lint in CI.
- **Tooling**: root `Makefile`, optional `.pre-commit-config.yaml`, and design
  docs for a possible CRD API and a ConfigMap size audit.
- **Chart docs**: `helm-docs`-generated `helm/README.md` from `# --`-annotated
  `values.yaml` and a `README.md.gotmpl` template, with a CI drift check and a
  `make helm-docs` target so the values reference is always current.
- **Lint**: expanded the golangci-lint set (`bodyclose`, `errorlint`, `noctx`,
  `nilerr`, `durationcheck`, `wastedassign`, `usestdlibvars`, `predeclared`).
- **Tests**: `internal/metrics` HTTP server coverage (readiness, dynamic
  handlers, all routes) — package went from 0% to 96.8%.

### Fixed

- **httpx**: retry backoff now unwraps the status error with `errors.As` instead
  of a direct type assertion, so a wrapped `*statusError` is still honored.

### Changed

- **metrics**: extracted the route wiring into a testable `buildMux` helper (no
  behavior change).

## [v0.2.1] - 2026-06-12

### Added

- Operations, troubleshooting, migration, and contributing docs refreshed for v0.2.
- GitHub Pages landing page updated to v0.2 (nine watchers, eight sinks, new features).
- Shared `handleCurrent` helper for state-based watchers (`internal/watchers/watcher.go`).
- Shared sink utilities (`internal/sinks/util.go`).

## [v0.2.0] - 2026-06-12

### Security

- `runbook-url` scheme validation (https-only, length-capped) now applies to the Teams, Discord, and Telegram sinks via the shared `templates.SafeRunbookURL` guard - previously only the Slack Block Kit button was protected (`internal/templates/blockkit.go`, `internal/sinks/{teams,discord,telegram}.go`).
- Pod **labels** are no longer back-filled into the control annotation keys (`alert-silence-until`, `alert-slack-channel`, `runbook-url`); only real annotations drive silencing, channel overrides, and runbook links. Labels are commonly writable by lower-privilege automation, so a label-writer could previously self-silence alerts or inject links (`internal/watchers/pod.go`).
- Startup now logs a prominent warning when the Alertmanager receiver is enabled without `ALERTKUBE_RECEIVER_TOKEN` - that combination accepts unauthenticated alert injection on the metrics port (`main.go`).

### Added

- **Four new watchers**: DaemonSet (unavailable on scheduled nodes), StatefulSet (ready < desired, generation-guarded), CronJob (`CronJobMissingSuccess` - a full schedule interval passed without a successful run, detected without cron parsing; plus suspend transitions), HPA (`HPAMaxedOut` - pinned at maxReplicas while ScalingLimited). The chart's RBAC already granted these resources (`internal/watchers/{daemonset,statefulset,cronjob,hpa}.go`).
- **Escalation rules** (`escalations` config): still-unresolved alerts matching a rule re-dispatch to extra sinks after `afterMinutes`, tagged `[ESCALATED]`, at most once per alert lifetime; marks clear on resolve. New `alertkube_escalations_total` counter (`internal/alert/store.go`, `main.go`).
- **Alertmanager-compatible receiver** (`receiver.enabled`): `POST /api/v1/alerts` on the metrics port accepts Alertmanager webhook payloads (version 4) and runs them through the same dedupe/grouping/routing/sink pipeline; upstream fingerprints are preserved for dedupe alignment, resolves forget local state to avoid duplicate synthetic resolves. Optional bearer auth via `ALERTKUBE_RECEIVER_TOKEN`. New `alertkube_received_alerts_total{status}` counter (`internal/receiver`).
- **`GET /api/alerts`**: read-only JSON of the active alert set plus a 200-entry ring of recent fires/resolves (Details stripped). Gate with the chart's NetworkPolicy `ingressFrom` on multi-tenant clusters (`internal/metrics`, `internal/alert/store.go`).
- **Grafana dashboard** at `docs/grafana-dashboard.json`: active alerts, severity rates, suppression breakdown, sink latency p95, storm indicator, receiver intake.
- **Alert grouping / storm folding** (`grouping.*` config, off by default): the first alert of a group (default identity: kind+namespace+reason+severity) dispatches immediately; later same-group alerts within the window collapse into one summary message ("4 more Pod CrashLoopBackOff alerts…"). Resolves fold into their own summaries. PagerDuty/Opsgenie still receive every member resolve so incidents close, and never receive summaries (`internal/group`, `main.go`).
- **New sinks**: Opsgenie (Alert API v2, alias=fingerprint dedupe, close-on-resolve, P1/P3 mapping, EU endpoint via `OPSGENIE_API_URL`), Discord (embeds), Telegram (Bot API, HTML-escaped) (`internal/sinks/{opsgenie,discord,telegram}.go`, `helm/`).
- **Slack bot-token mode**: set `slack.botToken` (`SLACK_BOT_TOKEN`) to send via `chat.postMessage` - per-severity channel routing that actually works with modern Slack apps. Takes precedence over the webhook URL (`internal/sinks/slack.go`).
- **Async enrichment pool**: events/logs API calls moved off the informer handler into a bounded pool (4 workers); under storm saturation alerts ship without enrichment instead of stalling event processing (`internal/watchers/pod.go`).
- **Teams Adaptive Cards**: the Teams sink now sends the `{type: message, attachments: [adaptive card]}` envelope required by Power Automate Workflows webhooks (Office 365 connectors are retired); includes FactSet and a Runbook button (`internal/sinks/teams.go`).
- **State persistence**: active alerts and mute history snapshot to a ConfigMap (`persistence.*` config, on by default in the Helm chart). A controller restart now still sends pending resolves - no more dangling PagerDuty incidents - and does not re-page standing conditions. Saves are skipped when nothing changed; a final save runs on shutdown (`internal/persist`, `internal/alert/snapshot.go`, `main.go`, `helm/`).
- `behavior.disableLogCollection`: turn off previous-container log enrichment for environments that must not forward workload logs to chat/paging sinks (`internal/config`, `internal/watchers/pod.go`).
- `behavior.disableAnnotationSilences`: ignore `alert-silence-until` annotations so workload authors cannot self-silence; config-file silences still apply (`internal/router`).
- `alertkube_dispatch_inflight` gauge: sink sends in flight, including time queued on the rate limiter - pins high when an alert storm is about to drop messages (`internal/metrics`, `internal/sinks`).
- Log redaction now also masks JWTs and URL-embedded basic-auth credentials (database connection strings, git remotes) (`internal/collectors/logs.go`).
- Release images are cosign-signed (keyless) and ship an SPDX SBOM attached to the GitHub release (`.github/workflows/release.yml`).

### Fixed

- **Inhibition expiry during long outages**: muted re-fires of a source alert (e.g. `NodeNotReady` inside its mute window) now re-arm their inhibitions; previously the inhibition expired after `duration` and the dependent pod-alert storm leaked through mid-outage (`main.go`, `internal/router/router.go`).
- Rate-limited sink drops now log at warning with the alert identity instead of a V(2) whisper (`internal/sinks/sink.go`).

### Changed

- Alert fingerprints now use sha256 (truncated) instead of sha1. Identity semantics are unchanged, but fingerprints differ across the upgrade: a condition firing during the rollout may page once more than expected as the old mute record no longer matches.

## [v0.1.0] - 2026-06-10

### Fixed (this release)

- **Inverted restart-count gate**: pods past `ignoreRestartCount` had *all* detection (CrashLoopBackOff / OOMKilled / ImagePull) suppressed - the noisiest workloads never alerted. The gate now applies only to per-restart delta alerts (`internal/watchers/pod.go`).
- **Sink retries**: Slack, PagerDuty, and the generic webhook now retry transient failures (429 / 5xx / network) with jittered exponential backoff honoring `Retry-After`; previously a single blip dropped the alert permanently (`internal/httpx`, `internal/sinks/{slack,pagerduty,webhook}.go`).
- **Mute only on delivery**: dedupe state is rolled back when every sink fails, so the next firing retries instead of being silenced for the full mute window (`internal/alert/store.go`, `internal/sinks/sink.go`, `main.go`).
- **Data race on shared alerts**: sinks now receive copies; the resolve sweeper and `Touch` no longer mutate structs concurrently read by sink goroutines (`internal/alert/store.go`, `main.go`).
- **Resolve delivery**: resolved alerts bypass the `Supports` severity gate, silences, and inhibitions - resolves always follow their trigger (no more dangling PagerDuty incidents), never arm inhibitions, and no longer pollute suppression metrics (`internal/router/router.go`, `internal/sinks/sink.go`).
- **Namespace filters** now apply to Deployment / PVC / Job watchers, not just Pods (`internal/watchers/`).
- **Config fails fast**: unreadable `--config` path is a hard error (was silently ignored); load-time validation rejects unknown sink names, bad silence timestamps, bad inhibition durations, and non-positive windows (`internal/config/config.go`).
- **UTF-8-safe Slack truncation**: log truncation no longer splits multi-byte runes, which produced invalid payloads Slack rejects wholesale (`internal/templates/blockkit.go`).
- **Leader election rollout deadlock**: followers now report Ready (standby is healthy), unblocking `RollingUpdate maxUnavailable: 0` (`main.go`).
- **`rbac.scope=namespace` crashloop**: namespace mode now scopes informers via `WATCH_NAMESPACE` and disables the node watcher instead of failing cache sync (`main.go`, `helm/`).
- **Helm**: ConfigMap/Secret checksum annotations trigger rollouts on config change; template fails when `replicaCount > 1` without leader election; NetworkPolicy requires explicit `apiServer.cidrs` (the old rule cut the controller off from the API); image points at `ghcr.io` with tag defaulting to the chart `appVersion`.
- **Release pipeline**: Helm chart actually publishes to `oci://ghcr.io/<owner>/charts` with versions derived from the tag; GHCR auth uses `GH_PAGE`; `:latest` publishes on tags; GitHub release gated on Trivy + Helm jobs; `build.sh` multi-arch push fixed.

### Added (this release)

- `behavior.startupGraceSeconds`: mutes informer initial-sync re-fires of standing conditions after a controller restart (`main.go`, `internal/config`).
- `severityOverrides`: remap alert severities before dedupe/routing with routing-rule match semantics, first match wins (`internal/config`, `main.go`).
- `sinkRates`: per-sink token-bucket overrides, wired to the previously-orphaned `Registry.SetRate` (`internal/config`, `main.go`).
- `behavior.pvcPendingSeconds`: configurable PVC Pending threshold (was a 5-minute constant) (`internal/watchers/pvc.go`).
- `SECURITY.md` with private vulnerability reporting policy.
- Tests: watchers 0 → 58 %, sinks 18 → 48 %, config 0 → 81 %, templates 91 % coverage; CI pins golangci-lint v1.64.8, validates charts with kubeconform, smoke-builds the Docker image on PRs.
- Runtime image switched from `alpine` to `gcr.io/distroless/static:nonroot`.

### Removed (this release)

- Dead `behavior.groupWaitSeconds` config key (parsed, documented, never implemented). Alert grouping is future work; the README no longer claims it.

### Security

- Slack channel-override annotation now validated against `^#?[a-z0-9._-]{1,80}$`; invalid values logged and ignored (`internal/sinks/slack.go`).
- `runbook-url` annotation requires `https://` and rejects whitespace / quote characters; non-conforming URLs are dropped from the Slack Block Kit button (`internal/templates/blockkit.go`).
- PagerDuty, Teams, and generic webhook credentials are now sourced via `secretKeyRef` (inline values land in a managed Secret, or use an external `*.SecretKeyRef`). No credential is rendered as plaintext `env.value` (`helm/templates/{deployment,secret}.yaml`, `helm/templates/_helpers.tpl`).
- Container `securityContext` defaults to `runAsNonRoot`, `runAsUser: 65532`, `readOnlyRootFilesystem: true`, `capabilities.drop: [ALL]`, plus matching `podSecurityContext` (`helm/values.yaml`, `helm/templates/deployment.yaml`).
- Dockerfile final stage now creates and runs as the `nonroot:65532` user, builds with `-trimpath -ldflags="-s -w"`, and uses `golang:1.23-alpine` + `alpine:3.20`.
- Pod logs streamed into `Details["Pod Logs Before Restart"]` now pass through `collectors.RedactSecrets` - AWS keys, GitHub / Slack / OpenAI tokens, Bearer headers, common `password|secret|token=` key-value pairs, and URL query-string credentials are masked before reaching sinks (`internal/collectors/logs.go`).

### Fixed

- `DeploymentWatcher` no longer panics on a `nil` `spec.replicas` pointer (`internal/watchers/deployment.go`).
- `Slack` sink honors the caller `context.Context` and uses a 10 s-bound `http.Client` instead of `http.DefaultClient` (`internal/sinks/slack.go`).
- `Router.activeInhibits` no longer grows unbounded; expired keys are pruned on every lookup (`internal/router/router.go`).
- `Store.ShouldSend` now sets `EndsAt` on first store so single-fire alerts are eligible for the resolve sweep (`internal/alert/store.go`).
- `Store.SweepResolved` deletes the resolved fingerprint from `lastSent` so a recurring incident is not silenced by the stale mute entry (`internal/alert/store.go`).
- `Store.CleanOldHistory` cutoff is now `max(2 * muteWindow, 10 m)` instead of the hardcoded `1 h` so non-default mute windows behave predictably (`internal/alert/store.go`).
- `/readyz` returns `503` until informer caches finish syncing; on partial sync the controller fails fast (`internal/metrics/metrics.go`, `main.go`).
- Metrics HTTP server now uses an explicit `*http.Server` with `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, and graceful `Shutdown` on SIGTERM (`internal/metrics/metrics.go`, `main.go`).
- `Registry.Dispatch` fans out per sink concurrently with a 15 s per-sink timeout and `defer recover()`; a slow Slack endpoint no longer head-of-lines PagerDuty or the informer worker (`internal/sinks/sink.go`).
- `alert.MatchLabels` now uses anchored regex (`^pattern$`) instead of the substring shim, so `namespace: prod-.*` no longer matches `dev-prod-tools`. Invalid patterns fall back to literal equality (`internal/alert/alert.go`).
- `collectors.NodeEvents` lists events from all namespaces with a `involvedObject.kind=Node,involvedObject.name=<name>` server-side selector instead of only `default` (`internal/collectors/events.go`).
- `PVCWatcher` now honors the documented 5-minute Pending threshold (`internal/watchers/pvc.go`).
- `JobWatcher` compares `cond.Status` against `corev1.ConditionTrue` instead of the literal string (`internal/watchers/job.go`).
- `PreviousContainerLogs` migrated to `k8s.io/utils/ptr.To` (deprecated `pointer.Int64Ptr` removed) (`internal/collectors/logs.go`).

### Added

- `alertkube_active_alerts` gauge is now updated from `Store` whenever the active set changes (`internal/alert/store.go`, `main.go`).
- Optional `PodDisruptionBudget` template gated behind `podDisruptionBudget.enabled` (`helm/templates/pdb.yaml`, `helm/values.yaml`).
- Optional `NetworkPolicy` template gated behind `networkPolicy.enabled` with configurable `ingressFrom` and `extraEgress` (`helm/templates/networkpolicy.yaml`, `helm/values.yaml`).
- Every watcher now registers `AddFunc` in addition to `UpdateFunc`, so initial-sync state (pods already in CrashLoopBackOff, Failed Jobs, Lost PVCs, NotReady Nodes) emits an alert immediately after cache sync (`internal/watchers/*.go`).
- Shared `watchers.recoverHandler` wraps every informer handler with `defer recover()` so a nil-deref or panic in collector code does not crash the controller (`internal/watchers/watcher.go`, all watchers).
- `httpx.PostJSON` now retries transient failures (408 / 425 / 429 / 5xx + network errors) with exponential backoff and full jitter; honors `Retry-After`; redacts the URL path/query in error strings via `sanitizeURL` (`internal/httpx/httpx.go`).
- Optional HMAC-SHA256 signing on the generic webhook sink. When `GENERIC_WEBHOOK_SECRET` is set, every POST carries `X-Alertkube-Signature: sha256=<hex>` and `X-Alertkube-Timestamp` so receivers can authenticate and reject replay (`internal/sinks/webhook.go`).
- Sink credentials (`SLACK_WEBHOOK_URL`, `PAGERDUTY_ROUTING_KEY`, `TEAMS_WEBHOOK_URL`, `GENERIC_WEBHOOK_URL`, `GENERIC_WEBHOOK_SECRET`) are now read on every `Send` instead of at constructor time, so Secret rotation propagates without a pod restart (`internal/sinks/{slack,pagerduty,teams,webhook}.go`).
- PagerDuty sink severity now maps from `alert.Severity` (`critical` / `warning` / `info`) instead of being hardcoded `critical`, so a non-critical routing rule that opts into PagerDuty no longer mis-tiers the page (`internal/sinks/pagerduty.go`).
- Leader election via `coordination.k8s.io/v1` Lease (15 s lease / 10 s renew / 2 s retry). Disabled by default; enable with `leaderElection.enabled=true`. The follower keeps `/metrics` and `/healthz` serving but `/readyz` returns 503 until it acquires the lease (`main.go`, `internal/metrics/metrics.go`).
- Helm `replicaCount` is now configurable. With leader election off, `strategy: Recreate` is used to avoid duplicate dispatches across rollout. With it on, `strategy: RollingUpdate maxSurge:1 maxUnavailable:0` so leadership transfers without an alert gap (`helm/templates/deployment.yaml`, `helm/values.yaml`).
- Helm `rbac.scope: cluster | namespace`. `namespace` mode ships a namespace-scoped `Role`/`RoleBinding` instead of the cluster-wide pair, dropping `nodes`/`persistentvolumes` (`helm/templates/rbac.yaml`, `helm/values.yaml`).
- Lease RBAC (`coordination.k8s.io/leases` get/list/watch/create/update/patch/delete + `events` create/patch) is added in `leaderElection.namespace` only when leader election is enabled (`helm/templates/rbac.yaml`).
- Per-sink token-bucket rate limiter (`golang.org/x/time/rate`, default 1 rps with burst 5) in `Registry.Dispatch`; rate-limited drops are accounted as `alertkube_alerts_suppressed_total{reason="ratelimited"}` (`internal/sinks/sink.go`).
- `buildClient` retries kube-client initialization with exponential backoff up to 30 s instead of `klog.Fatalf` on first transient failure (`main.go`).
- Unit tests for `internal/alert`, `internal/filter`, `internal/router`, `internal/collectors`, `internal/httpx`, and `internal/sinks` (table-driven, race-enabled).
- `docs/OPERATIONS.md` (SLOs, PrometheusRule, dashboards, runbooks, capacity planning, upgrade procedure).
- `docs/TROUBLESHOOTING.md` (symptom → cause → fix table).
- `docs/MIGRATION-FROM-V1.md` (env-var mapping + step-by-step upgrade from `k8s-pod-restart-info-collector`).
- `.github/PULL_REQUEST_TEMPLATE.md`, `.github/CODEOWNERS`, `CODE_OF_CONDUCT.md` (Contributor Covenant 2.1).
- `CONTRIBUTING.md` (renamed from `CONTRIBUTION.md`) with DCO-based contribution workflow.

## [v0.0.1] - 2026-06-09

Initial release.

### Added

- Multi-resource watchers: Pod, Node, Deployment, PersistentVolumeClaim, Job.
- Severity model (`critical` / `warning` / `info`) with distinct colors and emoji.
- Slack sink with Block Kit message templates (header, fields, summary, contextual logs, runbook button).
- PagerDuty sink (Events API v2) for critical-only paging with fingerprint dedupKey.
- Microsoft Teams sink with MessageCard payload.
- Generic JSON webhook sink for custom integrations.
- Stdout sink for local development.
- Per-severity Slack channel routing.
- YAML-first config: routing rules, inhibitions, silences, filters.
- Fingerprint-based dedupe and mute window.
- Resolve detection: synthetic resolved alert when fingerprint stops firing past TTL.
- Cross-kind inhibitions (e.g. `NodeNotReady` silences pod alerts on that node).
- Time-bounded silences via config or `alert-silence-until: RFC3339` annotation.
- Prometheus metrics endpoint (`/metrics`) with counters, histograms, and gauges.
- Health endpoints (`/healthz`, `/readyz`).
- Helm chart with optional ServiceMonitor template for Prometheus Operator.
- Pod-level annotations: `alert-slack-channel`, `alert-silence-until`, `runbook-url`.
- Namespace and pod-name-prefix filters (literal or regex).
