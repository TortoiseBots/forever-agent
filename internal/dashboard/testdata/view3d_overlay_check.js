"use strict";
// Run by view3d_test.go under node: the overlay model and the DOM it builds. A name with markup must land in
// textContent, never in innerHTML.
const assert = require("assert");
const O = require(process.argv[2]);

function elem(tag) {
  const e = {
    tag, className: "", textContent: "", hidden: false, style: {}, children: [],
    appendChild(c) { this.children.push(c); return c; },
    replaceChildren(...c) { this.children = c; },
    set innerHTML(_v) { throw new Error("innerHTML written"); },
    get innerHTML() { throw new Error("innerHTML read"); },
  };
  return e;
}
const doc = { createElement: elem };

const answer = {
  chat: [{ kind: "chat", text: "<script>alert(1)</script>" }, { kind: "chat", text: "normal line" }],
  events: [{ kind: "death", text: "Tank died" }, { kind: "pull", text: "pulling" }, { kind: "loot", text: "looted" },
    { kind: "a", text: "1" }, { kind: "b", text: "2" }, { kind: "c", text: "3" }, { kind: "d", text: "4" }, { kind: "e", text: "5" }, { kind: "f", text: "6" }, { kind: "g", text: "7" }],
  units: [
    { id: "0x01", name: "<img src=x onerror=alert(1)>", kind: "agent", hp: 50, max_hp: 100, power: 10, max_power: 40, target: "0xAA" },
    { id: "0xAA", name: "<b>Mob</b>", kind: "hostile", hp: 20, max_hp: 80, cast: "shadow bolt" },
  ],
};

const m = O.overlayModel(answer);
assert.strictEqual(m.agent.name, "<img src=x onerror=alert(1)>");
assert.strictEqual(m.agent.kind, "agent");
assert.strictEqual(m.target.name, "<b>Mob</b>");
assert.strictEqual(m.cast.who, "<b>Mob</b>");
assert.strictEqual(m.cast.text, "shadow bolt");
assert.strictEqual(m.chat.length, 2);
assert.strictEqual(m.events.length, O.SHOWN, "the overlay keeps the last lines only");
assert.strictEqual(m.events[0].text, "looted");

// A target the frames do not carry is still listed by its guid, never invented: no frame, no bars.
const partial = O.overlayModel({ units: [{ id: "0x01", kind: "agent", name: "Tank", target: "0xZZ" }] });
assert.strictEqual(partial.target.name, "0xZZ");
assert.strictEqual(partial.cast, null);

const host = elem("div");
const ov = O.createOverlay(doc, host);
ov.update(answer);
// the agent and target blocks: their names are text nodes, and the bars are widths
const names = [];
(function walk(e) { if (e.textContent) names.push(e.textContent); for (const c of e.children || []) walk(c); })(host);
assert.ok(names.includes("<img src=x onerror=alert(1)>"), "agent name as text");
assert.ok(names.includes("<b>Mob</b>"), "target name as text");
assert.ok(names.includes("<script>alert(1)</script>"), "chat line as text");
assert.ok(names.includes("looted"), "event line as text");
assert.ok(names.includes("<b>Mob</b>: shadow bolt"), "cast line: " + names.join(" | "));
assert.strictEqual(ov.agent.hp.style.width, "50%");
assert.strictEqual(ov.agent.power.style.width, "25%");
assert.strictEqual(ov.target.hp.style.width, "25%");
assert.strictEqual(ov.castBox.hidden, false);
assert.strictEqual(ov.chat.children.length, 2);
assert.strictEqual(ov.events.children.length, O.SHOWN);

// No frame at all: the blocks are hidden, not left showing the last run's numbers.
ov.update({ units: [], chat: [], events: [] });
assert.strictEqual(ov.agent.box.hidden, true);
assert.strictEqual(ov.target.box.hidden, true);
assert.strictEqual(ov.castBox.hidden, true);

console.log("ok");
