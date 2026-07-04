// AlertKube - shared primitives (composes Wooak DS bundle exports)
const akFM = window.FramerMotion || {};

/* --------------------------- VERSION ---------------------------- *
 * Single source of truth for the released version shown across the
 * landing page (nav badge, hero pill, install snippets, footer tag).
 * AK_VERSION is bumped by release-please and `just sync-version` (see
 * scripts/sync-version.sh). Update AK_VERSION_DATE when cutting a release. */
const AK_VERSION = "v1.2.0"; // x-release-please-version
const AK_VERSION_DATE = "2026-07-03";
// Helm chart version carries no leading "v" (semver per Chart.yaml).
const AK_CHART_VERSION = AK_VERSION.replace(/^v/, "");
const AK_REPO = "https://github.com/aryasoni98/alertkube";
const AK_RELEASE_URL = `${AK_REPO}/releases/tag/${AK_VERSION}`;

/* ----------------------------- LOGO ----------------------------- */
function AKLogo({ size = 28, withWord = true, white = false }) {
  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: 9 }}>
      <img
        src="assets/logo.png"
        alt=""
        aria-hidden="true"
        style={{
          width: size,
          height: size,
          borderRadius: size * 0.32,
          objectFit: "cover",
          boxShadow: "0 6px 14px -6px rgba(30,100,230,0.5)",
        }}
      />
      {withWord && (
        <span style={{ fontWeight: 700, fontSize: size * 0.64, letterSpacing: "-0.02em", color: white ? "#fff" : "var(--fg)" }}>
          alertkube
        </span>
      )}
    </span>
  );
}

/* ----------------------------- COUNT-UP ----------------------------- */
function AKCount({ to, suffix = "" }) {
  const ref = React.useRef(null);
  const inView = akFM.useInView(ref, { once: true, margin: "-15% 0px -15% 0px" });
  const ioBroken = useIOFallback(ref);
  const reduce = akFM.useReducedMotion();
  const [v, setV] = React.useState(reduce ? to : 0);
  React.useEffect(() => {
    if (ioBroken) { setV(to); return; }
    if (!inView || reduce) return;
    const ctrl = akFM.animate(0, to, {
      duration: 1.2,
      ease: [0.22, 1, 0.36, 1],
      onUpdate: (x) => setV(Math.round(x)),
    });
    return () => ctrl.stop();
  }, [inView, reduce, to, ioBroken]);
  return <span ref={ref}>{v}{suffix}</span>;
}

/* ----------------------------- SEVERITY DOT ----------------------------- */
function AKDot({ tone, size = 9, pulse = false }) {
  const colors = {
    critical: "var(--ak-critical)",
    warning: "var(--ak-warning)",
    info: "var(--ak-info)",
    resolved: "var(--ak-resolved)",
  };
  return (
    <span
      className={pulse ? "ak-dot ak-live" : "ak-dot"}
      style={{ width: size, height: size, background: pulse ? undefined : colors[tone] || colors.info }}
    ></span>
  );
}

/* ----------------------------- CODE CARD ----------------------------- */
function akEsc(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}
function akYamlHl(src) {
  return src
    .split("\n")
    .map((line) => {
      if (line.trim().startsWith("#")) return `<span class="y-c">${akEsc(line)}</span>`;
      const m = line.match(/^(\s*-?\s*)([A-Za-z_][\w.]*):(.*)$/);
      if (m) return `${akEsc(m[1])}<span class="y-k">${akEsc(m[2])}</span>:<span class="y-v">${akEsc(m[3])}</span>`;
      return akEsc(line);
    })
    .join("\n");
}
function akShHl(src) {
  return akEsc(src)
    .replace(/\$[A-Z_]+/g, (s) => `<span class="sh-v">${s}</span>`)
    .replace(/^(helm|docker|kubectl)\b/gm, (s) => `<span class="sh-cmd">${s}</span>`)
    .replace(/^#.*$/gm, (s) => `<span class="sh-c">${s}</span>`);
}
function AKCodeCard({ file, code, lang = "sh", style }) {
  const [copied, setCopied] = React.useState(false);
  const html = lang === "yaml" ? akYamlHl(code) : akShHl(code);
  const copy = () => {
    if (navigator.clipboard) navigator.clipboard.writeText(code);
    setCopied(true);
    setTimeout(() => setCopied(false), 1600);
  };
  return (
    <div className="ak-code" style={style}>
      <div className="ak-code__bar">
        <span className="ak-code__file">{file}</span>
        <button className="ak-code__copy" onClick={copy}>{copied ? "Copied" : "Copy"}</button>
      </div>
      <pre><code dangerouslySetInnerHTML={{ __html: html }}></code></pre>
    </div>
  );
}

/* ----------------------------- SECTION HEAD ----------------------------- */
function AKHead({ eyebrow, title, sub }) {
  return (
    <Reveal>
      <div className="wk-shead">
        <span className="wk-shead__eyebrow">{eyebrow}</span>
        <h2 className="wk-shead__title">{title}</h2>
        {sub && <p className="wk-shead__sub">{sub}</p>}
      </div>
    </Reveal>
  );
}

Object.assign(window, { akFM, AKLogo, AKCount, AKDot, AKCodeCard, AKHead });
