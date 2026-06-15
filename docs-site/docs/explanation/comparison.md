# How alertkube compares

Kubernetes alerting is a crowded space, and the honest answer to "should I use
alertkube?" is "it depends what you already run." alertkube is not trying to
replace your metrics stack or your incident manager. It occupies a specific
niche: a **Kubernetes-native, multi-resource event watcher** with **deterministic
suppression** and **multi-sink routing**, built as a single small Go binary. This
page positions it honestly against four neighbors — kwatch, Botkube, Robusta, and
Prometheus Alertmanager — including where each of them does something alertkube
does not.

The most useful framing up front: alertkube is *complementary* to Prometheus
Alertmanager, not a competitor to it. The two operate on different signals
(Kubernetes object state vs time-series metrics), and alertkube can even ingest
Alertmanager's webhooks and route them through its own pipeline.

## What alertkube is, precisely

alertkube watches a broad set of Kubernetes resources directly through informers —
Pods, Nodes, Deployments, StatefulSets, DaemonSets, Jobs, CronJobs, PVCs, and
HPAs — detects bad conditions on them (crashloops, OOMs, image-pull failures,
node pressure, unavailable workloads, missed CronJobs, maxed-out HPAs, and so on),
and turns each into a severity-tiered alert. Those alerts pass through
[fingerprint-based dedupe](fingerprint-and-dedup.md), the
[silence/inhibition/mute suppression triple](silence-vs-inhibition-vs-mute.md),
optional storm-folding, and fan out to one of eight sinks (Slack, PagerDuty,
Teams, Opsgenie, Discord, Telegram, generic webhook, stdout).

Its distinguishing properties are: it reads *object state* rather than metrics, it
covers many resource kinds out of the box, its suppression is
[fully deterministic](deterministic-design.md), and it ships as one distroless
binary with optional HA via leader election.

## The comparison

| | alertkube | kwatch | Botkube | Robusta | Prometheus Alertmanager |
|---|---|---|---|---|---|
| **Primary signal** | K8s object state across 9 resource kinds | Pod/container crash events | K8s events + resource changes, command interface | K8s events + Prometheus alerts, with playbooks | Time-series alerts from Prometheus rules |
| **Scope** | Multi-resource watcher + router | Lightweight crash watcher | ChatOps + monitoring assistant | Observability/automation platform | Alert dedupe, grouping, routing, silencing |
| **Suppression** | Deterministic mute + silence + inhibition | Minimal | Filters | Rule/playbook-based | Mature: inhibition, grouping, silences, routing tree |
| **Sinks** | 8 (Slack, PagerDuty, Teams, Opsgenie, Discord, Telegram, webhook, stdout) | Several chat sinks | Many (chat-first) | Many (sinks + actions) | Many receivers via integrations |
| **Two-way ChatOps** | No (one-way alerts) | No | Yes (run commands from chat) | Partial (interactive actions) | No |
| **Automated remediation** | No | No | Limited | Yes (playbooks/actions) | No |
| **Metrics-based alerting** | No (ingests Alertmanager webhooks instead) | No | Via integrations | Yes (consumes Prometheus) | Yes (its core job) |
| **Footprint** | Single Go binary, distroless | Very light | Heavier (agent + backend) | Heavier (platform) | Light, but needs Prometheus |
| **Determinism guarantee** | Explicit project value | Implicit | Mixed | Mixed (playbooks can call out) | Deterministic core |

## Where the others are stronger

It would be dishonest to present alertkube as a strict superset of any of these.
Each neighbor does something better, and for many teams that something is the
deciding factor.

**Prometheus Alertmanager** has the most mature suppression and routing model in
the ecosystem, period. Its routing tree, grouping, inhibition rules, and silence
UI have been hardened over years of production use at enormous scale. If your
alerting is *metrics-driven* — latency SLOs, error rates, saturation — Alertmanager
is the right tool, because alertkube does not evaluate time-series at all.
alertkube's suppression model is deliberately simpler and is scoped to *object-state*
alerts.

**Robusta** is a platform, not just a notifier. Its playbook engine can take
*action* — restart a workload, gather diagnostics, run a custom remediation — in
response to an alert, and it integrates Prometheus alerting with Kubernetes event
context. If you want automated remediation and a richer investigation workflow,
Robusta does substantially more than alertkube, which is intentionally a one-way
notifier that decides *whether and where* to alert and stops there.

**Botkube** is built around two-way ChatOps: you can run `kubectl`-style commands
and drive cluster operations from your chat tool, and its event sourcing and
filtering are mature. alertkube sends alerts *to* chat but does not let you operate
the cluster *from* chat. If interactive ChatOps is the goal, Botkube is purpose-built
for it.

**kwatch** is admirably lightweight and dead-simple to run if all you need is
crash/restart notifications for pods. alertkube covers far more resource kinds and
adds a real suppression and routing layer, but if your needs are genuinely just
"tell me when a pod crashes," kwatch's smaller surface area is a virtue, not a
shortcoming.

!!! note "Fair comparison, not a takedown"
    The goal here is to help you choose, not to disparage. Each of these projects
    is a good fit for the problem it was built for. alertkube's pitch is narrow:
    broad Kubernetes-object coverage plus deterministic suppression plus
    multi-sink delivery, in a tiny footprint.

## Why alertkube is complementary to Alertmanager

The most important relationship on this page is the one with Alertmanager,
because it is *not* either/or. alertkube ships an Alertmanager-compatible
receiver:

```yaml
receiver:
  enabled: true
```

With the receiver on, `POST /api/v1/alerts` accepts Alertmanager webhook payloads
(version 4) and runs them through alertkube's *own* dedupe, grouping, routing, and
sink pipeline. Upstream fingerprints are preserved so dedupe stays aligned, and
resolves forget local state to avoid emitting duplicate synthetic resolves. You
point an Alertmanager `webhook_config` at it:

```yaml
url: http://alertkube.monitoring:9090/api/v1/alerts
```

The reason this matters: Alertmanager is excellent at *metrics-based* alerting but
knows nothing about Kubernetes object state, while alertkube watches object state
but evaluates no metrics. Run both, send Alertmanager's metric-derived alerts
*into* alertkube, and you get a single, consistent delivery and suppression layer —
the same fingerprint dedupe, the same silence/inhibition rules, the same eight
sinks — covering *both* your metric alerts and your Kubernetes object-state alerts.
That is the intended deployment for teams who already run Prometheus: keep
Alertmanager for what it's best at, and let alertkube unify object-state alerting
and multi-sink routing on top.

!!! tip "Decision shortcut"
    - Already have Prometheus and want metrics SLO alerting? Keep **Alertmanager**
      — and optionally feed it into alertkube for unified routing.
    - Want automated remediation and investigation playbooks? Look at **Robusta**.
    - Want to operate the cluster from chat? Look at **Botkube**.
    - Just want pod crash pings with minimal setup? **kwatch** is the lightest.
    - Want broad Kubernetes object-state coverage, deterministic suppression, and
      many sinks in one small binary? That's **alertkube** — and it plays nicely
      with all of the above.

## Where to go next

- [Why alertkube is deterministic](deterministic-design.md) — the design value
  that most distinguishes its suppression model.
- [The fingerprint and dedup model](fingerprint-and-dedup.md) — the identity key
  that unifies internally-watched and Alertmanager-ingested alerts.
- [Silence vs inhibition vs mute window](silence-vs-inhibition-vs-mute.md) — the
  suppression mechanisms applied to every alert, wherever it came from.
