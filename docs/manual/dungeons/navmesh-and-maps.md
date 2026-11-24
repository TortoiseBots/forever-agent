---
type: Guide
title: Navmesh and maps
description: The mmap files every check and walk plans on, how the two generations differ, and the commands that draw maps, extract minimaps and read client data.
tags: [dungeons, navmesh, maps, minimaps]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T21:00:00Z }
sources:
  - id: planner
    resource: internal/navmesh/planner.go
  - id: format
    resource: internal/navmesh/format.go
  - id: path-check
    resource: cmd/forever-agent/path_check_cmd.go
  - id: minimap-cmd
    resource: cmd/forever-agent/minimap_cmd.go
  - id: minimap-meta
    resource: internal/minimap/tiles.go
  - id: casc-cat
    resource: cmd/forever-agent/casc_cmd.go
  - id: map-cmd
    resource: cmd/forever-agent/map_cmd.go
  - id: run-map
    resource: cmd/forever-agent/forever_runmap.go
---

# The mesh is the server's own

Every walk the agent plans and every offline check in this area runs on the server's own navmesh, the `mmaps`
folder its `PathFinder` reads, through the core's own Detour sources. The agent does not keep a second model of the
world: if a point is walkable in the mesh, it is walkable for the server's bots, and the plan checks and the live
walker agree because they read the same tiles.

Copy the folder the server uses (its `data/mmaps`, roughly 0.5 GB for the instance maps) and pass it as `--mmaps`
(or `mmaps_dir` in the agent's settings). The planner reads tiles on demand and keeps at most 64 loaded, so a full
folder costs nothing until a map is asked for.

# Two generations

| | 1.12 ancestor (`MaNGOS`) | ForeverCore (`Forever`) |
|---|---|---|
| Map file | `NNN.mmap`: a raw 28-byte `dtNavMeshParams` | `MMMM.mmap`: 40-byte header (magic `MMAP`, generator version 16, `dtNavMeshParams`, off-mesh count) |
| Tiles | `NNNYYXX.mmtile`, generator version 6 | `MMMM_XX_YY.mmtile`, generator version 16 |
| Tile payload | Detour version 7, 64-bit polygon refs | the same |
| Selected by | `navmesh.NewPlanner` | `navmesh.NewForeverPlanner` (the commands' `--forever`) |

The format is never guessed from the folder. A 16-format folder named as a 1.12 one fails on the map header and
the other way round, rather than being half read. The two generations also spend the polygon flag bits differently
(the 1.12 filter for a player on foot is ground and water in, lava, slime and steep out; the Forever pair differs,
where 0x02 is steep and 0x10 is road), which is why the planner applies each generation's own walk filter and why a
Forever mesh must never be queried with the 1.12 constants.

Errors you will see: `navmesh: this forever-agent was built without cgo, so it has no Detour engine` (rebuild with
`CGO_ENABLED=1` and a C++ compiler) and, when a map has no files, the check saying that the mesh cannot answer.

# Paths, floors and line of sight

- A planned path has a status: `complete`, `partial` (it ends at the reachable point nearest the goal) or
  `no_path`, with a reason.
- Its points are Detour's straight-path corners, the first the start and the last the goal; ground samples every
  4 yd between corners let a walk keep to the floor.
- For an instance map that has no tile of its own for a grid, a Forever planner reads the parent map's tile
  instead: the server's own parent-map rule, given as `--parent MAP=PARENT` (repeatable) or by the command's
  default.
- `Raycast` is a walkability ray, not true line of sight: the agent has no vmaps. The plan check uses it for
  physics-like gaps and says so.
- The server's own path query can be replayed: `ServerPath` plus `ServerMoveVerdict` answer from the same tiles the
  way the core's `PathGenerator` does. A hop the offline planner accepts and the server refuses shows up as
  `server unreachable`.

# Avoid boxes

Some ground kills although the mesh calls it plain ground: Ragefire Chasm's lava basin is under a yard deep, and
the mesh generator flags a liquid only where it is at least the agent's height deep. A plan's `avoid` boxes mark
those regions: a polygon at least a quarter of whose area lies inside a box gets the magma flag, so every query
keeps out of it, and no point of the plan may stand in one. The plan check reports `in an avoid box` and `cut by an
avoid box`; the drivers neither summon a bot onto nor raise a body in one.

# path-check

Replays one hop with both queries, which is how a live refusal is reproduced offline from the logged position:

```bash
forever-agent path-check --mmaps DIR --map N [--json] [--ray] -- X1 Y1 Z1 X2 Y2 Z2
```

It prints the offline path (status, length, reason) and the server's answer (type, verdict, actual end, how short it
came, start and end distance); `--ray` adds the walkability ray (`known`, `clear`, hit distance). Exit `0` on a
replayed hop, `1` when a query itself fails (no tiles, no engine), `2` on a usage error. Note that `path-check` reads
the 1.12 format only; for a ForeverCore mesh use `plan-check --forever` or `forever route-walk`.

# minimap-extract

Extracts the in-game minimaps of maps from the client, for the dashboard's map layer and for the route pictures of
`plan-diff`.

```bash
forever-agent minimap-extract --client <Data dir> --out <dir> [--maps 70,209|all-instances] [--mmaps DIR] \
    [--prefer minimap,worldmap,navmesh] [--list] [--verbose]
forever-agent minimap-extract --forever (--client <extract dir> | --casc <install> [--product P]) \
    --db2 <dbc dir> --out <dir> [--maps ...] [--mmaps DIR] [--wmo-block-yards YD]
forever-agent minimap-extract --forever --casc <install> --db2 <dbc dir> --maps <id> --wmo-probe
```

- `--client` is the 1.12 client's `Data` folder (its MPQ archives, read only); with `--forever` it is an outside
  CASC extractor's listfile-named folder.
- `--forever` needs `--db2`, the server's extracted `dbc/<locale>` folder holding `Map.db2`; `--casc` reads the
  install directly and replaces `--client`.
- `--maps` takes ids or `all-instances` (every dungeon and raid of `Map.dbc`). `--out` gets `<map>.png` and
  `<map>.json` per map; each layer also keeps `<map>.<kind>.png` and, for another floor, `<map>.<kind><n>f.png`
  with its `.json`.
- `--prefer` is the order of sources for the main picture: `minimap` (the terrain tiles, and the client's indoor WMO
  tiles), `worldmap` (the map picture of the area) and `navmesh` (a height-shaded render of the server's floor).
  Default: `worldmap,minimap,navmesh`, and with `--forever` `minimap,navmesh`.
- `--list` reports what each map would get and writes nothing.
- `--wmo-probe` (with `--forever --casc`) prints the map's root WMO, its groups' boxes and the WMO minimap texture
  blocks with the block size they imply; it writes nothing. Measure that size once per client and pass it as
  `--wmo-block-yards` for an indoor map.

The `.json` beside each picture says where the pixels lie in world coordinates: `map`, `name`, `source`, `width`,
`height`, `px_per_yard`, the world edges `north`, `south`, `west`, `east`, the tile count, `from` (`terrain`, `wmo`
or `terrain+wmo`) and an optional one-line `note` about a known gap in the picture.

# casc-cat

Writes one file out of a CASC install by FileDataID — the live check for the native reader of the encoding and root
tables:

```bash
forever-agent casc-cat --install <install dir> [--product P] --fdid N --out FILE
```

`--install` is the folder holding `.build.info`; `--product` picks its row when several are active; `--out` must not
exist yet. On success it prints `casc-cat: FileDataID N, B bytes`. A read or open failure exits `1` and leaves no
file behind.

# Drawing a run: map

Draws a run as a top-down map: one SVG per floor, and with `--html` a single-file replay with a time slider. The
positions come from a capture, botwatch samples or both; the navmesh can be the background; a plan can be drawn over
it.

```bash
forever-agent map [--capture FILE] [--events FILE] [--report FILE] [--botwatch FILE [--members a,b]] \
    [--plan dungeons/x.json] [--out run.svg] [--html] [--force] [--config FILE | --mmaps DIR] \
    [--map N] [--box x0,y0,x1,y1] [--from RFC3339] [--to RFC3339] [--pad YD] \
    [--floors auto|off|z1,z2] [--background=false] [--width PX]
```

| Flag | Default | Meaning |
|---|---|---|
| `--out` | `map.svg` | The SVG; a map of several floors writes `NAME-f1.svg`, `NAME-f2.svg`, … |
| `--html` | off | Also write the replay next to the SVG with `.html` for its extension. |
| `--mmaps` | from `mmaps_dir` | The mesh for the background. |
| `--map` | busiest map | The map to draw. |
| `--box` | whole run | Draw this area only, in game coordinates. |
| `--pad` | 25 yd | Yards of margin round what the run covers. |
| `--floors` | `auto` | Split floors from the mesh, `off`, or explicit heights such as `-30,-10`. |
| `--background=false` | on | Leave the floor render out (smaller, quicker). |
| `--width` | 960 | The longest side in pixels. |

`--from`/`--to` (RFC 3339) draw one stretch of a long run.

# Drawing a watch: forever run-map

Draws a finished (or cut-off) `dungeon-watch` folder — its trace, summary and optional combat log — with no login
and no server. The same writer runs after a watch with `dungeon-watch --map`.

```bash
forever-agent forever run-map DIR [--plan FILE] [--navmesh DIR [--parent MAP=PARENT[,..]]] [--out NAME.svg] \
    [--html] [--names] [--floors auto|off|z1,z2] [--width PX] [--from-s S] [--to-s S] [--force]
```

- `--plan` defaults to the summary's plan name under `dungeons/`.
- `--out` is one file name written into `DIR` (no path separator, no `..`); the default is `<run id>.svg`.
- `--names` labels the routes with the characters' names; the default labels the party slots, so a report shares
  no character name.
- `--navmesh` adds the floor background; without it the map has none and says so.
- `--from-s`/`--to-s` keep one stretch of the trace.
