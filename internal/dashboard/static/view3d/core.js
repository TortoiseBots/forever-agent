"use strict";
// The 3D view's model, kept free of three.js and of the page so a node check can run it (like view.js for the map
// view). Everything here is pure: tile selection, the scene and frame URLs, frame interpolation, the timeline,
// the marker model and the text rules. view3d.js draws what this makes.
(function (root) {
  const TILE_YARDS = 533.3333333, TILE_GRID = 64, MAX_TILES = 49;
  const SPEEDS = [0.5, 1, 2, 4, 8];
  const KINDS = {
    agent: "#ffd257",   // the agent's own character: the one the camera follows
    party: "#5fb3ff",   // its party
    hostile: "#ff5f5f", // creatures that fight the party
    neutral: "#9aa4ad", // anything else
  };
  const RAD = Math.PI / 180;

  // Game coordinates (x north, y west, z up) to the scene's (x north, y up, z south): north is +x, west is -z,
  // so the first-person habit of looking down +z is south. One unit is one yard.
  function worldPos(x, y, z) { return { x: x, y: z, z: -y }; }

  // The grid tile holding a game position, as the core names tiles (32 - x/533, 32 - y/533).
  function tileOf(x, y) { return { x: Math.trunc(32 - x / TILE_YARDS), y: Math.trunc(32 - y / TILE_YARDS) }; }

  // The rectangle of a tile in yards.
  function tileExtent(t) {
    return { x0: (31 - t.x) * TILE_YARDS, y0: (31 - t.y) * TILE_YARDS, x1: (32 - t.x) * TILE_YARDS, y1: (32 - t.y) * TILE_YARDS };
  }

  // The tile budget around a focus position: the tiles nearest the focus tile's middle, ties by row then column,
  // inside the 64 x 64 grid. Returns [{x, y}] with x the row and y the column, nearest first.
  function selectTiles(px, py, budget) {
    const n = Math.max(1, Math.min(budget == null ? MAX_TILES : budget, MAX_TILES));
    const f = tileOf(px, py);
    const cx = Math.max(0, Math.min(TILE_GRID - 1, f.x)), cy = Math.max(0, Math.min(TILE_GRID - 1, f.y));
    const out = [];
    for (let x = 0; x < TILE_GRID; x++) {
      for (let y = 0; y < TILE_GRID; y++) {
        const dx = x - cx, dy = y - cy;
        out.push({ x, y, d: dx * dx + dy * dy });
      }
    }
    out.sort((a, b) => a.d - b.d || a.x - b.x || a.y - b.y);
    return out.slice(0, n).map((t) => ({ x: t.x, y: t.y }));
  }

  // The scene request for a map and its tiles (at most MAX_TILES, as the server refuses more).
  function sceneURL(map, tiles) {
    const list = tiles.slice(0, MAX_TILES).map((t) => t.x + ":" + t.y).join(",");
    return "/api/view3d/scene?map=" + encodeURIComponent(map) + "&tiles=" + encodeURIComponent(list);
  }
  function liveURL(id) { return "/api/view3d/live" + (id ? "?id=" + encodeURIComponent(id) : ""); }
  function recordsURL() { return "/api/view3d/records"; }
  function replayURL(name) { return "/api/view3d/replay/" + encodeURIComponent(name); }

  // One marker of a frame in the scene's coordinates, coloured by kind, with its facing as a unit vector.
  function markerModel(units) {
    return (units || []).map((u) => {
      const p = worldPos(u.x, u.y, u.z);
      const a = (u.facing || 0);
      return {
        id: String(u.id), name: u.name || "?", kind: KINDS[u.kind] ? u.kind : "neutral", color: KINDS[u.kind] || KINDS.neutral,
        x: p.x, y: p.y, z: p.z, hp: u.hp || 0, max_hp: u.max_hp || 0, power: u.power || 0, max_power: u.max_power || 0,
        target: u.target || "", cast: u.cast || "", dead: !!u.dead,
        dirX: Math.cos(a), dirZ: Math.sin(a),
      };
    });
  }

  // Health as a 0..1 fraction (0 when the maximum is unknown).
  function fraction(v, max) { return max > 0 ? Math.max(0, Math.min(1, v / max)) : 0; }

  // The frame at time t, interpolating every unit's position, facing and health between its two neighbouring
  // frames. Units of one id are the same unit; a unit missing from one of the two frames keeps the other's
  // place (it has just appeared or gone), so nothing jumps across the map.
  function interpolate(frames, t) {
    if (!frames || !frames.length) return { t: 0, units: [], path: [] };
    let i = 0;
    while (i < frames.length - 1 && frames[i + 1].t <= t) i++;
    const a = frames[i], b = frames[Math.min(i + 1, frames.length - 1)];
    const span = b.t - a.t;
    const f = span > 0 ? Math.max(0, Math.min(1, (t - a.t) / span)) : 0;
    const byId = new Map();
    for (const u of a.units || []) byId.set(u.id, { a: u, b: null });
    for (const u of b.units || []) {
      const cur = byId.get(u.id);
      if (cur) cur.b = u; else byId.set(u.id, { a: null, b: u });
    }
    const units = [];
    for (const { a: ua, b: ub } of byId.values()) {
      const u = ub || ua, o = ua || ub;
      units.push({
        id: String(u.id), name: u.name || o.name, kind: u.kind || o.kind, class: u.class || o.class, level: u.level || o.level,
        x: ua && ub ? ua.x + (ub.x - ua.x) * f : u.x,
        y: ua && ub ? ua.y + (ub.y - ua.y) * f : u.y,
        z: ua && ub ? ua.z + (ub.z - ua.z) * f : u.z,
        facing: ua && ub ? ua.facing + shortAngle(ua.facing || 0, ub.facing || 0) * f : u.facing,
        hp: ua && ub ? Math.round(ua.hp + (ub.hp - ua.hp) * f) : u.hp,
        max_hp: u.max_hp, power: ua && ub ? Math.round(ua.power + (ub.power - ua.power) * f) : u.power,
        max_power: u.max_power, target: u.target, cast: u.cast, dead: !!u.dead,
      });
    }
    return { t, units, path: (frames[i].path || []) };
  }

  // The signed shortest turn from angle a to angle b, in radians.
  function shortAngle(a, b) {
    let d = (b - a) % (2 * Math.PI);
    if (d > Math.PI) d -= 2 * Math.PI;
    if (d < -Math.PI) d += 2 * Math.PI;
    return d;
  }

  // A replay timeline: position in seconds, playing or not, at one of the speeds.
  function newTimeline(duration) {
    return { t: 0, duration: Math.max(0, duration || 0), playing: true, rate: 1, ended: false };
  }
  function setRate(tl, rate) {
    const r = Number(rate);
    let best = SPEEDS[0];
    for (const s of SPEEDS) if (Math.abs(s - r) < Math.abs(best - r)) best = s;
    return Object.assign({}, tl, { rate: best });
  }
  function seek(tl, t) {
    const v = Math.max(0, Math.min(tl.duration, isFinite(t) ? t : 0));
    return Object.assign({}, tl, { t: v, ended: v >= tl.duration, playing: v >= tl.duration ? false : tl.playing });
  }
  function playPause(tl) {
    if (tl.ended) return Object.assign({}, tl, { t: 0, ended: false, playing: true });
    return Object.assign({}, tl, { playing: !tl.playing });
  }
  // Advance by dt real seconds; stops at the end.
  function advance(tl, dt) {
    if (!tl.playing) return tl;
    const t = tl.t + Math.max(0, dt) * tl.rate;
    if (t >= tl.duration) return Object.assign({}, tl, { t: tl.duration, playing: false, ended: true });
    return Object.assign({}, tl, { t });
  }

  // Whether a poll is due: never while another one is in flight, so a slow answer cannot stack a request per frame.
  function pollDue(now, last, inFlight, every) { return !inFlight && now - last >= every; }

  // One line of text as the page shows it: control characters dropped, runs of white space collapsed, at most
  // max runes. Names come from a game server and are untrusted; the page sets every one with textContent.
  function cleanText(s, max) {
    const lim = max || 96;
    let out = "", space = false, n = 0;
    for (const ch of String(s == null ? "" : s)) {
      const c = ch.codePointAt(0);
      if (c < 0x20 || (c >= 0x7f && c <= 0x9f) || c === 0xfffd) { space = out.length > 0; continue; }
      if (c === 0x20 || c === 0x09) { space = out.length > 0; continue; }
      if (space && out.length) out += " ";
      space = false;
      out += ch;
      if (++n >= lim) break;
    }
    return out.trim();
  }

  // Put a line of text into an element the only safe way: textContent, never markup.
  function setText(el, s) { if (el) el.textContent = cleanText(s); return el; }

  // What a live or replay answer says, with the missing parts filled in, so the page needs no defensive checks.
  function normalizeAnswer(a) {
    a = a || {};
    const frames = Array.isArray(a.frames) ? a.frames.map((f) => ({ t: +f.t || 0, units: Array.isArray(f.units) ? f.units : [], path: f.path || [] })) : [];
    frames.sort((x, y) => x.t - y.t);
    return {
      mode: a.mode === "replay" ? "replay" : "live", state: a.state || (a.mode === "replay" ? "live" : ""),
      reason: a.reason || "", agent: a.agent || "", map: +a.map || 0, at: a.at || "",
      duration: +a.duration || 0, t: +a.t || 0, units: Array.isArray(a.units) ? a.units : [], frames,
      chat: Array.isArray(a.chat) ? a.chat : [], events: Array.isArray(a.events) ? a.events : [], notes: Array.isArray(a.notes) ? a.notes : [],
    };
  }

  // The parsed scene: vertices and indices are arrays and each tile says where it is.
  function normalizeScene(s) {
    s = s || {};
    const tiles = (Array.isArray(s.tiles) ? s.tiles : []).map((t) => {
      const vertices = Array.isArray(t.vertices) ? t.vertices : [];
      let minZ = Infinity, maxZ = -Infinity;
      for (let i = 2; i < vertices.length; i += 3) { minZ = Math.min(minZ, vertices[i]); maxZ = Math.max(maxZ, vertices[i]); }
      return {
        x: +t.x || 0, y: +t.y || 0, missing: !!t.missing, vertices,
        indices: Array.isArray(t.indices) ? t.indices : [],
        min_z: isFinite(minZ) ? minZ : (+t.min_z || 0), max_z: isFinite(maxZ) ? maxZ : (+t.max_z || 0),
        triangles: +t.triangles || 0,
      };
    });
    let lo = Infinity, hi = -Infinity;
    for (const t of tiles) if (!t.missing && t.vertices.length) { lo = Math.min(lo, t.min_z); hi = Math.max(hi, t.max_z); }
    return { map: +s.map || 0, format: s.format || "", tiles, triangles: +s.triangles || 0, truncated: !!s.truncated,
      note: s.note || "", low: isFinite(lo) ? lo : 0, high: isFinite(hi) ? hi : 0 };
  }

  // The nine commands of the page's status line, so the page and its check agree on the words.
  function statusText(mode, state, reason, map) {
    if (mode === "replay") return "Replay of a recorded run" + (map ? " (map " + map + ")" : "");
    switch (state) {
      case "live": return "Live" + (map ? " (map " + map + ")" : "");
      case "no-serve": return "No serve: " + (reason || "the serve is not answering");
      case "waiting": return "Waiting: " + (reason || "the serve has not answered yet");
      default: return reason || "Live";
    }
  }

  const api = { TILE_YARDS, TILE_GRID, MAX_TILES, SPEEDS, KINDS, worldPos, tileOf, tileExtent, selectTiles, sceneURL, liveURL,
    recordsURL, replayURL, markerModel, fraction, interpolate, shortAngle, newTimeline, setRate, seek, playPause, advance,
    cleanText, setText, normalizeAnswer, normalizeScene, statusText, pollDue };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.View3DCore = api;
})(typeof self !== "undefined" ? self : this);
