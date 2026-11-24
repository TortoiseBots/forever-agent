---
type: Reference
title: Dungeon notes
description: What the five oldest hand-written plans contain, how far each has been checked, and the facts a first run should know.
tags: [dungeons, plans, notes]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T21:50:00Z }
sources:
  - id: plans-md
    resource: docs/PLANS.md
  - id: rfc
    resource: dungeons/ragefire_chasm.json
  - id: zf
    resource: dungeons/zulfarrak.json
  - id: uldaman
    resource: dungeons/uldaman.json
  - id: wc
    resource: dungeons/wailing_caverns.json
  - id: dm
    resource: dungeons/deadmines.json
---

# What this is

The repository ships a plan for every dungeon and raid. Five of them were written by hand before the generator was
trusted: Ragefire Chasm, Zul'Farrak, Uldaman, Wailing Caverns and the Deadmines. This page says what each contains and
how far it has been checked, so a first run knows what to expect. The generated table of every plan is
[docs/PLANS.md](../../PLANS.md); the plan files themselves carry the mapping history in their `notes` field.

# At a glance

| Plan | Map | `status` | `forever_status` | Level band | Legs | Pull groups | Bosses | Patrol groups | `tank_min_hp` |
|---|---|---|---|---|---|---|---|---|---|
| Ragefire Chasm | 389 | mapped, not live-checked | exported | 13-18 | 9 | 50 | 4 | 6 | 800 |
| Zul'Farrak | 209 | live-checked | todo | 44-48 | 20 | 67 | 11 | 2 | 2700 |
| Uldaman | 70 | live-checked | todo | 41-45 | 14 | 65 | 8 | 0 | 2850 |
| Wailing Caverns | 43 | mapped, not live-checked | todo | 18-23 | 13 | 87 | 6 | 8 | 850 |
| The Deadmines | 36 | draft, not mapped | todo | 18-22 | 15 | 126 | 6 | 40 | 1200 |

`status` is the route's own check; `forever_status` says whether the plan has been exported for a ForeverCore server.
A `todo` runs through the agent's own driver, not through exported run-leader rows.

# Ragefire Chasm

Map 389, entrance trigger 2230, portal named in the plan, two `avoid` boxes over the lava. Nine legs, four bosses:
Oggleflint, Taragaman, Jergosh and Bazzalan. `tank_min_hp` 800. The plan marks the two fleeing troggs (11319, 11324)
as `fleers` and 11319 as `kill_first`. Six packs have measured patrol loops. The mapping walk was GM-invisible and
nothing was attacked, so the route is `mapped`, not `live-checked`; the core has no instance script for the map, so
there are no doors or events. The lava is the commonest death; Bazzalan's last pack needs the stand point.

# Zul'Farrak

Map 209, entrance trigger 924, portal named. Twenty legs, eleven bosses, 67 pull groups and 43 steps. The route covers
Antu'sul, Theka, Hydromancer Velratha, the pyramid courtyard and Witch Doctor Zum'rah, then the scripted legs: the
stairs event, Sergeant Bly, the End Door and Chief Ukorz Sandscalp. `tank_min_hp` 2700. The plan's `optional` gahzrilla
leg needs the Mallet of Zul'Farrak (item 9240); the export leaves it out unless you hand the item to the lead bot and
pass `--with-optional gahzrilla`. The last scripted legs have not all been run live; the plan's notes name each one.

# Uldaman

Map 70, entrance trigger 286, entrance to Archaedas. Fourteen legs, eight bosses, 65 pull groups and 7 steps.
`tank_min_hp` 2850. The plan marks four `fleers` (4853, 4848, 4849, 7321) and the Stone Keeper 4852 as `kill_first`.
The first legs were run; the later legs (Obsidian Sentinel, Ancient Stone Keeper, Galgann, Grimlok, the Altar of the
Keepers and Archaedas) were written from the world database and the navmesh and have not been run as a party. Doors,
altars and the hole crossing are the parts to watch: the altar steps are rituals for the carrier and two bots, and the
west-to-east hole crossing has no link in the mesh.

# Wailing Caverns

Map 43, entrance trigger 228, thirteen legs, six bosses, 87 pull groups and eight measured patrol loops.
`tank_min_hp` 850. The route was walked GM-invisibly (401 walk calls, nothing attacked), so it is `mapped`, not
`live-checked`. The Serpentis ramp is a direct step with `check_skip`; Kresh is one neutral-river group; critters and
rares are skipped. The return after Naralex is the least proven part: the plan's notes call it a draft and it cuts at
the return gossip.

# The Deadmines

Map 36, entrance trigger 78, fifteen legs, six bosses, 126 pull groups, eight steps and 40 packs with patrol data.
`tank_min_hp` 1200. The plan is a `draft`: its doors are scripted deaths the mesh cannot prove (the walk goes through
shut doors), the foundry and cannon levers need a click and a wait-active, and the catwalk point stays in aggro until a
live run settles it. Nine `fleers` and the kill order on 636 and 622 are marked. Do not treat a draft as a route until
the mapping walk has corrected it.

# Related

- [Plans](plans.md): every field these notes name.
- [Plan tools](plan-tools.md): `plan-check` and `plan-scout` before a first run.
- [dungeon-run](dungeon-run.md), [dungeon-play](dungeon-play.md): the Forever drivers.
- [docs/PLANS.md](../../PLANS.md): the generated table of every plan.
