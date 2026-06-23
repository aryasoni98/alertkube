# AlertKube — Positioning

> Internal positioning reference. The job of this doc is to make every other
> piece of copy (README, launch post, talks) say the same true thing in the
> same order. Lead with Kubernetes-native alerting. Cloud sources are an
> **experimental bonus**, never the headline.

---

## One-line pitch

**AlertKube watches your Kubernetes resources and pages you on the things that actually break — CrashLoops, OOMKills, node pressure, failed Jobs, missed CronJobs, stuck PVCs, maxed HPAs — with deterministic routing, dedupe, suppression, and auto-resolve, delivered to Slack/PagerDuty/Teams/Opsgenie/Discord/Telegram/webhook in one Helm install. No Prometheus stack required.**

Shorter, for a headline: **Batteries-included Kubernetes alerting. No Prometheus required.**

---

## What it actually does (the stable core)

A single controller that watches the Kubernetes API directly — **Pods, Nodes,
Deployments, PVCs, Jobs, DaemonSets, StatefulSets, CronJobs, HPAs** — and turns
real failure conditions into routed, deduplicated, auto-resolving alerts:

- **Watchers:** pod restarts/crashloops/OOM/SIGKILL/image-pull, node
  readiness/pressure/cordon, workload availability, failed Jobs, missed
  CronJobs, maxed HPAs, lost/pending PVCs.
- **Classification:** every condition is `critical` / `warning` / `info`, with
  per-org severity overrides.
- **Dedupe:** `sha256(kind|namespace|name|reason)` fingerprint + a mute window,
  so a flapping pod pages you once, not fifty times.
- **Suppression:** time-bounded silences, source→target inhibitions (e.g. mute
  pod alerts on a node that's NotReady), optional storm grouping.
- **Resolve:** alerts auto-resolve when the condition clears; PagerDuty/Opsgenie
  incidents close themselves. State survives controller restarts via a
  ConfigMap snapshot, so you don't get dangling incidents or re-pages.
- **Delivery:** Slack, PagerDuty, Teams, Opsgenie, Discord, Telegram, generic
  signed webhook, stdout — plus an **Alertmanager webhook receiver** so it
  *complements* an existing Prometheus setup rather than fighting it.
- **Ops surface:** `/metrics`, `/healthz`, `/readyz`, `/api/alerts`,
  ServiceMonitor, a self-health PrometheusRule, and a Grafana dashboard.

## The experimental tier (honest framing)

**Multi-cloud sources** (`internal/sources`) and the **correlation-rules engine**
(`internal/rules`) are shipped **off by default and explicitly experimental**.
Cloud sources poll provider control planes (18 AWS / 6 Azure / 4 GCP services)
and flow through the *same* dedupe→route→group→sink pipeline. They are
unit-tested against recorded SDK responses but **not yet validated against live
cloud accounts at scale.** We say this in the CHANGELOG and the Helm values, and
we say it in the launch copy. Do not let cloud become the headline — it dilutes
a clean "Kubernetes-native" story and it's the least-proven surface.

---

## ICP — who this is for

**Primary:** A platform / DevOps engineer or a small SRE team (roughly **1–20
clusters, 2–15 engineers**) running Kubernetes **without a full
Prometheus + Alertmanager + kube-state-metrics stack** — or running it but tired
of writing PromQL to alert on basic resource health.

**Their pain:**
- They find out a pod has been CrashLooping for an hour from a *user*, not an alert.
- Standing up Prometheus + Alertmanager + kube-state-metrics + rules + routing
  just to get "tell me when a pod dies" feels like buying a data center to send
  one email.
- The event-forwarder tools they tried (kubewatch, BotKube) firehose every
  event into Slack and get muted within a week — no real dedupe, no inhibition,
  no auto-resolve.

**Why they pick AlertKube:** one Helm install, opinionated low-noise defaults,
deterministic config they can read and diff in Git, hardened by default
(distroless non-root, least-privilege RBAC, satisfies Pod Security `restricted`),
and it pages the right people on the things that matter without a metrics stack.

**Who it is NOT for (say this plainly):**
- Large orgs already deep in kube-prometheus-stack with mature Alertmanager
  routing — AlertKube is mostly redundant there (use it only as a complement
  for K8s-event-type alerts Prometheus handles poorly, via the receiver).
- Teams that want **AI triage or auto-remediation** — that's Robusta, not us.

---

## Why AlertKube over X

### vs Prometheus Alertmanager (the default)
- **No metrics stack to run.** Alertmanager needs Prometheus +
  kube-state-metrics + PromQL rules to alert on a CrashLoop; AlertKube watches
  the API directly and ships the routing/suppression engine built in.
- **It complements, it doesn't compete.** AlertKube has an Alertmanager webhook
  *receiver*, so if you already run Prometheus you can funnel its alerts through
  AlertKube's dedupe/inhibition/multi-sink pipeline too.
- **Honest limit:** Alertmanager is the ecosystem standard and the right tool
  for metric-threshold alerting. If you're going to run Prometheus anyway, that
  changes the math — we won't pretend otherwise.

### vs Robusta
- **Deterministic, not AI.** AlertKube's routing/suppression is config you read
  and diff in Git — no model in the path, no surprises, fully reproducible.
- **No Prometheus dependency.** Robusta extends Prometheus; AlertKube doesn't
  need it.
- **Honest limit:** Robusta does AI investigation, alert enrichment, and
  **auto-remediation** — AlertKube does none of that and isn't trying to. If you
  want self-healing and AI triage, Robusta is the better fit. We're the simpler,
  deterministic, security-hardened alerting engine.

### vs BotKube / kubewatch / k8s-event-exporter
- **A real alerting engine, not an event firehose.** These forward events;
  AlertKube adds fingerprint dedupe, mute windows, inhibitions, storm grouping,
  and **auto-resolve** so your Slack channel stays signal, not noise.
- **Multi-sink routing as deterministic config** — match by severity / kind /
  namespace / reason / labels and fan out to the right sinks, including
  PagerDuty/Opsgenie incident lifecycle (open *and* close).
- **Honest limit:** BotKube has interactive ChatOps and an AI assistant;
  kubewatch is now Robusta-maintained and well-known. AlertKube's edge is the
  suppression/resolve pipeline and security posture, not breadth of ChatOps.

---

## Elevator paragraph (publish-ready)

> AlertKube is a single Kubernetes controller that watches your cluster's core
> resources — Pods, Nodes, Deployments, PVCs, Jobs, DaemonSets, StatefulSets,
> CronJobs, and HPAs — and pages you on the conditions that actually mean
> something: CrashLoops, OOMKills, node pressure, failed Jobs, missed CronJobs,
> stuck PVCs. It deduplicates by fingerprint, suppresses noise with silences and
> inhibitions, auto-resolves when conditions clear, and delivers to eight sinks
> (Slack, PagerDuty, Teams, Opsgenie, Discord, Telegram, signed webhook, stdout)
> via routing rules you define in plain config. It installs with one Helm chart,
> needs no Prometheus stack, and is hardened by default — a distroless non-root
> image, least-privilege RBAC, and a Pod Security `restricted`-compliant
> workload out of the box. It also *complements* Prometheus via an Alertmanager
> webhook receiver. Multi-cloud control-plane sources (AWS/Azure/GCP) and a
> correlation-rules engine ship as opt-in, clearly-labeled experimental
> features; the Kubernetes watchers are the stable, production-oriented core.
