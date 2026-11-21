"use strict";
// Run by view_test.go under node: the run controls' button matrix, the confirm texts and the log lines.
const assert = require("assert");
const C = require(process.argv[2]);

const card = (o) => Object.assign({ id: "127.0.0.1:8765", online: true, character: "Tanky", label: { plan: "zulfarrak" }, run: { state: "running", mode: "" } }, o);
const verbs = (c) => C.runButtons(c).map((b) => b.verb);
const btn = (c, v) => C.runButtons(c).find((b) => b.verb === v);

// idle: only unbind, with a confirm
assert.deepStrictEqual(verbs(card({ run: { state: "stopped" } })), ["stop"]);
assert.strictEqual(btn(card({ run: { state: "stopped" } }), "stop").label, "Unbind kept party");
assert.strictEqual(btn(card({ run: { state: "stopped" } }), "stop").confirm, true);

// a group/leader run (mode ""): stop run only, confirm
assert.deepStrictEqual(verbs(card({ run: { state: "running", mode: "" } })), ["stop"]);
assert.strictEqual(btn(card({ run: { state: "running", mode: "" } }), "stop").label, "Stop run");
assert.strictEqual(btn(card({ run: { state: "running", mode: "" } }), "stop").confirm, true);

// an observe run: pause, resume, skip, stop
const obs = card({ run: { state: "running", mode: "observe", leader: { state: "running" } } });
assert.deepStrictEqual(verbs(obs), ["pause", "resume", "skip", "stop"]);
assert.strictEqual(btn(obs, "pause").enabled, true);
assert.strictEqual(btn(obs, "pause").confirm, true, "a pause is never sent without a confirm");
assert.strictEqual(btn(obs, "resume").enabled, false, "resume is off while the run leader is running");
assert.strictEqual(btn(obs, "skip").enabled, true);
assert.strictEqual(btn(obs, "stop").confirm, true);

// an observe run paused: resume on, pause off
const obsPaused = card({ run: { state: "running", mode: "observe", leader: { state: "paused" } } });
assert.strictEqual(btn(obsPaused, "pause").enabled, false);
assert.strictEqual(btn(obsPaused, "resume").enabled, true);

// a commander (remote) run: stop-keep-party (pause) and stop-and-unbind (stop), both with a confirm
for (const mode of ["remote", "commander"]) {
  const c = card({ run: { state: "running", mode } });
  assert.deepStrictEqual(verbs(c), ["pause", "stop"], mode);
  assert.strictEqual(btn(c, "pause").label, "Stop, keep party");
  assert.strictEqual(btn(c, "pause").confirm, true);
  assert.strictEqual(btn(c, "stop").label, "Stop and unbind");
  assert.strictEqual(btn(c, "stop").confirm, true);
}

// a Forever card: pause/resume/stop through the commander, plus the six orders
const fv = card({ kind: "forever", run: { state: "running" } });
assert.deepStrictEqual(verbs(fv), ["pause", "resume", "stop"]);
assert.strictEqual(btn(fv, "pause").action, "run");
assert.strictEqual(btn(fv, "pause").enabled, true);
assert.strictEqual(btn(fv, "resume").enabled, false);
assert.strictEqual(btn(fv, "stop").confirm, true);
assert.ok(/^Pauses/.test(C.confirmText(fv, "pause")), "a Forever pause says it pauses");
assert.ok(/stop the run/.test(C.confirmText(fv, "stop")), "a Forever stop says it stops the module's run");
const fvPaused = card({ kind: "forever", run: { state: "paused" } });
assert.strictEqual(btn(fvPaused, "pause").enabled, false);
assert.strictEqual(btn(fvPaused, "resume").enabled, true);
assert.deepStrictEqual(C.orderButtons(fv).map((b) => b.verb), C.ORDER_VERBS);
assert.deepStrictEqual(C.ORDER_VERBS, ["stay", "follow", "come", "attack", "pull", "release"]);
for (const b of C.orderButtons(fv)) assert.strictEqual(b.action, "order");
assert.strictEqual(C.orderButtons(fv)[0].label, "Stay");
assert.deepStrictEqual(C.orderButtons(card({ kind: "turtle" })), [], "a turtle card has no order buttons");
assert.deepStrictEqual(C.orderButtons(null), []);

// offline and synthetic cards have no controls
assert.deepStrictEqual(C.runButtons(card({ online: false })), []);
assert.deepStrictEqual(C.runButtons(null), []);
assert.deepStrictEqual(C.runButtons({ id: "1", online: true, synthetic: true }), []);

// the confirm texts name the character and the plan
assert.ok(C.confirmText(card({ run: { state: "running", mode: "remote" } }), "pause").indexOf("Tanky") >= 0);
assert.ok(C.confirmText(card({ run: { state: "running", mode: "remote" } }), "pause").indexOf("zulfarrak") >= 0);
assert.ok(C.confirmText(card({ run: { state: "running", mode: "remote" } }), "stop").indexOf("zulfarrak") >= 0);
assert.ok(C.confirmText(card({ run: { state: "stopped" } }), "stop").indexOf("zulfarrak") >= 0);
assert.ok(/resume/.test(C.confirmText(card({ run: { state: "running", mode: "remote" } }), "pause")));
assert.ok(/^Pauses/.test(C.confirmText(card({ run: { state: "running", mode: "observe" } }), "pause")), "an observe pause says it pauses");

// the log lines: time, who, what
const lines = C.logLines({ lines: [{ at: "2026-11-01T12:34:56Z", who: "owner", via: "trusted", verb: "pause", ok: true },
  { at: "2026-11-01T12:35:00Z", who: "owner", verb: "stop", ok: false, message: "boo" }] });
assert.strictEqual(lines.length, 2);
assert.ok(lines[0].indexOf("12:34:56") >= 0 && lines[0].indexOf("owner") >= 0 && lines[0].indexOf("pause ok") >= 0 && lines[0].indexOf("trusted") >= 0);
assert.ok(lines[1].indexOf("stop failed") >= 0 && lines[1].indexOf("boo") >= 0);
assert.ok(C.logLines({ lines: [{ at: "2026-11-01T12:36:00Z", who: "owner", verb: "pause", outcome: "indeterminate" }] })[0].indexOf("pause indeterminate") >= 0);
assert.deepStrictEqual(C.logLines(null), []);

console.log("ok");

// ---- the Positions panel (A2): unproject inverts Project, the request body
// is exact (map included), and the map click-picker arms, picks and disarms
assert.deepStrictEqual(C.unproject(-(-44), -(-18)), { x: -18, y: -44 }, "unproject(Project(x,y)) == (x,y)");
const proj = (x, y) => [-y, -x];
for (const [x, y] of [[-18.24, -46.69], [0, 0], [12.5, -30]]) {
  const [sx, sy] = proj(x, y);
  assert.deepStrictEqual(C.unproject(sx, sy), { x, y });
}
assert.deepStrictEqual(C.PLACE_SHAPES, ["close", "circle", "arc", "spread", "line", "column", "spear", "arrow"]);
assert.ok(C.placePanel({ id: "1", online: true, kind: "forever" }), "a Forever card has a Positions panel");
assert.strictEqual(C.placePanel({ id: "1", online: true, kind: "turtle" }), null);
const table = [
  [{ verb: "place", shape: "circle", gap: "wide", anchor: "target", who: ["Healers"] },
    { verb: "place", shape: "circle", gap: "wide", anchor: "target", who: ["Healers"] }],
  [{ verb: "place", shape: "line", gap: "5", anchor: "point", point: { x: 12.5, y: -30 }, map: 389 },
    { verb: "place", shape: "line", gap: "5.0", anchor: "point", point: { x: 12.5, y: -30 }, map: 389 }],
  [{ verb: "place_follow", who: ["Tanka"] }, { verb: "place_follow", who: ["Tanka"] }],
  [{ verb: "rally", rally: "point", point: { x: 1, y: 2 }, map: 389 },
    { verb: "rally", rally: "point", point: { x: 1, y: 2 }, map: 389 }],
  [{ verb: "fallback", to: "point", point: { x: 1, y: 2 }, map: 389 },
    { verb: "fallback", to: "point", point: { x: 1, y: 2 }, map: 389 }],
  [{ verb: "resume" }, { verb: "resume" }],
];
for (const [pick, want] of table) {
  const got = C.placeArgs(pick);
  if (got.error) throw new Error("placeArgs(" + JSON.stringify(pick) + ") failed: " + got.error);
  assert.deepStrictEqual(got.body, want, JSON.stringify(pick));
}
assert.ok(C.placeArgs({ verb: "place", shape: "square", anchor: "here" }).error, "a bad shape is an error");
assert.ok(C.placeArgs({ verb: "place", shape: "line", gap: "13", anchor: "here" }).error, "a gap of 13 is an error");
assert.ok(C.placeArgs({ verb: "place", shape: "line", anchor: "point", point: { x: 1, y: 2 } }).error, "a point without its map is an error");
assert.ok(C.placeArgs({ verb: "place", shape: "line", anchor: "point", point: { x: 1e6, y: 2 }, map: 389 }).error, "a point off the map is an error");
assert.ok(C.placeArgs({ verb: "rally", rally: "point", point: { x: NaN, y: 2 }, map: 389 }).error, "a non-finite point is an error");

// The map click-picker: arm attaches the click listener and the crosshair,
// a click unprojects the point and binds it to the card's map, disarm removes both.
function fakeSvg() {
  const listeners = new Map();
  const m = { a: 1, b: 0, c: 0, d: 1, e: 0, f: 0 };
  m.inverse = () => m;
  return {
    style: {},
    listener: (t) => listeners.get(t),
    addEventListener(t, f) { listeners.set(t, f); },
    removeEventListener(t, f) { if (listeners.get(t) === f) listeners.delete(t); },
    createSVGPoint() {
      return { x: 0, y: 0, matrixTransform(mat) { return { x: mat.a * this.x + mat.c * this.y + mat.e, y: mat.b * this.x + mat.d * this.y + mat.f }; } };
    },
    getScreenCTM() { return m; },
    fire(ev) { const f = listeners.get("click"); if (f) f(ev); },
  };
}
const pickS = { msg: { textContent: "", hidden: true }, place: { point: null, pointMap: null, pointLbl: { textContent: "" } } };
const svg = fakeSvg();
C.armPointPick(pickS, { id: "1", map: 389 }, svg);
assert.ok(svg.listener("click"), "arm attaches the click handler");
assert.strictEqual(svg.style.cursor, "crosshair", "arm sets the crosshair");
svg.fire({ clientX: 44, clientY: 18, defaultPrevented: false });
assert.deepStrictEqual(pickS.place.point, { x: -18, y: -44 }, "the click is unprojected");
assert.strictEqual(pickS.place.pointMap, 389, "the point is bound to the card's map");
assert.ok(!svg.listener("click"), "a pick disarms the handler");
assert.strictEqual(svg.style.cursor, "", "a pick restores the cursor");
assert.ok(pickS.place.pointLbl.textContent.indexOf("-18.00") >= 0);

const noMap = { msg: { textContent: "", hidden: true }, place: { point: null, pointMap: null, pointLbl: { textContent: "" } } };
const svg2 = fakeSvg();
C.armPointPick(noMap, { id: "1", map: null }, svg2);
assert.ok(!svg2.listener("click"), "a card without a map arms nothing");
assert.ok(/no map/.test(noMap.msg.textContent));
C.armPointPick(noMap, { id: "1", map: 389 }, null);
assert.ok(/not drawn/.test(noMap.msg.textContent), "a missing svg is said, never a silent no-op");

// ---- the Positions send buttons are held while a placement is in flight, and the elsewhere line (A2) ----
// fakeH is the page's h() reduced to what the control box reads and writes; a disabled button fires no click, as in a browser.
function fakeH(tag, attrs, ...kids) {
  const a = attrs || {};
  const el = { tag, children: [], listeners: {}, disabled: false, hidden: !!a.hidden, title: a.title || "", dataset: {}, style: {},
    className: a.class || "", textContent: a.text != null ? a.text : "", _v: a.value,
    append(...xs) { el.children.push(...xs); },
    replaceChildren(...xs) { el.children = xs.slice(); },
    addEventListener(t, f) { (el.listeners[t] = el.listeners[t] || []).push(f); },
    removeEventListener() {}, remove() {},
    click() { if (!el.disabled) for (const f of el.listeners.click || []) f({ defaultPrevented: false }); } };
  Object.defineProperty(el, "options", { get() { return el.children.filter((c) => c.tag === "option"); } });
  Object.defineProperty(el, "selectedOptions", { get() { return []; } });
  Object.defineProperty(el, "value", { get() { if (el._v !== undefined) return el._v; const o = el.options[0]; return o ? o.value : ""; }, set(v) { el._v = v; } });
  el.append(...kids);
  return el;
}
(async () => {
  const settle = () => new Promise((r) => setTimeout(r, 0));
  const find = (root, pred) => {
    if (pred(root)) return root;
    for (const c of root.children) { const f = find(c, pred); if (f) return f; }
    return null;
  };
  const text = (n) => typeof n.textContent === "string" ? n.textContent : "";
  const posts = [];
  let pending = null; // the one placement POST in flight: settled by the test
  global.fetch = (url, opts) => {
    if (url === "/api/control/status") return Promise.resolve({ ok: true, status: 200, json: async () => ({ enabled: true, local_trusted: true, trust_nonce: "n" }) });
    if (opts && opts.method === "POST") { posts.push(url); return new Promise((resolve, reject) => { pending = { resolve, reject }; }); }
    return Promise.resolve({ ok: true, status: 200, json: async () => ({ lines: [] }) });
  };
  const card = { id: "127.0.0.1:8774", online: true, kind: "forever", character: "Atank", label: { plan: "" }, run: { state: "stopped" },
    elsewhere: ["Aheal (map 1)", "rally point (map 1)"] };
  const ctl = C.create({ h: fakeH, rerender() {}, footer() {} });
  const box = fakeH("div", {});
  ctl.render(box, card);
  await settle(); await settle();
  ctl.render(box, card);
  const panel = find(box, (n) => n.className === "placebox");
  const btn = (label) => find(panel, (n) => n.tag === "button" && n.textContent === label);
  const place = btn("Place"), release = btn("Release"), resume = btn("Resume");
  assert.ok(place && release && resume, "the Positions send buttons are drawn");
  const line = find(panel, (n) => text(n).indexOf("Elsewhere") === 0);
  assert.ok(line && !line.hidden && text(line).indexOf("Aheal (map 1)") >= 0 && text(line).indexOf("rally point (map 1)") >= 0,
    "the elsewhere line names the bots and the rally point");

  place.click();
  assert.strictEqual(posts.length, 1, "one click sends one order");
  assert.ok(place.disabled && release.disabled && resume.disabled, "every send button is held while the placement is in flight");
  place.click(); release.click();
  assert.strictEqual(posts.length, 1, "a second click sends no second order");
  pending.resolve({ ok: true, status: 200, json: async () => ({ status: "ok", message: "placed", rpc: "x" }) });
  await settle(); await settle();
  assert.ok(!place.disabled && !release.disabled && !resume.disabled, "the answer re-enables the send buttons");

  place.click();
  assert.strictEqual(posts.length, 2, "a send after the answer goes out");
  pending.reject(new Error("link down"));
  await settle(); await settle();
  assert.ok(!place.disabled, "a send that fails re-enables the send buttons");
  assert.ok(find(box, (n) => text(n).indexOf("Could not reach the dashboard") === 0), "the failed send is said");

  ctl.render(box, Object.assign({}, card, { elsewhere: [] }));
  assert.ok(line.hidden, "no elsewhere line when every bot is on the shown map");
  console.log("ok placement");
})().catch((e) => { console.error(e); process.exit(1); });
