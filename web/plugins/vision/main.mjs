// vision (spec §8.5): a canvas per project for direction – pillars, reference images, notes, links and task
// cards. Free placement, resize, zoom and pan; several canvases per project. Everything goes through the plugin
// API; each item is its own storage key (model.mjs).
import * as m from "./model.mjs";

let api, t;
const st = {
  canvases: [],      // [{id, name, view}]
  canvas: "",        // current canvas id
  items: new Map(),  // id -> item
  tasks: new Map(),  // task id -> {title, status} or null (missing)
  selected: "",
  editing: "",       // id of the note or frame being edited
  busy: false,       // a drag is going on: live reloads wait
  pendingReload: false,
};
let viewport, world, bar, form;

function h(tag, attrs, ...kids) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k.startsWith("on")) n.addEventListener(k.slice(2), v); else n.setAttribute(k, v === true ? "" : v);
  }
  for (const c of kids.flat()) if (c != null && c !== false) n.append(c instanceof Node ? c : String(c));
  return n;
}
const cur = () => st.canvases.find((c) => c.id === st.canvas);
const fail = (e) => api.ui.toast(e && e.message ? e.message : String(e));

// ---------- loading ----------

async function loadCanvases() {
  const keys = await api.storage.list("");
  const ids = m.canvasIds(keys);
  st.canvases = (await Promise.all(ids.map(async (id) => {
    const c = (await api.storage.get(m.canvasKey(id))) || {};
    return { id, name: c.name || id, view: c.view || { x: 0, y: 0, zoom: 1 } };
  }))).sort((a, b) => a.name.localeCompare(b.name));
  if (!cur()) st.canvas = (st.canvases[0] || {}).id || "";
}

async function loadItems() {
  if (!st.canvas) { st.items = new Map(); return; }
  const keys = await api.storage.list(m.itemsPrefix(st.canvas));
  const loaded = await Promise.all(keys.map(async (k) => {
    const id = m.itemId(st.canvas, k);
    const v = id && (await api.storage.get(k));
    return v && m.TYPES.includes(v.type) ? Object.assign({ id }, v) : null;
  }));
  const next = new Map(loaded.filter(Boolean).map((it) => [it.id, it]));
  if (st.editing && st.items.has(st.editing)) next.set(st.editing, st.items.get(st.editing)); // keep what is being typed
  st.items = next;
  await loadTasks();
}

async function loadTasks() {
  const ids = [...new Set([...st.items.values()].filter((it) => it.type === "task").map((it) => it.task))];
  await Promise.all(ids.map(async (id) => {
    try { const x = await api.tasks.get(id); st.tasks.set(id, { title: x.title || x.Title, status: x.status || x.Status }); }
    catch (_) { st.tasks.set(id, null); }
  }));
}

async function reload() {
  if (st.busy || st.editing) { st.pendingReload = true; return; }
  st.pendingReload = false;
  try { await loadCanvases(); await loadItems(); render(); } catch (e) { fail(e); }
}

// ---------- saving ----------

const save = (it) => api.storage.put(m.itemKey(st.canvas, it.id), m.clean(it)).catch(fail);
let viewTimer;
function saveView() {
  const c = cur();
  if (!c) return;
  clearTimeout(viewTimer);
  viewTimer = setTimeout(() => api.storage.put(m.canvasKey(c.id), { name: c.name, view: c.view }).catch(fail), 800);
}
const topZ = () => Math.max(0, ...[...st.items.values()].map((it) => it.z | 0));

// add puts a new item at world point (x, y) – the viewport centre when not given.
async function add(fields, x, y) {
  if (!st.canvas) await newCanvas(t("canvas_default"));
  const c = cur();
  const [w, hh] = m.SIZES[fields.type];
  if (x == null) {
    const r = viewport.getBoundingClientRect();
    x = (r.width / 2 - c.view.x) / c.view.zoom - w / 2;
    y = (r.height / 2 - c.view.y) / c.view.zoom - hh / 2;
    // Step down-right past items already there, so items added one after another never hide each other.
    const taken = (px, py) => [...st.items.values()].some((o) => Math.abs(o.x - px) < 12 && Math.abs(o.y - py) < 12);
    for (let i = 0; i < 40 && taken(x, y); i++) { x += 24; y += 24; }
  }
  const it = Object.assign({ id: m.newId(), x, y, w, h: hh, z: fields.type === "frame" ? 0 : topZ() + 1 }, fields);
  st.items.set(it.id, it);
  st.selected = it.id;
  if (it.type === "task") await loadTasks();
  render();
  await save(it);
  return it;
}

async function addImage(file, x, y) {
  if (!/^image\/(png|jpeg|gif|webp)$/.test(file.type)) { api.ui.toast(t("image_type")); return; }
  if (file.size > 10 << 20) { api.ui.toast(t("image_big")); return; }
  if (!st.canvas) await newCanvas(t("canvas_default"));
  const id = m.newId();
  const key = m.blobKey(st.canvas, id, file.type);
  try { await api.storage.putBlob(key, file); } catch (e) { fail(e); return; }
  const size = await imageSize(file);
  const [w, hh] = size ? fit(size, 320) : m.SIZES.image;
  let it;
  try {
    it = await add({ type: "image", blob: key }, x, y);
    Object.assign(it, { w, h: hh });
    render();
    await api.storage.put(m.itemKey(st.canvas, it.id), m.clean(it));
  } catch (e) {
    // No item points at the image: do not leave it on the board.
    fail(e);
    if (it) { st.items.delete(it.id); render(); }
    api.storage.delete(key).catch(() => {});
  }
}

// refreshItems reloads only the given items (a storage change named them), not the whole canvas.
async function refreshItems(ids) {
  if (st.busy || st.editing) { st.pendingReload = true; return; }
  try {
    await Promise.all(ids.map(async (id) => {
      const v = await api.storage.get(m.itemKey(st.canvas, id));
      if (v && m.TYPES.includes(v.type)) st.items.set(id, Object.assign({ id }, v));
      else st.items.delete(id);
    }));
    await loadTasks();
    render();
  } catch (e) { fail(e); }
}
function imageSize(file) {
  return new Promise((ok) => {
    const img = new Image();
    img.onload = () => { ok([img.naturalWidth, img.naturalHeight]); URL.revokeObjectURL(img.src); };
    img.onerror = () => ok(null);
    img.src = URL.createObjectURL(file);
  });
}
const fit = ([w, hh], max) => (w > max ? [max, Math.round((hh * max) / w)] : [w, hh]);

async function remove(id) {
  const it = st.items.get(id);
  if (!it) return;
  st.items.delete(id);
  if (st.selected === id) st.selected = "";
  render();
  try {
    await api.storage.delete(m.itemKey(st.canvas, id));
    if (it.blob) await api.storage.delete(it.blob);
  } catch (e) { fail(e); }
}

async function newCanvas(name) {
  const id = m.newId();
  const c = { id, name: name || t("canvas_default"), view: { x: 40, y: 40, zoom: 1 } };
  await api.storage.put(m.canvasKey(id), { name: c.name, view: c.view });
  st.canvases.push(c);
  st.canvas = id;
  st.items = new Map();
}

async function deleteCanvas() {
  const c = cur();
  if (!c) return;
  const keys = await api.storage.list(c.id + "/");
  for (const k of keys) await api.storage.delete(k);
  st.canvas = "";
  await reload();
}

// ---------- toolbar ----------

// ask shows a small inline input under the toolbar (a sandboxed frame has no prompt() and cannot submit forms).
function ask(label, placeholder, value, done) {
  const input = h("input", { placeholder, value: value || "", spellcheck: "false" });
  const ok = () => { const v = input.value.trim(); form.replaceChildren(); if (v) Promise.resolve(done(v)).catch(fail); };
  input.addEventListener("keydown", (e) => { if (e.key === "Enter") ok(); if (e.key === "Escape") form.replaceChildren(); });
  form.replaceChildren(h("span", { class: "muted" }, label), input,
    h("button", { class: "primary", onclick: ok }, t("ok")), h("button", { onclick: () => form.replaceChildren() }, t("cancel")));
  input.focus();
}

function renderBar() {
  const c = cur();
  const sel = st.items.get(st.selected);
  let armed = false;
  const del = h("button", { class: "danger", disabled: !c, onclick: (e) => {
    if (!armed) { armed = true; e.target.textContent = t("delete_confirm"); return; }
    deleteCanvas().catch(fail);
  } }, t("delete_canvas"));
  // Delete sits in a ⋯ menu, away from Rename, and still asks for a second click.
  const more = h("details", { class: "more" }, h("summary", { title: t("more"), "aria-label": t("more") }, "⋯"), h("div", { class: "pop" }, del));
  bar.replaceChildren(...[
    h("select", { onchange: async (e) => { st.canvas = e.target.value; st.selected = ""; await loadItems(); render(); } },
      st.canvases.map((x) => h("option", { value: x.id, selected: x.id === st.canvas }, x.name))),
    h("button", { onclick: () => ask(t("new_canvas"), t("name_placeholder"), "", async (v) => { await newCanvas(v); render(); }) }, "+ " + t("canvas")),
    h("button", { disabled: !c, onclick: () => ask(t("rename"), t("name_placeholder"), c.name, (v) => { c.name = v; saveView(); render(); }) }, t("rename")),
    c ? more : null,
    h("span", { class: "sep" }),
    h("button", { onclick: () => add({ type: "note", text: "" }).then((it) => edit(it.id)) }, t("note")),
    h("button", { onclick: () => ask(t("link"), "https://…", "", (v) => add(m.fromText(v).type === "link" ? { type: "link", url: v } : { type: "link", url: "https://" + v })) }, t("link")),
    h("button", { onclick: () => ask(t("task"), t("task_placeholder"), "", (v) => add({ type: "task", task: v.toUpperCase() })) }, t("task")),
    h("button", { onclick: () => add({ type: "frame", title: t("frame_default") }).then((it) => edit(it.id)) }, t("frame")),
    h("label", { class: "file" }, t("image"), h("input", { type: "file", accept: "image/png,image/jpeg,image/gif,image/webp",
      onchange: (e) => { for (const f of e.target.files) addImage(f); e.target.value = ""; } })),
    h("span", { class: "sep" }),
    sel && sel.type === "note" && sel.text ? h("button", { onclick: () => api.ui.newTask(m.taskFields(sel.text, t("task_footer", { canvas: c.name }))) }, t("make_task")) : null,
    sel ? h("button", { onclick: () => remove(sel.id) }, t("delete")) : null,
    h("span", { class: "spacer" }),
    h("button", { title: t("zoom_reset"), disabled: !c, onclick: () => { c.view = { x: 40, y: 40, zoom: 1 }; saveView(); render(); } },
      c ? Math.round(c.view.zoom * 100) + "%" : "100%"),
  ].filter(Boolean));
}

// ---------- canvas ----------

function render() {
  renderBar();
  const c = cur();
  if (!c) {
    world.replaceChildren();
    viewport.dataset.empty = t("empty");
    return;
  }
  delete viewport.dataset.empty;
  world.style.transform = `translate(${c.view.x}px, ${c.view.y}px) scale(${c.view.zoom})`;
  world.replaceChildren(...[...st.items.values()].map(itemEl)); // z-index stacks them: frames below the rest
}

// paintSelection updates the selection and stacking in place: replacing the pressed element (render) would make
// the browser drop the click, and with it the double-click that edits or opens an item.
function paintSelection() {
  renderBar();
  for (const n of world.children) {
    const it = st.items.get(n.dataset.id);
    n.classList.toggle("on", it.id === st.selected);
    n.style.zIndex = zOf(it);
  }
}
const zOf = (it) => (it.type === "frame" ? it.z | 0 : 1000 + (it.z | 0));

function itemEl(it) {
  const n = h("div", { class: `item ${it.type}${it.id === st.selected ? " on" : ""}`, "data-id": it.id });
  Object.assign(n.style, { left: it.x + "px", top: it.y + "px", width: it.w + "px", height: it.h + "px", zIndex: zOf(it) });
  switch (it.type) {
    case "note":
      if (st.editing === it.id) {
        const ta = h("textarea", { placeholder: t("note_placeholder") });
        ta.value = it.text || "";
        ta.addEventListener("blur", () => { it.text = ta.value; st.editing = ""; save(it); render(); if (st.pendingReload) reload(); });
        ta.addEventListener("keydown", (e) => { e.stopPropagation(); if (e.key === "Escape") ta.blur(); });
        n.append(ta);
        setTimeout(() => ta.focus(), 0);
      } else n.append(h("div", { class: "text" }, it.text || h("span", { class: "muted" }, t("note_placeholder"))));
      break;
    case "frame":
      if (st.editing === it.id) {
        const inp = h("input", { value: it.title || "" });
        inp.addEventListener("blur", () => { it.title = inp.value; st.editing = ""; save(it); render(); if (st.pendingReload) reload(); });
        inp.addEventListener("keydown", (e) => { e.stopPropagation(); if (e.key === "Enter" || e.key === "Escape") inp.blur(); });
        n.append(inp);
        setTimeout(() => { inp.focus(); inp.select(); }, 0); // typing replaces the default name
      } else n.append(h("div", { class: "frame-title" }, it.title || t("frame_default")));
      break;
    case "image": {
      const img = h("img", { alt: "", draggable: "false" });
      api.storage.blobURL(it.blob).then((u) => { img.src = u; }).catch(() => n.classList.add("broken"));
      n.append(img);
      break;
    }
    case "link":
      n.append(h("div", { class: "link-title" }, it.title || hostOf(it.url)), h("div", { class: "muted small" }, it.url));
      n.title = t("link_hint");
      break;
    case "task": {
      const x = st.tasks.get(it.task);
      n.append(h("div", { class: "task-head" }, h("b", {}, it.task), x ? h("span", { class: "status s-" + x.status }, statusLabel(x.status)) : null),
        h("div", { class: "small" }, x ? x.title : x === null ? t("missing_task") : "…"));
      n.title = t("task_hint");
      break;
    }
  }
  n.append(h("div", { class: "handle" }));
  return n;
}
const hostOf = (u) => { try { return new URL(u).host; } catch (_) { return u; } };
let statusLabels = {};
const statusLabel = (s) => statusLabels[s] || s;

// ---------- pointer: pan, move, resize ----------

function wire() {
  let drag = null;
  viewport.addEventListener("pointerdown", (e) => {
    if (e.button !== 0 || e.target.closest("textarea, input")) return;
    const c = cur();
    if (!c) return;
    const n = e.target.closest(".item");
    form.replaceChildren();
    if (!n) {
      st.selected = "";
      drag = { kind: "pan", sx: e.clientX, sy: e.clientY, vx: c.view.x, vy: c.view.y };
    } else {
      const it = st.items.get(n.dataset.id);
      if (st.selected !== it.id) {
        st.selected = it.id;
        if (it.type !== "frame" && (it.z | 0) < topZ()) { it.z = topZ() + 1; drag = { raise: true }; }
      }
      const group = it.type === "frame" ? m.inside(it, [...st.items.values()]) : [];
      drag = Object.assign(drag || {}, { kind: e.target.classList.contains("handle") ? "resize" : "move", it, sx: e.clientX, sy: e.clientY,
        start: [it, ...group].map((x) => ({ x, ox: x.x, oy: x.y, ow: x.w, oh: x.h })), moved: false });
    }
    st.busy = true;
    viewport.setPointerCapture(e.pointerId);
    paintSelection();
  });
  viewport.addEventListener("pointermove", (e) => {
    if (!drag) return;
    const c = cur();
    const dx = e.clientX - drag.sx, dy = e.clientY - drag.sy;
    if (drag.kind === "pan") { c.view.x = drag.vx + dx; c.view.y = drag.vy + dy; world.style.transform = `translate(${c.view.x}px, ${c.view.y}px) scale(${c.view.zoom})`; return; }
    if (Math.abs(dx) + Math.abs(dy) > 2) drag.moved = true;
    const k = 1 / c.view.zoom;
    if (drag.kind === "resize") {
      const s = drag.start[0];
      drag.it.w = Math.max(60, s.ow + dx * k);
      drag.it.h = Math.max(40, s.oh + dy * k);
    } else for (const s of drag.start) { s.x.x = s.ox + dx * k; s.x.y = s.oy + dy * k; }
    for (const s of drag.start) {
      const n = world.querySelector(`[data-id="${s.x.id}"]`);
      if (n) Object.assign(n.style, { left: s.x.x + "px", top: s.x.y + "px", width: s.x.w + "px", height: s.x.h + "px" });
    }
  });
  const end = () => {
    if (!drag) return;
    if (drag.kind === "pan") saveView();
    else if (drag.moved) { for (const s of drag.start) save(s.x); }
    else if (drag.raise) save(drag.it);
    drag = null;
    st.busy = false;
    if (st.pendingReload) reload();
  };
  viewport.addEventListener("pointerup", end);
  viewport.addEventListener("pointercancel", end);
  viewport.addEventListener("dblclick", (e) => {
    // pointer capture sends the dblclick to the viewport, so look under the pointer
    const n = document.elementFromPoint(e.clientX, e.clientY)?.closest(".item");
    if (!n) return;
    const it = st.items.get(n.dataset.id);
    if (it.type === "note" || it.type === "frame") edit(it.id);
    if (it.type === "task") api.ui.openTask(it.task).catch(fail);
    if (it.type === "link") api.ui.openLink(it.url).catch(fail);
  });
  viewport.addEventListener("wheel", (e) => {
    const c = cur();
    if (!c) return;
    e.preventDefault();
    const r = viewport.getBoundingClientRect();
    c.view = m.zoomAt(c.view, Math.exp(-e.deltaY * 0.0015), e.clientX - r.left, e.clientY - r.top);
    render();
    saveView();
  }, { passive: false });
  document.addEventListener("keydown", (e) => {
    if (e.target.closest?.("textarea, input, select")) return;
    if ((e.key === "Delete" || e.key === "Backspace") && st.selected) { e.preventDefault(); remove(st.selected); }
    if (e.key === "Enter" && st.selected) edit(st.selected);
  });
  document.addEventListener("paste", (e) => {
    if (e.target.closest?.("textarea, input")) return;
    const files = [...(e.clipboardData.files || [])].filter((f) => f.type.startsWith("image/"));
    if (files.length) { e.preventDefault(); files.forEach((f) => addImage(f)); return; }
    const text = e.clipboardData.getData("text/plain");
    if (text && text.trim()) { e.preventDefault(); add(m.fromText(text)).catch(fail); }
  });
  viewport.addEventListener("dragover", (e) => e.preventDefault());
  viewport.addEventListener("drop", (e) => {
    e.preventDefault();
    const c = cur() || { view: { x: 0, y: 0, zoom: 1 } };
    const r = viewport.getBoundingClientRect();
    const x = (e.clientX - r.left - c.view.x) / c.view.zoom, y = (e.clientY - r.top - c.view.y) / c.view.zoom;
    const files = [...e.dataTransfer.files].filter((f) => f.type.startsWith("image/"));
    files.forEach((f, i) => addImage(f, x + i * 24, y + i * 24));
    const text = !files.length && e.dataTransfer.getData("text/plain");
    if (text) add(m.fromText(text), x, y).catch(fail);
  });
}

function edit(id) {
  const it = st.items.get(id);
  if (!it || (it.type !== "note" && it.type !== "frame")) return;
  st.selected = id;
  st.editing = id;
  render();
}

export async function activate(a) {
  api = a; t = a.t;
  document.head.append(h("link", { rel: "stylesheet", href: new URL("style.css", import.meta.url).href }));
  bar = h("div", { class: "bar" });
  form = h("div", { class: "ask" });
  world = h("div", { class: "world" });
  viewport = h("div", { class: "viewport", tabindex: "0" }, world);
  document.body.append(h("div", { class: "vision" }, bar, form, viewport, h("div", { class: "hint muted" }, t("hint"))));
  try { statusLabels = ((await api.suite()).labels || {}).statuses || {}; } catch (_) { /* keys then */ }
  wire();
  await reload();
  let itemsTimer, tasksTimer, full = false;
  const changed = new Set();
  api.on("change", (ev) => {
    if (ev.kind === "storage" || ev.kind === "all") {
      // An item key reloads that item; a blob needs nothing (its item changes with it); anything else – a canvas,
      // or a change without a key – reloads everything.
      const id = ev.kind === "storage" && ev.key ? m.itemId(st.canvas, ev.key) : "";
      if (id) changed.add(id);
      else if (!(ev.kind === "storage" && ev.key && m.isBlobKey(ev.key))) full = true;
      clearTimeout(itemsTimer);
      itemsTimer = setTimeout(() => {
        const ids = [...changed];
        changed.clear();
        if (full) { full = false; reload(); } else if (ids.length) refreshItems(ids);
      }, 300);
    }
    if (ev.kind === "task" || ev.kind === "all") {
      clearTimeout(tasksTimer);
      tasksTimer = setTimeout(() => loadTasks().then(() => { if (!st.busy && !st.editing) render(); }), 300);
    }
  });
}
