---
type: Reference
title: The 3D view data format
description: The four dashboard routes that feed the 3D view, the scene and frame JSON they answer, the limits each route enforces, and how another dashboard embeds the viewer.
tags: [developers, dashboard, 3d, format]
status: draft
generated: { by: forever-agent docs, at: 2026-11-25T04:30:00Z }
sources:
  - id: view3d-go
    resource: internal/dashboard/view3d.go
  - id: view3d-core
    resource: internal/dashboard/static/view3d/core.js
  - id: navmesh-polygons
    resource: internal/navmesh/planner.go
---

# The 3D view data format

The 3D view is one self-contained front-end component, `internal/dashboard/static/view3d/` (the page, `core.js` for
the pure model, `overlay.js` for the HTML overlay and a vendored three.js). It reads four JSON routes of the
dashboard. Another dashboard can embed the component by serving that folder and answering the same routes; nothing
in the component depends on the agent's internals.

Every route is `GET` (or `HEAD`) and sits behind the dashboard's normal guard: the `Host` header must be loopback or
a name given with `--allow-host`, exactly like `/api/state`. None of them needs a token, and none calls an agent on
the page's behalf.

## Coordinates

Positions are the game's: x north, y west, z up, one unit a yard. A renderer with y up maps them
`scene = (x, z, -y)`; north is `+x`, west is `-z` (so `+z` is east). A facing of 0 is north and grows to the west,
so the direction in that frame is `(cos facing, 0, -sin facing)`.

## Scene: `GET /api/view3d/scene?map=<id>&tiles=<row>:<col>,...`

The walkable surface around the focus, one navmesh grid tile at a time. `map` is a number 0-9999; map 0 is a real
map, and only a missing map is unset. `tiles` names up
to 49 grid cells of the map's 64 x 64 grid; a cell is written `<row>:<col>`, the two numbers of
`MMMM_XX_YY.mmtile`, each 0-63. The page picks the 49 cells nearest the agent, asks once, and asks again only when
that set changes.

```json
{
  "map": 389,
  "format": "forever-v16",
  "tiles": [
    {
      "x": 30, "y": 30,
      "min_z": 10, "max_z": 14.5,
      "vertices": [600, 640, 10, 640, 640, 10, 640, 680, 14.5],
      "indices": [0, 1, 2],
      "triangles": 1
    },
    { "x": 31, "y": 30, "missing": true }
  ],
  "triangles": 1,
  "truncated": false,
  "note": ""
}
```

* `vertices` is a flat `x, y, z` list; `indices` are corners of counter-clockwise triangles, a fan over each convex
  navmesh polygon. Both are empty on a missing tile.
* `min_z` and `max_z` are the tile's height band; a renderer tints the surface by height between the answer's
  lowest and highest band so depth reads.
* `missing` says the tile has no walkable polygon: no tile file for that cell, or a tile with nothing walkable in it.
* `truncated` says the answer was cut: the mesh under the cells holds more than 240,000 triangles (`View3DMaxTriangles`),
  or the navmesh reader cut the cells to its own tile cache.
* `note` explains an empty scene in one line.
* `format` is the navmesh generation in force (`forever-v16` or `mangos-v6`).

Errors are JSON `{"status":"error","message":"..."}`: 400 for a map id or tile outside the bounds or more than 49
cells, 503 when the dashboard was started without `--mmaps-dir` or the navmesh cannot be read. Only walkable ground
and water are listed; lava, slime and slopes above the climb angle are left out in stage 1. The reader never opens a
file outside the configured navmesh folder: cells are validated numbers and the navmesh package names the files.

## Frames

A frame is the same shape in both feeds:

```json
{
  "t": 12.5,
  "units": [
    {
      "id": "0xF130001C4E00AC91", "name": "Scorpid", "kind": "hostile",
      "class": "", "level": 60,
      "x": -16.0, "y": -44.0, "z": -21.0, "facing": 1.57,
      "hp": 800, "max_hp": 1000,
      "power": 0, "max_power": 0, "power_type": "",
      "target": "0x00000000000000010000000000000001",
      "cast": "spell 9613", "dead": false
    }
  ],
  "path": [[-18.0, -46.0, -21.85], [-16.0, -44.0, -21.0]]
}
```

* `id` is the unit's guid as text (a number would lose the high bits). A renderer matches a unit across frames by it.
* `kind` is `agent` (the character the camera follows), `party`, `hostile` or `neutral`.
* `hp`, `max_hp`, `power`, `max_power` are absolute numbers; a feed with no absolute health uses percentages with a
  maximum of 100.
* `target` is the id of the unit it targets, `""` when none.
* `cast` is the spell in hand as text, `""` when none; `dead` marks a corpse.
* `path` is optional: the planned path as `[x, y, z]` points, drawn on the ground when present.

A line of the overlay is `{"t": 12.5, "kind": "chat", "text": "pull now"}`. `t` is on the run's clock for a replay,
the wall clock for a live feed. Every text is untrusted (a name comes from a game server): the component sets it with
`textContent` and drops control characters.

## Live: `GET /api/view3d/live[?id=<agent>]`

One frame of a Forever serve slot, built from the dashboard's own poll of the slot's `units` (the answer the live
page already reads): the character, its party and the creatures within 100 yards, with positions, facing, health,
power, target and cast. `id` is the agent id `/api/state` lists (`host:port`, IPv6 bracketed); without it, the first
slot that has answered.

```json
{
  "mode": "live", "state": "live", "agent": "127.0.0.1:8774",
  "map": 389, "at": "2026-11-25T04:30:00Z",
  "units": [ ... ],
  "chat": [{"t": 0, "kind": "chat", "text": "pull now"}],
  "events": [{"t": 0, "kind": "death", "text": "Tank died"}]
}
```

The answer is served from a cache; a page request never makes the dashboard call the agent. The poll rate is the
poller's own (1.5 s); the page reads at most every 2 s.

`map` is the slot's map, and the field is left out when there is none: map 0 is a real map, so only an absent `map`
is unset. A map change reloads the scene without a page reload; an answer without a map leaves the scene alone.

`state` is `live` while the last answer is at most 15 s old. Otherwise the route answers HTTP 503 with
`{"mode":"live","state":"no-serve","reason":"..."}` (`state` is `waiting` when the slot has not answered yet), and
the page clears its markers.

## Replay: `GET /api/view3d/records` and `GET /api/view3d/replay/<record>`

The picker lists the run record folders under `--records-dir` (default `~/forever-agent/tests`):

```json
{"records": [{"name": "r-20261124T193012-a1b2", "run": "r-20261124T193012-a1b2", "map": 389, "start": "2026-11-24T19:30:12Z", "bytes": 184320}]}
```

A record is a folder holding one trace file of the `bt-trace` JSONL schema (the file `forever dungeon-watch --out`
writes): `<name>.jsonl` when present, else the folder's only `.jsonl` file. The list is sorted newest first and the
newest 400 folders are listed.

`GET /api/view3d/replay/<record>` reads that trace and answers its frames:

```json
{
  "mode": "replay", "map": 389, "at": "2026-11-24T19:30:12Z",
  "duration": 412.5,
  "frames": [{"t": 0, "units": [ ... ]}, {"t": 1, "units": [ ... ]}],
  "chat": [{"t": 41, "kind": "chat", "text": "pull now"}],
  "events": [{"t": 42, "kind": "pull", "text": "pulling the scorpids"}],
  "notes": ["no end record: the run ended early or the trace was cut"]
}
```

* `duration` is the trace's own length in seconds; `t` counts from its start.
* At most 3000 frames are answered; a longer record is thinned evenly and `notes` says so. The page interpolates
  positions, facing and health between neighbouring frames.
* `chat` and `events` are the trace's event lines, split by kind; a line the record does not carry is absent.
* A parsed replay is cached per trace file (name, size and modification time), at most two replays are read at
  once and equal asks share one read, so reopening a record costs no read and parallel asks cannot hold the whole
  trace a page asks for at once.
* Errors: 404 for a name that is not a record name (letters, digits, dot, underscore and dash, no leading dot and no
  `..`), for a folder outside `--records-dir` and for a folder with no trace; 413 above 48 MiB; 422 when the trace
  cannot be parsed.

The reader stays inside `--records-dir`: the name is matched against the record rule and the path is resolved and
checked to be a folder under the root, so neither `..` nor a symlink that points out is ever followed.

## Limits

| Limit | Value |
|---|---|
| Tiles per scene request | `View3DMaxTiles`, 49 |
| Triangles per scene answer | `View3DMaxTriangles`, 240000 |
| Scene answers cached | 32 (map and tile set), 32 MiB in all; one answer over 8 MiB is served but not kept |
| Scene builds at once | 2, equal requests share one build |
| Replay answers cached | 4 (trace name, size and modification time), 24 MiB in all; one answer over 8 MiB is served but not kept |
| Replay reads at once | 2 |
| Live answer considered fresh | 15 s |
| Replay frames per answer | 3000 |
| Trace file read | 48 MiB |
| Record folders listed | 400, the newest by start time |
| Map id | 0-9999 |
| Tile row and column | 0-63 |

## Embedding the component

Serve `internal/dashboard/static/view3d/` under any path. The page needs `core.js`, `overlay.js`, `view3d.js`,
`three.module.min.js` (r170, MIT, vendored unchanged) and `view3d.css`; there is no build step and no other
dependency. The dashboard's Content-Security-Policy is
`default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'`; the page uses no inline script and no
inline event handler, so it works unchanged under that policy (a policy without `'unsafe-inline'` for styles still
works: every style the page changes is a property, not a stylesheet).

The URL parameters select the feed: `?live=<agent id>` opens live on that slot, `?replay=<record>` opens the replay
of that record.
