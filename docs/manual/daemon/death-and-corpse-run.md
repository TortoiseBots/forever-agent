---
type: Method Reference
title: Death and the corpse run
description: "Read the character's death state and drive a corpse run: release the spirit, find the corpse, run back, reclaim it, accept a resurrect, and run the live death checks."
tags: [daemon, death, corpse, methods]
status: stable
generated: { by: forever-agent docs, at: 2026-11-25T00:30:00Z }
sources:
  - id: serve-death
    resource: internal/forever/world/serve_session_methods.go
  - id: death-core
    resource: internal/forever/world/death.go
  - id: method-index
    resource: docs/manual/daemon/method-index.md
  - id: death-check
    resource: cmd/forever-agent/forever_death.go
  - id: deathrecap
    resource: cmd/forever-agent/forever_deathrecap.go
---

# What this is

Six methods cover dying and coming back on a `forever serve` daemon. Use them when you drive the character yourself: a
script that dies, a test that has to prove a corpse run works, or a tool that finds the character dead. The Forever
drivers (`dungeon-run`, `dungeon-play`, `dungeon-watch`, `route-walk`) run their own recovery and do not need you to
call these; their stop reasons are in [Stop reasons](../dungeons/stop-reasons.md).

The state comes from the server's own packets: the character's health and the ghost aura for the phase, the corpse
position from a corpse-location answer or the point where the character fell, and the reclaim delay from
`SMSG_CORPSE_RECLAIM_DELAY`.

# death_status

Takes no parameters and answers the life state. This is a read: it still works while the link is down, and it never
sends a packet.

| Field | Type | Meaning |
|---|---|---|
| `dead` | bool | Health is zero and the spirit has not been released. |
| `ghost` | bool | The spirit is released. |
| `phase` | string | `alive`, `dead` (corpse not released), `ghost`, `being resurrected`, or a numbered unknown phase. |
| `released` | bool | The spirit has been released. |
| `resurrect` | bool | A resurrection offer is waiting for an answer. |
| `reclaim_in_ms` | number | Milliseconds until the corpse may be reclaimed; omitted when there is no limit. |
| `corpse_map`, `corpse_x`, `corpse_y`, `corpse_z` | number | The corpse's map and position, when the client knows it. |

```json
{"dead": true, "ghost": false, "phase": "dead", "released": false,
 "resurrect": false, "reclaim_in_ms": 28400,
 "corpse_map": 389, "corpse_x": -1287.2, "corpse_y": -2379.1, "corpse_z": 91.6}
```

# The calls

| Method | Params | Returns | Refuses with |
|---|---|---|---|
| `release_spirit` | `{}` | `{}` | nothing when the spirit is already released (it is a no-op); `world: the character is not dead` when alive; `world: the character is not in the world yet` before login. |
| `corpse_query` | `{}` | `{found, map, x, y, z}` | `world: the corpse's location is not known` when no answer arrives in 3 s or the answer names no corpse; `world: the corpse is on a transport (not supported)`; the character not being in the world. |
| `reclaim_corpse` | `{}` | `{}` | `world: the character's spirit is not released` when alive or dead without a release; `world: the corpse's location is not known`; `world: the corpse is on another map`; `world: the character is farther from its corpse than the reclaim radius` (39 yd); `world: the corpse cannot be reclaimed yet` while the announced delay runs; `world: the server refused the corpse reclaim` when the server answers nothing. |
| `accept_resurrect` | `{}` | the `death_status` object (`dead`, `ghost`, `phase`, `released`, `resurrect`, ...) | `world: the character is not dead` when alive; `world: there is no resurrection offer to answer` when the offer expired or none arrived; `world: the resurrection did not happen` when the answer did not revive the character. |
| `accept_summon` | `accept` (bool, default `true`) | `{summoner, accepted}` | `world: there is no summon offer to answer`; the character not being in the world. |
| `area_trigger` | `id` (int, required) | `{already_sent}` | `area_trigger: needs a trigger id` for `0` or less; a send error. `already_sent` is `true` when the trigger already went out from the volume the character stands in (the walk into the portal that ran ahead of this call), so nothing was sent. |

`release_spirit` waits for the server's answer and the graveyard transfer (up to 10 s). A same-map graveyard works; a
graveyard on another map is followed through the map change. `accept_resurrect` also follows the teleport to the
resurrector, so an offer from another map is accepted normally. `reclaim_corpse` waits out the announced delay itself,
checks the 39-yard radius before sending, and then waits for the resurrection.

# A corpse run, step by step

1. Ask the state:

   ```sh
   forever-agent rpc ./slot death_status
   ```

2. If the character is dead but not released, release:

   ```sh
   forever-agent rpc ./slot release_spirit
   ```

3. Find the corpse:

   ```sh
   forever-agent rpc ./slot corpse_query
   ```

4. Walk the ghost into 39 yd of the corpse. Walking is on the navmesh, or a straight walk with `walk_to` when the
   ghost is already close:

   ```sh
   forever-agent rpc ./slot walk_path '{"x": -1287.0, "y": -2379.6, "z": 91.7}'
   ```

5. Reclaim. The method waits for the reclaim delay:

   ```sh
   forever-agent rpc ./slot reclaim_corpse
   ```

   A dungeon corpse is reached the way a player reaches it: walk the ghost to the entrance and send the entrance
   portal's trigger (`area_trigger {"id": 2230}` for Ragefire Chasm, read from the plan's `entrance_trigger`). The
   server revives the character on the way in. A walk whose route crosses the portal's volume already sends the
   trigger (`walk_to` and `walk_path`, see [travel](travel.md#when-the-watch-is-armed)); the call then answers
   `{"already_sent": true}` and sends nothing.

   ```sh
   forever-agent rpc ./slot area_trigger '{"id": 2230}'
   ```

6. Or accept a resurrection instead of running back. An offer is answerable until the expiry the server sent:

   ```sh
   forever-agent rpc ./slot accept_resurrect
   ```

   A summon offer travels the same way: `accept_summon` with no body accepts it, `{"accept": false}` declines.

# What the driver does instead

The Forever drivers treat a dead member as a recovery, not a stop, while one member lives: they wait for a
resurrection, then run the ghost back, accept a summon, or use the entrance trigger, within their own budgets. A run
that cannot recover ends with a stop reason such as `dead-wait`, `wipe-unrecovered` or `recover:path`; see
[Stop reasons](../dungeons/stop-reasons.md). There is nothing to call by hand for that path.

# The live checks: forever death-check and forever deathrecap

`forever death-check` runs the whole sequence with no daemon: it logs in like `forever fight-check`, settles, and
waits for the character to die. It never kills the character itself (a server console command or a mob does that).
Then it does what a player does: release the spirit, ask the server where the corpse is, run back and reclaim it.

```bash
forever-agent forever death-check [--rest host:port] [--rpc host:port] [--ca FILE] --account EMAIL \
    --character NAME [--stay 3s] [--death-wait 5m] \
    [--accept-resurrect [--accept-resurrect-from NAME] [--resurrect-wait 1m]] \
    [--spirit-healer] [--navmesh DIR [--parent MAP=PARENT[,..]]] [--capture-dir DIR]
```

| Flag | Default | Meaning |
|---|---|---|
| `--stay` | `3s` | How long to settle before waiting for the death, 0-10m. |
| `--death-wait` | `5m` | How long to wait for the character to die, at most 1h. |
| `--accept-resurrect` | off | Wait for a resurrection offer and accept it instead of the corpse run. |
| `--accept-resurrect-from` | any | With `--accept-resurrect`: only answer an offer from this unit. |
| `--resurrect-wait` | `1m` | With `--accept-resurrect`: how long to wait for an offer before falling back to the corpse run. |
| `--spirit-healer` | off | Release, then walk to the spirit healer at the graveyard and use it instead of the corpse run. |
| `--navmesh` | none | A Forever mmaps folder: run the corpse run (and the walk to the healer) on a navmesh route instead of a straight line. |
| `--parent` | none | With `--navmesh`: `MAP=PARENT` instance pairs, comma separated. |

The summary names the phase, how the character came back, the corpse and the announced reclaim delay; each step is
printed with its time. Exit `0` when the flow completes; `5` when nothing killed the character within `--death-wait`
or a step fails after login; `6` when the link is lost.

`forever deathrecap` is the text helper over the command channel: it logs in like `forever cmd-check`, runs the GM
guard, asks `.deathrecap <name>` once and prints the parsed timeline of the core's last recorded death of that
player.

```bash
forever-agent forever deathrecap [--rest host:port] [--rpc host:port] [--ca FILE] --account EMAIL \
    --character NAME --name NAME [--stay 4s] [--quiet DURATION] [--timeout DURATION]
```

The name is normalised the way the game does it (one word, first letter upper-case) and validated, so a second
command can never ride along. `--quiet` (at most 30s, the default is 1.5s) ends a tokenless command after that long
without a reply line and `--timeout` (at most 2m, default 15s) is its hard deadline. A core without the command's
server-side support answers `the server has no .deathrecap command (a core without PR #380)`; a player the core has
no recording for answers `no recording for "<name>"`. Both exit `0`; a login failure exits `3` or `4` and a world failure `5`.

# Related

- [Method index](method-index.md): every method the daemon registers.
- [Status and world reads](status-and-world-reads.md): the `death_status` fields in the status page.
- [Movement](movement.md): `walk_path` and `walk_to` for the ghost's walk.
- [Travel](travel.md): `area_trigger` and the GM teleport.
