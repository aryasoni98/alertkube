// AlertKube - shared release history (used by landing changelog + changelog.html)
// Depends on AK_VERSION / AK_VERSION_DATE from ak-lib.jsx (loaded first).
const AK_RELEASES = [
  {
    v: AK_VERSION, date: AK_VERSION_DATE, tag: "Reliability & API v1", latest: true,
    items: [
      "Per-shard Lease and state ConfigMap so every shard leads and keeps its own mute/outbox history",
      "Native HTTP API under /api/v1 with 308 redirects; Alertmanager receiver at /api/v1/receiver/alerts",
      "Per-fingerprint dispatch serialization, foreign-outbox replay gate, and slow-sink circuit breaker",
      "Opt-in OpenTelemetry delivery tracing; typed Silence CRD under api/v1alpha1; importable Go module path",
    ],
  },
  {
    v: "v1.2.0", date: "2026-07-03", tag: "Scale & durability",
    items: [
      "Horizontal hash sharding spreads watch/evaluate load across replicas; durable outbox replays undelivered alerts after restart",
      "Dead-letter observability for permanently-abandoned deliveries; bounded resolve-retry for PagerDuty/Opsgenie",
      "Async dispatch worker pool decouples sink delivery from the informer thread",
    ],
  },
  {
    v: "v1.1.0", date: "2026-06-27", tag: "Hardening",
    items: ["Opt-in Silence CRD (client-go dynamic informer), recurring maintenance windows, per-sink circuit breaker", "Google Chat and Mattermost sinks; console live updates (SSE), sortable/expandable alerts, keyboard nav, light theme", "validate/version CLI, tunable client QPS/burst, expanded e2e, coverage gate ratcheted to 66%"],
  },
  {
    v: "v1.0.0", date: "2026-06-24", tag: "Console",
    items: ["Embedded web console for alerts, config review, runtime silences, and channel tests", "Security-gated write paths with token or Kubernetes RBAC auth"],
  },
  {
    v: "v0.2.4", date: "2026-06-19", tag: "Watchers",
    items: ["Alert on non-OOM SIGKILL (ContainerKilled) with termination cause", "Clearer pod termination reporting"],
  },
  {
    v: "v0.2.3", date: "2026-06-18", tag: "Hardening",
    items: ["Hardened controller shutdown, filtering, receiver, and delete handling", "Shared severity-tier mapping across sinks; dead code removed", "Landing + docs site SEO, performance, and a11y upgrade"],
  },
  {
    v: "v0.2.2", date: "2026-06-15", tag: "CNCF readiness",
    items: ["Governance, issue/PR templates, DCO, and security insights", "Project-maturity work - no controller behavior change"],
  },
  {
    v: "v0.2.1", date: "2026-06-12", tag: "Launch",
    items: ["Operations, troubleshooting, and migration docs", "Landing page and README aligned to v0.2.1", "Watcher and sink code cleanup"],
  },
  {
    v: "v0.2.0", date: "2026-06-12", tag: "Security hardening",
    items: ["Four new watchers: DaemonSet, StatefulSet, CronJob, HPA", "Opsgenie, Discord, Telegram sinks", "Alert grouping, escalations, state persistence", "Alertmanager receiver + GET /api/alerts", "Grafana dashboard + cosign-signed releases"],
  },
  {
    v: "v0.1.0", date: "2026-06-10", tag: "Production readiness",
    items: ["Sink retries, mute rollback, resolve delivery fixes", "Leader election, HA, namespace-scoped RBAC", "Log redaction, credential Secrets, distroless image", "Config validation, test coverage, CI hardening"],
  },
  {
    v: "v0.0.1", date: "2026-06-09", tag: "Initial release",
    items: ["Watchers: Pod, Node, Deployment, PVC, Job", "Severity model with distinct colors per tier", "Slack, PagerDuty, Teams, webhook, stdout sinks", "YAML-first config: routing, inhibitions, silences", "Fingerprint dedupe + Prometheus metrics", "Helm chart with optional ServiceMonitor"],
  },
];

Object.assign(window, { AK_RELEASES });
