---
type: Concept
title: How the aggro estimate works
description: The aggro radius, packs, call-for-help ring and drag sweep an offline check counts, and what the estimate cannot see.
tags: [dungeons, aggro, patrols, checks]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T21:42:00Z }
sources:
  - id: aggroest
    resource: internal/aggroest/aggroest.go
  - id: worldspawn
    resource: internal/worldspawn/worldspawn.go
  - id: scriptaggro
    resource: internal/worldspawn/scriptaggro.go
---

# What this is

Walking past a creature can wake it. The offline checks (`trash-check`, `plan-check`'s aggro and boss lints, and the
run leader's own pull estimate) count what would join before a pull: the target's pack, everything in aggro reach of
the walk, the call-for-help ring, and the drag sweep. The estimate mirrors the bot module's run leader, so the
offline check and the live run agree on the same rule.

It reads the map's world export, so it needs no login: see [World exports and spawns](world-exports.md) for the files.

# Aggro radius

Each spawn gets one aggro radius: its detection range minus the party-minus-creature level difference (floored at 25
levels), never below the smaller of detection and 5 yd, times the aggro rate. A creature with `NO_AGGRO` or no
detection range never wakes: 0. Three bosses wake farther than their template says, by their own script: Moam 60 yd,
Ossirian 45 yd, Ayamiss 40 yd. The estimate uses the party's lowest level, the widest radius.

# Packs and rings

A pack is the set that wakes as one: spawns sharing an `AGGRO_TOGETHER` creature group, plus both directions of every
aggro creature link. Only hostile spawns join (faction 35 is friendly; vendors and quest givers are not trash).
ForeverCore has no creature links; there a pack is its `creature_formations` group alone.

Proximity wakes every mob without `NO_AGGRO` inside its reach of the party's walk (2-yd samples, 3 yd of spread), the
tank-only walk (1.5 yd), the stand (3 yd, 10 for a raid) and the fight point, with sight from the mob to the point.
Then the ring carries: every member calls every creature of the same faction within 20 yd of its own position (sight
from caller to candidate, within 12 yd vertically), each call adding the called mob's pack, up to 2 hops. The drag
sweep pulls the same-faction creatures within 20 yd along the way back.

# Limits

The estimate reads spawn positions, not live positions: a patroller counts where it stands now, and an unknown patrol
phase over-counts. Without the navmesh there is no sight check, so through-wall aggro is over-counted and the report
says so. Other floors are cut by a 15-yd 3D filter and reported, not modelled. [trash-check](plan-tools.md) adds the
verdicts on top: what the plan targets itself, what walks into the route, and what only a patrol brings.

# Related

- [Plan tools](plan-tools.md): `trash-check`, `plan-check` and their flags.
- [World exports and spawns](world-exports.md): the spawn files the estimate reads.
- [Route tools](route-tools.md): `patrol-record`, which replaces an unknown patrol phase with a measured loop.
