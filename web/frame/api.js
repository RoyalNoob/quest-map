// The plugin API (spec §8.5). This script runs inside the plugin's sandboxed iframe: it waits for the host's
// init message, loads the plugin's entry module and calls activate(api). Every call is a message to the host,
// which checks the plugin's permissions and answers; the plugin itself has no network.

const pending = new Map();
const listeners = { change: [] };
let seq = 0;
let catalog = {};
let lang = "en";
let started = false;
const blobURLs = new Map(); // key -> Promise of a blob: URL

function call(method, ...args) {
  return new Promise((resolve, reject) => {
    const id = ++seq;
    pending.set(id, { resolve, reject });
    parent.postMessage({ qm: "call", id, method, args }, "*");
  });
}

// forgetBlob drops a cached blob: URL and frees its memory.
function forgetBlob(key) {
  const p = blobURLs.get(key);
  if (!p) return;
  blobURLs.delete(key);
  p.then((u) => URL.revokeObjectURL(u), () => {});
}

function applyTheme(vars) {
  for (const [k, v] of Object.entries(vars || {})) document.documentElement.style.setProperty(k, v);
}

// t returns the plugin's text for key in the chosen language ({name} placeholders from vars; a numeric vars.n
// picks key.one / key.other), else the key.
function t(key, vars) {
  let s;
  if (vars && typeof vars.n === "number") s = catalog[key + "." + new Intl.PluralRules(lang).select(vars.n)] ?? catalog[key + ".other"];
  s = s ?? catalog[key] ?? key;
  return vars ? s.replace(/\{(\w+)\}/g, (m, n) => (n in vars ? String(vars[n]) : m)) : s;
}

function download(name, text, type) {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([text], { type: type || "text/plain" }));
  a.download = name;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

window.addEventListener("message", async (e) => {
  if (e.source !== parent) return;
  const m = e.data || {};
  if (m.qm === "result") {
    const p = pending.get(m.id);
    if (!p) return;
    pending.delete(m.id);
    if (m.ok) p.resolve(m.value); else p.reject(new Error(m.error));
  } else if (m.qm === "event") {
    if (m.data && m.data.kind === "storage" && m.data.key) forgetBlob(m.data.key); // changed elsewhere too
    for (const fn of listeners[m.type] || []) { try { fn(m.data); } catch (err) { console.error(err); } }
  } else if (m.qm === "theme") {
    applyTheme(m.theme);
  } else if (m.qm === "init") {
    if (started) return; // a second ready/init must not run the plugin twice
    started = true;
    lang = m.lang;
    catalog = m.catalog || {};
    document.documentElement.lang = lang;
    applyTheme(m.theme);
    const api = {
      view: m.view,     // {id, title, scope}
      lang,
      timezone: m.timezone || undefined, // the board's IANA zone (undefined: the browser's), for counting days
      t,
      download,
      project: () => call("project"),
      tasks: { find: (filter, opts) => call("tasks.find", filter || "", opts || {}), get: (id) => call("tasks.get", id) },
      activity: (opts) => call("activity", opts || {}),
      releases: () => call("releases"),
      milestones: () => call("milestones"),
      suite: () => call("suite"),
      on: (type, fn) => { (listeners[type] = listeners[type] || []).push(fn); },
      storage: {
        list: (prefix) => call("storage.list", prefix || ""),
        get: (key) => call("storage.get", key),
        put: (key, value) => call("storage.put", key, value),
        delete: (key) => { forgetBlob(key); return call("storage.delete", key); },
        putBlob: (key, blob) => { forgetBlob(key); return call("storage.putBlob", key, blob); },
        // blobURL returns a blob: URL of this frame for a stored image (cached per key until it is put again or
        // deleted). A failed load is not cached: the image may not have synced yet.
        blobURL: (key) => {
          if (!blobURLs.has(key)) {
            const p = call("storage.blob", key).then((b) => URL.createObjectURL(b));
            p.catch(() => { if (blobURLs.get(key) === p) blobURLs.delete(key); });
            blobURLs.set(key, p);
          }
          return blobURLs.get(key);
        },
      },
      ui: {
        openTask: (id) => call("ui.openTask", id),
        newTask: (fields) => call("ui.newTask", fields || {}),
        toast: (text) => call("ui.toast", String(text)),
        openLink: (url) => call("ui.openLink", String(url)),
      },
    };
    try {
      const mod = await import(new URL("../" + m.entry, location.href).href);
      await mod.activate(api);
    } catch (err) {
      parent.postMessage({ qm: "error", error: String(err && err.message || err) }, "*");
    }
  }
});

parent.postMessage({ qm: "ready" }, "*");
