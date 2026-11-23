// reports (spec §8.5): charts for people, drawn as plain SVG from the plugin API only, each with a table and
// a CSV download. The chosen chart, period and filter are kept in the plugin's storage.
import * as calc from "./calc.mjs";

const PALETTE = ["#5b8def", "#f2a33a", "#4fb286", "#d0607a", "#9a7fd1", "#46a5b8", "#c9a227", "#7d8597"];
const STATUS_COLORS = { backlog: "#9aa0a6", todo: "#5b8def", doing: "#f2a33a", review: "#9a7fd1", done: "#4fb286" };
const CHARTS = ["cfd", "burnup", "throughput", "cycle", "bugs", "pipeline"];
const PERIODS = ["30", "90", "180", "365", ""];
const SETTINGS = "settings.json";
const NS = "http://www.w3.org/2000/svg";

let api, t;
let settings = { chart: "cfd", period: "90", filter: "", milestone: "", by: "discipline" };

function h(tag, attrs, ...kids) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith("on")) n.addEventListener(k.slice(2), v); else n.setAttribute(k, v === true ? "" : v);
  }
  for (const c of kids.flat()) if (c != null && c !== false) n.append(c instanceof Node ? c : String(c));
  return n;
}
function s(tag, attrs, ...kids) {
  const n = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs || {})) if (v != null) n.setAttribute(k, v);
  for (const c of kids.flat()) if (c != null) n.append(c instanceof Node ? c : document.createTextNode(String(c)));
  return n;
}

// ---------- SVG charts ----------

const W = 860, H = 300, M = { l: 44, r: 12, t: 12, b: 34 };

function frame(maxY, xLabels) {
  const svg = s("svg", { viewBox: `0 0 ${W} ${H}`, width: "100%", role: "img", class: "chart" });
  const ymax = Math.max(1, niceMax(maxY));
  const y = (v) => M.t + (H - M.t - M.b) * (1 - v / ymax);
  for (let i = 0; i <= 4; i++) {
    const v = (ymax * i) / 4;
    svg.append(s("line", { x1: M.l, x2: W - M.r, y1: y(v), y2: y(v), stroke: "var(--line)" }),
      s("text", { x: M.l - 6, y: y(v) + 4, "text-anchor": "end", class: "tick" }, fmt(v)));
  }
  const n = xLabels.length;
  const x = (i) => M.l + (n <= 1 ? 0 : ((W - M.l - M.r) * i) / (n - 1));
  const every = Math.max(1, Math.ceil(n / 8));
  xLabels.forEach((l, i) => { if (i % every === 0) svg.append(s("text", { x: x(i), y: H - 12, "text-anchor": "middle", class: "tick" }, l)); });
  return { svg, x, y };
}
// niceMax rounds the axis top up so its four ticks are whole numbers.
function niceMax(v) { if (v <= 40) return Math.ceil(v / 4) * 4; const p = 10 ** Math.floor(Math.log10(v)); return Math.ceil(v / p * 2) / 2 * p; }
const fmt = (v) => new Intl.NumberFormat(api.lang, { maximumFractionDigits: 1 }).format(v);

// areaChart draws stacked areas (stacked) or lines, one per series {name, color, values}.
function areaChart(xLabels, series, stacked) {
  const totals = xLabels.map((_, i) => series.reduce((a, sr) => a + (stacked ? sr.values[i] : 0), 0));
  const maxY = stacked ? Math.max(0, ...totals) : Math.max(0, ...series.flatMap((sr) => sr.values));
  const { svg, x, y } = frame(maxY, xLabels);
  const base = xLabels.map(() => 0);
  for (const sr of series) {
    const upper = sr.values.map((v, i) => (stacked ? base[i] + v : v));
    const pts = upper.map((v, i) => `${x(i)},${y(v)}`);
    if (stacked) {
      const bottom = base.map((v, i) => `${x(i)},${y(v)}`).reverse();
      svg.append(s("polygon", { points: [...pts, ...bottom].join(" "), fill: sr.color, "fill-opacity": 0.75 }, s("title", {}, sr.name)));
      upper.forEach((v, i) => { base[i] = v; });
    } else {
      svg.append(s("polyline", { points: pts.join(" "), fill: "none", stroke: sr.color, "stroke-width": 2 }, s("title", {}, sr.name)));
    }
  }
  return svg;
}

// barChart draws bars per row, one segment per key: stacked, or side by side (grouped).
function barChart(xLabels, keys, colors, rows, grouped) {
  const maxY = Math.max(0, ...rows.map((r) => (grouped ? Math.max(0, ...keys.map((k) => r[k] || 0)) : keys.reduce((a, k) => a + (r[k] || 0), 0))));
  const { svg, y } = frame(maxY, []);
  const n = rows.length, slot = (W - M.l - M.r) / Math.max(1, n), bw = Math.max(2, Math.min(48, slot * 0.7));
  rows.forEach((r, i) => {
    const cx = M.l + slot * i + slot / 2;
    let acc = 0;
    keys.forEach((k, j) => {
      const v = r[k] || 0;
      if (!v) return;
      const w = grouped ? bw / keys.length : bw, x0 = grouped ? cx - bw / 2 + w * j : cx - bw / 2;
      svg.append(s("rect", { x: x0, y: y(acc + v), width: w, height: y(acc) - y(acc + v), fill: colors[j % colors.length] },
        s("title", {}, `${xLabels[i]} · ${k}: ${v}`)));
      if (!grouped) acc += v;
    });
    if (n <= 16 || i % Math.ceil(n / 12) === 0) svg.append(s("text", { x: cx, y: H - 12, "text-anchor": "middle", class: "tick" }, xLabels[i]));
  });
  return svg;
}

function scatter(points, since, until, med) {
  const span = Math.max(1, until - since);
  const { svg, y } = frame(Math.max(1, ...points.map((p) => p.days)), []);
  const x = (d) => M.l + ((W - M.l - M.r) * (new Date(d) - since)) / span;
  if (med != null) svg.append(s("line", { x1: M.l, x2: W - M.r, y1: y(med), y2: y(med), stroke: "var(--accent)", "stroke-dasharray": "4 3" },
    s("title", {}, t("median", { days: fmt(med) }))));
  for (const p of points) {
    const c = s("circle", { cx: x(p.done + "T12:00:00"), cy: y(p.days), r: 4, fill: PALETTE[0], class: "pt" }, s("title", {}, `${p.id} · ${p.title} · ${fmt(p.days)}`));
    c.addEventListener("click", () => api.ui.openTask(p.id));
    svg.append(c);
  }
  [since, until].forEach((d, i) => svg.append(s("text", { x: i ? W - M.r : M.l, y: H - 12, "text-anchor": i ? "end" : "start", class: "tick" }, calc.dayKey(d))));
  return svg;
}

function legend(items) {
  return h("div", { class: "legend" }, items.map(([name, color]) => h("span", {}, h("i", { style: `background:${color}` }), name)));
}

// table shows rows with column headers; idCol links task ids to the drawer.
function table(rows, cols, heads, idCol) {
  return h("table", {}, h("thead", {}, h("tr", {}, heads.map((x) => h("th", {}, x)))),
    h("tbody", {}, rows.map((r) => h("tr", {}, cols.map((c) => h("td", {},
      c === idCol ? h("a", { href: "#", onclick: (e) => { e.preventDefault(); api.ui.openTask(r[c]); } }, r[c]) : typeof r[c] === "number" ? fmt(r[c]) : r[c]))))));
}

// ---------- page ----------

let root, body, data = null;

function period() {
  const until = new Date();
  if (settings.period) return { since: calc.addDays(until, -(Number(settings.period) - 1)), until };
  const first = Math.min(...(data ? data.tasks.map((x) => +new Date(x.Created)) : []), +until);
  return { since: calc.dayStart(new Date(first)), until };
}

async function save() { try { await api.storage.put(SETTINGS, settings); } catch (e) { api.ui.toast(e.message); } }

function controls() {
  const sel = (key, options, label) => h("label", {}, h("span", {}, label),
    h("select", { onchange: (e) => { settings[key] = e.target.value; save(); draw(); } },
      options.map(([v, l]) => h("option", { value: v, selected: settings[key] === v }, l))));
  const filter = h("input", { type: "search", value: settings.filter, placeholder: t("filter_placeholder"), spellcheck: "false" });
  filter.addEventListener("change", () => { settings.filter = filter.value.trim(); save(); load(); });
  const parts = [
    sel("chart", CHARTS.map((c) => [c, t("chart." + c)]), t("chart")),
    sel("period", PERIODS.map((p) => [p, p ? t("days", { n: Number(p) }) : t("all_time")]), t("period")),
    h("label", { class: "grow" }, h("span", {}, t("filter")), filter),
  ];
  if (settings.chart === "burnup") parts.push(sel("milestone", data.milestones.map((m) => [m.name, m.name]), t("milestone")));
  if (settings.chart === "throughput") parts.push(sel("by", [["discipline", t("by.discipline")], ["doer", t("by.doer")]], t("by")));
  return h("div", { class: "controls" }, parts);
}

function draw() {
  if (!data) return;
  if (settings.chart === "burnup" && !data.milestones.some((m) => m.name === settings.milestone)) settings.milestone = (data.milestones[0] || {}).name || "";
  const { since, until } = period();
  const { tasks, activity, suite } = data;
  const sl = (k) => (suite.labels.statuses || {})[k] || k;
  let chart, rows, cols, heads, idCol, file = settings.chart, note = null;
  switch (settings.chart) {
    case "cfd": {
      rows = calc.cumulativeFlow(tasks, activity, since, until);
      const order = [...calc.STATUSES].reverse();
      chart = [areaChart(rows.map((r) => r.day.slice(5)), order.map((st) => ({ name: sl(st), color: STATUS_COLORS[st], values: rows.map((r) => r[st]) })), true),
        legend(order.map((st) => [sl(st), STATUS_COLORS[st]]))];
      cols = ["day", ...calc.STATUSES]; heads = [t("col.day"), ...calc.STATUSES.map(sl)];
      break;
    }
    case "burnup": {
      if (!settings.milestone) { chart = h("p", { class: "muted" }, t("no_milestones")); rows = []; cols = heads = []; break; }
      rows = calc.burnUp(tasks, activity, settings.milestone, since, until);
      chart = [areaChart(rows.map((r) => r.day.slice(5)), [{ name: t("col.scope"), color: PALETTE[7], values: rows.map((r) => r.scope) },
        { name: t("col.done"), color: STATUS_COLORS.done, values: rows.map((r) => r.done) }], false),
        legend([[t("col.scope"), PALETTE[7]], [t("col.done"), STATUS_COLORS.done]])];
      const m = data.milestones.find((x) => x.name === settings.milestone);
      if (m && m.date) note = t("milestone_date", { date: m.date });
      cols = ["day", "scope", "done"]; heads = [t("col.day"), t("col.scope"), t("col.done")];
      file += "-" + settings.milestone;
      break;
    }
    case "throughput": {
      const res = calc.throughput(tasks, activity, settings.by, since, until);
      const gl = (g) => (settings.by === "doer" ? suite.labels.doers : suite.labels.disciplines)[g] || g;
      rows = res.rows;
      chart = [barChart(rows.map((r) => r.week.slice(5)), res.groups, PALETTE, rows), legend(res.groups.map((g, i) => [gl(g), PALETTE[i % PALETTE.length]]))];
      cols = ["week", ...res.groups, "total"]; heads = [t("col.week"), ...res.groups.map(gl), t("col.total")];
      break;
    }
    case "cycle": {
      rows = calc.cycleTimes(tasks, activity, since, until);
      const med = calc.median(rows.map((r) => r.days));
      chart = scatter(rows, since, until, med);
      if (med != null) note = t("median", { days: fmt(med) });
      cols = ["id", "title", "done", "days"]; heads = [t("col.id"), t("col.title"), t("col.done_on"), t("col.days")]; idCol = "id";
      break;
    }
    case "bugs": {
      rows = calc.bugsPerRelease(tasks, data.releases);
      chart = [barChart(rows.map((r) => r.release), ["found", "fixed"], [PALETTE[3], STATUS_COLORS.done], rows, true),
        legend([[t("col.found"), PALETTE[3]], [t("col.fixed"), STATUS_COLORS.done]])];
      cols = ["release", "found", "fixed"]; heads = [cap(suite.wording.release || "release"), t("col.found"), t("col.fixed")];
      note = t("bugs_note");
      break;
    }
    case "pipeline": {
      rows = calc.pipelineSteps(tasks, suite.pipelines || []);
      const pl = (p) => (suite.labels.pipelines || {})[p] || p, st = (x) => (suite.labels.steps || {})[x] || x;
      const shown = rows.map((r) => Object.assign({}, r, { label: `${pl(r.pipeline)} · ${st(r.step)}` }));
      const many = (suite.pipelines || []).length > 1;
      chart = rows.length ? barChart(shown.map((r) => (many ? r.label : st(r.step))), ["count"], [PALETTE[0]], shown) : h("p", { class: "muted" }, t("no_pipelines"));
      rows = shown.map((r) => Object.assign({}, r, { pipeline: pl(r.pipeline), step: st(r.step) }));
      cols = ["pipeline", "step", "count"]; heads = [t("col.pipeline"), t("col.step"), t("col.count")];
      note = t("pipeline_note");
      break;
    }
  }
  const dl = h("button", { onclick: () => api.download(`${file}.csv`, calc.csv(rows, cols), "text/csv") }, t("csv"));
  body.replaceChildren(controls(), h("div", { class: "chart-box" }, chart), h("div", { class: "row" }, note ? h("span", { class: "muted" }, note) : null,
    h("span", { class: "spacer" }), rows.length ? dl : null), rows.length ? table(rows, cols, heads, idCol) : h("p", { class: "muted" }, t("empty")));
}
const cap = (x) => (x ? x[0].toUpperCase() + x.slice(1) : x);

async function load() {
  try {
    const [tasks, activity, releases, milestones, suite] = await Promise.all([
      api.tasks.find(settings.filter, { archived: true }), api.activity({ kinds: ["status"] }), api.releases(), api.milestones(), api.suite()]);
    data = { tasks, activity, releases, milestones, suite };
    draw();
  } catch (e) {
    body.replaceChildren(h("p", { class: "error" }, e.message));
  }
}

export async function activate(a) {
  api = a; t = a.t;
  calc.setTimeZone(a.timezone); // days and weeks of the board's time zone, like Insights
  const css = h("link", { rel: "stylesheet", href: new URL("style.css", import.meta.url).href });
  document.head.append(css);
  root = h("div", { class: "reports" }, h("h2", {}, t("title")), body = h("div", {}, h("p", { class: "muted" }, t("loading"))));
  document.body.append(root);
  try { const saved = await api.storage.get(SETTINGS); if (saved) settings = Object.assign(settings, saved); } catch (_) { /* defaults */ }
  if (!CHARTS.includes(settings.chart)) settings.chart = "cfd";
  await load();
  let timer;
  api.on("change", (ev) => {
    if (!["task", "activity", "all", "releases", "project"].includes(ev.kind)) return;
    clearTimeout(timer);
    timer = setTimeout(load, 400);
  });
}
