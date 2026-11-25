"use strict";
// Run by view3d_test.go under node: the 3D view's pure model (tile selection, the frame math, the timeline and
// the text rules).
const assert = require("assert");
const C = require(process.argv[2]);
const close = (a, b, eps = 1e-6) => assert.ok(Math.abs(a - b) <= eps, a + " != " + b);

// the game's coordinates become the scene's: north is +x, up is +y, west is -z
assert.deepStrictEqual(C.worldPos(10, -20, 5), { x: 10, y: 5, z: 20 });

// the grid tile of a position, as the core names tiles
assert.deepStrictEqual(C.tileOf(0, 0), { x: 32, y: 32 });
assert.deepStrictEqual(C.tileOf(600, 600), { x: 30, y: 30 });
assert.deepStrictEqual(C.tileOf(-600, -600), { x: 33, y: 33 }); // -600 / 533 = -1.12, 32 - (-1.12) = 33.12
const ext = C.tileExtent({ x: 30, y: 31 });
close(ext.x0, 1 * C.TILE_YARDS); close(ext.x1, 2 * C.TILE_YARDS);

// the tile budget: the nearest tiles around the focus, never more than the budget and never more than 49
let tiles = C.selectTiles(600, 600, 9);
assert.strictEqual(tiles.length, 9);
assert.deepStrictEqual(tiles[0], { x: 30, y: 30 });
assert.strictEqual(new Set(tiles.map((t) => t.x + ":" + t.y)).size, 9);
assert.strictEqual(C.selectTiles(600, 600, 49).length, 49);
assert.strictEqual(C.selectTiles(600, 600, 500).length, 49);
assert.strictEqual(C.selectTiles(-17000, -17000, 49).length, 49); // a corner still fills the budget
for (const t of C.selectTiles(0, 0, 49)) assert.ok(t.x >= 0 && t.x < 64 && t.y >= 0 && t.y < 64, "tile outside the grid " + t.x + ":" + t.y);

// the request URLs the page builds
assert.strictEqual(C.sceneURL(389, [{ x: 30, y: 31 }]), "/api/view3d/scene?map=389&tiles=30%3A31");
assert.strictEqual(C.liveURL("127.0.0.1:8774"), "/api/view3d/live?id=127.0.0.1%3A8774");
assert.strictEqual(C.liveURL(""), "/api/view3d/live");
assert.strictEqual(C.recordsURL(), "/api/view3d/records");
assert.strictEqual(C.replayURL("run-a"), "/api/view3d/replay/run-a");

// markers: scene coordinates, the kind's colour and the facing as a unit vector (0 is north, pi/2 is west)
let m = C.markerModel([{ id: "0x01", name: "Tank", kind: "agent", x: 100, y: -200, z: 30, facing: Math.PI / 2, hp: 5, max_hp: 10 }]);
close(m[0].x, 100); close(m[0].y, 30); close(m[0].z, 200);
close(m[0].dirX, 0); close(m[0].dirZ, 1);
assert.strictEqual(m[0].color, C.KINDS.agent);
assert.strictEqual(C.markerModel([{ id: "x", kind: "nonsense" }])[0].kind, "neutral");

// interpolation: a unit's place and health between two frames, a unit that appears late, and the clamped ends
const frames = [
  { t: 0, units: [{ id: "a", name: "Tank", kind: "agent", x: 0, y: 0, z: 0, facing: 0, hp: 100, max_hp: 100, power: 20, max_power: 100, target: "b" },
    { id: "b", name: "Mob", kind: "hostile", x: 10, y: 0, z: 0, hp: 50, max_hp: 100 }] },
  { t: 10, units: [{ id: "a", name: "Tank", kind: "agent", x: 10, y: 20, z: 4, facing: Math.PI / 2, hp: 60, max_hp: 100, power: 40, max_power: 100, target: "b" },
    { id: "c", name: "Late", kind: "party", x: -5, y: 0, z: 0, hp: 10, max_hp: 10 }] },
];
let f = C.interpolate(frames, 5);
const byId = (fr, id) => fr.units.find((u) => u.id === id);
close(byId(f, "a").x, 5); close(byId(f, "a").y, 10); close(byId(f, "a").z, 2);
close(byId(f, "a").facing, Math.PI / 4); assert.strictEqual(byId(f, "a").hp, 80); assert.strictEqual(byId(f, "a").power, 30);
assert.ok(byId(f, "b"), "a unit the second frame dropped stays in place"); close(byId(f, "b").x, 10);
assert.ok(byId(f, "c"), "a unit that appears late is there");
assert.strictEqual(byId(f, "c").x, -5);
assert.strictEqual(C.interpolate(frames, -3).t, -3);
close(byId(C.interpolate(frames, 99), "a").x, 10);
assert.deepStrictEqual(C.interpolate([], 1), { t: 0, units: [], path: [] });

// a facing that crosses north takes the short way round
const turn = C.interpolate([
  { t: 0, units: [{ id: "a", x: 0, y: 0, z: 0, facing: 3.0 }] },
  { t: 1, units: [{ id: "a", x: 0, y: 0, z: 0, facing: -3.0 }] },
], 0.5);
assert.ok(Math.abs(turn.units[0].facing) > 3.1 && Math.abs(turn.units[0].facing) <= Math.PI + 0.01, "short turn: " + turn.units[0].facing);

// the timeline: play, speeds, seek, the end and a restart
let tl = C.newTimeline(10);
assert.strictEqual(tl.playing, true); assert.strictEqual(tl.rate, 1);
tl = C.advance(tl, 1.5); close(tl.t, 1.5);
tl = C.setRate(tl, 8); assert.strictEqual(tl.rate, 8);
tl = C.advance(tl, 1); close(tl.t, 9.5); assert.strictEqual(tl.ended, false);
tl = C.advance(tl, 10); close(tl.t, 10); assert.strictEqual(tl.ended, true); assert.strictEqual(tl.playing, false);
tl = C.playPause(tl); assert.strictEqual(tl.playing, true); assert.strictEqual(tl.t, 0);
assert.strictEqual(C.setRate(tl, 100).rate, 8);
assert.strictEqual(C.setRate(tl, 0.6).rate, 0.5);
assert.strictEqual(C.seek(tl, -5).t, 0);
assert.strictEqual(C.seek(tl, 99).t, 10);

// a poll is due only when none is in flight and the interval has passed
assert.strictEqual(C.pollDue(10000, 0, false, 2000), true);
assert.strictEqual(C.pollDue(1000, 0, false, 2000), false);
assert.strictEqual(C.pollDue(10000, 0, true, 2000), false);

// text: markup stays text, control characters go, a long name is cut
assert.strictEqual(C.cleanText("<b>Bob & co</b>"), "<b>Bob & co</b>");
assert.strictEqual(C.cleanText("  a\u0000b\t\tc\n"), "a b c");
assert.strictEqual(C.cleanText("x".repeat(200), 10), "x".repeat(10));
assert.strictEqual(C.cleanText(null), "");
const el = { innerHTML: null };
C.setText(el, "<i>name</i>");
assert.strictEqual(el.textContent, "<i>name</i>");
assert.strictEqual(el.innerHTML, null, "setText must never touch innerHTML");

// answers and scenes fill in their missing parts
const ans = C.normalizeAnswer({ mode: "replay", frames: [{ t: 5, units: [{ id: "a" }] }, { t: "1", units: [] }] });
assert.strictEqual(ans.mode, "replay"); assert.strictEqual(ans.frames.length, 2); assert.strictEqual(ans.frames[0].t, 1);
assert.deepStrictEqual(C.normalizeAnswer(null).units, []);
const sc = C.normalizeScene({ map: 389, tiles: [{ x: 1, y: 2, vertices: [0, 0, -5, 0, 1, 3], indices: [0, 1, 2] }, { x: 2, y: 2, missing: true }] });
assert.strictEqual(sc.low, -5); assert.strictEqual(sc.high, 3); assert.strictEqual(sc.tiles[1].missing, true);
assert.strictEqual(C.statusText("live", "no-serve", "no answer", 389), "No serve: no answer");
assert.ok(C.statusText("replay", "live", "", 389).startsWith("Replay"));

console.log("ok");
