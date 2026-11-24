"use strict";
// The run controls' page part (README "Controls (--control)"): the buttons of one agent's card, the confirm texts and the control log, and
// the DOM that sends one control. The choice of buttons is a pure function of the card (tested under node); the page puts every string in with
// textContent. The box is shown only when GET /api/control/status says the controls are on.
(function (root) {
  // ---- the buttons of a card (D14 / README "Controls"): the matrix, with no DOM ----
  function runButtons(card) {
    if (!card || !card.online || card.synthetic) return [];
    if (card.kind === "forever") {
      const running = !!(card.run && card.run.state === "running");
      return [
        { verb: "pause", action: "run", label: "Pause run", title: "Pause the module's run: the bots stop where they are and wait.", confirm: true, enabled: running },
        { verb: "resume", action: "run", label: "Resume run", title: "Let the module's paused run go on.", confirm: false, enabled: !running },
        { verb: "stop", action: "run", label: "Stop run", title: "Ask the module to stop its run.", confirm: true, enabled: true },
      ];
    }
    const r = card.run || {};
    const running = r.state === "running";
    const mode = r.mode || "";
    if (!running) {
      return [{ verb: "stop", label: "Unbind kept party", title: "Unbinds the party this agent kept after a run (the hires go back and the instance is lost).", confirm: true, enabled: true }];
    }
    if (mode === "observe") {
      const paused = !!(r.leader && r.leader.state === "paused");
      return [
        { verb: "pause", label: "Pause", title: "Pause the run leader: the bots stop where they are and wait.", confirm: true, enabled: !paused },
        { verb: "resume", label: "Resume", title: "Let the run leader go on.", confirm: false, enabled: paused },
        { verb: "skip", label: "Skip step", title: "Ask the run leader to skip the step it is on.", confirm: false, enabled: true },
        { verb: "stop", label: "Stop run", title: "Ends the run and unbinds: the hires go back to your raid and the instance is lost.", confirm: true, enabled: true },
      ];
    }
    if (mode === "remote" || mode === "commander") {
      return [
        { verb: "pause", label: "Stop, keep party", title: "Ends this agent's commander run; the party and the instance are kept, and the run can be resumed later.", confirm: true, enabled: true },
        { verb: "stop", label: "Stop and unbind", title: "Ends the run and unbinds: the hires go back to your raid and the instance is lost.", confirm: true, enabled: true },
      ];
    }
    return [{ verb: "stop", label: "Stop run", title: "Ends the run and unbinds: the hires go back to your raid and the instance is lost.", confirm: true, enabled: true }];
  }
  // The order buttons of a Forever card (the serve spec's six): stay, follow,
  // come, attack, pull and release. They go to POST /api/control/<id>/order
  // through the same token/nonce gate as the run buttons.
  const ORDER_VERBS = ["stay", "follow", "come", "attack", "pull", "release"];
  const ORDER_TITLES = {
    stay: "Tell the bot to stand still where it is.",
    follow: "Tell the bot to follow again.",
    come: "Call the bot to you.",
    attack: "Tell the bot to attack your target.",
    pull: "Tell the bot to pull your target.",
    release: "Tell the bot to let go of what it holds.",
  };
  function orderButtons(card) {
    if (!card || !card.online || card.synthetic || card.kind !== "forever") return [];
    return ORDER_VERBS.map((v) => ({ verb: v, action: "order", label: v.charAt(0).toUpperCase() + v.slice(1), title: ORDER_TITLES[v], confirm: false, enabled: true }));
  }

  // ---- the Positions panel (a Forever card's placement) ----

  const PLACE_SHAPES = ["close", "circle", "arc", "spread", "line", "column", "spear", "arrow"];
  const PLACE_GAPS = ["tight", "normal", "wide"];
  const PLACE_ANCHORS = [["here", "Here"], ["target", "Target"], ["tank", "Tank"], ["point", "Map point"]];
  const PLACE_WHO = ["all", "tanks", "healers", "melee", "ranged", "dps"];

  // The inverse of the dashboard's Project ((-y, -x) in geometry.go): after
  // the svg's own getScreenCTM().inverse, a click at (sx, sy) in the
  // drawing's plane is game (x, y) = (-sy, -sx).
  function unproject(sx, sy) { return { x: -sy, y: -sx }; }

  // placeButtons is the static shape of the panel; the page keeps the rest
  // (the shape/gap/anchor/face/who picks and the map point) in its own DOM.
  function placePanel(card) {
    if (!card || !card.online || card.synthetic || card.kind !== "forever") return null;
    return { shapes: PLACE_SHAPES, gaps: PLACE_GAPS, anchors: PLACE_ANCHORS, who: PLACE_WHO };
  }

  // ---- the live map (the serve's own picture of the party and the hostiles) ----

  // liveMapPanel is the Positions panel's picture: the Forever serve's map_snapshot drawing (the party members and
  // the hostile creatures on the Forever mesh, drawn by the daemon), which the dashboard asks for at its own bounded
  // rate (internal/dashboard/live_map.go). The page adds no rate of its own: the picture is fetched again only when
  // the card's version (live_map.at) changes. Pure of the DOM, so it is tested under node. A turtle card and the plan
  // view keep the recorded frames: they have no live map.
  function plural(n, word) { const c = n || 0; return c + " " + word + (c === 1 ? "" : "s"); }
  function liveMapPanel(card) {
    if (!card || card.synthetic || card.kind !== "forever") return null;
    const lm = card.live_map || null;
    if (!lm || !lm.have) {
      const why = (lm && lm.reason) ? String(lm.reason) : (card.online ? "the serve has not drawn one yet" : "the serve is not answering");
      return { src: null, version: "", alt: "", caption: "", notes: [], line: "No live map: " + why };
    }
    const version = String(lm.at || "");
    const bits = ["map " + lm.map, plural(lm.members, "member"), plural(lm.creatures, "hostile")];
    if (lm.floors && lm.floors.length > 1) bits.push("floor " + ((lm.floor || 0) + 1) + " of " + lm.floors.length);
    if (lm.seconds) bits.push(Math.round(lm.seconds) + " s of the run");
    if (typeof lm.age_ms === "number" && lm.age_ms > 0) bits.push("drawn " + Math.max(1, Math.round(lm.age_ms / 1000)) + " s ago");
    return {
      src: "/api/map/" + encodeURIComponent(card.id) + "?at=" + encodeURIComponent(version),
      version: version,
      alt: "Live map drawn by the serve: map " + lm.map + ", " + plural(lm.members, "party member") + " and " + plural(lm.creatures, "hostile creature"),
      caption: bits.join(" \u00b7 "),
      notes: (lm.notes || []).map(String),
      line: "",
    };
  }

  // The world's coordinate bound (Trinity::IsValidMapCoord): a point outside
  // it is not on the map. The Go handler checks the same bound, the card's
  // map id and the module's 100 yd reach; this is the page's first refusal.
  const MAP_BOUND = 17066;

  // pointBody checks one picked map point and returns the body's point and the
  // map it was picked on (the request binds the two, so a click before a zone
  // change never sends the old map's coordinates).
  function pointBody(pick) {
    const p = pick && pick.point;
    if (!p || typeof p.x !== "number" || typeof p.y !== "number" || !isFinite(p.x) || !isFinite(p.y)) {
      return { error: "the point needs finite {x, y}" };
    }
    if (Math.abs(p.x) > MAP_BOUND || Math.abs(p.y) > MAP_BOUND) return { error: "the point is not on the map" };
    if (pick.map == null) return { error: "the point needs the map it was picked on" };
    return { point: { x: p.x, y: p.y }, map: pick.map };
  }

  // placeArgs builds the POST /place body from the panel's picks, mirroring
  // the Go placeRequestArgs for the same table (the handler checks it again).
  // Returns {body} or {error}.
  function placeArgs(pick) {
    const verb = pick && pick.verb;
    if (verb === "place") {
      if (PLACE_SHAPES.indexOf(pick.shape) < 0) return { error: "shape must be one of " + PLACE_SHAPES.join(", ") };
      const body = { verb: "place", shape: pick.shape };
      if (pick.gap) {
        if (PLACE_GAPS.indexOf(String(pick.gap).toLowerCase()) >= 0) body.gap = String(pick.gap).toLowerCase();
        else {
          const n = Number(pick.gap);
          if (!isFinite(n) || n < 1.5 || n > 12) return { error: "gap must be tight, normal, wide or 1.5-12" };
          body.gap = n.toFixed(1);
        }
      }
      const anchor = pick.anchor || "here";
      const ai = PLACE_ANCHORS.map((a) => a[0]).indexOf(anchor);
      if (ai < 0) return { error: "anchor must be one of here, target, tank, point" };
      body.anchor = anchor;
      if (anchor === "point") {
        const pt = pointBody(pick);
        if (pt.error) return pt;
        body.point = pt.point;
        body.map = pt.map;
      }
      if (pick.face != null) {
        if (!Number.isInteger(pick.face) || pick.face < 0 || pick.face > 359) return { error: "face must be 0-359" };
        body.face = pick.face;
      }
      if (pick.who && pick.who.length) body.who = pick.who.slice();
      return { body };
    }
    if (verb === "place_follow") {
      const body = { verb: "place_follow" };
      if (pick.who && pick.who.length) body.who = pick.who.slice();
      return { body };
    }
    if (verb === "rally") {
      const r = pick.rally || "here";
      if (r === "here" || r === "clear" || r === "show") return { body: { verb: "rally", rally: r } };
      if (r === "point") {
        const pt = pointBody(pick);
        if (pt.error) return pt;
        return { body: { verb: "rally", rally: "point", point: pt.point, map: pt.map } };
      }
      return { error: "rally must be one of here, point, clear, show" };
    }
    if (verb === "fallback") {
      const t = pick.to || "default";
      if (t === "default" || t === "rally" || t === "here") return { body: { verb: "fallback", to: t } };
      if (t === "point") {
        const pt = pointBody(pick);
        if (pt.error) return pt;
        return { body: { verb: "fallback", to: "point", point: pt.point, map: pt.map } };
      }
      return { error: "to must be one of default, rally, here, point" };
    }
    if (verb === "resume") return { body: { verb: "resume" } };
    return { error: "verb must be one of place, place_follow, rally, fallback, resume" };
  }
  // The text of the confirm dialog, naming the character and the plan (a control that ends a run is never silent).
  function confirmText(card, verb) {
    const who = card && card.character ? card.character + " (agent " + card.id + ")" : "agent " + (card && card.id || "?");
    const plan = card && card.label && card.label.plan ? card.label.plan : "no plan";
    if (verb === "pause") {
      if (card && card.kind === "forever") {
        return "Pauses the run of " + who + " (" + plan + "): the bots stop where they are and wait. Resume when you are ready.";
      }
      if (card && card.run && card.run.mode === "observe") {
        return "Pauses the run of " + who + " (" + plan + "): the bots stop where they are and wait. The party and the instance are kept.";
      }
      return "Ends the run of " + who + " (" + plan + "). The party and the instance are kept; go on later with dungeon_run resume.";
    }
    if (card && card.kind === "forever") {
      return "Asks the module to stop the run of " + who + " (" + plan + ").";
    }
    const end = "the hires go back to the commander's raid and the instance is lost.";
    if (card && card.run && card.run.state === "running") return "Ends the run of " + who + " (" + plan + "): " + end;
    return "Unbinds the kept party of " + who + " (" + plan + "): " + end;
  }

  // The result lines of GET /api/control/<id>/log, said in words.
  function logLines(json) {
    const lines = json && Array.isArray(json.lines) ? json.lines : [];
    return lines.map((l) => {
      const t = l && l.at ? String(l.at).slice(11, 19) : "";
      const what = (l && l.verb ? l.verb : "") + (l && l.outcome === "indeterminate" ? " indeterminate" : l && l.ok ? " ok" : l && l.ok === false ? " failed" : "");
      return [t, l && l.who, l && l.via === "trusted" ? "trusted" : "", what, l && l.message ? l.message : ""].filter(Boolean).join(" \u00b7 ");
    });
  }

  const VERBS = ["pause", "resume", "skip", "stop"];

  // ---- the DOM part ----
  function armPointPick(s, card, svg) {
    if (!card || card.map == null) { s.msg.textContent = "The card has no map: no point is sent."; s.msg.hidden = false; return; }
    disarmPointPick(s);
    if (!svg) { s.msg.textContent = "The map is not drawn yet: pick the point once the map shows."; s.msg.hidden = false; return; }
    const onClick = (e) => {
      if (e.defaultPrevented) return;
      let picked = null;
      try {
        const pt = svg.createSVGPoint();
        pt.x = e.clientX; pt.y = e.clientY;
        const m = svg.getScreenCTM();
        if (!m) return;
        const loc = pt.matrixTransform(m.inverse());
        const g = unproject(loc.x, loc.y);
        if (!isFinite(g.x) || !isFinite(g.y)) return;
        picked = { x: g.x, y: g.y };
      } catch (err) { /* a svg without a CTM cannot pick */ }
      if (picked) {
        s.place.point = picked;
        s.place.pointMap = card.map;
        s.place.pointLbl.textContent = "point " + picked.x.toFixed(2) + ", " + picked.y.toFixed(2);
      }
      disarmPointPick(s);
    };
    const key = (e) => { if (e.key === "Escape") disarmPointPick(s); };
    s.pickPoint = { onClick, key, svg, cursor: svg.style.cursor };
    svg.addEventListener("click", onClick, { capture: true });
    svg.style.cursor = "crosshair";
    if (typeof document !== "undefined" && document.addEventListener) document.addEventListener("keydown", key);
    s.msg.textContent = "Click the map to pick the point (Esc cancels).";
    s.msg.hidden = false;
  }
  function disarmPointPick(s) {
    if (!s.pickPoint) return;
    const { onClick, key, svg, cursor } = s.pickPoint;
    if (svg && svg.removeEventListener) {
      svg.removeEventListener("click", onClick, { capture: true });
      svg.style.cursor = cursor || "";
    }
    if (typeof document !== "undefined" && document.removeEventListener) document.removeEventListener("keydown", key);
    s.pickPoint = null;
  }

  function create(env) {
    const h = env.h;
    const st = { asked: false, enabled: null, trusted: false, nonce: "" };
    const states = new Map();
    const sess = {
      get() { try { return window.sessionStorage.getItem("control.token") || ""; } catch (e) { return ""; } },
      set(v) { try { window.sessionStorage.setItem("control.token", v); } catch (e) { /* ignore */ } },
    };

    async function jget(url) {
      try {
        const r = await fetch(url, { cache: "no-store" });
        let json = null;
        try { json = await r.json(); } catch (e) { /* not JSON */ }
        return { ok: r.ok, status: r.status, json };
      } catch (e) { return { ok: false, status: 0, json: null }; }
    }

    function ask() {
      if (st.asked) return;
      st.asked = true;
      jget("/api/control/status").then((r) => {
        st.enabled = !!(r.ok && r.json && r.json.enabled === true);
        if (st.enabled) {
          st.trusted = r.json.local_trusted === true;
          st.nonce = st.trusted && typeof r.json.trust_nonce === "string" ? r.json.trust_nonce : "";
        }
        if (env.footer) env.footer(st.enabled);
        env.rerender();
      });
    }

    function state(card) {
      let s = states.get(card.id);
      if (s) return s;
      const el = h("div", { class: "ctls" });
      s = { el, title: h("h3", { text: "Controls" }), row: h("div", { class: "ctlrow" }), orderRow: h("div", { class: "ctlrow ctlorders", hidden: true }),
        placeBox: h("div", { class: "placebox", hidden: true }),
        token: h("input", { type: "password", autocomplete: "off", placeholder: "control token", "aria-label": "control token" }),
        msg: h("div", { class: "ctlmsg", hidden: true }), log: h("details", { class: "ctllog" }), btns: new Map(), orderBtns: new Map(), busy: new Set(), logAt: 0, pickPoint: null };
      s.token.value = sess.get();
      s.token.addEventListener("change", () => sess.set(s.token.value.trim()));
      const logPre = h("pre", { class: "ctllogpre" });
      s.log.append(h("summary", { text: "Control log (last 5)" }), logPre);
      s.logPre = logPre;
      s.el.append(s.title, s.row, s.orderRow, s.placeBox, h("label", { class: "ctltoken" }, h("span", { text: "Token " }), s.token), s.msg, s.log);
      states.set(card.id, s);
      return s;
    }

    function paintRow(row, btns, specs, s) {
      const have = new Set(specs.map((x) => x.verb));
      for (const [verb, b] of btns) if (!have.has(verb)) { b.remove(); btns.delete(verb); }
      for (const spec of specs) {
        let b = btns.get(spec.verb);
        if (!b) {
          b = h("button", { type: "button", class: "mb ctl" });
          b.addEventListener("click", () => { const cur = s.specs.get(b.dataset.action + ":" + b.dataset.verb); if (cur) send(s, s.card, cur); });
          btns.set(spec.verb, b);
        }
        b.textContent = spec.label;
        b.title = spec.title;
        b.dataset.verb = spec.verb;
        b.dataset.action = spec.action || "run";
        b.dataset.confirm = spec.confirm ? "1" : "";
        b.disabled = !spec.enabled || s.busy.has((spec.action || "run") + ":" + spec.verb);
      }
      // the order of the buttons follows the matrix
      for (const spec of specs) row.append(btns.get(spec.verb));
    }

    function paint(s, card) {
      const specs = runButtons(card);
      const orders = orderButtons(card);
      s.card = card; // the buttons keep the card and its specs current: a click never sends last poll's state
      s.specs = new Map(specs.concat(orders).map((x) => [(x.action || "run") + ":" + x.verb, x]));
      paintRow(s.row, s.btns, specs, s);
      paintRow(s.orderRow, s.orderBtns, orders, s);
      s.orderRow.hidden = orders.length === 0;
      paintPlace(s, card);
    }

    // paintLiveMap shows the serve's picture, or one line saying why there is none. The img is fetched again only
    // when the card's version changes (the server's own cadence): the page never adds a rate of its own.
    function paintLiveMap(s, lm) {
      const L = s.liveMap;
      L.box.hidden = !lm;
      if (!lm) return;
      if (lm.src) {
        if (L.version !== lm.version) { L.version = lm.version; L.img.src = lm.src; }
        L.img.alt = lm.alt;
        L.img.hidden = false;
        L.cap.textContent = lm.caption;
        L.cap.hidden = false;
      } else {
        // No picture: the img is only hidden (its last drawing is never shown as if it were live), and the next
        // picture sets the src again because the version was cleared.
        L.version = null;
        L.img.hidden = true;
        L.cap.hidden = true;
      }
      L.line.textContent = lm.line || "";
      L.line.hidden = !lm.line;
      L.notes.replaceChildren(...(lm.notes || []).map((t) => h("div", { text: t })));
      L.notes.hidden = !(lm.notes && lm.notes.length);
    }

    // paintPlace draws (once per control box) and refreshes the Positions
    // panel: the serve's live map, then Shape, Gap, Anchor, Facing, Who and
    // the seven buttons. Map point mode arms the next click on the card's map
    // svg to pick the point (crosshair cursor, Esc cancels); the page sends
    // only when the card's map is known. The live map is there whenever the
    // card is a Forever slot, a gone serve included: its line says why.
    function paintPlace(s, card) {
      const panel = placePanel(card), lmv = liveMapPanel(card);
      s.placeBox.hidden = !panel && !lmv;
      if (!panel && !lmv) return;
      if (!s.liveMap) {
        s.placeHead = h("h4", { text: "Positions" });
        s.liveMap = { box: h("div", { class: "livemap" }), img: h("img", { class: "lmimg", alt: "" }),
          cap: h("div", { class: "lmcap muted", hidden: true }), line: h("div", { class: "lmlin muted", hidden: true }),
          notes: h("div", { class: "lmnotes muted", hidden: true }), version: null };
        s.liveMap.img.hidden = true;
        s.liveMap.box.append(s.liveMap.img, s.liveMap.cap, s.liveMap.line, s.liveMap.notes);
        s.placeBox.append(s.placeHead, s.liveMap.box);
      }
      paintLiveMap(s, lmv);
      if (!panel) return;
      if (!s.place) {
        const box = s.placeBox;
        const shape = h("select", { class: "pshape", "aria-label": "shape" });
        shape.append(...panel.shapes.map((v) => h("option", { value: v, text: v })));
        shape.value = "circle";
        const gap = h("select", { class: "pgap", "aria-label": "gap" });
        gap.append(...panel.gaps.concat(["5"]).map((v) => h("option", { value: v, text: v })));
        gap.value = "normal";
        const gapNum = h("input", { class: "pgapnum", type: "number", min: "1.5", max: "12", step: "0.5", placeholder: "1.5-12", "aria-label": "gap yards" });
        const anchor = h("select", { class: "panchor", "aria-label": "anchor" });
        anchor.append(...panel.anchors.map((a) => h("option", { value: a[0], text: a[1] })));
        const face = h("select", { class: "pface", "aria-label": "facing" });
        face.append(h("option", { value: "auto", text: "Auto" }), h("option", { value: "0", text: "0 deg" }), h("option", { value: "90", text: "90 deg" }), h("option", { value: "180", text: "180 deg" }), h("option", { value: "270", text: "270 deg" }));
        const faceNum = h("input", { class: "pfacenum", type: "number", min: "0", max: "359", step: "1", placeholder: "0-359", "aria-label": "facing degrees" });
        const who = h("select", { class: "pwho", multiple: "", size: 6, "aria-label": "who" });
        const to = h("select", { class: "pto", "aria-label": "fall back to" });
        to.append(h("option", { value: "default", text: "Default" }), h("option", { value: "rally", text: "Rally" }), h("option", { value: "here", text: "Here" }), h("option", { value: "point", text: "Map point" }));
        const pointBtn = h("button", { type: "button", class: "mb ctl", text: "Pick point", title: "The next click on the card's map picks the point." });
        const pointLbl = h("span", { class: "muted", text: "no point" });
        const mk = (verb, label, title) => {
          const b = h("button", { type: "button", class: "mb ctl", text: label, title });
          b.addEventListener("click", () => sendPlace(s, s.card, verb));
          return b;
        };
        pointBtn.addEventListener("click", () => armPointPick(s, s.card, env.svgFor ? env.svgFor(s.card.id) : null));
        s.place = { shape, gap, gapNum, anchor, face, faceNum, who, to, pointBtn, pointLbl, point: null, pointMap: null,
          place: mk("place", "Place", "Hold the party in the shape at the anchor."),
          release: mk("place_follow", "Release", "Follow again (release the hold)."),
          rallyHere: mk("rallyHere", "Rally here", "Remember this spot as the fall-back point."),
          rallyPoint: mk("rallyPoint", "Rally at point", "Remember the map point as the fall-back point."),
          rallyClear: mk("rallyClear", "Clear rally", "Forget the fall-back point."),
          fallback: mk("fallback", "Fall back", "Form up on the fall-back point."),
          resume: mk("resume", "Resume", "Follow again (end the fall back)."),
          elsewhere: h("div", { class: "muted", hidden: true }) };
        box.append(
          h("label", { text: "Shape " }, shape),
          h("label", { text: "Gap " }, gap, gapNum),
          h("label", { text: "Anchor " }, anchor),
          h("label", { text: "Facing " }, face, faceNum),
          h("label", { text: "Who " }, who),
          h("label", { text: "Fall back " }, to),
          h("div", { class: "ctlrow" }, pointBtn, pointLbl),
          h("div", { class: "ctlrow" }, s.place.place, s.place.release, s.place.rallyHere, s.place.rallyPoint, s.place.rallyClear, s.place.fallback, s.place.resume),
          s.place.elsewhere);
      }
      const names = (card.bots || []).map((b) => b.name).filter(Boolean);
      const want = panel.who.concat(names);
      const sel = s.place.who;
      if (sel.options.length !== want.length || want.some((w, i) => sel.options[i].value !== w.toLowerCase() && sel.options[i].text !== w)) {
        sel.replaceChildren(...want.map((w) => h("option", { value: w.toLowerCase(), text: w })));
      }
      // A point picked on another map is stale: the map changed under it, so it is never sent there.
      if (s.place.point && s.place.pointMap !== card.map) { s.place.point = null; s.place.pointMap = null; }
      s.place.pointLbl.textContent = s.place.point ? "point " + s.place.point.x.toFixed(2) + ", " + s.place.point.y.toFixed(2) : "no point";
      // What the card lists as elsewhere (another map): said in words, never drawn on the map.
      const away = Array.isArray(card.elsewhere) ? card.elsewhere : [];
      s.place.elsewhere.textContent = away.length ? "Elsewhere (not drawn here): " + away.join(", ") : "";
      s.place.elsewhere.hidden = away.length === 0;
      // A placement request in flight holds every send button: a second click sends nothing until the answer (or an error) repaints.
      const sending = [...s.busy].some((k) => k.indexOf("place:") === 0);
      for (const b of [s.place.place, s.place.release, s.place.rallyHere, s.place.rallyPoint, s.place.rallyClear, s.place.fallback, s.place.resume]) b.disabled = sending;
      s.placeBox.hidden = false;
    }

    async function sendPlace(s, card, verb) {
      const p = s.place;
      const gapText = p.gap.value === "5" && p.gapNum.value ? p.gapNum.value : p.gap.value;
      // A typed number wins whatever the select says; Auto with no number sends no facing.
      const faceText = p.faceNum.value !== "" ? parseInt(p.faceNum.value, 10) : (p.face.value === "auto" ? null : parseInt(p.face.value, 10));
      const who = Array.from(p.who.selectedOptions).map((o) => o.text);
      let pick;
      if (verb === "place") pick = { verb, shape: p.shape.value, gap: gapText, anchor: p.anchor.value, face: faceText, who, point: p.point, map: p.pointMap };
      else if (verb === "place_follow") pick = { verb, who };
      else if (verb === "rallyHere") pick = { verb: "rally", rally: "here" };
      else if (verb === "rallyPoint") pick = { verb: "rally", rally: "point", point: p.point, map: p.pointMap };
      else if (verb === "rallyClear") pick = { verb: "rally", rally: "clear" };
      else if (verb === "fallback") pick = { verb: "fallback", to: p.to.value, point: p.point, map: p.pointMap };
      else pick = { verb: "resume" };
      const built = placeArgs(pick);
      if (built.error) { s.msg.textContent = built.error; s.msg.hidden = false; return; }
      const token = st.trusted ? "" : s.token.value.trim();
      if (!st.trusted && !token) { s.msg.textContent = "Type the control token first (kept for this browser tab only)."; s.msg.hidden = false; return; }
      const key = "place:" + (built.body.verb || verb);
      s.busy.add(key);
      paint(s, card);
      s.msg.textContent = "Sending " + verb + "...";
      s.msg.hidden = false;
      let r;
      try {
        r = await fetch("/api/control/" + encodeURIComponent(card.id) + "/place", { method: "POST", cache: "no-store",
          headers: token ? { "Content-Type": "application/json", Authorization: "Bearer " + token } : { "Content-Type": "application/json", "X-Control-Nonce": st.nonce },
          body: JSON.stringify(built.body) });
      } catch (e) { s.busy.delete(key); s.msg.textContent = "Could not reach the dashboard: " + e; paint(s, card); return; }
      let json = null;
      try { json = await r.json(); } catch (e) { /* not JSON */ }
      s.busy.delete(key);
      if (r.status === 401) { sess.set(""); s.token.value = ""; s.msg.textContent = "Wrong or missing control token" + (st.trusted ? " (the server did not accept this browser without one)." : "."); }
      else if (json && json.status === "ok") s.msg.textContent = (json.message || verb) + " sent to agent " + card.id + ".";
      else s.msg.textContent = "Refused: " + ((json && json.message) || ("HTTP " + r.status));
      paint(s, card);
      refreshLog(s, card);
    }

    function refreshLog(s, card) {
      jget("/api/control/" + encodeURIComponent(card.id) + "/log?n=5").then((r) => {
        const lines = r.ok ? logLines(r.json) : ["(the control log is not reachable)"];
        s.logPre.textContent = lines.length ? lines.join("\n") : "(no controls yet)";
      });
    }

    async function send(s, card, spec) {
      if (spec.confirm && !window.confirm(confirmText(card, spec.verb))) return;
      const token = st.trusted ? "" : s.token.value.trim();
      if (!st.trusted && !token) { s.msg.textContent = "Type the control token first (kept for this browser tab only)."; s.msg.hidden = false; return; }
      const action = spec.action || "run";
      const key = action + ":" + spec.verb;
      const body = action === "order" ? { verb: spec.verb } : { verb: spec.verb, confirm: !!spec.confirm };
      s.busy.add(key);
      paint(s, card);
      s.msg.textContent = "Sending " + spec.label + "...";
      s.msg.hidden = false;
      let r;
      try {
        r = await fetch("/api/control/" + encodeURIComponent(card.id) + "/" + action, { method: "POST", cache: "no-store",
          headers: token ? { "Content-Type": "application/json", Authorization: "Bearer " + token } : { "Content-Type": "application/json", "X-Control-Nonce": st.nonce },
          body: JSON.stringify(body) });
      } catch (e) { s.busy.delete(key); s.msg.textContent = "Could not reach the dashboard: " + e; paint(s, card); return; }
      let json = null;
      try { json = await r.json(); } catch (e) { /* not JSON */ }
      s.busy.delete(key);
      if (r.status === 401) { sess.set(""); s.token.value = ""; s.msg.textContent = "Wrong or missing control token" + (st.trusted ? " (the server did not accept this browser without one)." : "."); }
      // The command ran; only its log line failed. Said once, with no retry offered: a retry would send a second command.
      else if (json && json.executed === true && json.log_failed === true) s.msg.textContent = spec.verb + " done (control log write failed).";
      else if (json && json.status === "ok") s.msg.textContent = spec.verb + " sent to agent " + card.id + " (" + json.rpc + ").";
      else if (json && typeof json.message === "string" && json.message.indexOf("unknown:") === 0) s.msg.textContent = json.message;
      else s.msg.textContent = "Refused: " + ((json && json.message) || ("HTTP " + r.status));
      paint(s, card);
      refreshLog(s, card);
    }

    return {
      render(el, card) {
        ask();
        if (st.enabled !== true || !card || card.synthetic) { el.hidden = true; return; }
        el.hidden = false;
        const s = state(card);
        el.replaceChildren(s.el);
        s.token.hidden = st.trusted;
        paint(s, card);
        const now = Date.now();
        if (!s.logAt || now - s.logAt > 10000) { s.logAt = now; refreshLog(s, card); }
      },
      enabled() { return st.enabled; },
    };
  }

  const api = { runButtons, orderButtons, ORDER_VERBS, confirmText, logLines, create, VERBS,
    PLACE_SHAPES, PLACE_GAPS, PLACE_ANCHORS, PLACE_WHO, unproject, placePanel, placeArgs,
    armPointPick, disarmPointPick, MAP_BOUND, liveMapPanel };
  if (typeof module !== "undefined" && module.exports) module.exports = api; else root.CTL = api;
})(typeof self !== "undefined" ? self : this);
