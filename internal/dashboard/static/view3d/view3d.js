// The 3D view (#250, stage 1): the walkable mesh around the agent as a shaded surface, the units as capsules with a
// facing wedge, a name and a health bar, and an orbit camera that follows the agent. Two modes feed it: live (the
// dashboard's own poll of a serve slot) and replay (a recorded run, with a timeline). The data contract is in
// docs/manual/developers/view3d-data-format.md; core.js holds the pure model, overlay.js the HTML overlay.
import * as THREE from "./three.module.min.js";

const Core = window.View3DCore;
const Overlay = window.View3DOverlay;
const $ = (id) => document.getElementById(id);
const POLL_MS = 2000; // the live poll: the dashboard answers from its own cache, this is its read rate
const FOLLOW_RADIUS = 34, FOLLOW_PHI = 1.02;

const state = {
  mode: "live", agent: "", record: "", answer: null, scene: null, map: null, sceneKey: "",
  timeline: Core.newTimeline(0), replayFrames: [], lastPoll: 0, focus: new THREE.Vector3(), focusSet: false,
  agentUnit: null, polling: false, ctrl: null,
};

// ---------------------------------------------------------------- renderer, camera, lights

const canvas = $("view");
const renderer = new THREE.WebGLRenderer({ canvas, antialias: true });
renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
renderer.outputColorSpace = THREE.SRGBColorSpace;

const scene = new THREE.Scene();
scene.background = new THREE.Color(0x0c1116);
scene.fog = new THREE.Fog(0x0c1116, 220, 520);

const camera = new THREE.PerspectiveCamera(55, 1, 0.5, 4000);
const cam = { radius: FOLLOW_RADIUS, theta: Math.PI * 0.25, phi: FOLLOW_PHI, target: new THREE.Vector3() };

scene.add(new THREE.HemisphereLight(0xbfd4e6, 0x1a2229, 1.15));
const sun = new THREE.DirectionalLight(0xfff2d9, 1.5);
sun.position.set(-120, 260, 90);
scene.add(sun);

const tileGroup = new THREE.Group();
const markerGroup = new THREE.Group();
const lineGroup = new THREE.Group();
scene.add(tileGroup, markerGroup, lineGroup);

const targetLine = makeLine(0xffd257, 0.55);
const pathLine = makeLine(0x63e6be, 0.9);
lineGroup.add(targetLine, pathLine);

function makeLine(color, width) {
  const g = new THREE.BufferGeometry();
  g.setAttribute("position", new THREE.BufferAttribute(new Float32Array(6), 3));
  const m = new THREE.LineBasicMaterial({ color, transparent: true, opacity: 0.85, linewidth: width });
  const l = new THREE.Line(g, m);
  l.visible = false;
  return l;
}

function resize() {
  const w = canvas.clientWidth || 640, h = canvas.clientHeight || 400;
  renderer.setSize(w, h, false);
  camera.aspect = w / Math.max(1, h);
  camera.updateProjectionMatrix();
}
window.addEventListener("resize", resize);

// ---------------------------------------------------------------- orbit controls (mouse and touch)

let drag = null;
canvas.addEventListener("pointerdown", (e) => {
  drag = { x: e.clientX, y: e.clientY, button: e.button };
  canvas.setPointerCapture(e.pointerId);
});
canvas.addEventListener("pointerup", (e) => {
  drag = null;
  try { canvas.releasePointerCapture(e.pointerId); } catch (_) { /* already released */ }
});
canvas.addEventListener("pointermove", (e) => {
  if (!drag) return;
  const dx = e.clientX - drag.x, dy = e.clientY - drag.y;
  drag.x = e.clientX;
  drag.y = e.clientY;
  if (drag.button === 0) {
    cam.theta -= dx * 0.006;
    cam.phi = Math.max(0.12, Math.min(1.52, cam.phi - dy * 0.005));
  } else {
    // The right button pans the focus over the ground. With Follow on the agent pulls it back; with Follow off the
    // camera stays where it was put.
    const k = cam.radius * 0.0016;
    state.focus.x -= Math.cos(cam.theta) * dx * k + Math.sin(cam.theta) * dy * k;
    state.focus.z += Math.sin(cam.theta) * dx * k - Math.cos(cam.theta) * dy * k;
    state.focusSet = true;
  }
});
canvas.addEventListener("wheel", (e) => {
  e.preventDefault();
  cam.radius = Math.max(8, Math.min(900, cam.radius * (1 + Math.sign(e.deltaY) * 0.12)));
}, { passive: false });
canvas.addEventListener("contextmenu", (e) => e.preventDefault());

// ---------------------------------------------------------------- tiles

const heightLow = new THREE.Color(0x2f4a3c), heightHigh = new THREE.Color(0xd8c79a);

function buildTiles(json, url) {
  for (const child of [...tileGroup.children]) {
    tileGroup.remove(child);
    child.geometry.dispose();
    child.material.dispose();
  }
  const sc = Core.normalizeScene(json);
  state.scene = sc;
  const span = Math.max(1, sc.high - sc.low);
  for (const t of sc.tiles) {
    if (t.missing || !t.vertices.length || !t.indices.length) continue;
    const n = t.vertices.length / 3;
    const pos = new Float32Array(n * 3), col = new Float32Array(n * 3);
    for (let i = 0; i < n; i++) {
      const p = Core.worldPos(t.vertices[i * 3], t.vertices[i * 3 + 1], t.vertices[i * 3 + 2]);
      pos[i * 3] = p.x; pos[i * 3 + 1] = p.y; pos[i * 3 + 2] = p.z;
      const k = Math.max(0, Math.min(1, (t.vertices[i * 3 + 2] - sc.low) / span));
      const c = heightLow.clone().lerp(heightHigh, k);
      col[i * 3] = c.r; col[i * 3 + 1] = c.g; col[i * 3 + 2] = c.b;
    }
    const geo = new THREE.BufferGeometry();
    geo.setAttribute("position", new THREE.BufferAttribute(pos, 3));
    geo.setAttribute("color", new THREE.BufferAttribute(col, 3));
    geo.setIndex(t.indices);
    geo.computeVertexNormals();
    const mesh = new THREE.Mesh(geo, new THREE.MeshLambertMaterial({ vertexColors: true, side: THREE.DoubleSide }));
    mesh.name = "tile " + t.x + ":" + t.y;
    tileGroup.add(mesh);
  }
  state.sceneKey = url;
  if (sc.note || sc.truncated) setStatus(sc.note || "The mesh was cut to its tile or triangle budget around you.", true);
}

async function loadScene(focus) {
  if (state.map == null) return;
  const tiles = Core.selectTiles(focus.x, -focus.z, Core.MAX_TILES);
  const url = Core.sceneURL(state.map, tiles);
  if (url === state.sceneKey) return;
  state.sceneKey = url;
  try {
    const json = await getJSON(url);
    if (state.sceneKey !== url) return; // a newer scene was asked for while this one was in flight
    buildTiles(json, url);
  } catch (err) {
    if (state.sceneKey !== url) return; // a newer scene was asked for while this one was in flight
    state.sceneKey = "";
    setStatus(String(err.message || err), true);
  }
}

// ---------------------------------------------------------------- markers

const labels = new Map(); // "<kind>|<name>" -> SpriteMaterial
const markers = new Map(); // guid -> marker

const capsuleGeo = new THREE.CapsuleGeometry(0.65, 1.7, 4, 10);
const wedgeGeo = wedgeGeometry();

// A flat triangle in front of the capsule: the unit's facing.
function wedgeGeometry() {
  const g = new THREE.BufferGeometry();
  g.setAttribute("position", new THREE.BufferAttribute(new Float32Array([0, 0, 0, 2.4, 0, 0.9, 2.4, 0, -0.9]), 3));
  g.computeVertexNormals();
  return g;
}

function labelMaterial(name, kind) {
  const key = kind + "|" + name;
  let m = labels.get(key);
  if (m) return m;
  const c = document.createElement("canvas");
  c.width = 512; c.height = 72;
  const g = c.getContext("2d");
  g.font = "600 34px " + getComputedStyle(document.body).fontFamily;
  g.textAlign = "center";
  g.textBaseline = "middle";
  const text = name.length > 32 ? name.slice(0, 31) + "…" : name;
  g.lineWidth = 6;
  g.strokeStyle = "rgba(0,0,0,0.85)";
  g.strokeText(text, 256, 38);
  g.fillStyle = Core.KINDS[kind] || Core.KINDS.neutral;
  g.fillText(text, 256, 38);
  const tex = new THREE.CanvasTexture(c);
  tex.colorSpace = THREE.SRGBColorSpace;
  m = new THREE.SpriteMaterial({ map: tex, depthTest: false, transparent: true });
  labels.set(key, m);
  if (labels.size > 400) labels.delete(labels.keys().next().value); // bounded: names are untrusted
  return m;
}

function makeMarker(m) {
  const group = new THREE.Group();
  const color = new THREE.Color(m.color);
  const body = new THREE.Mesh(capsuleGeo, new THREE.MeshLambertMaterial({ color }));
  body.position.y = 1.6;
  const wedge = new THREE.Mesh(wedgeGeo, new THREE.MeshBasicMaterial({ color, transparent: true, opacity: 0.55, side: THREE.DoubleSide }));
  wedge.position.y = 0.12;
  const label = new THREE.Sprite(labelMaterial(m.name, m.kind));
  label.scale.set(12, 1.7, 1);
  label.position.y = 4.6;
  label.renderOrder = 3;
  const barBg = new THREE.Sprite(new THREE.SpriteMaterial({ color: 0x11161a, depthTest: false, transparent: true, opacity: 0.85 }));
  barBg.scale.set(5.0, 0.5, 1);
  barBg.position.y = 3.55;
  barBg.renderOrder = 2;
  const bar = new THREE.Sprite(new THREE.SpriteMaterial({ color, depthTest: false }));
  bar.scale.set(4.8, 0.3, 1);
  bar.position.y = 3.55;
  bar.renderOrder = 4;
  group.add(body, wedge, label, barBg, bar);
  markerGroup.add(group);
  const rec = { group, body, wedge, label, bar, barBg, name: m.name, kind: m.kind };
  markers.set(m.id, rec);
  return rec;
}

function updateMarkers(units) {
  const seen = new Set();
  let agent = null;
  for (const m of Core.markerModel(units)) {
    seen.add(m.id);
    let rec = markers.get(m.id);
    if (!rec || rec.name !== m.name || rec.kind !== m.kind) {
      if (rec) removeMarker(m.id);
      rec = makeMarker(m);
    }
    rec.group.position.set(m.x, m.y, m.z);
    rec.group.rotation.y = Core.facingYaw(m.dirX, m.dirZ);
    rec.body.material.opacity = m.dead ? 0.3 : 1;
    rec.body.material.transparent = m.dead;
    const frac = Core.fraction(m.hp, m.max_hp);
    rec.bar.scale.x = 4.8 * Math.max(0.001, frac);
    rec.bar.position.x = -2.4 + rec.bar.scale.x / 2;
    rec.bar.material.color.set(m.color);
    if (m.dead) rec.bar.material.color.set(0x555555);
    if (m.kind === "agent") agent = m;
  }
  for (const id of [...markers.keys()]) if (!seen.has(id)) removeMarker(id);
  return agent;
}

function removeMarkers() {
  for (const id of [...markers.keys()]) removeMarker(id);
}

function removeMarker(id) {
  const rec = markers.get(id);
  if (!rec) return;
  markerGroup.remove(rec.group);
  rec.body.material.dispose();
  rec.wedge.material.dispose();
  rec.bar.material.dispose();
  rec.barBg.material.dispose();
  markers.delete(id);
}

function drawTargetLine(units, agent) {
  const target = agent && units.find((u) => u.id === agent.target);
  if (!agent || !target) { targetLine.visible = false; return; }
  const a = Core.worldPos(agent.x, agent.y, agent.z), b = Core.worldPos(target.x, target.y, target.z);
  const p = targetLine.geometry.attributes.position;
  p.setXYZ(0, a.x, a.y + 1.4, a.z);
  p.setXYZ(1, b.x, b.y + 1.4, b.z);
  p.needsUpdate = true;
  targetLine.geometry.computeBoundingSphere();
  targetLine.visible = true;
}

function drawPath(path) {
  if (!path || path.length < 2) { pathLine.visible = false; return; }
  const pts = new Float32Array(path.length * 3);
  path.forEach((p, i) => {
    const q = Core.worldPos(p[0], p[1], p[2]);
    pts[i * 3] = q.x; pts[i * 3 + 1] = q.y + 0.35; pts[i * 3 + 2] = q.z;
  });
  pathLine.geometry.dispose();
  const g = new THREE.BufferGeometry();
  g.setAttribute("position", new THREE.BufferAttribute(pts, 3));
  pathLine.geometry = g;
  pathLine.visible = true;
}

// ---------------------------------------------------------------- camera

// agent is the marker model: its x, y and z are already the scene's (see Core.worldPos).
function followAgent(agent) {
  if (!agent || !$("follow").checked) return;
  state.focus.lerp(new THREE.Vector3(agent.x, agent.y + 2.2, agent.z), 0.18);
  state.focusSet = true;
}

function placeCamera() {
  if (cam.target.lengthSq() > 0) cam.target.lerp(state.focus, 0.12);
  else cam.target.copy(state.focus);
  const r = cam.radius, sp = Math.sin(cam.phi), cp = Math.cos(cam.phi);
  camera.position.set(
    cam.target.x + r * sp * Math.cos(cam.theta),
    cam.target.y + r * cp,
    cam.target.z + r * sp * Math.sin(cam.theta),
  );
  camera.lookAt(cam.target);
}

// ---------------------------------------------------------------- live and replay

async function getJSON(url, signal) {
  const r = await fetch(url, { cache: "no-store", signal });
  let j = null;
  try { j = await r.json(); } catch (_) { /* not JSON */ }
  if (!r.ok) throw new Error((j && (j.message || j.reason)) || ("HTTP " + r.status));
  return j;
}

function applyAnswer(raw) {
  const ans = Core.normalizeAnswer(raw);
  state.answer = ans;
  const map = Core.answerMap(state.map, ans);
  if (map != null && map !== state.map) {
    state.map = map;
    state.sceneKey = "";
    removeMarkers();
    loadScene(state.focus);
  }
  let units = [];
  if (ans.mode === "replay") {
    const at = Core.interpolate(ans.frames, state.timeline.t);
    units = at.units;
    drawPath(at.path);
  } else {
    units = ans.units;
    drawPath([]);
  }
  const agent = updateMarkers(units);
  state.agentUnit = agent;
  if (agent && !state.focusSet) {
    state.focus.set(agent.x, agent.y + 2.2, agent.z);
    state.focusSet = true;
    loadScene(state.focus);
  }
  const model = Core.markerModel(units);
  drawTargetLine(model, model.find((u) => u.kind === "agent"));
  overlay.update({ chat: ans.chat, events: ans.events, notes: ans.notes, units }, state.agent || ans.agent);
  if (ans.mode === "live") {
    setStatus(Core.statusText("live", ans.state, ans.reason, ans.map), ans.state !== "live");
    if (ans.state !== "live") {
      removeMarkers();
      state.agentUnit = null;
      targetLine.visible = false;
    }
  }
}

// One live poll at a time: a slow or stalled answer never stacks another request on top of it, and a mode or
// agent change aborts the one in flight.
async function pollLive() {
  if (state.mode !== "live" || state.polling) return;
  state.polling = true;
  state.ctrl = new AbortController();
  const timer = setTimeout(() => state.ctrl.abort(), 5000);
  try {
    const ans = await getJSON(Core.liveURL(state.agent), state.ctrl.signal);
    applyAnswer(ans);
  } catch (err) {
    if (err && err.name === "AbortError") return;
    setStatus(Core.statusText("live", "no-serve", String(err.message || err), state.map), true);
    removeMarkers();
    state.agentUnit = null;
    overlay.update({ chat: [], events: [], notes: [String(err.message || err)] }, state.agent);
  } finally {
    clearTimeout(timer);
    state.polling = false;
    state.lastPoll = performance.now();
  }
}

async function loadReplay(name) {
  state.record = name;
  setStatus("Loading the run record…", false);
  try {
    const ans = await getJSON(Core.replayURL(name));
    const norm = Core.normalizeAnswer(ans);
    state.timeline = Core.newTimeline(norm.duration);
    state.replayFrames = norm.frames;
    $("timeline").max = String(norm.duration);
    $("timeline").value = "0";
    $("play").textContent = "Pause";
    state.map = Core.answerMap(state.map, norm);
    state.focusSet = false;
    state.sceneKey = "";
    // The first frame places the focus; its agents and the scene then load around them.
    const first = Core.interpolate(norm.frames, 0);
    const agent = first.units.find((u) => u.kind === "agent") || first.units[0];
    if (agent) {
      const p = Core.worldPos(agent.x, agent.y, agent.z);
      state.focus.set(p.x, p.y + 2.2, p.z);
      state.focusSet = true;
    }
    await loadScene(state.focus);
    applyAnswer(ans);
    const notes = norm.notes.length ? " · " + norm.notes.join(" · ") : "";
    setStatus(Core.statusText("replay", "live", "", state.map) + notes, false);
  } catch (err) {
    setStatus("Replay: " + String(err.message || err), true);
  }
}

async function loadRecords() {
  const sel = $("record");
  try {
    const ans = await getJSON(Core.recordsURL());
    const list = Array.isArray(ans.records) ? ans.records : [];
    sel.replaceChildren();
    if (!list.length) {
      const o = document.createElement("option");
      o.textContent = "No run records";
      o.value = "";
      sel.appendChild(o);
      return;
    }
    for (const r of list) {
      const o = document.createElement("option");
      o.value = r.name;
      o.textContent = Core.recordLabel(r);
      sel.appendChild(o);
    }
    if (state.record) sel.value = state.record;
  } catch (err) {
    const o = document.createElement("option");
    o.textContent = "Records: " + String(err.message || err);
    o.value = "";
    sel.replaceChildren(o);
  }
}

async function loadAgents() {
  const sel = $("agent");
  try {
    const st = await getJSON("/api/state");
    const agents = (st.agents || []).filter((a) => a.kind === "forever");
    sel.replaceChildren();
    if (!agents.length) {
      const o = document.createElement("option");
      o.textContent = "No Forever serve slots";
      o.value = "";
      sel.appendChild(o);
      return;
    }
    for (const a of agents) {
      const o = document.createElement("option");
      o.value = a.id;
      o.textContent = (a.character || a.id) + (a.online ? "" : " (offline)");
      sel.appendChild(o);
    }
    if (state.agent) sel.value = state.agent;
    else state.agent = sel.value;
  } catch (err) {
    const o = document.createElement("option");
    o.textContent = "Agents: " + String(err.message || err);
    o.value = "";
    sel.replaceChildren(o);
  }
}

// ---------------------------------------------------------------- page

const overlay = Overlay.createOverlay(document, $("overlay"));

function setStatus(text, bad) {
  const s = $("status");
  s.textContent = text || "";
  s.className = "v3d-status" + (bad ? " bad" : "");
}

function setMode(mode) {
  if (state.ctrl) state.ctrl.abort();
  state.polling = false;
  state.mode = mode === "replay" ? "replay" : "live";
  $("timelinebox").hidden = state.mode !== "replay";
  $("agentbox").hidden = state.mode !== "live";
  $("recordbox").hidden = state.mode !== "replay";
  removeMarkers();
  state.agentUnit = null;
  state.sceneKey = "";
  state.focusSet = false;
  if (state.mode === "live") {
    loadAgents().then(pollLive);
  } else {
    loadRecords().then(() => {
      const name = state.record || $("record").value;
      if (name) loadReplay(name); else setStatus("Pick a run record to replay", true);
    });
  }
}

$("mode").addEventListener("change", (e) => setMode(e.target.value));
$("agent").addEventListener("change", (e) => {
  if (state.ctrl) state.ctrl.abort();
  state.polling = false;
  state.agent = e.target.value;
  pollLive();
});
$("record").addEventListener("change", (e) => loadReplay(e.target.value));
$("play").addEventListener("click", () => {
  state.timeline = Core.playPause(state.timeline);
  $("play").textContent = state.timeline.playing ? "Pause" : "Play";
});
$("speed").addEventListener("change", (e) => { state.timeline = Core.setRate(state.timeline, e.target.value); });
$("timeline").addEventListener("input", (e) => {
  state.timeline = Core.seek(state.timeline, parseFloat(e.target.value));
  if (state.timeline.ended) $("play").textContent = "Play";
  applyAnswer(state.answer);
});
$("follow").addEventListener("change", () => { state.focusSet = false; });

const params = new URLSearchParams(location.search);
const wantLive = params.get("live"), wantReplay = params.get("replay");
if (wantReplay) {
  state.record = wantReplay;
  $("mode").value = "replay";
  setMode("replay");
} else {
  state.agent = wantLive || "";
  $("mode").value = "live";
  setMode("live");
}

resize();
const clock = new THREE.Clock();
let nextScene = 0;
function tick() {
  requestAnimationFrame(tick);
  const dt = Math.min(0.1, clock.getDelta());
  if (state.mode === "live") {
    if (Core.pollDue(performance.now(), state.lastPoll, state.polling, POLL_MS)) pollLive();
  } else if (state.timeline.playing) {
    state.timeline = Core.advance(state.timeline, dt);
    $("timeline").value = String(state.timeline.t);
    $("clock").textContent = state.timeline.t.toFixed(1) + " / " + state.timeline.duration.toFixed(1) + " s";
    if (state.timeline.ended) $("play").textContent = "Play";
    applyAnswer(state.answer);
  } else if (state.answer) {
    $("clock").textContent = state.timeline.t.toFixed(1) + " / " + state.timeline.duration.toFixed(1) + " s";
  }
  followAgent(state.agentUnit); // the follow mode holds in a replay too
  // The tiles follow the agent: a reload only when the 49-tile set around it changed.
  if (performance.now() > nextScene) {
    nextScene = performance.now() + 1000;
    if (state.focusSet) loadScene(state.focus);
  }
  placeCamera();
  renderer.render(scene, camera);
}
tick();

// Live answers come from the dashboard's poll cache; the first one also names the map and the focus.
setInterval(() => { if (state.mode === "live") loadAgents(); }, 15000);
