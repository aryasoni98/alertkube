// AlertKube - Config, Install, Changelog, Final CTA, Footer
const botFM = window.FramerMotion || {};

/* ----------------------------- CONFIG ----------------------------- */
const AK_YAML = `cluster: prod-eu-west-1
metricsAddr: ":9090"

behavior:
  muteSeconds: 600
  resolveTTLSeconds: 600

persistence:
  enabled: true        # ConfigMap state snapshot

escalations:
  - match: {severity: critical}
    afterMinutes: 15
    sinks: [pagerduty]

routing:
  - match: {severity: critical}
    sinks: [slack, pagerduty]
  - match: {severity: warning}
    sinks: [slack]

inhibitions:
  - source: {kind: Node, reason: NodeNotReady}
    target: {kind: Pod}
    equal:  [node]
    duration: 10m`;

const AK_KEYS = [
  ["routing", "match labels → sinks, first hit wins"],
  ["inhibitions", "source → target with an equal-keys window"],
  ["silences", "matchers + an RFC3339 until"],
  ["filters", "comma or regex namespace + pod-name lists"],
  ["grouping", "storm folding by kind / ns / reason"],
  ["escalations", "re-dispatch alerts nobody resolved"],
  ["persistence", "state snapshot survives restarts"],
  ["receiver", "Alertmanager webhook intake"],
];

function AKConfig() {
  return (
    <section id="config" className="wk-section" data-screen-label="Config">
      <div className="wk-wrap">
        <div className="ak-config-grid">
          <Reveal>
            <div>
              <span className="wk-eyebrow" style={{ color: "var(--wk-blue-700)" }}>Config</span>
              <h2 className="wk-h2" style={{ margin: "14px 0 12px" }}>YAML first.<br />Env as fallback.</h2>
              <p className="wk-lede" style={{ fontSize: 17, maxWidth: 440 }}>
                One config.yaml drives routing, inhibitions, silences, filters,
                mute and resolve windows, and per-severity channels.
              </p>
              <ul className="ak-keys">
                {AK_KEYS.map(([k, v]) => (
                  <li key={k}><span className="k">{k}</span><span className="v">{v}</span></li>
                ))}
              </ul>
            </div>
          </Reveal>
          <Reveal delay={0.1}>
            <AKCodeCard file="config.yaml" lang="yaml" code={AK_YAML} />
          </Reveal>
        </div>
      </div>
    </section>
  );
}

/* ----------------------------- INSTALL ----------------------------- */
const AK_HELM = `helm upgrade --install alertkube \\
  oci://ghcr.io/aryasoni98/charts/alertkube --version ${AK_CHART_VERSION} \\
  --namespace monitoring --create-namespace \\
  --set cluster=prod-eu-west-1 \\
  --set slack.webhookUrl=$SLACK_WEBHOOK_URL \\
  --set pagerduty.routingKey=$PD_ROUTING_KEY`;

const AK_DOCKER = `docker pull ghcr.io/aryasoni98/alertkube:${AK_VERSION}
docker run --rm \\
  -e SLACK_WEBHOOK_URL=$SLACK_WEBHOOK_URL \\
  -e CLUSTER_NAME=local-dev \\
  -v $HOME/.kube/config:/root/.kube/config \\
  ghcr.io/aryasoni98/alertkube:${AK_VERSION}`;

function AKInstall() {
  return (
    <section id="install" className="wk-section" data-screen-label="Install">
      <div className="wk-wrap">
        <AKHead
          eyebrow="Install"
          title="Shipped in three commands"
          sub="Helm chart with an optional ServiceMonitor, or a single container against your kubeconfig. Distroless, cosign-signed."
        />
        <Stagger className="ak-install-grid">
          <AKCodeCard file="helm" code={AK_HELM} />
          <AKCodeCard file="docker" code={AK_DOCKER} />
        </Stagger>
      </div>
    </section>
  );
}

/* ----------------------------- CHANGELOG ----------------------------- */
const AK_RELEASES = [
  {
    v: AK_VERSION, date: AK_VERSION_DATE, tag: "Console", latest: true,
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
    items: ["Governance, issue/PR templates, DCO, and security insights", "Project-maturity work — no controller behavior change"],
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

function AKChangelog() {
  return (
    <section id="changelog" className="wk-section" data-screen-label="Changelog">
      <div className="wk-wrap">
        <AKHead
          eyebrow="Changelog"
          title="What shipped, when"
          sub="Semantic versioning, Keep a Changelog format. No surprise breaking changes."
        />
        <Stagger className="ak-timeline">
          {AK_RELEASES.map((r) => (
            <div key={r.v} className={r.latest ? "ak-release ak-release--latest" : "ak-release"}>
              <span className="ak-release__dot"></span>
              <div className="ak-release__head">
                <span className="ak-release__v">{r.v}</span>
                <span className="ak-release__date">{r.date}</span>
                <span className="ak-tag" style={r.latest ? { color: "var(--wk-spark-text)", background: "var(--wk-spark-wash)", borderColor: "var(--wk-spark-wash-border)" } : {}}>{r.tag}</span>
              </div>
              <ul>
                {r.items.map((it) => (
                  <li key={it}><Icon name="check" size={14} />{it}</li>
                ))}
              </ul>
            </div>
          ))}
        </Stagger>
      </div>
    </section>
  );
}

/* ----------------------------- FINAL CTA ----------------------------- */
function AKFinalCTA() {
  return (
    <section className="wk-finalcta" data-screen-label="Final CTA">
      <div className="wk-finalcta__conic" aria-hidden="true"></div>
      <div className="wk-wrap wk-finalcta__inner">
        <Reveal>
          <h2 className="wk-finalcta__title">
            Page less.<br /><span className="wk-text-gradient">Know more.</span>
          </h2>
        </Reveal>
        <Reveal delay={0.08}>
          <p className="wk-finalcta__sub">
            Open source, Apache-2.0, one small binary in your cluster.
            Free forever - like good infrastructure should be.
          </p>
        </Reveal>
        <Reveal delay={0.16}>
          <div style={{ display: "flex", gap: 12, justifyContent: "center", flexWrap: "wrap" }}>
            <Button size="lg" variant="grad" href="#install" trailing={<Icon name="arrow" size={16} />}>Install with Helm</Button>
            <Button size="lg" variant="ghost" href="https://github.com/aryasoni98/alertkube" icon={<Icon name="github" size={16} />} trailing={null}>Star on GitHub</Button>
          </div>
        </Reveal>
      </div>
    </section>
  );
}

/* ----------------------------- FOOTER ----------------------------- */
function AKFooterSec() {
  return (
    <footer className="wk-footer" data-screen-label="Footer">
      <div className="wk-wrap">
        <div className="wk-footer__cols" style={{ paddingTop: 0 }}>
          <div className="wk-footer__col">
            <AKLogo size={26} />
            <p className="wk-small" style={{ marginTop: 14, maxWidth: 280 }}>
              Kubernetes multi-resource alerting. Severity-aware, multi-sink,
              quiet until it shouldn't be.
            </p>
            <div style={{ display: "flex", gap: 8, marginTop: 16 }}>
              <span className="ak-tag"><span className="ak-live" style={{ width: 6, height: 6 }}></span> {AK_VERSION}</span>
              <span className="ak-tag">Apache-2.0</span>
            </div>
          </div>
          <div className="wk-footer__col">
            <h4>Product</h4>
            <a href="#pipeline">Pipeline</a>
            <a href="#severity">Severity</a>
            <a href="#watchers">Watchers</a>
            <a href="#sinks">Sinks</a>
          </div>
          <div className="wk-footer__col">
            <h4>Get started</h4>
            <a href="#install">Install</a>
            <a href="#config">Configuration</a>
            <a href="#changelog">Changelog</a>
          </div>
          <div className="wk-footer__col">
            <h4>Project</h4>
            <a href="https://github.com/aryasoni98/alertkube" target="_blank" rel="noopener noreferrer">GitHub</a>
            <a href="https://github.com/aryasoni98/alertkube/blob/master/docs/grafana-dashboard.json" target="_blank" rel="noopener noreferrer">Grafana dashboard</a>
            <a href="https://github.com/aryasoni98/alertkube/blob/master/docs/OPERATIONS.md" target="_blank" rel="noopener noreferrer">Operations guide</a>
          </div>
        </div>
        <div className="wk-footer__bot">
          <span className="copy">© 2026 AlertKube · Apache-2.0 · Built by Arya Soni, on caffeine and kubectl</span>
          <div className="social">
            <a href="https://github.com/aryasoni98/alertkube" aria-label="GitHub" target="_blank" rel="noopener noreferrer"><Icon name="github" size={17} /></a>
          </div>
        </div>
      </div>
    </footer>
  );
}

Object.assign(window, { AKConfig, AKInstall, AKChangelog, AKFinalCTA, AKFooterSec });
