// The vision plugin's data rules (spec §8.5): one key per item, ids that sort by time, frames that carry their
// items, pasted text turned into the right item, notes into New task fields.
import { test } from "node:test";
import assert from "node:assert/strict";
import * as m from "../plugins/vision/model.mjs";

test("ids are lowercase keys that sort by time", () => {
  const a = m.newId(1000, () => 0.5), b = m.newId(2000, () => 0);
  assert.match(a, /^[a-z0-9]{15}$/);
  assert.ok(a < b);
});

test("keys", () => {
  assert.equal(m.itemKey("c1", "x"), "c1/items/x.json");
  assert.equal(m.blobKey("c1", "x", "image/jpeg"), "c1/blobs/x.jpg");
  assert.deepEqual(m.canvasIds(["c1/canvas.json", "c1/items/a.json", "c2/canvas.json", "c2/blobs/b.png", "x.json"]), ["c1", "c2"]);
  assert.equal(m.itemId("c1", "c1/items/abc.json"), "abc");
  assert.equal(m.itemId("c1", "c2/items/abc.json"), "");
  assert.equal(m.itemId("a.b", "a-b/items/abc.json"), ""); // a "." in a canvas id is a dot, not any character
  assert.equal(m.itemId("a.b", "a.b/items/abc.json"), "abc");
  assert.equal(m.itemId("c1", "c1/items/x/y.json"), "");
  assert.ok(m.isBlobKey("c1/blobs/x.png") && !m.isBlobKey("c1/items/x.json") && !m.isBlobKey("c1/canvas.json"));
});

test("clean keeps the type's fields, rounded", () => {
  assert.deepEqual(m.clean({ id: "q", type: "note", x: 1.4, y: 2.6, w: 200, h: 120, z: 3, text: "hi", url: "x" }),
    { type: "note", x: 1, y: 3, w: 200, h: 120, z: 3, text: "hi" });
  assert.deepEqual(m.clean({ type: "task", x: 0, y: 0, w: 1, h: 1, task: "RPG-1" }), { type: "task", x: 0, y: 0, w: 1, h: 1, z: 0, task: "RPG-1" });
});

test("a frame carries the items whose centre is inside it", () => {
  const f = { type: "frame", x: 0, y: 0, w: 100, h: 100 };
  const inA = { type: "note", x: 10, y: 10, w: 20, h: 20 }, edge = { type: "note", x: 80, y: 80, w: 60, h: 60 }, other = { type: "frame", x: 5, y: 5, w: 10, h: 10 };
  assert.deepEqual(m.inside(f, [f, inA, edge, other]), [inA]);
});

test("pasted text", () => {
  assert.deepEqual(m.fromText(" RPG-12 "), { type: "task", task: "RPG-12" });
  assert.deepEqual(m.fromText("https://example.com/a b"), { type: "note", text: "https://example.com/a b" });
  assert.deepEqual(m.fromText("https://example.com/ref"), { type: "link", url: "https://example.com/ref" });
  assert.deepEqual(m.fromText("Moody fog\nlike Limbo"), { type: "note", text: "Moody fog\nlike Limbo" });
});

test("make a task from a note", () => {
  assert.deepEqual(m.taskFields("Fog shader\nVolumetric, cheap\n", "From the vision board Art."),
    { title: "Fog shader", body: "Volumetric, cheap\n\nFrom the vision board Art." });
  assert.deepEqual(m.taskFields("Just a title"), { title: "Just a title", body: "" });
});

test("zoom keeps the point under the cursor", () => {
  const v = m.zoomAt({ x: 10, y: 20, zoom: 1 }, 2, 110, 120);
  // world point under (110,120) was (100,100); after: 100*2 + x = 110
  assert.deepEqual(v, { x: -90, y: -80, zoom: 2 });
  assert.equal(m.zoomAt({ x: 0, y: 0, zoom: 4 }, 2, 0, 0).zoom, 4);
});
