"use strict";

// AlertKube read-only console. No framework, no build step. Talks to the
// token-gated /api/* endpoints and the open /metrics endpoint. Token lives in
// sessionStorage (this tab only) and is sent as an Authorization: Bearer header.

const TOKEN_KEY = "alertkube.token";
const WRITE_TOKEN_KEY = "alertkube.writeToken";
const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

let lastAlerts = { active: [], recent: [] };
let lastConfigSilences = [];
let lastConfigYaml = "";
let lastConfig = {};

function token() { return sessionStorage.getItem(TOKEN_KEY) || ""; }
function setToken(t) { t ? sessionStorage.setItem(TOKEN_KEY, t) : sessionStorage.removeItem(TOKEN_KEY); }
function writeToken() { return sessionStorage.getItem(WRITE_TOKEN_KEY) || ""; }
function setWriteToken(t) { t ? sessionStorage.setItem(WRITE_TOKEN_KEY, t) : sessionStorage.removeItem(WRITE_TOKEN_KEY); }

function setMetric(sel, value) {
  const el = $(sel);
  const next = String(value);
  if (el.textContent === next) return;
  el.textContent = next;
  el.classList.remove("metric-pop");
  void el.offsetWidth;
  el.classList.add("metric-pop");
}

function setHeroStatus(label) {
  const el = $("#hero-status");
  if (el) el.textContent = label;
}

function setConn(state, label) {
  const el = $("#conn");
  el.dataset.state = state;
  el.textContent = label;
  if (state === "ok") setHeroStatus("Live data is connected and refreshing automatically.");
  else if (state === "auth") setHeroStatus("Paste a read token to unlock the live dashboard.");
  else if (state === "err") setHeroStatus("This replica is not serving live data right now.");
  else setHeroStatus("Loading the latest alert and routing state...");
}

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function ago(ts) {
  if (!ts) return "—";
  const t = new Date(ts).getTime();
  if (isNaN(t)) return "—";
  const s = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (s < 60) return s + "s";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 86400) return Math.floor(s / 3600) + "h";
  return Math.floor(s / 86400) + "d";
}

// fetchJSON returns {ok, status, data}. 401 => prompt token; 503 => standby.
async function fetchJSON(path, opts) {
  const headers = Object.assign({}, (opts && opts.headers) || {});
  if (token()) headers["Authorization"] = "Bearer " + token();
  const res = await fetch(path, Object.assign({ headers }, opts));
  let data = null;
  try { data = await res.json(); } catch (_) { /* non-JSON */ }
  return { ok: res.ok, status: res.status, data };
}

// fetchWrite sends the WRITE token (mutations are gated separately, fail-closed
// server-side). Returns {ok, status, data}.
async function fetchWrite(path, opts) {
  const headers = Object.assign({}, (opts && opts.headers) || {});
  if (writeToken()) headers["Authorization"] = "Bearer " + writeToken();
  const res = await fetch(path, Object.assign({ headers }, opts));
  let data = null;
  try { data = await res.json(); } catch (_) { /* 204/empty */ }
  return { ok: res.ok, status: res.status, data };
}

// ---- Tabs ----
// activateTab selects one tab and reveals its panel, implementing the ARIA
// tabs pattern: the selected tab is the only one in the focus order
// (roving tabindex), the rest are removed from Tab order.
function activateTab(tab, focus) {
  $$(".tab").forEach((t) => {
    const selected = t === tab;
    t.setAttribute("aria-selected", selected ? "true" : "false");
    t.tabIndex = selected ? 0 : -1;
  });
  $$(".tabpanel").forEach((p) => p.classList.add("hidden"));
  $("#tab-" + tab.dataset.tab).classList.remove("hidden");
  if (focus) tab.focus();
}

function initTabs() {
  const tabs = $$(".tab");
  tabs.forEach((tab, i) => {
    tab.addEventListener("click", () => activateTab(tab, false));
    // Keyboard support per the WAI-ARIA tabs pattern: Left/Right (and
    // Home/End) move between tabs and activate them; the roving tabindex keeps
    // a single Tab stop.
    tab.addEventListener("keydown", (e) => {
      let next = -1;
      switch (e.key) {
        case "ArrowRight": case "ArrowDown": next = (i + 1) % tabs.length; break;
        case "ArrowLeft": case "ArrowUp": next = (i - 1 + tabs.length) % tabs.length; break;
        case "Home": next = 0; break;
        case "End": next = tabs.length - 1; break;
        default: return;
      }
      e.preventDefault();
      activateTab(tabs[next], true);
    });
  });
}

// ---- Theme (light/dark) ----
const THEME_KEY = "alertkube.theme";
function applyTheme(theme) {
  // theme is "light", "dark", or null (follow the OS preference).
  if (theme === "light" || theme === "dark") {
    document.documentElement.setAttribute("data-theme", theme);
  } else {
    document.documentElement.removeAttribute("data-theme");
  }
  const btn = $("#theme-toggle");
  if (btn) {
    const dark = theme === "dark" ||
      (!theme && window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
    btn.textContent = dark ? "☀" : "☾";
    btn.setAttribute("aria-label", dark ? "Switch to light theme" : "Switch to dark theme");
  }
}
function initTheme() {
  const saved = localStorage.getItem(THEME_KEY);
  applyTheme(saved);
  const btn = $("#theme-toggle");
  if (!btn) return;
  btn.addEventListener("click", () => {
    // Resolve the currently effective theme, then flip it and pin the choice.
    const current = document.documentElement.getAttribute("data-theme") ||
      (window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
    const next = current === "dark" ? "light" : "dark";
    localStorage.setItem(THEME_KEY, next);
    applyTheme(next);
  });
}

// ---- Auth ----
function showAuth(msg) {
  $("#auth").classList.remove("hidden");
  $("#auth-msg").textContent = msg || "";
  setConn("auth", "no token");
}
function hideAuth() { $("#auth").classList.add("hidden"); }

function initAuth() {
  $("#token").value = token();
  $("#write-token").value = writeToken();
  $("#token-save").addEventListener("click", () => {
    setToken($("#token").value.trim());
    setWriteToken($("#write-token").value.trim());
    hideAuth();
    refresh();
  });
  $("#token-clear").addEventListener("click", () => {
    setToken("");
    setWriteToken("");
    $("#token").value = "";
    $("#write-token").value = "";
    showAuth("Tokens cleared.");
  });
}

// ---- Alerts ----
// Sort state for the alerts table. Clicking a column header cycles asc/desc.
let alertSort = { key: "Severity", dir: "asc" };
// Fingerprints whose detail row is expanded (persists across refreshes).
const expandedAlerts = new Set();

// severityRank orders severities critical > warning > info for sensible sorting.
function severityRank(s) {
  return { critical: 0, warning: 1, info: 2 }[String(s || "").toLowerCase()] ?? 3;
}

function sortAlerts(rows) {
  const { key, dir } = alertSort;
  const mul = dir === "desc" ? -1 : 1;
  return rows.slice().sort((a, b) => {
    let av, bv;
    if (key === "Severity") { av = severityRank(a.Severity); bv = severityRank(b.Severity); }
    else if (key === "StartsAt") { av = new Date(a.StartsAt).getTime() || 0; bv = new Date(b.StartsAt).getTime() || 0; }
    else if (key === "Resolved") { av = a.Resolved ? 1 : 0; bv = b.Resolved ? 1 : 0; }
    else { av = String(a[key] || "").toLowerCase(); bv = String(b[key] || "").toLowerCase(); }
    if (av < bv) return -1 * mul;
    if (av > bv) return 1 * mul;
    return 0;
  });
}

// detailHTML renders an alert's Details map (events/logs) for the expanded row.
function detailHTML(a) {
  const d = a.Details || {};
  const keys = Object.keys(d);
  if (!keys.length) return '<span class="muted small">No additional detail (enrichment is dropped from recent/resolved entries).</span>';
  return keys.map((k) => `<div class="detail-block"><div class="detail-h">${esc(k)}</div><pre class="raw">${esc(d[k])}</pre></div>`).join("");
}

function fp(a) { return a.Fingerprint || (a.Kind + "/" + a.Namespace + "/" + a.Name + "/" + a.Reason); }

function renderAlerts() {
  const filter = ($("#alert-filter").value || "").toLowerCase();
  const showResolved = $("#show-resolved").checked;
  let rows = (showResolved ? [...lastAlerts.active, ...lastAlerts.recent] : lastAlerts.active)
    .filter((a) => {
      if (!filter) return true;
      return [a.Name, a.Namespace, a.Reason, a.Summary, a.Kind]
        .some((v) => String(v || "").toLowerCase().includes(filter));
    });
  rows = sortAlerts(rows);

  const tbody = $("#alerts-table tbody");
  tbody.innerHTML = rows.map((a) => {
    const sev = String(a.Severity || "info").toLowerCase();
    const state = a.Resolved ? '<span class="state-resolved">resolved</span>' : '<span class="state-firing">firing</span>';
    const id = fp(a);
    const open = expandedAlerts.has(id);
    const main = `<tr class="alert-row${open ? " expanded" : ""}" data-fp="${esc(id)}" tabindex="0" aria-expanded="${open}">
      <td data-label="Sev"><span class="sev sev-${esc(sev)}">${esc(sev)}</span></td>
      <td data-label="Kind">${esc(a.Kind)}</td>
      <td data-label="Namespace">${esc(a.Namespace)}</td>
      <td data-label="Name">${esc(a.Name)}</td>
      <td data-label="Reason">${esc(a.Reason)}</td>
      <td data-label="Summary" class="wrap">${esc(a.Summary)}</td>
      <td data-label="Since">${esc(ago(a.StartsAt))}</td>
      <td data-label="State">${state}</td>
    </tr>`;
    const detail = open
      ? `<tr class="alert-detail" data-detail="${esc(id)}"><td colspan="8">${detailHTML(a)}</td></tr>`
      : "";
    return main + detail;
  }).join("");
  $("#alerts-empty").classList.toggle("hidden", rows.length > 0);
  updateSortIndicators();
}

// updateSortIndicators reflects the active sort column/direction on the headers.
function updateSortIndicators() {
  $$(".th-sort").forEach((b) => {
    const active = b.dataset.sort === alertSort.key;
    b.setAttribute("aria-sort", active ? (alertSort.dir === "asc" ? "ascending" : "descending") : "none");
    b.dataset.active = active ? "true" : "false";
    b.dataset.dir = active ? alertSort.dir : "";
  });
}

function initAlertsTable() {
  // Header click toggles sort.
  $$(".th-sort").forEach((b) => {
    b.addEventListener("click", () => {
      const key = b.dataset.sort;
      if (alertSort.key === key) {
        alertSort.dir = alertSort.dir === "asc" ? "desc" : "asc";
      } else {
        alertSort = { key, dir: "asc" };
      }
      renderAlerts();
    });
  });
  // Row click/Enter toggles the expanded detail row.
  const tbody = $("#alerts-table tbody");
  const toggle = (tr) => {
    const id = tr && tr.getAttribute("data-fp");
    if (!id) return;
    if (expandedAlerts.has(id)) expandedAlerts.delete(id);
    else expandedAlerts.add(id);
    renderAlerts();
  };
  tbody.addEventListener("click", (e) => toggle(e.target.closest(".alert-row")));
  tbody.addEventListener("keydown", (e) => {
    if ((e.key === "Enter" || e.key === " ") && e.target.classList.contains("alert-row")) {
      e.preventDefault();
      toggle(e.target);
    }
  });
}

// ---- Config ----
function kv(pairs) {
  return '<dl class="kv">' + pairs.map(([k, v]) => `<dt>${esc(k)}</dt><dd>${v}</dd>`).join("") + "</dl>";
}
function onoff(b) { return b ? '<span class="on">on</span>' : '<span class="off">off</span>'; }

function renderConfig(cfg, raw) {
  cfg = cfg || {};
  lastConfig = cfg;

  // Sources
  const cloud = (name, c) => {
    if (!c) return `${name}: ${onoff(false)}`;
    const subs = Object.keys(c).filter((k) => c[k] === true && k !== "enabled");
    return `${name}: ${onoff(!!c.enabled)}` + (c.enabled && subs.length ? ` &nbsp;${subs.map((s) => `<span class="pill">${esc(s)}</span>`).join("")}` : "");
  };
  $("#cfg-sources").innerHTML = kv([
    ["Kubernetes", '<span class="on">on</span> (in-cluster watchers)'],
    ["AWS", cloud("", cfg.aws)],
    ["Azure", cloud("", cfg.azure)],
    ["GCP", cloud("", cfg.gcp)],
  ]);

  // Channels & routing
  const ch = cfg.channels || {};
  const routing = (cfg.routing || []).map((r) => {
    const m = Object.entries(r.match || {}).map(([k, v]) => `${esc(k)}=${esc(v)}`).join(", ") || "any";
    const sinks = (r.sinks || []).map((s) => `<span class="pill">${esc(s)}</span>`).join("");
    return `<div>${esc(m)} → ${sinks}</div>`;
  }).join("") || '<span class="muted">none</span>';
  $("#cfg-routing").innerHTML = kv([
    ["critical", esc(ch.critical || "—")],
    ["warning", esc(ch.warning || "—")],
    ["info", esc(ch.info || "—")],
    ["routing", routing],
  ]);

  // Rules (alert patterns)
  const rules = cfg.rules || [];
  $("#cfg-rules").innerHTML = rules.length
    ? `<div class="table-wrap"><table><thead><tr><th>Name</th><th>Sev</th><th>Type</th><th>Window</th></tr></thead><tbody>` +
      rules.map((r) => {
        const type = r.count ? "count" : r.all ? "all" : r.absent ? "absent" : "—";
        const win = r.absent && r.absent.forSeconds ? r.absent.forSeconds + "s" : (r.windowSeconds ? r.windowSeconds + "s" : "—");
        return `<tr><td>${esc(r.name)}</td><td>${esc(r.severity)}</td><td>${esc(type)}</td><td>${esc(win)}</td></tr>`;
      }).join("") + "</tbody></table></div>"
    : '<span class="muted">No correlation rules configured.</span>';

  // Grouping (pattern groups with a timeframe)
  const g = cfg.grouping || {};
  $("#cfg-grouping").innerHTML = kv([
    ["enabled", onoff(!!g.enabled)],
    ["window", g.windowSeconds ? esc(g.windowSeconds) + "s" : "—"],
    ["group by", (g.by || []).map((b) => `<span class="pill">${esc(b)}</span>`).join("") || '<span class="muted">kind, namespace, reason, severity (default)</span>'],
  ]);

  // Silences (config / Git)
  const sil = cfg.silences || [];
  lastConfigSilences = sil;
  const cfgSilHTML = sil.length
    ? sil.map((s) => {
        const m = Object.entries(s.matchers || {}).map(([k, v]) => `${esc(k)}=${esc(v)}`).join(", ");
        return `<div>${esc(m)} <span class="muted">until ${esc(s.until)}</span></div>`;
      }).join("")
    : '<span class="muted">No config silences.</span>';
  $("#cfg-silences").innerHTML = cfgSilHTML;
  if ($("#sil-config")) $("#sil-config").innerHTML = cfgSilHTML;

  // Maintenance windows (recurring suppression)
  const maint = cfg.maintenance || [];
  $("#cfg-maintenance").innerHTML = maint.length
    ? maint.map((w) => {
        const m = Object.entries(w.matchers || {}).map(([k, v]) => `${esc(k)}=${esc(v)}`).join(", ");
        const days = (w.days && w.days.length) ? w.days.join(",") : "every day";
        const tz = w.timezone || "UTC";
        const name = w.name ? `<strong>${esc(w.name)}</strong> ` : "";
        return `<div>${name}${esc(m)} <span class="muted">${esc(w.start)}–${esc(w.end)} ${esc(tz)} (${esc(days)})</span></div>`;
      }).join("")
    : '<span class="muted">No maintenance windows.</span>';

  lastConfigYaml = raw || "";
  $("#cfg-raw").textContent = lastConfigYaml;

  // overview counters that come from config
  setMetric("#ov-rules", rules.length);
  setMetric("#ov-channels", (cfg.routing || []).length);
  if (cfg.cluster) {
    $("#cluster").textContent = cfg.cluster;
    const heroCluster = $("#hero-cluster");
    if (heroCluster) heroCluster.textContent = cfg.cluster;
  }
}

// ---- Validate ----
function initValidate() {
  $("#validate-run").addEventListener("click", async () => {
    const body = $("#validate-input").value;
    const out = $("#validate-result");
    out.className = "validate-result";
    out.textContent = "validating…";
    const res = await fetchJSON("/api/config/validate", {
      method: "POST",
      headers: { "Content-Type": "application/x-yaml" },
      body,
    });
    if (res.status === 401) { out.textContent = "unauthorized — set token"; out.className = "validate-result bad"; return; }
    if (res.data && res.data.ok) {
      out.textContent = "✓ valid";
      out.className = "validate-result ok";
    } else {
      out.textContent = "✗ " + ((res.data && res.data.error) || ("status " + res.status));
      out.className = "validate-result bad";
    }
  });
}

// ---- Suppression (parse /metrics) ----
function parseSuppressed(text) {
  // alertkube_alerts_suppressed_total{reason="silence"} 12
  const out = [];
  const re = /^alertkube_alerts_suppressed_total\{reason="([^"]+)"\}\s+([0-9.e+]+)/gm;
  let m;
  while ((m = re.exec(text)) !== null) out.push([m[1], Number(m[2])]);
  return out.sort((a, b) => b[1] - a[1]);
}
function metricValue(text, name) {
  const re = new RegExp("^" + name + "(?:\\{[^}]*\\})?\\s+([0-9.e+]+)", "m");
  const m = re.exec(text);
  return m ? Number(m[1]) : null;
}

async function loadMetrics() {
  let text = "";
  try {
    const res = await fetch("/metrics");
    if (!res.ok) return;
    text = await res.text();
  } catch (_) { return; }

  const active = metricValue(text, "alertkube_active_alerts");
  if (active != null) setMetric("#ov-active", active);

  const supp = parseSuppressed(text);
  const total = supp.reduce((acc, [, n]) => acc + n, 0);
  setMetric("#ov-suppressed", total);

  const tbody = $("#supp-table tbody");
  tbody.innerHTML = supp.map(([r, n]) => `<tr><td>${esc(r)}</td><td class="num">${n}</td></tr>`).join("");
  $("#supp-empty").classList.toggle("hidden", supp.length > 0);
}

// ---- Silences (runtime) ----
function parseMatchers(text) {
  const out = {};
  (text || "").split("\n").forEach((line) => {
    const t = line.trim();
    if (!t) return;
    const i = t.indexOf("=");
    if (i <= 0) return;
    out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  });
  return out;
}

function silenceUntil() {
  const d = $("#sil-duration").value;
  if (d === "custom") {
    const v = $("#sil-until").value;
    if (!v) return null;
    return new Date(v).toISOString();
  }
  return new Date(Date.now() + Number(d) * 1000).toISOString();
}

function renderSilences(list) {
  const tbody = $("#sil-table tbody");
  tbody.innerHTML = (list || []).map((s) => {
    const m = Object.entries(s.matchers || {}).map(([k, v]) => `${esc(k)}=${esc(v)}`).join(", ");
    const until = s.until ? new Date(s.until).toLocaleString() : "—";
    return `<tr>
      <td>${esc(m)}</td>
      <td>${esc(until)}</td>
      <td class="wrap">${esc(s.comment || "")}</td>
      <td>${esc(s.createdBy || "")}</td>
      <td><button type="button" class="btn danger" data-del="${esc(s.id)}">Expire</button></td>
    </tr>`;
  }).join("");
  $("#sil-empty").classList.toggle("hidden", (list || []).length > 0);
  $("#write-disabled").classList.toggle("hidden", !!writeToken());
}

async function loadSilences() {
  const res = await fetchJSON("/api/silences");
  if (res.ok && res.data) renderSilences(res.data.runtime || []);
}

async function createSilence() {
  const msg = $("#sil-msg");
  msg.className = "validate-result";
  const matchers = parseMatchers($("#sil-matchers").value);
  if (Object.keys(matchers).length === 0) {
    msg.textContent = "add at least one key=value matcher";
    msg.className = "validate-result bad";
    return;
  }
  const until = silenceUntil();
  if (!until) { msg.textContent = "pick an expiry"; msg.className = "validate-result bad"; return; }

  msg.textContent = "creating…";
  const res = await fetchWrite("/api/silences", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ matchers, until, comment: $("#sil-comment").value }),
  });
  if (res.status === 403) { msg.textContent = "✗ writes disabled — set a write token"; msg.className = "validate-result bad"; return; }
  if (res.status === 401) { msg.textContent = "✗ write token rejected"; msg.className = "validate-result bad"; return; }
  if (!res.ok) { msg.textContent = "✗ " + ((res.data && res.data.error) || ("status " + res.status)); msg.className = "validate-result bad"; return; }
  msg.textContent = "✓ created";
  msg.className = "validate-result ok";
  $("#sil-matchers").value = "";
  $("#sil-comment").value = "";
  loadSilences();
}

async function deleteSilence(id) {
  const res = await fetchWrite("/api/silences/" + encodeURIComponent(id), { method: "DELETE" });
  if (res.status === 403 || res.status === 401) {
    $("#sil-msg").textContent = "✗ delete needs a valid write token";
    $("#sil-msg").className = "validate-result bad";
    return;
  }
  loadSilences();
}

function initSilences() {
  $("#sil-duration").addEventListener("change", () => {
    $("#sil-until-wrap").style.display = $("#sil-duration").value === "custom" ? "" : "none";
  });
  $("#sil-create").addEventListener("click", createSilence);
  $("#sil-table").addEventListener("click", (e) => {
    const id = e.target && e.target.getAttribute && e.target.getAttribute("data-del");
    if (id) deleteSilence(id);
  });
}

// ---- Channels (test-fire) ----
function renderChannels(list) {
  const tbody = $("#chan-table tbody");
  tbody.innerHTML = (list || []).map((name) => `<tr>
      <td>${esc(name)}</td>
      <td><button type="button" class="btn" data-test="${esc(name)}">Test</button></td>
      <td class="chan-result" data-result="${esc(name)}"></td>
    </tr>`).join("");
  $("#chan-empty").classList.toggle("hidden", (list || []).length > 0);
  $("#chan-disabled").classList.toggle("hidden", !!writeToken());
}

async function loadChannels() {
  const res = await fetchJSON("/api/channels");
  if (res.ok && res.data) renderChannels(res.data.channels || []);
}

async function testChannel(name) {
  const cell = document.querySelector(`[data-result="${CSS.escape(name)}"]`);
  if (cell) { cell.textContent = "sending…"; cell.className = "chan-result"; }
  const res = await fetchWrite("/api/channels/test", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sink: name }),
  });
  if (!cell) return;
  if (res.status === 403) { cell.textContent = "✗ writes disabled — set write token"; cell.className = "chan-result bad"; return; }
  if (res.status === 401) { cell.textContent = "✗ write token rejected"; cell.className = "chan-result bad"; return; }
  if (res.data && res.data.ok) { cell.textContent = "✓ sent"; cell.className = "chan-result ok"; }
  else { cell.textContent = "✗ " + ((res.data && res.data.error) || ("status " + res.status)); cell.className = "chan-result bad"; }
}

async function testChannelByRef() {
  const msg = $("#ref-msg");
  const name = $("#ref-name").value.trim();
  const key = $("#ref-key").value.trim();
  if (!name || !key) { msg.textContent = "secret name and key required"; msg.className = "chan-result bad"; return; }
  msg.textContent = "testing…";
  msg.className = "chan-result";
  const res = await fetchWrite("/api/channels/test-ref", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ type: $("#ref-type").value, secretRef: { name, key } }),
  });
  if (res.status === 403) { msg.textContent = "✗ disabled — needs api.allowSecretRead + write token"; msg.className = "chan-result bad"; return; }
  if (res.status === 401) { msg.textContent = "✗ write token rejected"; msg.className = "chan-result bad"; return; }
  if (res.data && res.data.ok) { msg.textContent = "✓ credential valid — channel reachable"; msg.className = "chan-result ok"; }
  else { msg.textContent = "✗ " + ((res.data && res.data.error) || ("status " + res.status)); msg.className = "chan-result bad"; }
}

function initChannels() {
  $("#chan-table").addEventListener("click", (e) => {
    const name = e.target && e.target.getAttribute && e.target.getAttribute("data-test");
    if (name) testChannel(name);
  });
  $("#ref-test").addEventListener("click", testChannelByRef);
}

// ---- Author (UI-as-PR: edit, validate, diff, export) ----
// lineDiff returns a unified-ish diff via an LCS over lines. Pure client-side;
// the server never sees an edit until the operator commits it to Git.
function lcsLines(a, b) {
  const m = a.length, n = b.length;
  const dp = Array.from({ length: m + 1 }, () => new Array(n + 1).fill(0));
  for (let i = m - 1; i >= 0; i--) {
    for (let j = n - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const out = [];
  let i = 0, j = 0;
  while (i < m && j < n) {
    if (a[i] === b[j]) { out.push([" ", a[i]]); i++; j++; }
    else if (dp[i + 1][j] >= dp[i][j + 1]) { out.push(["-", a[i]]); i++; }
    else { out.push(["+", b[j]]); j++; }
  }
  while (i < m) { out.push(["-", a[i]]); i++; }
  while (j < n) { out.push(["+", b[j]]); j++; }
  return out;
}

function renderDiff() {
  const card = $("#author-diff-card");
  const live = (lastConfigYaml || "").replace(/\s+$/, "").split("\n");
  const edited = ($("#author-input").value || "").replace(/\s+$/, "").split("\n");
  const diff = lcsLines(live, edited);
  const changed = diff.some((d) => d[0] !== " ");
  const el = $("#author-diff");
  el.innerHTML = changed
    ? diff.map(([sign, line]) => {
        const cls = sign === "+" ? "add" : sign === "-" ? "del" : "ctx";
        return `<span class="d-${cls}">${esc(sign + " " + line)}</span>`;
      }).join("\n")
    : '<span class="muted">No changes vs the live config.</span>';
  card.style.display = "";
}

async function validateAuthor() {
  const out = $("#author-msg");
  out.className = "validate-result";
  out.textContent = "validating…";
  const res = await fetchJSON("/api/config/validate", {
    method: "POST",
    headers: { "Content-Type": "application/x-yaml" },
    body: $("#author-input").value,
  });
  if (res.status === 401) { out.textContent = "unauthorized — set token"; out.className = "validate-result bad"; return; }
  if (res.data && res.data.ok) { out.textContent = "✓ valid"; out.className = "validate-result ok"; }
  else { out.textContent = "✗ " + ((res.data && res.data.error) || ("status " + res.status)); out.className = "validate-result bad"; }
}

async function copyAuthor() {
  const out = $("#author-msg");
  try {
    await navigator.clipboard.writeText($("#author-input").value);
    out.textContent = "✓ copied"; out.className = "validate-result ok";
  } catch (_) {
    out.textContent = "copy failed — select and copy manually"; out.className = "validate-result bad";
  }
}

function downloadAuthor() {
  const blob = new Blob([$("#author-input").value], { type: "application/x-yaml" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = "alertkube-config.yaml";
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

// ---- Author: form builders (rules / grouping / routing) ----
function matchersToText(m) {
  return Object.entries(m || {}).map(([k, v]) => `${k}=${v}`).join("\n");
}

function addRuleRow(r) {
  r = r || {};
  const div = document.createElement("div");
  div.className = "form-row rule-row";
  div.innerHTML = `
    <input class="r-name" placeholder="name" />
    <select class="r-sev"><option value="critical">critical</option><option value="warning">warning</option><option value="info">info</option></select>
    <select class="r-type"><option value="count">count</option><option value="all">all</option><option value="absent">absent</option></select>
    <input class="r-num" type="number" placeholder="threshold" title="count: threshold" style="width:90px" />
    <input class="r-win" type="number" placeholder="window/for (s)" title="count/all: window; absent: for" style="width:120px" />
    <button type="button" class="btn danger r-del" title="remove">×</button>
    <textarea class="r-match" rows="2" placeholder="matchers: key=value per line (for 'all', one condition per line)"></textarea>
    <input class="r-summary" placeholder="summary (optional)" />`;
  $("#rules-rows").appendChild(div);
  // Populate from the rule object.
  const type = r.count ? "count" : r.all ? "all" : r.absent ? "absent" : "count";
  div.querySelector(".r-name").value = r.name || "";
  div.querySelector(".r-sev").value = r.severity || "warning";
  div.querySelector(".r-type").value = type;
  div.querySelector(".r-summary").value = r.summary || "";
  if (type === "count" && r.count) {
    div.querySelector(".r-num").value = r.count.threshold || "";
    div.querySelector(".r-win").value = r.windowSeconds || "";
    div.querySelector(".r-match").value = matchersToText(r.count.match);
  } else if (type === "absent" && r.absent) {
    div.querySelector(".r-win").value = r.absent.forSeconds || "";
    div.querySelector(".r-match").value = matchersToText(r.absent.match);
  } else if (type === "all" && r.all) {
    div.querySelector(".r-win").value = r.windowSeconds || "";
    div.querySelector(".r-match").value = (r.all || []).map((m) => Object.entries(m).map(([k, v]) => `${k}=${v}`).join(",")).join("\n");
  }
}

function collectRules() {
  return Array.from(document.querySelectorAll(".rule-row")).map((row) => {
    const name = row.querySelector(".r-name").value.trim();
    if (!name) return null;
    const type = row.querySelector(".r-type").value;
    const sev = row.querySelector(".r-sev").value;
    const num = Number(row.querySelector(".r-num").value);
    const win = Number(row.querySelector(".r-win").value);
    const matchText = row.querySelector(".r-match").value;
    const summary = row.querySelector(".r-summary").value.trim();
    const rule = { name, severity: sev };
    if (summary) rule.summary = summary;
    if (type === "count") {
      rule.count = { match: parseMatchers(matchText), threshold: num || 0 };
      if (win) rule.windowSeconds = win;
    } else if (type === "absent") {
      rule.absent = { match: parseMatchers(matchText), forSeconds: win || 0 };
    } else if (type === "all") {
      rule.all = matchText.split("\n").map((line) => parseMatchers(line.replace(/,/g, "\n"))).filter((m) => Object.keys(m).length);
      if (win) rule.windowSeconds = win;
    }
    return rule;
  }).filter(Boolean);
}

function addRouteRow(rt) {
  rt = rt || {};
  const div = document.createElement("div");
  div.className = "form-row route-row";
  div.innerHTML = `
    <textarea class="rt-match" rows="2" placeholder="match: key=value per line (empty = any)"></textarea>
    <input class="rt-sinks" placeholder="sinks: comma-separated (slack,pagerduty)" />
    <button type="button" class="btn danger rt-del" title="remove">×</button>`;
  $("#routes-rows").appendChild(div);
  div.querySelector(".rt-match").value = matchersToText(rt.match);
  div.querySelector(".rt-sinks").value = (rt.sinks || []).join(",");
}

function collectRoutes() {
  return Array.from(document.querySelectorAll(".route-row")).map((row) => {
    const sinks = row.querySelector(".rt-sinks").value.split(",").map((s) => s.trim()).filter(Boolean);
    if (!sinks.length) return null;
    return { match: parseMatchers(row.querySelector(".rt-match").value), sinks };
  }).filter(Boolean);
}

function loadFormsFromLive() {
  $("#rules-rows").innerHTML = "";
  (lastConfig.rules || []).forEach(addRuleRow);
  $("#routes-rows").innerHTML = "";
  (lastConfig.routing || []).forEach(addRouteRow);
  const g = lastConfig.grouping || {};
  $("#g-enabled").checked = !!g.enabled;
  $("#g-window").value = g.windowSeconds || "";
  $("#g-by").value = (g.by || []).join(",");
  $("#form-msg").textContent = "loaded from live config";
  $("#form-msg").className = "validate-result";
}

async function renderFromForms() {
  const patch = {
    rules: collectRules(),
    routing: collectRoutes(),
    grouping: {
      enabled: $("#g-enabled").checked,
      windowSeconds: Number($("#g-window").value) || 0,
      by: $("#g-by").value.split(",").map((s) => s.trim()).filter(Boolean),
    },
  };
  const msg = $("#form-msg");
  msg.textContent = "rendering…";
  msg.className = "validate-result";
  const res = await fetchJSON("/api/config/render", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  });
  if (res.status === 401) { msg.textContent = "unauthorized — set token"; msg.className = "validate-result bad"; return; }
  if (res.ok && res.data && res.data.yaml) {
    $("#author-input").value = res.data.yaml;
    msg.textContent = "✓ rendered into the editor above — now Validate / Diff / Export";
    msg.className = "validate-result ok";
  } else {
    msg.textContent = "✗ " + ((res.data && res.data.error) || ("status " + res.status));
    msg.className = "validate-result bad";
  }
}

function initFormBuilders() {
  $("#add-rule").addEventListener("click", () => addRuleRow());
  $("#add-route").addEventListener("click", () => addRouteRow());
  $("#rules-rows").addEventListener("click", (e) => { if (e.target.classList.contains("r-del")) e.target.closest(".rule-row").remove(); });
  $("#routes-rows").addEventListener("click", (e) => { if (e.target.classList.contains("rt-del")) e.target.closest(".route-row").remove(); });
  $("#form-load").addEventListener("click", loadFormsFromLive);
  $("#form-render").addEventListener("click", renderFromForms);
}

function initAuthor() {
  $("#auth-load").addEventListener("click", () => {
    $("#author-input").value = lastConfigYaml || "";
    $("#author-msg").textContent = lastConfigYaml ? "loaded live config" : "no config loaded yet — connect first";
    $("#author-msg").className = "validate-result";
  });
  $("#auth-validate").addEventListener("click", validateAuthor);
  $("#auth-diff").addEventListener("click", renderDiff);
  $("#auth-copy").addEventListener("click", copyAuthor);
  $("#auth-download").addEventListener("click", downloadAuthor);
}

// ---- Refresh orchestration ----
async function refresh() {
  if (!token()) { showAuth(); return; }
  setConn("idle", "loading…");

  const alertsRes = await fetchJSON("/api/alerts");
  if (alertsRes.status === 401) { showAuth("Token rejected (401)."); return; }
  hideAuth();

  if (alertsRes.status === 503) {
    $("#standby").classList.remove("hidden");
    setConn("err", "standby");
  } else {
    $("#standby").classList.add("hidden");
  }

  if (alertsRes.ok && alertsRes.data) {
    lastAlerts = { active: alertsRes.data.active || [], recent: alertsRes.data.recent || [] };
    renderAlerts();
    const active = lastAlerts.active;
    setMetric("#ov-active", active.length);
    setMetric("#ov-critical", active.filter((a) => String(a.Severity).toLowerCase() === "critical").length);
    setMetric("#ov-warning", active.filter((a) => String(a.Severity).toLowerCase() === "warning").length);
    setConn("ok", "connected");
  }

  const cfgRes = await fetchJSON("/api/config");
  if (cfgRes.ok && cfgRes.data) renderConfig(cfgRes.data.config, cfgRes.data.yaml);

  await loadSilences();
  await loadChannels();
  await loadMetrics();
}

// ---- init ----
window.addEventListener("DOMContentLoaded", () => {
  initTheme();
  initTabs();
  initAuth();
  initValidate();
  initSilences();
  initAuthor();
  initFormBuilders();
  initChannels();
  initAlertsTable();
  $("#refresh").addEventListener("click", refresh);
  $("#alert-filter").addEventListener("input", renderAlerts);
  $("#show-resolved").addEventListener("change", renderAlerts);
  refresh();
  setInterval(refresh, 15000);
});
