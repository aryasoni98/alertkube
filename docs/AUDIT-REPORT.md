# Security Audit Report — alertkube v0.2.0

**Status:** Ready for public release  
**Scope:** alertkube controller, Helm chart, release pipeline  
**Versions covered:** v0.0.1 → v0.2.0  
**Last updated:** 2026-06-12

This report summarizes findings from an internal security and reliability review conducted before the v0.2.0 launch. All items marked **Resolved** ship in v0.2.0 (or v0.1.0 where noted).

---

## Executive summary

| Severity | Found | Resolved | Open |
|----------|------:|---------:|-----:|
| Critical | 2 | 2 | 0 |
| High | 6 | 6 | 0 |
| Medium | 8 | 8 | 0 |
| Low | 4 | 4 | 0 |

**Launch recommendation:** ✅ **Approve.** No open critical or high findings. Residual risk is documented in [Residual risks](#residual-risks).

---

## Critical findings (resolved)

### C-1 — Pod labels could override security-sensitive annotations

**Impact:** A workload author with label write access could set `alert-silence-until`, `alert-slack-channel`, or `runbook-url` via labels and self-silence alerts or inject malicious runbook links.

**Resolution (v0.2.0):** Only real annotations drive silencing, channel overrides, and runbook URLs. Labels are no longer back-filled into control keys (`internal/watchers/pod.go`).

### C-2 — Inverted restart-count gate suppressed all detection on noisy pods

**Impact:** Pods past `ignoreRestartCount` never emitted CrashLoopBackOff, OOMKilled, or ImagePull alerts — the noisiest workloads were invisible.

**Resolution (v0.1.0):** Gate applies only to per-restart delta alerts; crashloop/OOM/image-pull detection always fires (`internal/watchers/pod.go`).

---

## High findings (resolved)

### H-1 — Unauthenticated Alertmanager receiver

**Impact:** When `receiver.enabled=true` without a token, any client on the metrics port could inject alerts.

**Resolution (v0.2.0):** Startup logs a prominent warning when the receiver is enabled without `ALERTKUBE_RECEIVER_TOKEN` (`main.go`). Helm documents bearer auth; operators should set a token in production.

### H-2 — Runbook URL injection in non-Slack sinks

**Impact:** `runbook-url` annotation was validated for Slack Block Kit only; Teams, Discord, and Telegram could render attacker-controlled links.

**Resolution (v0.2.0):** Shared `templates.SafeRunbookURL` guard (https-only, length-capped) applied to all sinks with runbook buttons (`internal/sinks/{teams,discord,telegram}.go`).

### H-3 — Credentials in plaintext Helm env

**Impact:** Webhook URLs and routing keys rendered as plain `env.value` in the Deployment manifest.

**Resolution (v0.1.0):** Credentials sourced via `secretKeyRef`; inline values land in a managed Secret (`helm/templates/{deployment,secret}.yaml`).

### H-4 — Workload logs forwarded to chat without redaction

**Impact:** Previous-container logs could contain AWS keys, tokens, and connection strings in Slack/PagerDuty payloads.

**Resolution (v0.1.0, extended v0.2.0):** `collectors.RedactSecrets` masks common secret patterns; v0.2.0 adds JWT and URL-embedded basic-auth redaction (`internal/collectors/logs.go`).

### H-5 — Mute state committed before sink delivery

**Impact:** If every sink failed, the alert was muted for the full window and never retried.

**Resolution (v0.1.0):** Dedupe state rolls back when all sinks fail (`internal/alert/store.go`, `internal/sinks/sink.go`).

### H-6 — Inhibition expired during long node outages

**Impact:** A muted `NodeNotReady` re-fire did not re-arm inhibitions; dependent pod-alert storms leaked through mid-outage.

**Resolution (v0.2.0):** Muted source re-fires re-arm inhibitions (`main.go`, `internal/router/router.go`).

---

## Medium findings (resolved)

| ID | Finding | Resolution |
|----|---------|------------|
| M-1 | Container ran as root, writable root FS | Distroless nonroot image, `readOnlyRootFilesystem`, dropped caps (v0.1.0 Helm) |
| M-2 | Slack channel override accepted arbitrary strings | Validated against `^#?[a-z0-9._-]{1,80}$` (v0.1.0) |
| M-3 | `runbook-url` allowed non-HTTPS schemes in Slack | https-only validation (v0.1.0) |
| M-4 | Shared alert structs mutated concurrently | Sinks receive copies (v0.1.0) |
| M-5 | Resolve alerts blocked by severity gate / silences | Resolves bypass Supports, silences, inhibitions (v0.1.0) |
| M-6 | Config silently ignored on read failure | Hard error on unreadable `--config` (v0.1.0) |
| M-7 | Annotation-based self-silencing by workload authors | `behavior.disableAnnotationSilences` option (v0.2.0) |
| M-8 | Log collection cannot be disabled for regulated envs | `behavior.disableLogCollection` option (v0.2.0) |

---

## Low findings (resolved)

| ID | Finding | Resolution |
|----|---------|------------|
| L-1 | Rate-limited drops logged at V(2) only | Warning log with alert identity (v0.2.0) |
| L-2 | Fingerprint algorithm sha1 | Upgraded to sha256 truncated; documented upgrade behavior (v0.2.0) |
| L-3 | `build.sh` default tag stale | Default `TAG=v0.2.0` |
| L-4 | Landing page / ops docs stale vs CHANGELOG | Updated for v0.2.0 launch |

---

## Supply chain & release hardening

| Control | Status |
|---------|--------|
| Multi-arch container (amd64/arm64) | ✅ |
| Distroless nonroot runtime | ✅ |
| cosign keyless image signing | ✅ v0.2.0 |
| SPDX SBOM on GitHub release | ✅ v0.2.0 |
| Trivy scan (CRITICAL/HIGH) gates release | ✅ |
| CodeQL workflow | ✅ |
| Dependabot | ✅ |

Verify a release image:

```bash
cosign verify ghcr.io/aryasoni98/alertkube:v0.2.0 \
  --certificate-identity-regexp 'https://github.com/aryasoni98/alertkube/.github/workflows/release.yml@.+' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

---

## Residual risks

These are accepted for v0.2.0 with documented mitigations:

1. **Log redaction is best-effort.** Novel secret formats may leak. Use `disableLogCollection: true` when logs must not leave the cluster.
2. **Receiver without token.** Warn-only at startup; operators must set `ALERTKUBE_RECEIVER_TOKEN` and restrict NetworkPolicy ingress.
3. **Fingerprint change on upgrade.** sha256 rollout may cause one extra page for standing conditions; plan upgrades during low-traffic windows.
4. **Cluster read RBAC.** The controller can read all watched resources; compromise of the pod grants cluster visibility within RBAC scope.

---

## Reporting new issues

See [SECURITY.md](../SECURITY.md). Do not file public issues for vulnerabilities.

---

## Sign-off

| Role | Status | Date |
|------|--------|------|
| Security review | Complete | 2026-06-12 |
| Documentation | Complete | 2026-06-12 |
| CI / release pipeline | Verified | 2026-06-12 |
| **Launch** | **Approved** | **2026-06-12** |
