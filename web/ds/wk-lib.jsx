// AlertKube landing-page UI primitives - shared across sections
// Loaded as a Babel script. Components are exported to window.
const { motion, AnimatePresence, useScroll, useTransform, useInView, useReducedMotion, useMotionValue, useSpring } = window.FramerMotion || {};

/* ------------------------- ICON SET (lucide-style) ------------------------- */
function Icon({ name, size = 18, stroke = 1.75, className = "", style }) {
  const s = { width: size, height: size, ...style };
  const p = { fill: "none", stroke: "currentColor", strokeWidth: stroke, strokeLinecap: "round", strokeLinejoin: "round" };
  const paths = {
    arrow: <path {...p} d="M5 12h14M13 5l7 7-7 7"/>,
    sun: <><circle {...p} cx="12" cy="12" r="4"/><path {...p} d="M12 2v2M12 20v2M2 12h2M20 12h2M5 5l1.5 1.5M17.5 17.5L19 19M5 19l1.5-1.5M17.5 6.5L19 5"/></>,
    moon: <path {...p} d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>,
    chev: <path {...p} d="M6 9l6 6 6-6"/>,
    plus: <path {...p} d="M12 5v14M5 12h14"/>,
    check: <path {...p} d="M5 12l5 5L20 7"/>,
    calendar: <><rect {...p} x="3" y="4" width="18" height="18" rx="2"/><path {...p} d="M16 2v4M8 2v4M3 10h18"/></>,
    chart: <path {...p} d="M3 3v18h18M7 15l3-3 4 4 6-7"/>,
    ring: <circle {...p} cx="12" cy="12" r="9"/>,
    grid: <><rect {...p} x="3" y="3" width="7" height="7"/><rect {...p} x="14" y="3" width="7" height="7"/><rect {...p} x="3" y="14" width="7" height="7"/><rect {...p} x="14" y="14" width="7" height="7"/></>,
    cloud: <path {...p} d="M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9z"/>,
    zap: <path {...p} d="M13 2L3 14h7l-1 8 10-12h-7l1-8z"/>,
    bell: <><path {...p} d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path {...p} d="M10 21a2 2 0 0 0 4 0"/></>,
    layers: <path {...p} d="M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5"/>,
    book: <><path {...p} d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"/><path {...p} d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"/></>,
    target: <><circle {...p} cx="12" cy="12" r="9"/><circle {...p} cx="12" cy="12" r="5"/><circle {...p} cx="12" cy="12" r="1" fill="currentColor"/></>,
    shield: <path {...p} d="M12 2l8 4v6c0 5-4 9-8 10-4-1-8-5-8-10V6l8-4z"/>,
    msg: <path {...p} d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>,
    twitter: <path {...p} d="M22 5.8a8 8 0 0 1-2.4.6 4 4 0 0 0 1.8-2.2 8.5 8.5 0 0 1-2.6 1 4 4 0 0 0-6.8 3.6A11.4 11.4 0 0 1 3 4.7a4 4 0 0 0 1.2 5.3A4 4 0 0 1 2.4 9v.05a4 4 0 0 0 3.2 4 4 4 0 0 1-1.8.07 4 4 0 0 0 3.7 2.8A8 8 0 0 1 2 17.7 11.4 11.4 0 0 0 8.2 19.5c7.5 0 11.6-6.2 11.6-11.6v-.5A8 8 0 0 0 22 5.8z"/>,
    github: <path {...p} d="M9 19c-4 1-4-2-6-2m12 4v-3.5c0-1 .1-1.5-.5-2 2.8-.3 5.5-1.4 5.5-6a4.6 4.6 0 0 0-1.3-3.2 4.2 4.2 0 0 0-.1-3.2s-1.1-.3-3.5 1.3a12 12 0 0 0-6.4 0C6.3 2.8 5.2 3.1 5.2 3.1a4.2 4.2 0 0 0-.1 3.2A4.6 4.6 0 0 0 3.8 9.5c0 4.6 2.7 5.7 5.5 6-.6.5-.6 1-.5 2V21"/>,
  };
  return <svg viewBox="0 0 24 24" className={className} style={s} aria-hidden="true">{paths[name] || null}</svg>;
}

/* ----------------------------- BUTTON --------------------------------- */
function Button({ variant = "grad", size = "md", children, icon, trailing, onClick, href, ariaLabel, className = "" }) {
  const cls = `wk-btn wk-btn--${variant} wk-btn--${size} ${className}`;
  const inner = <>{icon && <span className="wk-btn__icon">{icon}</span>}{children}{trailing && <span className="wk-btn__trail">{trailing}</span>}</>;
  if (href) return <a className={cls} href={href} onClick={onClick} aria-label={ariaLabel}>{inner}</a>;
  return <button className={cls} onClick={onClick} aria-label={ariaLabel}>{inner}</button>;
}

/* ----------------------------- SECTION REVEAL --------------------------- */
// IntersectionObserver always delivers an initial entry for observed elements.
// If none arrives shortly after mount, IO is dead in this environment - force
// content visible rather than leaving it stuck at opacity 0.
function useIOFallback(ref) {
  const [broken, setBroken] = React.useState(false);
  React.useEffect(() => {
    if (!("IntersectionObserver" in window)) { setBroken(true); return; }
    let got = false;
    const io = new IntersectionObserver(() => { got = true; io.disconnect(); });
    if (ref.current) io.observe(ref.current);
    const t = setTimeout(() => { if (!got) setBroken(true); }, 800);
    return () => { clearTimeout(t); io.disconnect(); };
  }, []);
  return broken;
}

function Reveal({ children, delay = 0, y = 24, once = true, as: As = "div", style, className }) {
  const reduce = useReducedMotion();
  const ref = React.useRef(null);
  const inView = useInView(ref, { once, margin: "-10% 0px -10% 0px" });
  const ioBroken = useIOFallback(ref);
  const show = inView || ioBroken;
  if (reduce || ioBroken) return <As ref={ref} className={className} style={style}>{children}</As>;
  return (
    <motion.div
      ref={ref}
      initial={{ opacity: 0, y }}
      animate={show ? { opacity: 1, y: 0 } : {}}
      transition={{ duration: 0.6, delay, ease: [0.22, 1, 0.36, 1] }}
      className={className}
      style={style}
    >{children}</motion.div>
  );
}

function Stagger({ children, gap = 0.06, y = 24, once = true, className, style }) {
  const reduce = useReducedMotion();
  const ref = React.useRef(null);
  const inView = useInView(ref, { once, margin: "-10% 0px -10% 0px" });
  const ioBroken = useIOFallback(ref);
  const show = inView || ioBroken;
  if (reduce || ioBroken) return <div ref={ref} className={className} style={style}>{children}</div>;
  return (
    <motion.div ref={ref}
      initial="hidden" animate={show ? "show" : "hidden"}
      variants={{ show: { transition: { staggerChildren: gap } } }}
      className={className} style={style}>
      {React.Children.map(children, (c, i) => (
        <motion.div key={i} variants={{ hidden: { opacity: 0, y }, show: { opacity: 1, y: 0, transition: { duration: 0.6, ease: [0.22,1,0.36,1] } } }}>
          {c}
        </motion.div>
      ))}
    </motion.div>
  );
}

/* ----------------------------- BLOB CURSOR -------------------------------- */
function BlobCursor() {
  const reduce = useReducedMotion();
  const ref = React.useRef(null);
  const x = useMotionValue(-100), y = useMotionValue(-100);
  const sx = useSpring(x, { stiffness: 350, damping: 28, mass: 0.4 });
  const sy = useSpring(y, { stiffness: 350, damping: 28, mass: 0.4 });
  const [hot, setHot] = React.useState(false);
  const [isDark, setIsDark] = React.useState(() => document.documentElement.dataset.theme === 'dark');
  React.useEffect(() => {
    const obs = new MutationObserver(() => setIsDark(document.documentElement.dataset.theme === 'dark'));
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    return () => obs.disconnect();
  }, []);
  React.useEffect(() => {
    if (reduce) return;
    const isCoarse = window.matchMedia('(pointer:coarse)').matches;
    if (isCoarse) return;
    const move = (e) => { x.set(e.clientX); y.set(e.clientY); };
    const enter = (e) => { if (e.target.closest('[data-cursor-hot]')) setHot(true); };
    const leave = (e) => { if (e.target.closest('[data-cursor-hot]')) setHot(false); };
    window.addEventListener('mousemove', move);
    window.addEventListener('mouseover', enter);
    window.addEventListener('mouseout', leave);
    return () => { window.removeEventListener('mousemove', move); window.removeEventListener('mouseover', enter); window.removeEventListener('mouseout', leave); };
  }, [reduce]);
  if (reduce) return null;
  return (
    <motion.div aria-hidden="true" className="wk-cursor" style={{
      position: "fixed", top: 0, left: 0, width: 24, height: 24, borderRadius: "50%",
      pointerEvents: "none", x: sx, y: sy, translateX: "-50%", translateY: "-50%", zIndex: 99,
      background: hot ? "var(--wk-gradient)" : (isDark ? "rgba(244,244,238,0.55)" : "rgba(10,10,10,0.45)"),
      mixBlendMode: hot ? "normal" : (isDark ? "screen" : "multiply"),
      transition: "background 200ms ease, transform 200ms ease",
      transform: `translate(-50%,-50%) scale(${hot ? 1.6 : 1})`,
      filter: "blur(2px)",
      opacity: 0.85,
    }} />
  );
}

Object.assign(window, { Icon, Button, Reveal, Stagger, BlobCursor, useIOFallback });
