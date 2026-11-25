---
type: Concept
title: From plan to run leader rows
description: What plan-export writes into the bot module's run leader tables, which plan fields the rows carry, and which they cannot.
tags: [dungeons, plans, export, run-leader]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T21:46:00Z }
sources:
  - id: planexport
    resource: internal/planexport/planexport.go
  - id: forever-export
    resource: internal/planexport/forever.go
  - id: unexpressed
    resource: internal/planexport/unexpressed.go
  - id: plan-tools
    resource: docs/manual/dungeons/plan-tools.md
---

# What this is

`plan-export` turns `dungeons/<plan>.json` into one idempotent world migration the server's bot module reads: the run
leader tables `ai_dungeon_plan`, `ai_dungeon_leg` and `ai_dungeon_step`, plus `ai_dungeon_step_unit` and
`ai_dungeon_boss` on a ForeverCore server. The plan file stays the source of truth; the rows are generated, never
edited by hand.

Two exporters exist:

- the 1.12 rows, for the TortoiseBots run leader;
- the ForeverCore rows, `plan-export --forever`, which writes the module's own object entries from a plan's
  `alt_entries` and one unit row per pack member.

Both read the same plan. This page describes what the rows carry, what the run leader does with them, and what the
plan can say that the rows cannot.

# What the rows carry

Each leg becomes its waypoints (`move`, `move_direct`, `move_hop_exact`), its pulls (`pull`), its steps (`loot`,
`open`, `open_proof`, `use_item`, `wait_object`, `wait_clear`, `rest`, `ritual`, `hold`, `gossip`, `use_on`), and
its boss (`boss`). A step's kind is at most 16 characters; a longer kind is not a kind this format can write.

The step flags tell the module what to honor:

| Flag | Value | Meaning |
|---|---|---|
| ambush | 1 | A stealthed pack: walked through, not pulled. |
| alliance-only | 2 | The step works only for an Alliance party. |
| horde-only | 4 | The step works only for a Horde party. |
| done-by-data | 8 | A hold is done when its instance-data value reads as set. |
| done-by-object | 16 | A hold is done when its object is in the named state (0 ready, 1 active, 2 active_alt). |
| encounter | 32 | A hold follows the core's encounter states. |
| exact | 64 | A move row that must be stood on within the exact-point radius. |
| gone-is-done | 256 | A loot or open row is done when its object is gone. |
| dead-NPC-is-done | 512 | A gossip row is done when its NPC is dead. |
| proof-is-creature | 2048 | An `open_proof` row proves the open with a live creature, not a game object. |
| skip-unreachable | 4096 | A boss row whose boss may stand out of reach by design. |
| stand-pull | 8192 | The party pulls from its stand; the row carries `clear_yd` in its param. |
| reset-to-gossip | 16384 | A units hold whose event the script resets. |
| dead-gossip-ok | 32768 | A units hold whose starting NPC may already be dead. |

Flag 8 and flag 32 are often set together (40) for a hold that is done by instance data in the encounter's own
states.

Two options reshape the rows. `--mmaps DIR` cuts long hops into via points along the navmesh path (a hop over 60 yd
between two moves is cut; with `--approach-hops` the hop to a pull, boss or hold is cut too). `--floor-aware` cuts the
floor-aware way. Avoid boxes reroute hops around deadly ground when the mesh is given.

# What the run leader cannot do

Some steps need the agent's own character: a gossip by `agent`, a `wait_for` on a `unit` without a `hold`, or an
`area_trigger` by `agent`. The export stops before the first leg that holds one: it exports legs up to that point,
prints a `CUT:` line on stdout and notes the cut in the plan row's comment. A plan whose first leg holds such a step
is refused outright.

# What the rows cannot carry

Every plan attribute the rows cannot express is a warning, never a silent drop. The full list is in the exporter; the
ones that change what a run does:

| Plan field | What the run leader does instead |
|---|---|
| `entrance`, teleports, `portal` | The run leader starts inside the instance; getting there is the caller's job. |
| `level_band`, `hire_composition`, `raid` | The run leader leads the party it is bound to, whatever its level or make-up. |
| `team` | No team column on a step; the agent's own driver refuses the other team before the run. |
| `fleers`, `no_cc`, `summoned_kill_first` | No column; the bots' own AI and crowd control decide. |
| `watchdog` | The run leader has its own ceilings. |
| `avoid` | The run leader does not know avoid boxes; export with `--mmaps` to route its hops around them (a fight can still drag the party in). |
| `skip`, `leave` | No list column; a skipped or left-alone spawn near a hop is still a hostile to the run leader. |
| `leftover_entries` | No sweep of idle summoned units; the bots fight what aggroes. |
| `rest` | The run leader's own rest lines apply. |
| `companions_loot: false` | Dropped, and the export is refused unless `--drop-companions-loot` is passed; the bots' looting must be turned off in the run setup. |
| `needs_walk` | The point was not snapped to the mesh; the run leader walks to it as given. |
| `patrol` | No path or period; the run leader pulls the pack where it stands. |
| a boss's `tactics` | The plan's tactics are the agent's; a bound party uses the module's own boss tactics. |
| a step's `fail_text`, a `use_item`'s name | They live in the row's comment, not in a field. |

A `skip_export` leg stays out unless named with `--with-legs`; an `optional` leg stays out unless named with
`--with-optional` (and its item is yours to hand to the lead bot). A `gm_cross` waypoint has no run leader step and is
refused; `--gm-cross-direct` exports it as a `move_direct` for a drop the line crosses, with a warning that it was
never tried live.

# Examples

Export one plan for the 1.12 module, cutting its hops on the mesh:

```sh
forever-agent plan-export --mmaps ./mmaps --out ./migrations/20261124210000_world.sql ragefire_chasm
```

Export every live-checked plan for a ForeverCore server:

```sh
forever-agent plan-export --forever --all-live --out ./migrations
```

Print what would be exported, and why a plan is refused, without writing a file:

```sh
forever-agent plan-export --forever --report uldaman
```

# Related

- [Plan tools](plan-tools.md): every `plan-export` flag and the read-back check.
- [Plans](plans.md): the plan fields named here.
- [Plan troubleshooting](plan-troubleshooting.md): the `CUT:` line and the refusal messages.
