// AlertKube - app composition + tweaks
const appFM = window.FramerMotion || {};
const mApp = appFM.motion;

// The Tweaks panel is a development affordance (live theme/motion knobs), not a
// public landing-page feature. Mount it only on localhost or when ?dev is in the
// URL, so visitors don't download/see dev controls. The navbar still toggles the
// theme; the other tweaks fall back to TWEAK_DEFAULTS.
const devMode =
  new URLSearchParams(window.location.search).has("dev") ||
  /^(localhost|127\.0\.0\.1)$/.test(window.location.hostname);

const TWEAK_DEFAULTS = /*EDITMODE-BEGIN*/{
  "theme": "dark",
  "liveFeed": true,
  "blobCursor": true
}/*EDITMODE-END*/;

function AKApp() {
  const [t, setTweak] = useTweaks(TWEAK_DEFAULTS);
  const { scrollYProgress } = appFM.useScroll();

  React.useEffect(() => {
    document.documentElement.dataset.theme = t.theme;
  }, [t.theme]);

  return (
    <div className="wk-shell">
      {mApp && <mApp.div className="ak-progress" style={{ scaleX: scrollYProgress }} />}
      {t.blobCursor && <BlobCursor />}

      <AKNav theme={t.theme} setTheme={(v) => setTweak("theme", v)} />
      <AKHero live={t.liveFeed} />
      <AKMarquee />
      <AKPipeline />
      <AKArchitecture />
      <AKSeverity />
      <AKWatchers />
      <AKCloudSources />
      <AKMetricsBand />
      <AKSinks />
      <AKConfig />
      <AKInstall />
      <AKChangelog />
      <AKFinalCTA />
      <AKFooterSec />

      {devMode && (
        <TweaksPanel>
          <TweakSection label="Theme" />
          <TweakRadio label="Mode" value={t.theme} options={["light", "dark"]} onChange={(v) => setTweak("theme", v)} />
          <TweakSection label="Motion" />
          <TweakToggle label="Live alert feed" value={t.liveFeed} onChange={(v) => setTweak("liveFeed", v)} />
          <TweakToggle label="Blob cursor" value={t.blobCursor} onChange={(v) => setTweak("blobCursor", v)} />
        </TweaksPanel>
      )}
    </div>
  );
}

ReactDOM.createRoot(document.getElementById("root")).render(<AKApp />);
