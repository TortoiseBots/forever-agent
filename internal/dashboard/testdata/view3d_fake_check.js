"use strict";
// Run by view3d_test.go under node: the page's fetch path against a fake serve. The server answers what the
// dashboard's three routes answer; the check asks them through the core's URLs and proves the scene's triangles
// and the markers land at the game coordinates they came with. No browser is involved: this is the data path
// view3d.js uses (fetch -> normalizeScene/normalizeAnswer -> markerModel).
const assert = require("assert");
const http = require("http");
const C = require(process.argv[2]);
const close = (a, b, eps = 1e-6) => assert.ok(Math.abs(a - b) <= eps, a + " != " + b);

const scene = {
  map: 389, format: "forever-v16", truncated: false,
  tiles: [
    { x: 30, y: 30, min_z: 10, max_z: 10, triangles: 2, vertices: [600, 600, 10, 640, 600, 10, 640, 640, 10, 600, 640, 10], indices: [0, 1, 2, 0, 2, 3] },
    { x: 31, y: 30, missing: true },
  ],
  triangles: 2,
};
const live = {
  mode: "live", state: "live", agent: "127.0.0.1:8774", map: 389, at: "2026-11-25T04:00:00Z",
  units: [
    { id: "0x01", name: "Tank", kind: "agent", x: 100, y: -200, z: 30, facing: 0, hp: 5000, max_hp: 5000, target: "0xAA" },
    { id: "0xAA", name: "Scorpid", kind: "hostile", x: 110, y: -210, z: 30, hp: 800, max_hp: 1000 },
  ],
  chat: [{ kind: "chat", text: "hello" }], events: [{ kind: "death", text: "a bot died" }],
};

const seen = [];
const server = http.createServer((req, res) => {
  seen.push(req.url);
  const send = (obj, code) => { res.writeHead(code || 200, { "Content-Type": "application/json" }); res.end(JSON.stringify(obj)); };
  const u = new URL(req.url, "http://x");
  if (u.pathname === "/api/view3d/scene") {
    const map = u.searchParams.get("map"), tiles = (u.searchParams.get("tiles") || "").split(",");
    if (map !== "389" || tiles.length > 49) return send({ message: "bad request" }, 400);
    return send(scene);
  }
  if (u.pathname === "/api/view3d/live") return send(live);
  if (u.pathname === "/api/view3d/records") return send({ records: [{ name: "run-a", map: 389 }] });
  if (u.pathname === "/api/view3d/replay/run-a") {
    return send({ mode: "replay", map: 389, duration: 2, frames: [
      { t: 0, units: [{ id: "0x01", name: "Tank", kind: "agent", x: 0, y: 0, z: 0, hp: 100, max_hp: 100 }] },
      { t: 2, units: [{ id: "0x01", name: "Tank", kind: "agent", x: 0, y: -20, z: 0, hp: 50, max_hp: 100 }] },
    ] });
  }
  send({ message: "not found" }, 404);
});

server.listen(0, "127.0.0.1", async () => {
  const base = "http://127.0.0.1:" + server.address().port;
  const get = async (url) => {
    const r = await fetch(base + url, { cache: "no-store" });
    const j = await r.json();
    if (!r.ok) throw new Error((j && j.message) || "HTTP " + r.status);
    return j;
  };
  try {
    // The tiles the page asks for around the first marker position: 49, the nearest first.
    const tiles = C.selectTiles(100, 200, C.MAX_TILES);
    assert.strictEqual(tiles.length, 49);
    const sc = C.normalizeScene(await get(C.sceneURL(389, tiles)));
    assert.strictEqual(sc.map, 389);
    assert.strictEqual(sc.triangles, 2);
    assert.strictEqual(sc.tiles[0].triangles, 2);
    assert.strictEqual(sc.tiles[0].vertices.length, 12);
    assert.ok(sc.tiles[1].missing);
    assert.strictEqual(sc.high, 10);

    // A live frame: the markers at the game coordinates, in the scene's own mapping.
    const ans = C.normalizeAnswer(await get(C.liveURL("127.0.0.1:8774")));
    assert.strictEqual(ans.state, "live");
    const mk = C.markerModel(ans.units);
    close(mk[0].x, 100); close(mk[0].y, 30); close(mk[0].z, 200);
    close(mk[1].x, 110); close(mk[1].y, 30); close(mk[1].z, 210);
    assert.strictEqual(mk[0].kind, "agent");
    assert.strictEqual(ans.chat[0].text, "hello");

    // A replay: the timeline reads two frames, and the middle of the walk is a point on the line.
    const rep = C.normalizeAnswer(await get(C.replayURL("run-a")));
    assert.strictEqual(rep.duration, 2);
    const mid = C.interpolate(rep.frames, 1);
    close(mid.units[0].y, -10);
    assert.strictEqual(mid.units[0].hp, 75);
    let tl = C.newTimeline(rep.duration);
    tl = C.advance(tl, 1.5);
    close(C.interpolate(rep.frames, tl.t).units[0].y, -15);

    // The record picker's own route.
    const recs = await get(C.recordsURL());
    assert.strictEqual(recs.records[0].name, "run-a");

    // Every route was asked with the path view3d.js builds (a ': ' in tiles is percent-encoded).
    assert.ok(seen.some((u) => u.startsWith("/api/view3d/scene?map=389&tiles=")), "scene asked: " + seen.join(" "));
    assert.ok(seen.some((u) => u.startsWith("/api/view3d/live?id=127.0.0.1%3A8774")), "live asked: " + seen.join(" "));
    console.log("ok");
  } finally {
    server.close();
  }
});
