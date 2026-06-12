# Beta Launch Audit — alertkube

**Date:** 2026-06-12
**Scope:** full working tree (committed + uncommitted changes), including new packages `internal/group`, `internal/persist`, `internal/receiver`, new sinks (Discord, Opsgenie, Telegram), new watchers (CronJob, DaemonSet, HPA, StatefulSet), Helm chart, CI/CD workflows, and documentation.
**Method:** four independent deep scans — build/test verification, correctness review, security audit, release-readiness review — with findings verified against actual code (file:line references are exact).

---

## Verdict

**No hard blockers for the standard tag-push release path.** Build, `go vet`, the full test suite (including `-race`), and `gofmt` are all clean. The chart lints and renders correctly, RBAC is least-privilege, the container is distroless/non-root with a hardened security context, and there is no `InsecureSkipVerify` anywhere.

Six items should land **before cutting the beta tag** (estimated total effort: small):

| # | Item | Why before beta |
| - | ---- | --------------- |
| 1 | SEC-1: apply `safeRunbookURL` to Teams/Discord/Telegram sinks | Tenant-controlled link injection to on-call responders |
| 2 | SEC-2: stop honoring pod *labels* as control annotations | Label-writers can silence alerts / redirect channels |
| 3 | REL-2: bump `Chart.yaml` version/appVersion (currently 0.0.1) | `helm install ./helm` from checkout deploys a stale image |
| 4 | REL-3: stamp CHANGELOG `[Unreleased]` → beta version | Release body points readers at it |
| 5 | REL-1: fix or drop the broken `workflow_dispatch` release path | Only if manual dispatch will be used |
| 6 | SEC-3/REL-4: warn loudly when receiver enabled without token | Silent unauthenticated alert-injection endpoint |

Everything else is recommended hardening or post-beta polish.

> **Remediation status (2026-06-12):** all six pre-tag items above are fixed in the working tree — `templates.SafeRunbookURL` now guards Teams/Discord/Telegram (with regression test `TestRunbookURLGuardNewSinks`), labels no longer populate control annotation keys (test `TestMergeAnnotationsExcludesControlKeysFromLabels`), Chart.yaml bumped to 0.2.0, CHANGELOG stamped `[v0.2.0]` with a Security section, the release workflow's `workflow_dispatch` path checks out and tags `${TAG}` correctly, and startup warns when the receiver runs tokenless. Build, vet, `gofmt`, race tests, helm lint, and a full chart render are green after the changes.

---

## 1. Build & test health — PASS

- `go build ./...`, `go vet ./...`, `gofmt -l .` — all clean.
- `go test ./... -count=1` and `go test ./... -race -count=1` — all 11 testable packages pass, no flakes, no data races.
- Go version consistent everywhere: `go.mod` (1.25), CI uses `go-version-file: go.mod`, Dockerfile uses `golang:1.25-alpine`.
- **Coverage gap (Low):** `internal/metrics` is the only `internal/*` package with no `_test.go`. Root package (`main.go`) and `cmd/dumppayload` also untested (normal for entry points).

---

## 2. Security findings

### SEC-1 (High) — `runbook-url` rendered as clickable link without scheme validation in three sinks

- `internal/sinks/teams.go:88-92` — raw value into `Action.OpenUrl`.
- `internal/sinks/discord.go:61-63` — raw value into `embed["url"]`.
- `internal/sinks/telegram.go:47-49` — HTML-escaped but not scheme-validated `<a href>`.

The Slack path already guards this via `safeRunbookURL()` (`internal/templates/blockkit.go:54,88-96` — https-only, 2048-byte cap, rejects whitespace/quotes/angle brackets). The three newer sinks were added without the guard. Since `runbook-url` comes from pod annotations/labels (workload-author controlled — see SEC-2), a tenant who can annotate a pod can render an attacker-controlled link (arbitrary scheme on Discord/Teams) to on-call responders.

**Fix:** export the existing `safeRunbookURL` from a shared package and route all three sinks through it; silently drop the link on validation failure, matching Slack behavior.

> Note: `docs/LAUNCH_AUDIT.md` previously flagged this for Slack only; this finding extends it to the sinks where the guard was never applied.

### SEC-2 (Medium) — Pod labels are merged into the annotation map, so labels can drive control keys

`internal/watchers/pod.go:202-213` (`mergeAnnotations`) back-fills all pod labels into the annotation map. Downstream, control keys are read from that merged map: silences (`internal/router/router.go:84`), Slack channel override (`internal/sinks/slack.go:49`), runbook links (all sinks). Labels are commonly writable by lower-privilege automation than annotations, so a label-writer can silence their own alerts or inject runbook links. `disableAnnotationSilences` mitigates only the silence vector and is off by default.

**Fix:** read control keys (`alert-silence-until`, `alert-slack-channel`, `runbook-url`) only from real annotations; keep labels in a separate map.

### SEC-3 (Medium) — Receiver runs unauthenticated when token is unset, with no warning

`internal/receiver/receiver.go:59` skips the bearer check entirely when `ALERTKUBE_RECEIVER_TOKEN` is empty; `config.Validate` emits no warning, and `networkPolicy.enabled` defaults to `false` in `helm/values.yaml:244`. Result: `POST /api/v1/alerts` open to anyone who can reach the metrics port. The auth code itself is correct (constant-time compare via `crypto/subtle`, 4 MiB `MaxBytesReader` cap).

**Fix:** log a prominent startup warning (or fail validation) when the receiver is enabled with an empty token; document the NetworkPolicy pairing in values.yaml.

### SEC-4 (Medium) — `/api/alerts` debug endpoint unauthenticated on the metrics port

`internal/metrics/metrics.go:101` + `main.go:170-176` serve active/recent alerts as JSON with no auth. `Details` (pod logs/events) are correctly stripped (`store.go:177,191`), so exposure is limited to summaries, labels, annotations, namespaces, names — still useful reconnaissance. Metrics port is exposed via a Service, NetworkPolicy off by default.

**Fix:** gate behind the receiver token (or a separate one), or ship NetworkPolicy locked down by default; at minimum document the requirement.

### SEC-5 (Informational)

- **Telegram token in URL path** (`telegram.go:63`) — unavoidable per Telegram's API; `httpx.sanitizeURL` (`httpx.go:137-143`) correctly strips path/query from all error strings, so it never reaches logs. Add a test asserting this invariant.
- **No SSRF surface:** every sink destination (Slack/Teams/Discord/Webhook/Opsgenie/Telegram) is an operator-set env var; no alert-data-driven outbound fetch exists.
- **Log redaction** (`collectors/logs.go:51-87`) is good pattern coverage (JWTs, AWS keys, GitHub/Slack/OpenAI tokens, bearer/basic auth, key=value) and honestly documented as best-effort, with `disableLogCollection` as the off-switch.

### Verified clean

Secrets correctly separated (configmap has no secrets; all tokens via `secret.yaml`/`secretKeyRef`); no secret ever logged; metrics labels low-cardinality, no user data; receiver hardened (POST-only, constant-time compare, body cap, full server timeouts — Slowloris mitigated); RBAC exactly matches watcher needs, no `secrets` access, no wildcards, state Role `resourceNames`-pinned; deployment runs as 65532, `readOnlyRootFilesystem`, all capabilities dropped, seccomp `RuntimeDefault`, resource limits set; Dockerfile distroless static nonroot, CGO disabled; zero `InsecureSkipVerify`; persisted state is a ConfigMap with `Details` stripped before export (`snapshot.go:38-41`).

---

## 3. Correctness findings

### BUG-1 (High) — Synchronous `Dispatch` blocks the informer event loop for non-pod watchers

All non-pod watchers (`deployment.go`, `job.go`, `daemonset.go`, `statefulset.go`, `hpa.go`, `cronjob.go`, `node.go`, `pvc.go`) call `emit(a)` directly on the informer handler goroutine. `emit` → `Dispatch` (`internal/sinks/sink.go:142`) blocks on `wg.Wait()` with a 15s per-sink timeout plus rate-limiter waits (default 1/sec, burst 5). A slow or rate-limited sink stalls that resource type's entire informer processing loop, delaying all subsequent events. Only the pod watcher offloads via its bounded enrich pool (`pod.go:156`).

**Fix:** dispatch asynchronously off the handler goroutine — bounded worker pool/queue like the pod watcher uses.

### BUG-2 (Medium) — `Dispatch` reports success when every sink is filtered out

`internal/sinks/sink.go:143` — `return attempted == 0 || succeeded.Load() > 0`. If all routed sinks are skipped by the severity gate (e.g. an `info` alert routed only to Opsgenie/PagerDuty), `Dispatch` returns `true`, the emitter (`main.go:426`) never calls `MarkFailed`, and the alert is recorded active and muted for the full window despite never being delivered — then later emits a synthetic resolve to sinks that never saw the trigger.

**Fix:** distinguish "no eligible sink" from "delivered" (treat `attempted == 0` on a firing alert as not-sent), or validate at config load that each route's sinks can serve the severities that reach them.

### BUG-3 (Medium) — `receiver.Handler` dereferences nil callbacks

`internal/receiver/receiver.go:77,81` invoke `onResolved`/`onFiring` with no nil guard and `New` doesn't validate. Production wiring always passes non-nil; latent panic for any future caller or test.

**Fix:** guard or reject nil in `New`.

### BUG-4 (Medium) — HTTP response bodies closed but not drained in two sinks

`internal/sinks/webhook.go:64-68` and `internal/sinks/opsgenie.go:100-104` close without draining (`httpx.PostJSON` drains correctly at `httpx.go:62-64`). Undrained bodies prevent keep-alive reuse → new TCP/TLS handshake per send; meaningful churn during alert storms.

**Fix:** `io.Copy(io.Discard, resp.Body)` before close.

### BUG-5 (Low) — Pod `enrichWG` never awaited on shutdown

`internal/watchers/pod.go:35,158` — incremented per enrichment goroutine, `Wait()` never called, no shutdown hook. In-flight enrichments (and their dispatches) can outlive `runController`. Bounded (4 workers) and self-terminating via ctx, so low impact.

**Fix:** expose `Wait()`/`Stop()` and join it in shutdown alongside sweeper/grouper.

### BUG-6 (Low) — Config doesn't validate `Match`/`By` keys against the known field set

`internal/config/config.go:198-285` — `Validate` never checks keys in `Routing[].Match`, `SeverityOverrides[].Match`, `Escalations[].Match`, or `Grouping.By` against what `alert.FieldValue` (`alert.go:112`) resolves. A typo'd `Grouping.By` key resolves to the empty string for every alert, collapsing everything into one group; a typo'd match key silently never matches. Fails open with no signal.

**Fix:** validate against the well-known field set (severity, kind, namespace, node, reason, name) + label-key allowance; warn on unknown `Grouping.By` keys.

### BUG-7 (Low) — In-flight non-grouped dispatches cancelled instantly on SIGTERM

Dispatches use the controller ctx, so sends racing shutdown are cut off immediately; the grouper flush already uses a detached 20s ctx correctly. Mitigated by persistence restoring pending resolves.

**Fix (optional):** detach dispatch ctx with a bounded deadline like the grouper.

### Checked and cleared (not bugs)

Discord `truncate` bounds-safe; cronjob/node initial-sync handling nil-safe and no spurious alerts on Add; group bucket ownership race-free (removed under mutex before unlocked emit); alert maps single-writer-then-read-only; `persist.Save` single-writer under leader election with sweeper joined before final save; Retry-After header reads valid after deferred close.

---

## 4. Release readiness findings

### REL-1 (High) — `workflow_dispatch` release path is broken

`.github/workflows/release.yml`: manual dispatch checks out the default branch (not the input tag), and `docker/metadata-action` with `type=ref,event=tag` + `type=semver` produces **no tags** on a branch-ref dispatch — so the `:$TAG` image referenced by the SBOM/Trivy/Helm jobs is never pushed, and the chart is packaged from master. **The tag-push path is fine.**

**Fix:** add `with: ref: ${{ github.event.inputs.tag }}` to checkouts and `type=raw,value=${{ env.TAG }}` to metadata tags — or drop `workflow_dispatch`.

### REL-2 (Medium) — `Chart.yaml` stuck at 0.0.1

`version: 0.0.1` / `appVersion: "0.0.1"` while v0.1.0 is already released. Release CI overrides both from the tag, so OCI installs are fine — but `helm install ./helm` from a git checkout (the README's own install command) defaults `image.tag` to appVersion `0.0.1`, deploying an image that predates every beta feature against config it can't fully parse.

**Fix:** bump Chart.yaml in the release commit, or add a CI check that Chart.yaml matches the tag.

### REL-3 (Medium) — CHANGELOG beta features still under `[Unreleased]`

All beta features sit under `[Unreleased]`; the GitHub release body says "See CHANGELOG.md". Stamp the section to the chosen beta version before tagging.

### REL-4 (Medium) — README staleness

- Intro (line 5) still says "watches Pods, Nodes, Deployments, PersistentVolumeClaims, and Jobs" — missing CronJob/DaemonSet/HPA/StatefulSet.
- Architecture diagram (lines 34, 51) lists only the original 5 watchers / 5 sinks.
- Metrics list (line 22) omits `alertkube_escalations_total` and `alertkube_received_alerts_total`.
- "Optional flags" (lines 74-81) omits discord/telegram/opsgenie/receiver/grouping/leaderElection.

One documentation pass fixes all four.

### REL-5 (Medium) — No version wiring

No `-X main.version=` ldflag, no version variable, no `--version` flag, no version in startup logs. Operators can't tell what build runs except via image tag.

**Fix:** `var version = "dev"` + ldflag (release.yml passes the tag as build-arg), log at startup, optionally expose `alertkube_build_info` gauge.

### REL-6 (Low) — Persistence has no observability

`persister.Save` failures are log-only — silently broken persistence (e.g. RBAC drift) isn't alertable. Add `alertkube_state_save_total{result}` and/or `alertkube_state_last_save_timestamp`.

### REL-7 (Low) — Helm CI never exercises the new template branches

`helm.yml` never templates with `receiver.enabled=true`, `grouping.enabled=true`, or `networkPolicy.enabled=true`. Add one render with those toggled on.

### REL-8 (Low) — values.yaml gaps

`persistence.namespace` (a real config field) has no values.yaml override; `metrics.enabled=false` removes the Service the receiver still needs. Expose the former, document the latter.

### Verified in good shape

Helm/config parity complete (configmap renders every `Config` field; new sink env vars match `os.Getenv` calls exactly; new watchers' RBAC present in both scopes); persistence correctly needs no volume (ConfigMap store); probes match real endpoints (`/healthz`, `/readyz` with informer-sync gating); CI covers build/vet/race/Docker smoke/lint/helm-lint/kubeconform; release pipeline (tag path) does multi-arch + GHCR + cosign + SBOM + Trivy + OCI chart push; graceful shutdown drains grouper with detached ctx and runs a final persist save; Grafana dashboard valid JSON and referenced in README/CHANGELOG.

---

## 5. Suggested order of work

**Before tagging beta (small, contained):**

1. SEC-1 — shared `safeRunbookURL` for Teams/Discord/Telegram.
2. SEC-2 — control keys from annotations only.
3. REL-2 + REL-3 — Chart.yaml bump, CHANGELOG stamp.
4. SEC-3 — startup warning for tokenless receiver.
5. REL-1 — fix or remove `workflow_dispatch` path.

**Strongly recommended for beta (slightly larger):**

<!-- markdownlint-disable MD029 -->
6. BUG-1 — async dispatch for non-pod watchers.
7. BUG-2 — don't report success on zero eligible sinks.
8. REL-4 + REL-5 — README pass, version wiring.
<!-- markdownlint-enable MD029 -->

**Post-beta polish:**
BUG-3/4/5/6/7, SEC-4, REL-6/7/8, `internal/metrics` tests.
