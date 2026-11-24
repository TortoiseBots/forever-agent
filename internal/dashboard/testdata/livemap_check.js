"use strict";
// Run by view_test.go under node: the Positions panel's live map — the serve's picture, its version (so the page adds
// no rate of its own), the clear no-serve state, and what is not a live map (a turtle card, the plan view).
const assert = require("assert");
const C = require(process.argv[2]);

const card = (o) => Object.assign({ id: "127.0.0.1:8765", online: true, kind: "forever", label: { plan: "zulfarrak" } }, o);
const lm = (o) => Object.assign({ have: true, state: "live", map: 36, floor: 0, floors: ["ground"], members: 2, creatures: 1, seconds: 42.5,
  background: true, notes: ["background drawn"], at: "2026-11-24T20:00:00Z", age_ms: 3000 }, o);

// a live picture: the endpoint, the version and the words under it
const live = C.liveMapPanel(card({ live_map: lm() }));
assert.ok(live, "a forever card with a live map has a panel");
assert.strictEqual(live.src, "/api/map/" + encodeURIComponent("127.0.0.1:8765") + "?at=" + encodeURIComponent("2026-11-24T20:00:00Z"));
assert.strictEqual(live.version, "2026-11-24T20:00:00Z");
assert.ok(/map 36/.test(live.caption) && /2 members/.test(live.caption) && /1 hostile\b/.test(live.caption), live.caption);
assert.ok(/43 s of the run/.test(live.caption), live.caption);
assert.ok(/drawn 3 s ago/.test(live.caption), live.caption);
assert.strictEqual(live.line, "", "a picture has no no-serve line");
assert.deepStrictEqual(live.notes, ["background drawn"]);
assert.ok(/2 party members and 1 hostile creature/.test(live.alt), live.alt);

// the same version is the same request: the page re-asks nothing until the server's next picture
assert.strictEqual(C.liveMapPanel(card({ live_map: lm() })).src, live.src);
assert.notStrictEqual(C.liveMapPanel(card({ live_map: lm({ at: "2026-11-24T20:00:05Z" }) })).src, live.src);

// one member is said in the singular, and a multi-floor map names its floor
assert.ok(/1 member · 1 hostile/.test(C.liveMapPanel(card({ live_map: lm({ members: 1 }) })).caption));
assert.ok(/floor 2 of 3/.test(C.liveMapPanel(card({ live_map: lm({ floor: 1, floors: ["a", "b", "c"] }) })).caption));

// Eastern Kingdoms is map 0: the caption names it, never "map undefined" (the card always carries "map")
assert.ok(/^map 0 · 2 members · 1 hostile/.test(C.liveMapPanel(card({ live_map: lm({ map: 0 }) })).caption),
  C.liveMapPanel(card({ live_map: lm({ map: 0 }) })).caption);
assert.ok(/^Live map drawn by the serve: map 0, /.test(C.liveMapPanel(card({ live_map: lm({ map: 0 }) })).alt));

// no serve: the reason the server gave is the line, and no picture is requested
const gone = C.liveMapPanel(card({ online: false, live_map: { have: false, state: "no-serve", reason: "serveclient: status: rpc error -32002: world: offline" } }));
assert.strictEqual(gone.src, null, "a gone serve never leaves a stale picture");
assert.ok(/^No live map: /.test(gone.line) && /-32002/.test(gone.line), gone.line);

// the serve's own "nothing to draw yet" is not called a gone serve
const waiting = C.liveMapPanel(card({ live_map: { have: false, state: "waiting", reason: "nothing to draw yet: the character is not in the world" } }));
assert.ok(/^No live map: nothing to draw yet/.test(waiting.line), waiting.line);

// a card with no answer yet still says so, in words
assert.ok(/^No live map: the serve is not answering/.test(C.liveMapPanel(card({ online: false })).line));
assert.ok(/^No live map: the serve has not drawn one yet/.test(C.liveMapPanel(card({ live_map: { have: false, state: "waiting" } })).line));

// a turtle card and the plan view keep the recorded frames: no live map panel at all
assert.strictEqual(C.liveMapPanel(card({ kind: "turtle", live_map: lm() })), null);
assert.strictEqual(C.liveMapPanel(card({ synthetic: true, live_map: lm() })), null);
assert.strictEqual(C.liveMapPanel(null), null);

console.log("ok");
