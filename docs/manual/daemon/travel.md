---
type: Guide
title: Teleports and travel
description: Move the character with the GM teleport, area triggers, mounts and server-flown taxi flights, and check the map handling live.
tags: [daemon, teleport, mount, taxi, area-trigger]
status: stable
generated: { by: forever-agent docs, at: 2026-11-25T04:40:00Z }
sources:
  - id: serve-gm-gaps
    resource: internal/forever/world/serve_gm_gaps.go
  - id: serve-mount
    resource: internal/forever/world/mount.go
  - id: serve-taxi
    resource: internal/forever/world/taxi_serve.go
  - id: serve-mapchange
    resource: internal/forever/world/mapchange.go
  - id: map-check
    resource: cmd/forever-agent/forever_map.go
  - id: serve-trigger
    resource: internal/forever/world/trigger.go
  - id: serve-trigger-plan
    resource: internal/forever/world/trigger_plan.go
  - id: serve-fall
    resource: internal/forever/world/fall.go
---

# What this is

These methods ask the server to move the character instead of walking it:

- `teleport` is the core's `.go xyz` line, plus the fall when the point is above the ground.
- `area_trigger` enters an area trigger as if the character walked into it (an instance portal, a scripted
  trigger).
- `mount` and `dismount` mount and unmount the character.
- `taxi` takes a flight path, with the server flying the route.

Walking and navmesh routes are on [Movement](movement.md).

A teleport, its fall and a `wait_landing` flight hold the session goroutine until they finish, so they block
other calls while they run. Give them a `--timeout` that covers the wait (see [RPC basics](rpc-basics.md)).

# teleport

The core's `.go xyz` line, through the GM command channel.

| Parameter | Type | Meaning |
|---|---|---|
| `x`, `y`, `z` | float | Required; the destination. Bounded to ±1,000,000. |
| `map` | int | Optional map id (0 to `2147483647`). Without it the line keeps the current map. |
| `require_ground` | bool | Make an unknown ground an error. The answer still says the character was moved. |

The call waits for the server to move the character. A point above ground the navmesh knows starts a fall, and the
answer comes when the server's own move is confirmed, at the fall's start (`ground: "falling"`, the flight still
left in `fall_ms`): the fall goes on flying on every later wait, so a `status` call during it shows the character in
the air, and the next `walk_to`/`walk_path` waits for the landing before it walks.

| Field | Meaning |
|---|---|
| `sent` | The command line sent. |
| `map`, `x`, `y`, `z` | Where the character stands afterwards. |
| `fell`, `drop_yd` | A fall was flown, and how far. |
| `falling`, `fall_ms`, `fall_land_z` | The fall is in the air, the flight this long still to run, and the floor it lands on. |
| `teleport_seq` | The session's teleport counter. |
| `z_unknown` | The height is not trustworthy. |
| `ground` | `standing` (ground within a yard), `falling` (a fall is in the air), `landed` (a fall was flown), or `unknown`. |
| `ground_why` | Why the ground is unknown: no navmesh, no floor in reach, a failed query, water, or a far teleport (which starts no fall). |

Teleporting moves only this session's own character, so it needs no `--allow-gm-units`; the session must be
allowed to run GM commands on the server, and its GM guard is confirmed on first use.

# area_trigger

Sends one area trigger id (`CMSG_AREA_TRIGGER`, as if the character walked into it). Used to take an
instance portal or a scripted trigger.

| Parameter | Type | Meaning |
|---|---|---|
| `id` | int | Trigger id, above 0. |

The answer is `{"already_sent": bool}`. `already_sent` is `true` when nothing went out: the same trigger already
did, from a volume the character still stands in, less than 10 s ago — the watch's own crossing of a walked portal,
or an earlier call. One crossing is one packet, so the explicit call that follows a walk into the portal (a follow
loop zoning after its lead, a ghost run taking its corpse's entrance) does not repeat it.

The hold clears as soon as the character leaves the volume, on the map change itself (a character that zones in and
straight back out stands at the portal volume again, and that is a new crossing), when a volume of that id is armed
again at another point, or after 10 s, so a transfer that really failed is retried by the next attempt. A trigger the
watch never armed has no volume to hold, and every explicit call sends it.

There is no server answer to wait for: check `status.map` afterwards. A map change appears in `event_log` as
a `session` row whose `data.state` is `map_change`.

# area_trigger_watch

Arms a trigger volume the character's own walking enters, so a portal fires on the step in rather than through an
`area_trigger` call. The client keeps no table of triggers: it sends `CMSG_AREA_TRIGGER` itself the moment its own
movement takes the character into an armed volume.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `id` | int | — (required unless `clear`) | Trigger id, above 0. |
| `map` | int | the current map | Map the volume belongs to. |
| `x`, `y`, `z` | float | the character's own position | The volume's centre. All three or none: one or two are refused. |
| `radius` | float | `4` | The volume's radius in yards, over 0 and at most 100. Standing inside a classic entrance sphere (7 yd across) is what the default buys; a walking leg (a 500 ms heartbeat at 7 yd/s) usually samples it. |
| `clear` | bool | `false` | Remove every armed volume instead of adding one; `id` is not needed then. |

A volume that was not inside the character and is inside now sends the packet once; leaving re-arms it, so each
crossing sends exactly once. A volume the character already stands in sends at once, and a walk that only cuts
through a sphere, with both sampled ends outside, is sampled at the path's closest approach and entered from there.
Arming the same id again moves its volume and keeps its entered state, so a retried call is not a second crossing.
One session carries at most 32 volumes.

The result is `{"triggers":[{id, map, x, y, z, radius}, ...]}`: the armed volumes after the call, in arming order
(empty after `clear`). What the trigger did shows on `status.map` and in `event_log`, as for `area_trigger`.

# When the watch is armed

Three things arm it:

- **This call.** `area_trigger_watch` arms a volume until `clear` (or the session ends). Use it for a trigger the
  daemon's plans do not name, or to aim one at a point of your own.
- **A walk, by itself.** The daemon's own walk methods, `walk_to` and `walk_path`, arm the instance-portal volumes
  of the current map that the walk's route crosses — its destination or the line or polyline it follows — and remove
  them again when the walk ends. That is why a plain `walk_to` into an instance portal fires its trigger with no call
  first: the walk arms the portal's volume, sends `CMSG_AREA_TRIGGER` on the step in, and the map changes. Automatic
  arming is the daemon's walk methods' alone: a leg of `dungeon_run` (the module walks the party) arms nothing.
- **Nothing else.** Standing still, a fight, a follow loop or a teleport arms no volume: there is no movement of the
  character's own to cross one. A plan's `area_trigger` step is not armed by a walk either: its own step fires it
  once, after its own walk to the step's `stand`, and a remote run's `by` guard stays the only way a `bot` or `auto`
  zone fires.

The instance-portal volumes come from the daemon's plan folder (`forever serve --plans-dir`, the same files
`dungeon_run` reads):

| Volume | Where it is | Radius |
|---|---|---|
| A plan's `entrance_trigger` | The plan's `portal` point, on the portal's map (the outer map) | The default 4 yd |

A walk that begins inside a volume did not step into it and sends nothing for it. An id the caller already armed by
hand keeps the caller's volume and is not removed by the walk: one state per id is what keeps a walked volume from
firing twice, and re-arming an id moves it. A walk that finds the 32-volume bound full arms no more and says so in
the daemon log. A daemon whose plan folder is missing or holds no plan for the current map arms nothing on a walk —
the explicit call above is then the way.

The walk's own packet is held for 10 s: an [`area_trigger`](#area_trigger) call for the same id while the character
still stands in that volume answers `already_sent: true` and sends nothing, so a follow loop or a ghost run that
walks in and then sends the trigger does not repeat the walk's packet. Leaving the volume, a map change, arming a
volume of that id again, or the 10 s passing ends the hold, and the next call sends.

# mount and dismount

| Parameter | Type | Meaning |
|---|---|---|
| `spell` | int | A mount spell the character knows. |
| `entry` | int | A mount item entry; with `spell` the item is used and the named spell cast. |

Exactly one of `spell` and `entry` for a spell mount; both for an item mount. The spell must be in the
character's spellbook, because a second cast of a spell that worked would toggle or double-cast it.

Result: `mounted`, `display_id`, `spell`, `entry`, `attempts` (casts spent; `0` when already mounted),
`learned_spell` (for an item mount). Already mounted is answered as it is. A cast the server refuses is an
error naming `SMSG_MOUNT_RESULT`; a cast that produced no display within four seconds is an error too.

`dismount` takes no parameters: it sends `CMSG_CANCEL_MOUNT_AURA` and waits up to two seconds for the mount
display to clear. Already on foot is answered as it is.

Mounted state also shows on `status` and on a unit row as `mount_display_id`. Neither method works during a
taxi flight: the display in the air belongs to the flight.

# taxi

Opens a flight master's window, or takes one flight.

| Parameter | Type | Meaning |
|---|---|---|
| `guid` | GUID | Required: the flight master. It must have the flight-master flag. |
| `node` | uint | Destination node. Without it the call only opens the window. |
| `nodes` | uint array | The older shape: source first, destination last, at most 64. The first must be the master's own node, the last the destination. |
| `wait_landing` | bool | Hold the call until the flight lands (needs a destination). |
| `wait_ms` | int | `0` uses 30,000; allowed up to 60,000. |

The result: `since` (event cursor), `ok`, `status` (`window`, `flying`, `landed`, `refused`, `no_takeoff`),
`unit`, `current_node`, `known` and `usable` nodes, `node`, `reply`/`error`, `on_taxi`, and after a landing
`x`/`y`/`z` and `detail`; `event` and `lines` carry the client's own frame.

A flight is the server's: without `wait_landing` the call returns at takeoff, and the character stays on
the taxi (the server's fifteen-minute ceiling) while `status.on_taxi` is true and `taxi_flight` events mark
takeoff and landing. No movement goes out until the landing is seen. `no_takeoff` means the request went
out and the server neither refused it nor started a flight within the wait; a late flight may still start.

# forever map-check

A one-shot live check of the client's map handling, no daemon involved: it logs in, settles, prints the map the client
is on, optionally sends one area trigger, then stays in the world and follows every map change the server makes.
Each change is printed with the from and to map, the reason, the difficulty, the client's own position and the
objects the new map has five seconds later.

```bash
forever-agent forever map-check [--rest host:port] [--rpc host:port] [--ca FILE] --account EMAIL \
    --character NAME [--settle 3s] [--stay 60s] [--area-trigger ID]
```

| Flag | Default | Meaning |
|---|---|---|
| `--settle` | `3s` | How long to settle in the world before the map is printed and the trigger sent, 0-1m. |
| `--stay` | `60s` | How long to stay and follow map changes, 0-30m. |
| `--area-trigger` | `0` | Send `CMSG_AREA_TRIGGER` for this id once, after the settle; `0` sends none. |

The summary is three lines and then one line per change:

```text
map-check: Mychar ended on map 389 (difficulty 0) at 12.00 -3.00 -44.00, 148 object(s)
map-check: 1 transfer pending(s), 1 followed, 0 aborted, 1 area trigger(s) sent, 0 same-map teleport(s)
  1: map 0 -> 389 (reason 4), difficulty 0, position 12.00 -3.00 -44.00, 148 object(s) after the wait
```

A refused transfer prints its reason and argument and says the client stayed on the old map. Exit `0` when the run
completes; `5` when the world server refuses the session or the run fails after login.

# Examples

```sh
# into a dungeon through its trigger, then read the map
forever-agent forever rpc ./slot area_trigger '{"id":1234}'
forever-agent forever rpc ./slot status

# a teleport with a known height, on the same map
forever-agent forever rpc --timeout 30s ./slot teleport '{"x":-8870,"y":-120,"z":83}'

# a teleport to another map and wait out the fall
forever-agent forever rpc --timeout 2m ./slot teleport '{"x":-8870,"y":-120,"z":300,"map":33,"require_ground":true}'

# mount, then dismount
forever-agent forever rpc ./slot mount '{"spell":12345}'
forever-agent forever rpc ./slot dismount

# open the flight master's window, then fly to a node
forever-agent forever rpc ./slot taxi '{"guid":"0x0000000000000000000000000012ABCD"}'
forever-agent forever rpc --timeout 3m ./slot taxi '{"guid":"0x0000000000000000000000000012ABCD","node":8,"wait_landing":true}'
```

# Common problems

| Symptom | Cause and fix |
|---|---|
| `teleport: ... is not a coordinate` | A coordinate is NaN, infinite or beyond ±1,000,000. |
| `teleport` answers `ground: "unknown"` | No navmesh, or no floor in reach, or water under the point. The height is the one stated, not a checked one. |
| `mount: spell N is not one this character knows` | Use a spell from `spells`, not an arbitrary id. |
| `mount: spell N was cast but the server refused the mount` | The spell is not a usable mount for this character. |
| `taxi: entry N is not a flight master` | The GUID is not a flight master; check `units` or `gameobjects`. |
| `taxi: wait_landing needs a destination node` | Pass `node` (or `nodes`) with `wait_landing`. |
| `taxi` status `no_takeoff` | The request went out but no flight started within the wait; check `status.on_taxi` and the `taxi_flight` events. |
| `mount`/`dismount` refused during a flight | Wait for `status.on_taxi` to clear; the display in the air belongs to the flight. |

# Related

- [Movement](movement.md): walks, navmesh routes and follow
- [RPC basics](rpc-basics.md), [Method index](method-index.md)
- [Status and world reads](status-and-world-reads.md): `status.on_taxi`, `teleport_seq`, `z_unknown`
- [Events and logs](events-and-logs.md): `taxi_flight`, `session` teleport and fall rows
- [Trainers and flight paths](../player-actions/trainers-and-flight-paths.md)
