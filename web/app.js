// quest-map web UI. Plain JavaScript, no build step. All data comes from the local server's /api.
"use strict";

const STATUSES = ["backlog", "todo", "doing", "review", "done"];
const $ = (id) => document.getElementById(id);
const DRAG_TYPE = "application/x-quest-map-task";

const state = {
  meta: null,
  projects: [],
  project: "",
  filter: "",
  group: "",
  tasks: [],
  stale: new Set(),
  selected: "",     // task id with keyboard focus
  open: null,       // task shown in the drawer
  dirty: false,     // drawer has unsaved edits
  preview: false,
  view: "board",    // board | milestones | releases
  releases: [],     // releases of the current project
  sort: "",         // "" (id), priority, updated, created, title
  layout: "board",  // board | table
  checked: new Set(), // table rows ticked for bulk edits
  mine: false,      // "My tasks": only tasks assigned to myName()
  fields: [],       // card fields; empty = DEFAULT_FIELDS
  myViews: [],      // views saved in this browser
  collapsedCols: new Set(),  // statuses shown as a narrow strip (per browser)
  allDone: false,            // the Done column shows every card, not only the latest DONE_SHOWN (per browser)
  collapsedLanes: new Set(), // laneKey() ("project/group:lane") of folded swimlanes (per browser)
};

// projectNow and suiteNow return the current project and its suite (spec §4.1).
const projectNow = () => state.projects.find((x) => x.slug === state.project) || null;
function suiteNow() {
  const p = projectNow();
  return (p && p.suite) || { words: { release: "release", releases: "releases", target: "target", item: "item", feedback: "feedback report", discipline: "discipline" },
    disciplines: (state.meta && state.meta.disciplines) || [], pipelines: [], release_targets: [] };
}
const cap = (s) => (s ? s[0].toUpperCase() + s.slice(1) : s);

// ---------- language (spec §4.4) ----------

// i18n holds the chosen language's catalog and the English one (the fallback for missing keys).
const i18n = { lang: "en", cat: {}, en: {}, langs: [], fallback: "en" };

// t returns the UI text for key in the chosen language, with {name} placeholders filled from vars. With a
// numeric vars.n it picks key.one / key.other (Intl.PluralRules). Missing: the language's own .other, then
// English, then the key itself.
function t(key, vars) {
  let s;
  if (vars && typeof vars.n === "number") {
    const form = key + "." + new Intl.PluralRules(i18n.lang).select(vars.n);
    s = i18n.cat[form] ?? i18n.cat[key + ".other"] ?? i18n.en[form] ?? i18n.en[key + ".other"];
  }
  s = s ?? i18n.cat[key] ?? i18n.en[key] ?? key;
  if (!vars) return s;
  return s.replace(/\{(\w+)\}/g, (m, n) => (n in vars ? (typeof vars[n] === "number" ? fmtNum(vars[n]) : String(vars[n])) : m));
}

// tx is t for code whose task variable is named t.
const tx = (key, vars) => t(key, vars);

const fmtUSD = (v) => new Intl.NumberFormat(i18n.lang, { style: "currency", currency: "USD" }).format(v);
const fmtNum = (n) => new Intl.NumberFormat(i18n.lang).format(n);
const fmtDate = (d) => new Date(d).toLocaleDateString(i18n.lang);
const fmtDateTime = (d) => new Date(d).toLocaleString(i18n.lang, { dateStyle: "medium", timeStyle: "short" });

// suiteLang returns the project's suite labels for the chosen language ([lang.<code>] in the suite file).
function suiteLang() {
  const l = suiteNow().lang || {};
  return l[i18n.lang] || l[i18n.lang.split("-")[0]] || {};
}

// label shows a key (a status, doer, priority, discipline, pipeline step, field, template or view) in the
// chosen language. The key itself is what is stored and filtered on; only its label changes (§4.4).
function label(kind, key) {
  if (!key) return key;
  const own = (suiteLang()[kind === "status" ? "statuses" : kind + "s"] || {})[key];
  if (own) return own;
  if (kind === "status" || kind === "doer" || kind === "priority") return t(`value.${kind}.${key}`);
  return key;
}

// A discipline is typed in a text box (other values stay valid): the box shows its label, and discKey turns what
// was typed back into the key, so a Japanese and an English browser write the same file.
const discShow = (d) => (d ? label("discipline", d) : d || "");
// st is the task's suite: the drawer may show a task of another project, whose suite names disciplines its own way.
function discKey(v, st = suiteNow()) {
  v = (v || "").trim();
  const lang = st.lang || {};
  const own = (lang[i18n.lang] || lang[i18n.lang.split("-")[0]] || {}).disciplines || {};
  return (st.disciplines || []).find((d) => d === v || (own[d] || d) === v) || v;
}

// word returns a suite word (release, target, item, feedback, discipline) in the chosen language.
const word = (name) => (suiteLang().wording || {})[name] || suiteNow().words[name] || name;

// wordVars fills {release}, {Release}, … in catalog text with the project's suite words.
function wordVars(extra) {
  const v = {};
  for (const n of ["release", "releases", "target", "item", "feedback", "discipline"]) { v[n] = word(n); v[cap(n)] = cap(word(n)); }
  return Object.assign(v, extra);
}

// fillParts puts catalog text into node with some {placeholders} replaced by nodes (a name in bold, a path in
// mono), so a translation can put them anywhere in its sentence. vars fill the plain-text placeholders.
function fillParts(node, key, parts, vars) {
  node.replaceChildren(...t(key, vars || {}).split(/(\{\w+\})/).filter((x) => x !== "")
    .map((x) => { const m = x.match(/^\{(\w+)\}$/); return m && parts[m[1]] ? parts[m[1]] : x; }));
}

// applyI18n fills elements marked data-i18n (text) and data-i18n-<attr> (title, placeholder, aria-label).
function applyI18n(root) {
  const vars = wordVars();
  (root || document).querySelectorAll("[data-i18n]").forEach((n) => { n.textContent = t(n.dataset.i18n, vars); });
  for (const attr of ["title", "placeholder", "aria-label", "label"]) {
    (root || document).querySelectorAll(`[data-i18n-${attr}]`).forEach((n) => n.setAttribute(attr, t(n.getAttribute("data-i18n-" + attr), vars)));
  }
  document.documentElement.lang = i18n.lang;
}

// pseudo turns English into en-XA: accented, about a third longer, bracketed – placeholders kept – so text
// that skips the catalog, and layouts that cannot take longer words, stand out.
function pseudo(s) {
  const acc = { a: "á", e: "é", i: "í", o: "ö", u: "ü", c: "ç", n: "ñ", A: "Å", E: "É", I: "Î", O: "Ø", U: "Û", s: "š", y: "ý" };
  const out = s.split(/(\{\w+\})/).map((p) => (p.startsWith("{") ? p : p.replace(/[aeioucnAEIOUsy]/g, (c) => acc[c]))).join("");
  return "⟦" + out + "~".repeat(Math.ceil(s.length / 3)) + "⟧";
}

// chosenLang is the language to show: the Settings choice, else the first browser language with a catalog,
// else the board's [ui] language, else English.
function chosenLang() {
  const have = new Set(i18n.langs.map((l) => l.code));
  const set = store("lang");
  if (set && have.has(set)) return set;
  for (const l of navigator.languages || []) {
    if (have.has(l)) return l;
    const base = l.split("-")[0];
    const match = i18n.langs.find((x) => x.code === base || (base === "zh" && x.code === "zh-TW" && /TW|HK|Hant/i.test(l)));
    if (match) return match.code;
  }
  return have.has(i18n.fallback) ? i18n.fallback : "en";
}

// loadLang loads the catalogs and applies the chosen language to the page.
async function loadLang() {
  const list = await api("GET", "/api/i18n");
  i18n.langs = [...list.languages, { code: "en-XA", name: "Pseudo (en-XA)" }];
  i18n.fallback = list.default || "en";
  i18n.en = await api("GET", "/api/i18n/en");
  i18n.lang = chosenLang();
  if (i18n.lang === "en") i18n.cat = i18n.en;
  else if (i18n.lang === "en-XA") i18n.cat = Object.fromEntries(Object.entries(i18n.en).map(([k, v]) => [k, pseudo(v)]));
  else i18n.cat = await api("GET", "/api/i18n/" + encodeURIComponent(i18n.lang));
  applyI18n();
}

// ---------- small helpers ----------

function store(key, value) {
  try {
    if (value === undefined) return localStorage.getItem("qm." + key);
    localStorage.setItem("qm." + key, value);
  } catch (_) { return null; }
}

function el(tag, attrs, ...children) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") n.className = v;
    else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    n.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return n;
}

let toastTimer;
function toast(msg, error) {
  const t = $("toast");
  t.textContent = msg;
  t.className = "toast" + (error ? " error" : "");
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, error ? 6000 : 2500);
}

async function api(method, path, body, raw) {
  const opts = { method, headers: {} };
  if (raw) { opts.body = raw.data; opts.headers["Content-Type"] = raw.type || "application/octet-stream"; }
  else if (body !== undefined) { opts.body = JSON.stringify(body); opts.headers["Content-Type"] = "application/json"; }
  const res = await fetch(path, opts);
  let data = null;
  const text = await res.text();
  try { data = text ? JSON.parse(text) : null; } catch (_) { data = { error: text }; }
  if (!res.ok) {
    const err = new Error(res.status === 401 ? t("err.session") : (data && data.error) || res.statusText);
    err.status = res.status;
    throw err;
  }
  return data;
}

function fmtTokens(n) {
  return new Intl.NumberFormat(i18n.lang, { notation: "compact", maximumFractionDigits: Math.abs(n) < 1e4 || Math.abs(n) >= 1e6 ? 1 : 0 }).format(n);
}

function fmtDuration(sec) {
  if (!sec) return "";
  const neg = sec < 0; sec = Math.abs(sec);
  const m = Math.round(sec / 60);
  const s = m === 0 ? t("dur.lt1m") : m < 60 ? t("dur.m", { m }) : m % 60 ? t("dur.hm", { h: Math.floor(m / 60), m: m % 60 }) : t("dur.h", { h: m / 60 });
  return (neg ? "-" : "") + s;
}

function spentText(tokens, seconds, partlySelf) {
  const parts = [];
  if (tokens) parts.push(t("spent.tok", { tok: fmtTokens(tokens) }));
  if (seconds) parts.push(fmtDuration(seconds));
  if (!parts.length) return "";
  return (partlySelf ? "~" : "") + parts.join(" · ");
}

const splitList = (s) => s.split(",").map((x) => x.trim()).filter(Boolean);

// A small, safe Markdown renderer for previews: everything is escaped first; only images, links, code,
// headings, emphasis, lists and pipe tables are turned back into markup. Relative attachment links point at /files/.
function renderMarkdown(src, project, id) {
  const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  const url = (u) => {
    const m = u.match(/^\.\.\/attachments\/([A-Za-z0-9]+-\d+)\/([^/]+)$/);
    if (m) return "/files/" + encodeURIComponent(project) + "/" + m[1] + "/" + m[2];
    if (/^https?:\/\//i.test(u)) return u;
    return "#";
  };
  // Plain text: escaped, then emphasis (which adds only fixed tags, never attributes).
  const text = (s) => esc(s)
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/(^|[^*])\*([^*]+)\*/g, "$1<em>$2</em>");
  // Code spans, images and links are matched on the raw line in one pass, so no pattern ever runs over markup
  // this function produced (which would let text break out of an attribute).
  const inline = (s) => {
    const re = /`([^`]+)`|!\[([^\]]*)\]\(([^)\s]+)\)|\[([^\]]+)\]\(([^)\s]+)\)/g;
    let out = "", last = 0, m;
    while ((m = re.exec(s))) {
      out += text(s.slice(last, m.index));
      if (m[1] !== undefined) out += "<code>" + esc(m[1]) + "</code>";
      else if (m[3] !== undefined) out += `<img alt="${esc(m[2])}" src="${esc(url(m[3]))}">`;
      // A link the page cannot open (file:///… paths agents write) shows its text, with the target on hover.
      else if (url(m[5]) === "#") out += `<span class="md-link" title="${esc(m[5])}">${text(m[4])}</span>`;
      else out += `<a href="${esc(url(m[5]))}" target="_blank" rel="noopener noreferrer">${text(m[4])}</a>`;
      last = re.lastIndex;
    }
    return out + text(s.slice(last));
  };
  const out = [];
  let inCode = false, list = false, rows = [];
  // Pipe tables: a header row, a |---| separator, then body rows. Rows without a separator stay paragraphs.
  const cells = (r) => r.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map((c) => c.trim());
  const flushTable = () => {
    if (rows.length >= 2 && /^[\s|:-]+$/.test(rows[1]) && rows[1].includes("-")) {
      const tr = (r, tag) => "<tr>" + cells(r).map((c) => `<${tag}>${inline(c)}</${tag}>`).join("") + "</tr>";
      out.push("<table class=\"md\"><thead>" + tr(rows[0], "th") + "</thead><tbody>" + rows.slice(2).map((r) => tr(r, "td")).join("") + "</tbody></table>");
    } else out.push(...rows.map((r) => "<p>" + inline(r) + "</p>"));
    rows = [];
  };
  for (const line of src.split("\n")) {
    if (!inCode && /^\s*\|.*\|\s*$/.test(line)) {
      if (list) { out.push("</ul>"); list = false; }
      rows.push(line);
      continue;
    }
    flushTable();
    if (line.startsWith("```")) {
      if (list) { out.push("</ul>"); list = false; }
      out.push(inCode ? "</code></pre>" : "<pre><code>");
      inCode = !inCode;
      continue;
    }
    if (inCode) { out.push(esc(line) + "\n"); continue; }
    const li = line.match(/^\s*[-*]\s+(.*)/);
    if (li) { if (!list) { out.push("<ul>"); list = true; } out.push("<li>" + inline(li[1]) + "</li>"); continue; }
    if (list) { out.push("</ul>"); list = false; }
    const h = line.match(/^(#{1,4})\s+(.*)/);
    if (h) out.push(`<h${h[1].length + 2}>${inline(h[2])}</h${h[1].length + 2}>`);
    else if (line.trim() === "") out.push("<br>");
    else out.push("<p>" + inline(line) + "</p>");
  }
  flushTable();
  if (list) out.push("</ul>");
  if (inCode) out.push("</code></pre>");
  return out.join("");
}

// ---------- loading ----------

async function loadMeta() {
  state.meta = await api("GET", "/api/meta");
  const dl = $("disciplines");
  dl.replaceChildren(...state.meta.disciplines.map((d) => el("option", { value: d })));
}

async function loadProjects() {
  state.projects = await api("GET", "/api/projects");
  const saved = store("project");
  state.project = state.projects.some((p) => p.slug === saved) ? saved : (state.projects[0] || {}).slug || "";
  renderSidebar();
  fillProjectLists();
}

// refreshCounts updates only the open-task counts in the sidebar after a task change.
async function refreshCounts() {
  const ps = await api("GET", "/api/projects");
  for (const p of ps) {
    const cur = state.projects.find((x) => x.slug === p.slug);
    if (cur) cur.open = p.open;
  }
  renderSidebar();
}

// ---------- sidebar ----------

function renderSidebar() {
  const menuFor = state.projectMenu;
  $("project-list").replaceChildren(...state.projects.map((p) => el("li", { class: "side-row" + (menuFor === p.slug ? " menu-open" : "") },
    el("button", {
      class: "side-item" + (p.slug === state.project ? " on" : ""), title: t("side.project_title", { name: p.name, key: p.key, open: p.open || 0 }),
      "aria-current": p.slug === state.project ? "page" : null,
      onclick: () => switchProject(p.slug),
      oncontextmenu: (e) => { e.preventDefault(); openProjectMenu(p.slug, e.currentTarget); },
    }, el("span", { class: "side-icon" }, p.key.slice(0, 3)), el("span", { class: "side-label" }, p.name),
    el("span", { class: "side-count" }, p.open || 0)),
    el("button", {
      class: "side-more", title: t("side.more", { name: p.name }), "aria-label": t("side.more", { name: p.name }), "aria-haspopup": "menu",
      "aria-expanded": String(menuFor === p.slug),
      onclick: (e) => { e.stopPropagation(); menuFor === p.slug ? closeProjectMenu() : openProjectMenu(p.slug, e.currentTarget); },
    }, "⋯"))));
}

// ---------- project menu (⋯ next to a project, or right-click it) ----------

function openProjectMenu(slug, anchor) {
  closeProjectMenu();
  const p = state.projects.find((x) => x.slug === slug);
  if (!p) return;
  state.projectMenu = slug;
  const items = [
    { label: t("menu.rename"), run: () => openRenameProject(p) },
    { label: t("menu.settings"), run: () => { switchProject(p.slug); openSettings(); } },
    { label: t("menu.delete"), danger: true, run: () => openDeleteProject(p) },
  ];
  const buttons = items.map((it) => el("button", {
    class: "menu-item" + (it.danger ? " danger" : ""), role: "menuitem",
    onclick: () => { closeProjectMenu(); it.run(); },
  }, it.label));
  const menu = el("div", { class: "menu", id: "project-menu", role: "menu", "aria-label": t("menu.aria", { name: p.name }) }, ...buttons);
  menu.addEventListener("keydown", (e) => {
    e.stopPropagation();
    const i = buttons.indexOf(document.activeElement);
    if (e.key === "Escape") {
      e.preventDefault(); closeProjectMenu();
      // The sidebar was drawn again, so focus its new ⋯ button for this project.
      const more = [...document.querySelectorAll(".side-more")].find((b) => b.closest(".side-row") === b.parentElement && b.getAttribute("aria-label") === t("side.more", { name: p.name }));
      if (more) more.focus();
    }
    else if (e.key === "ArrowDown") { e.preventDefault(); buttons[(i + 1) % buttons.length].focus(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); buttons[(i - 1 + buttons.length) % buttons.length].focus(); }
  });
  document.body.append(menu);
  const r = anchor.getBoundingClientRect();
  const w = menu.offsetWidth, h = menu.offsetHeight;
  menu.style.left = Math.max(8, Math.min(r.right + 4, innerWidth - w - 8)) + "px";
  menu.style.top = Math.max(8, Math.min(r.top, innerHeight - h - 8)) + "px";
  renderSidebar();
  buttons[0].focus();
  // A click anywhere else closes it (deferred, so the click that opened it does not).
  setTimeout(() => document.addEventListener("mousedown", projectMenuOutside, true), 0);
}

function projectMenuOutside(e) {
  const m = $("project-menu");
  if (m && !m.contains(e.target)) closeProjectMenu();
}

function closeProjectMenu() {
  document.removeEventListener("mousedown", projectMenuOutside, true);
  const m = $("project-menu");
  if (m) m.remove();
  if (state.projectMenu) { state.projectMenu = null; renderSidebar(); }
}

function openRenameProject(p) {
  state.renaming = p.slug;
  $("pname-input").value = p.name;
  fillParts($("pname-note"), "pname.note", { key: el("span", { class: "mono" }, p.key) });
  $("pname-dialog").showModal();
  $("pname-input").select();
}

async function saveProjectName(e) {
  e.preventDefault();
  const name = $("pname-input").value.trim();
  if (!name) { $("pname-input").focus(); return; }
  try {
    await api("PUT", `/api/projects/${encodeURIComponent(state.renaming)}/name`, { name });
    $("pname-dialog").close();
    await loadProjects();
    toast(t("project.renamed"));
  } catch (err) { toast(err.message, true); }
}

function openDeleteProject(p) {
  state.deleting = p;
  fillParts($("pdel-question"), "pdel.question", { name: el("strong", {}, `${p.name} (${p.key})`) }, { count: t("project.open_count", { open: p.open || 0 }) });
  fillParts($("pdel-note"), "pdel.note", { trash: el("span", { class: "mono" }, `.trash/${p.slug}-…`), projects: el("span", { class: "mono" }, "projects/") }); // i18n-ok: paths
  fillParts($("pdel-confirm"), "pdel.confirm", { key: el("span", { class: "mono" }, p.key) });
  $("pdel-input").value = "";
  $("pdel-ok").disabled = true;
  $("pdel-error").hidden = true;
  $("pdel-dialog").showModal();
  $("pdel-input").focus();
}

async function deleteProject(e) {
  e.preventDefault();
  const p = state.deleting;
  if (!p || $("pdel-input").value.trim().toUpperCase() !== p.key.toUpperCase()) return;
  $("pdel-ok").disabled = true;
  try {
    const out = await api("DELETE", `/api/projects/${encodeURIComponent(p.slug)}`, { confirm: p.key });
    $("pdel-dialog").close();
    await loadProjects();
    loadBoard(); renderView();
    toast(t("project.deleted", { name: p.name, folder: out.trashed }));
  } catch (err) {
    $("pdel-error").textContent = err.message;
    $("pdel-error").hidden = false;
    $("pdel-ok").disabled = false;
  }
}

// ---------- new project ----------

// suggestKey makes a task-id prefix from a name: the initials of several words (up to 4), else the first
// three letters, uppercase ASCII; a number is added while the key is taken. "" if the name has no ASCII letter.
function suggestKey(name) {
  const words = name.normalize("NFKD").replace(/[^A-Za-z0-9 ]+/g, " ").trim().split(/\s+/).filter((w) => /^[A-Za-z]/.test(w));
  if (!words.length) return "";
  let key = (words.length > 1 ? words.slice(0, 4).map((w) => w[0]).join("") : words[0].slice(0, 3)).toUpperCase();
  if (key.length < 2 && words[0].length > 1) key = words[0].slice(0, 3).toUpperCase();
  const taken = new Set(state.projects.map((p) => p.key));
  for (let i = 2, base = key; taken.has(key); i++) key = base + i;
  return key;
}

async function openNewProject() {
  const d = state.newProject = { keyEdited: false, slugEdited: false };
  for (const id of ["pnew-name", "pnew-key", "pnew-slug", "pnew-repo"]) $(id).value = "";
  $("pnew-error").hidden = true;
  $("pnew-dialog").showModal();
  $("pnew-name").focus();
  try {
    const suites = await api("GET", "/api/suites");
    if (state.newProject !== d) return;
    $("pnew-suite").replaceChildren(...suites.suites.map((s) => el("option", { value: s.name }, s.title || s.name)));
    $("pnew-suite").value = suites.default || (suites.suites[0] || {}).name || "";
  } catch (err) { toast(err.message, true); }
}

function newProjectInput(e) {
  const d = state.newProject;
  if (!d) return;
  if (e.target.id === "pnew-key") { d.keyEdited = true; e.target.value = e.target.value.toUpperCase(); }
  if (e.target.id === "pnew-slug") d.slugEdited = true;
  if (e.target.id === "pnew-name" && !d.keyEdited) $("pnew-key").value = suggestKey($("pnew-name").value);
  if (!d.slugEdited) $("pnew-slug").value = $("pnew-key").value.toLowerCase();
}

async function createProject(e) {
  e.preventDefault();
  const body = {
    name: $("pnew-name").value.trim(), key: $("pnew-key").value.trim(), slug: $("pnew-slug").value.trim(),
    suite: $("pnew-suite").value, repo: $("pnew-repo").value.trim(),
  };
  if (!body.key) { $("pnew-key").focus(); $("pnew-error").textContent = t("project.need_key"); $("pnew-error").hidden = false; return; }
  try {
    const out = await api("POST", "/api/projects", body);
    $("pnew-dialog").close();
    state.newProject = null;
    await loadProjects();
    if (state.project === out.slug) { loadBoard(); renderView(); } // the first project: already picked
    else switchProject(out.slug);
    toast(t("project.created", { name: out.name, key: out.key }));
  } catch (err) {
    $("pnew-error").textContent = err.message;
    $("pnew-error").hidden = false;
    loadProjects().catch(() => {}); // a project whose repo link failed was still created
  }
}

function wireProjectMenu() {
  $("project-new").addEventListener("click", openNewProject);
  $("pnew-form").addEventListener("submit", createProject);
  $("pnew-form").addEventListener("input", newProjectInput);
  $("pnew-cancel").addEventListener("click", () => { $("pnew-dialog").close(); state.newProject = null; });
  $("pname-form").addEventListener("submit", saveProjectName);
  $("pname-cancel").addEventListener("click", () => $("pname-dialog").close());
  $("pdel-form").addEventListener("submit", deleteProject);
  $("pdel-cancel").addEventListener("click", () => $("pdel-dialog").close());
  $("pdel-input").addEventListener("input", () => {
    $("pdel-ok").disabled = !state.deleting || $("pdel-input").value.trim().toUpperCase() !== state.deleting.key.toUpperCase();
  });
  window.addEventListener("resize", closeProjectMenu);
}

function switchProject(slug) {
  if (slug === state.project) return;
  state.project = slug; store("project", slug);
  renderSidebar(); fillProjectLists(); loadBoard(); renderView();
}

function setSidebar(collapsed) {
  $("sidebar").classList.toggle("collapsed", collapsed);
  const b = $("side-toggle");
  b.textContent = collapsed ? "»" : "«";
  b.title = t(collapsed ? "side.expand_key" : "side.collapse_key");
  b.setAttribute("aria-label", t(collapsed ? "side.expand" : "side.collapse"));
  b.setAttribute("aria-expanded", String(!collapsed));
  $("s-sidebar").checked = collapsed;
  store("sidebar", collapsed ? "collapsed" : "open");
}

function openSettings() {
  $("s-theme").value = store("theme") || "";
  const p = projectNow();
  $("s-project-title").textContent = t("settings.project", { name: p ? p.name : t("common.none") });
  $("s-archive").disabled = !p;
  $("s-archive").value = p ? p.archive_days || 0 : 0;
  archiveNote();
  $("s-workdir").disabled = !p;
  $("s-workdir").value = p && p.workdir_pinned ? p.workdir : "";
  workdirNote();
  fillParts($("s-css-note"), "settings.css", { path: el("span", { class: "mono" }, (state.meta && state.meta.custom_css) || t("settings.css_default")) });
  $("s-me").value = store("me") || "";
  $("s-me").placeholder = (state.meta && state.meta.user) || t("settings.me_placeholder");
  $("s-columns").replaceChildren(...STATUSES.map((s) => el("label", {},
    el("input", { type: "checkbox", checked: state.collapsedCols.has(s), onchange: () => toggleColumn(s) }), label("status", s))));
  renderLangPicker();
  renderPluginSettings();
  renderSyncSettings();
  $("settings-dialog").showModal();
}

function fillProjectLists() {
  const p = projectNow();
  const st = suiteNow();
  $("milestones").replaceChildren(...((p && p.milestones) || []).map((m) => el("option", { value: m.name })));
  $("disciplines").replaceChildren(...st.disciplines.map((d) => el("option", { value: label("discipline", d) })));
  $("d-status").replaceChildren(...STATUSES.map((s) => el("option", { value: s }, label("status", s))));
  for (const id of ["d-doer", "n-doer", "d-priority", "n-priority", "n-status"]) {
    for (const o of $(id).options) o.textContent = label(id.endsWith("doer") ? "doer" : id.endsWith("priority") ? "priority" : "status", o.value);
  }
  $("targets").replaceChildren(...(st.release_targets || []).map((d) => el("option", { value: d })));
  // Rebuilding the pipeline list must not change what the open drawer shows: a save would then send it.
  const prevPipeline = $("d-pipeline").value;
  $("d-pipeline").replaceChildren(el("option", { value: "" }, t("common.none")), ...st.pipelines.map((pl) => el("option", { value: pl.name }, t("pipeline.option", { name: label("pipeline", pl.name), n: pl.steps.length }))));
  if (state.open) {
    if (!state.dirty) fillDrawer(state.open);
    else setPipelineChoice(prevPipeline);
  }
  applyWords();
  loadReleases().catch(() => {});
  $("d-move-to").replaceChildren(...state.projects.filter((x) => x.slug !== state.project)
    .map((x) => el("option", { value: x.slug }, x.key)));
}

// ---------- search box: id jump and pills ----------

// idInSearch returns the task id the search box holds ("RPG-12", "rpg-12", or "#12" in the current project),
// or "" if it holds a filter. KEY-12 is a jump only when KEY is a project's key, so words like "pre-2" filter.
function idInSearch(text) {
  const m = text.trim().match(/^(?:([A-Za-z][A-Za-z0-9]*)-|#)(\d+)$/);
  if (!m) return "";
  let key = (projectNow() || {}).key;
  if (m[1]) {
    const p = state.projects.find((x) => x.key && x.key.toUpperCase() === m[1].toUpperCase());
    key = p && p.key;
  }
  return key ? `${key.toUpperCase()}-${+m[2]}` : "";
}

// filterTerms splits a filter the way the server reads it: -key:value, key:"two words", "phrase", word.
function filterTerms(f) {
  const out = [];
  for (const m of f.matchAll(/(-?)(?:([A-Za-z_]+):)?("[^"]*"?|\S+)/g)) {
    const raw = m[0];
    if (!raw.replace(/[-"]/g, "")) continue; // a lone - or "" means nothing
    out.push({ raw, neg: m[1] === "-", key: m[2] || "", value: m[3].replace(/^"|"$/g, "") });
  }
  return out;
}

// fieldName is the label of a task field or filter key: a custom field's suite label, else the catalog's.
function fieldName(k) {
  const own = (suiteLang().fields || {})[k];
  if (own) return own;
  const cf = customFields().find((x) => x.key === k);
  if (cf) return cf.label || k;
  return ("field." + k) in i18n.en ? t("field." + k) : k;
}

// valueLabel shows a filter value with its label where the key has labelled values.
function valueLabel(key, v) {
  const kind = { status: "status", doer: "doer", priority: "priority", discipline: "discipline" }[key];
  return kind ? label(kind, v) : v;
}

// withLink splits catalog text at the \u0000 placeholder and puts a link node there.
function withLink(text, link) {
  const [a, b] = text.split("\u0000");
  return b === undefined ? [text, " ", link] : [a, link, b];
}

// renderPills shows the filter's terms as pills over the search box while it is not being edited. Each pill
// has a × that drops its term; clicking anywhere else edits the text.
function renderPills() {
  const terms = filterTerms(state.filter);
  const box = $("filter-pills");
  box.replaceChildren(...terms.map((t, i) => el("span", { class: "pill" + (t.neg ? " neg" : ""), title: t.raw, onclick: () => $("filter").focus() },
    t.neg ? el("span", { class: "pill-key" }, tx("pill.not")) : null,
    t.key ? el("span", { class: "pill-key" }, fieldName(t.key)) : null,
    el("span", { class: "pill-value" }, valueLabel(t.key, t.value)),
    el("button", { class: "pill-x", title: tx("pill.remove", { term: t.raw }), "aria-label": tx("pill.remove", { term: t.raw }), onmousedown: (e) => e.preventDefault(),
      onclick: (e) => { e.stopPropagation(); boardFilter(terms.filter((_, j) => j !== i).map((x) => x.raw).join(" ")); } }, "×"))));
  $("filter").parentElement.classList.toggle("has-pills", terms.length > 0);
}

let loadSeq = 0;
async function loadBoard() {
  renderPills();
  if (!state.project) { renderBoard(); return; }
  const seq = ++loadSeq;
  const q = new URLSearchParams({ project: state.project, q: boardQuery() });
  try {
    const tasks = await api("GET", "/api/tasks?" + q);
    if (seq !== loadSeq) return;
    state.tasks = tasks;
    rememberAssignees(tasks);
    $("filter-error").hidden = true;
  } catch (e) {
    if (seq !== loadSeq) return;
    const fe = $("filter-error");
    fe.textContent = e.message;
    fe.hidden = false;
    return;
  }
  renderBoard();
}

function warnLink(id) {
  return el("a", { href: "#" + id, onclick: (e) => { e.preventDefault(); $("warn-dialog").close(); openTask(id); } }, id);
}

// ---------- board sync (spec §3.5) ----------

// renderSync shows the Sync button when the board folder is in git with an upstream: its state and what is
// waiting to go up (↑) or come down (↓), as of the last fetch.
function renderSync(st) {
  state.sync = st;
  const b = $("sync");
  b.hidden = st.state === "off";
  if (b.hidden) return;
  b.className = "sync " + st.state + (st.running ? " running" : "");
  const counts = (st.ahead ? " ↑" + st.ahead : "") + (st.behind ? " ↓" + st.behind : "");
  b.textContent = st.running ? t("sync.running") : st.state === "ok" ? "⇅ " + t("sync.button") + counts : "⇅ " + t("sync.state_" + st.state);
  b.title = [t("sync.title", { upstream: st.upstream }),
    st.last_sync && !st.last_sync.startsWith("0001") ? t("sync.last", { when: fmtDateTime(st.last_sync) }) : t("sync.never"),
    st.message || ""].filter(Boolean).join("\n");
  b.disabled = !!st.running;
}

async function loadSync() {
  try { renderSync(await api("GET", "/api/sync")); } catch (_) { /* an older server: no sync */ }
}

async function syncNow() {
  renderSync({ ...state.sync, running: true });
  try {
    const st = await api("POST", "/api/sync");
    renderSync(st);
    if (st.state === "ok") toast(t("sync.ok"));
    else toast(st.message || t("sync.state_" + st.state), true);
  } catch (e) { toast(e.message, true); loadSync(); }
  loadWarnings().catch(() => {});
}

function renderSyncSettings() {
  const st = state.sync || { state: "off" };
  $("s-sync-auto").checked = !!st.auto;
  $("s-sync-every").value = st.interval_minutes || 10;
  $("s-sync-auto").disabled = $("s-sync-every").disabled = st.state === "off";
  $("s-sync-note").textContent = st.state === "off" ? t("settings.sync_off_note", { message: st.message || "" }) : t("settings.sync_note", { upstream: st.upstream });
}

async function saveSyncSettings() {
  const minutes = Math.max(1, Math.min(1440, parseInt($("s-sync-every").value || "10", 10) || 10));
  $("s-sync-every").value = minutes;
  try {
    renderSync(await api("PUT", "/api/sync", { auto: $("s-sync-auto").checked, interval_minutes: minutes }));
    toast($("s-sync-auto").checked ? t("settings.sync_on", { n: minutes }) : t("settings.sync_off"));
  } catch (e) { toast(e.message, true); }
  renderSyncSettings();
}

async function loadWarnings() {
  const w = await api("GET", "/api/warnings");
  state.stale = new Set(w.stale);
  const n = w.parse.length + w.stale.length + w.renumbered.length + (w.plugins || []).length + (w.sync ? 1 : 0);
  const b = $("warnings");
  b.hidden = n + (w.i18n || []).length === 0;
  b.textContent = "⚠ " + fmtNum(n + (w.i18n || []).length);
  const list = $("warn-list");
  list.replaceChildren();
  for (const p of w.parse) list.append(el("div", { class: "warn-item" }, t(p.id ? "warn.parse_id" : "warn.parse", { path: p.path, id: p.id, error: p.error })));
  for (const id of w.stale) list.append(el("div", { class: "warn-item" }, ...withLink(t("warn.stale", { id: "\u0000" }), warnLink(id))));
  for (const r of w.renumbered) list.append(el("div", { class: "warn-item" }, ...withLink(t("warn.renumbered", { old: r.OldID, project: r.Project, id: "\u0000" }), warnLink(r.NewID))));
  for (const x of w.i18n || []) list.append(el("div", { class: "warn-item" }, t("warn.i18n", { file: x.file, error: x.error })));
  for (const x of w.plugins || []) list.append(el("div", { class: "warn-item" }, t("warn.plugin", { plugin: x.plugin, error: x.error })));
  if (w.sync) list.append(el("div", { class: "warn-item" }, t("warn.sync", { message: w.sync })));
  if (n + (w.i18n || []).length === 0) list.append(el("div", { class: "muted" }, t("warn.none")));
}

// ---------- board ----------

// ---------- views (spec §4.1) ----------

const FIELDS = ["priority", "claim", "steps", "doer", "assignees", "discipline", "milestone", "labels", "spent", "found"];
// Card fronts stay terse by default: id, title, priority, who works on it or is assigned, its discipline and
// where its pipeline is. The rest is in the drawer (and a view can add any field back).
const DEFAULT_FIELDS = ["priority", "claim", "steps", "assignees", "discipline"];
const ALL_TASKS = { name: "", filter: "", lanes: "", sort: "", fields: [] }; // name: t("view.all")
const PRIORITY_RANK = { urgent: 0, high: 1, normal: 2, low: 3 };

// allFields: the built-in card fields, then the custom fields of the project's suite (spec §4.1).
const customFields = () => suiteNow().fields || [];
const allFields = () => [...FIELDS, ...customFields().map((f) => f.key)];
// fieldsNow: the stored card fields this project has (another suite's custom keys drop out), else the default.
function fieldsNow() {
  const have = new Set(allFields());
  const f = state.fields.filter((x) => have.has(x));
  return f.length ? f : DEFAULT_FIELDS;
}
const normFields = (f) => (f && f.length ? allFields().filter((x) => f.includes(x)) : DEFAULT_FIELDS).join(",");

// sameView reports whether the board shows exactly what view v describes.
function sameView(v) {
  return (v.filter || "").trim() === state.filter && (v.lanes || "") === state.group && (v.sort || "") === state.sort &&
    normFields(v.fields) === normFields(state.fields) && (v.layout || "board") === state.layout;
}

// viewChoices lists the views in the picker: the plain board, the suite's views, then this browser's.
function viewChoices() {
  return [{ key: "all", v: ALL_TASKS, all: true },
    ...(suiteNow().views || []).map((v) => ({ key: "s:" + v.name, v, suite: true })),
    ...state.myViews.map((v) => ({ key: "m:" + v.name, v, mine: true }))];
}

function currentView() { return viewChoices().find((c) => sameView(c.v)) || null; }

function renderViewPicker() {
  const choices = viewChoices();
  const cur = currentView();
  const opt = (c) => el("option", { value: c.key }, c.all ? t("view.all") : c.suite ? label("view", c.v.name) : c.v.name);
  const groups = [opt(choices[0])];
  const suite = choices.filter((c) => c.suite), mine = choices.filter((c) => c.mine);
  if (suite.length) groups.push(el("optgroup", { label: t("view.suite_group", { suite: suiteLang().title || suiteNow().title || t("view.suite") }) }, suite.map(opt)));
  if (mine.length) groups.push(el("optgroup", { label: t("view.mine_group") }, mine.map(opt)));
  if (!cur) groups.push(el("option", { value: "", disabled: true }, t("view.custom")));
  const sel = $("view-pick");
  sel.replaceChildren(...groups);
  sel.value = cur ? cur.key : "";
}

function applyView(v) {
  const filterChanged = (v.filter || "").trim() !== state.filter;
  state.filter = (v.filter || "").trim();
  state.group = v.lanes || "";
  state.sort = v.sort || "";
  state.fields = v.fields && v.fields.length ? allFields().filter((x) => v.fields.includes(x)) : [];
  $("filter").value = state.filter;
  $("group").value = state.group;
  $("sort").value = state.sort;
  store("filter", state.filter); store("group", state.group); store("sort", state.sort); store("fields", JSON.stringify(state.fields));
  state.layout = v.layout === "table" ? "table" : "board";
  store("layout", state.layout);
  if (filterChanged) loadBoard(); else renderBoard();
}

function openViewDialog() {
  const cur = currentView();
  const shown = new Set(fieldsNow());
  $("v-fields").replaceChildren(...allFields().map((f) => el("label", {},
    el("input", { type: "checkbox", checked: shown.has(f), onchange: (e) => {
      const set = new Set(fieldsNow());
      if (e.target.checked) set.add(f); else set.delete(f);
      state.fields = allFields().filter((x) => set.has(x));
      if (normFields(state.fields) === normFields([])) state.fields = [];
      store("fields", JSON.stringify(state.fields));
      renderBoard(); viewSummary();
    } }), fieldName(f))));
  $("v-name").value = cur && cur.mine ? cur.v.name : "";
  $("v-delete").hidden = !(cur && cur.mine);
  viewSummary();
  $("view-dialog").showModal();
}

function viewSummary() {
  const lanes = $("group").selectedOptions[0], sort = $("sort").selectedOptions[0];
  $("v-summary").textContent = t("view.summary", { filter: state.filter || t("view.no_filter"), lanes: lanes ? lanes.text : t("common.none"),
    sort: sort ? sort.text : t("sort.id"), layout: t("layout." + state.layout) });
}

function saveView(e) {
  e.preventDefault();
  const name = $("v-name").value.trim();
  if (!name) { toast(t("view.need_name"), true); $("v-name").focus(); return; }
  const lower = name.toLowerCase();
  if (lower === t("view.all").toLowerCase() || (suiteNow().views || []).some((v) => v.name.toLowerCase() === lower || label("view", v.name).toLowerCase() === lower)) {
    toast(t("view.taken", { name }), true); return;
  }
  const i = state.myViews.findIndex((v) => v.name.toLowerCase() === lower);
  const cur = currentView();
  if (i >= 0 && !(cur && cur.mine && cur.v.name.toLowerCase() === lower) && !confirm(t("view.replace", { name: state.myViews[i].name }))) return;
  const v = { name, filter: state.filter, lanes: state.group, sort: state.sort, fields: state.fields.slice(), layout: state.layout };
  if (i >= 0) state.myViews[i] = v; else state.myViews.push(v);
  store("views", JSON.stringify(state.myViews));
  $("view-dialog").close();
  renderViewPicker();
  toast(t("view.saved", { name }));
}

// saveViewToSuite adds the board as it is now to the project's suite as a view everyone sees (QM-40). This
// browser's view of the same name is dropped: it is shared now.
async function saveViewToSuite() {
  const name = $("v-name").value.trim();
  if (!name) { toast(t("view.need_name"), true); $("v-name").focus(); return; }
  const st = suiteNow();
  if (!confirm(t("viewdlg.suite_confirm", { name, suite: st.name }))) return;
  const v = { name, filter: state.filter, lanes: state.group, sort: state.sort, fields: state.fields.slice(), layout: state.layout };
  try {
    await api("POST", `/api/suites/${encodeURIComponent(st.name)}/views`, v);
    state.myViews = state.myViews.filter((x) => x.name.toLowerCase() !== name.toLowerCase());
    store("views", JSON.stringify(state.myViews));
    $("view-dialog").close();
    await refreshSuite();
    renderViewPicker();
    toast(t("viewdlg.suite_saved", { name, suite: st.name }));
  } catch (err) { toast(err.message, true); }
}

function deleteView() {
  const cur = currentView();
  if (!cur || !cur.mine || !confirm(t("view.delete_confirm", { name: cur.v.name }))) return;
  state.myViews = state.myViews.filter((v) => v !== cur.v);
  store("views", JSON.stringify(state.myViews));
  $("view-dialog").close();
  renderViewPicker();
}

function readViews() {
  try {
    const v = JSON.parse(store("views") || "[]");
    return Array.isArray(v) ? v.filter((x) => x && typeof x.name === "string" && x.name.trim()) : [];
  } catch (_) { return []; }
}

function sortTasks(tasks) {
  const by = {
    priority: (a, b) => (PRIORITY_RANK[a.Priority || "normal"] ?? 2) - (PRIORITY_RANK[b.Priority || "normal"] ?? 2),
    updated: (a, b) => String(b.Updated).localeCompare(String(a.Updated)),
    created: (a, b) => String(b.Created).localeCompare(String(a.Created)),
    title: (a, b) => a.Title.localeCompare(b.Title),
  }[state.sort];
  return by ? tasks.slice().sort(by) : tasks; // Array sort is stable: ties keep id order
}

// ---------- cards ----------

// blockedChip shows the blockers still open on a card or table row (spec §8.3). It is state, like stale, so no
// view hides it.
function blockedChip(t) {
  const b = t.Blocked || [];
  if (!b.length) return null;
  const names = b.map((id) => { const o = state.tasks.find((x) => x.ID === id); return o ? `${id} (${label("status", o.Status)})` : id; });
  return el("span", { class: "chip blocked", title: tx("card.blocked_title", { ids: names.join(", ") }) },
    "⛔ " + b[0] + (b.length > 1 ? " +" + (b.length - 1) : ""));
}

// stillBlocked is added to the toast when a task moves to doing while it waits on open tasks: a hint, not a
// refusal, since people may know better.
const stillBlocked = (status, open) => status === "doing" && open && open.length ? " · " + tx("task.still_blocked", { ids: open.join(", ") }) : "";

function cardFor(t) {
  const f = new Set(fieldsNow());
  const chips = [blockedChip(t)].filter(Boolean);
  if (f.has("doer") && t.Doer === "agent") chips.push(el("span", { class: "chip agent" }, label("doer", "agent")));
  if (f.has("discipline") && t.Discipline) chips.push(el("span", { class: "chip" }, label("discipline", t.Discipline)));
  if (f.has("milestone") && t.Milestone) chips.push(el("span", { class: "chip", title: tx("field.milestone") }, "◆ " + t.Milestone));
  if (f.has("labels")) for (const l of (t.Labels || []).slice(0, 3)) chips.push(el("span", { class: "chip" }, l));
  if (f.has("found") && t.FoundIn) chips.push(el("span", { class: "chip", title: tx("card.found_title", wordVars()) }, tx("card.found", { id: t.FoundIn })));
  const sp = f.has("spent") && spentText(t.SpentTokens, Math.round(t.SpentTime / 1e9), t.SpentSelf > 0);
  if (sp) chips.push(el("span", { class: "chip spent", title: tx(t.SpentSelf > 0 ? "card.spent_self" : "card.spent") }, sp));
  for (const cf of customFields()) {
    const v = (t.Custom || {})[cf.key];
    if (f.has(cf.key) && v) chips.push(el("span", { class: "chip custom", title: fieldName(cf.key) }, v));
  }
  if (state.stale.has(t.ID)) chips.push(el("span", { class: "chip stale", title: tx("card.stale_title") }, tx("card.stale")));
  if (t.ParseError) chips.push(el("span", { class: "chip bad", title: t.ParseError }, tx("card.file_error")));
  const card = el("div", {
    class: `item card p-${t.Priority || "normal"}` + ((t.Blocked || []).length ? " blocked" : "") + (state.selected === t.ID ? " sel" : "") + (state.open && state.open.id === t.ID ? " open" : ""),
    draggable: "true", "data-id": t.ID, tabindex: "-1",
    onclick: () => { select(t.ID, false); openTask(t.ID); },
    onmouseenter: () => hoverSelect(t.ID),
    ondragstart: (e) => { e.dataTransfer.setData(DRAG_TYPE, t.ID); e.dataTransfer.effectAllowed = "move"; },
  },
  el("div", { class: "top-line" }, el("span", { class: "mono" }, t.ID),
    f.has("priority") && t.Priority && t.Priority !== "normal" ? el("span", {}, label("priority", t.Priority)) : null,
    f.has("claim") && t.ClaimBy ? el("span", { class: "claim", title: tx("card.claimed") + ": " + t.ClaimBy }, "● " + t.ClaimBy.split(":")[0]) : null,
    f.has("assignees") && (t.Assignees || []).length ? el("span", { class: "who", title: tx("card.assigned", { names: t.Assignees.join(", ") }) }, t.Assignees.map((a) => "@" + a).join(" ")) : null),
  el("div", { class: "title" }, t.Title),
  f.has("steps") && t.PipelineName ? stepBar(t.PipelineName, t.PipelineAt, false) : null,
  chips.length ? el("div", { class: "chips" }, chips) : null);
  return card;
}

function readList(key) {
  try { const v = JSON.parse(store(key) || "[]"); return Array.isArray(v) ? v.map(String) : []; } catch (_) { return []; }
}

function toggleSet(set, key, storeKey) {
  if (set.has(key)) set.delete(key); else set.add(key);
  store(storeKey, JSON.stringify([...set]));
  renderBoard();
}
const toggleColumn = (status) => toggleSet(state.collapsedCols, status, "collapsedCols");
// laneKey names a folded swimlane per project: "Combat" in one project is not "Combat" in another.
// (Keys stored before this had no project part; they match nothing now and are harmless.)
const laneKey = (k) => state.project + "/" + state.group + ":" + k;

// column draws one status column. A collapsed column is a narrow strip with its name and count; it still takes
// dropped cards, and a click opens it again.
// DONE_SHOWN is how many cards the Done column shows until "Show all": the latest changed ones (QM-50).
const DONE_SHOWN = 10;

function column(status, tasks) {
  const folded = state.collapsedCols.has(status);
  const all = tasks.length;
  let more = null;
  if (status === "done" && all > DONE_SHOWN) {
    if (!state.allDone) {
      const latest = new Set([...tasks].sort((a, b) => String(b.Updated).localeCompare(String(a.Updated))).slice(0, DONE_SHOWN).map((x) => x.ID));
      tasks = tasks.filter((x) => latest.has(x.ID));
    }
    const p = projectNow();
    more = el("div", { class: "col-more" },
      el("button", { class: "small", onclick: (e) => { e.stopPropagation(); state.allDone = !state.allDone; store("allDone", state.allDone ? "1" : "0"); renderBoard(); } },
        state.allDone ? t("column.show_latest", { n: DONE_SHOWN }) : t("column.show_all", { n: all })),
      el("div", { class: "muted" }, p && p.archive_days ? t("column.archive_on", { n: p.archive_days }) : t("column.archive_hint"), " ",
        el("a", { href: "#", onclick: (e) => { e.preventDefault(); e.stopPropagation(); openSettings(); } }, t("column.archive_settings"))));
  }
  const toggle = el("button", {
    class: "head-toggle", title: t(folded ? "column.expand" : "column.collapse", { status: label("status", status) }), "aria-expanded": String(!folded),
    onclick: (e) => { e.stopPropagation(); toggleColumn(status); },
  }, el("span", { class: "chev" }, folded ? "▸" : "▾"), el("span", {}, label("status", status)));
  const col = el("div", { class: "column" + (folded ? " collapsed" : ""), "data-status": status,
    onclick: folded ? () => toggleColumn(status) : null },
    el("div", { class: "column-head" }, toggle, el("span", { class: "count" }, all)),
    folded ? null : tasks.map(cardFor),
    !folded && !all ? el("div", { class: "col-empty muted" }, t("column.empty")) : null,
    folded ? null : more);
  col.addEventListener("dragover", (e) => {
    if (!e.dataTransfer.types.includes(DRAG_TYPE)) return; // only cards, not text dragged from elsewhere
    e.preventDefault();
    col.classList.add("drop");
  });
  col.addEventListener("dragleave", () => col.classList.remove("drop"));
  col.addEventListener("drop", (e) => {
    e.preventDefault();
    col.classList.remove("drop");
    const id = e.dataTransfer.getData(DRAG_TYPE);
    if (id) setStatus(id, status);
  });
  return col;
}

function workdirNote() {
  const p = projectNow();
  $("s-workdir-note").textContent = !p ? "" : p.workdir_pinned ? t("settings.workdir_pinned")
    : p.workdir ? t("settings.workdir_last", { dir: p.workdir }) : t("settings.workdir_none");
}

function archiveNote() {
  const d = parseInt($("s-archive").value || "0", 10) || 0;
  $("s-archive-note").textContent = d ? t("settings.archive_after", { n: d }) : t("settings.archive_never");
}

// ---------- my tasks ----------

// myName is who "My tasks" looks for among the assignees: set in Settings, else the server's user name.
function myName() { return (store("me") || (state.meta && state.meta.user) || "").trim(); }

// boardQuery is the filter sent to the server: the search box plus, with "My tasks" on, an assignee term.
function boardQuery() {
  const me = myName();
  if (!state.mine || !me) return state.filter;
  const v = /[\s":]/.test(me) || me.startsWith("-") ? `"${me.replace(/"/g, "")}"` : me;
  return (state.filter + " assignee:" + v).trim();
}

function setMine(on) {
  if (on && !myName()) { toast(t("mine.need_name"), true); openSettings(); return; }
  state.mine = on;
  store("mine", on ? "1" : "");
  renderMine();
  loadBoard();
}

function renderMine() {
  const b = $("mine");
  b.classList.toggle("on", state.mine);
  b.setAttribute("aria-pressed", String(state.mine));
  b.title = t(state.mine ? "mine.on" : "mine.off", { name: myName() || t("mine.you") });
}

// ---------- assignees ----------

// Known assignee names: those on loaded tasks, remembered in this browser so a filtered board still offers them.
function knownAssignees() { return readList("assignees"); }

function rememberAssignees(tasks) {
  const names = new Set(knownAssignees());
  for (const t of tasks) for (const a of t.Assignees || []) names.add(a);
  const list = [...names].sort((a, b) => a.localeCompare(b));
  store("assignees", JSON.stringify(list));
  $("assignees").replaceChildren(...list.map((n) => el("option", { value: n })));
}

// ---------- quick picker (keys p, d, l, a on the selected card) ----------

// quickPick shows a small list next to the selected card. Typing filters it; Enter picks the highlighted
// entry, or with allowNew the typed text. Esc closes. Returns false if the selected card is not on screen.
function quickPick({ title, items, allowNew, placeholder, onPick }) {
  closeQuickPick();
  const anchor = document.querySelector(`.item[data-id="${CSS.escape(state.selected)}"]`);
  if (!anchor) return false;
  let hi = Math.max(0, items.findIndex((x) => x.on));
  const input = el("input", { class: "qp-input", placeholder: placeholder || t("picker.filter"), spellcheck: "false", autocomplete: "off" });
  const list = el("ul", { class: "qp-list", role: "listbox" });
  // shown: the entries on offer. With allowNew, typed text that is not exactly an existing entry comes first
  // as "Add …", so Enter adds "bug" even while "bugfix" matches the filter.
  const shown = () => {
    const raw = input.value.trim(), q = raw.toLowerCase();
    const xs = items.filter((x) => !q || x.label.toLowerCase().includes(q)).map((x) => ({ ...x }));
    if (allowNew && raw && !items.some((x) => x.label.toLowerCase() === q)) xs.unshift({ value: raw, label: t("picker.add", { text: raw }), isNew: true });
    return xs;
  };
  const draw = () => {
    const xs = shown();
    hi = Math.min(hi, Math.max(0, xs.length - 1));
    list.replaceChildren(...xs.map((x, i) => el("li", {
      class: "qp-item" + (x.isNew ? " qp-new" : "") + (i === hi ? " hi" : ""), role: "option", "aria-selected": String(i === hi),
      onmousedown: (e) => { e.preventDefault(); done(x.value); },
    }, x.isNew ? null : el("span", { class: "qp-mark" }, x.on ? "✓" : ""), x.label, x.key ? el("kbd", {}, x.key) : null)));
  };
  const done = (v) => { closeQuickPick(); onPick(v); };
  input.addEventListener("input", () => { hi = 0; draw(); });
  input.addEventListener("keydown", (e) => {
    // Ctrl+S still saves the drawer.
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") return;
    // Otherwise the picker owns every key while it is open: once it closes, Enter or a digit must not also
    // reach the board's keys (open the card, set a status).
    e.stopPropagation();
    const xs = shown();
    if (e.key === "ArrowDown") { e.preventDefault(); hi = Math.min(hi + 1, xs.length - 1); draw(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); hi = Math.max(hi - 1, 0); draw(); }
    else if (e.key === "Enter") {
      e.preventDefault();
      if (xs.length) done(xs[hi].value);
    } else if (e.key === "Escape") { e.preventDefault(); closeQuickPick(); }
    else if (!input.value && /^[1-9]$/.test(e.key) && items[+e.key - 1] && items[+e.key - 1].key) { e.preventDefault(); done(items[+e.key - 1].value); }
  });
  input.addEventListener("blur", () => setTimeout(closeQuickPick, 0));
  const box = el("div", { class: "quickpick", id: "quickpick", role: "dialog", "aria-label": title },
    el("div", { class: "qp-title" }, title, el("span", { class: "mono muted" }, " " + state.selected)), input, list);
  document.body.append(box);
  const r = anchor.getBoundingClientRect();
  const w = box.offsetWidth, h = box.offsetHeight;
  box.style.left = Math.max(8, Math.min(r.left, innerWidth - w - 8)) + "px";
  box.style.top = (r.bottom + h + 8 < innerHeight ? r.bottom + 4 : Math.max(8, r.top - h - 4)) + "px";
  draw();
  input.focus();
  return true;
}

function closeQuickPick() {
  const q = $("quickpick");
  if (q) q.remove();
}

// ---------- task panel ----------

function setDrawerCollapsed(collapsed) {
  const d = $("drawer");
  d.classList.toggle("collapsed", collapsed);
  const b = $("d-collapse");
  b.textContent = collapsed ? "«" : "»";
  b.title = t(collapsed ? "drawer.expand_key" : "drawer.collapse_key");
  b.setAttribute("aria-label", t(collapsed ? "drawer.expand" : "drawer.collapse"));
  b.setAttribute("aria-expanded", String(!collapsed));
}

function setDrawerWidth(px) {
  const w = Math.round(Math.max(340, Math.min(px, innerWidth * 0.7)));
  document.documentElement.style.setProperty("--drawer-w", w + "px");
  return w;
}

// wireDrawerPanel: collapse to a rail (button, ], or a click on the rail) and resize from the left edge.
function wireDrawerPanel() {
  const d = $("drawer");
  // The stored width is what the person dragged to; each window size clamps it again (without storing).
  const applySaved = () => { const saved = parseInt(store("drawerWidth") || "", 10); if (saved) setDrawerWidth(saved); };
  applySaved();
  window.addEventListener("resize", applySaved);
  $("d-collapse").addEventListener("click", (e) => { e.stopPropagation(); setDrawerCollapsed(!d.classList.contains("collapsed")); });
  d.addEventListener("click", () => { if (d.classList.contains("collapsed")) setDrawerCollapsed(false); });
  $("d-resize").addEventListener("pointerdown", (e) => {
    e.preventDefault();
    const h = e.target;
    h.setPointerCapture(e.pointerId);
    d.classList.add("resizing");
    let last = 0;
    const move = (ev) => { last = setDrawerWidth(innerWidth - ev.clientX); };
    // end runs once for pointerup, pointercancel or a lost capture (whichever comes first): the resizing class
    // must never stay on. A cancelled drag keeps the width it reached (a cancel's clientX is not reliable).
    const end = (ev) => {
      h.removeEventListener("pointermove", move);
      for (const t of ["pointerup", "pointercancel", "lostpointercapture"]) h.removeEventListener(t, end);
      d.classList.remove("resizing");
      if (ev.type === "pointerup") last = setDrawerWidth(innerWidth - ev.clientX);
      if (last) store("drawerWidth", String(last));
    };
    h.addEventListener("pointermove", move);
    for (const t of ["pointerup", "pointercancel", "lostpointercapture"]) h.addEventListener(t, end);
  });
}

// cardKey runs p, d, l or a on the selected task.
function cardKey(k) {
  const t = state.tasks.find((x) => x.ID === state.selected);
  if (!t) return false;
  // Labels and assignees toggle one entry (add_/remove_ ops): a label an agent added meanwhile is never lost.
  const hasCI = (xs, v) => xs.some((x) => x.toLowerCase() === v.toLowerCase());
  if (k === "p") {
    const pr = t.Priority || "normal";
    return quickPick({ title: tx("field.priority_title"), items: ["urgent", "high", "normal", "low"].map((v, i) => ({ value: v, label: label("priority", v), on: v === pr, key: String(i + 1) })),
      onPick: (v) => { if (v !== pr) patchTask(t, { priority: v }); } });
  } else if (k === "d") {
    return quickPick({ title: tx("field.doer_title"), items: ["agent", "person"].map((v, i) => ({ value: v, label: label("doer", v), on: v === t.Doer, key: String(i + 1) })),
      onPick: (v) => { if (v !== t.Doer) patchTask(t, { doer: v }); } });
  } else if (k === "l") {
    const has = t.Labels || [];
    const all = new Set(has);
    for (const x of state.tasks) for (const l of x.Labels || []) all.add(l);
    return quickPick({ title: tx("picker.label_title"), allowNew: true, placeholder: tx("picker.label_placeholder"),
      items: [...all].sort((a, b) => a.localeCompare(b)).map((l) => ({ value: l, label: l, on: hasCI(has, l) })),
      onPick: (v) => patchTask(t, { [hasCI(has, v) ? "remove_labels" : "add_labels"]: [v] }) });
  } else if (k === "a") {
    const cur = t.Assignees || [];
    const names = [...new Set([...cur, ...knownAssignees()])].sort((a, b) => a.localeCompare(b));
    return quickPick({ title: tx("picker.assignee_title"), allowNew: true, placeholder: tx("picker.assignee_placeholder"),
      items: [{ value: "", label: tx("picker.nobody"), on: !cur.length }, ...names.map((n) => ({ value: n, label: n, on: hasCI(cur, n) }))],
      onPick: (v) => {
        if (!v) { if (cur.length) patchTask(t, { remove_assignees: cur }); return; }
        patchTask(t, { [hasCI(cur, v) ? "remove_assignees" : "add_assignees"]: [v] });
      } });
  }
  return false;
}

// laneName labels a swimlane: a discipline's label, "—" for tasks without a value, else the milestone name.
const laneName = (k) => (k === "—" || state.group !== "discipline" ? k : label("discipline", k));

// fieldHelp is a custom field's help text in the chosen language.
const fieldHelp = (cf) => ((suiteLang().fields_help || {})[cf.key]) || cf.help || "";

// laneGroups splits the sorted tasks into swimlanes (one unnamed lane without grouping).
function laneGroups() {
  const lanes = new Map();
  for (const t of sortTasks(state.tasks)) {
    const key = state.group ? (t[state.group === "discipline" ? "Discipline" : "Milestone"] || "—") : "";
    if (!lanes.has(key)) lanes.set(key, []);
    lanes.get(key).push(t);
  }
  if (!lanes.size) lanes.set("", []);
  const keys = [...lanes.keys()].sort((a, b) => (a === "—") - (b === "—") || a.localeCompare(b));
  return { lanes, keys };
}

// ---------- table layout ----------

// Table columns after id and title: [card field or "", header, cell]. Card fields pick the optional ones.
// Headers are catalog keys (table.*); the sortable ones name their sort.
const TABLE_COLS = [
  ["", "table.status", (t) => pick(t, "status", STATUSES, t.Status)],
  ["priority", "table.priority", (t) => pick(t, "priority", ["urgent", "high", "normal", "low"], t.Priority || "normal")],
  ["doer", "table.doer", (t) => pick(t, "doer", ["agent", "person"], t.Doer)],
  ["assignees", "table.assignees", (t) => textCell(t, "assignees", (t.Assignees || []).join(", "))],
  ["discipline", "table.discipline", (t) => textCell(t, "discipline", discShow(t.Discipline), "disciplines")],
  ["milestone", "table.milestone", (t) => textCell(t, "milestone", t.Milestone, "milestones")],
  ["labels", "table.labels", (t) => textCell(t, "labels", (t.Labels || []).join(", "))],
  ["claim", "table.claim", (t) => (t.ClaimBy ? el("span", { class: "claim" }, "● " + t.ClaimBy) : "")],
  ["steps", "table.step", (t) => (t.PipelineName ? `${label("pipeline", t.PipelineName)}: ${label("step", t.PipelineAt)}` : "")],
  ["spent", "table.spent", (t) => el("span", { class: "mono" }, spentText(t.SpentTokens, Math.round(t.SpentTime / 1e9), t.SpentSelf > 0))],
  ["found", "table.found", (t) => t.FoundIn || ""],
];

const SORTABLE = { "table.priority": "priority", "table.title": "title", "table.changed": "updated" };

function pick(t, field, values, cur) {
  return el("select", { class: "cell-edit", "aria-label": tx("table.cell_aria", { field: fieldName(field), id: t.ID }), onchange: (e) => patchTask(t, { [field]: e.target.value }) },
    values.map((v) => el("option", { value: v, selected: v === cur }, label(field, v))));
}

// cellKeys: Enter keeps a cell edit, Escape drops it. Neither may also reach the board keys (open the hovered
// row, close the drawer), so they stop here and are marked handled.
function cellKeys(e, cur) {
  if (e.key !== "Enter" && e.key !== "Escape") return;
  e.preventDefault();
  e.stopPropagation();
  if (e.key === "Escape") e.target.value = cur || "";
  e.target.blur();
}

// listDiff compares two comma lists case-insensitively: what to add and what to remove.
function listDiff(before, after) {
  const b = splitList(before), a = splitList(after);
  const has = (xs, x) => xs.some((y) => y.toLowerCase() === x.toLowerCase());
  return { add: a.filter((x) => !has(b, x)), remove: b.filter((x) => !has(a, x)) };
}

function textCell(t, field, cur, list) {
  return el("input", {
    class: "cell-edit", value: cur || "", list: list || null, spellcheck: "false", "aria-label": tx("table.cell_aria", { field: fieldName(field), id: t.ID }),
    onkeydown: (e) => cellKeys(e, cur),
    onchange: (e) => {
      if (field === "discipline") { patchTask(t, { discipline: discKey(e.target.value) }); return; }
      if (field !== "labels" && field !== "assignees") { patchTask(t, { [field]: e.target.value.trim() }); return; }
      // Lists send only the entries this edit added or removed, so a label an agent added meanwhile stays.
      const d = listDiff(cur || "", e.target.value);
      const patch = {};
      if (d.add.length) patch["add_" + field] = d.add;
      if (d.remove.length) patch["remove_" + field] = d.remove;
      if (Object.keys(patch).length) patchTask(t, patch);
    },
  });
}

// customCell edits a custom field in a table row: a choice is a select, anything else a text box.
function customCell(t, cf) {
  const cur = (t.Custom || {})[cf.key] || "";
  const save = (v) => patchTask(t, { fields: { [cf.key]: v } });
  if (cf.type === "choice") {
    const values = cf.values.includes(cur) || !cur ? cf.values : [...cf.values, cur];
    return el("select", { class: "cell-edit", "aria-label": tx("table.cell_aria", { field: fieldName(cf.key), id: t.ID }), onchange: (e) => save(e.target.value) },
      el("option", { value: "" }, "—"), values.map((v) => el("option", { value: v, selected: v === cur }, v)));
  }
  return el("input", {
    class: "cell-edit", value: cur, placeholder: fieldHelp(cf), spellcheck: "false", "aria-label": tx("table.cell_aria", { field: fieldName(cf.key), id: t.ID }),
    onkeydown: (e) => cellKeys(e, cur),
    onchange: (e) => save(e.target.value.trim()),
  });
}

// customInputs builds the form controls for a suite's custom fields (drawer and New task dialog).
function customInputs(fields, values, onInput) {
  return fields.map((cf) => {
    const cur = (values || {})[cf.key] || "";
    let input;
    if (cf.type === "choice") {
      const vals = cf.values.includes(cur) || !cur ? cf.values : [...cf.values, cur];
      input = el("select", { "data-field": cf.key }, el("option", { value: "" }, "—"), vals.map((v) => el("option", { value: v, selected: v === cur }, v)));
    } else {
      input = el("input", { "data-field": cf.key, value: cur, placeholder: fieldHelp(cf), spellcheck: "false" });
    }
    if (onInput) { input.addEventListener("input", onInput); input.addEventListener("change", onInput); }
    return el("label", {}, fieldName(cf.key), input);
  });
}

// ---------- field rules (spec §4.1 [show]) ----------

// The built-in fields a suite's [show] may hide, by the id suffix of their input (d-found, n-pipeline, …).
const SHOW_INPUTS = { asset: "asset", pipeline: "pipeline", found_in: "found", fixed_in: "fixed", milestone: "milestone", url: "url", blocked_by: "blocked" };
let moreFields = store("moreFields") === "1"; // "More fields": show hidden ones too (per browser)

// showTerm checks one [show] term (label:, discipline:, doer:, status:, priority: or a custom field key; "-"
// negates) against the values a form holds. Values match in any case, like the filter.
function showTerm(term, task) {
  const neg = term.startsWith("-");
  const [k, ...rest] = (neg ? term.slice(1) : term).split(":");
  const key = k.toLowerCase(), want = rest.join(":").replace(/^"|"$/g, "").toLowerCase();
  const has = key === "label" ? task.labels.some((l) => l.toLowerCase() === want)
    : String((key in task ? task[key] : task.fields[key]) || "").toLowerCase() === want;
  return has !== neg;
}

// applyShow hides the fields of the drawer ("d") or New task dialog ("n") that the suite's [show] rules leave
// out for this task. A field with no rule, with a value, or with a matching term shows; "More fields" shows all.
function applyShow(form) {
  const st = form === "d" && state.open ? suiteOf(state.open.project) : suiteNow();
  const v = (id) => { const n = $(form + "-" + id); return n ? n.value.trim() : ""; };
  const fields = customValues($(form + "-custom"));
  const task = { labels: splitList(v("labels")), discipline: discKey(v("discipline"), st), doer: v("doer"), status: v("status"), priority: v("priority"), fields };
  let hidden = 0;
  document.querySelectorAll(`#${form === "d" ? "drawer" : "new-dialog"} .rule-hidden`).forEach((n) => n.classList.remove("rule-hidden"));
  for (const [key, terms] of Object.entries(st.show || {})) {
    const input = SHOW_INPUTS[key] ? $(form + "-" + SHOW_INPUTS[key]) : $(form + "-custom").querySelector(`[data-field="${CSS.escape(key)}"]`);
    const box = input && input.closest("label");
    if (!box) continue;
    const value = SHOW_INPUTS[key] ? v(SHOW_INPUTS[key]) : fields[key];
    if (value || terms.some((x) => showTerm(x, task))) continue;
    box.classList.add("rule-hidden");
    hidden++;
  }
  const b = $(form + "-more");
  b.hidden = !hidden;
  b.textContent = moreFields ? t("drawer.fewer_fields") : t("drawer.more_fields", { n: hidden });
  $(form === "d" ? "drawer" : "new-dialog").classList.toggle("show-all", moreFields);
}

function toggleMoreFields(form) {
  moreFields = !moreFields;
  store("moreFields", moreFields ? "1" : "0");
  applyShow(form);
}

// customValues reads custom field controls back: {key: value} ("" for empty).
function customValues(box) {
  const out = {};
  box.querySelectorAll("[data-field]").forEach((n) => { out[n.dataset.field] = n.value.trim(); });
  return out;
}

// suiteOf returns the suite of a project by slug (the drawer may show another project's task).
function suiteOf(slug) {
  const p = state.projects.find((x) => x.slug === slug);
  return (p && p.suite) || suiteNow();
}

// LIST_OPS change one entry of a list and never drop what others added, so they need no version.
const LIST_OPS = ["add_labels", "remove_labels", "add_assignees", "remove_assignees"];
const listOpsOnly = (patch) => Object.keys(patch).every((k) => LIST_OPS.includes(k));

// patchTask saves a quick edit (table cell, picker) of task row t. A field set carries the row's Version: if
// the file changed since the board loaded it, the server refuses (409) and the board reloads instead of
// overwriting the newer task.
async function patchTask(t, patch) {
  const body = listOpsOnly(patch) || !t.Version ? patch : { ...patch, version: t.Version };
  try {
    await api("PATCH", "/api/tasks/" + encodeURIComponent(t.ID), body);
    toast(tx("task.saved_fields", { id: t.ID, fields: Object.keys(patch).map((k) => fieldName(k.replace(/^(add|remove)_/, ""))).join(", ") }) +
      stillBlocked(patch.status, t.Blocked));
  } catch (e) {
    if (e.status === 409) toast(tx("task.changed_reloaded", { id: t.ID }), true);
    else toast(tx("task.error", { id: t.ID, error: e.message }), true);
  }
  await loadBoard();
  if (state.open && state.open.id === t.ID && !state.dirty) openTask(t.ID);
}

function fmtWhen(ts) {
  const d = new Date(ts);
  if (isNaN(d) || d.getFullYear() < 2000) return "";
  const days = Math.floor((Date.now() - d) / 864e5);
  return days < 1 ? d.toLocaleTimeString(i18n.lang, { hour: "2-digit", minute: "2-digit" })
    : days < 7 ? new Intl.RelativeTimeFormat(i18n.lang, { numeric: "always", style: "short" }).format(-days, "day") : fmtDate(d);
}

function renderTable(board, lanes, keys) {
  const f = new Set(fieldsNow());
  const cols = [...TABLE_COLS.filter(([field]) => !field || f.has(field)),
    ...customFields().filter((cf) => f.has(cf.key)).map((cf) => [cf.key, "", (t) => customCell(t, cf), fieldName(cf.key)])];
  const ids = new Set(state.tasks.map((t) => t.ID));
  for (const id of state.checked) if (!ids.has(id)) state.checked.delete(id); // gone or filtered out
  // "Select all" means the rows on screen: not those inside folded lanes.
  const drawn = [];
  for (const k of keys) {
    const tasks = lanes.get(k);
    if (state.group && tasks.length > 0 && state.collapsedLanes.has(laneKey(k))) continue;
    for (const t of tasks) drawn.push(t.ID);
  }
  const all = el("input", { type: "checkbox", "aria-label": t("table.select_all"), checked: drawn.length > 0 && drawn.every((id) => state.checked.has(id)),
    onchange: (e) => { state.checked = e.target.checked ? new Set(drawn) : new Set(); renderBoard(); } });
  const th = (k, text) => {
    const name = text || t(k);
    const key = SORTABLE[k];
    if (!key) return el("th", {}, name);
    const on = state.sort === key;
    return el("th", {}, el("button", { class: "th-sort" + (on ? " on" : ""), title: on ? t("table.sort_off") : t("table.sort_by", { name }),
      onclick: () => { state.sort = on ? "" : key; $("sort").value = state.sort; store("sort", state.sort); renderBoard(); } }, name, on ? " ↓" : ""));
  };
  const head = el("tr", {}, el("th", { class: "check" }, all), el("th", {}, t("table.id")), th("table.title"), cols.map(([, k, , text]) => th(k, text)), th("table.changed"));
  const span = cols.length + 4;
  const body = [];
  for (const k of keys) {
    const tasks = lanes.get(k);
    const lk = laneKey(k);
    const folded = state.group && tasks.length > 0 && state.collapsedLanes.has(lk);
    if (state.group && tasks.length) {
      body.push(el("tr", { class: "lane-row" }, el("td", { colspan: span },
        el("button", { class: "lane-title" + (folded ? " collapsed" : ""), "aria-expanded": String(!folded),
          onclick: () => toggleSet(state.collapsedLanes, lk, "collapsedLanes") },
        el("span", { class: "chev" }, folded ? "▸" : "▾"), laneName(k), el("span", { class: "count" }, tasks.length)))));
    }
    if (folded) continue;
    for (const t of tasks) body.push(rowFor(t, cols));
  }
  board.replaceChildren(bulkBar(), el("div", { class: "table-wrap" }, el("table", { class: "tasks" }, el("thead", {}, head), el("tbody", {}, body))));
}

function rowFor(t, cols) {
  const stop = (e) => e.stopPropagation(); // editing a cell must not open the drawer
  return el("tr", {
    class: `item trow p-${t.Priority || "normal"}` + (state.selected === t.ID ? " sel" : "") + (state.open && state.open.id === t.ID ? " open" : "") +
      (state.checked.has(t.ID) ? " checked" : ""),
    "data-id": t.ID, onclick: () => { select(t.ID, false); openTask(t.ID); }, onmouseenter: () => hoverSelect(t.ID),
  },
  el("td", { class: "check", onclick: stop }, el("input", { type: "checkbox", "aria-label": tx("table.select", { id: t.ID }), checked: state.checked.has(t.ID),
    onchange: (e) => { if (e.target.checked) state.checked.add(t.ID); else state.checked.delete(t.ID); renderBoard(); } })),
  el("td", { class: "mono id" }, t.ID, state.stale.has(t.ID) ? el("span", { class: "chip stale", title: tx("card.stale_title") }, tx("card.stale")) : null),
  el("td", { class: "title-cell" }, t.Title, blockedChip(t), t.ParseError ? el("span", { class: "chip bad", title: t.ParseError }, tx("card.file_error")) : null),
  cols.map(([, , cell]) => {
    const c = cell(t);
    return el("td", c instanceof Node && c.classList.contains("cell-edit") ? { onclick: stop } : {}, c);
  }),
  el("td", { class: "muted when", title: String(t.Updated) }, fmtWhen(t.Updated)));
}

// bulkBar edits every checked task at once: the fields triage needs.
function bulkBar() {
  const n = state.checked.size;
  if (!n) return el("div", { class: "bulk muted" }, t("bulk.hint"));
  const sel = (field, values, name) => el("label", { class: "inline" }, name,
    el("select", { onchange: (e) => { if (e.target.value) bulkPatch(() => ({ [field]: e.target.value })); } },
      el("option", { value: "" }, "—"), values.map((v) => el("option", { value: v }, label(field, v)))));
  const text = (name, list, make) => el("label", { class: "inline" }, name,
    el("input", { list, spellcheck: "false", onkeydown: (e) => cellKeys(e, ""),
      onchange: (e) => { const v = e.target.value.trim(); if (v) bulkPatch((t) => make(t, v)); } }));
  return el("div", { class: "bulk on" }, el("strong", {}, t("bulk.selected", { n })),
    sel("status", STATUSES, t("table.status")), sel("priority", ["urgent", "high", "normal", "low"], t("table.priority")), sel("doer", ["agent", "person"], t("table.doer")),
    text(t("bulk.add_assignee"), "assignees", (_, v) => ({ add_assignees: splitList(v) })),
    text(t("table.discipline"), "disciplines", (_, v) => ({ discipline: discKey(v) })),
    text(t("table.milestone"), "milestones", (_, v) => ({ milestone: v })),
    text(t("bulk.add_label"), null, (_, v) => ({ add_labels: splitList(v) })),
    el("span", { class: "spacer" }),
    el("button", { class: "small", onclick: () => { state.checked.clear(); renderBoard(); } }, t("bulk.clear")));
}

async function bulkPatch(make) {
  const tasks = state.tasks.filter((t) => state.checked.has(t.ID));
  const failed = [];
  for (const t of tasks) {
    // A field set carries each row's Version (list ops need none); a task changed meanwhile is a failure.
    const patch = make(t);
    const body = listOpsOnly(patch) || !t.Version ? patch : { ...patch, version: t.Version };
    try { await api("PATCH", "/api/tasks/" + encodeURIComponent(t.ID), body); } catch (e) {
      failed.push(tx("task.error", { id: t.ID, error: e.status === 409 ? tx("task.changed_meanwhile") : e.message }));
    }
  }
  if (document.activeElement && document.activeElement.blur) document.activeElement.blur();
  if (failed.length) toast(t("bulk.partly", { done: tasks.length - failed.length, n: tasks.length, errors: failed.join("; ") }), true);
  else toast(t("bulk.done", { n: tasks.length }));
  await loadBoard();
}

function setLayout(layout) {
  state.layout = layout === "table" ? "table" : "board";
  store("layout", state.layout);
  renderBoard();
}

function renderBoard() {
  const board = $("board");
  const empty = $("empty");
  if (!state.project) {
    board.replaceChildren();
    empty.hidden = false;
    empty.replaceChildren(...withLink(t("empty.no_projects", { button: "\u0000" }), el("button", { class: "primary", onclick: openNewProject }, t("project.new"))));
    return;
  }
  // A live reload must not replace a table field someone is typing in; it renders when they leave it.
  // (Checkboxes are not typed in: ticking one must show the bulk bar at once.)
  const a = document.activeElement;
  if (state.layout === "table" && board.contains(a) && (a.tagName === "SELECT" || (a.tagName === "INPUT" && a.type !== "checkbox"))) {
    renderBoard.pending = true;
    return;
  }
  renderBoard.pending = false;
  hover.armed = false; // new cards under a still mouse are not a hover
  empty.hidden = state.view !== "board" || state.tasks.length > 0; // live reloads also run on other views
  empty.textContent = state.mine && myName() ? t(state.filter ? "empty.mine_filter" : "empty.mine", { name: myName() })
    : t(state.filter ? "empty.filter" : "empty.none");
  renderViewPicker();
  document.querySelectorAll("#layout button").forEach((b) => b.classList.toggle("on", b.dataset.layout === state.layout));
  board.classList.toggle("table-layout", state.layout === "table");
  const { lanes, keys } = laneGroups();
  if (state.layout === "table") { renderTable(board, lanes, keys); return; }
  // A status with no task on the whole board is a narrow strip (it still takes drops); folded ones are thinner.
  const used = new Set(state.tasks.map((x) => x.Status));
  const width = (wide) => STATUSES.map((s) => (state.collapsedCols.has(s) ? "40px" : used.has(s) ? wide : "120px")).join(" ");
  board.style.setProperty("--cols", width("minmax(200px, 1fr)"));
  board.style.setProperty("--cols-narrow", width("240px"));
  board.replaceChildren(...keys.map((k) => {
    const lk = laneKey(k);
    const tasks = lanes.get(k);
    const titled = state.group && tasks.length > 0; // no lane header on an empty board
    const folded = titled && state.collapsedLanes.has(lk);
    return el("div", { class: "lane" },
      titled ? el("button", {
        class: "lane-title" + (folded ? " collapsed" : ""), "aria-expanded": String(!folded),
        title: t(folded ? "lane.expand" : "lane.collapse"),
        onclick: () => toggleSet(state.collapsedLanes, lk, "collapsedLanes"),
      }, el("span", { class: "chev" }, folded ? "▸" : "▾"), laneName(k), el("span", { class: "count" }, tasks.length)) : null,
      folded ? null : el("div", { class: "columns" }, STATUSES.map((s) => column(s, tasks.filter((t) => t.Status === s)))));
  }));
}

// select makes a card (or table row) the one the keys act on. Keyboard moves scroll it into view; hovering
// does not (scrolling under the mouse would move other cards under it).
function select(id, scroll = true) {
  if (scroll) hover.armed = false; // a keyboard move: the card that slides under the mouse must not take over
  if (state.selected === id && !scroll) return;
  state.selected = id;
  document.querySelectorAll(".item.sel").forEach((c) => c.classList.remove("sel"));
  const c = document.querySelector(`.item[data-id="${CSS.escape(id)}"]`);
  if (c) { c.classList.add("sel"); if (scroll) c.scrollIntoView({ block: "nearest", inline: "nearest" }); }
}

// hoverSelect: the card under the mouse is the selected one, so p, d, l, a and 1–5 act on it without a click.
// Not while a picker is open: it belongs to the card it was opened on. And only once the mouse really moved
// after the last keyboard selection or re-render: a mouseenter caused by cards moving under a still pointer
// (scroll into view, reload) must not steal the selection from the keyboard.
const hover = { armed: false, x: -1, y: -1 };
function hoverSelect(id) { if (hover.armed && !$("quickpick")) select(id, false); }

function onMouseMove(e) {
  if (e.clientX === hover.x && e.clientY === hover.y) return; // browsers send moves without motion after layout
  hover.x = e.clientX; hover.y = e.clientY;
  if (hover.armed) return;
  hover.armed = true;
  const it = e.target instanceof Element && e.target.closest("#board .item");
  if (it) hoverSelect(it.dataset.id); // the card under a pointer that starts moving again (no mouseenter for it)
}

async function setStatus(id, status) {
  const t = state.tasks.find((x) => x.ID === id);
  if (t && t.Status === status) return;
  try {
    await api("PATCH", "/api/tasks/" + encodeURIComponent(id), { status });
    toast(`${id} → ${label("status", status)}` + stillBlocked(status, t && t.Blocked));
  } catch (e) { toast(e.message, true); }
  await loadBoard(); // do not rely on the event stream alone
  if (state.open && state.open.id === id && !state.dirty) openTask(id);
}

// ---------- drawer ----------

const fieldMap = {
  "d-title": "title", "d-status": "status", "d-doer": "doer", "d-assignees": "assignees", "d-priority": "priority", "d-discipline": "discipline",
  "d-milestone": "milestone", "d-labels": "labels", "d-asset": "asset", "d-blocked": "blocked_by", "d-found": "found_in",
  "d-fixed": "fixed_in", "d-url": "url", "d-body": "body", "d-pipeline": "pipeline",
};

async function openTask(id) {
  if (state.dirty && state.open && state.open.id !== id && !confirm(t("drawer.discard", { id: state.open.id }))) return;
  let t;
  try { t = await api("GET", "/api/tasks/" + encodeURIComponent(id)); }
  catch (e) { toast(e.message, true); return; }
  state.open = t;
  state.dirty = false;
  history.replaceState(null, "", "#" + id);
  fillDrawer(t);
  loadLocks(t);
  loadTaskKB(t.id);
  $("drawer").hidden = false;
  setDrawerCollapsed(false); // opening a card means working on it
  document.querySelectorAll(".item.open").forEach((c) => c.classList.remove("open"));
  const c = document.querySelector(`.item[data-id="${CSS.escape(id)}"]`);
  if (c) c.classList.add("open");
}

// loadLocks shows the files a task names and who holds a lock on them (spec §5). It is asked after the drawer
// opens, so a slow lock server (up to 3 s) never holds the drawer up; the server caches answers for 60 s.
async function loadLocks(t) {
  const box = $("d-locks");
  // The box keeps its rows while the new answer comes (a live refresh of the same task does not flicker);
  // another task's rows go at once.
  if (box.dataset.task !== t.id) { box.hidden = true; box.replaceChildren(); }
  let r;
  try { r = await api("GET", "/api/tasks/" + encodeURIComponent(t.id) + "/locks"); }
  catch (e) { return; }
  if (!state.open || state.open.id !== t.id) return; // another task was opened meanwhile
  box.dataset.task = t.id;
  box.replaceChildren();
  box.hidden = true;
  if (!r.files.length) {
    if (!r.workdir && (t.asset || /`[^`\s]+`/.test(t.body || ""))) {
      box.append(el("div", { class: "muted" }, t("locks.no_workdir")));
      box.hidden = false;
    }
    return;
  }
  const locks = new Map(r.locks.locks.map((l) => [l.path, l]));
  for (const f of r.files) {
    const l = locks.get(f);
    const row = el("div", { class: "lock-row" + (l ? " locked" : "") }, el("code", {}, f));
    if (l) {
      const owner = l.owner || t("locks.someone");
      const s = l.since ? t("locks.by_since", { owner, since: fmtDateTime(l.since) }) : t("locks.by", { owner });
      row.append(el("span", { class: "lock-owner", title: l.provider }, "🔒 " + s));
    } else if (r.locks.checked && !(r.locks.unknown || []).length) {
      row.append(el("span", { class: "muted" }, t("locks.free")));
    }
    box.append(row);
  }
  for (const u of r.locks.unknown || []) box.append(el("div", { class: "muted", title: u }, t("locks.unknown", { reason: u })));
  box.hidden = false;
}

// setPipelineChoice selects a pipeline in the drawer. One the suite does not list still shows, as an extra
// option that belongs to this task only.
function setPipelineChoice(name) {
  const sel = $("d-pipeline");
  sel.querySelectorAll("option[data-extra]").forEach((o) => o.remove());
  if (name && ![...sel.options].some((o) => o.value === name)) {
    sel.append(el("option", { value: name, "data-extra": "1" }, t("pipeline.not_in_suite", { name })));
  }
  sel.value = name || "";
}

const openBlockers = (t) => (t.blockers || []).filter((l) => l.status && l.status !== "done" && !l.archived).map((l) => l.id);

// taskLinks lists blocked_by tasks or tasks waiting on this one, each with its status; done and archived ones
// are struck through.
function taskLinks(links) {
  return links.flatMap((l, i) => [i ? ", " : "",
    el("a", { href: "#" + l.id, class: l.status === "done" || l.archived ? "done" : "", title: l.title,
      onclick: (e) => { e.preventDefault(); openTask(l.id); } }, l.id),
    " ", el("span", { class: "muted" }, l.status ? label("status", l.status) : tx("drawer.no_task"))]);
}

function fillDrawer(t) {
  $("d-id").textContent = t.archived ? tx("drawer.archived_id", { id: t.id }) : t.id;
  $("d-claim").textContent = t.claim ? tx("drawer.claimed_by", { who: t.claim.by }) : "";
  const values = {
    title: t.title, status: t.status, doer: t.doer, assignees: (t.assignees || []).join(", "), priority: t.priority || "normal", discipline: discShow(t.discipline),
    milestone: t.milestone, labels: t.labels.join(", "), asset: t.asset, blocked_by: t.blocked_by.join(", "),
    found_in: t.found_in, fixed_in: t.fixed_in, url: t.url, body: t.body, pipeline: t.pipeline ? t.pipeline.name : "",
  };
  for (const [elId, key] of Object.entries(fieldMap)) if (elId !== "d-pipeline") $(elId).value = values[key] || "";
  const cfs = suiteOf(t.project).fields || [];
  $("d-custom").replaceChildren(...customInputs(cfs, t.fields, () => setDirty(true)));
  $("d-custom").hidden = cfs.length === 0;
  setPipelineChoice(values.pipeline);
  applyShow("d");
  const pl = t.pipeline && t.pipeline.name ? t.pipeline : null;
  $("d-steps-row").hidden = !pl;
  if (pl) {
    $("d-steps").replaceChildren(stepBar(pl.name, pl.at, true));
    const def = suiteNow().pipelines.find((x) => x.name.toLowerCase() === pl.name.toLowerCase());
    $("d-advance").disabled = !def || def.steps.indexOf(pl.at) === def.steps.length - 1;
  }
  const by = t.blockers || [], blocks = t.blocks || [];
  $("d-links").replaceChildren(
    by.length ? el("div", {}, el("strong", {}, tx("drawer.blocked_by")), " ", ...taskLinks(by)) : "",
    blocks.length ? el("div", {}, el("strong", {}, tx("drawer.blocks")), " ", ...taskLinks(blocks)) : "");
  $("d-links").hidden = !by.length && !blocks.length;
  const banner = $("d-banner");
  banner.hidden = !t.warning;
  banner.textContent = t.warning ? tx("drawer.unreadable", { error: t.warning }) : "";
  setDirty(false);
  renderPreview();

  const sp = t.spent;
  const box = $("d-spent");
  box.replaceChildren();
  if (!sp.records) box.append(el("div", { class: "muted" }, tx("spent.none")));
  else {
    box.append(el("div", {}, el("strong", {}, spentText(sp.total, sp.seconds, sp.self > 0) || "0"),
      el("span", { class: "muted" }, " · " + (sp.self ? tx("spent.self", { self: sp.self, n: sp.records }) : tx("spent.measured"))),
      sp.cost_usd ? el("span", { class: "muted" }, " · " + fmtUSD(sp.cost_usd)) : null));
    const models = Object.entries(sp.models);
    if (models.length) {
      box.append(el("table", { class: "spent" },
        el("tr", {}, ...["model", "input", "output", "cache_read", "cache_write", "other"].map((k) => el("th", {}, tx("spent.col_" + k)))),
        models.map(([m, k]) => el("tr", {}, el("td", {}, m), el("td", {}, fmtTokens(k.input || 0)), el("td", {}, fmtTokens(k.output || 0)),
          el("td", {}, fmtTokens(k.cacheRead || 0)), el("td", {}, fmtTokens(k.cacheCreation || 0)), el("td", {}, fmtTokens(k.total || 0))))));
    }
  }

  // Log lines are Markdown (agents write lists, code and links in their notes): rendered like the description.
  $("d-log").replaceChildren(...t.log.slice().reverse().map((e) => {
    const text = el("div", { class: "log-text" });
    text.innerHTML = renderMarkdown(e.text, t.project, t.id); // escapes everything first; see renderMarkdown
    return el("li", {}, el("div", { class: "log-head" }, el("span", { class: "when" }, e.time), el("span", { class: "who" }, e.who)), text);
  }));
  // Status changes and people's usage corrections (agents' own usage reports are summed above).
  const hist = t.history.filter((h) => h.kind === "status" || (h.kind === "usage" && h.source === "human"));
  $("d-history").replaceChildren(...(hist.length ? hist.map((h) => el("li", {},
    el("span", { class: "when" }, fmtDateTime(h.at)),
    h.kind === "status" ? `${h.from ? label("status", h.from) : tx("history.created")} → ${label("status", h.to)}` : tx("history.correction", { spent: spentText(h.tokens, h.seconds, false) || "0" }),
    el("span", { class: "muted" }, " " + tx(h.model ? "history.by_model" : "history.by", { who: h.session || h.actor || "?", model: h.model }))))
    : [el("li", { class: "muted" }, tx("history.none"))]));

  $("d-release").hidden = !t.claim;
  $("d-archive").hidden = t.status !== "done" || t.archived;
  $("d-move").disabled = $("d-move-to").options.length === 0;
}

function setDirty(d) {
  state.dirty = d;
  $("d-save").disabled = !d;
  $("d-dirty").textContent = d ? t("drawer.unsaved") : "";
}

function renderPreview() {
  const pv = $("d-preview"), ta = $("d-body");
  pv.hidden = !state.preview;
  ta.hidden = state.preview;
  $("d-preview-toggle").textContent = t(state.preview ? "drawer.edit" : "drawer.preview");
  if (state.preview && state.open) pv.innerHTML = renderMarkdown(ta.value, state.open.project, state.open.id);
}

async function saveDrawer() {
  const t = state.open;
  if (!t || !state.dirty) return;
  const now = {
    title: $("d-title").value.trim(), status: $("d-status").value, doer: $("d-doer").value, assignees: splitList($("d-assignees").value), priority: $("d-priority").value,
    discipline: discKey($("d-discipline").value, suiteOf(t.project)), milestone: $("d-milestone").value.trim(), asset: $("d-asset").value.trim(),
    found_in: $("d-found").value.trim(), fixed_in: $("d-fixed").value.trim(), url: $("d-url").value.trim(), body: $("d-body").value,
    labels: splitList($("d-labels").value), blocked_by: splitList($("d-blocked").value).map((x) => x.toUpperCase()),
    pipeline: $("d-pipeline").value,
  };
  const before = {
    title: t.title, status: t.status, doer: t.doer, assignees: t.assignees || [], priority: t.priority || "normal", discipline: t.discipline, milestone: t.milestone,
    asset: t.asset, found_in: t.found_in, fixed_in: t.fixed_in, url: t.url, body: t.body, labels: t.labels, blocked_by: t.blocked_by,
    pipeline: t.pipeline ? t.pipeline.name : "",
  };
  const patch = {};
  for (const k of Object.keys(now)) if (JSON.stringify(now[k]) !== JSON.stringify(before[k])) patch[k] = now[k];
  const custom = customValues($("d-custom"));
  const fieldPatch = {};
  for (const [k, v] of Object.entries(custom)) if (v !== ((t.fields || {})[k] || "")) fieldPatch[k] = v;
  if (Object.keys(fieldPatch).length) patch.fields = fieldPatch;
  if ("pipeline" in patch) {
    // A newly chosen pipeline starts at its first step; "" removes it.
    const def = suiteNow().pipelines.find((x) => x.name === patch.pipeline);
    patch.pipeline = patch.pipeline ? { name: patch.pipeline, at: def ? def.steps[0] : "" } : { name: "" };
  }
  if (!Object.keys(patch).length) { setDirty(false); return; }
  await sendPatch(t, patch, t.version);
}

// sendPatch saves a drawer edit made on version v of the task. If the file changed since, the server answers
// 409 and the person chooses: load the other version (losing these edits) or save these edits over it.
async function sendPatch(t, patch, v) {
  try {
    await api("PATCH", "/api/tasks/" + encodeURIComponent(t.id), v ? { ...patch, version: v } : patch);
    state.dirty = false;
    await openTask(t.id);
    // Blockers as saved: this save may have changed blocked_by too.
    const now = state.open && state.open.id === t.id ? state.open : t;
    toast(tx("drawer.saved", { id: t.id }) + stillBlocked(patch.status, openBlockers(now)));
    loadBoard();
  } catch (e) {
    if (e.status === 409 && v && /changed by someone else/.test(e.message)) showChanged(t, patch);
    else toast(e.message, true);
  }
}

// showChanged tells the person the open task changed under their unsaved edits.
function showChanged(t, patch) {
  const b = $("d-banner");
  b.hidden = false;
  const load = el("button", { class: "small", onclick: () => { state.dirty = false; openTask(t.id); } }, tx("drawer.load_theirs"));
  const keep = patch ? el("button", { class: "small", onclick: () => sendPatch(t, patch, "") }, tx("drawer.save_mine")) : null;
  b.replaceChildren(...[tx("drawer.changed") + " ", load, keep && " ", keep].filter(Boolean));
}

function closeDrawer() {
  if (state.dirty && !confirm(t("drawer.discard_any"))) return;
  state.open = null;
  state.dirty = false;
  $("drawer").hidden = true;
  history.replaceState(null, "", location.pathname);
  document.querySelectorAll(".item.open").forEach((c) => c.classList.remove("open"));
}

// drawerAction runs a write for the open task. fn returns false when the person cancelled it. After a write the
// board and drawer are reloaded here too, so the view is right even while the event stream is down.
async function drawerAction(fn, msg) {
  if (!state.open) return;
  let res;
  try {
    res = await fn(state.open);
  } catch (e) { toast(e.message, true); return; }
  if (res === false) return;
  if (msg) toast(msg);
  await loadBoard();
  if (state.open && !state.dirty) openTask(state.open.id);
  return res;
}

// Paste an image (or any file) into the description: it is uploaded as an attachment and linked.
async function onPaste(e) {
  const t = state.open;
  if (!t) return;
  const files = [...(e.clipboardData || {}).files || []];
  if (!files.length) return;
  e.preventDefault();
  const ta = $("d-body");
  for (const f of files) {
    if (f.size > state.meta.max_upload) { toast(tx("attach.too_big", { name: f.name || tx("attach.file"), mb: state.meta.max_upload >> 20 }), true); continue; }
    try {
      const name = f.name && f.name !== "image.png" ? f.name : "pasted-" + new Date().toISOString().replace(/[:.]/g, "-") + "." + ((f.type.split("/")[1] || "bin").replace("jpeg", "jpg"));
      const res = await api("POST", `/api/tasks/${encodeURIComponent(t.id)}/attachments?name=${encodeURIComponent(name)}`, undefined, { data: f, type: f.type });
      const pos = ta.selectionStart;
      ta.value = ta.value.slice(0, pos) + res.markdown + ta.value.slice(ta.selectionEnd);
      setDirty(true);
      toast(tx("attach.done", { name: res.name }));
    } catch (err) { toast(err.message, true); }
  }
}

// ---------- new task ----------

function openNew() {
  if (!state.project) { toast(t("new.need_project"), true); return; }
  $("new-form").reset();
  const st = suiteNow();
  const templates = fillTemplates();
  $("n-pipeline").replaceChildren(el("option", { value: "" }, t("common.none")), ...st.pipelines.map((pl) => el("option", { value: pl.name }, label("pipeline", pl.name))));
  newTemplate = null;
  $("n-custom").replaceChildren(...customInputs(st.fields || [], {}));
  $("n-custom").hidden = !(st.fields || []).length;
  applyShow("n");
  $("new-dialog").showModal();
  (templates.length ? $("n-template") : $("n-title")).focus();
}

// fillTemplates lists the suite's templates in the New task dialog and returns them.
function fillTemplates() {
  const templates = suiteNow().templates || [];
  $("n-template").replaceChildren(el("option", { value: "" }, t("common.none")), ...templates.map((x) => el("option", { value: x.name }, label("template", x.name))));
  $("n-template-row").hidden = templates.length === 0;
  return templates;
}

// saveNewAsTemplate adds what the New task form holds to the project's suite as a template (QM-40). The
// title is kept as typed: it is a title start, like "Bug: ".
async function saveNewAsTemplate() {
  const st = suiteNow();
  const name = (prompt(t("new.template_name", { suite: st.name })) || "").trim();
  if (!name) return;
  const tpl = {
    name, title: $("n-title").value, body: $("n-body").value, status: $("n-status").value, doer: $("n-doer").value,
    priority: $("n-priority").value, discipline: discKey($("n-discipline").value), labels: splitList($("n-labels").value), pipeline: $("n-pipeline").value,
  };
  try {
    await api("POST", `/api/suites/${encodeURIComponent(st.name)}/templates`, tpl);
    await refreshSuite();
    fillTemplates();
    $("n-template").value = name;
    newTemplate = (suiteNow().templates || []).find((x) => x.name === name) || null;
    toast(t("new.template_saved", { name, suite: st.name }));
  } catch (err) { toast(err.message, true); }
}

// The template last applied in the New task dialog: switching templates replaces what it filled in, but never
// what the person typed.
let newTemplate = null;

function applyNewTemplate() {
  const prev = newTemplate || {};
  const t = (suiteNow().templates || []).find((x) => x.name === $("n-template").value) || {};
  const swap = (id, from, to) => {
    const n = $(id);
    if (n.value === (from || "") || n.value.trim() === "") n.value = to || "";
  };
  swap("n-title", prev.title, t.title);
  swap("n-body", prev.body, t.body);
  swap("n-discipline", discShow(prev.discipline), discShow(t.discipline));
  // Selects always have a value: set them only from the template, and back to the default without one.
  $("n-status").value = t.status || "todo";
  $("n-doer").value = t.doer || "agent";
  $("n-priority").value = t.priority || "normal";
  $("n-pipeline").value = t.pipeline && [...$("n-pipeline").options].some((o) => o.value === t.pipeline) ? t.pipeline : "";
  const labels = splitList($("n-labels").value).filter((l) => !(prev.labels || []).includes(l));
  for (const l of t.labels || []) if (!labels.includes(l)) labels.push(l);
  $("n-labels").value = labels.join(", ");
  newTemplate = t.name ? t : null;
  applyShow("n");
  const title = $("n-title");
  title.focus();
  title.setSelectionRange(title.value.length, title.value.length);
}

async function createTask(e) {
  e.preventDefault();
  const body = {
    project: state.project, title: $("n-title").value.trim(), doer: $("n-doer").value, assignees: splitList($("n-assignees").value), status: $("n-status").value,
    priority: $("n-priority").value, discipline: discKey($("n-discipline").value), milestone: $("n-milestone").value.trim(),
    labels: splitList($("n-labels").value), body: $("n-body").value,
  };
  const pl = suiteNow().pipelines.find((x) => x.name === $("n-pipeline").value);
  if (pl) body.pipeline = { name: pl.name, at: pl.steps[0] };
  const fields = Object.fromEntries(Object.entries(customValues($("n-custom"))).filter(([, v]) => v));
  if (Object.keys(fields).length) body.fields = fields;
  if (!body.title) return;
  try {
    const res = await api("POST", "/api/tasks", body);
    $("new-dialog").close();
    toast(t("new.created", { id: res.id }));
    await loadBoard();
    select(res.id);
  } catch (err) { toast(err.message, true); }
}

// ---------- keyboard ----------

function visibleCards() { return [...document.querySelectorAll(".item")]; }

function moveSelection(dx, dy) {
  const cards = visibleCards();
  if (!cards.length) return;
  const cur = cards.find((c) => c.dataset.id === state.selected);
  if (!cur) { select(cards[0].dataset.id); return; }
  if (state.layout === "table") { // one list of rows, top to bottom
    const i = cards.indexOf(cur) + dy;
    if (dy && i >= 0 && i < cards.length) select(cards[i].dataset.id);
    return;
  }
  if (dy) {
    const col = cur.parentElement, list = [...col.querySelectorAll(".card")];
    const i = list.indexOf(cur) + dy;
    if (i >= 0 && i < list.length) select(list[i].dataset.id);
    return;
  }
  const cols = [...document.querySelectorAll(".column")];
  let ci = cols.indexOf(cur.parentElement);
  const row = [...cur.parentElement.querySelectorAll(".card")].indexOf(cur);
  for (ci += dx; ci >= 0 && ci < cols.length; ci += dx) {
    const list = [...cols[ci].querySelectorAll(".card")];
    if (list.length) { select(list[Math.min(row, list.length - 1)].dataset.id); return; }
  }
}

function onKey(e) {
  const a = document.activeElement;
  const typing = /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName) || a.isContentEditable;
  // On a focused button or link, Enter, Space and the digit keys belong to it, not to the selected card.
  const onControl = /^(BUTTON|A|SUMMARY)$/.test(a.tagName);
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
    e.preventDefault(); // never the browser's "save page" dialog
    if ($("suite-dialog").open) $("se-form").requestSubmit();
    else if (state.open) saveDrawer();
    return;
  }
  // A key some control already handled (Enter or Escape in a table cell, the search box's id jump) is done.
  if (e.defaultPrevented) return;
  if (e.key === "Escape") {
    if (document.querySelector("dialog[open]")) return;
    if (typing) { document.activeElement.blur(); return; }
    if (state.open) closeDrawer();
    return;
  }
  if (typing || e.ctrlKey || e.metaKey || e.altKey || document.querySelector("dialog[open]")) return;
  if (onControl && (e.key === "Enter" || e.key === " " || /^[1-5]$/.test(e.key))) return;
  if (e.key !== "Shift") hover.armed = false; // the keyboard acts on the selection now, not the mouse
  // Caps Lock must not change what a letter does: an unshifted letter is read lower-case.
  const key = e.key.length === 1 && !e.shiftKey ? e.key.toLowerCase() : e.key;
  switch (key) {
    case "/": e.preventDefault(); $("filter").focus(); $("filter").select(); break;
    case "n": e.preventDefault(); openNew(); break;
    case "b": setView("board"); break;
    case "m": case "M": // Shift+M toggles My tasks (with or without Caps Lock); a plain m opens milestones
      if (!e.shiftKey) setView("milestones");
      else if (state.view === "board") setMine(!state.mine);
      break;
    case "r": setView("releases"); break;
    case "i": setView("insights"); break;
    case "k": setView("knowledge"); break;
    case "?": $("help-dialog").showModal(); break;
    case "v": if (state.view === "board") { e.preventDefault(); $("view-pick").focus(); try { $("view-pick").showPicker(); } catch (_) { /* focus is enough */ } } break;
    case "p": case "d": case "l": case "a":
      if (state.view === "board" && state.selected && cardKey(key)) e.preventDefault(); // nothing done: key passes
      break;
    case "t": if (state.view === "board") setLayout(state.layout === "table" ? "board" : "table"); break;
    case "x":
      if (state.view === "board" && state.layout === "table" && state.selected) {
        if (state.checked.has(state.selected)) state.checked.delete(state.selected); else state.checked.add(state.selected);
        renderBoard();
      }
      break;
    case "]": if (state.open) setDrawerCollapsed(!$("drawer").classList.contains("collapsed")); break;
    case "[": setSidebar(!$("sidebar").classList.contains("collapsed")); break;
    case "c": {
      if (state.view !== "board" || state.layout === "table" || !state.selected) return;
      const t = state.tasks.find((x) => x.ID === state.selected);
      if (t) toggleColumn(t.Status);
      break;
    }
    case "g": {
      const order = ["", "discipline", "milestone"];
      state.group = order[(order.indexOf(state.group) + 1) % order.length];
      $("group").value = state.group; store("group", state.group); renderBoard();
      break;
    }
    case "ArrowLeft": case "ArrowRight": case "ArrowUp": case "ArrowDown": case "Enter":
      if (state.view !== "board") return; // the card keys act on the board only
      boardKey(e);
      break;
    default:
      if (state.view === "board" && /^[1-5]$/.test(e.key) && state.selected) setStatus(state.selected, STATUSES[+e.key - 1]);
  }
}

function boardKey(e) {
  switch (e.key) {
    case "ArrowLeft": e.preventDefault(); moveSelection(-1, 0); break;
    case "ArrowRight": e.preventDefault(); moveSelection(1, 0); break;
    case "ArrowUp": e.preventDefault(); moveSelection(0, -1); break;
    case "ArrowDown": e.preventDefault(); moveSelection(0, 1); break;
    case "Enter": if (state.selected) openTask(state.selected); break;
  }
}

// ---------- live updates ----------

let reloadTimer;
function scheduleReload(ev) {
  clearTimeout(reloadTimer);
  reloadTimer = setTimeout(async () => {
    await loadBoard();
    loadWarnings().catch(() => {});
    if (ev.kind === "project" || ev.kind === "suites" || ev.kind === "all") loadProjects().then(loadBoard).then(renderView).catch(() => {});
    else {
      if (ev.kind === "task") refreshCounts().catch(() => {});
      if (state.view !== "board") renderView();
    }
    if (ev.kind === "releases") loadReleases().catch(() => {});
  }, 150);
  const t = state.open;
  if (!t) return;
  const mine = ev.kind === "all" || (ev.kind === "task" && ev.id === t.id) || (ev.kind === "activity" && ev.project === t.project);
  if (mine) {
    clearTimeout(scheduleReload.drawerTimer);
    scheduleReload.drawerTimer = setTimeout(() => refreshOpen(t.id), 200);
  }
}

// refreshOpen reloads the open task after a live event. With unsaved edits it only warns, and only if the task
// file really changed (activity records, such as an agent's usage, do not touch the file).
async function refreshOpen(id) {
  if (!state.open || state.open.id !== id) return;
  if (!state.dirty) { openTask(id); return; }
  try {
    const cur = await api("GET", "/api/tasks/" + encodeURIComponent(id));
    if (state.open && state.open.id === id && state.dirty && cur.version !== state.open.version) showChanged(state.open, null);
  } catch (_) { /* gone or unreadable: the save will say so */ }
}

function connectEvents() {
  const es = new EventSource("/api/events");
  let opened = false;
  es.onopen = () => {
    $("live").classList.add("on");
    $("live").title = t("live.on");
    // Changes made while the stream was down sent no events: load everything again.
    if (opened) scheduleReload({ kind: "all" });
    opened = true;
  };
  es.onerror = () => {
    $("live").classList.remove("on");
    if (es.readyState === EventSource.CLOSED) {
      // The browser gives up after a refused reconnect: the server restarted with a new session.
      $("live").title = t("live.disconnected");
      toast(t("live.restarted"), true);
    } else $("live").title = t("live.reconnecting");
  };
  es.addEventListener("change", (m) => {
    let ev;
    try { ev = JSON.parse(m.data); } catch (_) { return; }
    pluginEvent(ev);
    if (ev.kind === "storage") return; // only plugins keep data there
    if (ev.kind === "sync") { loadSync(); loadWarnings().catch(() => {}); return; }
    const open = state.open && state.open.project;
    if (ev.project && ev.project !== state.project && ev.project !== open && ev.kind !== "project") {
      // Another project's task changed: only its sidebar count needs updating.
      if (ev.kind === "task") {
        clearTimeout(connectEvents.countTimer);
        connectEvents.countTimer = setTimeout(() => refreshCounts().catch(() => {}), 300);
      }
      return;
    }
    scheduleReload(ev);
  });
}

// ---------- theme ----------

function applyTheme(t) {
  if (t) document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
  pluginTheme();
}

// ---------- wiring ----------

function wire() {
  $("side-toggle").addEventListener("click", () => setSidebar(!$("sidebar").classList.contains("collapsed")));
  $("settings-open").addEventListener("click", openSettings);
  $("sync").addEventListener("click", syncNow);
  $("s-sync-auto").addEventListener("change", saveSyncSettings);
  $("s-sync-every").addEventListener("change", saveSyncSettings);
  wireProjectMenu();
  wireSuiteEditor();
  $("s-theme").addEventListener("change", (e) => { applyTheme(e.target.value); store("theme", e.target.value); });
  $("s-sidebar").addEventListener("change", (e) => setSidebar(e.target.checked));
  $("s-archive").addEventListener("change", async (e) => {
    const p = projectNow();
    const days = Math.max(0, Math.min(3650, parseInt(e.target.value || "0", 10) || 0));
    e.target.value = days;
    if (!p || days === (p.archive_days || 0)) return;
    try {
      const res = await api("PUT", `/api/projects/${encodeURIComponent(p.slug)}/archive`, { days });
      p.archive_days = days;
      toast(!days ? t("settings.archive_off") : res.archived ? t("settings.archive_set_now", { n: days, now: res.archived }) : t("settings.archive_set", { n: days }));
      if (res.archived) loadBoard();
    } catch (err) { toast(err.message, true); e.target.value = p.archive_days || 0; }
    archiveNote();
  });
  $("s-workdir").addEventListener("change", async (e) => {
    const p = projectNow();
    if (!p) return;
    const dir = e.target.value.trim();
    try {
      const res = await api("PUT", `/api/projects/${encodeURIComponent(p.slug)}/workdir`, { dir });
      p.workdir = res.workdir; p.workdir_pinned = res.workdir_pinned;
      toast(t(dir ? "settings.workdir_set" : "settings.workdir_cleared"));
      if (state.open) loadLocks(state.open);
    } catch (err) { toast(err.message, true); e.target.value = p.workdir_pinned ? p.workdir : ""; }
    workdirNote();
  });
  $("s-me").addEventListener("change", (e) => {
    store("me", e.target.value.trim());
    renderMine();
    if (state.mine) loadBoard();
  });
  $("mine").addEventListener("click", () => setMine(!state.mine));
  $("s-keys").addEventListener("click", () => { $("settings-dialog").close(); $("help-dialog").showModal(); });
  wireViews();
  let ft;
  $("filter").addEventListener("input", (e) => {
    clearTimeout(ft);
    const id = idInSearch(e.target.value);
    const hint = $("filter-hint");
    hint.hidden = !id;
    if (id) { hint.textContent = t("filter.jump", { id }); return; } // an id is a jump, not a filter
    ft = setTimeout(() => { state.filter = e.target.value.trim(); store("filter", state.filter); loadBoard(); }, 250);
  });
  $("filter").addEventListener("keydown", (e) => {
    if (e.key !== "Enter") return;
    e.preventDefault(); // handled here: Enter must not also open the selected card once the box loses focus
    const id = idInSearch(e.target.value);
    if (id) {
      e.target.value = state.filter; // back to the filter the board shows
      $("filter-hint").hidden = true;
      select(id, false);
      openTask(id);
    }
    e.target.blur();
  });
  $("filter").addEventListener("blur", () => {
    if (idInSearch($("filter").value)) { $("filter").value = state.filter; $("filter-hint").hidden = true; }
  });
  $("group").addEventListener("change", (e) => { state.group = e.target.value; store("group", state.group); renderBoard(); });
  $("sort").addEventListener("change", (e) => { state.sort = e.target.value; store("sort", state.sort); renderBoard(); });
  $("view-pick").addEventListener("change", (e) => {
    const c = viewChoices().find((x) => x.key === e.target.value);
    if (c) applyView(c.v);
    e.target.blur(); // back to the board keys
  });
  $("view-edit").addEventListener("click", openViewDialog);
  document.querySelectorAll("#layout button").forEach((b) => b.addEventListener("click", () => setLayout(b.dataset.layout)));
  $("board").addEventListener("focusout", () => setTimeout(() => { if (renderBoard.pending) renderBoard(); }));
  $("view-form").addEventListener("submit", saveView);
  $("v-cancel").addEventListener("click", () => $("view-dialog").close());
  $("v-delete").addEventListener("click", deleteView);
  $("v-suite").addEventListener("click", saveViewToSuite);
  $("n-save-template").addEventListener("click", saveNewAsTemplate);
  $("new").addEventListener("click", openNew);
  $("new-form").addEventListener("submit", createTask);
  $("n-template").addEventListener("change", applyNewTemplate);
  for (const ev of ["input", "change"]) {
    $("new-form").addEventListener(ev, (e) => { if (e.target.id !== "n-template") applyShow("n"); });
    $("drawer").addEventListener(ev, () => { if (state.open) applyShow("d"); });
  }
  $("n-more").addEventListener("click", () => toggleMoreFields("n"));
  $("d-more").addEventListener("click", () => toggleMoreFields("d"));
  $("n-cancel").addEventListener("click", () => $("new-dialog").close());
  $("warnings").addEventListener("click", () => $("warn-dialog").showModal());
  $("help").addEventListener("click", () => $("help-dialog").showModal());

  $("d-close").addEventListener("click", (e) => { e.stopPropagation(); closeDrawer(); });
  wireDrawerPanel();
  $("d-advance").addEventListener("click", () => drawerAction(async (t) => {
    if (state.dirty && !confirm(tx("drawer.discard_advance"))) return false;
    const btn = $("d-advance");
    btn.disabled = true; // a double click must not advance two steps
    try {
      // from: the step shown; the server refuses (409) if the task is no longer there.
      const res = await api("POST", `/api/tasks/${encodeURIComponent(t.id)}/advance`, { from: t.pipeline ? t.pipeline.at : "" });
      state.dirty = false;
      toast(tx("drawer.advanced", { id: t.id, step: label("step", res.at) }));
    } finally { btn.disabled = false; }
  }));
  for (const id of Object.keys(fieldMap)) {
    $(id).addEventListener("input", () => setDirty(true));
    $(id).addEventListener("change", () => setDirty(true));
  }
  $("d-body").addEventListener("paste", onPaste);
  $("d-save").addEventListener("click", saveDrawer);
  $("d-preview-toggle").addEventListener("click", () => { state.preview = !state.preview; renderPreview(); });
  $("d-note-add").addEventListener("click", () => drawerAction(async (t) => {
    const text = $("d-note").value.trim();
    if (!text) throw new Error(tx("drawer.note_empty"));
    await api("POST", `/api/tasks/${encodeURIComponent(t.id)}/notes`, { text });
    $("d-note").value = "";
  }, t("drawer.note_added")));
  $("c-add").addEventListener("click", () => drawerAction(async (t) => {
    const tokens = parseInt($("c-tokens").value || "0", 10) || 0;
    await api("POST", `/api/tasks/${encodeURIComponent(t.id)}/usage`, { tokens, time: $("c-time").value.trim(), note: $("c-note").value.trim() });
    $("c-tokens").value = $("c-time").value = $("c-note").value = "";
  }, t("drawer.correction_added")));
  $("d-release").addEventListener("click", () => drawerAction(async (t) => {
    const note = prompt(tx("drawer.release_prompt", { id: t.id }), "released from the web UI"); // i18n-ok: the default note is written into the task file, which stays English (§4.4)
    if (note === null) return false;
    await api("POST", `/api/tasks/${encodeURIComponent(t.id)}/release`, { note });
  }, t("drawer.released")));
  $("d-archive").addEventListener("click", () => drawerAction(async (t) => {
    if (state.dirty && !confirm(tx("drawer.archive_discard", { id: t.id }))) return false;
    await api("POST", `/api/tasks/${encodeURIComponent(t.id)}/archive`);
    state.dirty = false;
    closeDrawer();
  }, t("drawer.archived")));
  $("d-move").addEventListener("click", () => drawerAction(async (t) => {
    const to = $("d-move-to").value;
    if (!to || !confirm(tx("drawer.move_confirm", { id: t.id, to }))) return false;
    const res = await api("POST", `/api/tasks/${encodeURIComponent(t.id)}/move`, { to });
    state.dirty = false;
    closeDrawer();
    toast(tx(res.warning ? "drawer.moved_warn" : "drawer.moved", { id: res.id, warning: res.warning }), !!res.warning);
  }));
  $("d-delete").addEventListener("click", () => drawerAction(async (t) => {
    const typed = prompt(tx("drawer.delete_prompt", { id: t.id }));
    if (typed === null) return false;
    if (typed.trim().toUpperCase() !== t.id) throw new Error(tx("drawer.delete_mismatch"));
    await api("DELETE", `/api/tasks/${encodeURIComponent(t.id)}`);
    state.dirty = false;
    closeDrawer();
  }, t("drawer.deleted")));
  document.addEventListener("keydown", onKey);
  document.addEventListener("mousemove", onMouseMove, { passive: true });
  window.addEventListener("beforeunload", (e) => { if (state.dirty) e.preventDefault(); });
}

async function main() {
  applyTheme(store("theme"));
  wire();
  setSidebar(store("sidebar") === "collapsed");
  state.collapsedCols = new Set(readList("collapsedCols").filter((s) => STATUSES.includes(s)));
  state.allDone = store("allDone") === "1";
  state.collapsedLanes = new Set(readList("collapsedLanes"));
  state.group = store("group") || "";
  $("group").value = state.group;
  state.filter = store("filter") || "";
  $("filter").value = state.filter;
  state.sort = store("sort") || "";
  if (![...$("sort").options].some((o) => o.value === state.sort)) state.sort = "";
  $("sort").value = state.sort;
  state.fields = readList("fields"); // custom keys are checked against the suite where used
  state.layout = store("layout") === "table" ? "table" : "board";
  state.mine = store("mine") === "1";
  state.myViews = readViews();
  try {
    await loadLang();
    await loadMeta();
    renderMine();
    await loadProjects();
    await loadBoard();
    await loadWarnings();
    await loadSync();
    await loadPlugins();
    setView(store("view") || "board");
  } catch (e) { toast(e.message, true); }
  connectEvents();
  const hash = decodeURIComponent(location.hash.slice(1));
  if (/^[A-Z][A-Z0-9]*-\d+$/.test(hash)) openTask(hash);
}

// ---------- suite wording ----------

// applyWords fills the page's text again: catalog keys whose text uses the project's suite words ({release},
// {Releases}, …) change with the project and the language.
function applyWords() {
  applyI18n();
  $("d-asset").placeholder = suiteNow().words.item === "asset" ? "/Game/Props/SM_..." : word("item"); // i18n-ok: an Unreal path
}

// renderLangPicker fills the Settings language list: Auto, then every language with a catalog.
function renderLangPicker() {
  const sel = $("s-lang");
  sel.replaceChildren(el("option", { value: "" }, t("settings.lang_auto")), ...i18n.langs.map((l) => el("option", { value: l.code }, l.name)));
  sel.value = store("lang") || "";
}

// setLang switches the UI language in this browser and draws everything again.
async function setLang(code) {
  store("lang", code);
  try { await loadLang(); } catch (e) { toast(e.message, true); return; }
  await loadPlugins();
  fillProjectLists();
  renderSidebar(); renderMine(); renderPreview();
  renderBoard(); renderView();
  if (state.open && !state.dirty) fillDrawer(state.open);
  loadWarnings().catch(() => {});
  openSettings();
}

// stepBar draws a pipeline's steps with the current one marked.
function stepBar(name, at, big) {
  const def = suiteNow().pipelines.find((x) => x.name.toLowerCase() === (name || "").toLowerCase());
  const steps = def ? def.steps : [at];
  const i = steps.indexOf(at);
  const bar = el("div", { class: "steps" + (big ? " big" : ""), title: t("steps.title", { name: label("pipeline", name), step: label("step", at), i: i + 1, n: steps.length }) },
    steps.map((s, j) => el("span", { class: "step" + (j < i ? " done" : j === i ? " now" : "") }, big ? label("step", s) : "")));
  if (!big) bar.append(el("span", { class: "step-label" }, label("step", at)));
  return bar;
}

// ---------- views ----------

function setView(v) {
  if (!["board", "milestones", "releases", "insights", "knowledge"].includes(v) && !pluginViews().some((x) => x.key === v)) v = "board";
  state.view = v;
  store("view", v);
  document.querySelectorAll(".tabs button").forEach((b) => b.classList.toggle("on", b.dataset.view === v));
  $("plugin-view").hidden = !v.startsWith("plugin:");
  $("board").hidden = v !== "board";
  $("empty").hidden = v !== "board" || state.tasks.length > 0;
  $("milestones-view").hidden = v !== "milestones";
  $("releases-view").hidden = v !== "releases";
  $("insights-view").hidden = v !== "insights";
  $("knowledge-view").hidden = v !== "knowledge";
  document.querySelector(".filter").style.visibility = v === "board" ? "" : "hidden";
  $("board-bar").hidden = v !== "board";
  renderView();
}

function renderView() {
  if (state.view === "milestones") renderMilestones().catch((e) => toast(e.message, true));
  if (state.view === "releases") renderReleases().catch((e) => toast(e.message, true));
  if (state.view === "insights") renderInsights().catch((e) => toast(e.message, true));
  if (state.view === "knowledge") renderKnowledge().catch((e) => toast(e.message, true));
  mountPlugin();
}

// ---------- UI plugins (spec §8.5) ----------

// Plugins come from the server; which ones are on is chosen in Settings and kept in this browser. A plugin view
// is a sandboxed iframe that reaches the board only through the messages handled here.
const plugins = { list: [], cats: {}, frame: null, view: null, key: "", stopped: new Set() }; // stopped: until the page reloads

const pluginOn = (name) => readList("plugins").includes(name);
const pluginViews = () => plugins.list.filter((p) => pluginOn(p.name)).flatMap((p) => p.views.map((v) => ({ plugin: p, v, key: `plugin:${p.name}/${v.id}` })));
const pluginText = (p, key, fallback) => (plugins.cats[p.name] || {})[key] || fallback;

// pluginCatalog loads a plugin's English text and the chosen language's over it (§4.4 fallback).
async function pluginCatalog(p) {
  const get = async (code) => {
    if (!p.langs.includes(code)) return {};
    try { const r = await fetch(`/plugins/${p.name}/i18n/${code}.json`); return r.ok ? await r.json() : {}; } catch (_) { return {}; }
  };
  const base = i18n.lang.split("-")[0];
  return Object.assign({}, await get("en"), base !== i18n.lang ? await get(base) : {}, await get(i18n.lang));
}

async function loadPlugins() {
  try { plugins.list = (await api("GET", "/api/plugins")).plugins; } catch (e) { plugins.list = []; toast(e.message, true); }
  plugins.cats = {};
  for (const p of plugins.list.filter((x) => pluginOn(x.name))) plugins.cats[p.name] = await pluginCatalog(p);
  renderPluginTabs();
}

function renderPluginTabs() {
  document.querySelectorAll(".tabs .plugin-tab").forEach((b) => b.remove());
  const nav = document.querySelector(".tabs");
  for (const pv of pluginViews()) {
    const title = pluginText(pv.plugin, "view." + pv.v.id, pv.v.title);
    nav.append(el("button", { class: "plugin-tab" + (state.view === pv.key ? " on" : ""), "data-view": pv.key, title: pv.plugin.title, onclick: () => setView(pv.key) },
      pv.v.icon ? pv.v.icon + " " : "", title));
  }
}

async function setPluginOn(name, on) {
  const list = new Set(readList("plugins"));
  if (on) list.add(name); else list.delete(name);
  store("plugins", JSON.stringify([...list]));
  await loadPlugins();
  if (!on && state.view.startsWith(`plugin:${name}/`)) setView("board");
}

// THEME_VARS are the page's CSS variables a plugin view gets, so it looks like the rest of the UI.
const THEME_VARS = ["--bg", "--panel", "--card", "--line", "--text", "--muted", "--accent", "--accent-weak", "--on-accent", "--danger", "--warn", "--warn-bg", "--ok",
  "--p-urgent", "--p-high", "--p-low"];
function themeVars() {
  const cs = getComputedStyle(document.documentElement);
  const out = Object.fromEntries(THEME_VARS.map((k) => [k, cs.getPropertyValue(k).trim()]));
  out["--font"] = getComputedStyle(document.body).fontFamily;
  return out;
}
function pluginTheme() {
  if (plugins.frame) plugins.frame.contentWindow.postMessage({ qm: "theme", theme: themeVars() }, "*");
}

// mountPlugin shows the current plugin view, loading its frame again only when the view, project or language
// changed.
function mountPlugin() {
  const pv = pluginViews().find((x) => x.key === state.view);
  const box = $("plugin-view");
  if (!pv) { box.replaceChildren(); plugins.frame = null; plugins.key = ""; return; }
  const key = [pv.key, pv.v.scope === "project" ? state.project : "", i18n.lang].join("|");
  if (key === plugins.key && plugins.frame) return;
  plugins.key = key;
  plugins.view = pv;
  if (plugins.stopped.has(pv.plugin.name)) { box.replaceChildren(el("div", { class: "empty" }, t("plugin.navigated", { plugin: pv.plugin.title }))); plugins.frame = null; return; }
  if (pv.v.scope === "project" && !state.project) { box.replaceChildren(el("div", { class: "empty" }, t("new.need_project"))); plugins.frame = null; return; }
  const f = el("iframe", { class: "plugin-frame", sandbox: "allow-scripts allow-downloads", title: pluginText(pv.plugin, "view." + pv.v.id, pv.v.title),
    src: `/plugins/${pv.plugin.name}/_qm/frame.html?view=${encodeURIComponent(pv.v.id)}` });
  // The sandbox cannot stop a frame from navigating itself, which could carry board data to another site
  // (spec §8.5). Its first load is its own page; any later one means it left: remove it and say so.
  let loads = 0;
  f.addEventListener("load", () => {
    if (++loads === 1) return;
    plugins.stopped.add(pv.plugin.name);
    if (plugins.frame === f) plugins.frame = null;
    f.remove();
    box.replaceChildren(el("div", { class: "empty" }, t("plugin.navigated", { plugin: pv.plugin.title })));
    toast(t("plugin.navigated", { plugin: pv.plugin.title }), true);
  });
  plugins.frame = f;
  box.replaceChildren(f);
  // A frame that never says "ready" (blocked by the browser, a script that does not run) would stay blank:
  // say so, with a way to try again (QM-53).
  setTimeout(() => {
    if (plugins.frame !== f || f.dataset.ready) return;
    box.replaceChildren(el("div", { class: "empty" }, t("plugin.no_start", { plugin: pv.plugin.title }), " ",
      el("button", { class: "small", onclick: () => { plugins.key = ""; mountPlugin(); } }, t("plugin.retry"))));
    plugins.frame = null;
  }, PLUGIN_START_WAIT);
}

// PLUGIN_START_WAIT is how long a plugin frame has to say "ready" before the view says it did not start.
const PLUGIN_START_WAIT = 5000;

function pluginEvent(ev) {
  const pv = plugins.view;
  if (!plugins.frame || !pv) return;
  if (pv.v.scope === "project" && ev.project && ev.project !== state.project && ev.kind !== "all") return;
  if (ev.kind === "storage" && ev.id !== pv.plugin.name) return;
  plugins.frame.contentWindow.postMessage({ qm: "event", type: "change", data: ev }, "*");
}

// pluginSuite is the suite with its labels in the chosen language (api.suite()).
function pluginSuite() {
  const st = suiteNow();
  const map = (kind, keys) => Object.fromEntries((keys || []).map((k) => [k, label(kind, k)]));
  const steps = [...new Set((st.pipelines || []).flatMap((p) => p.steps))];
  return Object.assign({}, st, {
    labels: { statuses: map("status", STATUSES), doers: map("doer", ["agent", "person"]), priorities: map("priority", ["urgent", "high", "normal", "low"]),
      disciplines: map("discipline", st.disciplines), pipelines: map("pipeline", (st.pipelines || []).map((p) => p.name)), steps: map("step", steps) },
    wording: Object.fromEntries(["release", "releases", "target", "item", "feedback", "discipline"].map((n) => [n, word(n)])),
  });
}

// newTaskFrom opens the New task dialog filled in from a plugin (api.ui.newTask): the person creates the task.
function newTaskFrom(f) {
  openNew();
  if (!$("new-dialog").open) return;
  const set = (id, v) => { if (v != null && v !== "") $(id).value = Array.isArray(v) ? v.join(", ") : String(v); };
  set("n-title", f.title); set("n-body", f.body); set("n-milestone", f.milestone); set("n-labels", f.labels); set("n-assignees", f.assignees);
  if (f.discipline) $("n-discipline").value = discShow(f.discipline);
  for (const [id, v] of [["n-priority", f.priority], ["n-doer", f.doer], ["n-status", f.status]]) {
    if (v && [...$(id).options].some((o) => o.value === v)) $(id).value = v;
  }
}

async function pluginCall(pv, method, args) {
  const p = pv.plugin;
  const scoped = pv.v.scope === "project" ? state.project : "";
  const need = (perm) => { if (perm !== "read" && !p.permissions.includes(perm)) throw new Error(t("plugin.denied", { plugin: p.name, perm })); };
  const storePath = (key) => `/api/plugins/${p.name}/storage${key === undefined ? "" : "/" + key.split("/").map(encodeURIComponent).join("/")}?` +
    new URLSearchParams(scoped ? { project: scoped } : {});
  const proj = projectNow();
  switch (method) {
    case "project": return proj && { slug: proj.slug, key: proj.key, name: proj.name, milestones: proj.milestones || [] };
    case "tasks.find": {
      const q = { q: String(args[0] || "") };
      if (scoped) q.project = scoped;
      if (args[1] && args[1].archived) q.archived = "yes";
      return api("GET", "/api/tasks?" + new URLSearchParams(q));
    }
    case "tasks.get": return api("GET", "/api/tasks/" + encodeURIComponent(String(args[0])));
    case "activity": {
      const o = args[0] || {};
      const q = { follow: "yes" };
      if (scoped) q.project = scoped;
      if (o.since) q.since = String(o.since);
      if (o.until) q.until = String(o.until);
      if (o.kinds) q.kinds = [].concat(o.kinds).join(",");
      return api("GET", "/api/activity?" + new URLSearchParams(q));
    }
    case "releases": return proj ? api("GET", `/api/projects/${encodeURIComponent(proj.slug)}/releases`) : [];
    case "milestones": return (proj && proj.milestones) || [];
    case "suite": return pluginSuite();
    case "storage.list": need("storage"); return api("GET", storePath() + "&" + new URLSearchParams({ prefix: String(args[0] || "") }));
    case "storage.get": need("storage"); return api("GET", storePath(String(args[0])));
    case "storage.put": need("storage"); return api("PUT", storePath(String(args[0])), args[1] === undefined ? null : args[1]);
    case "storage.delete": need("storage"); return api("DELETE", storePath(String(args[0])));
    case "storage.putBlob": {
      need("storage");
      const b = args[1];
      if (!(b instanceof Blob)) throw new Error(t("plugin.not_blob"));
      return api("PUT", storePath(String(args[0])).replace("/storage/", "/blobs/"), undefined, { data: b, type: b.type });
    }
    case "storage.blob": {
      // The frame makes its own blob: URL from this (its origin cannot read ours).
      need("storage");
      const res = await fetch(storePath(String(args[0])).replace("/storage/", "/blobs/"));
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || res.statusText);
      return res.blob();
    }
    case "ui.openTask": need("ui"); openTask(String(args[0])); return null;
    case "ui.newTask": need("ui"); newTaskFrom(args[0] || {}); return null;
    case "ui.toast": need("ui"); toast(String(args[0]).slice(0, 300)); return null;
    case "ui.openLink": {
      need("ui");
      const u = new URL(String(args[0]));
      if (u.protocol !== "http:" && u.protocol !== "https:") throw new Error(t("plugin.bad_link"));
      window.open(u.href, "_blank", "noopener,noreferrer");
      return null;
    }
  }
  throw new Error(t("plugin.unknown_call", { method: String(method) }));
}

window.addEventListener("message", async (e) => {
  const pv = plugins.view;
  if (!plugins.frame || !pv || e.source !== plugins.frame.contentWindow) return;
  const m = e.data || {};
  const reply = (msg) => { if (plugins.frame && e.source === plugins.frame.contentWindow) e.source.postMessage(msg, "*"); };
  if (m.qm === "ready") {
    plugins.frame.dataset.ready = "1";
    reply({ qm: "init", entry: pv.plugin.entry, lang: i18n.lang, timezone: (state.meta && state.meta.timezone) || "", theme: themeVars(), catalog: plugins.cats[pv.plugin.name] || {},
      view: { id: pv.v.id, title: pv.v.title, scope: pv.v.scope } });
  } else if (m.qm === "call") {
    try { reply({ qm: "result", id: m.id, ok: true, value: await pluginCall(pv, m.method, m.args || []) }); }
    catch (err) { reply({ qm: "result", id: m.id, ok: false, error: err.message }); }
  } else if (m.qm === "error") {
    $("plugin-view").replaceChildren(el("div", { class: "empty" }, t("plugin.failed", { plugin: pv.plugin.title, error: String(m.error) })));
    plugins.frame = null;
  }
});

function renderPluginSettings() {
  const box = $("s-plugins");
  if (!plugins.list.length) { box.replaceChildren(el("div", { class: "muted" }, t("settings.plugins_none"))); return; }
  box.replaceChildren(...plugins.list.map((p) => el("label", { class: "plugin-row" },
    el("input", { type: "checkbox", checked: pluginOn(p.name), onchange: (e) => setPluginOn(p.name, e.target.checked).catch((err) => toast(err.message, true)) }),
    el("span", {}, el("b", {}, pluginText(p, "title", p.title)), " ", el("span", { class: "muted" }, p.version || ""),
      el("div", { class: "muted small-note" }, t(p.builtin ? "settings.plugin_builtin" : "settings.plugin_folder"), " · ",
        t("settings.plugin_perms", { perms: ["read", ...p.permissions.filter((x) => x !== "read")].map((x) => t("perm." + x)).join(", ") }))))));
}

// ---------- knowledge base (§4.5) ----------

const kb = { page: "", editing: false, data: null };

// The server's reasons for "unknown", shown in the chosen language.
const KB_REASONS = {
  "no file sources": "kb.r_nosources", "checked without a revision": "kb.r_norev", "no working copy to compare with": "kb.r_noworkdir",
  "the working copy is not a git repository": "kb.r_notgit", "the checked revision is not in this working copy": "kb.r_notinclone",
};

function freshBadge(f) {
  const tip = f.status === "stale" ? t("kb.stale_title", { changed: (f.changed || []).join(", ") })
    : f.status === "unknown" ? (KB_REASONS[f.reason] ? t(KB_REASONS[f.reason]) : f.reason || "") : t("kb.fresh_title");
  return el("span", { class: "chip kb-" + f.status, title: tip }, t("kb." + f.status));
}

async function renderKnowledge() {
  if (!state.project || kb.editing) return;
  const terms = [$("kb-q").value.trim(), $("kb-retired").checked ? "retired:yes" : ""].filter(Boolean).join(" ");
  const res = await api("GET", "/api/kb?" + new URLSearchParams({ project: state.project, q: terms }));
  const list = $("kb-list");
  if (!res.length) {
    list.replaceChildren(el("div", { class: "empty" }, t(terms ? "kb.none_found" : "kb.empty")));
  } else {
    list.replaceChildren(...res.map((r) => el("button", {
      class: "kb-item" + (r.slug === kb.page ? " on" : "") + (r.retired ? " retired" : ""), onclick: () => showPage(r.slug),
    }, el("div", { class: "kb-item-title" }, r.title), el("div", { class: "kb-item-meta" }, freshBadge(r.freshness),
      r.retired ? el("span", { class: "chip" }, t("kb.retired")) : null, ...(r.tags || []).map((x) => el("span", { class: "chip" }, x))))));
  }
  if (kb.page) await showPage(kb.page, true);
  else $("kb-page").replaceChildren(el("div", { class: "muted kb-hint" }, t("kb.pick")));
}

async function showPage(slug, quiet) {
  let pg;
  try { pg = await api("GET", `/api/kb/${encodeURIComponent(state.project)}/${encodeURIComponent(slug)}`); }
  catch (e) { if (!quiet) toast(e.message, true); kb.page = ""; return; }
  kb.page = slug; kb.data = pg; kb.editing = false;
  document.querySelectorAll(".kb-item").forEach((n) => n.classList.toggle("on", n.textContent.startsWith(pg.title)));
  const c = pg.checked;
  const checked = c && c.at ? t("kb.checked", { when: fmtDateTime(c.at), by: c.by || "?", rev: (c.rev || "–").slice(0, 10) }) : t("kb.never_checked");
  const sources = (pg.sources || []).map((s) => (s.task ? el("a", { href: "#", onclick: (e) => { e.preventDefault(); openTask(s.task); } }, s.task) : el("code", {}, s.path)));
  const body = el("div", { class: "preview kb-body" });
  body.innerHTML = renderMarkdown(pg.body || "", state.project, "");
  $("kb-page").replaceChildren(...[ // replaceChildren would show a null as the text "null"
    el("div", { class: "kb-head" }, el("h3", {}, pg.title), freshBadge(pg.freshness), pg.retired ? el("span", { class: "chip" }, t("kb.retired")) : null,
      el("span", { class: "spacer" }),
      el("button", { class: "small", onclick: () => editPage(pg) }, t("kb.edit")),
      el("button", { class: "small", onclick: () => savePage(pg.slug, { retired: !pg.retired, version: pg.version }, pg.retired ? "kb.restored" : "kb.retired_toast") },
        t(pg.retired ? "kb.restore" : "kb.retire"))),
    pg.question ? el("div", { class: "kb-question" }, t("kb.question", { question: pg.question })) : null,
    (pg.tags || []).length ? el("div", { class: "chips" }, pg.tags.map((x) => el("span", { class: "chip" }, x))) : null,
    sources.length ? el("div", { class: "kb-sources" }, el("span", { class: "muted" }, t("kb.sources") + " "), ...sources.flatMap((n, i) => (i ? [", ", n] : [n]))) : null,
    el("div", { class: "muted kb-checked" }, checked),
    body].filter(Boolean));
}

// editPage shows the page form: an existing page (pg), or a new one (pg null).
function editPage(pg) {
  kb.editing = true;
  const field = (key, input) => el("label", {}, el("span", {}, t(key)), input);
  const title = el("input", { value: pg ? pg.title : "", required: true });
  const question = el("input", { value: pg ? pg.question || "" : "" });
  const tags = el("input", { value: pg ? (pg.tags || []).join(", ") : "", spellcheck: "false" });
  const sources = el("textarea", { rows: 3, spellcheck: "false" });
  sources.value = pg ? (pg.sources || []).map((s) => (s.task ? "task:" + s.task : s.path)).join("\n") : "";
  const body = el("textarea", { rows: 16 });
  body.value = pg ? pg.body || "" : "";
  const save = async (e) => {
    e.preventDefault();
    const data = { title: title.value, question: question.value, body: body.value, tags: splitList(tags.value),
      sources: sources.value.split("\n").map((x) => x.trim()).filter(Boolean) };
    if (pg) await savePage(pg.slug, { ...data, version: pg.version }, "kb.saved");
    else await createPage(data);
  };
  $("kb-page").replaceChildren(el("form", { class: "kb-form", onsubmit: save },
    field("kb.f_title", title), field("kb.f_question", question), field("kb.f_tags", tags), field("kb.f_sources", sources), field("kb.f_body", body),
    el("div", { class: "dialog-buttons" },
      el("button", { type: "button", onclick: () => { kb.editing = false; if (pg) showPage(pg.slug); else renderKnowledge(); } }, t("common.cancel")),
      el("button", { class: "primary" }, t("kb.save")))));
  title.focus();
}

async function savePage(slug, data, msg) {
  try {
    const pg = await api("PUT", `/api/kb/${encodeURIComponent(state.project)}/${encodeURIComponent(slug)}`, data);
    kb.editing = false;
    toast(t(msg, { title: pg.title }));
    kb.page = pg.slug;
    await renderKnowledge();
  } catch (e) { toast(e.message, true); }
}

async function createPage(data) {
  try {
    const pg = await api("POST", `/api/kb/${encodeURIComponent(state.project)}`, data);
    kb.editing = false;
    toast(t("kb.created", { title: pg.title }));
    kb.page = pg.slug;
    await renderKnowledge();
  } catch (e) { toast(e.message, true); }
}

// loadTaskKB fills the drawer's Knowledge section: the pages that name the open task as a source.
async function loadTaskKB(id) {
  const box = $("d-kb");
  let pages = [];
  try { pages = await api("GET", "/api/tasks/" + encodeURIComponent(id) + "/kb"); } catch (_) { /* shown as none */ }
  if (!state.open || state.open.id !== id) return;
  box.replaceChildren(...(pages.length ? pages.map((r) => el("div", { class: "kb-link" },
    el("a", { href: "#", onclick: (e) => { e.preventDefault(); kb.page = r.slug; setView("knowledge"); } }, r.title), " ", freshBadge(r.freshness)))
    : [el("div", { class: "muted" }, t("drawer.kb_none"))]));
}

// ---------- insights (§8.4) ----------

const pct = (r) => (r == null ? "–" : new Intl.NumberFormat(i18n.lang, { style: "percent" }).format(r));
const usd = (v) => (v == null ? "–" : fmtUSD(v));
const med = (s, n) => (n ? fmtDuration(s) || t("dur.m", { m: 0 }) : "–");

// Each metric: its catalog key (ins.<key> and ins.<key>_help), and its value and sample size from a Metrics object.
const INSIGHT_ROWS = [
  ["first_pass", (m) => [pct(m.first_pass_rate), m.decided]],
  ["cost_accepted", (m) => [usd(m.cost_per_accepted), m.done]],
  ["waste", (m) => [usd(m.waste_usd), m.waste_tasks]],
  ["rework", (m) => [m.reviewed ? t("ins.rework_value", { moves: m.rework_moves, tok: fmtTokens(m.rework_tokens), cost: usd(m.rework_usd) }) : "–", m.reviewed]],
  ["reopened", (m) => [pct(m.reopen_rate) + (m.regressions ? " " + t("ins.regressions", { n: m.regressions }) : ""), m.reached_done]],
  ["abandoned", (m) => [pct(m.abandon_rate), m.claims]],
  ["review_wait", (m) => [med(m.review_wait_median_s, m.review_wait_n), m.review_wait_n]],
  ["notes", (m) => [m.notes_per_task == null ? "–" : new Intl.NumberFormat(i18n.lang, { maximumFractionDigits: 1, minimumFractionDigits: 1 }).format(m.notes_per_task), m.tasks]],
  ["claim_review", (m) => [med(m.claim_to_review_median_s, m.claim_to_review_n), m.claim_to_review_n]],
  ["review_done", (m) => [med(m.review_to_done_median_s, m.review_to_done_n), m.review_to_done_n]],
  ["coverage", (m) => [pct(m.coverage), m.worked]],
  ["measured", (m) => [pct(m.measured_share), m.with_usage]],
  ["total_cost", (m) => [usd(m.cost_usd), m.tasks]],
];

function insightCell(m, fn) {
  const [v, n] = fn(m);
  return el("td", { class: m.tasks < 5 ? "few" : "" }, v, el("span", { class: "n", title: t("ins.sample") }, " " + fmtNum(n)));
}

async function renderInsights() {
  if (!state.project) return;
  const since = $("ins-since").value, by = $("ins-by").value;
  const q = new URLSearchParams({ project: state.project, since, by });
  $("ins-raw").href = "/api/activity?" + new URLSearchParams({ project: state.project, since });
  const rep = await api("GET", "/api/insights?" + q);
  const box = $("ins-body");
  if (!rep.groups || !rep.groups.length) {
    box.replaceChildren(el("div", { class: "empty" }, t("ins.empty")));
    return;
  }
  const tables = rep.groups.map((g) => el("section", { class: "ins-group" },
    g.name ? el("h3", {}, rep.by === "discipline" ? label("discipline", g.name) : g.name) : null,
    el("table", { class: "insights" },
      el("tr", {}, el("th", {}, ""), el("th", {}, t(g.agent.multi_model ? "ins.agent_multi" : "ins.agent", { n: g.agent.tasks, multi: g.agent.multi_model })),
        el("th", {}, t("ins.person", { n: g.person.tasks }))),
      INSIGHT_ROWS.map(([key, fn]) => el("tr", {}, el("td", { title: t("ins." + key + "_help") }, t("ins." + key)), insightCell(g.agent, fn), insightCell(g.person, fn))))));
  const notes = [el("div", {}, t("ins.overhead", { cost: usd(rep.overhead_usd) }))];
  if (rep.unpriced && rep.unpriced.length) notes.push(el("div", { class: "warn-text" }, t("ins.unpriced", { models: rep.unpriced.map((m) => ({ "(total only)": t("ins.total_only"), "(no model)": t("ins.no_model") })[m] || m).join(", ") })));
  notes.push(el("div", { class: "muted" }, t("ins.note")));
  box.replaceChildren(...[insightQueue(rep.queue), ...tables, el("div", { class: "ins-notes" }, notes)].filter(Boolean)); // no queue: null, not the text "null"
}

// insightQueue draws the agent review queue per day: bar height = tasks waiting in review at the end of the day.
function insightQueue(days) {
  if (!days || !days.length) return null;
  const max = Math.max(1, ...days.map((d) => d.depth));
  return el("section", { class: "ins-queue" },
    el("h3", {}, t("ins.queue")),
    el("div", { class: "qbars" }, days.map((d) => {
      const bar = el("span", { class: "qbar", title: t("ins.queue_day", { day: fmtDate(d.day + "T12:00:00"), depth: d.depth, review: d.to_review, done: d.to_done }) });
      bar.style.height = Math.max(2, (d.depth / max) * 100) + "%"; // CSSOM, allowed by the page's CSP
      return bar;
    })));
}

// boardFilter switches to the board with a filter, e.g. milestone:alpha.
function boardFilter(f) {
  state.filter = f;
  $("filter").value = f;
  store("filter", f);
  setView("board");
  loadBoard();
}

const quoteTerm = (v) => (/[\s"]/.test(v) ? `"${v.replace(/"/g, "")}"` : v);

async function renderMilestones() {
  if (!state.project) return;
  const p = projectNow();
  // Suite selector.
  const suites = await api("GET", "/api/suites");
  const sel = $("suite-select");
  const cur = p.suite ? p.suite.name : "";
  sel.replaceChildren(...suites.suites.map((s) => el("option", { value: s.name }, `${s.title || s.name} (${s.name})`)));
  sel.value = cur;
  const se = $("suite-error");
  se.hidden = !(p.suite_error || suites.error);
  se.textContent = p.suite_error || suites.error || "";

  const list = await api("GET", `/api/projects/${encodeURIComponent(state.project)}/milestones`);
  const box = $("ms-list");
  if (!list.length) {
    box.replaceChildren(el("div", { class: "empty" }, t("ms.empty")));
    return;
  }
  const rows = list.map((m) => {
    const name = m.name || t("ms.unscheduled");
    const total = m.total || 0;
    const bar = el("div", { class: "msbar", title: STATUSES.map((s) => `${label("status", s)}: ${fmtNum(m.counts[s] || 0)}`).join(", ") },
      STATUSES.map((s) => {
        if (!m.counts[s]) return null;
        const seg = el("span", { class: "seg s-" + s });
        seg.style.flex = String(m.counts[s]); // CSSOM, allowed by the page's CSP (style attributes are not)
        return seg;
      }));
    const done = m.counts.done || 0;
    return el("div", { class: "ms-row" + (m.name ? "" : " unscheduled") },
      el("div", { class: "ms-name" },
        el("a", { href: "#", onclick: (e) => { e.preventDefault(); boardFilter(m.name ? "milestone:" + quoteTerm(m.name) : "-status:done"); } }, name),
        m.date ? el("span", { class: "muted" }, " · " + fmtDate(m.date + "T12:00:00")) : null,
        m.overdue ? el("span", { class: "chip bad" }, t("ms.overdue")) : null,
        m.name && !m.defined ? el("span", { class: "chip", title: t("ms.not_listed_title") }, t("ms.not_listed")) : null),
      el("div", { class: "ms-progress" }, total ? bar : el("span", { class: "muted" }, t("ms.no_tasks")),
        el("span", { class: "muted" }, total ? " " + t("ms.done_of", { done, total }) : "")),
      el("div", { class: "ms-spent" }, spentText(m.tokens, m.seconds, m.partly_self_reported) || el("span", { class: "muted" }, "—")),
      el("div", { class: "ms-open muted" }, m.open_tasks.length ? t("project.open_count", { open: m.open_tasks.length }) : ""));
  });
  box.replaceChildren(el("div", { class: "ms-row ms-head" }, el("div", {}, t("ms.col_name")), el("div", {}, t("ms.col_progress")), el("div", {}, t("ms.col_spent")), el("div", {}, "")), ...rows);
}

function msRow(m) {
  return el("div", { class: "row ms-edit-row" },
    el("input", { class: "ms-n", value: m.name || "", placeholder: t("ms.name_placeholder"), required: true }),
    el("input", { class: "ms-d", type: "date", value: m.date || "" }),
    el("button", { type: "button", class: "small", onclick: (e) => e.target.closest(".row").remove() }, t("ms.remove")));
}

function openMilestoneEditor() {
  const p = projectNow();
  $("ms-rows").replaceChildren(...(p.milestones || []).map(msRow));
  $("ms-form").hidden = false;
}

async function saveMilestones(e) {
  e.preventDefault();
  const ms = [...document.querySelectorAll(".ms-edit-row")].map((r) => ({ name: r.querySelector(".ms-n").value.trim(), date: r.querySelector(".ms-d").value }))
    .filter((m) => m.name);
  try {
    await api("PUT", `/api/projects/${encodeURIComponent(state.project)}/milestones`, ms);
    $("ms-form").hidden = true;
    toast(t("ms.saved"));
    await loadProjects();
    renderMilestones();
  } catch (err) { toast(err.message, true); }
}

async function loadReleases() {
  if (!state.project) { state.releases = []; return; }
  state.releases = await api("GET", `/api/projects/${encodeURIComponent(state.project)}/releases`);
  $("releases").replaceChildren(...state.releases.map((r) => el("option", { value: r.id }, r.target || "")));
}

async function renderReleases() {
  if (!state.project) return;
  await loadReleases();
  const box = $("rel-list");
  if (!state.releases.length) {
    box.replaceChildren(el("div", { class: "empty" }, t("rel.empty", wordVars())));
    return;
  }
  // Open tasks found in each release, from the loaded board when it is unfiltered, else counted per row on demand.
  const found = new Map();
  for (const t of state.filter ? [] : state.tasks) if (t.FoundIn && t.Status !== "done") found.set(t.FoundIn, (found.get(t.FoundIn) || 0) + 1);
  box.replaceChildren(el("table", { class: "releases" },
    el("tr", {}, el("th", {}, t("rel.col_id")), el("th", {}, word("target")), el("th", {}, t("rel.col_date")), el("th", {}, t("rel.col_revision")),
      el("th", {}, t("rel.col_notes")), el("th", {}, t("rel.col_open"))),
    state.releases.map((r) => el("tr", {},
      el("td", { class: "mono" }, r.id), el("td", {}, r.target || ""), el("td", {}, fmtDateTime(r.date)),
      el("td", { class: "mono" }, r.revision || ""), el("td", {}, r.notes || ""),
      el("td", {}, el("a", { href: "#", onclick: (e) => { e.preventDefault(); boardFilter("found:" + quoteTerm(r.id)); } },
        found.has(r.id) ? fmtNum(found.get(r.id)) : t("rel.show")))))));
}

async function recordRelease(e) {
  e.preventDefault();
  const body = { id: $("rel-id").value.trim(), target: $("rel-target").value.trim(), revision: $("rel-revision").value.trim(), notes: $("rel-notes").value.trim() };
  if (!body.id) return;
  try {
    await api("POST", `/api/projects/${encodeURIComponent(state.project)}/releases`, body);
    $("rel-form").reset();
    toast(t("rel.recorded", wordVars({ id: body.id })));
    renderReleases();
  } catch (err) { toast(err.message, true); }
}

async function fileFeedback(e) {
  e.preventDefault();
  const body = { project: state.project, text: $("fb-text").value.trim(), release: $("fb-release").value.trim(), reporter: $("fb-reporter").value.trim() };
  if (!body.text) return;
  try {
    const res = await api("POST", "/api/feedback", body);
    $("fb-dialog").close();
    toast(t(res.warning ? "fb.filed_warn" : "fb.filed", wordVars({ id: res.id, warning: res.warning })), !!res.warning);
  } catch (err) { toast(err.message, true); }
}

function wireViews() {
  document.querySelectorAll(".tabs button").forEach((b) => b.addEventListener("click", () => setView(b.dataset.view)));
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", pluginTheme);
  $("ms-edit").addEventListener("click", openMilestoneEditor);
  $("ms-add").addEventListener("click", () => $("ms-rows").append(msRow({})));
  $("ms-cancel").addEventListener("click", () => { $("ms-form").hidden = true; });
  $("ms-form").addEventListener("submit", saveMilestones);
  $("suite-select").addEventListener("change", async (e) => {
    const name = e.target.value;
    if (!confirm(t("suite.confirm", { name }))) { renderMilestones(); return; }
    try {
      await api("PUT", `/api/projects/${encodeURIComponent(state.project)}/suite`, { suite: name });
      await loadProjects();
      fillProjectLists();
      loadBoard();
      renderMilestones();
      toast(t("suite.set", { name }));
    } catch (err) { toast(err.message, true); }
  });
  $("rel-form").addEventListener("submit", recordRelease);
  for (const id of ["ins-since", "ins-by"]) {
    const v = store(id);
    if (v !== null && v !== undefined) $(id).value = v;
    $(id).addEventListener("change", (e) => { store(id, e.target.value); renderInsights().catch((err) => toast(err.message, true)); });
  }
  $("fb-open").addEventListener("click", () => { $("fb-form").reset(); $("fb-dialog").showModal(); $("fb-text").focus(); });
  $("fb-cancel").addEventListener("click", () => $("fb-dialog").close());
  $("fb-form").addEventListener("submit", fileFeedback);
  $("s-lang").addEventListener("change", (e) => setLang(e.target.value));
  let kq;
  $("kb-q").addEventListener("input", () => { clearTimeout(kq); kq = setTimeout(() => renderKnowledge().catch((e) => toast(e.message, true)), 250); });
  $("kb-q").addEventListener("keydown", (e) => { if (e.key === "Escape") e.target.blur(); e.stopPropagation(); });
  $("kb-retired").addEventListener("change", () => renderKnowledge().catch((e) => toast(e.message, true)));
  $("kb-new").addEventListener("click", () => { kb.page = ""; editPage(null); });
}

main();

// ---------- suite editor (QM-22, spec §4.1) ----------

// suiteEd is the suite file open in the editor: its name and the text as last loaded or saved.
const suiteEd = { name: "", text: "", builtin: false, version: "" }; // version: the file as opened, for stale saves

const suiteDirty = () => suiteEd.name !== "" && $("se-text").value !== suiteEd.text;

async function openSuiteEditor() {
  const p = projectNow();
  Object.assign(suiteEd, { name: "", text: "" });
  $("se-text").value = "";
  $("suite-dialog").showModal();
  try {
    const suites = await api("GET", "/api/suites");
    $("suite-names").replaceChildren(...suites.suites.map((s) => el("option", { value: s.name }, s.title || s.name)));
  } catch (err) { toast(err.message, true); }
  await loadSuiteFile((p && p.suite && p.suite.name) || "");
}

function suiteError(msg) {
  $("se-error").textContent = msg || "";
  $("se-error").hidden = !msg;
}

// loadSuiteFile opens the board's file for a suite; a name with no file starts empty (a new suite, or one
// that extends the built-in suite of that name).
async function loadSuiteFile(name) {
  name = name.trim();
  if (name === suiteEd.name) return;
  if (suiteDirty() && !confirm(t("se.discard", { name: suiteEd.name }))) { $("se-name").value = suiteEd.name; return; }
  $("se-name").value = name;
  suiteError("");
  if (!name) return;
  try {
    const f = await api("GET", `/api/suites/${encodeURIComponent(name)}/file`);
    Object.assign(suiteEd, { name, text: f.text, builtin: f.builtin, version: f.version || "" });
    $("se-text").value = f.text;
    $("se-base").textContent = f.base || "";
    $("se-base-box").hidden = !f.base;
    suiteFileState(f.exists);
  } catch (err) { suiteError(err.message); }
}

function suiteFileState(exists) {
  const path = `suites/${suiteEd.name}.toml`;
  $("se-note").textContent = t(suiteEd.builtin ? (exists ? "se.note_override" : "se.note_builtin") : (exists ? "se.note_custom" : "se.note_new"), { path });
  $("se-delete").hidden = !exists;
  $("se-delete").textContent = t(suiteEd.builtin ? "se.reset" : "se.delete");
}

// refreshSuite shows a suite change everywhere: lists, cards, the milestones view.
async function refreshSuite() {
  await loadProjects();
  fillProjectLists();
  loadBoard();
  if (state.view === "milestones") renderMilestones();
}

async function saveSuiteFile(e) {
  e.preventDefault();
  const typed = $("se-name").value.trim();
  if (typed !== suiteEd.name) { await loadSuiteFile(typed); return; } // Enter in the name field opens that suite
  if (!suiteEd.name) return;
  const text = $("se-text").value;
  try {
    const res = await api("PUT", `/api/suites/${encodeURIComponent(suiteEd.name)}/file`, { text, version: suiteEd.version });
    suiteEd.text = text;
    suiteEd.version = res.version;
    suiteError("");
    suiteFileState(true);
    toast(t("se.saved", { name: suiteEd.name }));
    refreshSuite().catch(() => {});
  } catch (err) {
    suiteError(err.message);
    // Someone else saved the file meanwhile: offer their version (the text typed here stays until then).
    if (err.status === 409 && confirm(t("se.changed_reload", { name: suiteEd.name }))) {
      const name = suiteEd.name;
      suiteEd.name = "";
      suiteEd.text = $("se-text").value; // not dirty: the person chose to replace it
      await loadSuiteFile(name);
    }
  }
}

async function deleteSuiteFile() {
  const { name, builtin } = suiteEd;
  if (!confirm(t(builtin ? "se.reset_confirm" : "se.delete_confirm", { name }))) return;
  try {
    await api("DELETE", `/api/suites/${encodeURIComponent(name)}/file`);
    toast(t(builtin ? "se.reset_done" : "se.deleted", { name }));
    suiteEd.name = "";
    await loadSuiteFile(name);
    refreshSuite().catch(() => {});
  } catch (err) { suiteError(err.message); }
}

function wireSuiteEditor() {
  $("suite-edit").addEventListener("click", openSuiteEditor);
  $("se-name").addEventListener("change", (e) => loadSuiteFile(e.target.value));
  $("se-form").addEventListener("submit", saveSuiteFile);
  $("se-delete").addEventListener("click", deleteSuiteFile);
  $("se-cancel").addEventListener("click", () => { if (!suiteDirty() || confirm(t("se.discard", { name: suiteEd.name }))) $("suite-dialog").close(); });
  $("suite-dialog").addEventListener("cancel", (e) => { if (suiteDirty() && !confirm(t("se.discard", { name: suiteEd.name }))) e.preventDefault(); });
}
