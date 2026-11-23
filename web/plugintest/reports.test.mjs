// Done-when (M10): every report matches a hand calculation from a fixture board:
// node --test web/plugintest/*.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import * as c from "../plugins/reports/calc.mjs";

// Reports count days in the board's time zone (api.timezone), whatever the browser's is. The hand
// calculations below are for a board in UTC; the last tests use other zones.
c.setTimeZone("UTC");

// 2026-11-02 is a Monday.
const tasks = [
  { ID: "A-1", Title: "Bridge mesh", Created: "2026-11-02T09:00:00Z", Status: "done", Discipline: "art", Doer: "agent", Milestone: "M1",
    FoundIn: "b1", FixedIn: "b2", PipelineName: "prop", PipelineAt: "texture" },
  { ID: "A-2", Title: "Save game", Created: "2026-11-02T12:00:00Z", Status: "review", Discipline: "code", Doer: "person", Milestone: "M1", FoundIn: "b2" },
  { ID: "A-3", Title: "Tree", Created: "2026-11-04T00:00:00Z", Status: "done", Discipline: "art", Doer: "agent", Milestone: "M2", FixedIn: "b2" },
  // Made before the period, by hand: no activity records.
  { ID: "A-4", Title: "Rock", Created: "2026-11-01T08:00:00Z", Status: "todo", Discipline: "art", Doer: "agent", Milestone: "M1",
    FoundIn: "b1", FixedIn: "b3", PipelineName: "Prop", PipelineAt: "model" },
];
const st = (task, at, from, to) => ({ kind: "status", task, at, from, to });
const activity = [
  st("A-1", "2026-11-02T09:00:00Z", "", "todo"), st("A-1", "2026-11-03T10:00:00Z", "todo", "doing"), st("A-1", "2026-11-05T10:00:00Z", "doing", "done"),
  st("A-2", "2026-11-02T12:00:00Z", "", "backlog"), st("A-2", "2026-11-04T08:00:00Z", "backlog", "doing"), st("A-2", "2026-11-06T08:00:00Z", "doing", "review"),
  st("A-3", "2026-11-04T00:00:00Z", "", "todo"), st("A-3", "2026-11-04T06:00:00Z", "todo", "doing"), st("A-3", "2026-11-04T18:00:00Z", "doing", "done"),
  st("A-3", "2026-11-05T09:00:00Z", "done", "doing"), st("A-3", "2026-11-09T09:00:00Z", "doing", "done"),
  { kind: "note", task: "A-1", at: "2026-11-03T11:00:00Z" },
];
const since = new Date("2026-11-02T00:00:00Z"), until = new Date("2026-11-09T23:59:59Z");

test("cumulative flow", () => {
  const rows = c.cumulativeFlow(tasks, activity, since, until).map((r) => [r.day, r.backlog, r.todo, r.doing, r.review, r.done]);
  assert.deepEqual(rows, [
    ["2026-11-02", 1, 2, 0, 0, 0],
    ["2026-11-03", 1, 1, 1, 0, 0],
    ["2026-11-04", 0, 1, 2, 0, 1],
    ["2026-11-05", 0, 1, 2, 0, 1],
    ["2026-11-06", 0, 1, 1, 1, 1],
    ["2026-11-07", 0, 1, 1, 1, 1],
    ["2026-11-08", 0, 1, 1, 1, 1],
    ["2026-11-09", 0, 1, 0, 1, 2],
  ]);
});

test("burn-up", () => {
  const m1 = c.burnUp(tasks, activity, "M1", since, until).map((r) => [r.scope, r.done]);
  assert.deepEqual(m1, [[3, 0], [3, 0], [3, 0], [3, 1], [3, 1], [3, 1], [3, 1], [3, 1]]);
  const m2 = c.burnUp(tasks, activity, "M2", since, until).map((r) => [r.scope, r.done]);
  assert.deepEqual(m2, [[0, 0], [0, 0], [1, 1], [1, 0], [1, 0], [1, 0], [1, 0], [1, 1]]);
});

test("throughput", () => {
  // A-1 and A-3 in the week of 11-02 (A-3 counted once), A-3 again in the week of 11-09.
  assert.deepEqual(c.throughput(tasks, activity, "discipline", since, until),
    { groups: ["art"], rows: [{ week: "2026-11-02", art: 2, total: 2 }, { week: "2026-11-09", art: 1, total: 1 }] });
  assert.deepEqual(c.throughput(tasks, activity, "doer", since, until).groups, ["agent"]);
});

test("cycle time", () => {
  // A-1: doing 11-03 10:00 → done 11-05 10:00 = 2 days. A-3: first doing 11-04 06:00 → last done 11-09 09:00 = 5.125.
  const rows = c.cycleTimes(tasks, activity, since, until);
  assert.deepEqual(rows.map((r) => [r.id, r.done, r.days]), [["A-1", "2026-11-05", 2], ["A-3", "2026-11-09", 5.1]]);
  assert.equal(c.median(rows.map((r) => r.days)), 3.55);
  // Up to 11-08, A-3's done is 11-04 18:00: half a day.
  const early = c.cycleTimes(tasks, activity, since, new Date("2026-11-08T23:59:59Z"));
  assert.deepEqual(early.map((r) => [r.id, r.days]), [["A-3", 0.5], ["A-1", 2]]);
});

test("bugs per release", () => {
  const rels = [{ id: "b2", date: "2026-11-08T00:00:00Z" }, { id: "b1", date: "2026-11-01T00:00:00Z" }];
  assert.deepEqual(c.bugsPerRelease(tasks, rels), [
    { release: "b1", found: 2, fixed: 0 }, { release: "b2", found: 1, fixed: 2 }, { release: "b3", found: 0, fixed: 1 }]);
});

test("pipeline steps", () => {
  // A-1 is done, so only A-4 counts (pipeline names match without case).
  assert.deepEqual(c.pipelineSteps(tasks, [{ name: "prop", steps: ["model", "texture", "done"] }]).map((r) => r.count), [1, 0, 0]);
});

test("csv", () => {
  assert.equal(c.csv([{ a: 'x,"y"', b: 1 }, { a: "z" }], ["a", "b"]), 'a,b\n"x,""y""",1\nz,\n');
  // Text a spreadsheet would run as a formula stays text; negative numbers stay numbers.
  assert.equal(c.csv([{ a: '=HYPERLINK("http://x")', b: -2 }, { a: "@SUM(A1)", b: "+1" }], ["a", "b"]),
    'a,b\n"\'=HYPERLINK(""http://x"")",-2\n\'@SUM(A1),\'+1\n');
});

// A day is the board's: on a board in Tokyo (UTC+9) A-3's done at 11-04 18:00Z falls on 11-05, and the period
// (11-02 00:00Z to 11-09 23:59Z) reaches into 11-10 there.
test("cumulative flow on a board in Asia/Tokyo", () => {
  c.setTimeZone("Asia/Tokyo");
  try {
    const rows = c.cumulativeFlow(tasks, activity, since, until).map((r) => [r.day, r.backlog, r.todo, r.doing, r.review, r.done]);
    assert.deepEqual(rows, [
      ["2026-11-02", 1, 2, 0, 0, 0],
      ["2026-11-03", 1, 1, 1, 0, 0],
      ["2026-11-04", 0, 1, 3, 0, 0],
      ["2026-11-05", 0, 1, 2, 0, 1],
      ["2026-11-06", 0, 1, 1, 1, 1],
      ["2026-11-07", 0, 1, 1, 1, 1],
      ["2026-11-08", 0, 1, 1, 1, 1],
      ["2026-11-09", 0, 1, 0, 1, 2],
      ["2026-11-10", 0, 1, 0, 1, 2],
    ]);
  } finally {
    c.setTimeZone("UTC");
  }
});

// Day starts follow the zone's clock across a daylight-saving change (Los Angeles, 2026-03-08 02:00 → 03:00).
test("days across a daylight-saving change", () => {
  c.setTimeZone("America/Los_Angeles");
  try {
    const d = c.days(new Date("2026-03-07T12:00:00Z"), new Date("2026-03-09T12:00:00Z"));
    assert.deepEqual(d.map((x) => x.toISOString()), ["2026-03-07T08:00:00.000Z", "2026-03-08T08:00:00.000Z", "2026-03-09T07:00:00.000Z"]);
    assert.equal(c.dayKey(new Date("2026-03-09T06:59:59Z")), "2026-03-08");
    assert.equal(c.weekStart(new Date("2026-03-11T20:00:00Z")).toISOString(), "2026-03-09T07:00:00.000Z"); // Monday
  } finally {
    c.setTimeZone("UTC");
  }
});
