---
type: Method Reference
title: Status and world reads
description: Read the character's own state and the units, game objects, ground spells, item data and map picture around it.
tags: [daemon, status, units, world, reads]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T20:35:07Z }
sources:
  - id: serve-status
    resource: internal/forever/world/serve.go
  - id: serve-session-methods
    resource: internal/forever/world/serve_session_methods.go
  - id: serve-unit-lookup
    resource: internal/forever/world/serve_session.go
  - id: serve-loot-methods
    resource: internal/forever/world/serve_loot_methods.go
  - id: serve-item-info
    resource: internal/forever/world/serve_item_info.go
  - id: serve-map
    resource: internal/forever/world/serve_map.go
  - id: objects-cc
    resource: internal/forever/objects/cc.go
  - id: objects-mover
    resource: internal/forever/objects/mover.go
---

# What this is

These methods answer what the session knows right now: where the character stands, what it is doing, which
units and game objects are in view, which ground spells are down, what an item id is, and a picture of the
run so far. They are the cheap way to decide what to do before sending an action.

Most of them are read-only: while the world link is reconnecting they are still served from the last
session (`status`, `units`, `inv`, `spells`, `dynamic_objects`, `map_snapshot`, `death_status`,
`trade_status`, `chat_log`, `follow_status`), though one can still fail with a link error mid-call. `unit`
and `target` queue on the session like any action.

Call shape and errors are on [RPC basics](rpc-basics.md). Units are also what
[spells and combat](../player-actions/spells-and-combat.md) targets; the inventory is on
[items and inventory](../player-actions/items-and-inventory.md).

# status

`status` takes no parameters and answers one object.

```json
{"jsonrpc":"2.0","id":1,"method":"status"}
```

| Field | Type | Meaning |
|---|---|---|
| `character`, `team`, `level` | string, string, int | Name, `alliance`/`horde` from the race, and level. |
| `guid` | GUID | The character's own GUID, as `0x` plus 32 hex digits. |
| `map` | uint | Map id. |
| `x`, `y`, `z`, `o` | float | Position in yards and orientation in radians. |
| `target` | GUID | The unit `target`/`attack` selected, or the server's own target field. |
| `health`, `max_health`, `power`, `max_power`, `power_type` | int | Vitals, and which power bar `power` is. |
| `in_combat`, `falling`, `z_unknown` | bool | In combat; falling or airborne; the client could not work out the height after a fall, a knockback or a teleport. |
| `fall_ms`, `fall_land_z` | int, float | While a fall is in the air: the flight left in milliseconds and the floor it lands on (omitted otherwise). |
| `teleport_seq` | uint | How many server teleports this session has seen; a change means the server moved the character. |
| `dead`, `ghost`, `on_taxi` | bool | Dead; dead as a ghost; on a taxi flight (the unit flag is set, or a flight has not landed). |
| `latency_ms`, `server_packets`, `parse_errors` | uint, int, int | Last round trip, packets received, packets the decoder could not parse. |
| `mono_ms`, `base_time`, `wall_offset_ms` | int, time, int | The steady clock at this answer, the wall instant it counts from, and how far the host clock has been stepped. Combat and event rows use the same axis. |
| `combo_points`, `combo_target` | uint, GUID | Combo points and the unit they are on (omitted while zero). |
| `gcd_ms` | int | Milliseconds left on the global cooldown. |
| `guild`, `guild_name` | GUID, string | The character's own guild (omitted while none), and the guild of the last confirmed invite. |
| `state`, `error`, `reconnect_attempt`, `reconnect_limit` | string, string, int | The link's lifecycle (`online`, `reconnecting`, `offline`), why it is not online, and the attempt number and budget. |
| `locale`, `addon` | string | Client language claimed, and `on` or `off` for the add-on transport. |

Example (abbreviated):

```json
{
  "character": "Mychar", "team": "alliance", "guid": "0x0000000000000000000000000000ABCD",
  "level": 60, "map": 0, "x": -8949.95, "y": -132.49, "z": 83.53, "o": 1.57,
  "health": 3200, "max_health": 3200, "power": 0, "max_power": 100, "power_type": 1,
  "in_combat": false, "falling": false, "z_unknown": false, "dead": false, "ghost": false,
  "on_taxi": false, "state": "online", "locale": "enUS", "addon": "on"
}
```

While reconnecting, `state` is `"reconnecting"`, `error` names the drop, and `reconnect_attempt` /
`reconnect_limit` say where the budget stands.

# units

Lists units in view, nearest first.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `within` | float | `0` (no limit) | Keep units this many yards away or closer (3-D distance). |
| `near` | float | `0` | Older name for `within`; used only when `within` is 0. |
| `entry` | int | `0` (any) | Keep one creature entry. |
| `name` | string | — | Case-insensitive substring of the unit's name. |
| `hostile` | bool | unset (both) | `true` keeps only units hostile to the character, `false` only non-hostile ones. |
| `owner` | GUID | empty | Keep only units whose `owner` is this GUID (a hunter's pet, a warlock's demon). |
| `limit` | int | `100` | Page size; a negative limit is refused. |

The `hostile` filter compares the mob's faction template with the character's own faction; the server answer
is fetched once and cached for the session, so the first filtered call is slower than later ones.

Order is distance, then GUID. A row is a `unit` row:

| Field | Meaning |
|---|---|
| `guid`, `type`, `entry`, `name` | Identity. `type` is `player`, `active player`, `unit` (a creature), `game object`, `corpse`, ... |
| `x`, `y`, `z`, `o`, `distance`, `distance_2d` | Position, orientation, and distance from the character (3-D and flat). |
| `health`, `max_health`, `power`, `max_power`, `power_type` | Vitals. |
| `level`, `class`, `target` | Level, class (players), and the unit's own target. |
| `in_combat`, `dead`, `ghost`, `moving` | State: fighting, dead, dead as a ghost, on a move spline right now. |
| `casting`, `spell_id` | The unit is casting, and what. |
| `auras`, `cc` | Every aura still up (`spell_id`, `caster`, `stacks`, `remaining_ms`; `0` for no duration), and `cc` present and `true` while one crowd-controls the unit. |
| `mount_display_id` | Non-zero while the unit is mounted. |
| `owner`, `owner_kind` | Owner and which field named it (`summoned_by`, `created_by`, `charmed_by`). |
| `pet`, `pet_number`, `created_by_spell`, `creature_family` | The unit's own pet, and a pet's own fields. |

```json
{"jsonrpc":"2.0","id":2,"method":"units","params":{"within":30,"hostile":true,"limit":2}}
```

# unit and target

Both take `guid` or `name` and answer one `unit` row. `target` also selects the unit on the server
(`CMSG_SET_SELECTION`) and records it for `attack` and `cast`. A player name is normalised as the server
does; a creature name is matched case-insensitively against units in view.

A name that is not in the model yet makes the session ask the server for the names of players it has seen;
the call itself fails with `no unit named "..."` nearby. Retry after a moment.

# gameobjects

Lists game objects in view (doors, chests, levers, mailboxes, ...), nearest first.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `within` | float | `0` | Keep objects this close. |
| `radius` | float | `0` | Older name for `within`. |
| `entry` | int | `0` | One game object entry. |
| `type` | int | unset | One game object type id. |
| `name` | string | — | Case-insensitive substring. |
| `limit` | int | `100` | Page size. |

A row: `guid`, `entry`, `name`, `type`, `type_name`, `x`, `y`, `z`, `o`, `distance`, `distance_2d`,
`state`, `flags`.

Using or opening one is on [quests and loot](../player-actions/quests-and-loot.md).

# dynamic_objects

Takes no parameters. Lists the ground spells in view (a Blizzard area, a fire patch, ...), nearest first:
`guid`, `kind`, `spell_id`, `caster`, `radius`, `x`, `y`, `z`, `distance`. `distance` is flat and is `-1` while
the object has no position.

`kind` names the object type the area arrived in: `dynamic object` (a persistent area aura, the usual ground spell)
or `area trigger` (this core's `SPELL_EFFECT_CREATE_AREATRIGGER`, which is how Blizzard and Flamestrike land). An
area trigger that carried no spell (a portal) is not a ground spell and stays out of the list.

# item_info

Answers what the server's item data says about up to 64 item entries.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `entry` | int | — | One item entry, above 0. The single-item form of `entries`; both at once is refused. |
| `entries` | int array | — (1 to 64 required) | Item entries to look up. A repeated entry is repeated in the answer. |
| `wait_ms` | int | `0` | Not supported yet: any non-zero value is refused. The lookup uses the session's own database deadline. |

Give `entry` or `entries` (1 to 64 ids).

The result is `{"items":[...]}`, one row per requested entry in request order:

| Field | Meaning |
|---|---|
| `item_entry`, `found` | The entry, and whether the server knew it. |
| `name`, `quality`, `quality_name` | Name and quality (`0` poor through `7` heirloom). |
| `class`, `subclass` | Item class and subclass; absent when the server did not answer them. |
| `inventory_type`, `item_level`, `required_level`, `stackable`, `bonding`, `container_slots` | Item data: slot type, levels, stack size, bonding rule and bag slots. |
| `sell_price`, `buy_price` | Copper prices. |

A failed fetch is an error that says how many entries the server answered, so a caller can tell whether a
retry is worth it.

# map_snapshot

Draws the run so far as an SVG: party members, hostile creatures, the combat events, and optionally the
navmesh floor.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `floor` | int | `0` | Which floor to draw (`0` for a single-floor map). |
| `plan` | string | — | A plan file name in `--plans-dir` to draw over the run: `a-z`, `0-9`, `_` and `-`, without `.json`. |
| `floors` | string | `auto` | `auto` splits stacked floors by height, `off` draws one, or heights like `-30,-10`. |
| `background` | bool | `true` | Draw the navmesh background. `false` is smaller and faster; without `--navmesh` the note says the background was left out. |
| `html` | bool | `false` | Also return a single-file HTML replay. |
| `pad` | float | map default | Margin around the drawing, in yards. |

The result: `map`, `floor`, `floors` (each floor's name), `seconds` (run length), `members`, `creatures`,
`events` (counts drawn), `background`, `notes`, `svg`, and `html` when asked.

Errors are `-32602` for a bad `plan`/`floors`/`floor` and `-32003` when the recorder has no position yet
(the character is not in the world).

# death_status

Takes no parameters and answers the character's life state.

| Field | Meaning |
|---|---|
| `dead`, `ghost` | Dead, and dead as a ghost. |
| `phase` | `alive`, `dead` (corpse not released), `ghost`, or `being resurrected`. |
| `released` | The spirit has been released. |
| `resurrect` | A resurrection offer is waiting for an answer. |
| `reclaim_in_ms` | Milliseconds until the corpse may be reclaimed (omitted when there is no limit). |
| `corpse_map`, `corpse_x`, `corpse_y`, `corpse_z` | The corpse's map and position. |

The corpse run itself (`release_spirit`, `reclaim_corpse`, `accept_resurrect`, `accept_summon`,
`corpse_query`) is listed in the [method index](method-index.md).

# combat_near

Answers who else hit mobs near a point in the last N seconds, read from the session event log. It is how a
camp's traffic is measured without asking the server anything.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `x`, `y`, `z` | float | the character's position | The point. All three or none. |
| `radius` | float | `30` | Radius in yards, `5` to `100`. The distance is flat. |
| `window_s` | int | `60` | Seconds back, `1` to `600`. |
| `who` | string | `others` | `others` keeps players outside the party; `party` keeps party members' rows. The character's own rows never count. |

Result: `count` (hit rows counted), `players` (`{guid, name, hits, first_ms, last_ms}`, sorted), `mobs`
(distinct creatures hit), `unplaced` (rows whose target the model could not place at a position).

# Examples

```sh
forever-agent forever rpc ./slot status
forever-agent forever rpc ./slot units '{"within":15,"name":"wolf"}'
forever-agent forever rpc ./slot gameobjects '{"within":10,"entry":1731}'
forever-agent forever rpc ./slot dynamic_objects
forever-agent forever rpc ./slot item_info '{"entries":[2589,2592]}'
forever-agent forever rpc --timeout 2m ./slot map_snapshot '{"plan":"uldaman","background":false}'
```

# Common problems

| Symptom | Cause and fix |
|---|---|
| `units` answers `[]` while the character stands in a camp | The character is not in the world yet, or `within` is smaller than the distance. Try without `within`. |
| `hostile` filter answers nothing | The character's own faction row was not available; the call errors instead of guessing. Retry once the character is fully in the world. |
| `no unit named "..." nearby` | The unit is not in the model. For a player seen for the first time, wait for the name query; for a creature, check `units` first. |
| `map_snapshot` error `nothing to draw yet` | The recorder has no position: the character is not in the world. |
| `map_snapshot` note `no navmesh background: ...` | The daemon was started without `--navmesh`, or the folder lacks that map's tiles. |
| `item_info: needs 1-64 entries` | Split the request into pages of at most 64 entries. |
| `item_info: give entry or entries, not both` | Use one form. `entry` is the single-item shortcut. |
| `item_info: entry N is not a positive item id` | An item entry is above 0. |

# Related

- [RPC basics](rpc-basics.md)
- [Method index](method-index.md)
- [Movement](movement.md)
- [Events and logs](events-and-logs.md)
- [forever serve](serve.md)
