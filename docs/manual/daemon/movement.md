---
type: Guide
title: Movement
description: Move the character with straight walks, navmesh routes and follow loops, ask the navmesh what ground is walkable, and check a walk live.
tags: [daemon, movement, navmesh, follow, pathing]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-25T04:40:00Z }
sources:
  - id: serve-movement
    resource: internal/forever/world/serve_session.go
  - id: serve-nav-route
    resource: internal/navmesh/route.go
  - id: serve-nav-planner
    resource: internal/navmesh/planner.go
  - id: serve-follow
    resource: internal/forever/world/follow_session.go
  - id: follow-loop
    resource: internal/forever/follow/follow.go
  - id: mover
    resource: internal/forever/objects/mover.go
  - id: walk-check
    resource: cmd/forever-agent/forever_walk.go
---

# What this is

These methods walk the character or plan how to walk it. Two kinds exist:

- **Straight movement** with no map data: `walk_to` runs in a straight line at the character's run speed.
- **Navmesh movement**, which needs `--navmesh` on the daemon: `path_to` plans a route, `walk_path` plans and
  follows it, `walk_ray` answers whether a straight line stays on walkable ground, and `follow_start` keeps
  the character near another player.

Teleports, area triggers, mounts and taxi flights ask the server to move the character instead of walking
it: see [Teleports and travel](travel.md).

A walk or a follow step runs on the session goroutine, so it holds other calls until it finishes. Give a long
call a `--timeout` that covers it (see [RPC basics](rpc-basics.md)).

# Navmesh setup

`--navmesh DIR` points at a copy of the server's `data/mmaps` files: one `MMMM.mmap` per map and
`MMMM_XX_YY.mmtile` tiles, in the format the Forever core's generator writes. Tiles are read on demand, and
a map keeps at most 64 tiles loaded, so a large map answers a long route slower than a dungeon.

`--parent MAP=PARENT` (repeatable) tells the planner that an instance map without its own tiles falls back to
a parent map, as the server does.

Without `--navmesh`, `path_to`, `walk_path`, `walk_ray` and `follow_start` answer
`world: no navmesh: serve was started without --navmesh`.

# walk_to

| Parameter | Type | Meaning |
|---|---|---|
| `x`, `y`, `z` | float | Destination in yards. A missing coordinate is read as `0`. |

One straight run: the character faces the point, starts forward and stops within 0.5 yards of it. It does not
avoid walls, cliffs or water — use `walk_path` for that. The call answers `{}` when the character arrives.

A walk that ends in a map change — the route steps into an instance portal, whose area trigger the walk fires
itself (see [area triggers](travel.md#area_trigger_watch)) — is a success, not an error. The answer then carries
where the character landed:

| Field | Meaning |
|---|---|
| `changed_map.map` | The map the walk ended on. |
| `changed_map.x`, `y`, `z` | The position the server put the character at on that map. |

The only between-maps error is a walk the server leaves between maps until the call's own deadline (or the
session's 60-second map-change limit): it ends with `objects: the character is between maps: the character is
still between maps at the walk's deadline`. A walk started while a map change is already under way never moves
and is refused with `objects: the character is between maps: the walk did not start`. A walk whose route crosses
a trigger volume the plans name arms that volume for the walk's own duration and removes it when the walk ends
([travel](travel.md#area_trigger_watch) says which volumes).

Refusals: no mover (the character's create block never arrived), the character is airborne or falling, or a
map change is in progress. No navmesh is needed.

```json
{"jsonrpc":"2.0","id":1,"method":"walk_to","params":{"x":-8949.0,"y":-118.0,"z":83.5}}
```

# path_to

Plans a route on the navmesh and moves nothing.

| Parameter | Type | Meaning |
|---|---|---|
| `x`, `y`, `z` | float | Destination. |

The result:

| Field | Meaning |
|---|---|
| `map` | Map the route was planned on. |
| `from`, `to` | Asked start and goal. |
| `status` | `complete`, `partial` or `none`. |
| `reason` | Why a route is partial or missing. |
| `points` | The walk polyline: every corner plus ground samples on long slopes. |
| `corners` | Number of Detour corners. |
| `length` | Polyline length in yards. |

`partial` means the mesh reached a point near the goal but not the goal; `none` means nothing was planned.
Both are answers, not errors. An error (no navmesh folder, no map, a broken tile, a build without the Detour
engine) means a retry will not help.

# walk_path

Plans like `path_to`, then walks the polyline: each leg is a straight run at run speed, and the call answers
the route once the character stands at its end. It refuses a route that is not `complete`:

```
walk_path: no full route to 12.3 45.6 7.8 (partial)
```

| Parameter | Type | Meaning |
|---|---|---|
| `x`, `y`, `z` | float | Destination. |

Result: the same fields as `path_to`. Refusals also cover a missing mover and a link that dropped mid-walk. A
map change ends the walk with `objects: the character is between maps` and no route; the trigger volumes the
route crosses are armed for the walk like `walk_to`'s and removed when it ends.

# walk_ray

Answers whether the straight line from the character (or `from`) to a point stays on ground the navmesh
holds. This is **not** line of sight: the agent has no collision geometry, so a cliff, a water edge or a
target off the mesh reads as blocked, and a ceiling or a bridge is not seen.

| Parameter | Type | Meaning |
|---|---|---|
| `x`, `y`, `z` | float | The point to look at. |
| `from` | float trio | Optional start; default is the character's own position. |

| Field | Meaning |
|---|---|
| `map`, `from`, `to` | The answer's frame. |
| `known` | The mesh can answer. `false` when either end is off the mesh or a tile is missing. |
| `in` | The line stays on walkable ground (meaningless when `known` is `false`). |
| `hit` | Yards from `from` to where the line leaves the mesh (omitted when `in`). |
| `source` | Always `navmesh_raycast`. |

# face

Turns the character toward a unit: `guid` or `name`. Refused while airborne. Answers `{}`.

# follow_start, follow_stop, follow_status

A follow loop keeps the character behind one other player. It ticks on the navmesh and stays inside a
distance band, so a follower that is close enough does not twitch.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `lead` | string | — (required) | The character to follow. |
| `near` | float | `3.0` | Inside this distance the loop stops walking. Allowed `0.5` to `50`. |
| `far` | float | `8.0` | Beyond this distance it walks to the point behind the leader. Must be above `near`, at most `100`. |
| `lost_s` | int | `30` | Seconds unseen before the leader counts as lost. Allowed `1` to `3600`. |
| `tick_ms` | int | `250` | Loop tick. Allowed `50` to `5000`. |

Refusals: the lead is the character itself; the daemon has no `--navmesh`; a loop is already running (call
`follow_stop` first); a band or tick out of range.

The result of all three methods is the loop's state:

| Field | Meaning |
|---|---|
| `running` | A loop is running now. |
| `state` | `idle` (nothing started), `running`, `stopped`, `lost` or `failed`. |
| `lead`, `near`, `far`, `lost_s`, `tick_ms` | The rule in force. |
| `lead_seen`, `lead_yd` | The lead is in the model, and how far away it is. |
| `map` | The character's map. |
| `walks`, `stops` | How many walks and early stops the loop did. |
| `stopped_reason`, `detail` | Why the loop ended. |
| `started`, `seconds` | When it started, and how long it ran. |

The loop ends when the leader changes map, stays unseen for `lost_s` (`lost`), or the client cannot walk or
stop for long enough (`failed`). `follow_stop` cancels it before queueing, so a walk in flight ends instead
of blocking the stop; the call waits up to 5 seconds for the loop's result and answers it. A walk or stop
refused once does not end the loop: it retries, one attempt per gap, then toward the leader's own position
instead of the point behind it.

Two characters following each other settle inside `far` instead of chasing: a documented limit.

# forever walk-check

A one-shot live check of the client's object model and movement, with no daemon: it logs in, settles, prints what it
understood of the objects around the character and the nearest units, walks `--dx`/`--dy` yards from the spawn,
stands `--pause`, walks back, prints the position the client itself believes, and logs out. With `--navmesh` it walks
a navmesh route to an absolute point on the current map instead of the Dx/Dy walk.

```bash
forever-agent forever walk-check [--rest host:port] [--rpc host:port] [--ca FILE] --account EMAIL \
    --character NAME [--stay 8s] [--dx 8] [--dy 0] [--pause 3s] \
    [--navmesh DIR --to-x X --to-y Y --to-z Z [--parent MAP=PARENT[,..]]]
```

| Flag | Default | Meaning |
|---|---|---|
| `--stay` | `8s` | How long to settle in the world before the summary and the walk, 0-10m. |
| `--dx`, `--dy` | `8`, `0` | Yards to walk from the spawn, each -200 to 200. |
| `--pause` | `3s` | How long to stand at the far point before walking back. |
| `--navmesh` | none | A Forever mmaps folder: walk a navmesh route to the absolute `--to-x/--to-y/--to-z` target instead of the Dx/Dy walk. |
| `--parent` | none | With `--navmesh`: `MAP=PARENT` instance pairs, comma separated. |

The last lines are the check:

```text
walk-check: Mychar on map 0: 148 objects (140 created, 8 updated, 0 destroyed, 0 out of range) in 9 updates
walk-check: from 12.00 -3.00 -44.00 to 20.00 -3.00 -44.00 and back to 12.00 -3.00 -44.00
walk-check: client position 12.00 -3.00 -44.00 (orientation 0.75), 3 monster moves, 2 movement relays, 0 teleports, 0 map changes, 0 names asked
```

Exit `0` when the run completes; `5` when the world server refuses the session or the run fails after login.

# Examples

```sh
# plan, then walk a route
forever-agent forever rpc ./slot path_to '{"x":-8870,"y":-120,"z":83}'
forever-agent forever rpc --timeout 5m ./slot walk_path '{"x":-8870,"y":-120,"z":83}'

# a straight walk with no map data
forever-agent forever rpc --timeout 1m ./slot walk_to '{"x":-8940,"y":-125,"z":83.5}'

# follow a player for a while, then inspect and stop
forever-agent forever rpc ./slot follow_start '{"lead":"Leadname","near":2,"far":6}'
forever-agent forever rpc ./slot follow_status
forever-agent forever rpc --timeout 15s ./slot follow_stop

# is the straight line to a point on walkable ground?
forever-agent forever rpc ./slot walk_ray '{"x":-8860,"y":-118,"z":83}'
```

# Common problems

| Symptom | Cause and fix |
|---|---|
| `world: no navmesh: serve was started without --navmesh` | Start the daemon with `--navmesh DIR`. |
| `path_to` answers `none` or `partial` | The goal is off the mesh, behind a drop, or its tiles are missing. Check `reason`, and whether the map needs `--parent`. |
| `walk_path: no full route to ...` | The route is not complete. Walk to a closer point with `path_to` first, or use `walk_to` for short straight hops. |
| A walk returns an airborne/fall error | The character is still falling or was knocked into the air. Wait for `status.falling` to clear. |
| `walk_ray` answers `known: false` | Either end is off the mesh, or the map's tiles are missing. |
| `follow_start: no --navmesh: following walks on the navmesh, so the daemon must be started with one` | Same as above; follow cannot work without map data. |
| `follow_start: ... is this character: a character cannot follow itself` | Use another character as `lead`. |
| `follow_start: a follow is already running (follow_stop first)` | Stop the current loop first. |

# Related

- [Teleports and travel](travel.md): teleport, area triggers, mounts and taxi flights
- [RPC basics](rpc-basics.md), [Method index](method-index.md)
- [Status and world reads](status-and-world-reads.md): `status.falling`, `z_unknown`, `teleport_seq`
- [Events and logs](events-and-logs.md)
- [Navmesh and maps](../dungeons/navmesh-and-maps.md)
- [Route tools](../dungeons/route-tools.md)
