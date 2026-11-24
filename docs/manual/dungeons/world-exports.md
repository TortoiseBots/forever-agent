---
type: Concept
title: World exports and spawns
description: The files that describe a map's spawns, packs, patrols, game objects and entrances, and how the checks read them.
tags: [dungeons, spawns, export, patrols]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T21:44:00Z }
sources:
  - id: exporter
    resource: tools/dungeongen/export.sh
  - id: forever-exporter
    resource: tools/dungeongen/export-forever.sh
  - id: worldspawn
    resource: internal/worldspawn/worldspawn.go
  - id: foreverdb
    resource: internal/worldspawn/foreverdb.go
  - id: packs
    resource: internal/worldspawn/packs.go
  - id: patrol
    resource: internal/worldspawn/patrol.go
  - id: plan-world
    resource: cmd/forever-agent/plan_world.go
---

# What this is

A world export is a read-only snapshot of one map's spawns, written from the server's world database. The offline plan
checks read it to estimate aggro, packs and patrols; they never touch a running server. `plan-check`, `plan-probe`,
`trash-check` and `plan-scout` take it as `--spawns`, and `plan-check --forever` takes a ForeverCore export or a thin
spawn list in the same place.

# The files

One folder per map, for example `tools/dungeongen/testdata/map36`. The 1.12 exporter, `tools/dungeongen/export.sh
<map> <outdir>`, writes:

| File | Contents |
|---|---|
| `creatures.tsv` | One row per spawn: guid, entry, name, levels, faction, position, movement, detection range, flags, speed, group, pool. |
| `groups.tsv` | `creature_groups` rows: who moves and wakes together. |
| `links.tsv`, `link_templates.tsv` | `creature_linking` and its template rows: the aggro links between spawns. |
| `movement.tsv`, `movement_template.tsv` | Waypoint loops, per spawn guid and per entry. |
| `gameobjects.tsv` | Doors, chests and other spawns the plan may use. |
| `entrances.tsv`, `portals.tsv` | Area triggers and the entrances they lead to. |
| `script_waypoint.tsv` | Scripted waypoints, for the plans that need them. |

An older export may lack the raid columns and the movement files; the loader says what it had to assume, and an export
with no `creatures.tsv` is refused with the command that writes one.

For ForeverCore use `tools/dungeongen/export-forever.sh <map> <dir>` (or `--all <root>` for every map). It writes
`creatures.tsv`, `groups.tsv` (the core's `creature_formations` rows and their `group_ai` flags), `movement.tsv`,
`movement_template.tsv`, `gameobjects.tsv`, `entrances.tsv` and `portals.tsv`. ForeverCore has no `creature_linking`
or `creature_movement` tables, so no `links.tsv` or `link_templates.tsv` is written, and `portals.tsv` carries only
its header: the trigger's own position is in the client's `AreaTrigger.db2`. The loader notes both.

# Packs

`Packs` groups hostile spawns with a union-find: spawns sharing an `AGGRO_TOGETHER` group join, and every aggro link
joins slave to master in both directions. Each pack records its member guids (lowest first), its centroid and radius,
and whether it patrols (a member with movement type 2, or a formation follower of one) or is pooled (any member with a
pool entry, so it may not be spawned at all).

In a ForeverCore export the group's option set is the OR of every member's `group_ai`, including the leader's own row;
a formation whose leader has no row of its own is dropped, as the core drops it, and the note says how many. A spawn
whose level the core does not carry is assumed level 60 and the export says how many.

# Patrols

The export's waypoint loops become patrols: the loop's path and waits, the period (loop length at 2.5 yd/s plus waits),
and the source (`creature_movement <guid>` or `creature_movement_template <entry>`). Formation followers attach to
their leader's loop. The database gives positions, not phases: the checks know where a patrol walks, not where it is
right now, so [patrol-record](route-tools.md) exists to measure a real loop and feed it in.

# Game objects and entrances

`gameobjects.tsv` names the doors, chests and objects a plan can use, with their positions. `entrances.tsv` names the
area triggers that lead into an instance; `portals.tsv` carries the portal rows on the 1.12 export, while a
ForeverCore export has only its header (the core keeps the trigger's position in the client's `AreaTrigger.db2`). A
plan step that uses a locked object also needs its key item, which the plan carries as `requires_item`: the exporter
has no lock data in `gameobjects.tsv` and the plan check reports the missing key.

# Related

- [Plan tools](plan-tools.md): the checks that read the export.
- [Aggro estimate](aggro-estimate.md): the rule the export feeds.
- [Route tools](route-tools.md): `patrol-record` for measured loops.
- [Plans](plans.md): the plan fields that name objects, entrances and patrols.
