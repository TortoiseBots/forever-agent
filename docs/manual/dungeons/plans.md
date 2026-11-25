---
type: Guide
title: Dungeon plans
description: What a dungeon plan file is, every field it can carry, and what its status and forever_status mean.
tags: [dungeons, plans, reference, format]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:40:00Z }
sources:
  - id: plan-go
    resource: internal/dungeon/plan.go
  - id: plan-keys
    resource: internal/dungeon/plan_keys.go
  - id: plans-md
    resource: docs/PLANS.md
  - id: dungeongen
    resource: tools/dungeongen/main.go
  - id: play-core
    resource: internal/forever/driver/play.go
---

# What a plan is

A dungeon plan is one JSON file, `dungeons/<name>.json`, that tells the agent where to walk inside an instance,
which groups to pull in which order, which doors, chests, NPCs and items to handle, and which boss to kill in each
leg. The same file drives every Forever run command: `forever dungeon-run` (the agent leads five bots), `forever
dungeon-play` (the agent plays one character next to four bots), `forever dungeon-watch` (a server module runs the
dungeon and the agent watches), and `forever route-walk` (a walk of the route with no fighting at all).

The plan file is the single source of truth. Exported run-leader rows are generated from it with
[plan-export](plan-tools.md); never edit the rows by hand.

The repository ships a plan for every dungeon and raid the server has under `dungeons/`. `docs/PLANS.md` is the generated table of
all of them (map, level band, legs, status, mmaps, what the instance needs besides killing); it is written by
`go run ./tools/dungeongen -spec tools/dungeongen/plans.json -table docs/PLANS.md` from `tools/dungeongen/plans.json`
and the plan files, so do not edit it by hand.

# File name and loader

The loader is strict. An unknown field, a field with a wrong type, a missing `version`, `name` or `map`, or a leg
that would only walk to its `to` point is refused with the field's own path, for example
`legs[2].steps[0].use_object.entry: required`. All problems are listed in one answer so the file can be fixed in one
pass.

- The file name must be a plan name: lower-case letters, digits, `_` and `-`. Commands take the name, not the path,
  and read `<dir>/<name>.json` with `--dir` defaulting to `dungeons`.
- `version` must be `1`.
- A plan of `kind` `raid` carries a `raid` block; a dungeon plan must not.

# Top-level fields

| Field | Default | Meaning |
|---|---|---|
| `version` | required | Format version; `1`. |
| `name` | required | The plan's name, the same as the file name. |
| `kind` | `dungeon` | `dungeon` (up to five players) or `raid`. |
| `status` | absent | `draft`, `mapped` or `live-checked`, then free text (see below). |
| `forever_status` | absent | Where the plan stands on a Forever server: `n/a`, `todo`, `exported` or `live-checked`, then free text. |
| `team` | either | `alliance` or `horde`, when some step only works for one team. The other team's run is refused before the first order. |
| `map` | required | The instance map id. |
| `entrance` | required | `{"x":..,"y":..,"z":..}` inside the instance. |
| `entrance_trigger` | 0 | Area trigger id of the entrance portal; lets a ghost whose corpse lies inside be taken in and revived. |
| `entrance_tele` | empty | `game_tele` row that puts a character at the entrance inside the instance (letters, digits, `_`). |
| `outside_tele` | empty | `game_tele` row for the entrance outside, on the outer map. |
| `portal` | absent | `{"map":N,"x":..,"y":..,"z":..}`: the entrance's area trigger centre on the outer map, where a run without GM rights walks to. |
| `level_band` | absent | `[low, high]`, `1 <= low <= high <= 60`; the hired party takes the middle. |
| `tank_min_hp` | absent | Least max health the hired tank should have; below it is a preflight warning. `100`-`20000`. |
| `hire_composition` | built in | The hired party, one `class` or `class:role` per slot. |
| `fleers` | none | Creature entries that run at low health and must be killed before they get away. |
| `cc_priority` | none | Entries the pack crowd control takes first, best first (healers and casters). |
| `cc_always` | none | Entries that are crowd-controlled in any pack they are in. |
| `no_cc` | none | Entries crowd control never takes (undead and the like). |
| `kill_first` | none | Entries the party kills before the rest of a fight, best first. |
| `summoned_kill_first` | none | The `kill_first` entries that are summoned units; each must also be in `kill_first`. |
| `raid` | absent | Raid plans only: `{"size":10\|20\|40,"tanks":..,"healers":..,"dps":..}`, the three roles adding up to `size`. |
| `notes` | none | Free text lines for people. |
| `pull_ceiling` | lint default | Plan-wide pull size ceiling in bodies, `1`-`80`; the pull-size estimate (the `joiners` lint) warns above it. |
| `watchdog` | 120/240/420 s | Stall levels for a commander run: `warn_s`, `recover_s`, `fail_s`; a negative value turns a level off. The daemon's `dungeon_run` method can override them field by field. |
| `avoid` | none | Boxes of ground the party must never stand or walk in: `{"name":...,"min":{"x","y","z"},"max":{...},"why":...}` each. Max Z is just above the deadly surface, so ledges above stay walkable. |
| `legs` | required | At least one leg, run in order. |

# Status and forever_status

`status` says how far the plan's route has been checked:

| First word | Meaning |
|---|---|
| `draft` | A skeleton from the world database export. The route has not had its mapping walk, so waypoints, pull order, doors and events are unchecked. |
| `mapped` | The route was corrected from a mapping walk (a GM-invisible walk before the first live run). |
| `live-checked` | The plan was run live by a party. |

`forever_status` is a separate axis: `n/a` (Turtle-custom content a Forever server does not have), `todo` (not
exported for a Forever server yet), `exported` (`tools/dungeongen` wrote the plan's run-leader rows for one and its
spawns were checked against a Forever world export), `refused` (it was checked against ForeverCore data and cannot
be cleared for a Forever run: the free text names the refused field, or the spawns or the route the server does not
have) or `live-checked` (it was run on a Forever server). `plan-list` prints the whole value, free text included.

A naming rule that bites: `plan-export` refuses a `draft` plan unless `--allow-draft` is passed, and
`--all-live` exports only `live-checked` plans. The plan check does not care about the status: it walks the file as
it stands and prints what is wrong.

# Legs

A leg is one stretch: walk to `to` (through `via`), clear its packs in order, run its `steps`, then kill its `boss`.
Every part is optional, but a leg must hold at least a pull group, a step, a boss or an after-boss step.

| Field | Meaning |
|---|---|
| `name` | Leg name for messages and for `--legs`/`--last-leg` on the commands. |
| `to` | Where the leg ends, `{"x","y","z"}`. |
| `via` | Waypoints walked in order before `to`. A plain point is a navmesh walk; extras below. |
| `pull_groups` | The packs, in the order they are cleared. |
| `skip` | Entries the leg never waits for (rares, escorts, friendly NPCs, adds that die with the boss). |
| `leave` | Spawns the run must not pull, fight or walk up to: `{"entry","spawn","at":{"x","y","z"},"why"}`. Approaches and stands keep 15 yd away. |
| `steps` | Things done between the packs and the boss. |
| `boss` | The boss of the leg. |
| `after_boss` | Steps done once the boss is dead. Same kinds as `steps`. |
| `optional` | `{"item":N,"name":"..."}`: the leg is skipped, not failed, when neither the lead bot nor the carrier carries that item. |
| `skip_export` | Keeps the leg out of the run-leader rows unless `plan-export --with-legs` names it. Never with `optional`. |
| `companions_loot` | `false` orders the party's corpse and object looting off for the whole leg. |
| `leftover_entries` | Summoned creatures a step leaves idle; the driver sweeps them before the next batch. |
| `needs_walk` | The `to` point is not on the navmesh; the mapping walk must find the real waypoint. |
| `rest` | Overrides for the rest gates (below). |

Waypoint extras: `"direct": true` makes the step to that point a straight line instead of a navmesh path (at most
25 yd across and 8 yd up or down, ground under the end, and a `via` point before it where the party gathers);
`"exact": true` makes the point before a direct step exact (the lead bot must stand within 1.5 yd of it);
`"check_skip": "reason"` exempts that hop from the plan check; `"direct": true, "gm_cross": true` has a GM move the
party there (any distance, every living member within 10 yd before the run goes on).

A pull group holds `entries` and `positions` (one point per member at most; the first is the one to pull). Extras:
`ambush` (stealthed: walked through, not pulled), `radius` (yards from a position that still count as a member,
default 25), `member_radius` (the same for position matching), `team`, `equiv` (a label tying packs that can stand in
for each other), `patrol` (a loop that walks near the pack: `entries`, `path`, `period_s`, `period_estimate`,
`source`), `approach` (up to four points the run leader walks before pulling), `hold`, `stand_pull` with `clear_yd`,
and `max_bodies`.

# Steps

A step holds exactly one of these keys:

| Key | What it does |
|---|---|
| `use_object` | Uses a game object: `entry`, `near`, `range` (default 8 yd), `spawn` (exact object guid), `alt_entries` (the entry another server uses for the object at the same position, keyed by server name; `plan-check --forever` reads the `forever` entry and every other reader keeps `entry`), `stand`, `requires_item`, `via_item` (use the item on the object), `proof`. |
| `wait_for` | Waits for `gameobject` (`state` `ready`, `active`, `active_alt`, plus `gone_ok`), `hostiles_gone` (`radius`, optional `center`) or `unit` (`entries`, `near`, `radius` default 50 yd, `state` `seen`/`hostile`/`dead`/`gone`). `timeout_s` default 60, at most 600 (a hold's at most 1800); `fail_text` says why it matters. |
| `loot_object` | Opens a chest or node and takes the contents: `entry`/`near`, `stand`, `expect_item`, `batch`. A batch is 3-6 chests within 15 yd of each other. |
| `use_item` | Uses an item from the leader's bags: `entry`, `name`, `requires_item` (a reagent the use consumes), `expect_item`. |
| `ritual` | Several bots use a summoning object together: `entry`, `position` (also as `[x,y,z]`), `participants` 2-5, `stand`. |
| `gossip` | Talks to an NPC and picks a menu option: `npc`, `near`, `option` (index, at most 99) or `text` (at most 80 characters, exactly one of the two), `by` (`agent`, `carrier`, `tank`), `stand` (within 5 yd of `near`), `retry_s` (default 15, at most 120), `fail_text`, `dead_is_done`. |
| `area_trigger` | Steps into an area trigger: `id`, `center`, `radius`, `stand` (inside the zone), `by` (`auto`, `bot`, `agent`), `expect` (`unit <entry>[,<entry>...] <state>`), `timeout_s`. |

A `hold` block (on a pull group, a boss or inside a `wait_for`) is a scripted event the run leader fights out on one
spot: `instance_data` (the value's index), `object` plus `state` (done by a game object instead), `value` (the exact
instance-data value), `spot`, `radius` 1-100 yd, `timeout_s` up to 1800, `entries`, `skip_if_dead_of_gossip`,
`reset_to_gossip`. A boss's hold may leave `instance_data` out (the event ends when its units are dead).

# Boss and rest

The boss holds `entry` and `position`. Extras: `first` (entries to kill before the boss, in order), `phase_entries`
(what the encounter turns into or summons), `team`, `loot_item` (the item the leader must take from the corpse),
`companions_loot`, `tactics` (a player's extra rules for the fight), `needs_walk`, `radius` (search radius for a
patrolling boss, default 30), `stand_pull`, `approach`, `hold`, `wait_s` (how long to wait at the point for a
patrolling boss, 1-600), `skip_unreachable` (why the boss may stand out of reach), `summon_open_step` (the 1-based
`steps` place of the `use_object` that summons the boss, for raid walks).

A leg's `rest` overrides the rest gates: `health` (default 90 percent), `healer_mana` (50), `mana` (35), `max_s`
(60 s), and the walking-rest floors `walk_yd` (60 yd), `walk_health` (70), `walk_healer_mana` (40), `walk_mana` (25).

Before a boss the run holds the party to a gate of its own, apart from the rest overrides: after both rest windows
the healer must be at 50% mana and the tank at 70% health. A party still short stops `boss-not-ready` and the run
records a `boss_rest_timeout` event; the gate fails closed, so a missing, dead or unreadable member, or a healer
whose power is not mana, counts as short.

# Keys and items

A `use_object` on a locked object does nothing for a bot that does not carry the key, and the run is refused
`no-item` at the step. The plan check reports two halves of that gap as issues with the reason `key`: a locked
object the plan names without `requires_item`, and a `requires_item` that no earlier step or boss loot provides. The
objects whose lock is known are the Zul'Farrak troll cages (Executioner's Key, item 8444) and Uldaman's Keystone
(Staff of Prehistoria, item 7733).

# Examples

A minimal leg: walk in, pull one pack, kill the boss.

```json
{
  "version": 1,
  "name": "example",
  "map": 389,
  "entrance": {"x": 0, "y": 0, "z": 0},
  "legs": [
    {
      "name": "one_pack",
      "to": {"x": 20, "y": 10, "z": 0},
      "pull_groups": [
        {"entries": [12345], "positions": [{"x": 30, "y": 10, "z": 0}]}
      ],
      "boss": {"entry": 12346, "position": {"x": 60, "y": 10, "z": 0}}
    }
  ]
}
```

A door that needs an item (the pattern Zul'Farrak uses):

```json
"steps": [
  {"use_object": {"entry": 141073, "near": {"x": 10, "y": 5, "z": 0},
                  "requires_item": {"entry": 8444, "name": "Executioner's Key"}}},
  {"wait_for": {"gameobject": {"entry": 141073, "near": {"x": 10, "y": 5, "z": 0}, "state": "active"},
                "timeout_s": 60, "fail_text": "the cage did not open"}}
]
```

# Writing a plan for a new dungeon

1. Generate a draft from the map's world export with `tools/dungeongen` (see [docs/PLANS.md](../../PLANS.md)), or
   copy the shape of a nearby plan.
2. Fix the order by hand: packs, doors, events and the boss order are the world export's guesses, not a route.
3. Check it: [plan-check and plan-scout](plan-tools.md) for placement, line of sight, pull sizes and events.
4. Walk it with zero fighting: [route-tools](route-tools.md), `forever route-walk`.
5. Run it: [dungeon-run](dungeon-run.md), [dungeon-play](dungeon-play.md) or, with a module-led run,
   [dungeon-watch](dungeon-watch.md). Then set `status` to what actually happened.
