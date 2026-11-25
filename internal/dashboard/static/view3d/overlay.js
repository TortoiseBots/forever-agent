"use strict";
// The 3D view's HTML overlay: the agent and target frames with health and power, a cast bar, the last party
// chat lines and the agent's last events. Every piece of text is set with textContent (Core.setText): names come
// from a game server and are never markup. The model half (overlayModel) is pure and the node check runs it.
(function (root) {
  const Core = root.View3DCore || (typeof require !== "undefined" ? require("./core.js") : null);
  const SHOWN = 8; // lines of chat and events the overlay keeps on screen

  function lastLines(list, n) {
    const out = [];
    const src = Array.isArray(list) ? list : [];
    for (let i = Math.max(0, src.length - n); i < src.length; i++) {
      const line = src[i] || {};
      out.push({ kind: line.kind || "", text: Core.cleanText(line.text || "", 200) });
    }
    return out;
  }

  // What the overlay shows for one frame: the agent, its target, the cast in hand, chat and events. Nil when the
  // frame has no such thing.
  function overlayModel(answer, agentId) {
    const units = (answer && answer.units) || [];
    let agent = null, target = null, cast = null;
    for (const u of units) if (u.kind === "agent") { agent = u; break; }
    if (!agent && agentId) for (const u of units) if (u.id === agentId) { agent = u; break; }
    if (agent && agent.target) {
      for (const u of units) if (u.id === agent.target) { target = u; break; }
      if (!target) target = { id: agent.target, name: agent.target, kind: "neutral", hp: 0, max_hp: 0 };
    }
    for (const u of units) if (u.cast) { cast = { who: u.name, text: u.cast, hp: 1, max_hp: 1, x: 0 }; break; }
    return {
      agent: agent && frameOf(agent), target: target && frameOf(target), cast: cast && { who: Core.cleanText(cast.who), text: Core.cleanText(cast.text) },
      chat: lastLines(answer && answer.chat, SHOWN), events: lastLines(answer && answer.events, SHOWN),
      notes: ((answer && answer.notes) || []).map((n) => Core.cleanText(n, 200)),
    };
  }

  function frameOf(u) {
    return { name: Core.cleanText(u.name || "?"), kind: u.kind || "neutral", hp: Core.fraction(u.hp, u.max_hp), power: Core.fraction(u.power, u.max_power),
      dead: !!u.dead, hpText: Math.max(0, u.hp || 0) + "/" + Math.max(0, u.max_hp || 0), powerText: Math.max(0, u.power || 0) + "/" + Math.max(0, u.max_power || 0) };
  }

  function el(doc, tag, cls, parent) {
    const e = doc.createElement(tag);
    if (cls) e.className = cls;
    if (parent) parent.appendChild(e);
    return e;
  }

  // A frame block: a name, a health bar and a power bar (the target has no power bar).
  function frameBlock(doc, parent, title) {
    const box = el(doc, "div", "v3d-frame", parent);
    const head = el(doc, "div", "v3d-frame-head", box);
    const name = el(doc, "span", "v3d-name", head);
    el(doc, "span", "v3d-frame-title", head).textContent = title;
    const hpWrap = el(doc, "div", "v3d-bar v3d-hp", box);
    const hp = el(doc, "i", "", hpWrap);
    const powerWrap = el(doc, "div", "v3d-bar v3d-power", box);
    const power = el(doc, "i", "", powerWrap);
    const text = el(doc, "div", "v3d-small muted", box);
    return { box, name, hp, power, powerWrap, text };
  }

  // Build the overlay inside host and return {update(answer, agentId), setStatus(text), host}.
  function createOverlay(doc, host) {
    host.replaceChildren();
    const frames = el(doc, "div", "v3d-frames", host);
    const agent = frameBlock(doc, frames, "agent");
    const target = frameBlock(doc, frames, "target");
    const castBox = el(doc, "div", "v3d-cast", frames);
    const castName = el(doc, "div", "v3d-small", castBox);
    const castWrap = el(doc, "div", "v3d-bar v3d-castbar", castBox);
    const castFill = el(doc, "i", "", castWrap);
    const chatBox = el(doc, "div", "v3d-lines", host);
    el(doc, "div", "v3d-lines-title", chatBox).textContent = "Party chat";
    const chat = el(doc, "ol", "v3d-chat", chatBox);
    const evBox = el(doc, "div", "v3d-lines", host);
    el(doc, "div", "v3d-lines-title", evBox).textContent = "Agent events";
    const events = el(doc, "ol", "v3d-events", evBox);

    function setFrame(b, f) {
      b.box.hidden = !f;
      if (!f) return;
      Core.setText(b.name, f.name);
      b.hp.style.width = Math.round(f.hp * 100) + "%";
      b.power.style.width = Math.round(f.power * 100) + "%";
      b.powerWrap.hidden = b.powerWrap && !f.powerText;
      b.text.textContent = f.hpText + (f.powerText && b === agent ? " · " + f.powerText : "") + (f.dead ? " · dead" : "");
      b.box.className = "v3d-frame v3d-" + f.kind;
    }
    function setLines(list, ol) {
      ol.replaceChildren();
      for (const line of list) {
        const li = el(doc, "li", "v3d-" + (line.kind || "line"), ol);
        li.textContent = line.text; // textContent only: a name with markup stays text
      }
    }
    function update(answer, agentId) {
      const m = overlayModel(answer, agentId);
      setFrame(agent, m.agent);
      setFrame(target, m.target);
      castBox.hidden = !m.cast;
      if (m.cast) { castName.textContent = m.cast.who + ": " + m.cast.text; castFill.style.width = "100%"; }
      setLines(m.chat, chat);
      setLines(m.events, events);
    }
    return { update, host, agent, target, castBox, chat, events };
  }

  const api = { overlayModel, createOverlay, SHOWN };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.View3DOverlay = api;
})(typeof self !== "undefined" ? self : this);
