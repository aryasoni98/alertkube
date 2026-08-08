// AlertKube - Watchers grid, Metrics band, Sinks table
const midFM = window.FramerMotion || {};

/* ----------------------------- ARCHITECTURE ----------------------------- */
const AK_ARCH_RESOURCES = ["Pods", "Nodes", "Deployments", "PVCs", "Jobs", "CronJobs", "StatefulSets", "DaemonSets", "HPA"];
const AK_ARCH_CLOUD = ["AWS", "Azure", "GCP"];
const AK_ARCH_STEPS = [
  ["Watch", "shared informers stream resource state"],
  ["Collect", "events + describe payloads become alerts"],
  ["Route", "silence, inhibition, mute, severity override"],
  ["Group", "storm folding before notification"],
  ["Dispatch", "sink registry sends with retries"],
];
const AK_ARCH_SINKS = ["Slack", "Discord", "PagerDuty", "Opsgenie", "Teams", "Telegram", "Webhook", "stdout"];
const AK_ARCH_STATE = ["ConfigMap state snapshot", "ConfigMap config", "Secret sink credentials", "Prometheus metrics"];
const AK_ARCH_SECURITY = [
  { icon: "shield", text: "Least-privilege read-only RBAC for watches" },
  { icon: "shield", text: "Bearer token webhook auth with constant-time compare" },
  { icon: "shield", text: "Sink credentials stay in Kubernetes Secrets" },
  { icon: "chart", text: "Prometheus metrics expose controller health" },
];

function AKArchNode({ icon, title, sub, tone = "info", compact = false }) {
  return (
    <div className={`ak-arch-node ak-arch-node--${tone} ${compact ? "ak-arch-node--compact" : ""}`}>
      <span className="ak-arch-node__icon"><Icon name={icon} size={16} /></span>
      <span>
        <span className="ak-arch-node__title">{title}</span>
        {sub && <span className="ak-arch-node__sub">{sub}</span>}
      </span>
    </div>
  );
}

function AKArchitecture() {
  return (
    <section id="architecture" className="wk-section ak-arch-section" data-screen-label="Architecture">
      <div className="wk-wrap">
        <AKHead
          eyebrow="Architecture"
          title="Watch, route, dispatch - with state and security built in"
          sub="Kubernetes signals enter through read-only watches, pass through AlertKube's routing core, and leave through controlled sink dispatch."
        />
        <Reveal>
          <div className="ak-arch-layout">
            <div className="ak-arch-board" role="img" aria-label="Animated AlertKube architecture workflow from cluster and cloud sources to sinks and storage">
              <div className="ak-arch-glow ak-arch-glow--blue" aria-hidden="true"></div>
              <div className="ak-arch-glow ak-arch-glow--green" aria-hidden="true"></div>

              <div className="ak-arch-row ak-arch-row--sources">
                <div className="ak-arch-band-label">
                  <span>01</span>
                  Signal sources
                </div>
                <div className="ak-arch-entry">
                  <AKArchNode icon="target" title="Operator / kubectl" sub="read-only intent" compact />
                  <span className="ak-arch-arrow" aria-hidden="true"></span>
                  <AKArchNode icon="grid" title="Kubernetes API Server" sub="watch streams" tone="kube" />
                </div>
                <div className="ak-arch-resource-cloud">
                  {AK_ARCH_RESOURCES.map((r) => <span key={r} className="ak-arch-chip">{r}</span>)}
                </div>
                <div className="ak-arch-entry">
                  <AKArchNode icon="cloud" title="Cloud control planes" sub="optional polling · experimental" tone="info" />
                </div>
                <div className="ak-arch-resource-cloud">
                  {AK_ARCH_CLOUD.map((r) => <span key={r} className="ak-arch-chip">{r}</span>)}
                </div>
              </div>

              <div className="ak-arch-row ak-arch-row--controller">
                <div className="ak-arch-band-label">
                  <span>02</span>
                  Application layer
                </div>
                <div className="ak-arch-receiver">
                  <AKArchNode icon="bell" title="Alertmanager Receiver" sub="external webhook, bearer-authenticated" tone="critical" />
                  <AKArchNode icon="shield" title="authz.BearerEqual" sub="constant-time token compare" tone="security" compact />
                </div>
                <ol className="ak-arch-steps" aria-label="AlertKube controller pipeline">
                  {AK_ARCH_STEPS.map(([title, sub], i) => (
                    <li key={title} className="ak-arch-step" style={{ "--i": i }}>
                      <span className="ak-arch-step__num">{String(i + 1).padStart(2, "0")}</span>
                      <span>
                        <span className="ak-arch-step__title">{title}</span>
                        <span className="ak-arch-step__sub">{sub}</span>
                      </span>
                    </li>
                  ))}
                </ol>
                <div className="ak-arch-sweeper">
                  <Icon name="check" size={15} />
                  Sweeper resolves stale alerts
                </div>
              </div>

              <div className="ak-arch-row ak-arch-row--storage">
                <div className="ak-arch-band-label">
                  <span>03</span>
                  Sinks and storage
                </div>
                <div className="ak-arch-sinks">
                  {AK_ARCH_SINKS.map((sink, i) => (
                    <span key={sink} className="ak-arch-sink" style={{ "--i": i }}>{sink}</span>
                  ))}
                </div>
                <div className="ak-arch-state">
                  {AK_ARCH_STATE.map((s) => (
                    <span key={s} className="ak-arch-state__item">
                      <Icon name={s.includes("Secret") ? "shield" : s.includes("Prometheus") ? "chart" : "book"} size={14} />
                      {s}
                    </span>
                  ))}
                </div>
              </div>

              <span className="ak-arch-packet ak-arch-packet--one" aria-hidden="true"></span>
              <span className="ak-arch-packet ak-arch-packet--two" aria-hidden="true"></span>
              <span className="ak-arch-packet ak-arch-packet--three" aria-hidden="true"></span>
            </div>

            <div className="ak-arch-posture-grid" aria-label="Security posture">
              <p className="ak-arch-posture-grid__label">Security posture</p>
              {AK_ARCH_SECURITY.map((item) => (
                <div key={item.text} className="ak-arch-posture-card">
                  <span className="ak-arch-posture-card__icon"><Icon name={item.icon} size={16} /></span>
                  <span>{item.text}</span>
                </div>
              ))}
            </div>
          </div>
        </Reveal>
      </div>
    </section>
  );
}

/* ----------------------------- WATCHERS ----------------------------- */
const AK_WATCHERS = [
  { n: "Deployment", icon: "target", file: "watchers/deployment.go", d: "Unavailable replicas and ProgressDeadlineExceeded, with desired / ready / updated counts.", tags: ["Unavailable", "ProgressDeadline"] },
  { n: "PVC", icon: "ring", file: "watchers/pvc.go", d: "ClaimLost is critical. ClaimPending fires as warning so storage flapping doesn't page on-call.", tags: ["Lost", "Pending"] },
  { n: "Job", icon: "check", file: "watchers/job.go", d: "Backoff-limit hit, with active / succeeded / failed counts and the condition message.", tags: ["JobFailed"] },
  { n: "DaemonSet", icon: "zap", file: "watchers/daemonset.go", d: "Unavailable pods on scheduled nodes - the quiet way node agents break.", tags: ["Unavailable"] },
  { n: "StatefulSet", icon: "layers", file: "watchers/statefulset.go", d: "Ready replicas below desired, generation-guarded to avoid stale fires.", tags: ["ReplicaShortfall"] },
  { n: "CronJob", icon: "calendar", file: "watchers/cronjob.go", d: "Missing success after a full schedule interval; suspend transitions land as info.", tags: ["MissingSuccess", "Suspended"] },
  { n: "HPA", icon: "chart", file: "watchers/hpa.go", d: "Pinned at maxReplicas while ScalingLimited - the capacity ceiling, before users feel it.", tags: ["MaxedOut"] },
];

function AKWatchers() {
  return (
    <section id="watchers" className="wk-section" data-screen-label="Watchers">
      <div className="wk-wrap">
        <AKHead
          eyebrow="Watchers"
          title="Nine kinds. One alert shape."
          sub="Each watcher owns its reason taxonomy and emits the same canonical alert. Writing your own is about forty lines."
        />
        <Reveal>
          <div className="ak-wgrid">
            {/* Pod - flagship tile */}
            <div className="ak-wtile ak-wtile--feat">
              <div className="thead">
                <span className="ic-chip" style={{ background: "var(--wk-gradient)", color: "#fff" }}><Icon name="grid" size={16} /></span>
                <span className="file">internal/watchers/pod.go</span>
              </div>
              <h3>Pod</h3>
              <p>Restart counts, container waiting reasons, OOM, image pulls. Filters by namespace and pod-name prefix - literal or regex.</p>
              <div className="minis">
                <div className="ak-row" style={{ padding: "10px 12px" }}>
                  <AKDot tone="critical" />
                  <div className="ak-row__main">
                    <div className="ak-row__title">api-7d9fc · CrashLoopBackOff</div>
                    <div className="ak-row__meta">restarts 6 · backoff 5m</div>
                  </div>
                </div>
                <div className="ak-row" style={{ padding: "10px 12px" }}>
                  <AKDot tone="warning" />
                  <div className="ak-row__main">
                    <div className="ak-row__title">worker-2 · ImagePullBackOff</div>
                    <div className="ak-row__meta">registry denied · tag v2.4.1</div>
                  </div>
                </div>
              </div>
              <div className="tags">
                {["CrashLoopBackOff", "OOMKilled", "ImagePullBackOff", "Restart"].map((t) => <span key={t} className="ak-tag">{t}</span>)}
              </div>
            </div>

            {/* Node - second flagship */}
            <div className="ak-wtile ak-wtile--feat">
              <div className="thead">
                <span className="ic-chip"><Icon name="shield" size={16} /></span>
                <span className="file">watchers/node.go</span>
              </div>
              <h3>Node</h3>
              <p>Transitions only - NodeReady flips, memory / disk / PID pressure, manual cordon. Inhibits its dependent pod alerts.</p>
              <div className="minis">
                {[
                  ["critical", "ip-10-0-3-41 · NotReady", "kubelet stopped posting"],
                  ["warning", "ip-10-0-1-12 · MemoryPressure", "evicting best-effort pods"],
                  ["info", "ip-10-0-2-08 · Cordoned", "by mia@ - maintenance"],
                ].map(([tone, t, s]) => (
                  <div key={t} style={{ display: "flex", alignItems: "center", gap: 10 }}>
                    <AKDot tone={tone} size={7} />
                    <span style={{ fontSize: 13, fontWeight: 600 }}>{t}</span>
                    <span style={{ fontSize: 12, color: "var(--fg-muted)", marginLeft: "auto" }}>{s}</span>
                  </div>
                ))}
              </div>
              <div className="tags">
                {["NotReady", "MemoryPressure", "DiskPressure", "Cordon"].map((t) => <span key={t} className="ak-tag">{t}</span>)}
              </div>
            </div>

            {AK_WATCHERS.map((w) => (
              <div key={w.n} className="ak-wtile">
                <div className="thead">
                  <span className="ic-chip"><Icon name={w.icon} size={16} /></span>
                  <span className="file">{w.file}</span>
                </div>
                <h3>{w.n}</h3>
                <p>{w.d}</p>
                <div className="tags">
                  {w.tags.map((t) => <span key={t} className="ak-tag">{t}</span>)}
                </div>
              </div>
            ))}

            {/* Your watcher */}
            <div className="ak-wtile ak-wtile--wide ak-wtile--dash">
              <div className="thead">
                <span className="ic-chip" style={{ background: "var(--wk-spark-wash)", color: "var(--wk-spark-500)" }}><Icon name="plus" size={16} /></span>
                <span className="file">~40 LOC</span>
              </div>
              <h3>Your watcher</h3>
              <p>
                Implement <span className="wk-mono" style={{ fontSize: 12.5 }}>Name() / Setup(ctx, factory, emit)</span>, emit a canonical alert,
                and you're in the pipeline - dedupe, routing, and metrics included.
              </p>
            </div>
          </div>
        </Reveal>
      </div>
    </section>
  );
}

/* ----------------------------- METRICS BAND ----------------------------- */
function AKMetricsBand() {
  const stats = [
    { n: 9, label: "resource kinds watched" },
    { n: 8, label: "sinks, one interface" },
    { n: 3, label: "severity tiers" },
    { n: 8, label: "Prometheus metrics" },
  ];
  return (
    <section className="wk-section" data-screen-label="Metrics band" style={{ paddingLeft: 0, paddingRight: 0 }}>
      <div className="wk-metrics">
        <div className="wk-wrap">
          <Stagger className="wk-metrics__grid">
            {stats.map((s) => (
              <div key={s.label}>
                <div className="wk-metrics__num"><AKCount to={s.n} /></div>
                <div className="wk-metrics__label">{s.label}</div>
              </div>
            ))}
          </Stagger>
        </div>
      </div>
    </section>
  );
}

/* ----------------------------- SINKS ----------------------------- */
const AK_SINKS = [
  { n: "Slack", pages: "all", tone: "info", transport: "Webhook or bot token", payload: "Block Kit - header, fields, summary, runbook", env: "SLACK_WEBHOOK_URL" },
  { n: "PagerDuty", pages: "critical only", tone: "critical", transport: "Events API v2", payload: "Trigger / resolve, dedupKey = fingerprint", env: "PAGERDUTY_ROUTING_KEY" },
  { n: "Microsoft Teams", pages: "all", tone: "info", transport: "Power Automate webhook", payload: "Adaptive Card with FactSet + runbook button", env: "TEAMS_WEBHOOK_URL" },
  { n: "Opsgenie", pages: "all", tone: "info", transport: "Alert API v2", payload: "Create / close, alias = fingerprint", env: "OPSGENIE_API_KEY" },
  { n: "Discord", pages: "all", tone: "info", transport: "Channel webhook", payload: "Embed with severity color + runbook", env: "DISCORD_WEBHOOK_URL" },
  { n: "Telegram", pages: "all", tone: "info", transport: "Bot API", payload: "HTML-escaped message", env: "TELEGRAM_BOT_TOKEN" },
  { n: "Generic webhook", pages: "all", tone: "info", transport: "HTTP POST", payload: "Raw alert as JSON", env: "GENERIC_WEBHOOK_URL" },
  { n: "stdout", pages: "all", tone: "info", transport: "klog", payload: "Single-line summary - local dev", env: "-" },
];

function AKSinks() {
  return (
    <section id="sinks" className="wk-section" data-screen-label="Sinks">
      <div className="wk-wrap">
        <AKHead
          eyebrow="Sinks"
          title="Eight sinks. One interface."
          sub="Every sink implements Name / Send / Supports. Add a new one in about thirty lines; register it at boot."
        />
        <Reveal>
          <div className="ak-tablewrap">
            <table className="ak-table">
              <thead>
                <tr>
                  <th>Sink</th>
                  <th>Pages on</th>
                  <th>Transport</th>
                  <th>Payload</th>
                  <th>Env</th>
                </tr>
              </thead>
              <tbody>
                {AK_SINKS.map((s) => (
                  <tr key={s.n}>
                    <td>{s.n}</td>
                    <td>
                      <span className="ak-tag" style={s.tone === "critical" ? { color: "var(--ak-critical)", background: "var(--ak-critical-wash)", borderColor: "transparent" } : {}}>
                        <AKDot tone={s.tone} size={6} />
                        {s.pages}
                      </span>
                    </td>
                    <td>{s.transport}</td>
                    <td>{s.payload}</td>
                    <td className="env">{s.env}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Reveal>
      </div>
    </section>
  );
}

/* ----------------------------- CLOUD SOURCES ----------------------------- */
const AK_CLOUD = [
  {
    n: "AWS", count: 18, cred: "Standard AWS credential chain - IRSA in-cluster, or static keys via env.",
    services: ["EKS", "CloudWatch", "EC2", "ELBv2", "RDS", "Aurora", "DynamoDB", "ElastiCache", "S3", "CloudTrail", "ASG", "KMS", "EBS", "EFS", "NAT", "Route53", "ACM", "VPN"],
  },
  {
    n: "Azure", count: 6, cred: "DefaultAzureCredential - AKS Workload Identity in-cluster.",
    services: ["AKS", "Monitor", "VMs", "Storage", "SQL", "Redis"],
  },
  {
    n: "GCP", count: 4, cred: "Application Default Credentials - GKE Workload Identity in-cluster.",
    services: ["GKE", "Monitoring", "Compute", "Cloud SQL"],
  },
];

function AKCloudSources() {
  return (
    <section id="cloud" className="wk-section" data-screen-label="Cloud sources">
      <div className="wk-wrap">
        <AKHead
          eyebrow="Cloud sources"
          title="Twenty-eight cloud sources. Same pipeline."
          sub="Beyond the cluster, AlertKube polls AWS, Azure, and GCP control planes - every cloud signal flows through the same dedupe, route, group, dispatch path as the Kubernetes watchers. Each service is an independent, off-by-default toggle. Experimental."
        />
        <Reveal>
          <div className="ak-wgrid">
            {AK_CLOUD.map((c) => (
              <div key={c.n} className="ak-wtile">
                <div className="thead">
                  <span className="ic-chip"><Icon name="cloud" size={16} /></span>
                  <span className="file">{c.count} sources</span>
                </div>
                <h3>{c.n}</h3>
                <p>{c.cred}</p>
                <div className="tags">
                  {c.services.map((t) => <span key={t} className="ak-tag">{t}</span>)}
                </div>
              </div>
            ))}
          </div>
        </Reveal>
      </div>
    </section>
  );
}

Object.assign(window, { AKArchitecture, AKWatchers, AKMetricsBand, AKSinks, AKCloudSources });
