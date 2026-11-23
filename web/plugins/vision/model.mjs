// vision data (spec §8.5): one storage key per item, so two board copies that changed different items merge
// cleanly in git. Keys:
//   <canvas>/canvas.json        {name, view: {x, y, zoom}}
//   <canvas>/items/<id>.json    {type, x, y, w, h, z, ...content}
//   <canvas>/blobs/<id>.<ext>   pasted or dropped images
// Pure functions only, so a test can check them.

export const TYPES = ["note", "image", "link", "task", "frame"];
export const SIZES = { note: [200, 120], image: [320, 220], link: [240, 64], task: [240, 72], frame: [480, 320] };

// newId is a lowercase, time-ordered id: 9 base-36 digits of milliseconds, then 6 random ones.
export function newId(now = Date.now(), rand = Math.random) {
  let r = "";
  for (let i = 0; i < 6; i++) r += Math.floor(rand() * 36).toString(36);
  return now.toString(36).padStart(9, "0") + r;
}

export const canvasKey = (c) => `${c}/canvas.json`;
export const itemKey = (c, id) => `${c}/items/${id}.json`;
export const itemsPrefix = (c) => `${c}/items/`;
export const blobKey = (c, id, type) => `${c}/blobs/${id}.${{ "image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp" }[type] || "img"}`;

// canvasIds returns the canvases among storage keys.
export function canvasIds(keys) {
  return keys.map((k) => k.match(/^([^/]+)\/canvas\.json$/)).filter(Boolean).map((m) => m[1]);
}

// itemId returns the item id of an item key of canvas c, or "". Plain string checks: a canvas id may hold a
// "." that a regular expression would read as any character.
export function itemId(c, key) {
  const p = itemsPrefix(c);
  if (!c || !key.startsWith(p) || !key.endsWith(".json")) return "";
  const id = key.slice(p.length, -".json".length);
  return id && !id.includes("/") ? id : "";
}

// isBlobKey reports whether key is a canvas's image.
export const isBlobKey = (key) => /^[^/]+\/blobs\/[^/]+$/.test(key);

// clean keeps the fields an item of its type stores; numbers are rounded so a file changes only when it moved.
export function clean(it) {
  const out = { type: it.type, x: Math.round(it.x), y: Math.round(it.y), w: Math.round(it.w), h: Math.round(it.h), z: it.z | 0 };
  for (const f of { note: ["text"], image: ["blob"], link: ["url", "title"], task: ["task"], frame: ["title"] }[it.type] || []) {
    if (it[f] != null && it[f] !== "") out[f] = it[f];
  }
  return out;
}

// inside lists the non-frame items whose centre lies in frame f: they move with it.
export function inside(f, items) {
  return items.filter((it) => it.type !== "frame" && it !== f &&
    it.x + it.w / 2 >= f.x && it.x + it.w / 2 <= f.x + f.w && it.y + it.h / 2 >= f.y && it.y + it.h / 2 <= f.y + f.h);
}

// fromText turns pasted text into an item: a task id, a link, or a note.
export function fromText(text) {
  const s = text.trim();
  if (/^[A-Z][A-Z0-9]*-\d+$/.test(s)) return { type: "task", task: s };
  if (/^https?:\/\/\S+$/i.test(s)) return { type: "link", url: s };
  return { type: "note", text: s };
}

// taskFields turns a note into New task dialog fields: the first line is the title, the rest the description,
// then footer (where it came from).
export function taskFields(text, footer) {
  const lines = (text || "").trim().split("\n");
  const title = lines[0].trim().slice(0, 200);
  const rest = lines.slice(1).join("\n").trim();
  return { title, body: [rest, footer || ""].filter(Boolean).join("\n\n") };
}

// zoomAt zooms the view by factor around the screen point (px, py), keeping that point still.
export function zoomAt(view, factor, px, py) {
  const zoom = Math.min(4, Math.max(0.1, view.zoom * factor));
  const k = zoom / view.zoom;
  return { x: px - (px - view.x) * k, y: py - (py - view.y) * k, zoom };
}
