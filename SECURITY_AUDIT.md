# alertkube Security Audit

**Audit date:** 2026-06-23
**Auditor:** Senior Security Engineer (DevSecOps / cloud-security focus)
**Repository:** `/Users/aryasoni/Documents/opensource/alertkube`
**Branch audited:** `stage` (modified + new untracked files included)
**Component:** Kubernetes alerting/runbook controller (Go), Helm chart deployment

---

## 1. Executive Summary

alertkube is a well-engineered Kubernetes controller with a notably strong security
posture. The codebase shows clear evidence of deliberate, defense-in-depth security
design: a least-privilege RBAC model (no wildcards, no `secrets` read, no
cluster-admin), a hardened pod/container `securityContext` by default, a static
distroless non-root container image with digest-pinned bases, constant-time bearer
comparison, an SSRF guard on outbound webhook destinations, best-effort secret
redaction of collected logs, a fail-closed alert receiver, and runbook-URL validation
to prevent `javascript:`/`data:` injection into chat sinks.

Static analysis is clean: `go vet ./...` passes with no findings, and
`govulncheck ./...` reports **no known vulnerabilities** in the dependency graph
(dependencies are current: Go 1.26, k8s.io v0.36.2, AWS SDK v2 latest, etc.).

The findings below are therefore mostly **hardening opportunities and defense-in-depth
gaps**, not exploitable critical defects. The single most material confirmed code issue
is unescaped interpolation of an attacker-influenceable fingerprint into the Opsgenie
URL path (Medium). The remaining items are operational-default and deployment-hardening
concerns.

### Findings by severity

| Severity | Count |
|----------|-------|
| Critical | 0 |
| High     | 0 |
| Medium   | 3 |
| Low      | 4 |
| Info     | 4 |
| **Total**| **11** |

### Top 3 issues

1. **M-1 — Unescaped external fingerprint in Opsgenie URL path** (request-path
   injection / CWE-88). External Alertmanager fingerprints are accepted verbatim and
   later interpolated raw into `/v2/alerts/{fingerprint}/close`.
2. **M-2 — Unauthenticated `/metrics` and `/api/alerts` by default; NetworkPolicy
   disabled by default** (CWE-306 / information exposure). Active-alert contents and
   internal metrics are exposed unless the operator opts into a token or NetworkPolicy.
3. **M-3 — ServiceAccount token is automounted (default) with no
   `automountServiceAccountToken` pinning** (CWE-250 / token exposure surface). A
   compromised container has an ambient API token available on disk.

### Remediation status (2026-06-23)

The three Medium findings have been remediated in this branch:

| ID  | Status | Fix |
|-----|--------|-----|
| M-1 | **Fixed** | `neturl.PathEscape` on the Opsgenie alias (`internal/sinks/opsgenie.go`) + strict `fingerprintOK` regex gate on ingestion (`internal/receiver/receiver.go`); invalid upstream fingerprints fall back to the locally computed one. |
| M-2 | **Mitigated** | New `helm/templates/NOTES.txt` emits explicit install-time warnings when `/api/alerts` is unauthenticated, NetworkPolicy is disabled, or inline secrets are set; all-clear message when hardened. (Defaults unchanged to avoid breaking installs that need `networkPolicy.apiServer.cidrs`.) |
| M-3 | **Fixed** | Pod spec now sets `automountServiceAccountToken` explicitly (`helm/templates/deployment.yaml`), surfaced as a value (`automountServiceAccountToken: true`). |
| L-3 | **Mitigated** | Covered by the same `NOTES.txt` inline-credential warning. |

Verified: `go build ./...` and `go vet` clean; `helm lint` clean; `NOTES.txt`
renders correctly across default/hardened/inline-secret paths. L-1/L-2/L-4 and the
Info items remain as documented (deeper/medium-term work).

---

## 2. Scope & Methodology

### Reviewed

- **Entry points / control plane:** `main.go`, `controller.go`, `leaderelection.go`,
  `sweeper.go`, `builders.go`.
- **HTTP / network:** `internal/httpx/httpx.go` (SSRF guard, retry, TLS),
  `internal/metrics/metrics.go` (server, timeouts, route auth),
  `internal/receiver/receiver.go` (webhook ingestion), `internal/authz/bearer.go`.
- **Input handling / parsing:** `internal/config/config.go` (YAML),
  `internal/alert/alert.go` (fingerprint, regex matching), `internal/alert/snapshot.go`
  (untrusted snapshot restore), `internal/router/router.go`, `internal/rules/rules.go`,
  `internal/sources/*` (cloud poller framework + AWS provider).
- **Sinks (outbound):** `slack.go`, `telegram.go`, `opsgenie.go`, `pagerduty.go`,
  `teams.go`, `discord.go`, `webhook.go`, `sink.go`, `internal/templates/blockkit.go`.
- **Secret/credential handling:** log redaction in `internal/collectors/logs.go`,
  AWS credential chain in `internal/sources/aws/aws.go`, env-var secret loading.
- **Kubernetes deployment:** `helm/templates/rbac.yaml`, `deployment.yaml`,
  `configmap.yaml`, `secret.yaml`, `networkpolicy.yaml`, `service.yaml`, `values.yaml`.
- **Supply chain:** `go.mod`, `go.sum`, `Dockerfile`, `.dockerignore`, `.gitignore`.

### Tools run (read-only)

- `go vet ./...` — **clean (exit 0)**.
- `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` — **"No vulnerabilities
  found."**
- `grep`/`Glob` pattern sweeps for: `InsecureSkipVerify`, `tls.Config`/`MinVersion`,
  `os/exec`/`exec.Command`, hardcoded secret literals, `url.PathEscape`/`QueryEscape`,
  `automountServiceAccountToken`. (No `InsecureSkipVerify`, no `exec.*`, no hardcoded
  secret literals found.)
- `gosec` was **not available** in the environment and was not installed; running it in
  CI is recommended (see roadmap).

Every finding below is grounded in code that was read. Items that could not be fully
confirmed are explicitly marked "needs verification".

---

## 3. Findings (ordered by severity)

### M-1 — Unescaped, attacker-influenceable fingerprint interpolated into Opsgenie URL path

- **Severity:** Medium
- **CWE:** CWE-88 (Argument/Request-Path Injection), CWE-20 (Improper Input Validation)
- **CVSS-style reasoning:** Network-adjacent, requires the operator to have enabled the
  receiver and routed external alerts to Opsgenie. Impact is limited to malforming a
  single outbound Opsgenie API request (potential request-path manipulation against the
  Opsgenie API, e.g. altering query parameters via an injected `?`/`#`, or hitting an
  unintended Opsgenie endpoint), not RCE or data exfiltration. Roughly CVSS 4–5.
- **Affected files:**
  - `internal/receiver/receiver.go:107-108` (accepts untrusted fingerprint verbatim)
  - `internal/sinks/opsgenie.go:53` (interpolates it into the URL path)

**Evidence**

The Alertmanager receiver copies the upstream-supplied fingerprint with no validation:

```go
// internal/receiver/receiver.go
if am.Fingerprint != "" {
    a.Fingerprint = am.Fingerprint   // attacker-controlled when the receiver is open/forwarded
}
```

That same `Fingerprint` is later interpolated raw (no `url.PathEscape`) into the
Opsgenie close URL:

```go
// internal/sinks/opsgenie.go:53
url = fmt.Sprintf("%s/v2/alerts/%s/close?identifierType=alias", base, a.Fingerprint)
```

Internally-generated fingerprints are always 12 hex chars (`alert.ComputeFingerprint`
→ `hex.EncodeToString(...)[:12]`), so the *built-in* path is safe. The risk is solely
the externally-ingested value, which can contain `/`, `?`, `#`, `%`, or spaces. The
PagerDuty (`DedupKey`), Slack, Teams, Discord, and Telegram sinks place the fingerprint
in a request *body* (JSON-encoded, safe) or HTML-escape it, so Opsgenie is the only sink
that puts it in a URL path.

**Impact**

A sender able to reach an open/forwarded `/api/v1/alerts` can craft a fingerprint that
manipulates the Opsgenie request target (e.g. appending `?`/`#` to change the effective
endpoint or query). It cannot reach arbitrary hosts (base URL is operator-controlled),
so this is request-path manipulation against Opsgenie, not full SSRF.

**Remediation**

Escape the fingerprint when it is used in a URL path, and/or validate fingerprint shape
on ingestion. Minimal fix in the sink:

```go
import "net/url"
// ...
url = fmt.Sprintf("%s/v2/alerts/%s/close?identifierType=alias",
    base, url.PathEscape(a.Fingerprint))
```

Defense-in-depth at the receiver boundary — reject or normalize fingerprints that are
not safe identifiers:

```go
var fpOK = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
if am.Fingerprint != "" && fpOK.MatchString(am.Fingerprint) {
    a.Fingerprint = am.Fingerprint
} // else fall back to the locally computed fingerprint
```

---

### M-2 — `/metrics` and `/api/alerts` are unauthenticated by default; NetworkPolicy disabled by default

- **Severity:** Medium
- **CWE:** CWE-306 (Missing Authentication for Critical Function), CWE-200 (Exposure of
  Sensitive Information)
- **CVSS-style reasoning:** Requires network reach to the pod's metrics port. `/api/alerts`
  discloses active and recent alert contents (names, namespaces, summaries, and possibly
  enrichment for active alerts). Information disclosure, no integrity/availability impact.
  ~CVSS 4–5 depending on cluster network model.
- **Affected files:**
  - `controller.go:140-154` (`/api/alerts` open when `ALERTKUBE_API_TOKEN` is empty)
  - `internal/metrics/metrics.go:138-157` (`/metrics` always unauthenticated)
  - `helm/values.yaml` (`api.token: ""` and `networkPolicy.enabled: false` defaults)

**Evidence**

`/api/alerts` runs token-optional; with no token it serves alert contents to anyone who
can reach the port (the code does warn loudly):

```go
// controller.go
apiToken := os.Getenv("ALERTKUBE_API_TOKEN")
if apiToken == "" {
    klog.Warningf("/api/alerts ... is UNAUTHENTICATED and exposes active alert contents; ...")
}
// ...
if apiToken != "" && !authz.BearerEqual(req.Header.Get("Authorization"), apiToken) {
    w.WriteHeader(http.StatusUnauthorized)
    return
}
```

`/metrics` is mounted with no auth wrapper at all (`promhttp.Handler()`), which is the
Prometheus norm but still exposes operational metadata. The chart's NetworkPolicy — which
would otherwise restrict who can scrape the port — defaults to `enabled: false`, and the
metrics `Service` has no ingress restriction.

**Impact**

In a flat cluster network, any pod/namespace can read alert contents and metrics
(namespaces, workload names, alert reasons). Defense relies entirely on the operator
opting into either `api.token` or a NetworkPolicy.

**Note (positive):** The design is partially fail-safe — it warns, and the *receiver*
(the write path) fails closed (see Positive Observations). This finding is about the
read path and the safe-by-default deployment posture.

**Remediation**

- Ship `networkPolicy.enabled: true` as the recommended/default value, or document it as
  a required hardening step in the chart README and surface it in `helm install` notes.
- Consider gating `/api/alerts` behind a token by default (generate one if unset and log
  it), or require an explicit `api.allowAnonymous: true` mirroring the receiver's
  fail-closed pattern.
- For `/metrics`, document that it should be restricted to the monitoring namespace via
  `networkPolicy.ingressFrom`.

---

### M-3 — ServiceAccount token automounted by default; `automountServiceAccountToken` not pinned

- **Severity:** Medium
- **CWE:** CWE-250 (Execution with Unnecessary Privileges) / token-exposure surface
- **CVSS-style reasoning:** Local-to-pod; raises the blast radius of any container
  compromise (the API token is on disk and usable with the controller's RBAC). ~CVSS 4.
- **Affected files:**
  - `helm/templates/rbac.yaml:3-10` (ServiceAccount — no `automountServiceAccountToken`)
  - `helm/templates/deployment.yaml:35-46` (pod spec — no `automountServiceAccountToken`)

**Evidence**

`grep -rn automountServiceAccountToken helm/` returns nothing. The controller does need
the projected token to talk to the apiserver, so it cannot be disabled outright — but
the default mount path token can be replaced with a tightly-scoped projected token, and
the field should be explicit rather than relying on the cluster default of `true`.

**Impact**

A compromised controller container has an ambient, auto-refreshed API token at
`/var/run/secrets/kubernetes.io/serviceaccount/token` with the controller's full RBAC
(read pods/nodes/workloads cluster-wide in `cluster` scope, plus `pods/log`). This is the
intended access — the concern is that nothing constrains or makes the mount explicit.

**Remediation**

Make the mount explicit and document the trade-off. Because the controller needs API
access, prefer a bounded projected token rather than disabling:

```yaml
# deployment.yaml pod spec
automountServiceAccountToken: true   # explicit; the controller requires API access
```

Optionally use a projected `serviceAccountToken` volume with an `expirationSeconds`
bound and `audiences` set, instead of the legacy auto-mounted token, to reduce token
lifetime. (Lower priority than M-1/M-2 since the token grants only the already-minimal
RBAC.)

---

### L-1 — `guardDest` SSRF allowlist does not protect third-party SDK sinks (Slack/PagerDuty) and is bypassed by DNS rebinding

- **Severity:** Low
- **CWE:** CWE-918 (SSRF) — residual/defense-in-depth
- **Affected files:** `internal/httpx/httpx.go:181-219`, `internal/sinks/slack.go`,
  `internal/sinks/pagerduty.go`, `internal/sinks/opsgenie.go`

**Evidence**

`guardDest` is a solid SSRF check (blocks link-local/IMDS unconditionally; loopback +
RFC-1918 when `ALERTKUBE_STRICT_WEBHOOK_EGRESS=true`). However:

1. It only runs inside `PostJSON*` (webhook, telegram, opsgenie, discord, teams). The
   Slack and PagerDuty sinks use their vendor SDKs (`slack.PostWebhookCustomHTTPContext`,
   `pd.ManageEventWithContext`) which **do not** pass through `guardDest`. For Slack that
   matters because `SLACK_WEBHOOK_URL` is operator-supplied and could point inward.
2. It performs a check-then-connect resolve (`net.DefaultResolver.LookupIPAddr` in
   `guardDest`, then a separate dial), so a TOCTOU/DNS-rebinding host could pass the
   guard and resolve to a blocked IP at dial time.
3. A DNS resolution *failure* returns `nil` (allows the dial). This is intentional
   (the real dial surfaces the error) but means a hostname that fails to resolve during
   the guard skips IP filtering.

These are mitigated by the design note that destinations come from operator-controlled
env/Secrets (trust boundary), so this is genuinely defense-in-depth, not a primary
control. The NetworkPolicy egress allowlist is the real boundary.

**Remediation**

- Route Slack/PagerDuty SDK calls through an `*http.Client` whose `DialContext` enforces
  the same IP allowlist at connect time (eliminates both the SDK gap and the TOCTOU).
  Example: a custom `net.Dialer.Control` that rejects blocked IPs on every connection.
- Document that NetworkPolicy `sinkCIDRs`/egress is the authoritative SSRF control and
  recommend enabling it.

---

### L-2 — Best-effort log redaction is pattern-based and can miss secrets forwarded to chat sinks

- **Severity:** Low
- **CWE:** CWE-532 (Insertion of Sensitive Information into Log/Output)
- **Affected files:** `internal/collectors/logs.go:51-87`, `internal/templates/blockkit.go`

**Evidence**

`PreviousContainerLogs` fetches up to 50 lines of prior-container logs and runs them
through `RedactSecrets`, which masks JWTs, AWS keys (`AKIA…`), GitHub tokens, Slack
tokens, `sk-…`, bearer tokens, `key=value` secrets, and URL-embedded credentials. This
is a thoughtful list, but regex redaction is inherently incomplete (custom token formats,
multi-line secrets, base64 blobs, secrets split across lines all evade it). Redacted log
tails are then forwarded to Slack/Teams/Discord/etc. as alert detail blocks.

The code itself flags this honestly (`values.yaml`/`config.go`: "redaction is
pattern-based and best-effort — strict environments should disable collection instead of
trusting it") and provides `behavior.disableLogCollection`, plus the RBAC chart drops
`pods/log` entirely when collection is disabled.

**Remediation**

- Keep the existing escape hatch; consider defaulting `disableLogCollection: true` for
  security-sensitive distributions, or documenting it prominently as the recommended
  setting where logs may contain secrets.
- Add the documented note to the chart `NOTES.txt` so operators see it at install time.
- Optionally add an allowlist/entropy-based detector as a second pass.

---

### L-3 — Inline secret values rendered into the Helm release Secret (`helm get manifest` exposure)

- **Severity:** Low
- **CWE:** CWE-312 (Cleartext Storage) / CWE-522 (Insufficiently Protected Credentials)
- **Affected files:** `helm/templates/secret.yaml:8-59`, `helm/values.yaml` (`*.token`,
  `*.webhookUrl`, `*.apiKey`, `genericWebhook.signingSecret`)

**Evidence**

When operators set inline credential values (`slack.webhookUrl`, `api.token`,
`pagerduty.routingKey`, `genericWebhook.signingSecret`, …), the chart base64-encodes them
into a templated `Secret`. base64 is encoding, not encryption, and the values become
visible in `helm get manifest`, the release Secret, and any CI logs that print rendered
manifests.

The template **already documents this clearly** and steers operators to the
`*SecretKeyRef` fields (reference a pre-existing Secret, never templated) for production —
this is good. The residual risk is purely that the inline path exists and may be used.

**Remediation**

- No code change strictly required (well-documented). Optionally add a Helm `NOTES.txt`
  warning when any inline secret value is set, e.g. "WARNING: inline credentials are in
  the release Secret; use *SecretKeyRef in production."
- Recommend External Secrets Operator / sealed-secrets / CSI Secrets Store in the README.

---

### L-4 — Unbounded `lastSent`/regex caches are bounded only by config and time, not by external input — verify under receiver storm

- **Severity:** Low (needs verification under load)
- **CWE:** CWE-770 (Allocation of Resources Without Limits) — residual
- **Affected files:** `internal/alert/store.go` (`lastSent`, `active`, `recent`),
  `internal/alert/alert.go:283-324` (`regexCache`)

**Evidence**

The store is generally well-bounded: `recent` is capped (`recentCap = 200`), the receiver
caps body size (`maxBodyBytes = 4MiB`) and alert count per request
(`maxAlertsPerPayload = 2000`), and `lastSent` entries are evicted after `2*muteWindow`
by `CleanOldHistory`. The `regexCache` is explicitly documented as unbounded but keyed
only by config-supplied patterns (bounded key set), with a code comment warning to add a
cap if alert-supplied values ever feed it.

The residual concern: with the receiver enabled, external alerts each carry an attacker-
influenced `Fingerprint`, and every distinct fingerprint creates a `lastSent` entry
(and an `active` entry for firing alerts) that persists for up to `2*muteWindow` /
`resolveTTL` (default 600s/1200s). A sustained, high-cardinality stream of unique
fingerprints (up to 2000 per request, many requests/sec) could grow `active`/`lastSent`
to a large transient size before eviction — a memory-pressure DoS within the pod's
256Mi limit. `regexCache` is **not** fed by alert input, so it is safe.

**Remediation / verification**

- Verify behavior with a load test: sustained unique-fingerprint receiver traffic vs the
  `256Mi` memory limit. The `resources.limits.memory` cap plus the controller's restart
  semantics bound the worst case to a pod OOM-restart (availability blip), not a node
  problem — so impact is low.
- Consider a hard cap on `active`/`lastSent` cardinality (LRU eviction) when the receiver
  is enabled, and per-source rate limiting on `/api/v1/alerts`.

---

### I-1 — (Info) TLS configuration relies on Go defaults (no explicit `MinVersion`)

- **Severity:** Info
- **Affected files:** all outbound HTTP clients (`internal/httpx/httpx.go:29`,
  `internal/sinks/slack.go:38`)

No `tls.Config` is set anywhere, and no `InsecureSkipVerify` exists — outbound calls use
Go's default `http.Transport`, which **verifies certificates** and negotiates TLS ≥ 1.2
on modern Go (1.26). This is secure by default. For compliance regimes that mandate an
explicit floor, consider setting `tls.Config{MinVersion: tls.VersionTLS12}` (or 1.3) on
the shared client to make the policy auditable. No action required for correctness.

### I-2 — (Info) Panic isolation and context-bounded timeouts are well implemented

- **Severity:** Info (positive observation, recorded for completeness)

Sink dispatch (`internal/sinks/sink.go:103-108`) and the cloud source runner
(`internal/sources/runner.go:42-46`) both recover panics so one faulty
sink/provider cannot crash the controller and silence Kubernetes watchers. The delivery
path has a clearly documented nested timeout budget (dispatch 20s → per-sink 15s →
per-request 10s), and all sleeps run under `ctx`. This is mature DoS/resilience hygiene.

### I-3 — (Info) Untrusted snapshot restore is validated

- **Severity:** Info

`internal/alert/snapshot.go:49-87` rejects snapshots from a future schema version, drops
`lastSent` entries with future timestamps (anti-permanent-mute poisoning), and rejects
active entries whose `Kind`/`Severity` enums are invalid — preventing a poisoned state
ConfigMap from injecting arbitrary alerts that the sweeper would later emit as synthetic
resolves. JSON (not gob/yaml-with-tags) is used, so there is no unsafe-deserialization
gadget surface. Good.

### I-4 — (Info) `gosec` not run; add to CI

- **Severity:** Info

`gosec` was unavailable in the audit environment. `go vet` and `govulncheck` are clean,
but adding `gosec` (and `govulncheck`) to CI / the existing `.golangci.yml` and
`.pre-commit-config.yaml` would make these checks continuous. (A `trivy-results.sarif`
entry in `.gitignore` suggests Trivy is already wired into CI — confirm it scans both the
image and IaC.)

---

## 4. Risk Matrix / Summary Table

| ID  | Title                                                        | Severity | Confirmed?        | CWE        | Primary location |
|-----|--------------------------------------------------------------|----------|-------------------|------------|------------------|
| M-1 | Unescaped external fingerprint in Opsgenie URL path          | Medium   | Confirmed         | CWE-88/20  | `opsgenie.go:53`, `receiver.go:107` |
| M-2 | `/metrics` + `/api/alerts` unauth by default; NetPol off     | Medium   | Confirmed         | CWE-306/200| `controller.go:140`, `values.yaml` |
| M-3 | SA token automounted; not pinned                             | Medium   | Confirmed         | CWE-250    | `helm/templates/*` |
| L-1 | SSRF guard skips SDK sinks; TOCTOU/DNS-rebind                | Low      | Confirmed (DiD)   | CWE-918    | `httpx.go:181`, `slack.go` |
| L-2 | Best-effort log redaction can miss secrets                   | Low      | Confirmed         | CWE-532    | `collectors/logs.go` |
| L-3 | Inline secrets in Helm release Secret                        | Low      | Confirmed         | CWE-312    | `secret.yaml` |
| L-4 | Receiver-driven cardinality growth (memory DoS)              | Low      | Needs verification| CWE-770    | `store.go` |
| I-1 | TLS relies on Go defaults (no explicit MinVersion)           | Info     | Confirmed (safe)  | —          | `httpx.go` |
| I-2 | Panic isolation / timeout budget (positive)                  | Info     | Confirmed         | —          | `sink.go`, `runner.go` |
| I-3 | Snapshot restore validation (positive)                       | Info     | Confirmed         | —          | `snapshot.go` |
| I-4 | Add `gosec`/`govulncheck` to CI                              | Info     | Recommendation    | —          | CI |

---

## 5. Prioritized Remediation Roadmap

### Quick wins (low effort, do first)

1. **M-1:** Wrap the Opsgenie fingerprint with `url.PathEscape` (`opsgenie.go:53`) **and**
   validate `am.Fingerprint` shape in `receiver.go` with a strict identifier regex. ~10
   lines, no behavior change for legitimate traffic.
2. **M-2 (docs):** Flip the recommended chart posture — document `networkPolicy.enabled:
   true` and `api.token` as required hardening, and emit a `NOTES.txt` warning when the
   metrics port is unauthenticated and unprotected.
3. **M-3:** Set `automountServiceAccountToken: true` explicitly in the pod spec (and
   document the trade-off) so the mount is auditable rather than relying on cluster
   default.
4. **L-3 / I-4:** Add a `NOTES.txt` warning when inline secrets are set; add `gosec` and
   `govulncheck` to CI / pre-commit.

### Medium-term

5. **L-1:** Move Slack/PagerDuty SDK calls onto a shared `*http.Client` with a
   `Dialer.Control` IP allowlist so the SSRF guard applies at connect time (closing both
   the SDK gap and the TOCTOU/DNS-rebind window).
6. **L-4:** Load-test the receiver under high-cardinality fingerprint storms; add an LRU
   cap on `active`/`lastSent` and per-source rate limiting on `/api/v1/alerts` if growth
   is unbounded within the pod memory limit.
7. **M-3 (deeper):** Migrate to a bounded projected `serviceAccountToken` volume with
   `expirationSeconds` + `audiences`.

### Longer-term / posture

8. **L-2:** Consider defaulting `disableLogCollection: true` for hardened distributions;
   add an entropy/allowlist redaction pass as a second layer.
9. **I-1:** Set an explicit `tls.Config.MinVersion` on the shared HTTP client for
   auditable compliance evidence.
10. Add Pod Security Admission enforcement docs (the workload already satisfies the
    `restricted` profile — see below — so labeling the namespace `restricted` is free).

---

## 6. Positive Observations (what's already done well)

The codebase demonstrates strong, deliberate security engineering. Highlights:

- **Least-privilege RBAC** (`helm/templates/rbac.yaml`): no wildcards, no `secrets` read,
  no cluster-admin, no `create/delete` on workloads. `pods/log` is conditionally granted
  and disappears when log collection is off. Leader-election and state-ConfigMap Roles
  are namespaced and verb-minimized (e.g. no `delete` on leases to prevent forced
  re-elections; `get/update/patch` pinned to the named state ConfigMap via
  `resourceNames`). A `namespace` RBAC scope is offered as a tighter alternative to
  cluster scope.
- **Hardened workload defaults** (`helm/values.yaml`): `runAsNonRoot`, `runAsUser
  65532`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`,
  `capabilities.drop: ["ALL"]`, `seccompProfile: RuntimeDefault`. This satisfies the
  Kubernetes **Pod Security `restricted`** profile out of the box.
- **Hardened image** (`Dockerfile`): `CGO_ENABLED=0` static binary on
  `gcr.io/distroless/static:nonroot`, both builder and base **digest-pinned**,
  `-trimpath`, explicit `USER 65532:65532`, no shell. Minimal CVE surface.
- **Fail-closed receiver** (`controller.go:224-249`): an enabled receiver with no token
  is a **fatal** startup error unless `allowAnonymous` is explicitly set — preventing an
  accidental open alert-injection endpoint.
- **Constant-time bearer comparison** (`internal/authz/bearer.go`) via `crypto/subtle`.
- **SSRF guard** (`internal/httpx/httpx.go`): unconditionally blocks link-local / IMDS
  (`169.254.169.254`), optional strict mode for loopback/RFC-1918, scheme allowlist
  (`http`/`https` only), context-bounded DNS so a slow resolver cannot hang a send.
- **URL secret sanitization** (`sanitizeURL`): webhook tokens stripped from error logs.
- **Runbook URL validation** (`templates.SafeRunbookURL`): only well-formed `https://`
  URLs, length-capped, rejects whitespace/quote/angle-bracket chars — blocks
  `javascript:`/`data:`/`file:` injection into Slack buttons, Teams `Action.OpenUrl`,
  Discord embed URLs, and Telegram anchors.
- **HTML escaping** in the Telegram sink (`html.EscapeString` on every interpolated
  field) and structured JSON bodies everywhere else (no string-concatenated payloads).
- **Privilege-boundary awareness** (`internal/watchers/pod.go:275-301`): control
  annotations (`alert-silence-until`, `alert-slack-channel`, `runbook-url`) are never
  back-filled from labels, because labels are typically writable by lower-privilege
  automation — preventing self-silencing / link injection. `disableAnnotationSilences`
  exists for environments where workload authors must not control alerting at all.
- **Receiver input hardening**: `http.MaxBytesReader` (4MiB), per-payload alert cap
  (2000), and `http.TimeoutHandler` on the synchronous receiver route. Server has
  `ReadHeaderTimeout`/`ReadTimeout`/`WriteTimeout`/`IdleTimeout` set (slowloris defense).
  The receiver strips behavior-control annotations from forwarded alerts so one upstream
  sender cannot silence/reroute everyone's alerts.
- **Secret redaction of collected logs** before they leave the cluster
  (`internal/collectors/logs.go`), with an honest "best-effort" disclaimer and a hard
  off-switch.
- **Untrusted-state validation**: snapshot restore rejects future versions, future
  timestamps, and invalid enums (`internal/alert/snapshot.go`).
- **Cloud credentials** use the standard provider chains (IRSA / AKS & GKE Workload
  Identity recommended) — no static keys in code; narrow per-service API interfaces.
- **Dependency hygiene**: Go 1.26, current k8s/AWS/Azure/GCP SDKs, `go.sum` present,
  Dependabot referenced for digest bumps; `govulncheck` clean; `go vet` clean.
- **Resilience**: panic isolation per sink and per cloud source; documented nested
  timeout budgets; graceful drain on shutdown that flushes in-flight alerts and saves
  state; demoted-leader handlers return 503 instead of silently dropping POSTs.

---

*End of report.*
