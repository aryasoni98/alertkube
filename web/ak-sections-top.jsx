// AlertKube - Nav, Hero, Marquee, Pipeline, Severity
const topFM = window.FramerMotion || {};
const mT = topFM.motion;

/* ----------------------------- NAV ----------------------------- */
function AKNav({ theme, setTheme }) {
  // In-page section anchors. "Docs" is a real destination (the MkDocs manual
  // deployed at /alertkube/manual/), kept separate from the anchors below.
  const links = [
    { label: "Pipeline", href: "#pipeline" },
    { label: "Architecture", href: "#architecture" },
    { label: "Severity", href: "#severity" },
    { label: "Watchers", href: "#watchers" },
    { label: "Cloud", href: "#cloud" },
    { label: "Sinks", href: "#sinks" },
    { label: "Install", href: "#install" },
    { label: "Changelog", href: "#changelog" },
  ];
  // Relative so it tracks the canonical base (https://aryasoni98.github.io/alertkube/);
  // resolves to /alertkube/manual/ on the deployed site.
  const docsHref = "manual/";

  const [drawerOpen, setDrawerOpen] = React.useState(false);
  const drawerRef = React.useRef(null);
  const hamburgerRef = React.useRef(null);
  const drawerId = "ak-nav-drawer";

  // Close on Escape
  React.useEffect(() => {
    if (!drawerOpen) return;
    const onKey = (e) => {
      if (e.key === "Escape") {
        setDrawerOpen(false);
        hamburgerRef.current && hamburgerRef.current.focus();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [drawerOpen]);

  // Trap focus inside open drawer and move focus in
  React.useEffect(() => {
    if (!drawerOpen || !drawerRef.current) return;
    const firstFocusable = drawerRef.current.querySelector("a, button");
    firstFocusable && firstFocusable.focus();
  }, [drawerOpen]);

  // Prevent body scroll while drawer open
  React.useEffect(() => {
    document.body.style.overflow = drawerOpen ? "hidden" : "";
    return () => { document.body.style.overflow = ""; };
  }, [drawerOpen]);

  const closeDrawer = () => {
    setDrawerOpen(false);
    hamburgerRef.current && hamburgerRef.current.focus();
  };

  return (
    <nav className="wk-nav">
      <div className="wk-wrap wk-nav__inner">
        <a href="#top" aria-label="AlertKube home"><AKLogo /></a>
        <div className="wk-nav__center">
          {links.map((l) => (
            <a key={l.label} className="wk-nav__link" href={l.href}>{l.label}</a>
          ))}
          <a className="wk-nav__link" href={docsHref}>Docs</a>
        </div>
        <div className="wk-nav__right">
          <a
            className="ak-ver"
            href={AK_RELEASE_URL}
            target="_blank"
            rel="noopener noreferrer"
            title={`AlertKube ${AK_VERSION} - released ${AK_VERSION_DATE}. View release notes`}
            aria-label={`Current version ${AK_VERSION}, view release notes on GitHub`}
          >
            <span className="ak-ver__dot" aria-hidden="true"></span>{AK_VERSION}
          </a>
          <button
            className="ak-iconbtn"
            aria-label="Toggle theme"
            onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
          >
            <Icon name={theme === "dark" ? "sun" : "moon"} size={17} />
          </button>
          <a className="ak-iconbtn" href="https://github.com/aryasoni98/alertkube" aria-label="GitHub" target="_blank" rel="noopener noreferrer"><Icon name="github" size={17} /></a>
          <span className="wk-nav__divider"></span>
          <Button size="sm" variant="grad" href="#install">Install</Button>
          {/* Hamburger - visible only below 880px via CSS */}
          <button
            ref={hamburgerRef}
            className="ak-hamburger"
            aria-label={drawerOpen ? "Close navigation menu" : "Open navigation menu"}
            aria-expanded={drawerOpen}
            aria-controls={drawerId}
            onClick={() => setDrawerOpen((o) => !o)}
          >
            <span className={`ak-hamburger__icon ${drawerOpen ? "ak-hamburger__icon--open" : ""}`} aria-hidden="true">
              <span></span><span></span><span></span>
            </span>
          </button>
        </div>
      </div>

      {/* Backdrop */}
      <div
        className={`ak-drawer-backdrop ${drawerOpen ? "ak-drawer-backdrop--visible" : ""}`}
        aria-hidden="true"
        onClick={closeDrawer}
      />

      {/* Drawer */}
      <div
        id={drawerId}
        ref={drawerRef}
        className={`ak-drawer ${drawerOpen ? "ak-drawer--open" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-label="Navigation menu"
      >
        <div className="ak-drawer__header">
          <AKLogo />
          <button
            className="ak-iconbtn"
            aria-label="Close navigation menu"
            onClick={closeDrawer}
          >
            {/* X icon inline */}
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" aria-hidden="true">
              <path d="M18 6L6 18M6 6l12 12"/>
            </svg>
          </button>
        </div>
        <nav aria-label="Site navigation" className="ak-drawer__nav">
          {links.map((l) => (
            <a
              key={l.label}
              className="ak-drawer__link"
              href={l.href}
              onClick={closeDrawer}
            >
              {l.label}
            </a>
          ))}
          <a
            className="ak-drawer__link"
            href={docsHref}
            onClick={closeDrawer}
          >
            <Icon name="book" size={16} />
            Docs
          </a>
          <a
            className="ak-drawer__link"
            href="https://github.com/aryasoni98/alertkube"
            target="_blank"
            rel="noopener noreferrer"
            onClick={closeDrawer}
          >
            <Icon name="github" size={16} />
            GitHub
          </a>
        </nav>
        <div className="ak-drawer__footer">
          <Button size="md" variant="grad" href="#install" onClick={closeDrawer} trailing={<Icon name="arrow" size={15} />}>
            Install with Helm
          </Button>
        </div>
      </div>
    </nav>
  );
}

/* ----------------------------- HERO ----------------------------- */
const AK_FEED = [
  { tone: "critical", title: "payments/api-7d9fc · CrashLoopBackOff", meta: "fp a3f29c41b07e · restarts 6", sinks: ["slack", "pagerduty"] },
  { tone: "warning", title: "ingest/worker-2 · ImagePullBackOff", meta: "fp 9c01d4e2aa18 · pull denied", sinks: ["slack"] },
  { tone: "critical", title: "node ip-10-0-3-41 · NodeNotReady", meta: "fp 77b2e09c43dd · inhibits 12 pod alerts", sinks: ["slack", "pagerduty"] },
  { tone: "warning", title: "search/indexer · DeploymentUnavailable", meta: "fp e4f81b29cc07 · ready 2/4", sinks: ["slack"] },
  { tone: "resolved", title: "payments/api-7d9fc · Resolved", meta: "fp a3f29c41b07e · TTL sweep", sinks: ["slack"] },
  { tone: "critical", title: "ml/train-nightly · JobFailed", meta: "fp 5d20cf83b1ea · backoff limit", sinks: ["slack", "pagerduty"] },
  { tone: "warning", title: "cache/redis-data-0 · PVCPending", meta: "fp 1bb74a90de52 · no volume bound", sinks: ["slack"] },
];

function AKFeedRow({ a }) {
  return (
    <div className={a.tone === "resolved" ? "ak-row ak-row--resolved" : "ak-row"}>
      <AKDot tone={a.tone} />
      <div className="ak-row__main">
        <div className="ak-row__title">{a.title}</div>
        <div className="ak-row__meta">{a.meta}</div>
      </div>
      <div className="ak-row__sinks">
        {a.sinks.map((s) => <span key={s} className="ak-tag">{s}</span>)}
      </div>
    </div>
  );
}

function AKHero({ live }) {
  const reduce = topFM.useReducedMotion();
  const idRef = React.useRef(4);
  const [items, setItems] = React.useState(() => AK_FEED.slice(0, 4).map((a, i) => ({ ...a, id: i })));

  React.useEffect(() => {
    if (!live || reduce) return;
    const t = setInterval(() => {
      setItems((prev) => {
        const next = { ...AK_FEED[idRef.current % AK_FEED.length], id: idRef.current };
        idRef.current += 1;
        return [next, ...prev].slice(0, 4);
      });
    }, 3400);
    return () => clearInterval(t);
  }, [live, reduce]);

  return (
    <header id="top" className="wk-hero" data-screen-label="Hero">
      <div className="wk-wrap">
        <div className="wk-hero__grid">
          <div>
            <Reveal>
              <span className="wk-hero__pill">
                <span className="shimmer"></span>
                <span className="ak-live" style={{ width: 7, height: 7 }}></span>
                {AK_VERSION} - Horizontal sharding, durable delivery, dead-letter observability
              </span>
            </Reveal>
            <Reveal delay={0.06}>
              <h1 className="wk-hero__title">
                Alerts that know<br />
                what <span className="wk-text-gradient wk-text-gradient--animate">matters.</span>
              </h1>
            </Reveal>
            <Reveal delay={0.12}>
              <p className="wk-hero__sub">
                AlertKube watches nine Kubernetes resource kinds, dedupes the
                storms, and gives responders a browser console for live alerts,
                config review, runtime silences, and channel tests.
              </p>
            </Reveal>
            <Reveal delay={0.18}>
              <div className="wk-hero__ctas">
                <Button size="lg" variant="grad" href="#install" trailing={<Icon name="arrow" size={16} />}>
                  Install with Helm
                </Button>
                <Button size="lg" variant="ghost" href="#pipeline">See the pipeline</Button>
              </div>
            </Reveal>
            <Reveal delay={0.26}>
              <div className="wk-hero__trust">
                <div className="wk-hero__trust-label">In the box</div>
                <div className="wk-hero__logos">
                  <span><AKCount to={9} /> watchers</span>
                  <span><AKCount to={8} /> sinks</span>
                  <span><AKCount to={6} />-stage pipeline</span>
                  <span>web console</span>
                  <span>1 binary</span>
                </div>
              </div>
            </Reveal>
          </div>

          <div className="wk-hero__visual">
            <div className="wk-hero__orbs" aria-hidden="true">
              <span className="wk-hero__orb" style={{ background: "var(--wk-blue-300)", top: "-6%", left: "-8%" }}></span>
              <span className="wk-hero__orb" style={{ background: "var(--wk-green-300)", bottom: "-10%", right: "-12%" }}></span>
              <span className="wk-hero__orb" style={{ background: "var(--wk-spark-300)", width: 160, height: 160, top: "42%", left: "46%", opacity: 0.35 }}></span>
            </div>

            <div className="wk-hero__frame">
              <div className="wk-hero__frame-bar">
                <span className="dots"><span></span><span></span><span></span></span>
                <span className="url">alertkube.monitoring.svc:9090/feed</span>
              </div>
              <div className="ak-feed" style={{ height: "calc(100% - 43px)" }}>
                <div className="ak-feed__head">
                  <span className="ak-live"></span>
                  Active alerts
                  <span className="cluster">prod-eu-west-1</span>
                </div>
                <div className="ak-feed__list">
                  {reduce || !mT ? (
                    items.map((a) => <AKFeedRow key={a.id} a={a} />)
                  ) : (
                    <topFM.AnimatePresence mode="popLayout" initial={false}>
                      {items.map((a) => (
                        <mT.div
                          key={a.id}
                          layout
                          initial={{ opacity: 0, y: -18, scale: 0.97 }}
                          animate={{ opacity: 1, y: 0, scale: 1 }}
                          exit={{ opacity: 0, y: 14 }}
                          transition={{ duration: 0.5, ease: [0.22, 1, 0.36, 1] }}
                        >
                          <AKFeedRow a={a} />
                        </mT.div>
                      ))}
                    </topFM.AnimatePresence>
                  )}
                </div>
              </div>
            </div>

            {mT && (
              <mT.div
                className="wk-hero__notif"
                style={{ top: 56, right: -18 }}
                animate={reduce ? {} : { y: [0, -9, 0] }}
                transition={{ duration: 5.5, repeat: Infinity, ease: "easeInOut" }}
              >
                <span className="av" style={{ background: "var(--wk-gradient)", display: "grid", placeItems: "center", color: "#fff" }}>
                  <Icon name="msg" size={15} />
                </span>
                <span>
                  <span className="t" style={{ display: "block" }}>#alerts-critical</span>
                  <span className="s">CrashLoopBackOff · payments/api</span>
                </span>
              </mT.div>
            )}
            {mT && (
              <mT.div
                className="wk-hero__notif"
                style={{ bottom: 34, left: -26 }}
                animate={reduce ? {} : { y: [0, 8, 0] }}
                transition={{ duration: 6.5, repeat: Infinity, ease: "easeInOut", delay: 1.2 }}
              >
                <span className="av" style={{ background: "var(--wk-spark-wash)", display: "grid", placeItems: "center", color: "var(--wk-spark-500)" }}>
                  <Icon name="bell" size={15} />
                </span>
                <span>
                  <span className="t" style={{ display: "block" }}>PagerDuty - paged Mia</span>
                  <span className="s">critical only · dedupKey a3f29c41b07e</span>
                </span>
              </mT.div>
            )}
          </div>
        </div>
      </div>
    </header>
  );
}

/* ----------------------------- MARQUEE ----------------------------- */
function AKMarquee() {
  const names = ["Slack", "PagerDuty", "Microsoft Teams", "Opsgenie", "Discord", "Telegram", "Webhooks", "Prometheus", "Grafana", "Alertmanager"];
  const reel = [...names, ...names];
  return (
    <section className="wk-marquee" data-screen-label="Marquee" aria-label="Integrations">
      <div className="wk-marquee__reel">
        {reel.map((n, i) => <span key={i} className="wk-marquee__logo">{n}</span>)}
      </div>
    </section>
  );
}

/* ----------------------------- PIPELINE ----------------------------- */
const AK_STAGES = [
  { t: "Fingerprint", d: "sha256(kind | ns | name | reason), truncated to 12 chars. Same fingerprint, same alert - dedupe for free." },
  { t: "Mute", d: "muteSeconds blocks re-firing of the same fingerprint. The last-sent table self-cleans after an hour." },
  { t: "Silence", d: "Matchers plus an RFC3339 until. The alert-silence-until annotation works per-resource." },
  { t: "Inhibit", d: "A source alert arms a window; matching targets are dropped while it holds. NodeNotReady mutes its pods." },
  { t: "Route", d: "First matching rule wins. No match falls through to the default sinks." },
  { t: "Resolve", d: "A background sweeper expires alerts past their TTL and emits a synthetic resolved event downstream." },
];

function AKStageVisual({ i }) {
  if (i === 0) return (
    <div className="pv">
      <div className="pv-card"><span className="pv-mono">kind=Pod · ns=payments · name=api-7d9fc<br />reason=CrashLoopBackOff</span></div>
      <span className="pv-arrow"><Icon name="chev" size={18} /></span>
      <span className="pv-hash">a3f29c41b07e</span>
      <span className="pv-note">one identity per failure, not per event</span>
    </div>
  );
  if (i === 1) return (
    <div className="pv">
      <div className="pv-card"><AKDot tone="critical" /><span><span className="pv-name">a3f29c41b07e</span><span className="pv-sub">sent 09:14:02</span></span><span className="ak-tag" style={{ marginLeft: "auto", color: "var(--wk-green-700)" }}><Icon name="check" size={11} /> delivered</span></div>
      <div className="pv-card pv-card--off"><AKDot tone="critical" /><span><span className="pv-name">a3f29c41b07e</span><span className="pv-sub">re-fired 09:15:40</span></span><span className="ak-tag" style={{ marginLeft: "auto" }}>muted 600s</span></div>
      <div className="pv-card pv-card--off"><AKDot tone="critical" /><span><span className="pv-name">a3f29c41b07e</span><span className="pv-sub">re-fired 09:18:11</span></span><span className="ak-tag" style={{ marginLeft: "auto" }}>muted 600s</span></div>
    </div>
  );
  if (i === 2) return (
    <div className="pv">
      <div className="pv-card">
        <span style={{ color: "var(--fg-muted)", display: "inline-flex" }}><Icon name="moon" size={18} /></span>
        <span><span className="pv-name">alert-silence-until</span><span className="pv-sub">2026-06-13T08:00:00Z · ns=staging</span></span>
      </div>
      <span className="pv-arrow"><Icon name="chev" size={18} /></span>
      <div className="pv-card pv-card--off"><AKDot tone="warning" /><span><span className="pv-name">staging/canary · ContainerRestart</span><span className="pv-sub">matched silence · dropped</span></span></div>
      <span className="pv-note">time-bounded, then it lets go on its own</span>
    </div>
  );
  if (i === 3) return (
    <div className="pv">
      <div className="pv-card"><AKDot tone="critical" /><span><span className="pv-name">Node · NodeNotReady</span><span className="pv-sub">source · arms 10m window · equal: [node]</span></span></div>
      <span className="pv-arrow"><Icon name="chev" size={18} /></span>
      <div className="pv-card pv-card--off"><AKDot tone="critical" /><span className="pv-name">payments/api-7d9fc · CrashLoopBackOff</span></div>
      <div className="pv-card pv-card--off"><AKDot tone="critical" /><span className="pv-name">search/indexer-1 · CrashLoopBackOff</span></div>
      <span className="pv-note">12 pod alerts inhibited - one page, not thirteen</span>
    </div>
  );
  if (i === 4) return (
    <div className="pv">
      <div className="pv-rule pv-rule--hit">severity: critical → [slack, pagerduty]<span style={{ marginLeft: "auto", color: "var(--wk-green-500)", display: "inline-flex" }}><Icon name="check" size={15} /></span></div>
      <div className="pv-rule">severity: warning → [slack]</div>
      <div className="pv-rule">default → [stdout]</div>
      <span className="pv-note">first match wins; default catches the rest</span>
    </div>
  );
  return (
    <div className="pv">
      <div className="pv-card"><AKDot tone="critical" /><span><span className="pv-name">payments/api-7d9fc · CrashLoopBackOff</span><span className="pv-sub">no re-fire for 600s</span></span></div>
      <span className="pv-arrow"><Icon name="chev" size={18} /></span>
      <div className="pv-card"><AKDot tone="resolved" /><span><span className="pv-name">payments/api-7d9fc · Resolved</span><span className="pv-sub">synthetic event · PagerDuty incident closed</span></span><span className="ak-tag" style={{ marginLeft: "auto", color: "var(--wk-green-700)" }}>resolved</span></div>
    </div>
  );
}

function AKPipeline() {
  const reduce = topFM.useReducedMotion();
  const [active, setActive] = React.useState(0);
  const [auto, setAuto] = React.useState(true);
  React.useEffect(() => {
    if (!auto || reduce) return;
    const t = setInterval(() => setActive((a) => (a + 1) % AK_STAGES.length), 5000);
    return () => clearInterval(t);
  }, [auto, reduce]);

  return (
    <section id="pipeline" className="ak-inset" data-screen-label="Pipeline">
      <div className="wk-wrap">
        <AKHead
          eyebrow="The pipeline"
          title="From raw event to a page worth taking"
          sub="Every alert flows through the same six stages. No magic - a small, inspectable Go pipeline with metrics at every hop."
        />
        <Reveal>
          <div className="ak-stages">
            <div className="ak-stagelist" role="tablist" aria-label="Pipeline stages">
              {AK_STAGES.map((s, i) => (
                <button
                  key={s.t}
                  role="tab"
                  aria-selected={i === active}
                  className={i === active ? "ak-stage active" : "ak-stage"}
                  onClick={() => { setActive(i); setAuto(false); }}
                >
                  <span className="ak-stage__num">{String(i + 1).padStart(2, "0")}</span>
                  <span>
                    <span className="ak-stage__t" style={{ display: "block" }}>{s.t}</span>
                    <span className="ak-stage__d" style={{ display: "block" }}>{s.d}</span>
                  </span>
                  {i === active && auto && <span key={active} className="ak-stage__bar run"></span>}
                </button>
              ))}
            </div>
            <div className="ak-stagepanel">
              {mT ? (
                <topFM.AnimatePresence mode="wait">
                  <mT.div
                    key={active}
                    initial={reduce ? false : { opacity: 0, y: 16 }}
                    animate={{ opacity: 1, y: 0 }}
                    exit={reduce ? {} : { opacity: 0, y: -10 }}
                    transition={{ duration: 0.45, ease: [0.22, 1, 0.36, 1] }}
                    style={{ width: "100%", display: "flex", justifyContent: "center" }}
                  >
                    <AKStageVisual i={active} />
                  </mT.div>
                </topFM.AnimatePresence>
              ) : (
                <AKStageVisual i={active} />
              )}
            </div>
          </div>
        </Reveal>
      </div>
    </section>
  );
}

/* ----------------------------- SEVERITY ----------------------------- */
const AK_SEV = [
  {
    name: "Critical", tone: "critical", hex: "#E11D48",
    color: "var(--ak-critical)", tagline: "Pages on-call. Reserved for things that are on fire right now.",
    route: "→ slack · pagerduty · escalate 15m",
    items: ["CrashLoopBackOff", "OOMKilled", "NodeNotReady", "PVC lost", "Job failed", "ProgressDeadlineExceeded"],
  },
  {
    name: "Warning", tone: "warning", hex: "#F59E0B",
    color: "var(--ak-warning)", tagline: "Goes to the channel, not the phone. Worth a look before standup.",
    route: "→ slack #alerts",
    items: ["ContainerRestart", "ImagePullBackOff", "Deployment unavailable", "PVC pending", "Node cordon"],
  },
  {
    name: "Info", tone: "info", hex: "#1E64E6",
    color: "var(--ak-info)", tagline: "For the record. Resolutions and state changes nobody should chase.",
    route: "→ slack · stdout",
    items: ["Resolved events", "CronJob suspended", "Operator-defined notices"],
  },
];

function AKSeverity() {
  return (
    <section id="severity" className="wk-section" data-screen-label="Severity">
      <div className="wk-wrap">
        <AKHead
          eyebrow="Severity model"
          title="Three tiers. One source of truth."
          sub="Severity drives color, channel, and which sinks accept the page. PagerDuty only rings on critical - Slack takes all three."
        />
        <Stagger className="ak-sev-grid">
          {AK_SEV.map((s) => (
            <div key={s.name} className={`ak-sev ak-sev--${s.tone}`}>
              <div className="ak-sev__head">
                <AKDot tone={s.tone} size={10} />
                <span className="ak-sev__name" style={{ color: s.color }}>{s.name}</span>
                <span className="ak-sev__hex">{s.hex}</span>
              </div>
              <div className="ak-sev__tagline">{s.tagline}</div>
              <ul className="ak-sev__list">
                {s.items.map((it) => (
                  <li key={it}><AKDot tone={s.tone} size={6} />{it}</li>
                ))}
              </ul>
              <div className="ak-sev__route">{s.route}</div>
            </div>
          ))}
        </Stagger>
      </div>
    </section>
  );
}

Object.assign(window, { AKNav, AKHero, AKMarquee, AKPipeline, AKSeverity });
