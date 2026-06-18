# alertkube — End-to-End Playground Audit

**Date:** 2026-06-18
**Cluster:** Docker Desktop Kubernetes `docker-desktop`, single node, v1.34.1
**Image under test:** `alertkube:e2e` (built locally from this branch, `pullPolicy: Never`)
**Install:** Helm chart `./helm`, release `alertkube`, namespace `alertkube`
**Sinks exercised:** Slack (incoming webhook) + stdout
**Result:** Pipeline works end-to-end. **1 HIGH bug found and fixed**, 1 MEDIUM reliability gap, several LOW/observability + hardening items.

> ⚠️ **Security — act now:** the Slack webhook URL was passed on the command line in plaintext and is now stored in the `alertkube` Secret and in shell history. **Rotate it** in Slack after testing. It is not committed to git (injected via `--set`), but treat it as exposed.

---

## 1. What was set up

| Step | Command (abridged) | Outcome |
| --- | --- | --- |
| Target the right cluster | `kubectl config use-context docker-desktop` | ✅ Verified single-node `docker-desktop`. **The active context was `prod-banyan` (real EKS) — switched away before any apply.** |
| Build image | `docker build -t alertkube:e2e .` | ✅ 60.9 MB distroless image (after pre-pulling `golang:1.25-alpine` + `distroless/static` — the registry pull timed out on first try) |
| Install | `helm upgrade --install alertkube ./helm -n alertkube -f install-values.yaml --set-string slack.webhookUrl=…` | ✅ Pod Ready, receiver enabled, metrics up |
| Demo workloads | `kubectl apply -f demo-workloads.yaml` | ✅ 6 workloads in `alertkube-demo` |

Artifacts in `test/playground/`:
- `install-values.yaml` — Helm values tuned for fast, visible alerts (not prod).
- `demo-workloads.yaml` — the 6 dummy workloads.
- `AUDIT.md` — this report.

The values were tuned for a fast demo: `startupGraceSeconds: 0`, `resolveTTLSeconds: 60`, `pvcPendingSeconds: 30`, `muteSeconds: 120`, routing every severity to `slack` **and** `stdout` so dispatch is visible in pod logs.

---

## 2. Dummy services & test matrix

| # | Workload | Intended condition | Alert(s) observed | Sev | Verdict |
| --- | --- | --- | --- | --- | --- |
| 1 | `crashloop-api` (Deploy+Svc) | exits 1, restarts forever | `ContainerRestart`, `CrashLoopBackOff`, `DeploymentUnavailable` | warn→crit | ✅ |
| 2 | `imagepull-web` (Deploy+Svc) | bad image tag | `ErrImagePull`, `ImagePullBackOff`, `DeploymentUnavailable` | warn | ✅ |
| 3 | `oom-cache` (Deploy) | allocates 250M with 64Mi limit | `OOMKilled`, then `CrashLoopBackOff` | crit | ✅ |
| 4 | `batch-report` (Job) | exits 2, backoffLimit 1 | `JobFailed` | crit | ✅ |
| 5 | `payments-data` (PVC) | non-existent storageClass | `PVCPending` | warn | ⚠️ fired only after a real Update event — see Finding F2 |
| 6 | `healthy-frontend` (Deploy+Svc) | **control — must not alert** | none (transient `DeploymentUnavailable` during rollout, auto-resolved) | — | ✅ no false page |
| 7 | Alertmanager receiver | `POST /api/v1/alerts` | `External / HighLatency` flowed through pipeline | crit | ✅ |

### Final metrics snapshot (`/metrics`)

```
alertkube_alerts_total{kind="Deployment",reason="DeploymentUnavailable",severity="warning"} 16
alertkube_alerts_total{kind="External",reason="HighLatency",severity="critical"} 1
alertkube_alerts_total{kind="Job",reason="JobFailed",severity="critical"} 1
alertkube_alerts_total{kind="PersistentVolumeClaim",reason="PVCPending",severity="warning"} 1
alertkube_alerts_total{kind="Pod",reason="ContainerRestart",severity="warning"} 6
alertkube_alerts_total{kind="Pod",reason="CrashLoopBackOff",severity="critical"} 12
alertkube_alerts_total{kind="Pod",reason="ErrImagePull",severity="warning"} 4
alertkube_alerts_total{kind="Pod",reason="ImagePullBackOff",severity="warning"} 5
alertkube_alerts_total{kind="Pod",reason="OOMKilled",severity="critical"} 6
alertkube_alerts_suppressed_total{reason="muted"} 26       # dedupe/mute working
alertkube_received_alerts_total{status="firing"} 1         # receiver working
alertkube_sink_send_seconds_count{result="ok",sink="slack"}  48
alertkube_sink_send_seconds_count{result="ok",sink="stdout"} 48
# alertkube_sink_errors_total — ABSENT (zero sink errors after the fix)
```

### Capabilities verified

- ✅ **Multi-resource watchers** — Pod, Deployment, Job, PVC, plus External (receiver). 9 distinct alert reasons across 5 kinds.
- ✅ **Severity tiers** — warning/critical correctly assigned.
- ✅ **Dedupe / mute** — 26 alerts suppressed (`reason=muted`); re-fires spaced at the 120s mute window.
- ✅ **Resolve detection** — 16 resolved-flagged entries in `/api/alerts` `recent[]`; Slack send count rose 19→48 including resolves; deleting `imagepull-web` aged its alerts out of the active set.
- ✅ **Persistence** — `alertkube-state` ConfigMap holds `snapshot.json` with active alerts + mute history.
- ✅ **Alertmanager receiver** — `401` without token, `202` with `Authorization: Bearer demo-receiver-token`, alert dispatched.
- ✅ **HTTP API/health** — `/healthz` 200, `/readyz` 200, `/api/alerts` returns `{active, recent}`.
- ✅ **Control / no false positives** — healthy-frontend never produced a pod-level alert.
- ✅ **Graceful image use** — local image resolved by cri-dockerd with `pullPolicy: Never`.

---

## 3. Findings

### F1 — HIGH (fixed): Slack webhook delivery fails out of the box with modern app webhooks

**Symptom.** First send failed: `sink "slack" send failed: slack server error: 404 Not Found`.

**Root cause.** alertkube's webhook message always set `WebhookMessage.Channel` from the per-severity config (`alerts-critical` / `alerts-warning` / `alerts-info`). A **modern Slack app incoming webhook is bound to a single channel and rejects any other channel name with `404 channel_not_found`** — so every alert was dropped at the sink. Bisected directly against the supplied webhook:

```
{"text":"x"}                          -> HTTP 200 ok
{"channel":"alerts-warning","text":x} -> HTTP 404 channel_not_found
{"blocks":[…],"username":…}           -> HTTP 200 ok   (no channel field)
```

The default `channels` in `values.yaml` are non-empty, and `internal/config/config.go` **forces them back to defaults even if you blank them** (`envOr("SLACK_CHANNEL_CRITICAL","alerts-critical")`), so there was no config-only way to avoid sending a channel. Net effect: **a fresh `helm install` with only `slack.webhookUrl` set delivers zero alerts to a modern Slack app webhook.** The README's note ("webhooks ignore the channel field") is too optimistic — modern webhooks don't ignore it, they reject the whole request.

**Fix applied** (`internal/sinks/slack.go`, uncommitted, on the working tree for review):
- Webhook mode now sends `channel` **only** when a workload explicitly sets the `alert-slack-channel` annotation (the legacy-webhook use case). The per-severity default channel is no longer put on webhook messages.
- Bot-token mode is unchanged — `chat.postMessage` still routes per-severity channels, which is the only mode where that actually works.

After the fix: 48 Slack sends, **0 errors**.

**Follow-ups recommended:**
- Update the README "Slack channel routing" note to say modern webhooks *reject* (404) non-bound channels, not merely ignore them.
- Add a unit test asserting the webhook payload omits `channel` unless the annotation is present.

### F2 — MEDIUM: time-threshold alerts (PVCPending) depend on informer resync and did not fire as expected

**Observed.** The Pending PVC (created 18:04:17Z, `pvcPendingSeconds: 30`) produced **no** `PVCPending` alert for ~8 minutes, across at least one 5-minute resync boundary. It fired **instantly** the moment a real `Update` event was forced (annotating the PVC). 

**Why.** A PVC bound to a non-existent storageClass has no provisioner and receives no further watch events after creation. `evaluatePVC` only runs on Add/Update; at Add the age is 0 (< threshold), so nothing fires until something re-delivers the object. The code relies on the 5-minute informer resync (`informerResyncPeriod`, `controller.go:31`) to re-evaluate standing conditions — but in this run the resync did not re-evaluate the idle PVC within the expected window.

**Impact.** Effective `PVCPending` latency is `pending-duration + (up to one resync period)`, and for objects with no further activity it depends entirely on resync re-delivery firing reliably. Setting `pvcPendingSeconds: 30` gives a false sense of a 30s SLA. With production defaults (`pvcPendingSeconds: 300`, resync 300s) the first resync lands right at the threshold boundary (`age < threshold` → `300 < 300` is false), which is fragile.

**Recommendation.** Don't depend on informer resync for time-based thresholds. Add an explicit periodic re-scan in the sweeper (it already ticks every 30s) that re-lists Pending PVCs (and other age-gated conditions) and re-evaluates them. At minimum, document the resync dependency and the real latency formula.

### F3 — LOW (observability): stdout sink does not distinguish resolved alerts

`StdoutSink.Send` prints `a.String()`, and `Alert.String()` (`internal/alert/alert.go:204`) has no `Resolved` field. A resolved alert logs identically to a firing one:

```
ALERT [warning] Pod …/imagepull-web-… reason=ImagePullBackOff fp=…   # could be fire OR resolve
```

Operators reading logs (and anyone scraping stdout for a JSON sink) can't tell a page from an all-clear. Add a `RESOLVED`/`FIRING` prefix or a `resolved=true` field. (Slack/PagerDuty Block Kit *do* render resolves distinctly — this is stdout-only.)

### F4 — LOW (config ergonomics): per-severity channels cannot be disabled

Because `applyEnvDefaults` re-injects `alerts-critical/warning/info` whenever the channel is empty, there is no way to run "webhook mode, no channel field" purely via config — which is exactly what most modern-webhook users need. After F1's fix this is less urgent, but consider honoring an explicit empty string (e.g. a sentinel) so channels are opt-in.

### F5 — INFO (security/operational), verified-good but worth flagging
- **Receiver auth** works and the controller logs a loud warning if `receiver.enabled` without a token — good. Keep the token set (we used `demo-receiver-token`).
- **Webhook secret** is mounted from a Secret and read per-send (rotation without restart) — good design. The plaintext-on-CLI exposure is an operator concern, not a code bug → rotate.
- **Pod hardening** is strong: distroless nonroot, `readOnlyRootFilesystem`, `drop: ALL`, seccomp `RuntimeDefault`, `allowPrivilegeEscalation: false`. ✅
- **RBAC** defaults to a cluster-wide read ClusterRole + a namespaced Role for the state ConfigMap. Reasonable; `rbac.scope: namespace` is available for tighter installs.

---

## 4. What's missing / improvements / best approaches

**Testing & release**
- Add a CI smoke job that does exactly this run on `kind`: install chart → apply failing workloads → assert `alertkube_alerts_total` and a clean `alertkube_sink_errors_total`. The current `test/e2e/chainsaw/crashloop` covers crashloop only; broaden to OOM/Job/PVC/ImagePull and a **negative** (healthy workload → no alert) case.
- Add a sink contract test that posts a real Block Kit payload to a fake Slack server returning `channel_not_found` to lock in F1's regression.

**Reliability**
- F2: sweeper-driven re-evaluation for age-gated conditions (PVCPending, and any future "stuck for N" alert).
- Consider a self-watch guard so alertkube's own rollout doesn't emit a `DeploymentUnavailable` for itself (seen at startup; harmless but noisy). An ignore rule for the release namespace/name, or `ignoredNamespaces` in the chart's own values, would clean this up.

**Observability**
- F3: mark resolved in stdout.
- Ship the `ServiceMonitor` + `PrometheusRule` (both off by default) in the documented "production" values; the self-health rules ("who watches the watcher") are valuable and already authored.

**Docs**
- Correct the Slack webhook channel note (F1).
- Document the resync/latency relationship for time-threshold alerts (F2).
- A one-command local playground (this audit's scripts) would be a great `make playground` target / tutorial.

**Defaults**
- Default routing is `critical → [slack, pagerduty]`. With no PagerDuty key the `pagerduty` sink silently no-ops (verified safe), but a first-time user gets a confusing routing rule referencing an unconfigured sink. Consider defaulting critical to `[slack]` and documenting how to add pagerduty.

---

## 5. Priority recommendations

| Pri | Item | Action |
| --- | --- | --- |
| P0 | F1 Slack channel 404 | ✅ fix on working tree — review, add test, commit; fix README note |
| P0 | Rotate the shared Slack webhook | Operator action in Slack |
| P1 | F2 PVC/time-threshold reliability | Re-evaluate age-gated conditions from the 30s sweeper, not informer resync |
| P2 | F3 stdout resolved marker | Add `resolved=`/`RESOLVED` to stdout output |
| P2 | Broaden e2e CI to all alert kinds + negative case | New chainsaw/kind tests |
| P3 | F4 allow channel-less config; default routing tidy-up | Config + values tweak |

---

## 6. Reproduce / clean up

```bash
# everything targeted docker-desktop explicitly; nothing touched EKS
kubectl config use-context docker-desktop

# rebuild + redeploy after the slack.go fix
docker build -t alertkube:e2e .
kubectl -n alertkube rollout restart deploy/alertkube

# tear down
kubectl delete ns alertkube-demo
helm -n alertkube uninstall alertkube
kubectl delete ns alertkube
```

**Bottom line:** alertkube's core pipeline — watch → classify → dedupe → route → fan-out → resolve → persist → expose metrics/API/receiver — is solid and behaved correctly under a realistic multi-failure load, with no false page on the healthy control. The one blocker for real Slack delivery (F1) was a default-config bug that's now fixed; F2 is a latency/reliability gap worth closing before relying on time-threshold alerts.
