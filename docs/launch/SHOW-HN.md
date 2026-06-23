# Show HN draft

> Post-ready. Pick one title, paste the body. The comment section is the real
> launch — plan to be present for the first 48 hours. HN rewards a responsive,
> candid author and punishes a silent or defensive one. The pre-empted critiques
> below are the questions you *will* get; answering them in the post earns trust.

---

## Title options

1. **Show HN: AlertKube – Kubernetes alerting to Slack/PagerDuty, no Prometheus required**
2. **Show HN: AlertKube – batteries-included K8s alerts with dedupe, suppression, and auto-resolve**
3. **Show HN: AlertKube – watch K8s resources, page on real failures, no metrics stack**

(#1 leads with the clearest wedge — "no Prometheus required" — and the most
recognized sinks. Recommended.)

---

## Body

Hi HN — I'm Arya, and AlertKube is a single Kubernetes controller that watches
your cluster's core resources and pages you when they actually break.

It watches **Pods, Nodes, Deployments, PVCs, Jobs, DaemonSets, StatefulSets,
CronJobs, and HPAs** directly off the Kubernetes API and alerts on the
conditions that matter — CrashLoopBackOff, OOMKills, non-OOM SIGKILLs,
ImagePullBackOff, node NotReady/pressure/cordon, failed Jobs, missed CronJobs,
maxed HPAs, lost/pending PVCs.

The part I actually care about is what happens *after* it detects something:

- **Dedupe** by `sha256(kind|namespace|name|reason)` + a mute window, so a
  flapping pod pages you once, not fifty times.
- **Suppression**: time-bounded silences and source→target inhibitions (mute the
  pod alerts on a node that's NotReady), plus optional storm grouping.
- **Auto-resolve**: alerts clear themselves when the condition clears, and
  PagerDuty/Opsgenie incidents close. State survives controller restarts via a
  ConfigMap snapshot, so you don't get dangling incidents or re-pages on every
  redeploy.
- **Routing** to eight sinks — Slack, PagerDuty, Teams, Opsgenie, Discord,
  Telegram, signed generic webhook, stdout — by matching severity / kind /
  namespace / reason / labels.

It installs with one Helm chart and **needs no Prometheus, no
kube-state-metrics, no PromQL**. The whole thing is one distroless, non-root,
digest-pinned image with least-privilege RBAC (no wildcards, no `secrets` read,
no cluster-admin) that satisfies the Pod Security `restricted` profile out of the
box. There's a `/metrics` endpoint, health/readiness probes, a ServiceMonitor, a
self-health PrometheusRule, and a Grafana dashboard if you want them.

It also ships an **Alertmanager webhook receiver**, so if you *do* run
Prometheus, you can funnel its alerts through the same dedupe/inhibition/routing
pipeline instead of replacing anything.

Apache-2.0. Install:

```
helm upgrade --install alertkube oci://ghcr.io/aryasoni98/charts/alertkube \
  --set cluster=my-cluster \
  --set slack.webhookUrl=https://hooks.slack.com/services/...
```

Repo: https://github.com/aryasoni98/alertkube

### Why I built it

Every place I've worked either ran the full kube-prometheus-stack just to get
"tell me when a pod dies," or wired kubewatch/BotKube straight into Slack and
muted the channel within a week because there was no real dedupe or
auto-resolve. I wanted the middle ground: a serious alerting engine
(suppression, inhibition, resolve, multi-sink routing) that you can install in
one command and read as plain config in Git, without standing up a metrics
platform.

### Where it sits vs the usual suspects (honestly)

- **vs Prometheus Alertmanager**: it complements it (there's a receiver) more
  than it replaces it. If you're already running Prometheus, Alertmanager is the
  right tool for metric-threshold alerts — AlertKube's win is *not needing the
  stack* to alert on K8s resource health.
- **vs Robusta**: Robusta does AI investigation and auto-remediation on top of
  Prometheus. AlertKube does neither and isn't trying to — it's deterministic
  config, no model in the path, no Prometheus dependency.
- **vs BotKube / kubewatch**: those forward events; AlertKube adds the
  dedupe/suppress/resolve pipeline so the channel stays signal.

### What's NOT done yet (because you'll ask, and you should)

- **It's solo-maintained.** That's the honest bus-factor right now, and I'm
  actively looking for a co-maintainer (there's a call-for-maintainers thread).
  I don't want anyone betting prod on a one-person project without knowing that.
- **Zero public adopters so far.** I've run it on my own clusters; I haven't yet
  collected outside production stories. I'd rather say that than fake a logo
  wall. If you try it, an ADOPTERS.md PR (even "evaluating" or "home lab")
  genuinely helps.
- **Cloud sources are experimental.** There's an opt-in, off-by-default
  multi-cloud layer (18 AWS / 6 Azure / 4 GCP control-plane checks) and a
  correlation-rules engine. They're unit-tested against recorded SDK responses
  but **not yet validated against live cloud accounts at scale** — treat them as
  preview, not the reason to adopt. The Kubernetes watchers are the stable core.
- **Config is ConfigMap-based, not CRDs.** Deliberate (one ADR explains why),
  but it means no `kubectl get`-able rule objects or admission validation yet.
  There's a CRD design sketch if/when that becomes the right move.
- **Coverage is ~57%** and e2e currently runs in CI on kind (1.29–1.31), not yet
  proven across a fleet of real managed clusters under storm conditions.

Happy to go deep on the dedupe/inhibition design, the security model, or why I
chose client-go over controller-runtime. Feedback very welcome — especially from
anyone running K8s without Prometheus, since that's the case I built this for.
