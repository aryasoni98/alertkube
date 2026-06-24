# PRD: AlertKube Web UI / Control Plane

**Status**: Draft (for maintainer decision)
**Author**: Alex (Product Management)
**Last Updated**: 2026-06-24
**Version**: 1.0
**Stakeholders**: Maintainer (aryasoni98), future co-maintainer, adopters
**Related**: `PRODUCT_LAUNCH_REVIEW.md`, `docs/design/crd-sketch.md`, `docs/decisions/0001-client-go-over-controller-runtime.md`

---

## TL;DR / Recommendation

The maintainer proposed a **web UI to manage everything at runtime** (enable/disable alerts, author correlation rules and pattern groups, add/update notification channels) instead of editing static YAML.

**My recommendation: do NOT build a runtime-mutation control plane as the first move. Build a read-only observability UI first (Phase 0), then add mutation as a "UI-as-PR-generator" that keeps Git as the source of truth (Phase 1), and ship channel-add/test as the one truly-runtime capability (Phase 2).**

In one paragraph: AlertKube's entire credible differentiation today is *"deterministic, GitOps-friendly, security-hardened, config-is-code-reviewed-in-Git, low-footprint."* A runtime-mutation UI backed by a new dynamic store directly attacks that moat, multiplies the attack surface on a security-conscious project, and forces the maintainer (bus-factor 1, per the launch review) to solve five hard distributed-systems problems at once — leader-election state propagation, secret handling, RBAC, audit, and GitOps drift — before a single user benefits. The smart sequence is to deliver the *visibility* users actually ask for first (which is low-risk and high-value), then make the UI **author changes as reviewable Git PRs / ConfigMap diffs** so the "config is code" property is preserved rather than destroyed. The one piece of genuinely-runtime mutation worth doing — adding and **test-firing a notification channel** — is also the highest-value, lowest-blast-radius wedge, and it can be done with K8s-Secret references so the app never becomes a secret store.

**The framing I am rejecting** (full runtime-mutation UI with a dynamic config store, framing **B**) is the one the request reads as. I'm rejecting it on impact-per-risk, not on feasibility. I'm ~80% confident in this call; the thing that would change it is hard evidence that target users want to *abandon* GitOps, not augment it (see Open Questions).

---

## 1. Problem Statement

### 1.1 What's actually being asked

> "I want a web UI to manage everything at runtime instead of editing static YAML: enable/disable alerts, author alert patterns (correlation rules), group patterns over a timeframe, and add/update notification channels (Slack, PagerDuty, Teams, Opsgenie, Discord, Telegram, generic webhook)."

This is a **solution** ("a runtime UI"), not a problem. Per critical rule #1, I dug for the underlying jobs-to-be-done. There are at least four distinct pains hiding inside this one request, and they have *very different* best solutions:

| # | Underlying job | Who feels it | Today's friction | Is "runtime mutation" actually required? |
|---|----------------|--------------|------------------|------------------------------------------|
| J1 | **See what AlertKube is doing** — which alerts are active, what rules exist, where they route, why something was suppressed | On-call, SRE, app-dev | `/api/alerts` is a raw JSON dump; rules/routing/config are invisible unless you read the ConfigMap | **No.** Pure read. |
| J2 | **Silence / disable a noisy alert *right now*** during an incident | On-call at 3am | Edit YAML → commit → CI → redeploy/restart. Far too slow mid-incident. | **Partially.** A *time-boxed* silence wants to be fast; a *permanent* disable wants to be in Git. |
| J3 | **Author / tune correlation rules and grouping** | Platform eng, SRE | Hand-writing nested YAML (`rules`, `grouping`) is error-prone; no validation feedback until restart | **No.** This is deliberate, reviewed config. It *belongs* in Git; the pain is authoring ergonomics, not runtime-ness. |
| J4 | **Add / update / test a notification channel** | Platform eng onboarding a team | Set env var/Secret → edit `channels`/`routing` → redeploy → hope the token works (no test path) | **Yes, mostly.** "Did my Slack token work?" is a runtime question with a runtime answer. |

The maintainer collapsed four jobs into "a runtime UI." **They are not the same project, and conflating them is the trap.** J1 and J3 do *not* need runtime mutation. J2 needs *fast time-boxed* mutation. Only J4 genuinely wants live runtime behavior.

### 1.2 Where static YAML hurts (real)

- **Incident-time latency (J2):** editing YAML → PR → CI → rolling restart is minutes-to-hours. During an alert storm at 3am that's unacceptable. Alertmanager solved exactly this with a runtime silence API + Karma/`amtool`; AlertKube has *no* silence mutation path at all today (silences are static config with an `until` timestamp).
- **Authoring ergonomics (J3):** the `rules` and `grouping` schemas are powerful but hand-edited. There is `config.Validate()` but you only see its errors at boot — a slow feedback loop. No dry-run.
- **Discoverability (J1):** a new on-call engineer cannot answer "is this alert even enabled? where does critical route? why was X suppressed?" without `kubectl get configmap -o yaml` and reading Go-flavored YAML. The `alertkube_alerts_suppressed_total{reason}` metric exists but nobody correlates Prometheus counters to a specific alert at 3am.
- **Channel onboarding friction (J4):** adding a team's Slack channel means an env var + Secret + a `routing` edit + a redeploy, with **no test-fire** to confirm the token is valid. Robusta's whole onboarding flow is `robusta gen-config` precisely because "did my sink credential work" is painful.

### 1.3 Where static YAML is a *feature*, not a bug (critical)

This is the part the feature request glosses over, and it's the crux of the whole decision.

- **Auditability & review.** `config.yaml` in Git gives you `git blame`, PR review, and a complete change history *for free*. The `PRODUCT_LAUNCH_REVIEW.md` explicitly names *"config is code, reviewed in Git"* as a selling point and a differentiator vs Robusta/BotKube.
- **Determinism & reproducibility.** Same ConfigMap → same behavior, every restart. This is *the* pitch. A mutable runtime store breaks "what you see in Git is what's running."
- **Disaster recovery.** Rebuild the cluster from Git, get the exact same alerting. A DB or mutated ConfigMap is now a *stateful* thing you must back up.
- **Security posture.** The launch review documents a genuinely top-decile security posture for a solo project: least-privilege RBAC (notably **no `secrets` read**), distroless non-root digest-pinned image, fail-closed receiver, constant-time bearer compare, SSRF guards. A mutating, authenticated, secret-touching web UI is the single largest new attack surface you could possibly bolt on, and it directly threatens several of those properties.

**Conclusion:** the problem is real but *narrow and segmented*. The job that genuinely needs runtime mutation (J2 fast silence, J4 channel test) is small; the jobs that dominate the request (J1 visibility, J3 authoring) are best served *without* abandoning Git-as-source-of-truth. Any framing that throws away the GitOps property to serve all four jobs uniformly is solving the wrong problem.

---

## 2. Users & Jobs-to-be-Done

Per the launch review's ICP: *a platform/DevOps engineer or small SRE team running 1–20 clusters, valuing determinism and security over AI bells and whistles.*

| Persona | Context | Primary job here | What they'd actually use |
|---------|---------|------------------|--------------------------|
| **Platform Engineer (primary)** | Owns the AlertKube install, writes the config, onboards teams | J3 author rules/grouping; J4 add channels | A *better authoring + validation* experience; an export-to-Git flow; a channel test button |
| **On-call SRE (primary)** | Paged at 3am, wants the noise to stop *now* | J2 fast silence; J1 see active alerts & why-suppressed | A read-only incident view + a "silence this for 2h" button |
| **App-team Developer (secondary)** | Owns a service, occasionally tunes their alerts | J1 see their alerts; maybe J2 silence their own | Read-only, scoped to their namespace |
| **Security / Compliance reviewer (constraint, not user)** | Approves what runs in-cluster | Wants every change auditable and least-privilege | Will *block* a UI that reads Secrets or mutates without an audit trail |

**Key insight:** the on-call wants *speed* (runtime), the platform engineer wants *control* (Git review). A single "mutate everything at runtime" UI serves the on-call and **betrays** the platform engineer and the security reviewer. The phased plan below splits these deliberately.

---

## 3. Research Findings (how comparable tools resolve this)

I researched how the category handles "UI vs GitOps-as-code." The pattern is remarkably consistent: **the successful, trusted tools keep declarative config as source-of-truth and make the UI either read-only or a PR-generator; the runtime-mutation surface is narrow (silences/acks) and the heavyweight runtime control planes are either SaaS or getting archived.**

### 3.1 Alertmanager + Karma / unsee — the read-only-vs-mutate split done right
Alertmanager keeps **routing config declarative** (YAML, reloaded) but exposes a **runtime silence API** for the one thing that must be fast. **Karma** is a read-only-by-default dashboard: its `readonly` upstream option *disallows silence creation/editing*, and even when it allows it, Karma itself **sends no mutating requests** — the browser talks to the Alertmanager API directly (optionally proxied). `amtool` is the CLI mutation path. The lesson: **separate the read dashboard from the narrow runtime-mutation API; default the dashboard to read-only.** This is almost exactly framing (a)+(d) below.
- https://karma-dashboard.io/docs/CONFIGURATION.html
- https://github.com/prymitive/karma

### 3.2 Grafana Git Sync — the "edit in UI → PR → merge syncs back" pattern (the model for framing C)
Grafana 12+ added **Git Sync**: you edit a dashboard in the UI, and Save triggers a workflow that **commits to a branch and opens a pull request** (with a diff/preview comment); merging the PR syncs it back. It's bidirectional and *preserves Git as source of truth while giving a UI authoring experience*. **This is the single most important precedent in this PRD**: it proves you can have a UI *and* keep GitOps, by making the UI emit reviewable changes rather than mutate live state. This is exactly the recommended framing (C) for AlertKube's rules/grouping/routing.
- https://grafana.com/blog/git-sync-grafana/
- https://grafana.com/docs/grafana/latest/as-code/observability-as-code/git-sync/

### 3.3 Robusta — config stays as code, even with a SaaS UI
Robusta's playbooks and sinks are configured in **`generated_values.yaml`** (Helm values); changing a playbook is `helm upgrade`. Sink credentials (Slack `api_key`) live in Helm values **or a referenced Secret** ("if you don't want to put your Slack key in Helm values, use a secret"). The SaaS UI sits *on top of* this; the declarative config is still the substrate. Onboarding pain is acknowledged and solved with a CLI generator (`robusta gen-config`), not a runtime mutation UI.
- https://docs.robusta.dev/master/configuration/sinks/slack.html
- https://github.com/robusta-dev/robusta/blob/master/helm/robusta/values.yaml

### 3.4 BotKube — the cost of a real runtime control plane
BotKube's self-hosted agent watches a ConfigMap and, on change, **restarts the pod** (`configWatcher`). True *no-restart* runtime config exists only via **Botkube Cloud**: the agent connects out to a **GraphQL control plane** (`api.botkube.io/graphql`) and pulls remote config. In other words, **the no-restart UX requires a whole external SaaS control plane.** That's a multi-person-year investment and a hosted service — wildly out of scope for a bus-factor-1 OSS controller. The OSS path *is* "change config → restart," which is what AlertKube already effectively is.
- https://docs.botkube.io/architecture/
- https://pkg.go.dev/github.com/kubeshop/botkube/pkg/config

### 3.5 Grafana OnCall — a cautionary tale about heavy control planes
Grafana **OnCall OSS was archived (read-only) on 2026-03-24**; Grafana now steers users to Grafana Cloud IRM. The integration/routing UI was a major surface, and the OSS version of that heavyweight control plane proved unsustainable to maintain. **Directly relevant warning for a solo maintainer:** a rich, stateful, runtime-mutation control-plane UI is exactly the kind of surface that becomes a maintenance albatross.
- https://runframe.io/comparisons/grafana-oncall-alternatives
- https://incident.io/blog/best-open-source-pagerduty-alternatives-2026

### 3.6 K8s dashboards (Headlamp / Lens / k9s) — the auth model to copy
Headlamp authenticates via a **ServiceAccount token or OIDC**, and **delegates authorization to Kubernetes RBAC** — it doesn't invent its own user/role system; it passes the user's identity to the API server and lets RBAC decide. This is the credible minimum-auth model: **don't build a bespoke auth system; ride K8s RBAC via `TokenReview`/`SubjectAccessReview` or OIDC.** Inventing users/roles/sessions in a small Go controller is both a security risk and a scope explosion.
- https://headlamp.dev/docs/latest/installation/in-cluster/oidc/
- https://docs-bigbang.dso.mil/latest/packages/headlamp/docs/RBAC/

### 3.7 Argo CD / Flux — the GitOps community's framing of UI mutation
The Argo CD community's settled position: a UI is fine as a **visualization tool**, but **"all day-2 operations should go through Git"**; direct cluster mutation creates **drift** that the controller flags as `OutOfSync`. The tension is explicit and well-known: imperative UI edits vs Git as single source of truth. The accepted resolution is *the UI helps you produce the declarative change*, it doesn't bypass it.
- https://argo-cd.readthedocs.io/en/stable/
- https://openliberty.io/blog/2024/04/26/argocd-drift-pt1.html

### 3.8 Demand signal — real but unquantified
There is a genuine, widely-noted gap: the Prometheus/Alertmanager ecosystem **lacks a comprehensive web UI for creating/managing alert rules without YAML editing**, and Kubernetes alert config is widely cited as a source of *sprawl* and *alert fatigue*. But I found **no hard, AlertKube-specific demand** (no issues, no threads) — the project has zero external adopters per the launch review, so "users are asking for this" is currently a hypothesis, not evidence. **This is the single biggest reason to start small and read-only rather than bet big on runtime mutation.**
- https://drdroid.io/engineering-tools/guide-for-kubernetes-alerting-best-practices-for-setting-alerts-in-kubernetes
- https://grafana.com/blog/the-inside-scoop-on-alerting-changes-in-kubernetes-monitoring/

### 3.9 Research synthesis (the one-paragraph takeaway)
Every tool that earned trust in this category **kept declarative config as the source of truth** and made the UI either (a) read-only, (b) a narrow runtime-mutation surface for *silences/acks only*, or (c) a PR-generator (Grafana Git Sync). The only "mutate everything at runtime" experiences are **SaaS control planes** (Botkube Cloud, Grafana Cloud IRM) — and the OSS heavyweight control plane (Grafana OnCall OSS) **got archived**. For a security-positioned, bus-factor-1, GitOps-differentiated controller, the evidence points hard at: **read-only first, PR-generator for authored config, narrow runtime mutation for silence + channel-test only.**

---

## 4. Feature Framings (brainstorm)

Six framings. For each: what it is, user value, scope, effort, and the hard problems it forces.

### Framing A — Read-only Observability UI
**What:** Embedded SPA (or server-rendered pages) that *visualizes* active alerts, recent history, the loaded config (rules, grouping, routing, channels, silences), and *why* alerts were suppressed (surfacing `alerts_suppressed_total{reason}` per-alert). **Zero mutation.**
**Value:** Directly serves J1 (the most common job) and most of the on-call's J2 need ("what's firing and why"). Makes the product *legible*.
**Scope:** New read-only handlers on the existing metrics server; embed assets via `go:embed`. Reuses the existing store's `ActiveList()`/`Recent()`.
**Effort:** **S–M.**
**Hard problems forced:** Almost none new. Auth is the same bearer token (read-only). Leader-only handlers already exist (followers 503). No secrets, no Git, no drift. **This is the safe wedge.**

### Framing B — Runtime-mutation UI + dynamic config store + hot-reload (the literal request)
**What:** UI writes changes to a live dynamic store (DB/CRD/ConfigMap); a write API + hot-reload applies them without restart. Add channels (with secrets), author rules, toggle alerts, all at runtime.
**Value:** Maximally serves all four jobs *if* you ignore GitOps. Flashiest demo.
**Scope:** New write API; dynamic store + reconcile/hot-reload; secret management; RBAC; audit log; leader-failover state propagation; drift handling vs ConfigMap. Essentially a new product.
**Effort:** **XL.**
**Hard problems forced (all at once):** GitOps drift (the moat), leader-election state propagation, secret-exfiltration surface, real RBAC, audit, validation/rollback, single-binary footprint/CSP, and DB-vs-CRD-vs-ConfigMap substrate. **Every hard problem in §6 lands simultaneously.** For bus-factor 1, this is the path to burnout and a weakened security story. **Rejected as the first move.**

### Framing C — "UI as PR / ConfigMap-diff generator" (GitOps-preserving authoring)
**What:** UI provides a great authoring/validation experience for rules, grouping, routing, channel *routing* (not secrets), and **emits a Git PR (or a rendered ConfigMap YAML diff to copy/apply)** rather than mutating live state. Modeled on **Grafana Git Sync** (§3.2). Runs `config.Validate()` server-side as live dry-run feedback.
**Value:** Serves J3 (authoring) and the *durable* part of J2/J4 (permanent disable, channel routing) **without breaking GitOps**. Preserves and *strengthens* the "config is code, reviewed in Git" differentiator.
**Scope:** Validation endpoint (reuse `Validate()`); a YAML renderer for the edited config; optional Git provider integration (GitHub/GitLab PR) OR a no-integration "download/diff this YAML" mode for v1.
**Effort:** **M** (diff/download mode) → **L** (full PR integration with stored Git creds).
**Hard problems forced:** Git credential handling (only if PR-integration; the diff/download mode dodges it entirely). No leader-state, no live drift, no in-app secrets in v1. **The most *strategically aligned* framing.**

### Framing D — Channel-management-only MVP (add / update / **test** sinks)
**What:** A thin UI to add/update a notification channel and **test-fire** it, where the secret is provided as a **reference to a K8s Secret** the operator creates (the app never stores the secret). The *non-secret* routing change is emitted as config (à la C).
**Value:** Serves J4 — the one job that *genuinely* wants runtime feedback ("did my token work?"). High-value onboarding wedge; this is what Robusta's `gen-config` exists to solve.
**Scope:** A "test this sink" endpoint that loads a Secret-by-reference and sends a synthetic alert; UI to enter Secret ref + channel + severity routing.
**Effort:** **M.**
**Hard problems forced:** Reading Secrets (RBAC change — today the controller has **no** `secrets` read, a documented security property; this must be a *separate, opt-in, narrowly-scoped* Role). Test-fire SSRF/abuse (reuse existing SSRF guard). **One hard problem (secrets), confronted in isolation.**

### Framing E — Runtime silence/disable only (the Alertmanager pattern)
**What:** A narrow **runtime mutation API for time-boxed silences and alert enable/disable**, modeled on Alertmanager's silence API + Karma (§3.1). Mutations are *ephemeral* (TTL'd) and stored in the **existing persistence ConfigMap** (`alertkube-state`) — the same place active-alert/mute state already survives restart and leader failover. *Permanent* config changes still go through Git (framing C).
**Value:** Serves J2 — the 3am job — without touching declarative config or secrets. Silences are *expected* to be ephemeral and out-of-Git (Alertmanager normalized this), so there's **no GitOps-drift objection** for time-boxed silences specifically.
**Scope:** Extend `internal/persist` to also hold runtime silences/toggles; a small write API behind a write-scoped token; the router already evaluates silences.
**Effort:** **M.**
**Hard problems forced:** Leader-failover propagation (mostly *solved already* — `persist` does RetryOnConflict read-modify-write across leader handoff). Write-vs-read auth split. No secrets, no Git drift (ephemeral by design). **Reuses the most existing machinery.**

### Framing F (recommended) — Phased composite: A → (C + E) → D
**What:** Sequence the above to de-risk: **Phase 0 = A** (read-only, ship value fast, zero risk). **Phase 1 = E + C** (runtime *silences/toggles* via the existing state ConfigMap + UI-as-PR for durable config). **Phase 2 = D** (channel add/test via Secret-references). Framing **B** is explicitly *not* a goal.
**Value:** Each phase ships standalone value, de-risks the *next* architectural decision, and **never sacrifices the GitOps/security moat**. Matches the proven category pattern (read-only dashboard + narrow runtime mutation + PR-generator authoring).
**Effort:** **S → M → M**, spread over releases, each independently shippable.
**Hard problems forced:** Each phase confronts *one* hard problem at a time (auth-read → ephemeral-state+auth-write → secrets), never all of §6 at once.

---

## 5. Scoring Matrix & Decision

Scored 1–5 (5 = best). Weights reflect AlertKube's situation: a security-positioned, GitOps-differentiated, **bus-factor-1** project with **zero adopters** (so de-risking and shippability matter more than feature completeness).

| Criterion (weight) | A Read-only | B Runtime-mutation | C UI-as-PR | D Channel+test | E Silence-only | **F Phased (rec.)** |
|---|---|---|---|---|---|---|
| **User impact** (25%) | 3 | 5 | 4 | 4 | 4 | **5** |
| **Effort / shippability** (20%) | 5 | 1 | 3 | 3 | 3 | **4** |
| **Risk** (security/ops/maint.) (20%) | 5 | 1 | 4 | 3 | 4 | **4** |
| **GitOps alignment** (differentiation) (20%) | 5 | 1 | 5 | 4 | 4 | **5** |
| **Differentiation / "wow"** (15%) | 2 | 5 | 4 | 4 | 3 | **4** |
| **Weighted total** | **4.05** | **2.40** | **4.05** | **3.55** | **3.65** | **4.45** |

**Decision: Framing F — the phased composite.** It tops the matrix, and more importantly it's the only option that delivers near-B-level eventual user impact while **never** putting the GitOps/security moat or the solo maintainer at the risk B demands.

### Trade-offs I am explicitly accepting
- **Slower to the flashy demo.** Phase 0 is "just" a read-only dashboard; it won't wow HN the way "edit anything live" would. I accept this — the launch review already says the credibility problem is *proof and narrative*, not missing flash, and an over-claimed mutating UI is exactly the kind of thing HN shreds.
- **Durable config edits still require a Git round-trip (a PR).** Platform engineers don't get instant live mutation for rules/routing. I accept this **on purpose** — it's the feature, not the bug. Grafana, Argo, and Robusta all landed here.
- **No general hot-reload of `config.yaml`.** We are *not* adding fsnotify/SIGHUP for the whole config in this plan (runtime mutation is scoped to ephemeral silences + channel-test). I accept the limitation to protect determinism.
- **Channel secrets stay as K8s-Secret references (Phase 2), never stored in-app.** Slightly less magical than "paste your token in the box," but it preserves the no-app-secret-store property that the security audit depends on.

### Why not the literal request (B), stated plainly
B is rejected because it forces the project to simultaneously (1) break its #1 differentiator (GitOps determinism), (2) take on the largest possible new attack surface on a project whose *entire reputation* is security rigor, (3) solve leader-failover state propagation, secret storage, RBAC, audit, and drift *before any user benefit ships*, and (4) do all of that with one maintainer. The category evidence (§3) is unanimous that the only people who pull off "mutate everything at runtime" do it as a funded SaaS — and the OSS attempt at a heavy control plane (Grafana OnCall) got archived. **The same user value is reachable via F without any of that risk.**

---

## 6. Confronting the Hard Problems (for the recommended framing F)

Mapped to each phase so it's clear *when* each problem actually has to be solved.

### 6.1 GitOps drift / source-of-truth
**The core tension:** if the UI mutates runtime state, the ConfigMap in Git no longer reflects reality. AlertKube's moat dies the moment "what's in Git" diverges from "what's running" with no reconciliation.
**F's answer — segment by mutation lifetime:**
- **Durable config (rules, grouping, routing, severity overrides, channel routing):** *never* mutated live. The UI (framing C, Phase 1) **emits a PR or a downloadable ConfigMap diff**. Git stays source of truth; the existing `helm upgrade`/GitOps flow applies it. Modeled on Grafana Git Sync (§3.2). **Zero drift by construction.**
- **Ephemeral runtime state (time-boxed silences, temporary enable/disable):** these are *expected* to live outside Git — Alertmanager normalized this for a decade (§3.1). They're stored in the existing `alertkube-state` ConfigMap (operational state, not config), TTL'd, and clearly labeled in the UI as "temporary — not in Git; expires at T." **No drift objection because nobody expects ephemeral silences in Git.**
- **Drift visibility:** the read-only UI (Phase 0) should *show* when a runtime silence is active that isn't in `config.yaml`, exactly as Argo CD surfaces `OutOfSync` (§3.7). Honesty about drift is itself a feature.

### 6.2 Leader election & state-sync across failover
**The problem:** handlers + store live only on the leader (`controller.go`/`metrics.go`); followers serve only `/healthz`+`/metrics` and 503 the dynamic handlers. A write to the leader must survive failover and reach the new leader.
**F's answer:**
- **Read-only (Phase 0):** trivial — the new leader rebuilds the store on `runController` re-entry (`controllerRuns` already handles re-entrancy); the UI just reads whatever leader is active.
- **Ephemeral silences (Phase 1, framing E):** **reuse `internal/persist`.** It already does a conflict-retried read-modify-write to the `alertkube-state` ConfigMap *specifically to survive leader handoff* (the code comments call out the outgoing/incoming-leader double-write race and solve it with `RetryOnConflict`). Runtime silences ride the same snapshot. On failover, the new leader's `runController` already `Load()`s that ConfigMap before informers start. **This problem is ~80% already solved by existing code** — a major reason E is cheap.
- **The 503-on-non-leader contract is preserved:** writes only land on the leader; followers reject. The existing `Clear*Handler()` on demotion already prevents a demoted leader from accepting writes into an abandoned store — extend that same pattern to the new write handler.

### 6.3 Secret management for channels (the scariest part)
**The problem:** sink credentials (Slack token, PagerDuty key) are **env vars / K8s Secrets** today, and the controller's RBAC **deliberately cannot read Secrets** — a documented, audited security property. A UI that "adds a channel" must not become a secret-exfiltration surface or a secret store.
**F's answer (Phase 2, framing D) — references, not storage:**
- The UI **never stores or transmits raw secrets.** The operator creates the K8s Secret out-of-band (`kubectl create secret`); the UI accepts a **Secret *reference*** (name/key) plus non-secret routing (channel, severity).
- **The sinks already read credentials from the environment per-send** (verified in `internal/sinks/slack.go`: `os.Getenv("SLACK_BOT_TOKEN")` is read inside `Send`, with a comment that this honors Secret rotation without restart). So "add a channel" mostly means "route to an existing sink + point at a Secret-backed env var," not "ingest a credential."
- **Test-fire** loads the referenced Secret *at send time only*, sends one synthetic alert through the existing sink + SSRF guard, and **never echoes the secret back** to the UI — only "ok / failed: <reason>."
- **RBAC is the price:** test-fire requires `get` on the *specific* referenced Secret. This must be a **separate, opt-in Role** (e.g. `secrets: get` limited by name where possible), *off by default*, documented as "enabling channel-test grants the controller read on these Secrets." The default install keeps the zero-secrets-read posture. **Make the security trade-off explicit and opt-in — never silent.**

### 6.4 AuthZ / RBAC (today = one static bearer token)
**The problem:** `internal/authz/bearer.go` is a single static token, no users/roles/sessions. That's fine for a read-only metrics-port endpoint; it's **not** credible for a mutating UI.
**F's answer — phase the auth up, ride K8s, don't invent a user system (per Headlamp, §3.6):**
- **Phase 0 (read-only):** keep the existing bearer token (read-only data, same risk class as `/api/alerts` today). Ship behind the chart's NetworkPolicy as today.
- **Phase 1 (mutation):** introduce a **read token vs write token** split at minimum (cheap, immediate). The write path requires the stronger credential.
- **Phase 1.5 / Phase 2 (credible):** delegate to Kubernetes — accept a user's bearer token and call **`TokenReview` + `SubjectAccessReview`** (or OIDC), exactly as Headlamp does, so "can this user create a silence" is answered by K8s RBAC, not a bespoke role table. **Do not build users/roles/sessions in-app.** This keeps the security surface auditable and reuses the cluster's existing identity.
- **Never** put the mutating UI on an unauthenticated port; the receiver's fail-closed precedent (`ALERTKUBE_RECEIVER_TOKEN` is *fatal* if empty) is the right bar — apply it to writes.

### 6.5 Audit log (who changed what)
**The problem:** mutations need attribution or they fail every security/compliance review.
**F's answer:**
- **Durable config (framing C):** audit is **free** — it's a Git PR with an author, timestamp, and diff. This is *better* audit than most apps build, and it's another reason the PR-generator framing wins.
- **Ephemeral silences (framing E):** record `{who (from the auth identity), what matcher, until, created_at}` in the `alertkube-state` snapshot and surface it in the read-only UI ("silenced by alice until 14:00"). Emit a structured klog line and a Prometheus counter (`alertkube_runtime_mutations_total{action,result}`) so audit is observable in the existing metrics/logging stack — no new infra.

### 6.6 Config validation, dry-run, rollback / versioning
**The problem:** a mutating/authoring UI must validate before applying, and allow rollback.
**F's answer — reuse `config.Validate()`:**
- **Dry-run:** the existing `Config.Validate()` is already a pure function over the parsed config. Expose it as a **`POST /api/config/validate`** endpoint so the UI gives live feedback while authoring (framing C). This is high-value, low-effort, and reuses tested code.
- **Rollback / versioning:** for durable config, **Git is the version store** (revert the PR). For ephemeral silences, they're TTL'd and individually deletable — "rollback" is "expire it now." **No new versioning system needed.**

### 6.7 Single-binary footprint & supply chain
**The problem:** AlertKube's distroless, digest-pinned, cosign-signed, SBOM'd, minimal-dependency image is a security selling point. Embedding a UI must not bloat the image or weaken CSP.
**F's answer:**
- **Embed via `go:embed`** (single binary preserved; no second service, no new deploy unit). The Go embed pattern is well-trodden for SPAs; minify assets so the binary grows by a small, bounded amount (tens to low-hundreds of KB for a lean UI, not MB). Keep the SPA dependency-light (consider a small framework or even server-rendered HTML + htmx to minimize npm supply-chain surface — relevant given the security positioning).
- **CSP:** serve a strict `Content-Security-Policy` (no inline scripts, `default-src 'self'`), set correct content-types, and serve assets with `http.ServeContent`. The SPA must not introduce a CDN dependency (offline/airgapped installs are part of the ICP).
- **Keep it on the leader, behind the NetworkPolicy**, same as the existing endpoints. No new listening port unless the maintainer wants UI traffic isolated from the metrics scrape path (a reasonable later option).

### 6.8 CRD vs ConfigMap vs DB substrate — recommendation
**The decision:** what backs the dynamic/runtime state?

| Substrate | Pros | Cons | Verdict |
|---|---|---|---|
| **DB (Postgres/SQLite)** | Rich queries, real versioning | New stateful dependency, backups, breaks "just a controller," supply-chain bloat, contradicts low-footprint pitch | **No.** Kills the footprint moat; massive over-build for this scope. |
| **CRD** | `kubectl get silences`, per-object RBAC, admission validation, status subresource | Requires controller-runtime (supersedes ADR-0001), CRD lifecycle weight; the existing `crd-sketch.md` explicitly says **"do not build until concrete demand"** | **Not now** — but it's the *right long-term home for durable config* (rules/routes/silences) if/when demand appears. Phase 1's PR-generator can later target CRDs instead of ConfigMap YAML with no UI rewrite. |
| **ConfigMap (existing `alertkube-state`)** | Already exists, already survives restart **and leader failover** (`persist` solves the handoff race), no new dependency, no new RBAC | 1 MiB object cliff (already guarded at 900 KiB in `persist`), not per-object RBAC | **Yes — for Phase 1 ephemeral runtime state.** Reuse it. |
| **Git / ConfigMap-via-Helm (existing `config.yaml`)** | Source of truth, audit, determinism — the moat | Not "instant runtime" | **Yes — for all durable config** (framing C emits changes here). |

**Recommendation:** **Two substrates, by lifetime.** Durable config → **stays in Git/ConfigMap** (UI emits PRs/diffs, framing C). Ephemeral runtime state → **the existing `alertkube-state` ConfigMap** via `internal/persist` (framing E). **Introduce neither a DB nor CRDs for this initiative.** Keep CRDs as the documented future home for durable config *if* per-object RBAC demand materializes (revisit ADR-0001 then, exactly as the CRD sketch prescribes) — and note that framing C's renderer is the natural migration tool to get there.

---

## 7. Phased Plan

### Phase 0 — Read-only Observability UI (MVP) — effort S–M
**Smallest shippable slice that delivers real value and de-risks the rest.**
- Embedded SPA via `go:embed`, served on the leader behind the existing bearer auth + NetworkPolicy.
- Views: **active alerts** (from `store.ActiveList()`), **recent history** (`store.Recent()`), **loaded config** (rules, grouping, routing, channels, silences — read from the in-memory `*config.Config`), and **why-suppressed** (surface `alerts_suppressed_total{reason}` and, ideally, per-fingerprint suppression reasons).
- `POST /api/config/validate` endpoint wrapping `config.Validate()` (sets up Phase 1).
- **Explicitly NOT in Phase 0:** any mutation, any secret access, any Git integration, any RBAC change.
- **Why first:** serves the most common job (J1), proves the embed/CSP/footprint approach, and ships value with essentially zero new risk. This is the de-risking spike *and* a real feature.

### Phase 1 — Runtime silences (E) + UI-as-PR authoring (C) — effort M
- **Runtime silences/toggles:** write API behind a **write-scoped** token (read/write token split); silences stored in `alertkube-state` via `persist`; router already enforces silences. Audit `{who, matcher, until}` in the snapshot + a `runtime_mutations_total` counter. Read-only UI shows active runtime silences and flags drift vs Git.
- **UI-as-PR/diff authoring:** UI edits to rules/grouping/routing produce a **rendered ConfigMap YAML diff** (v1: download/copy; v1.1: optional GitHub/GitLab PR). Live `Validate()` feedback while editing.
- **Auth hardening:** introduce `TokenReview`/`SubjectAccessReview` (or OIDC) for the write path, per Headlamp's model.
- **Explicitly NOT in Phase 1:** secrets, in-app credential storage, full config hot-reload.

### Phase 2 — Channel add / update / test (D) — effort M
- UI to add a channel as **non-secret routing + a K8s-Secret reference**; **test-fire** endpoint that loads the referenced Secret at send-time, sends one synthetic alert via the existing sink + SSRF guard, returns ok/fail (never echoes the secret).
- **Opt-in, narrowly-scoped `secrets: get` Role**, off by default, documented as an explicit security trade-off. Default install keeps zero-secrets-read.
- Routing change emitted as config (framing C), not mutated live.

### Phase 3 (later, only if demand proven) — CRDs for durable config
- If per-object RBAC / `kubectl get silences` / GitOps-per-rule demand materializes, promote durable config to CRDs (revisit ADR-0001, adopt controller-runtime). Framing C's renderer becomes the migration path. **Gated on real adopter demand**, per the existing CRD sketch.

### Success Metrics

| Phase | Metric | Baseline | Target | Window |
|---|---|---|---|---|
| 0 | Adopters who enable the UI | 0 | ≥ 3 of first 5 adopters | 60 days post-release |
| 0 | "How do I see what's firing/why" support questions | (qualitative) | UI is the answer; questions drop | 90 days |
| 0 | Image size delta from embed | — | < +1 MB | at merge |
| 1 | Time-to-silence a noisy alert (3am job) | minutes–hours (YAML+restart) | < 30 seconds via UI | on release |
| 1 | Durable config changes that went through a PR (not live mutation) | n/a | 100% (drift = 0 by construction) | ongoing |
| 2 | Channel onboarding: time to validated working channel | redeploy + guess | one test-fire, < 2 min | on release |
| 2 | Installs that opted into `secrets: get` | — | tracked; should stay a *minority* (proves opt-in works) | ongoing |
| all | New P1/P2 security findings attributable to the UI | 0 | **0** | every audit |

---

## 8. Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| **UI weakens the security posture** (the project's #1 asset) | Med | High | Read-only first; K8s-Secret refs not storage; opt-in secrets Role; strict CSP; `go:embed` not a second service; security review each phase |
| **GitOps moat eroded by live mutation** | Low (in F) | High | Durable config is *only ever* a PR/diff (framing C); live mutation limited to ephemeral, TTL'd silences that nobody expects in Git |
| **Maintainer burnout (bus-factor 1)** building XL scope | Med | High | Phased S→M→M; each phase ships alone; framing B explicitly out of scope; lean/server-rendered UI to cut npm/supply-chain load |
| **Embedded SPA supply-chain bloat** undercuts the lean image | Med | Med | Minify; dependency-light UI (consider htmx/server-render); SBOM the embedded assets; no CDN dependency |
| **Auth under-built for a mutating endpoint** | Med | High | Read/write token split immediately; `TokenReview`/`SAR` or OIDC before any GA of mutation; fail-closed like the receiver |
| **We build it and nobody wanted it** (zero current demand signal) | Med | Med | Phase 0 is cheap and independently valuable; gate Phases 1–2 on observed Phase-0 usage + adopter asks |
| **Test-fire becomes an abuse/SSRF vector** | Low | Med | Reuse existing SSRF guard; rate-limit; write-auth required; never echo secrets |

---

## 9. Open Questions & Decisions for the Maintainer

These are the calls only the maintainer can make.

1. **Is GitOps-as-source-of-truth a hill you defend, or a default you'd trade away?** My entire recommendation assumes you *defend* it (the launch review says it's a differentiator). If you actually believe target users want to *leave* GitOps behind, that's a different PRD — and I'd want hard evidence (issues/interviews) before believing it. **This is the load-bearing decision.**
2. **Are you willing to add an opt-in `secrets: get` RBAC Role for channel-test (Phase 2)?** This is the one place the celebrated zero-secrets-read posture bends. If "never read Secrets" is sacred, Phase 2 becomes "routing-only, no test-fire," and channel validation stays manual. Your call on the trade.
3. **Auth ceiling: bearer-token split, or full K8s `TokenReview`/SAR / OIDC?** The former is days; the latter is the credible-for-mutation bar but more work. How much auth are you willing to build/maintain before exposing *any* write path?
4. **Embed the UI in the single binary, or accept a second (optional) image/component?** Embedding preserves the single-binary story (my recommendation) but couples UI releases to controller releases. A separate read-only UI component decouples them at the cost of the "one binary" pitch.
5. **UI tech: SPA (React/Svelte) vs server-rendered (Go templates + htmx)?** Given the security positioning and solo maintenance, I lean server-rendered/htmx to minimize the npm supply-chain surface — but if you want a richer interactive authoring experience for rules, an SPA may be worth the cost.
6. **Sequencing vs the launch:** the launch review says the next moves are *proof, narrative, and a co-maintainer* — **not more engineering**. Does the UI come **after** seeding adopters and the public launch (my strong recommendation — Phase 0 makes a great post-launch v0.4), or do you want it as a launch headline (riskier; an immature mutating UI is HN-shred bait)?

---

## 10. Appendix — Source Map

- Karma read-only/silence model — https://karma-dashboard.io/docs/CONFIGURATION.html , https://github.com/prymitive/karma
- Grafana Git Sync (UI→PR pattern, the model for framing C) — https://grafana.com/blog/git-sync-grafana/ , https://grafana.com/docs/grafana/latest/as-code/observability-as-code/git-sync/
- Robusta config-as-code + secret refs — https://docs.robusta.dev/master/configuration/sinks/slack.html , https://github.com/robusta-dev/robusta/blob/master/helm/robusta/values.yaml
- BotKube architecture / configWatcher restart + Cloud GraphQL control plane — https://docs.botkube.io/architecture/ , https://pkg.go.dev/github.com/kubeshop/botkube/pkg/config
- Grafana OnCall OSS archived (heavy control-plane cautionary tale) — https://runframe.io/comparisons/grafana-oncall-alternatives , https://incident.io/blog/best-open-source-pagerduty-alternatives-2026
- Headlamp auth (OIDC + K8s RBAC delegation; the auth model to copy) — https://headlamp.dev/docs/latest/installation/in-cluster/oidc/ , https://docs-bigbang.dso.mil/latest/packages/headlamp/docs/RBAC/
- Argo CD / GitOps drift framing — https://argo-cd.readthedocs.io/en/stable/ , https://openliberty.io/blog/2024/04/26/argocd-drift-pt1.html
- Demand signal (UI-for-alert-rules gap; config sprawl/fatigue) — https://drdroid.io/engineering-tools/guide-for-kubernetes-alerting-best-practices-for-setting-alerts-in-kubernetes , https://grafana.com/blog/the-inside-scoop-on-alerting-changes-in-kubernetes-monitoring/
- Go embed for SPAs — https://www.smartinary.com/blog/how-to-embed-a-react-app-in-a-go-binary/

**Repo files verified for this PRD:** `internal/config/config.go` (schema + `Validate()`), `controller.go` (leader-only handlers, `Clear*Handler` on demotion, store), `main.go` (static `config.Load` at boot, no hot-reload), `metrics.go` (HTTP surface, dynamic-handler 503-when-nil), `internal/authz/bearer.go` (single static token), `internal/persist/persist.go` (ConfigMap state survives leader handoff via `RetryOnConflict`), `internal/rules/rules.go` (Count/All/Absent engine), `internal/sinks/slack.go` (per-send `os.Getenv` credential read), `leaderelection.go` (Lease, leader/follower roles), `docs/design/crd-sketch.md` + `docs/decisions/0001-client-go-over-controller-runtime.md` (CRD future + ADR to revisit).
