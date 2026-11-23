// Report numbers (spec §8.5 reports): pure functions of tasks (api.tasks.find rows) and activity records
// (api.activity), so a test can check them against a hand calculation. Days and weeks are those of the board's
// time zone (setTimeZone; the browser's until it is set), like Insights.

export const STATUSES = ["backlog", "todo", "doing", "review", "done"];
const DAY = 86400000;

let zone; // IANA name; undefined = the browser's own zone
let parts;
// setTimeZone sets the zone days are counted in (the board's `timezone`, api.timezone).
export function setTimeZone(tz) {
  zone = tz || undefined;
  const f = new Intl.DateTimeFormat("en-US", { timeZone: zone, hourCycle: "h23", year: "numeric", month: "numeric", day: "numeric",
    hour: "numeric", minute: "numeric", second: "numeric" });
  parts = (d) => {
    const o = {};
    for (const p of f.formatToParts(d)) if (p.type !== "literal") o[p.type] = Number(p.value);
    return o;
  };
}
setTimeZone(undefined);

// midnight is the instant 00:00 starts day y-m-d (m 1-based; out-of-range days roll over) in the zone.
function midnight(y, m, d) {
  const wall = Date.UTC(y, m - 1, d);
  const offset = (t) => { const p = parts(new Date(t)); return Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second) - Math.floor(t / 1000) * 1000; };
  const guess = wall - offset(wall);
  return new Date(wall - offset(guess)); // a second look covers a zone whose offset changes near midnight
}

const pad = (n) => String(n).padStart(2, "0");
export const dayKey = (d) => { const p = parts(d); return `${p.year}-${pad(p.month)}-${pad(p.day)}`; };
export function dayStart(d) { const p = parts(d); return midnight(p.year, p.month, p.day); }
// addDays is the start of the day n days after d's day.
export function addDays(d, n) { const p = parts(d); return midnight(p.year, p.month, p.day + n); }
const dayEnd = (d) => new Date(addDays(d, 1) - 1);
// weekStart is the Monday of d's week.
export function weekStart(d) {
  const p = parts(d);
  const wd = new Date(Date.UTC(p.year, p.month - 1, p.day)).getUTCDay();
  return midnight(p.year, p.month, p.day - ((wd + 6) % 7));
}

// days lists the day starts from since to until, both included.
export function days(since, until) {
  const out = [];
  for (let d = dayStart(since); d <= until; d = addDays(d, 1)) out.push(d);
  return out;
}

// timelines groups status records by task, in time order: id -> [{at, from, to}].
export function timelines(activity) {
  const m = new Map();
  for (const r of activity) {
    if (r.kind !== "status" || !r.task) continue;
    if (!m.has(r.task)) m.set(r.task, []);
    m.get(r.task).push({ at: new Date(r.at), from: r.from || "", to: r.to || "" });
  }
  for (const l of m.values()) l.sort((a, b) => a.at - b.at);
  return m;
}

// statusAt is a task's status at time t, or null when it did not exist yet.
export function statusAt(task, tl, t) {
  const recs = tl.get(task.ID) || [];
  let s = null;
  for (const r of recs) {
    if (r.at > t) break;
    s = r.to;
  }
  if (s !== null) return s || null;
  if (new Date(task.Created) > t) return null;
  return recs.length ? recs[0].from || null : task.Status; // no record before t: what it was before the first one
}

// cumulativeFlow: tasks per status at the end of each day.
export function cumulativeFlow(tasks, activity, since, until) {
  const tl = timelines(activity);
  return days(since, until).map((d) => {
    const row = { day: dayKey(d) };
    for (const s of STATUSES) row[s] = 0;
    for (const t of tasks) {
      const s = statusAt(t, tl, dayEnd(d));
      if (s in row) row[s]++;
    }
    return row;
  });
}

// burnUp: for the tasks of one milestone, how many existed (scope) and how many were done at the end of each day.
export function burnUp(tasks, activity, milestone, since, until) {
  const tl = timelines(activity);
  const mine = tasks.filter((t) => t.Milestone === milestone);
  return days(since, until).map((d) => {
    let scope = 0, done = 0;
    for (const t of mine) {
      const s = statusAt(t, tl, dayEnd(d));
      if (s) scope++;
      if (s === "done") done++;
    }
    return { day: dayKey(d), scope, done };
  });
}

// throughput: tasks that moved to done per week (Monday), counted once per week, grouped by discipline or doer.
export function throughput(tasks, activity, by, since, until) {
  const byID = new Map(tasks.map((t) => [t.ID, t]));
  const field = by === "doer" ? "Doer" : "Discipline";
  const seen = new Set();
  const weeks = new Map();
  for (let w = weekStart(since); w <= until; w = addDays(w, 7)) weeks.set(dayKey(w), {});
  const groups = new Set();
  for (const r of activity) {
    const t = byID.get(r.task);
    const at = new Date(r.at);
    if (r.kind !== "status" || r.to !== "done" || !t || at < since || at > until) continue;
    const week = dayKey(weekStart(at));
    if (seen.has(week + " " + t.ID)) continue;
    seen.add(week + " " + t.ID);
    const g = t[field] || "–";
    groups.add(g);
    const row = weeks.get(week) || {};
    row[g] = (row[g] || 0) + 1;
    weeks.set(week, row);
  }
  const gs = [...groups].sort();
  const rows = [...weeks.entries()].sort().map(([week, c]) => {
    const row = { week, total: 0 };
    for (const g of gs) { row[g] = c[g] || 0; row.total += row[g]; }
    return row;
  });
  return { groups: gs, rows };
}

// cycleTimes: for each task done in the period (its last move to done), the days since it first went to doing.
export function cycleTimes(tasks, activity, since, until) {
  const tl = timelines(activity);
  const out = [];
  for (const t of tasks) {
    const recs = tl.get(t.ID) || [];
    const done = recs.filter((r) => r.to === "done" && r.at >= since && r.at <= until).pop();
    if (!done) continue;
    const start = recs.find((r) => r.to === "doing" && r.at <= done.at);
    if (!start) continue;
    out.push({ id: t.ID, title: t.Title, done: dayKey(done.at), days: Math.round((done.at - start.at) / DAY * 10) / 10 });
  }
  return out.sort((a, b) => (a.done < b.done ? -1 : a.done > b.done ? 1 : a.id < b.id ? -1 : 1));
}

export function median(xs) {
  if (!xs.length) return null;
  const s = [...xs].sort((a, b) => a - b);
  const m = s.length >> 1;
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
}

// bugsPerRelease: tasks found in and fixed in each release, in release order, then ids only tasks name.
export function bugsPerRelease(tasks, releases) {
  const ids = [...releases].sort((a, b) => new Date(a.date) - new Date(b.date)).map((r) => r.id);
  for (const t of tasks) for (const id of [t.FoundIn, t.FixedIn]) if (id && !ids.includes(id)) ids.push(id);
  return ids.map((id) => ({ release: id, found: tasks.filter((t) => t.FoundIn === id).length, fixed: tasks.filter((t) => t.FixedIn === id).length }));
}

// pipelineSteps: open items per pipeline step, in the pipeline's step order.
export function pipelineSteps(tasks, pipelines) {
  const out = [];
  for (const p of pipelines) {
    const mine = tasks.filter((t) => t.Status !== "done" && (t.PipelineName || "").toLowerCase() === p.name.toLowerCase());
    for (const step of p.steps) out.push({ pipeline: p.name, step, count: mine.filter((t) => t.PipelineAt === step).length });
  }
  return out;
}

// csv turns rows into CSV text with the given columns. Text that a spreadsheet would run as a formula (=, +, -,
// @, tab, CR first – a task title is anyone's text) gets a leading ' so it stays text; numbers stay numbers.
export function csv(rows, cols) {
  const q = (v) => {
    let s = v == null ? "" : String(v);
    if (typeof v !== "number" && /^[=+\-@\t\r]/.test(s)) s = "'" + s;
    return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
  };
  return [cols.map(q).join(","), ...rows.map((r) => cols.map((c) => q(r[c])).join(","))].join("\n") + "\n";
}
