"use strict";

// ---------- bridge to Go ----------
const go = () => window.go.main.App;
const rt = () => window.runtime;

// ---------- helpers ----------
const $ = (id) => document.getElementById(id);

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "style") el.style.cssText = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (v === true) el.setAttribute(k, "");
    else el.setAttribute(k, v);
  }
  for (const kid of kids.flat()) {
    if (kid == null || kid === false) continue;
    el.append(kid.nodeType ? kid : document.createTextNode(kid));
  }
  return el;
}

const clear = (el) => el.replaceChildren();
// Element.append stringifies null ("null"), so drop empty children first.
const put = (el, ...kids) => el.append(...kids.flat().filter((k) => k != null && k !== false));
const errText = (e) => (typeof e === "string" ? e : e?.message || String(e));
const shortName = (m) => (m.name.includes(": ") ? m.name.split(": ").slice(1).join(": ") : m.name);
// Per-image prices are fractions of a cent, so keep up to 4 decimals (trailing zeros trimmed to 2).
const money = (n) => {
  let s = n.toFixed(n >= 1 ? 2 : n >= 0.1 ? 3 : 4);
  const dot = s.indexOf(".");
  while (s.length - dot - 1 > 2 && s.endsWith("0")) s = s.slice(0, -1);
  return "$" + s;
};
const fmtSecs = (s) => (s >= 60 ? `${Math.floor(s / 60)}m ${Math.round(s % 60)}s` : `${s.toFixed(s < 10 ? 1 : 0)}s`);
const fmtBytes = (b) => (b > 1 << 20 ? (b / (1 << 20)).toFixed(1) + " MB" : Math.max(1, Math.round(b / 1024)) + " KB");
const debounce = (fn, ms) => { let t; return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); }; };

function toast(msg, isErr, action) {
  const t = h("div", { class: "toast" + (isErr ? " err" : "") }, msg,
    action ? h("button", { class: "link", onclick: () => { t.remove(); action.fn(); } }, action.label) : null);
  $("toasts").append(t);
  setTimeout(() => t.remove(), isErr || action ? 7000 : 2600);
}

// ---------- state ----------
const S = {
  keys: [],
  models: [],
  byId: new Map(),
  images: [],
  jobs: new Map(), // job id -> job
  early: new Map(), // events that arrived before Generate() returned
  keyStatus: new Map(),
  refs: [], // {src, preview}
  filter: { q: "", fav: false, model: "", archived: false, folder: "" },
  folders: [], // {id, name}, sorted by name
  quality: { byId: new Map(), meta: {} }, // crowd-sourced image Elo by model id
  sel: new Set(), // selected image ids (always a subset of what's visible)
  anchor: null, // last clicked id, start of a shift-click range
  outputDir: "",
  prefs: { favorites: [], recents: [], selected: [], params: {}, prompt: "", thumb: 220 },
};

const savePrefs = debounce(() => go().SetPrefs(JSON.stringify(S.prefs)).catch(() => {}), 400);
const P = () => S.prefs;

function setModels(list) {
  S.models = list || [];
  S.byId = new Map(S.models.map((m) => [m.id, m]));
}

const activeKey = () => S.keys.find((k) => k.active);

// ---------- capabilities & params ----------
const PARAMS = [
  { key: "aspectRatio", cap: "aspect_ratio", label: "Aspect ratio", kind: "ratio" },
  { key: "resolution", cap: "resolution", label: "Resolution", kind: "seg" },
  { key: "quality", cap: "quality", label: "Quality", kind: "seg" },
  { key: "outputFormat", cap: "output_format", label: "Format", kind: "seg" },
  { key: "background", cap: "background", label: "Background", kind: "seg" },
  { key: "n", cap: "n", label: "Images per model", kind: "n" },
  { key: "compression", cap: "output_compression", label: "Compression", kind: "range" },
  { key: "seed", cap: "seed", label: "Seed", kind: "seed" },
  { key: "stream", cap: "stream", label: "Live preview", kind: "stream" },
];

const selectedModels = () => P().selected.map((id) => S.byId.get(id)).filter(Boolean);

// What the current model selection can do: union of capabilities across selected models.
function capsUnion() {
  const out = {};
  for (const m of selectedModels()) {
    if (m.supports_streaming) out.stream = { type: "boolean", values: [], min: 0, max: 0 };
    for (const [k, c] of Object.entries(m.supported_parameters || {})) {
      const u = (out[k] ||= { type: c.type, values: [], min: Infinity, max: 0 });
      for (const v of c.values || []) if (v !== "auto" && !u.values.includes(v)) u.values.push(v);
      u.min = Math.min(u.min, c.min ?? 0);
      u.max = Math.max(u.max, c.max ?? 0);
    }
  }
  return out;
}

const ratioVal = (r) => { const [a, b] = r.split(":").map(Number); return a / b; };

// Params with anything the selected models can't honour zeroed out.
function effectiveParams() {
  const caps = capsUnion(), p = P().params, out = {};
  if (caps.stream && p.stream !== false) out.stream = true;
  for (const d of PARAMS) {
    if (d.kind === "stream") continue;
    const c = caps[d.cap], v = p[d.key];
    if (!c || v == null || v === "") continue;
    if ((d.kind === "ratio" || d.kind === "seg") && !c.values.includes(v)) continue;
    out[d.key] = v;
  }
  if (out.n) out.n = Math.min(out.n, caps.n.max);
  return out;
}

function renderParams() {
  const box = $("params"), caps = capsUnion(), p = P().params;
  clear(box);
  const set = (k, v) => { p[k] = v; savePrefs(); renderParams(); };

  for (const d of PARAMS) {
    const c = caps[d.cap];
    if (!c) continue;
    let body;
    if (d.kind === "ratio" || d.kind === "seg") {
      let vals = [...c.values];
      if (d.kind === "ratio") vals.sort((a, b) => ratioVal(a) - ratioVal(b));
      else if (d.key === "resolution") vals.sort((a, b) => parseFloat(a) - parseFloat(b) || a.localeCompare(b));
      const cur = vals.includes(p[d.key]) ? p[d.key] : "";
      const opt = (v, label) => h("button", { class: v === cur ? "on" : "", type: "button", onclick: () => set(d.key, v) },
        d.kind === "ratio" && v ? ratioIcon(v) : null, label);
      body = h("div", { class: "seg" }, opt("", "Auto"), vals.map((v) => opt(v, v)));
    } else if (d.kind === "n") {
      const max = Math.max(1, c.max), cur = Math.min(p.n || 1, max);
      body = h("div", { class: "stepper" },
        h("input", { type: "range", min: 1, max, value: cur, oninput: (e) => { p.n = +e.target.value; e.target.nextSibling.textContent = p.n; savePrefs(); } }),
        h("span", {}, String(cur)));
      body.querySelector("input").addEventListener("change", () => renderParams());
    } else if (d.kind === "range") {
      const cur = p.compression ?? "";
      body = h("div", { class: "stepper" },
        h("input", { type: "number", min: c.min, max: c.max, placeholder: "auto", value: cur, onchange: (e) => set("compression", e.target.value === "" ? null : +e.target.value) }),
        h("span", { class: "hint" }, `${c.min}–${c.max} (jpeg/webp)`));
    } else if (d.kind === "stream") {
      const on = p.stream !== false, multi = (p.n || 1) > 1;
      body = h("label", { class: "check" },
        h("input", { type: "checkbox", checked: on, onchange: (e) => set("stream", e.target.checked) }),
        h("span", {}, on ? "Show partial renders while generating" : "Wait for the final image"),
        multi && on ? h("span", { class: "hint" }, " (single image only)") : null);
    } else if (d.kind === "seed") {
      body = h("div", { class: "stepper" },
        h("input", { type: "number", placeholder: "random", value: p.seed ?? "", onchange: (e) => set("seed", e.target.value === "" ? null : Math.trunc(+e.target.value)) }),
        h("button", { class: "btn sm", type: "button", onclick: () => set("seed", Math.floor(Math.random() * 2 ** 31)) }, "🎲"));
    }
    box.append(h("div", { class: "param" }, h("div", { class: "plabel" }, d.label), body));
  }
  if (selectedModels().length && !box.children.length) {
    box.append(h("div", { class: "hint" }, "This model has no adjustable settings."));
  }
}

function ratioIcon(r) {
  const [a, b] = r.split(":").map(Number), k = 12 / Math.max(a, b);
  return h("span", { class: "ar", style: `width:${Math.max(4, a * k)}px;height:${Math.max(4, b * k)}px` });
}

// ---------- selection / quick bar ----------
function selectModels(ids, { silent } = {}) {
  P().selected = ids;
  savePrefs();
  renderSelection();
  if (!silent && ids.length) noteRecent(ids[ids.length - 1]);
}

function noteRecent(id) {
  P().recents = [id, ...P().recents.filter((x) => x !== id)].slice(0, 12);
  savePrefs();
}

function toggleModel(id) {
  const sel = P().selected;
  selectModels(sel.includes(id) ? sel.filter((x) => x !== id) : [...sel, id]);
}

function renderSelection() {
  renderQuick();
  renderChips();
  renderParams();
  renderGo();
}

function renderChips() {
  const box = $("selected");
  clear(box);
  const sel = selectedModels();
  if (!sel.length) box.append(h("span", { class: "none" }, macText("No model chosen — press Ctrl+K")));
  for (const m of sel) {
    box.append(h("span", { class: "chip", title: m.id },
      h("span", {}, shortName(m)),
      h("button", { type: "button", "aria-label": "Remove " + m.name, onclick: () => toggleModel(m.id) }, "✕")));
  }
}

function renderQuick() {
  const box = $("quick");
  clear(box);
  const ids = [...P().favorites, ...P().recents.filter((r) => !P().favorites.includes(r)).slice(0, 4)]
    .filter((id) => S.byId.has(id));
  for (const id of ids) {
    const m = S.byId.get(id), on = P().selected.includes(id);
    box.append(h("button", {
      class: "pill" + (on ? " on" : ""), title: `${m.name}\nClick: use alone · ${MOD}/Shift-click: add to compare`,
      onclick: (e) => (e.ctrlKey || e.shiftKey || e.metaKey ? toggleModel(id) : selectModels([id])),
    }, (P().favorites.includes(id) ? "★ " : "") + shortName(m)));
  }
}

function renderGo() {
  const n = P().selected.length, b = $("go"), busy = [...S.jobs.values()].some((j) => j.state === "running" || j.state === "queued");
  b.textContent = n > 1 ? `Generate with ${n} models` : "Generate";
  b.append(h("kbd", { style: "color:inherit;opacity:.6" }, `${MOD} ↵`));
  b.disabled = false;
  b.title = busy ? "Jobs are running — you can queue more" : "";
}

// ---------- keys ----------
function renderKeyButton() {
  const k = activeKey(), btn = $("btnKey");
  clear(btn);
  if (!k) { btn.append(h("span", { class: "dot" }), "Connect OpenRouter"); return; }
  const st = S.keyStatus.get(k.id);
  const ex = expiryInfo(st?.expiresAt);
  put(btn, h("span", { class: "dot live" }), k.name, h("span", { class: "credit" }, creditText(st)),
    ex?.soon ? h("span", { class: "badge warn" }, ex.text) : null);
}

// "in 2h 5m" / "expired" / date for far-off expiries
function expiryInfo(iso) {
  if (!iso) return null;
  const ms = new Date(iso).getTime() - Date.now();
  if (Number.isNaN(ms)) return null;
  const mins = Math.round(ms / 60000), hrs = Math.floor(mins / 60);
  let text;
  if (ms <= 0) text = "expired";
  else if (mins < 60) text = `expires in ${mins}m`;
  else if (hrs < 48) text = `expires in ${hrs}h ${mins % 60}m`;
  else text = "expires " + new Date(iso).toLocaleDateString();
  return { text, soon: ms < 24 * 3600 * 1000 };
}

function creditText(st) {
  if (!st) return "";
  if (st.error) return "—";
  if (st.remaining != null) return money(st.remaining) + " left";
  return money(st.usage || 0) + " used";
}

async function loadKeyStatus(id) {
  const st = await go().KeyStatus(id);
  S.keyStatus.set(id, st);
  if (activeKey()?.id === id) renderKeyButton();
  return st;
}
const refreshActiveStatus = debounce(() => { const k = activeKey(); if (k) loadKeyStatus(k.id); }, 800);

function setKeys(keys) {
  S.keys = keys || [];
  renderKeyButton();
  if (!$("keyMenu").hidden) renderKeyMenu();
  renderEmpty();
}

let editingKey = null, armedKey = null;

function renderKeyMenu() {
  const m = $("keyMenu");
  clear(m);
  S.keys.forEach((k, i) => {
    const st = S.keyStatus.get(k.id);
    const nameEl = editingKey === k.id
      ? h("input", { type: "text", value: k.name, class: "rename", onclick: (e) => e.stopPropagation(),
          onkeydown: (e) => { e.stopPropagation(); if (e.key === "Enter") commitRename(k, e.target.value); else if (e.key === "Escape") { editingKey = null; renderKeyMenu(); } },
          onblur: (e) => commitRename(k, e.target.value) })
      : h("div", { class: "name" }, k.name);
    m.append(h("div", { class: "item" + (k.active ? " active" : ""), onclick: () => switchKey(k.id) },
      h("span", { class: "num" }, i < 9 ? String(i + 1) : ""),
      h("span", { class: "dot" + (k.active ? " live" : "") }),
      h("div", { class: "meta" },
        nameEl,
        h("div", { class: "sub" }, k.hint + (st ? " · " + (st.error ? st.error : [creditText(st), expiryInfo(st.expiresAt)?.text].filter(Boolean).join(" · ")) : " · …"))),
      h("button", { class: "mini", title: "Show key & details", onclick: (e) => { e.stopPropagation(); closeKeyMenu(); openKeyInfo(k); } }, "ⓘ"),
      h("button", { class: "mini", title: "Rename", onclick: (e) => { e.stopPropagation(); editingKey = k.id; renderKeyMenu(); m.querySelector(".rename")?.select(); } }, "✎"),
      h("button", { class: "mini danger" + (armedKey === k.id ? " armed" : ""), title: "Remove this key from the computer", onclick: (e) => { e.stopPropagation(); removeKey(k); } },
        armedKey === k.id ? "Remove?" : "✕")));
  });
  if (S.keys.length) m.append(h("div", { class: "sep" }));
  m.append(h("div", { class: "item", onclick: () => { closeKeyMenu(); openKeyDialog(); } }, h("span", { class: "num" }), "＋ Add key…"));
}

function openKeyMenu() {
  const m = $("keyMenu");
  m.hidden = false;
  renderKeyMenu();
  S.keys.forEach((k) => loadKeyStatus(k.id).then(() => { if (!m.hidden) renderKeyMenu(); }));
}
const closeKeyMenu = () => { $("keyMenu").hidden = true; };

async function switchKey(id) {
  closeKeyMenu();
  if (activeKey()?.id === id) return;
  try {
    setKeys(await go().SetActiveKey(id));
    toast(`Using key “${activeKey().name}”`);
    loadKeyStatus(id);
  } catch (e) { toast(errText(e), true); }
}

async function commitRename(k, name) {
  if (editingKey !== k.id) return;
  editingKey = null;
  name = name.trim();
  if (name && name !== k.name) {
    try { setKeys(await go().RenameKey(k.id, name)); return; } catch (e) { toast(errText(e), true); }
  }
  renderKeyMenu();
}

async function removeKey(k) {
  if (armedKey !== k.id) {
    armedKey = k.id;
    renderKeyMenu();
    setTimeout(() => { if (armedKey === k.id) { armedKey = null; if (!$("keyMenu").hidden) renderKeyMenu(); } }, 3000);
    return;
  }
  armedKey = null;
  try {
    setKeys(await go().RemoveKey(k.id));
    S.keyStatus.delete(k.id);
    const a = activeKey();
    if (a) loadKeyStatus(a.id);
  } catch (e) { toast(errText(e), true); }
}

// ---------- key details ----------
let infoKey = null, revealed = false;

function openKeyInfo(k) {
  infoKey = k;
  revealed = false;
  $("kiTitle").textContent = k.name;
  $("kiSecret").value = k.hint;
  $("kiReveal").textContent = "Show";
  $("kiError").hidden = true;
  clear($("kiInfo"));
  $("kiInfo").hidden = true;
  $("kiMore").textContent = "Show more info";
  $("dlgKeyInfo").showModal();
}

async function toggleReveal() {
  if (revealed) {
    revealed = false;
    $("kiSecret").value = infoKey.hint;
    $("kiReveal").textContent = "Show";
    return;
  }
  try {
    $("kiSecret").value = await go().RevealKey(infoKey.id);
    revealed = true;
    $("kiReveal").textContent = "Hide";
  } catch (e) { toast(errText(e), true); }
}

async function copyKey() {
  try {
    await navigator.clipboard.writeText(await go().RevealKey(infoKey.id));
    toast("Key copied to clipboard");
  } catch (e) { toast(errText(e), true); }
}

async function loadKeyInfo() {
  const btn = $("kiMore"), box = $("kiInfo");
  btn.disabled = true;
  btn.textContent = "Loading…";
  $("kiError").hidden = true;
  try {
    const i = await go().KeyDetails(infoKey.id);
    clear(box);
    const row = (k, v, cls) => v != null && v !== "" && box.append(h("dt", {}, k), h("dd", { class: cls || "" }, v));
    const ex = expiryInfo(i.expires_at);
    row("OpenRouter label", i.label);
    row("Expires", i.expires_at ? `${new Date(i.expires_at).toLocaleString()} (${ex.text})` : "Never", ex?.soon ? "warn" : "");
    row("Credit limit", i.limit != null ? money(i.limit) + (i.limit_reset ? ` / ${i.limit_reset}` : "") : "No limit");
    if (i.limit_remaining != null) row("Remaining", money(i.limit_remaining));
    row("Used (all time)", money(i.usage || 0));
    row("Used today", money(i.usage_daily || 0));
    row("Used this week", money(i.usage_weekly || 0));
    row("Used this month", money(i.usage_monthly || 0));
    if (i.byok_usage) row("BYOK usage", money(i.byok_usage));
    row("Free tier", i.is_free_tier ? "Yes" : "No");
    if (i.is_management_key) row("Type", "Management key");
    if (i.allowed_data_regions?.length) row("Data regions", i.allowed_data_regions.join(", "));
    box.hidden = false;
    S.keyStatus.delete(infoKey.id);
    loadKeyStatus(infoKey.id);
  } catch (e) {
    const m = errText(e);
    $("kiError").textContent = /401|rejected/i.test(m) ? "OpenRouter rejected this key — it may have expired or been revoked." : m;
    $("kiError").hidden = false;
  } finally {
    btn.disabled = false;
    btn.textContent = "Refresh info";
  }
}

// ---------- connect (OAuth) ----------
let connecting = false;

function showConnectPane(waiting) {
  $("connectPane").hidden = waiting;
  $("waitPane").hidden = !waiting;
}

async function startConnect() {
  if (!$("dlgKey").open) openKeyDialog();
  $("keyError").hidden = true;
  connecting = true;
  showConnectPane(true);
  try {
    const res = await go().ConnectOpenRouter();
    setKeys(res.keys);
    if ($("dlgKey").open) $("dlgKey").close();
    toast(`Connected! If an image fails with a credits message, add some at openrouter.ai/settings/credits`);
    loadKeyStatus(activeKey().id);
    refreshQuality(false);
  } catch (e) {
    const m = errText(e);
    if (m !== "cancelled") {
      $("keyError").textContent = m;
      $("keyError").hidden = false;
    }
  } finally {
    connecting = false;
    showConnectPane(false);
  }
}

function openKeyDialog() {
  showConnectPane(connecting);
  $("manual").open = false;
  $("keyName").value = "";
  $("keySecret").value = "";
  $("keySecret").type = "password";
  $("keyReveal").textContent = "Show";
  $("keyError").hidden = true;
  $("dlgKey").showModal();
  $("btnConnect").focus();
}

async function saveKey(fromEnv) {
  const btn = $("keySave");
  btn.disabled = true;
  btn.textContent = "Checking…";
  $("keyError").hidden = true;
  try {
    const res = fromEnv ? await go().AddKeyFromEnv() : await go().AddKey($("keyName").value, $("keySecret").value);
    setKeys(res.keys);
    $("dlgKey").close();
    if (res.warning) toast(res.warning, true);
    else toast(`Key “${activeKey().name}” added`);
    loadKeyStatus(activeKey().id);
    refreshQuality(false);
  } catch (e) {
    $("keyError").textContent = errText(e);
    $("keyError").hidden = false;
  } finally {
    btn.disabled = false;
    btn.textContent = "Save key";
  }
}

// ---------- quality & value ----------
function setQuality(q) {
  S.quality = { byId: new Map((q?.scores || []).map((x) => [x.modelId, x])), meta: q?.meta || {} };
  const has = S.quality.byId.size > 0;
  $("viewSeg").hidden = !has;
  for (const o of $("modelSort").options) if (o.value === "value" || o.value === "quality") o.disabled = !has;
  if (!has && ["value", "quality"].includes($("modelSort").value)) $("modelSort").value = "new";
  if (!has) showView("list");
  if ($("dlgModels").open) renderPalette();
}

const refreshQuality = (force) => go().RefreshQuality(!!force).then(setQuality).catch(() => {});

// Average billed cost per image for a model, from the user's own generations (the most trustworthy price).
function observedCost(id) {
  const xs = S.images.filter((m) => m.model === id && m.cost != null && m.cost > 0).map((m) => m.cost);
  return xs.length >= 2 ? { avg: xs.reduce((a, b) => a + b, 0) / xs.length, n: xs.length } : null;
}

// Models with both a rating and a usable price; `front` marks the Pareto frontier (nothing cheaper-and-better).
function valuePoints() {
  const pts = [];
  for (const m of S.models) {
    const q = S.quality.byId.get(m.id);
    if (!q) continue;
    const obs = observedCost(m.id);
    let x, lo, hi;
    if (obs) x = lo = hi = obs.avg;
    else if (m.price_min > 0) { lo = m.price_min; hi = m.price_max; x = Math.sqrt(lo * hi); }
    else continue;
    pts.push({ m, q, x, lo, hi, observed: !!obs, n: obs?.n || 0 });
  }
  for (const a of pts) a.front = !pts.some((b) => b !== a && b.x <= a.x && b.q.elo >= a.q.elo && (b.x < a.x || b.q.elo > a.q.elo));
  return pts;
}

function sortModels(list) {
  const mode = $("modelSort").value;
  if (mode === "new") return list.sort((a, b) => b.created - a.created);
  const pts = new Map(valuePoints().map((p) => [p.m.id, p]));
  const elo = (m) => S.quality.byId.get(m.id)?.elo ?? -Infinity;
  const price = (m) => pts.get(m.id)?.x ?? (m.price_min > 0 ? Math.sqrt(m.price_min * m.price_max) : Infinity);
  if (mode === "price") return list.sort((a, b) => price(a) - price(b) || b.created - a.created);
  if (mode === "quality") return list.sort((a, b) => elo(b) - elo(a) || b.created - a.created);
  const rank = (m) => (pts.get(m.id)?.front ? 0 : pts.has(m.id) ? 1 : 2);
  return list.sort((a, b) => rank(a) - rank(b) || (rank(a) === 0 ? price(a) - price(b) : elo(b) - elo(a)) || b.created - a.created);
}

let chartMode = false;

function showView(v) {
  chartMode = v === "chart" && S.quality.byId.size > 0;
  $("viewList").classList.toggle("on", !chartMode);
  $("viewChart").classList.toggle("on", chartMode);
  $("modelList").hidden = chartMode;
  $("valueView").hidden = !chartMode;
  $("modelSort").hidden = chartMode;
  $("modelSearch").hidden = chartMode;
  const foot = $("modelFoot");
  const cite = S.quality.meta?.citation || (S.quality.byId.size ? "Ratings: Design Arena image arena via OpenRouter." : "");
  foot.textContent = chartMode || S.quality.byId.size
    ? `${cite}${S.quality.meta?.asOf ? " As of " + new Date(S.quality.meta.asOf).toLocaleDateString() + "." : ""} Elo is a crowd-voted preference score, not a guarantee for your prompts.`
    : macText("Click a model to use it alone · tick boxes (or Ctrl/Shift-click) to compare several at once · ★ pins it to the top bar");
  if (chartMode) renderValueChart();
}

const SVGNS = "http://www.w3.org/2000/svg";
function svgEl(tag, attrs, ...kids) {
  const el = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v);
  }
  for (const kid of kids.flat()) if (kid != null) el.append(kid.nodeType ? kid : document.createTextNode(kid));
  return el;
}

function renderValueChart() {
  const box = $("valueView");
  clear(box);
  const pts = valuePoints();
  if (pts.length < 2) {
    box.append(h("div", { class: "empty" }, "Not enough models with both a rating and a per-image price to compare yet."));
    return;
  }
  const W = 720, H = 400, L = 62, R = 24, T = 16, B = 52;
  const xs = pts.flatMap((p) => [p.lo, p.hi]), es = pts.map((p) => p.q.elo);
  const x0 = Math.min(...xs) * 0.8, x1 = Math.max(...xs) * 1.3;
  const e0 = Math.floor((Math.min(...es) - 25) / 50) * 50, e1 = Math.ceil((Math.max(...es) + 25) / 50) * 50;
  const X = (v) => L + ((Math.log(v) - Math.log(x0)) / (Math.log(x1) - Math.log(x0))) * (W - L - R);
  const Y = (e) => H - B - ((e - e0) / (e1 - e0)) * (H - B - T);
  const svg = svgEl("svg", { viewBox: `0 0 ${W} ${H}`, role: "img", "aria-label": "Price per image against arena rating for each model" });

  const eStep = e1 - e0 > 300 ? 100 : 50;
  for (let e = e0; e <= e1; e += eStep) svg.append(svgEl("line", { class: "grid", x1: L, x2: W - R, y1: Y(e), y2: Y(e) }), svgEl("text", { x: L - 8, y: Y(e) + 4, "text-anchor": "end" }, String(e)));
  for (const t of [0.005, 0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1]) {
    if (t < x0 || t > x1) continue;
    svg.append(svgEl("line", { class: "grid", x1: X(t), x2: X(t), y1: T, y2: H - B }), svgEl("text", { x: X(t), y: H - B + 16, "text-anchor": "middle" }, money(t)));
  }
  svg.append(svgEl("line", { class: "axis", x1: L, x2: W - R, y1: H - B, y2: H - B }), svgEl("line", { class: "axis", x1: L, x2: L, y1: T, y2: H - B }),
    svgEl("text", { x: (L + W - R) / 2, y: H - 10, "text-anchor": "middle" }, "Price per image (log scale) — cheaper is left"),
    svgEl("text", { transform: `translate(14 ${(T + H - B) / 2}) rotate(-90)`, "text-anchor": "middle" }, "Arena rating (Elo) — better is up"));

  const front = pts.filter((p) => p.front).sort((a, b) => a.x - b.x);
  svg.append(svgEl("polyline", { fill: "none", stroke: "var(--ok)", "stroke-width": 2, "stroke-dasharray": "6 4", points: front.map((p) => `${X(p.x)},${Y(p.q.elo)}`).join(" ") }));

  const tip = h("div", { class: "vtip", hidden: true });
  const labels = [];
  const picked = new Set(P().selected);
  const showTip = (p, ev) => {
    const spd = p.q.avgMs ? `~${fmtSecs(p.q.avgMs / 1000)} typical` : null;
    clear(tip);
    put(tip, h("b", {}, p.m.name),
      h("div", {}, `Rating ${Math.round(p.q.elo)}${p.q.games ? ` from ${p.q.games} matches` : ""}`),
      h("div", {}, p.observed ? `You paid ~${money(p.x)}/img (${p.n} images)` : `List price ${p.m.price || money(p.x)}`),
      spd ? h("div", {}, spd) : null,
      h("div", { class: "hint" }, p.front ? "Best value: nothing is both cheaper and better" : "A cheaper or better model beats this on both"),
      h("div", { class: "hint" }, macText("Click to use · Ctrl/Shift-click to compare")));
    const r = box.getBoundingClientRect();
    tip.hidden = false;
    tip.style.left = Math.min(ev.clientX - r.left + 14, r.width - 270) + "px";
    tip.style.top = ev.clientY - r.top + box.scrollTop + 14 + "px";
  };
  for (const p of pts) {
    const y = Y(p.q.elo);
    if (p.hi > p.lo * 1.05) svg.append(svgEl("line", { x1: X(p.lo), x2: X(p.hi), y1: y, y2: y, stroke: p.front ? "var(--ok)" : "var(--faint)", "stroke-width": 2, opacity: 0.45 }));
    const thin = p.q.games > 0 && p.q.games < 30;
    const act = (e) => (e.ctrlKey || e.shiftKey || e.metaKey ? (toggleModel(p.m.id), renderValueChart()) : (selectModels([p.m.id]), $("dlgModels").close()));
    svg.append(svgEl("circle", {
      class: "dot", cx: X(p.x), cy: y, r: 7, tabindex: 0, role: "button", "aria-label": `${p.m.name}, rating ${Math.round(p.q.elo)}, ${money(p.x)} per image`,
      fill: p.front ? "var(--ok)" : "var(--faint)", "fill-opacity": thin ? 0.3 : 1,
      stroke: picked.has(p.m.id) ? "var(--accent)" : thin ? "var(--accent)" : "var(--bg)", "stroke-width": picked.has(p.m.id) ? 3.5 : 2,
      onmouseenter: (e) => showTip(p, e), onmousemove: (e) => showTip(p, e), onmouseleave: () => { tip.hidden = true; },
      onclick: act, onkeydown: (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); act(e); } },
    }));
    if (p.front || pts.length <= 10) labels.push({ p, x: X(p.x), y });
  }
  // Labels sit above-right of their dot (left near the edge) and step up a line when they would overlap.
  const placed = [];
  for (const { p, x, y } of labels.sort((a, b) => a.x - b.x)) {
    let name = shortName(p.m);
    if (name.length > 28) name = name.slice(0, 27) + "…";
    const w = name.length * 6.6, left = x + 11 + w > W - 4, lx = left ? x - 11 - w : x + 11;
    let ly = y - 9;
    for (let i = 0; i < 6 && placed.some((r) => Math.abs(r.y - ly) < 13 && lx < r.x + r.w && lx + w > r.x); i++) ly -= 13;
    placed.push({ x: lx, y: ly, w });
    svg.append(svgEl("text", { class: "lbl", x: left ? x - 11 : x + 11, y: ly, "text-anchor": left ? "end" : "start" }, name));
  }

  const plotted = new Set(pts.map((p) => p.m.id));
  const rated = [...S.quality.byId.keys()].filter((id) => S.byId.has(id));
  const unpriced = rated.filter((id) => !plotted.has(id)).length;
  const mid = front.length ? [front[0], front[Math.floor((front.length - 1) / 2)], front[front.length - 1]] : [];
  const picks = [...new Set(mid.map((p) => p.m.id))];
  box.append(
    h("div", { class: "vlegend" },
      h("span", {}, h("i", { style: "background:var(--ok)" }), "Best value (on the frontier)"),
      h("span", {}, h("i", { style: "background:var(--faint)" }), "Beaten on both price and rating"),
      h("span", {}, h("i", { style: "background:var(--accent-soft);border:2px solid var(--accent)" }), "Few matches so far"),
      h("div", { class: "spacer" }),
      picks.length > 1 ? h("button", { class: "btn sm", onclick: () => {
        selectModels(picks);
        $("dlgModels").close();
        toast(`Selected ${picks.length} best-value models — write a prompt and generate to compare them`);
      } }, `Compare ${picks.length} best-value models`) : null),
    svg, tip,
    h("div", { class: "vnote" }, `${pts.length} models plotted. Bars show a price range that depends on resolution or quality; prices you actually paid replace list prices.` +
      (unpriced ? ` ${unpriced} rated model${unpriced === 1 ? "" : "s"} can't be plotted: billed per token or by size, so there's no reliable per-image price yet (they appear as you generate with them).` : "")));
}

// ---------- model palette ----------
let cur = 0;

function paletteRows() {
  const q = $("modelSearch").value.trim().toLowerCase();
  let list = S.models.filter((m) => !q || `${m.name} ${m.id} ${m.description}`.toLowerCase().includes(q));
  if (q) return [{ rows: sortModels(list) }];
  if ($("modelSort").value !== "new") return [{ head: "All image models", rows: sortModels(list) }];
  const fav = P().favorites.map((id) => S.byId.get(id)).filter(Boolean);
  const rec = P().recents.map((id) => S.byId.get(id)).filter((m) => m && !fav.includes(m)).slice(0, 5);
  const used = new Set([...fav, ...rec]);
  const rest = sortModels(list.filter((m) => !used.has(m)));
  return [
    { head: "Favorites", rows: fav }, { head: "Recent", rows: rec }, { head: "All image models", rows: rest },
  ].filter((g) => g.rows.length);
}

function renderPalette() {
  const box = $("modelList");
  clear(box);
  S.frontier = new Set(valuePoints().filter((p) => p.front).map((p) => p.m.id));
  const groups = paletteRows(), flat = [];
  for (const g of groups) {
    if (g.head) box.append(h("div", { class: "ghead" }, g.head));
    for (const m of g.rows) { flat.push(m); box.append(modelRow(m, flat.length - 1)); }
  }
  if (!flat.length) box.append(h("div", { class: "empty" }, S.models.length ? "No models match." : "No models loaded yet — check your connection."));
  cur = Math.min(cur, Math.max(0, flat.length - 1));
  box.dataset.count = flat.length;
  markCurrent();
  box._flat = flat;
}

function modelRow(m, idx) {
  const sel = P().selected.includes(m.id), fav = P().favorites.includes(m.id);
  const caps = m.supported_parameters || {};
  const edit = (caps.input_references?.max || 0) > 0;
  return h("div", {
    class: "mrow" + (sel ? " sel" : ""), role: "option", "aria-selected": String(sel), "data-i": idx,
    onclick: (e) => {
      if (e.ctrlKey || e.shiftKey || e.metaKey) { toggleModel(m.id); renderPalette(); }
      else { selectModels([m.id]); $("dlgModels").close(); }
    },
  },
    h("span", { class: "tick", title: "Add to comparison", onclick: (e) => { e.stopPropagation(); toggleModel(m.id); renderPalette(); } }, sel ? "✓" : ""),
    h("div", { class: "info" }, h("div", { class: "nm" }, m.name), h("div", { class: "id" }, m.id)),
    h("div", { class: "tags" },
      S.frontier?.has(m.id) ? h("span", { class: "tag best", title: "Best value: no other model is both cheaper and rated higher" }, "Best value") : null,
      S.quality.byId.has(m.id) ? h("span", { class: "tag elo", title: `Arena rating from ${S.quality.byId.get(m.id).games || "?"} matches` }, "Elo " + Math.round(S.quality.byId.get(m.id).elo)) : null,
      edit ? h("span", { class: "tag", title: "Accepts reference images" }, "edit") : null,
      m.price ? h("span", { class: "tag price", title: "Approximate cost per image. A range means price depends on resolution or quality." }, m.price) : null),
    h("button", { class: "fav" + (fav ? " on" : ""), title: fav ? "Unpin" : "Pin to top bar", "aria-label": "Favorite",
      onclick: (e) => { e.stopPropagation(); toggleFav(m.id); renderPalette(); } }, fav ? "★" : "☆"));
}

function markCurrent() {
  const box = $("modelList");
  box.querySelectorAll(".mrow.cur").forEach((e) => e.classList.remove("cur"));
  const el = box.querySelector(`.mrow[data-i="${cur}"]`);
  if (el) { el.classList.add("cur"); el.scrollIntoView({ block: "nearest" }); }
}

function toggleFav(id) {
  const f = P().favorites;
  P().favorites = f.includes(id) ? f.filter((x) => x !== id) : [...f, id];
  savePrefs();
  renderQuick();
}

function openPalette() {
  const d = $("dlgModels");
  if (d.open) return;
  $("modelSearch").value = "";
  cur = 0;
  showView(chartMode ? "chart" : "list");
  renderPalette();
  d.showModal();
  $("modelSearch").focus();
}

// ---------- reference images ----------
async function fileToRef(file) {
  const bmp = await createImageBitmap(file);
  const big = Math.max(bmp.width, bmp.height) > 2048 || file.size > 1.5 * 1024 * 1024;
  let src;
  if (big) {
    const k = Math.min(1, 2048 / Math.max(bmp.width, bmp.height));
    const c = document.createElement("canvas");
    c.width = Math.round(bmp.width * k);
    c.height = Math.round(bmp.height * k);
    c.getContext("2d").drawImage(bmp, 0, 0, c.width, c.height);
    src = c.toDataURL("image/jpeg", 0.92);
  } else {
    src = await new Promise((res, rej) => { const r = new FileReader(); r.onload = () => res(r.result); r.onerror = rej; r.readAsDataURL(file); });
  }
  bmp.close();
  return { src, preview: src };
}

async function addFiles(files) {
  // Snapshot first: a drop's FileList is emptied once the event handler returns.
  for (const f of Array.from(files)) {
    if (!f.type.startsWith("image/")) continue;
    try { S.refs.push(await fileToRef(f)); } catch { toast(`Couldn't read ${f.name || "image"}`, true); }
  }
  renderRefs();
}

const addGalleryRef = (id) => addGalleryRefs([id]);

function addGalleryRefs(ids) {
  let added = 0;
  for (const id of ids) {
    if (S.refs.some((r) => r.src === "gallery:" + id)) continue;
    S.refs.push({ src: "gallery:" + id, preview: "/thumb/" + id });
    added++;
  }
  renderRefs();
  toast(added ? `Added ${added} reference image${added > 1 ? "s" : ""}` : "Already a reference image");
}

function renderRefs() {
  const box = $("refs");
  clear(box);
  if (!S.refs.length) box.append(h("span", { class: "none" }, "Drop, paste or add images to edit or restyle"));
  S.refs.forEach((r, i) => box.append(h("div", { class: "ref" },
    h("img", { src: r.preview, alt: "" }),
    h("button", { type: "button", "aria-label": "Remove reference", onclick: () => { S.refs.splice(i, 1); renderRefs(); } }, "✕"))));
  const max = Math.max(0, ...selectedModels().map((m) => m.supported_parameters?.input_references?.max || 0));
  $("promptHint").textContent = S.refs.length && !max ? "selected models ignore references" : "";
}

// ---------- generation ----------
async function generate() {
  const prompt = $("prompt").value.trim();
  if (!activeKey()) { openKeyDialog(); return; }
  if (!prompt) { toast("Write a prompt first", true); $("prompt").focus(); return; }
  if (!P().selected.length) { openPalette(); return; }
  const models = P().selected.slice();
  models.forEach(noteRecent);
  renderQuick();
  try {
    const batch = await go().Generate({ prompt, models, params: effectiveParams(), refs: S.refs.map((r) => r.src) });
    const input = { prompt, params: effectiveParams(), refs: S.refs.map((r) => r.src) };
    for (const j of batch.jobs) {
      S.jobs.set(j.id, { ...j, batch: batch.id, state: "queued", started: Date.now(), input });
      for (const u of S.early.get(j.id) || []) applyJob(u);
      S.early.delete(j.id);
    }
    renderGrid();
    renderGo();
  } catch (e) { toast(errText(e), true); }
}

function applyJob(u) {
  const j = S.jobs.get(u.id);
  if (!j) { (S.early.get(u.id) || S.early.set(u.id, []).get(u.id)).push(u); return; }
  if (u.state === "running" && j.state !== "running") j.started = Date.now();
  Object.assign(j, { state: u.state, error: u.error, notes: u.notes || j.notes, seconds: u.seconds });
  if (u.state === "done") {
    S.jobs.delete(u.id);
    S.images.unshift(...u.images.slice().reverse());
    if (u.notes?.length) toast(`${j.modelName}: ${u.notes.join(" · ")}`);
    refreshActiveStatus();
  } else if (u.state === "cancelled") {
    S.jobs.delete(u.id);
  }
  renderGrid();
  renderGo();
}

function retryJob(j) {
  S.jobs.delete(j.id);
  go().Generate({ ...j.input, models: [j.model] }).then((batch) => {
    for (const nj of batch.jobs) {
      S.jobs.set(nj.id, { ...nj, batch: batch.id, state: "queued", started: Date.now(), input: j.input });
      for (const u of S.early.get(nj.id) || []) applyJob(u);
      S.early.delete(nj.id);
    }
    renderGrid();
  }).catch((e) => toast(errText(e), true));
  renderGrid();
}

// ---------- gallery grid ----------
const els = new Map(); // key -> element

function filteredImages() {
  const { q, fav, model, folder } = S.filter, needle = q.trim().toLowerCase();
  return S.images.filter((m) =>
    !!m.archived === S.filter.archived && (!fav || m.favorite) && (!model || m.model === model) && (!folder || m.folder === folder) &&
    (!needle || `${m.prompt} ${m.modelName} ${m.model}`.toLowerCase().includes(needle)));
}

function jobCard(j) {
  let el = els.get("job:" + j.id);
  if (!el) {
    el = h("div", { class: "card job" });
    els.set("job:" + j.id, el);
  }
  const live = j.pv && j.state === "running";
  el.className = "card job " + j.state + (j.state === "error" ? " err" : "") + (live ? " live" : "");
  el.style.setProperty("--r", live && j.pvRatio ? j.pvRatio.toFixed(4) : aspectOf(j.input?.params?.aspectRatio));
  clear(el);
  if (live) {
    el.append(h("img", { class: "pv", src: j.pv, alt: "", draggable: false }),
      h("div", { class: "pvbar" },
        h("div", {}, h("b", {}, j.modelName), h("span", { "data-job": j.id, "data-live": "1" }, elapsed(j))),
        h("button", { class: "btn sm", onclick: () => go().CancelJobs([j.id]) }, "Cancel")));
    return el;
  }
  el.append(h("div", { class: "mname" }, j.modelName));
  if (j.state === "error") {
    el.append(h("div", { class: "status", title: j.error }, j.error),
      h("div", { class: "acts" },
        h("button", { class: "btn sm", onclick: () => retryJob(j) }, "Retry"),
        h("button", { class: "btn sm", onclick: () => { S.jobs.delete(j.id); renderGrid(); } }, "Dismiss")));
  } else {
    put(el, h("div", { class: "spin" }),
      h("div", { class: "status", "data-job": j.id }, j.state === "queued" ? "Queued…" : elapsed(j)),
      j.notes?.length ? h("div", { class: "status", title: j.notes.join("\n") }, "⚠ " + j.notes[0]) : null,
      h("button", { class: "btn sm", onclick: () => go().CancelJobs([j.id]) }, "Cancel"));
  }
  return el;
}

const elapsed = (j) => `${j.pv ? "Streaming" : "Generating"}… ${fmtSecs((Date.now() - j.started) / 1000)}`;

function aspectOf(ar) {
  if (!ar) return 1;
  const r = ratioVal(ar);
  return Math.min(3, Math.max(0.4, r || 1));
}

function imageCard(m) {
  const key = "img:" + m.id;
  let el = els.get(key);
  if (!el) {
    el = h("div", { class: "card", tabindex: 0, role: "button", "aria-label": m.prompt,
      onclick: (e) => (e.shiftKey || e.ctrlKey || e.metaKey || S.sel.size ? selectClick(m.id, e) : openViewer(m.id)),
      onkeydown: (e) => {
        if (e.key === "Enter") openViewer(m.id);
        else if (e.key === " ") { e.preventDefault(); selectClick(m.id, e); }
        else if (e.key === "ContextMenu" || (e.key === "F10" && e.shiftKey)) {
          e.preventDefault();
          const r = e.currentTarget.getBoundingClientRect();
          showImageMenu(m.id, r.left + r.width / 2, r.top + r.height / 2);
        }
      },
      oncontextmenu: (e) => { e.preventDefault(); showImageMenu(m.id, e.clientX, e.clientY); } },
      h("button", { class: "pick", type: "button", tabindex: -1, "aria-label": "Select", onclick: (e) => { e.stopPropagation(); selectClick(m.id, e); } }, "✓"),
      h("img", { src: "/thumb/" + m.id, loading: "lazy", decoding: "async", alt: m.prompt, draggable: false }),
      h("div", { class: "shade" }, h("b", {}, m.modelName), h("i", {}, (m.seconds ? fmtSecs(m.seconds) + " · " : "") + m.prompt)),
      h("button", { class: "star", "aria-label": "Favorite", onclick: (e) => { e.stopPropagation(); toggleImageFav(m.id); } }, "★"));
    els.set(key, el);
  }
  const r = m.width && m.height ? m.width / m.height : 1;
  el.style.setProperty("--r", Math.min(3, Math.max(0.4, r)).toFixed(4));
  el.querySelector(".star").className = "star" + (m.favorite ? " on" : "");
  el.classList.toggle("sel", S.sel.has(m.id));
  return el;
}

function renderGrid() {
  const grid = $("grid"), want = [];
  const jobs = S.filter.archived ? [] : [...S.jobs.values()].reverse();
  for (const j of jobs) want.push(jobCard(j));
  const imgs = filteredImages();
  for (const m of imgs) want.push(imageCard(m));
  // reconcile in place so existing <img> elements (and scroll position) survive
  want.forEach((el, i) => { if (grid.children[i] !== el) grid.insertBefore(el, grid.children[i] || null); });
  while (grid.children.length > want.length) grid.lastElementChild.remove();
  const live = new Set(want);
  for (const [k, el] of els) if (!live.has(el)) els.delete(k);

  const archivedN = S.images.filter((m) => m.archived).length, pool = S.filter.archived ? archivedN : S.images.length - archivedN;
  $("count").textContent = S.filter.archived ? `${imgs.length} archived` : imgs.length === pool ? `${imgs.length} images` : `${imgs.length} of ${pool}`;
  $("archivedToggle").textContent = archivedN ? `Archived (${archivedN})` : "Archived";
  renderFolders();
  renderEmpty();
  renderSelBar();
  refreshModelFilter();
}

function renderEmpty() {
  const e = $("empty");
  const noKey = !S.keys.length && !S.filter.archived;
  const none = S.filter.archived ? !S.images.some((m) => m.archived) : !S.images.length && !S.jobs.size;
  e.hidden = !(noKey || none || (!filteredImages().length && (S.filter.archived || !S.jobs.size)));
  if (e.hidden) return;
  clear(e);
  if (S.filter.archived && none) {
    e.append(h("h2", {}, "Nothing archived"),
      h("p", {}, "Select images and choose Archive to hide them from the main view without deleting them."));
  } else if (noKey) {
    e.append(h("div", { class: "penny" }), h("h2", {}, "Welcome to Pennyframe"),
      h("p", {}, "Connect your OpenRouter account to start making images — one click, and OpenRouter walks you through signing in. You can connect several keys and switch between them with Alt+1…9."),
      h("button", { class: "btn primary big", onclick: startConnect }, "Connect with OpenRouter"));
  } else if (none) {
    e.append(h("div", { class: "penny" }), h("h2", {}, "Nothing here yet"),
      h("p", {}, macText("Pick a model, write a prompt and press Ctrl+Enter. Select several models to compare them side by side.")));
  } else if (S.filter.folder && !S.images.some((m) => m.folder === S.filter.folder && !!m.archived === S.filter.archived)) {
    e.append(h("h2", {}, folderById(S.filter.folder)?.name || "Folder"),
      h("p", {}, "This folder is empty. Right-click images and choose Move to folder to put them here."));
  } else {
    e.append(h("p", {}, "No images match your filters."));
  }
}

function refreshModelFilter() {
  const sel = $("modelFilter"), seen = new Map();
  for (const m of S.images) seen.set(m.model, m.modelName);
  const sig = [...seen.keys()].sort().join("|") + "#" + S.filter.model;
  if (sel.dataset.sig === sig) return;
  sel.dataset.sig = sig;
  clear(sel);
  sel.append(h("option", { value: "" }, "All models"));
  for (const [id, name] of [...seen].sort((a, b) => a[1].localeCompare(b[1]))) sel.append(h("option", { value: id }, name));
  sel.value = S.filter.model;
}

// ---------- selection ----------
const visibleIds = () => filteredImages().map((m) => m.id);

function selectClick(id, e) {
  const ids = visibleIds();
  if (e.shiftKey && S.anchor && ids.includes(S.anchor)) {
    const a = ids.indexOf(S.anchor), b = ids.indexOf(id);
    if (!(e.ctrlKey || e.metaKey)) S.sel = new Set();
    ids.slice(Math.min(a, b), Math.max(a, b) + 1).forEach((x) => S.sel.add(x));
  } else {
    S.sel.has(id) ? S.sel.delete(id) : S.sel.add(id);
    S.anchor = id;
  }
  paintSelection();
}

function clearSelection() {
  if (!S.sel.size) return;
  S.sel = new Set();
  S.anchor = null;
  paintSelection();
}

function paintSelection() {
  const vis = new Set(visibleIds());
  for (const id of [...S.sel]) if (!vis.has(id)) S.sel.delete(id);
  for (const m of S.images) els.get("img:" + m.id)?.classList.toggle("sel", S.sel.has(m.id));
  $("grid").classList.toggle("selecting", S.sel.size > 0);
  renderSelBar();
}

let deleteArmed = null;

function renderSelBar() {
  const bar = $("selbar"), n = S.sel.size;
  bar.hidden = n === 0;
  document.querySelector(".toolbar:not(.selbar)").hidden = n > 0;
  if (!n) { deleteArmed = null; return; }
  const ids = [...S.sel], picked = S.images.filter((m) => S.sel.has(m.id));
  const allFav = picked.every((m) => m.favorite), archivedView = S.filter.archived, total = visibleIds().length;
  clear(bar);
  put(bar,
    h("b", {}, `${n} selected`),
    n < total ? h("button", { class: "btn sm", onclick: selectAll }, `Select all ${total}`) : null,
    h("div", { class: "spacer" }),
    h("button", { class: "btn", onclick: () => bulkFavorite(ids, !allFav) }, allFav ? "☆ Unfavorite" : "★ Favorite"),
    h("button", { class: "btn", onclick: () => bulkArchive(ids, !archivedView) }, archivedView ? "Unarchive" : "Archive"),
    h("button", { class: "btn", "aria-haspopup": "menu", onclick: (e) => { const r = e.currentTarget.getBoundingClientRect(); showMenu(folderMenuItems(ids), r.left, r.bottom + 4, { targets: ids }); } }, "Folder ▾"),
    deleteArmed
      ? [h("span", { class: "warn-text" }, `Delete ${n} permanently?`),
         h("button", { class: "btn danger", onclick: () => bulkDelete(ids) }, "Yes, delete"),
         h("button", { class: "btn", onclick: () => { deleteArmed = null; renderSelBar(); } }, "Keep")]
      : h("button", { class: "btn danger", onclick: armDelete }, "Delete"),
    h("button", { class: "btn icon", title: "Clear selection (Esc)", "aria-label": "Clear selection", onclick: clearSelection }, "✕"));
}

function selectAll() {
  S.sel = new Set(visibleIds());
  paintSelection();
}

function armDelete() {
  const token = (deleteArmed = {});
  renderSelBar();
  setTimeout(() => { if (deleteArmed === token) { deleteArmed = null; renderSelBar(); } }, 6000);
}

async function bulkFavorite(ids, fav) {
  try {
    await go().SetFavorites(ids, fav);
    const set = new Set(ids);
    S.images.forEach((m) => { if (set.has(m.id)) m.favorite = fav; });
    renderGrid();
    if ($("dlgView").open) renderViewer();
  } catch (e) { toast(errText(e), true); }
}

async function bulkArchive(ids, archived) {
  try {
    await go().ArchiveImages(ids, archived);
    const set = new Set(ids);
    S.images.forEach((m) => { if (set.has(m.id)) m.archived = archived; });
    S.sel = new Set();
    renderGrid();
    paintSelection();
    const word = ids.length === 1 ? "image" : "images";
    toast(`${archived ? "Archived" : "Moved back"} ${ids.length} ${word}`, false, { label: "Undo", fn: () => bulkArchive(ids, !archived) });
  } catch (e) { toast(errText(e), true); }
}

async function bulkDelete(ids) {
  deleteArmed = null;
  try {
    await go().DeleteImages(ids);
    const set = new Set(ids);
    S.images = S.images.filter((m) => !set.has(m.id));
    S.sel = new Set();
    renderGrid();
    paintSelection();
    toast(`Deleted ${ids.length} ${ids.length === 1 ? "image" : "images"}`);
  } catch (e) {
    // some may have been removed already; reload what is really on disk
    try { S.images = (await go().Bootstrap()).images || []; } catch { /* keep list */ }
    S.sel = new Set();
    renderGrid();
    toast(errText(e), true);
  }
}

async function toggleImageFav(id) {
  const m = S.images.find((x) => x.id === id);
  if (!m) return;
  m.favorite = !m.favorite;
  try { await go().SetFavorite(id, m.favorite); } catch (e) { m.favorite = !m.favorite; toast(errText(e), true); }
  renderGrid();
  if ($("dlgView").open) renderViewer();
}

// ---------- viewer ----------
let viewId = null;

function openViewer(id) {
  viewId = id;
  renderViewer();
  if (!$("dlgView").open) $("dlgView").showModal();
}

function viewList() { return filteredImages(); }

function stepViewer(d) {
  const list = viewList(), i = list.findIndex((m) => m.id === viewId);
  if (i < 0) return;
  const n = list[i + d];
  if (n) { viewId = n.id; renderViewer(); }
}

const PARAM_LABELS = { aspect_ratio: "Aspect", resolution: "Resolution", quality: "Quality", output_format: "Format", background: "Background", n: "Images", seed: "Seed", output_compression: "Compression" };

function renderViewer() {
  const m = S.images.find((x) => x.id === viewId);
  if (!m) { $("dlgView").close(); return; }
  $("lbImg").src = "/img/" + m.id;
  $("lbImg").alt = m.prompt;
  const list = viewList(), i = list.findIndex((x) => x.id === m.id);
  $("lbPrev").hidden = i <= 0;
  $("lbNext").hidden = i < 0 || i >= list.length - 1;

  const kv = [];
  const add = (k, v) => v != null && v !== "" && kv.push(h("dt", {}, k), h("dd", {}, v));
  add("Model", m.model);
  add("Size", m.width ? `${m.width} × ${m.height}` : null);
  add("File", fmtBytes(m.bytes));
  add("Generation time", m.seconds ? fmtSecs(m.seconds) : null);
  add("Cost", m.cost != null ? money(m.cost) : "not reported");
  if (m.costCount > 1 && m.costTotal != null) add("Request total", `${money(m.costTotal)} for ${m.costCount} images`);
  add("Key", m.keyName);
  add("Folder", folderById(m.folder)?.name);
  add("References", m.refs || null);
  for (const [k, v] of Object.entries(m.params || {})) add(PARAM_LABELS[k] || k, String(v));
  add("Created", new Date(m.createdAt).toLocaleString());

  const side = $("lbSide");
  clear(side);
  side.append(
    h("h3", {}, m.modelName),
    h("div", { class: "stats" },
      m.seconds ? h("span", { class: "stat" }, h("b", {}, fmtSecs(m.seconds)), "to generate") : null,
      h("span", { class: "stat", title: m.cost == null ? "OpenRouter didn't report a cost for this request" : "Billed by OpenRouter, split evenly across the images in the request" },
        h("b", {}, m.cost != null ? money(m.cost) : "—"), m.costCount > 1 ? "per image" : "cost"),
      m.width ? h("span", { class: "stat" }, h("b", {}, `${m.width}×${m.height}`), "pixels") : null),
    h("div", { class: "prompt" }, m.prompt),
    h("dl", { class: "kv" }, kv),
    h("div", { class: "lb-actions" },
      h("button", { class: "btn", onclick: () => reuse(m) }, "Reuse settings"),
      h("button", { class: "btn", onclick: () => { addGalleryRef(m.id); } }, "Use as reference"),
      h("button", { class: "btn", onclick: () => toggleImageFav(m.id) }, m.favorite ? "★ Favorited" : "☆ Favorite"),
      h("button", { class: "btn", onclick: () => copyImage(m.id) }, "Copy image"),
      h("button", { class: "btn", onclick: () => navigator.clipboard.writeText(m.prompt).then(() => toast("Prompt copied")) }, "Copy prompt"),
      h("button", { class: "btn", onclick: () => viewerArchive(m) }, m.archived ? "Unarchive" : "Archive"),
      h("button", { class: "btn", "aria-haspopup": "menu", onclick: (e) => { const r = e.currentTarget.getBoundingClientRect(); showMenu(folderMenuItems([m.id]), r.left, r.bottom + 4, { targets: [m.id] }); } }, "Folder ▾"),
      h("button", { class: "btn", onclick: () => saveAs(m.id) }, "Save as…"),
      h("button", { class: "btn", onclick: () => go().RevealImage(m.id) }, IS_MAC ? "Show in Finder" : "Show in Explorer"),
      deleteButton(m.id)));
}

async function viewerArchive(m) {
  const list = viewList(), i = list.findIndex((x) => x.id === m.id);
  const next = list[i + 1] || list[i - 1];
  await bulkArchive([m.id], !m.archived);
  if (next) { viewId = next.id; renderViewer(); } else $("dlgView").close();
}

function deleteButton(id) {
  let armed = false, timer;
  const b = h("button", { class: "btn danger", onclick: async () => {
    if (!armed) { armed = true; b.textContent = "Click again to delete"; timer = setTimeout(() => { armed = false; b.textContent = "Delete"; }, 3000); return; }
    clearTimeout(timer);
    deleteInViewer(id);
  } }, "Delete");
  return b;
}

async function deleteInViewer(id) {
  try {
    await go().DeleteImages([id]);
    const list = viewList(), i = list.findIndex((x) => x.id === id);
    const next = list[i + 1] || list[i - 1];
    S.images = S.images.filter((x) => x.id !== id);
    renderGrid();
    if (next) { viewId = next.id; renderViewer(); } else $("dlgView").close();
  } catch (e) { toast(errText(e), true); }
}

function reuse(m) {
  $("prompt").value = m.prompt;
  P().prompt = m.prompt;
  if (S.byId.has(m.model)) selectModels([m.model]);
  const map = { aspect_ratio: "aspectRatio", resolution: "resolution", quality: "quality", output_format: "outputFormat", background: "background", n: "n", seed: "seed", output_compression: "compression" };
  P().params = {};
  for (const [k, v] of Object.entries(m.params || {})) if (map[k]) P().params[map[k]] = v;
  savePrefs();
  renderParams();
  $("dlgView").close();
  toast("Settings loaded");
  $("prompt").focus();
}

async function copyImage(id) {
  try {
    const img = new Image();
    img.src = "/img/" + id;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.naturalWidth; c.height = img.naturalHeight;
    c.getContext("2d").drawImage(img, 0, 0);
    const blob = await new Promise((r) => c.toBlob(r, "image/png"));
    await navigator.clipboard.write([new ClipboardItem({ "image/png": blob })]);
    toast("Image copied");
  } catch (e) { toast("Couldn't copy image: " + errText(e), true); }
}

async function saveAs(id) {
  try {
    const p = await go().SaveImageAs(id);
    if (p) toast("Saved to " + p);
  } catch (e) { toast(errText(e), true); }
}

// ---------- context menu ----------
let menu = null; // {root, targets, prevFocus, onKey}

function closeMenu() {
  if (!menu) return;
  const { root, targets, prevFocus, onKey } = menu;
  menu = null;
  window.removeEventListener("keydown", onKey, true);
  window.removeEventListener("resize", closeMenu);
  window.removeEventListener("blur", closeMenu);
  root.remove();
  for (const id of targets) els.get("img:" + id)?.classList.remove("ctx-target");
  if (prevFocus?.isConnected) prevFocus.focus({ preventScroll: true });
}

// Drops empty entries and stray separators so callers can build lists with `cond && item`.
function cleanItems(items) {
  const out = [];
  for (const it of items.filter(Boolean)) {
    if (it === "-" && (!out.length || out[out.length - 1] === "-")) continue;
    out.push(it);
  }
  while (out[out.length - 1] === "-") out.pop();
  return out;
}

// items: "-" | {label, onclick, sub, checked, danger, arm, disabled}. `arm` is the label of a click-again confirmation.
function showMenu(items, x, y, { targets = [] } = {}) {
  closeMenu();
  items = cleanItems(items);
  if (!items.length) return;
  // Inside an open lightbox the menu must be a descendant of the modal dialog, or the page treats it as inert.
  const host = $("dlgView").open ? $("dlgView") : document.body;
  const root = h("div", {
    class: "ctx-root",
    onpointerdown: (e) => { if (e.target === root) closeMenu(); },
    oncontextmenu: (e) => { e.preventDefault(); if (e.target === root) closeMenu(); },
  });

  const place = (el, left, top, flipLeft) => {
    el.style.left = "0px";
    el.style.top = "0px";
    const w = el.offsetWidth, hgt = el.offsetHeight;
    if (left + w > innerWidth - 8) left = flipLeft ?? innerWidth - w - 8;
    el.style.left = Math.max(8, left) + "px";
    el.style.top = Math.max(8, Math.min(top, innerHeight - hgt - 8)) + "px";
  };

  const buildPanel = (list) => {
    const panel = h("div", { class: "ctx", role: "menu", tabindex: "-1" });
    let sub = null;
    const closeSub = () => { if (sub) { sub.panel.remove(); sub.btn.setAttribute("aria-expanded", "false"); sub = null; } };
    const openSub = (btn, subItems) => {
      if (sub?.btn === btn) return null;
      closeSub();
      const p = buildPanel(cleanItems(subItems));
      root.append(p);
      const r = btn.getBoundingClientRect();
      place(p, r.right - 4, r.top - 6, r.left - p.offsetWidth + 4);
      sub = { panel: p, btn };
      btn.setAttribute("aria-expanded", "true");
      return p;
    };
    for (const it of list) {
      if (it === "-") { panel.append(h("div", { class: "ctx-sep", role: "separator" })); continue; }
      let armed = false;
      const lbl = h("span", { class: "lbl" }, it.label);
      const btn = h("button", {
        type: "button", role: "menuitem", class: "ctx-item" + (it.danger ? " danger" : ""), disabled: it.disabled,
        "aria-haspopup": it.sub ? "menu" : false,
        onmouseenter: () => { if (it.sub) openSub(btn, it.sub); else closeSub(); },
        onclick: () => {
          if (it.sub) { (openSub(btn, it.sub) || sub.panel).querySelector(".ctx-item")?.focus(); return; }
          if (it.arm && !armed) { armed = true; lbl.textContent = it.arm; btn.classList.add("armed"); return; }
          closeMenu();
          it.onclick?.();
        },
      }, h("span", { class: "ck", "aria-hidden": "true" }, it.checked ? "✓" : ""), lbl, it.sub ? h("span", { class: "arr", "aria-hidden": "true" }, "›") : null);
      panel.append(btn);
    }
    panel._sub = () => sub;
    panel._closeSub = closeSub;
    return panel;
  };

  const onKey = (e) => {
    const panels = [...root.querySelectorAll(".ctx")];
    const main = panels[0], sub = main._sub();
    const cur = panels.find((p) => p.contains(document.activeElement)) || main;
    const btns = [...cur.querySelectorAll(".ctx-item:not(:disabled)")];
    const i = btns.indexOf(document.activeElement);
    const backToParent = () => { const b = sub.btn; main._closeSub(); b.focus(); };
    switch (e.key) {
      case "Escape":
        e.preventDefault(); e.stopPropagation();
        if (sub) backToParent(); else closeMenu();
        return;
      case "ArrowDown": case "ArrowUp": {
        e.preventDefault(); e.stopPropagation();
        if (!btns.length) return;
        const d = e.key === "ArrowDown" ? 1 : -1;
        btns[i < 0 ? (d > 0 ? 0 : btns.length - 1) : (i + d + btns.length) % btns.length].focus();
        return;
      }
      case "Home": case "End":
        e.preventDefault(); e.stopPropagation();
        btns[e.key === "Home" ? 0 : btns.length - 1]?.focus();
        return;
      case "ArrowRight":
        e.preventDefault(); e.stopPropagation();
        if (document.activeElement?.getAttribute("aria-haspopup") === "menu") document.activeElement.click();
        return;
      case "ArrowLeft":
        e.preventDefault(); e.stopPropagation();
        if (sub && cur !== main) backToParent();
        return;
      case "Tab": closeMenu(); return;
      case "Enter": case " ": return; // let the focused item activate
      default: e.stopPropagation(); // keep viewer shortcuts from firing under an open menu
    }
  };

  host.append(root);
  const panel = buildPanel(items);
  root.append(panel);
  place(panel, x, y, x - panel.offsetWidth);
  menu = { root, targets, prevFocus: document.activeElement, onKey };
  for (const id of targets) els.get("img:" + id)?.classList.add("ctx-target");
  window.addEventListener("keydown", onKey, true);
  window.addEventListener("resize", closeMenu);
  window.addEventListener("blur", closeMenu);
  panel.focus({ preventScroll: true });
}

// A right-click on a selected image acts on the whole selection; on any other image, on just that one.
function showImageMenu(id, x, y, viewer = false) {
  const ids = !viewer && S.sel.has(id) ? [...S.sel] : [id];
  showMenu(imageMenuItems(ids, viewer), x, y, { targets: ids });
}

function imageMenuItems(ids, viewer) {
  const picked = ids.map((id) => S.images.find((m) => m.id === id)).filter(Boolean);
  if (!picked.length) return [];
  const n = picked.length, one = n === 1 ? picked[0] : null;
  const allFav = picked.every((m) => m.favorite), allArch = picked.every((m) => m.archived);
  const noun = n === 1 ? "image" : `${n} images`;
  return [
    one && !viewer && { label: "Open", onclick: () => openViewer(one.id) },
    one && { label: "Copy image", onclick: () => copyImage(one.id) },
    one && { label: "Copy prompt", onclick: () => navigator.clipboard.writeText(one.prompt).then(() => toast("Prompt copied")) },
    "-",
    { label: allFav ? "Remove from favorites" : "Add to favorites", onclick: () => bulkFavorite(ids, !allFav) },
    { label: allArch ? "Unarchive" : "Archive", onclick: () => (viewer ? viewerArchive(one) : bulkArchive(ids, !allArch)) },
    { label: "Move to folder", sub: folderMenuItems(ids) },
    "-",
    { label: "Use as reference", onclick: () => addGalleryRefs(ids) },
    one && { label: "Reuse settings", onclick: () => reuse(one) },
    one && { label: "Save as…", onclick: () => saveAs(one.id) },
    one && { label: IS_MAC ? "Show in Finder" : "Show in Explorer", onclick: () => go().RevealImage(one.id) },
    "-",
    { label: `Delete ${noun}…`, danger: true, arm: `Click again to delete ${noun}`, onclick: () => (viewer ? deleteInViewer(one.id) : bulkDelete(ids)) },
  ];
}

// ---------- folders ----------
const folderById = (id) => (id ? S.folders.find((f) => f.id === id) : undefined);
const sortFolders = () => S.folders.sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }));

function folderMenuItems(ids) {
  const picked = ids.map((id) => S.images.find((m) => m.id === id)).filter(Boolean);
  const first = picked[0]?.folder || "", same = picked.every((m) => (m.folder || "") === first);
  const items = S.folders.map((f) => ({ label: f.name, checked: same && first === f.id, onclick: () => moveToFolder(ids, f.id) }));
  if (items.length) items.push("-");
  items.push({ label: "New folder…", onclick: () => createFolder(ids) });
  if (picked.some((m) => folderById(m.folder))) items.push({ label: "Remove from folder", onclick: () => moveToFolder(ids, "") });
  return items;
}

async function resyncGallery() {
  try {
    const b = await go().Bootstrap();
    S.images = b.images || [];
    S.folders = b.folders || [];
  } catch { /* keep what we have */ }
  renderGrid();
  paintSelection();
}

async function moveToFolder(ids, folderId, quiet) {
  const set = new Set(ids);
  const changed = S.images.filter((m) => set.has(m.id) && (m.folder || "") !== folderId);
  if (!changed.length) { if (!quiet) toast("Already there"); return; }
  const prev = new Map(changed.map((m) => [m.id, m.folder || ""]));
  const list = viewList(), vi = list.findIndex((m) => m.id === viewId), neighbor = list[vi + 1] || list[vi - 1];
  try {
    await go().MoveToFolder(changed.map((m) => m.id), folderId);
  } catch (e) {
    toast(errText(e), true);
    await resyncGallery();
    return;
  }
  changed.forEach((m) => { m.folder = folderId || undefined; });
  renderGrid();
  paintSelection();
  if ($("dlgView").open) {
    if (viewList().some((m) => m.id === viewId)) renderViewer();
    else if (neighbor && viewList().some((m) => m.id === neighbor.id)) { viewId = neighbor.id; renderViewer(); } else $("dlgView").close();
  }
  if (quiet) return;
  const word = changed.length === 1 ? "image" : "images";
  const msg = folderId ? `Moved ${changed.length} ${word} to “${folderById(folderId)?.name || "folder"}”` : `Took ${changed.length} ${word} out of their folder`;
  toast(msg, false, { label: "Undo", fn: () => undoMove(prev) });
}

async function undoMove(prev) {
  const groups = new Map();
  for (const [id, f] of prev) (groups.get(f) || groups.set(f, []).get(f)).push(id);
  for (const [f, ids] of groups) await moveToFolder(ids, f, true);
}

async function createFolder(ids) {
  const f = await nameFlow("New folder", "", "Create", (n) => go().CreateFolder(n));
  if (!f) return;
  S.folders.push(f);
  sortFolders();
  if (ids?.length) await moveToFolder(ids, f.id); else renderGrid();
}

async function renameFolder(f) {
  const name = await nameFlow("Rename folder", f.name, "Rename", async (n) => { await go().RenameFolder(f.id, n); return n; });
  if (name == null) return;
  const cur = folderById(f.id);
  if (cur) cur.name = name.replace(/\s+/g, " ").trim();
  sortFolders();
  renderGrid();
  if ($("dlgView").open) renderViewer();
}

async function deleteFolder(f) {
  try { await go().DeleteFolder(f.id); } catch (e) { toast(errText(e), true); await resyncGallery(); return; }
  S.folders = S.folders.filter((x) => x.id !== f.id);
  S.images.forEach((m) => { if (m.folder === f.id) m.folder = undefined; });
  if (S.filter.folder === f.id) S.filter.folder = "";
  renderGrid();
  paintSelection();
  toast(`Deleted folder “${f.name}”. Its images are still in your gallery.`);
}

function openFolder(id) {
  if (S.filter.folder === id) return;
  S.filter.folder = id;
  S.sel = new Set();
  renderGrid();
  paintSelection();
  $("scroll").scrollTop = 0;
}

function folderMenu(f, x, y) {
  showMenu([
    { label: "Open", onclick: () => openFolder(f.id) },
    { label: "Rename…", onclick: () => renameFolder(f) },
    "-",
    { label: "Delete folder…", danger: true, arm: "Click again to delete folder", onclick: () => deleteFolder(f) },
  ], x, y);
}

let foldersSig = "";

function renderFolders() {
  if (S.filter.folder && !folderById(S.filter.folder)) S.filter.folder = "";
  const counts = new Map();
  for (const m of S.images) if (!m.archived && m.folder) counts.set(m.folder, (counts.get(m.folder) || 0) + 1);
  const sig = S.filter.folder + "|" + S.folders.map((f) => `${f.id}:${f.name}:${counts.get(f.id) || 0}`).join("|");
  if (sig === foldersSig) return;
  foldersSig = sig;
  const box = $("folders");
  clear(box);
  const all = !S.filter.folder;
  put(box, h("button", { class: "pill" + (all ? " on" : ""), "aria-pressed": all, onclick: () => openFolder("") }, "All"));
  for (const f of S.folders) {
    const on = S.filter.folder === f.id;
    const more = (e) => { const r = e.currentTarget.getBoundingClientRect(); folderMenu(f, r.left, r.bottom + 4); };
    put(box, h("span", { class: "fgroup" },
      h("button", {
        class: "pill" + (on ? " on" : ""), "aria-pressed": on, title: "Right-click to rename or delete",
        onclick: () => openFolder(f.id),
        oncontextmenu: (e) => { e.preventDefault(); folderMenu(f, e.clientX, e.clientY); },
        onkeydown: (e) => { if (e.key === "ContextMenu" || (e.key === "F10" && e.shiftKey)) { e.preventDefault(); more(e); } },
      }, f.name, h("span", { class: "n" }, String(counts.get(f.id) || 0))),
      on ? h("button", { class: "more", "aria-label": "Folder options", "aria-haspopup": "menu", onclick: more }, "⋯") : null));
  }
  put(box, h("button", { class: "pill add", onclick: () => createFolder(null) }, "＋ New folder"));
}

// Resolves to the entered name, or null if cancelled. `submit` may throw to show its message and ask again.
async function nameFlow(title, initial, okLabel, submit) {
  let err = "", val = initial;
  for (;;) {
    const name = await askName(title, val, okLabel, err);
    if (name == null) return null;
    try { return await submit(name); } catch (e) { err = errText(e); val = name; }
  }
}

function askName(title, value, okLabel, error) {
  const dlg = $("dlgName"), input = $("nameInput");
  $("nameTitle").textContent = title;
  $("nameOk").textContent = okLabel;
  $("nameError").textContent = error || "";
  $("nameError").hidden = !error;
  input.value = value;
  dlg.returnValue = "";
  return new Promise((resolve) => {
    dlg.addEventListener("close", () => resolve(dlg.returnValue === "ok" ? input.value : null), { once: true });
    dlg.showModal();
    input.select();
  });
}

// ---------- updates ----------
let pendingUpdate = null;
const DAY = 24 * 3600 * 1000;

function showUpdateBadge(info) {
  pendingUpdate = info;
  const b = $("btnUpdate");
  b.hidden = !(info?.available && P().skipVersion !== info.latest);
  if (!b.hidden) b.textContent = `Update ${info.latest}`;
}

// Quiet background check: failures (offline, rate limit) are ignored.
async function maybeCheckUpdate() {
  if (P().autoUpdate === false || Date.now() - (P().lastUpdateCheck || 0) < DAY) return;
  try {
    const info = await go().CheckForUpdate();
    P().lastUpdateCheck = Date.now();
    savePrefs();
    showUpdateBadge(info);
  } catch { /* try again next launch */ }
}

function openUpdateDialog(info) {
  $("upTitle").textContent = `Pennyframe ${info.latest} is available`;
  $("upSub").textContent = `You have ${info.current}. Your images, settings and keys are kept.`;
  $("upNotes").textContent = info.notes || "";
  $("upNotes").hidden = !info.notes;
  const cmd = !!info.command, dl = !cmd;
  $("upCmd").hidden = !cmd;
  $("upDl").hidden = !dl;
  if (cmd) {
    $("upCmdText").textContent = info.command;
    $("upCmdHint").textContent = info.method === "scoop"
      ? "Quit Pennyframe first (Scoop won't update a running app), then run this in PowerShell and open Pennyframe again."
      : "Quit Pennyframe first, then run this in Terminal and open Pennyframe again.";
  } else {
    $("upDownload").textContent = info.method === "installer" ? "Download the new installer" : "Download update";
    $("upDlHint").textContent = info.method === "installer"
      ? "Close Pennyframe, run the installer and it replaces this version."
      : info.download && /\.zip$/.test(info.download) ? "Open the zip and drag the app into Applications, replacing the old one." : "Opens the download in your browser.";
  }
  $("upDownload").onclick = () => rt().BrowserOpenURL(info.download || info.page);
  $("upCopy").onclick = () => navigator.clipboard.writeText(info.command).then(() => toast("Command copied"));
  $("upNotesLink").onclick = () => rt().BrowserOpenURL(info.page);
  $("upSkip").onclick = () => { P().skipVersion = info.latest; savePrefs(); showUpdateBadge(info); $("dlgUpdate").close(); };
  $("upLater").onclick = () => $("dlgUpdate").close();
  $("dlgUpdate").showModal();
}

async function checkUpdateNow() {
  const btn = $("updateCheck");
  btn.disabled = true;
  $("updateStatus").textContent = "Checking…";
  try {
    const info = await go().CheckForUpdate();
    P().lastUpdateCheck = Date.now();
    savePrefs();
    if (info.available) {
      P().skipVersion = null; // asked explicitly, so don't hide it
      showUpdateBadge(info);
      $("updateStatus").textContent = `Version ${info.latest} is available.`;
      $("dlgSettings").close();
      openUpdateDialog(info);
    } else {
      $("updateStatus").textContent = `You're up to date (${info.current}).`;
    }
  } catch (e) {
    $("updateStatus").textContent = "Couldn't check: " + errText(e);
  } finally {
    btn.disabled = false;
  }
}

// ---------- settings ----------
function openSettings() {
  $("outDir").textContent = S.outputDir;
  $("modelStats").textContent = `${S.models.length} image models cached`;
  go().Version().then((v) => { $("appVersion").textContent = "Pennyframe " + v; });
  $("autoUpdate").checked = P().autoUpdate !== false;
  $("updateStatus").textContent = "";
  $("dlgSettings").showModal();
}

async function refreshModels(force) {
  try {
    setModels(await go().RefreshModels(force));
    afterModelsChanged();
    if (force) toast(`${S.models.length} models loaded`);
  } catch (e) { toast(errText(e), true); }
}

function afterModelsChanged() {
  // Drop selections that no longer exist; seed a first-run default.
  if (S.models.length) {
    P().selected = P().selected.filter((id) => S.byId.has(id));
    if (!P().selected.length) P().selected = [S.models[0].id];
  }
  renderSelection();
  renderRefs();
  if ($("dlgModels").open) renderPalette();
}

// ---------- wiring ----------
function wire() {
  $("btnModels").onclick = $("btnPick").onclick = openPalette;
  $("modelsDone").onclick = () => $("dlgModels").close();
  $("modelSort").onchange = () => { cur = 0; renderPalette(); };
  $("viewList").onclick = () => showView("list");
  $("viewChart").onclick = () => showView("chart");
  $("modelSearch").addEventListener("input", () => { cur = 0; renderPalette(); });
  $("modelSearch").addEventListener("keydown", (e) => {
    const flat = $("modelList")._flat || [];
    if (e.key === "ArrowDown") { cur = Math.min(flat.length - 1, cur + 1); markCurrent(); e.preventDefault(); }
    else if (e.key === "ArrowUp") { cur = Math.max(0, cur - 1); markCurrent(); e.preventDefault(); }
    else if (e.key === "Enter" && flat[cur]) {
      e.preventDefault();
      if (e.ctrlKey || e.shiftKey) { toggleModel(flat[cur].id); renderPalette(); }
      else { selectModels([flat[cur].id]); $("dlgModels").close(); }
    }
  });
  $("dlgModels").addEventListener("click", (e) => { if (e.target === $("dlgModels")) $("dlgModels").close(); });

  $("btnKey").onclick = (e) => { e.stopPropagation(); $("keyMenu").hidden ? (S.keys.length ? openKeyMenu() : openKeyDialog()) : closeKeyMenu(); };
  document.addEventListener("click", (e) => { if (!e.target.closest(".keyswitch")) closeKeyMenu(); });
  $("keyForm").addEventListener("submit", (e) => { e.preventDefault(); saveKey(false); });
  $("keyCancel").onclick = () => $("dlgKey").close();
  $("btnConnect").onclick = startConnect;
  $("btnConnectCancel").onclick = () => go().CancelConnect();
  $("btnReopen").onclick = () => go().ReopenConnect().catch((e) => toast(errText(e), true));
  $("dlgKey").addEventListener("close", () => { if (connecting) go().CancelConnect(); });
  $("linkCredits").onclick = (e) => { e.preventDefault(); rt().BrowserOpenURL("https://openrouter.ai/settings/credits"); };
  $("keyReveal").onclick = () => {
    const i = $("keySecret"), show = i.type === "password";
    i.type = show ? "text" : "password";
    $("keyReveal").textContent = show ? "Hide" : "Show";
  };
  $("keyEnv").onclick = () => saveKey(true);
  $("kiReveal").onclick = toggleReveal;
  $("kiCopy").onclick = copyKey;
  $("kiMore").onclick = loadKeyInfo;
  $("kiClose").onclick = () => $("dlgKeyInfo").close();
  $("dlgKeyInfo").addEventListener("close", () => { revealed = false; $("kiSecret").value = ""; }); // don't leave the secret in the DOM
  

  $("btnAddKey").onclick = openKeyDialog;
  $("btnSettings").onclick = openSettings;
  $("settingsClose").onclick = () => $("dlgSettings").close();
  $("outOpen").onclick = () => go().OpenOutputDir();
  $("outChange").onclick = async () => {
    try {
      const b = await go().ChooseOutputDir();
      if (!b) return;
      S.images = b.images || [];
      S.folders = b.folders || [];
      S.filter.folder = "";
      S.outputDir = b.outputDir;
      $("outDir").textContent = S.outputDir;
      renderGrid();
    } catch (e) { toast(errText(e), true); }
  };
  $("btnUpdate").onclick = () => pendingUpdate && openUpdateDialog(pendingUpdate);
  $("updateCheck").onclick = checkUpdateNow;
  $("autoUpdate").onchange = (e) => { P().autoUpdate = e.target.checked; savePrefs(); };
  $("modelsRefresh").onclick = async () => { await refreshModels(true); $("modelStats").textContent = `${S.models.length} image models cached`; };

  $("go").onclick = generate;
  $("prompt").addEventListener("input", (e) => { P().prompt = e.target.value; savePrefs(); });
  $("prompt").addEventListener("paste", (e) => {
    const files = [...(e.clipboardData?.files || [])].filter((f) => f.type.startsWith("image/"));
    if (files.length) { e.preventDefault(); addFiles(files); }
  });
  $("btnAddRef").onclick = () => $("refFile").click();
  $("refFile").onchange = async (e) => { await addFiles(e.target.files); e.target.value = ""; };

  const compose = $("compose");
  ["dragenter", "dragover"].forEach((t) => compose.addEventListener(t, (e) => { if (e.dataTransfer?.types?.includes("Files")) { e.preventDefault(); $("refs").classList.add("drop"); } }));
  ["dragleave", "drop"].forEach((t) => compose.addEventListener(t, () => $("refs").classList.remove("drop")));
  compose.addEventListener("drop", (e) => { e.preventDefault(); addFiles(e.dataTransfer.files); });

  $("search").addEventListener("input", debounce((e) => { S.filter.q = e.target.value; renderGrid(); paintSelection(); }, 120));
  $("modelFilter").onchange = (e) => { S.filter.model = e.target.value; renderGrid(); paintSelection(); };
  $("favOnly").onclick = (e) => {
    S.filter.fav = !S.filter.fav;
    e.currentTarget.setAttribute("aria-pressed", S.filter.fav);
    renderGrid();
    paintSelection();
  };
  $("archivedToggle").onclick = (e) => {
    S.filter.archived = !S.filter.archived;
    e.currentTarget.setAttribute("aria-pressed", S.filter.archived);
    S.sel = new Set();
    renderGrid();
    paintSelection();
  };
  $("thumbSize").addEventListener("input", (e) => {
    document.documentElement.style.setProperty("--thumb", e.target.value + "px");
    P().thumb = +e.target.value;
    savePrefs();
  });

  $("lbClose").onclick = () => $("dlgView").close();
  $("lbPrev").onclick = () => stepViewer(-1);
  $("lbNext").onclick = () => stepViewer(1);
  $("lbStage").addEventListener("click", (e) => { if (e.target === $("lbStage")) $("dlgView").close(); });
  $("lbStage").addEventListener("contextmenu", (e) => {
    if (e.target.closest("button")) return;
    e.preventDefault();
    showImageMenu(viewId, e.clientX, e.clientY, true);
  });
  $("nameCancel").onclick = () => $("dlgName").close("cancel");

  document.addEventListener("keydown", (e) => {
    const mod = e.ctrlKey || e.metaKey;
    if (mod && !e.shiftKey && e.key.toLowerCase() === "k") { e.preventDefault(); $("dlgModels").open ? $("dlgModels").close() : openPalette(); return; }
    if (mod && e.key === "Enter" && !document.querySelector("dialog[open]")) { e.preventDefault(); generate(); return; }
    if (mod && e.shiftKey && e.key.toLowerCase() === "k") {
      e.preventDefault();
      S.keys.length ? ($("keyMenu").hidden ? openKeyMenu() : closeKeyMenu()) : openKeyDialog();
      return;
    }
    if (e.altKey && /^[1-9]$/.test(e.key)) {
      const k = S.keys[+e.key - 1];
      if (k) { e.preventDefault(); switchKey(k.id); }
      return;
    }
    if ($("dlgView").open && !/input|textarea/i.test(e.target.tagName)) {
      if (e.key === "ArrowLeft") stepViewer(-1);
      else if (e.key === "ArrowRight") stepViewer(1);
      else if (e.key.toLowerCase() === "f") toggleImageFav(viewId);
    }
    if (e.key === "Escape") closeKeyMenu();
    const typing = /input|textarea|select/i.test(e.target.tagName) || e.target.isContentEditable;
    if (!document.querySelector("dialog[open]") && !typing) {
      if (mod && e.key.toLowerCase() === "a") { e.preventDefault(); selectAll(); }
      else if (e.key === "Escape") clearSelection();
      else if (e.key === "Delete" && S.sel.size) { e.preventDefault(); armDelete(); }
    }
  });

  // live elapsed timers on running job cards
  setInterval(() => {
    for (const j of S.jobs.values()) {
      if (j.state !== "running") continue;
      const n = document.querySelector(`[data-job="${j.id}"]`);
      if (n) n.textContent = elapsed(j);
    }
  }, 500);
}

// ---------- boot ----------
const IS_MAC = /Mac/i.test(navigator.platform);
const MOD = IS_MAC ? "⌘" : "Ctrl";
const macText = (str) => (IS_MAC ? str.replace(/Ctrl\+?/g, "⌘") : str);
function macifyLabels() {
  if (!IS_MAC) return;
  for (const el of document.querySelectorAll("kbd, [title]")) {
    if (el.tagName === "KBD" && /^Ctrl/.test(el.textContent)) el.textContent = el.textContent.replace(/^Ctrl\s*/, "⌘");
    const t = el.getAttribute("title");
    if (t && t.includes("Ctrl")) el.setAttribute("title", t.replace(/Ctrl\+/g, "⌘").replace(/Ctrl/g, "⌘"));
  }
}

async function main() {
  while (!window.go?.main?.App) await new Promise((r) => setTimeout(r, 20)); // bindings arrive just after load
  wire();
  macifyLabels();
  const b = await go().Bootstrap();
  try { Object.assign(S.prefs, JSON.parse(b.prefs || "{}")); } catch { /* fresh prefs */ }
  P().params ||= {};
  setModels(b.models);
  S.images = b.images || [];
  S.folders = b.folders || [];
  S.outputDir = b.outputDir;
  $("prompt").value = P().prompt || "";
  $("thumbSize").value = P().thumb || 220;
  document.documentElement.style.setProperty("--thumb", $("thumbSize").value + "px");
  $("keyEnv").hidden = !b.envKey;
  setKeys(b.keys);
  setQuality(b.quality);
  afterModelsChanged();
  renderGrid();
  if (!S.keys.length) {
    setTimeout(() => { if (!S.keys.length) openKeyDialog(); }, 250);
  } else {
    loadKeyStatus(activeKey().id);
    refreshQuality(false);
  }

  rt().EventsOn("job", applyJob);
  rt().EventsOn("job:preview", ({ id, n }) => {
    const j = S.jobs.get(id);
    if (!j) return;
    // Preload so the card swaps images without a blank flash.
    const url = `/preview/${id}?n=${n}`, img = new Image();
    img.src = url;
    img.decode().then(() => {
      if (!S.jobs.has(id) || (j.pvN || 0) > n) return;
      j.pv = url; j.pvN = n; j.pvRatio = Math.min(3, Math.max(0.4, img.naturalWidth / img.naturalHeight));
      renderGrid();
    }).catch(() => {});
  });
  rt().EventsOn("model:price", ({ id, price, min, max }) => {
    const m = S.byId.get(id);
    if (!m) return;
    m.price = price; m.price_min = min; m.price_max = max;
    if ($("dlgModels").open) { renderPalette(); if (chartMode) renderValueChart(); }
  });
  refreshModels(false);
  setTimeout(maybeCheckUpdate, 4000); // after first paint, off the critical path
}

main().catch((e) => { document.body.prepend(h("pre", { style: "color:#f07178;padding:20px" }, "Startup failed: " + errText(e))); });
