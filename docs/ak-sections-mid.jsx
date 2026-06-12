// AlertKube — Watchers grid, Metrics band, Sinks table
const midFM = window.FramerMotion || {};

/* ----------------------------- WATCHERS ----------------------------- */
const AK_WATCHERS = [
  { n: "Deployment", icon: "target", file: "watchers/deployment.go", d: "Unavailable replicas and ProgressDeadlineExceeded, with desired / ready / updated counts.", tags: ["Unavailable", "ProgressDeadline"] },
  { n: "PVC", icon: "ring", file: "watchers/pvc.go", d: "ClaimLost is critical. ClaimPending fires as warning so storage flapping doesn't page on-call.", tags: ["Lost", "Pending"] },
  { n: "Job", icon: "check", file: "watchers/job.go", d: "Backoff-limit hit, with active / succeeded / failed counts and the condition message.", tags: ["JobFailed"] },
  { n: "DaemonSet", icon: "zap", file: "watchers/daemonset.go", d: "Unavailable pods on scheduled nodes — the quiet way node agents break.", tags: ["Unavailable"] },
  { n: "StatefulSet", icon: "layers", file: "watchers/statefulset.go", d: "Ready replicas below desired, generation-guarded to avoid stale fires.", tags: ["ReplicaShortfall"] },
  { n: "CronJob", icon: "calendar", file: "watchers/cronjob.go", d: "Missing success after a full schedule interval; suspend transitions land as info.", tags: ["MissingSuccess", "Suspended"] },
  { n: "HPA", icon: "chart", file: "watchers/hpa.go", d: "Pinned at maxReplicas while ScalingLimited — the capacity ceiling, before users feel it.", tags: ["MaxedOut"] },
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
            {/* Pod — flagship tile */}
            <div className="ak-wtile ak-wtile--feat">
              <div className="thead">
                <span className="ic-chip" style={{ background: "var(--wk-gradient)", color: "#fff" }}><Icon name="grid" size={16} /></span>
                <span className="file">internal/watchers/pod.go</span>
              </div>
              <h3>Pod</h3>
              <p>Restart counts, container waiting reasons, OOM, image pulls. Filters by namespace and pod-name prefix — literal or regex.</p>
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

            {/* Node — second flagship */}
            <div className="ak-wtile ak-wtile--feat">
              <div className="thead">
                <span className="ic-chip"><Icon name="shield" size={16} /></span>
                <span className="file">watchers/node.go</span>
              </div>
              <h3>Node</h3>
              <p>Transitions only — NodeReady flips, memory / disk / PID pressure, manual cordon. Inhibits its dependent pod alerts.</p>
              <div className="minis">
                {[
                  ["critical", "ip-10-0-3-41 · NotReady", "kubelet stopped posting"],
                  ["warning", "ip-10-0-1-12 · MemoryPressure", "evicting best-effort pods"],
                  ["info", "ip-10-0-2-08 · Cordoned", "by mia@ — maintenance"],
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
                and you're in the pipeline — dedupe, routing, and metrics included.
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
  { n: "Slack", pages: "all", tone: "info", transport: "Webhook or bot token", payload: "Block Kit — header, fields, summary, runbook", env: "SLACK_WEBHOOK_URL" },
  { n: "PagerDuty", pages: "critical only", tone: "critical", transport: "Events API v2", payload: "Trigger / resolve, dedupKey = fingerprint", env: "PAGERDUTY_ROUTING_KEY" },
  { n: "Microsoft Teams", pages: "all", tone: "info", transport: "Power Automate webhook", payload: "Adaptive Card with FactSet + runbook button", env: "TEAMS_WEBHOOK_URL" },
  { n: "Opsgenie", pages: "all", tone: "info", transport: "Alert API v2", payload: "Create / close, alias = fingerprint", env: "OPSGENIE_API_KEY" },
  { n: "Discord", pages: "all", tone: "info", transport: "Channel webhook", payload: "Embed with severity color + runbook", env: "DISCORD_WEBHOOK_URL" },
  { n: "Telegram", pages: "all", tone: "info", transport: "Bot API", payload: "HTML-escaped message", env: "TELEGRAM_BOT_TOKEN" },
  { n: "Generic webhook", pages: "all", tone: "info", transport: "HTTP POST", payload: "Raw alert as JSON", env: "GENERIC_WEBHOOK_URL" },
  { n: "stdout", pages: "all", tone: "info", transport: "klog", payload: "Single-line summary — local dev", env: "—" },
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

Object.assign(window, { AKWatchers, AKMetricsBand, AKSinks });
