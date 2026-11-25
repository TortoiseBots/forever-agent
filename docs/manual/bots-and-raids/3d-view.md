---
type: Guide
title: Watch the agent in 3D
description: Open the dashboard's 3D view, follow a running serve or replay a recorded run, understand the markers and the camera, and know what stage 1 does not draw yet.
tags: [bots-and-raids, dashboard, 3d, replay]
status: draft
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-25T04:30:00Z }
sources:
  - id: view3d-go
    resource: internal/dashboard/view3d.go
  - id: view3d-core
    resource: internal/dashboard/static/view3d/core.js
  - id: view3d-page
    resource: internal/dashboard/static/view3d/view3d.js
  - id: dashboard-cmd
    resource: cmd/forever-agent/dashboard_cmd.go
---

# Watch the agent in 3D

The dashboard's 3D view draws the ground the agent can walk on as a shaded surface, with the party and the hostile
creatures on it as markers, and follows the agent as it moves. It works in a dungeon and in the open world, because
it is built from the same walkable-map tiles (the server's `mmaps`) the agent already uses for pathfinding.

Open it from the dashboard's header (**3D view**), or go to `/view3d/` on the dashboard's address. The page is a
self-contained component under `internal/dashboard/static/view3d/`; another dashboard can serve the same folder and
feed it the same data (the format is in [the 3D view data format](../developers/view3d-data-format.md)).

## 1. Start the dashboard with the navmesh

The scene needs the navmesh folder of the server the runs use, the same one the plan view takes, and the folder of
run records a replay reads:

```sh
forever-agent dashboard --mmaps-dir <walk-dir>/mmaps --mmaps-format forever --records-dir <run-records>
```

`--mmaps-format` is `forever` for ForeverCore's v16 files and `turtle` for the 1.12 ancestor's v6 files; it is never
guessed from the folder. Without `--mmaps-dir` the page still opens but says
`no navmesh: start the dashboard with --mmaps-dir ...` instead of a scene. `--records-dir` defaults to
`~/forever-agent/tests`; the replay reads only folders inside it.

## 2. Live: follow a running serve

Pick **Live** and the serve slot with the **Agent** list (the list is the dashboard's Forever slots). The page reads
`/api/view3d/live`, which answers from the dashboard's own poll of the slot's `units` (the party and the creatures
around it) every 1.5 s; the page asks for it at most every 2 s, so opening the view adds no load to the agent. Every
marker carries the unit's position, facing, health and current target; the agent's current target is joined to it by a
line.

The status line says **Live** while answers arrive. When the serve stops answering, the markers go and the line says
**No serve: <reason>**; it never shows a scene pinned under a stopped run. The camera follows the agent across tile
borders without a reload, and a map change (an instance portal, a teleport) reloads the scene for the new map in the
same page.

## 3. Replay: watch a recorded run

Pick **Replay** and a record from the **Record** list. A record is one run's folder under `--records-dir` with its
trace (a `.jsonl` file of the `bt-trace` schema, the file `forever dungeon-watch --out` and a run write); a folder
with exactly one trace in it is a record, and so is a folder named after its trace. The timeline below the bar plays,
pauses, seeks and runs at 0.5x, 1x, 2x, 4x or 8x; markers interpolate between the frames, so a 1 s record still moves
smoothly.

The replay opens on `?replay=<record>`, so a link can point at one run. A record with no positions says so in the
status line instead of showing an empty scene.

## 4. What the markers mean

| Marker | What it is |
|---|---|
| Yellow capsule, wedge, name and health bar | The agent's own character (the camera follows it) |
| Blue capsules | Its party members |
| Red capsules | Hostile creatures (the creatures the serve's `units` answer holds within 100 yards) |
| Grey capsules | Anything else (a data feed may mark a unit neutral) |
| Line from the agent | The agent's current target |
| Green line on the ground | The planned path, when the frames carry one |

The HTML panels in the corners show the agent's and the target's health and power bars, a cast bar when the frames
carry a cast, the last party chat lines and the agent's last events. Every name is shown as text: a name that holds
markup is displayed exactly as it came.

## 5. Controls

* **Drag** with the left mouse button: orbit around the agent.
* **Wheel**: zoom.
* **Drag** with the right mouse button: pan the focus away from the agent; tick **Follow** off and on to go back to
  following.
* **Follow** (on by default): the camera keeps the agent in the middle as it walks, across tile borders.
* Replay only: the timeline slider, **Pause**/**Play** and the speed list.

## 6. The limits of stage 1

* The surface is the walkable mesh only: no walls, no floors of buildings, no textures. Stage 2 reads those from your
  own local game client at run time; nothing from the client is in the repository.
* No character or creature models: a capsule, a facing wedge, a name and a health bar say what a unit is and where it
  is going.
* Terrain is loaded around the focus, 49 tiles at a time (about 3.4 km a side), and dropped as the agent moves; the
  open world is far too big to load whole.
* The live view shows the units the serve's `units` answer holds (within 100 yards of the character); a unit farther
  away appears when it comes in range.
* One scene request carries at most 240,000 triangles; a very busy area is cut to that and the page says the mesh was
  cut.
