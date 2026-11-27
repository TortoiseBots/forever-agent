# Dungeon plan Forever status (issue #223)

Every plan file under `dungeons/` carries a `forever_status`: where it stands against a ForeverCore server. This page
is the one table of all of them, refreshed from the plan files (the plan's own value is the source of truth; `forever-agent
plan-list` prints it, and `forever-agent plan-list --json` gives the same fields).

The words are the ones [the plan format](../manual/dungeons/plans.md#status-and-forever_status) defines:

| Word | Meaning |
|---|---|
| `n/a` | Turtle-custom content: a Forever server has no such dungeon or wing. |
| `todo` | Not checked against ForeverCore data yet. |
| `exported` | `plan-export --forever` writes the plan's run-leader rows, and every exported unit row matched a ForeverCore spawn of its entry and every exported segment walks on the Forever tiles. |
| `refused` | Checked against ForeverCore data and cannot be cleared for a Forever run; the detail names the refused field, the missing spawns or the segments that do not walk. |
| `live-checked` | Run live on a Forever server. |

Checked 2026-11-27 (`tools/dungeongen/export-forever.sh`, the Forever v16 tiles, `plan-export --forever`,
`plan-check --forever`, `plan-scout --forever`; `plan-probe --forever` and `trash-check --forever` where a plan
exports its rows). Each map's spawn list is exported at its instance's own difficulty (1 for the upper spire, 148 for
AQ20, 9 for the other five raids). Counts: `n/a` 14, `todo` 1, `exported` 2, `refused` 32, `live-checked` 0.

## Status

| Plan | Kind | Map | Level | Forever | Detail |
|---|---|---|---|---|---|
| `frostmane_hollow` | dungeon | 822 | 13-18 | `n/a` | Turtle custom content |
| `ragefire_chasm` | dungeon | 389 | 13-18 | `exported` |  |
| `deadmines` | dungeon | 36 | 18-22 | `refused` | 2026-11-24: the map-36 layout differs: 23 pull units stand beyond any ForeverCore spawn of their entry, the 8 Turtle-only Defias Companions (3450) have no spawn at all, and the hall route has no path on the Forever tiles |
| `wailing_caverns` | dungeon | 43 | 18-23 | `refused` | 2026-11-26: 3 of 203 exported unit rows miss a ForeverCore map-43 spawn of their entry within the row's radius — two Druids of the Fang (3840) and the Kresh 3653 boss — and 1 of 222 plan-check points is off the Forever tiles; plan-export --forever refuses no field |
| `shadowfang_keep` | dungeon | 33 | 19-25 | `refused` | 2026-11-24: plan-export refuses the courtyard door's gossip step, the Turtle-only Sever/Prelate Ironmane/Mistblade Rippers have no ForeverCore spawn, and the baron hall's packs stand 34.6-99.9 yd from the keep's ForeverCore spawns |
| `blackfathom_deeps` | dungeon | 48 | 22-27 | `refused` | 2026-11-27: on the full plan the Turtle-only Velthelaxx wing's 62528/62529/62530 and the 4798/4799 spawns have no ForeverCore map-48 spawn within their rows' radius (25 units), 40 of 203 exported segments do not walk on the Forever tiles and 43 of 244 plan-check points fail |
| `stormwind_stockade` | dungeon | 34 | 23-28 | `exported` | 2026-11-24: all 90 units match ForeverCore map-34 spawns and every exported segment walks on the Forever tiles |
| `windhorn_canyon` | dungeon | 820 | 24-29 | `n/a` | Turtle custom content |
| `dragonmaw_retreat` | dungeon | 816 | 26-31 | `n/a` | Turtle custom content |
| `razorfen_kraul` | dungeon | 47 | 26-31 | `refused` | 2026-11-26: 15 Turtle-only units (62501/62502/62503) have no ForeverCore map-47 spawn, 5 more stand 16.3-52.2 yd from any core spawn of their entry and 34 of 227 exported segments do not walk on the Forever tiles |
| `gnomeregan` | dungeon | 90 | 27-33 | `refused` | 2026-11-26: the Launch Bay entry does not walk on the Forever map-90 tiles (the leg-3 via-to hop stops 21.7 yd short), 4 of 167 units (6207, 6211, 6230, 6232) miss a core spawn within the row's radius and 2 of 101 exported segments are server partial |
| `scarlet_graveyard` | dungeon | 189 | 30-35 | `refused` | 2026-11-26: 18 Turtle-only units (61971/61972/14693) have no ForeverCore map-189 spawn, 32 more stand 20.3-101.3 yd from any core spawn of their entry and 15 of 57 exported segments do not walk (server NOPATH) |
| `crescent_grove` | dungeon | 802 | 31-36 | `n/a` | Turtle custom content |
| `scarlet_library` | dungeon | 189 | 33-38 | `refused` | 2026-11-26: the boss's 61983 has no ForeverCore map-189 spawn, 6 units of 4540 stand 24.4-41.7 yd from any core spawn of the entry and 5 of 47 exported segments are server NOPATH |
| `stormwrought_ruins` | dungeon | 818 | 33-38 | `n/a` | Turtle custom content |
| `razorfen_downs` | dungeon | 129 | 35-40 | `refused` | 2026-11-25: 51 units of the Turtle-only entries 62676/62677/62678/62679 have no ForeverCore map-129 spawn, 4 more unit rows (7343, 7353, 7354) miss a core spawn of their entry and 38 of 264 points fail on the Forever map-129 tiles |
| `scarlet_armory` | dungeon | 189 | 35-40 | `refused` | 2026-11-25: the boss's 61982 has no ForeverCore map-189 spawn, 17 units stand 29.0-86.1 yd from any core spawn of their entry and 9 of 45 exported segments are server NOPATH |
| `scarlet_cathedral` | dungeon | 189 | 37-42 | `refused` | 2026-11-25: plan-export refuses legs[1].boss.hold; the module's plan table has no hold rows, and the plan keeps the hold for the Turtle stack |
| `uldaman` | dungeon | 70 | 41-45 | `refused` | 2026-11-25: plan-export --forever refuses six fields (Revelosh's use_item and loot_item, two rituals, the Stone Keeper hold and the Archaedas hold) the module's plan table has no row for; the 255 points all walk on the Forever map-70 tiles |
| `gilneas_city` | dungeon | 815 | 43-49 | `n/a` | Turtle custom content |
| `maraudon_purple` | dungeon | 349 | 44-49 | `refused` | 2026-11-25: 73 of 490 unit rows miss a ForeverCore map-349 spawn of their entry within the row's radius and 1 of 214 segments does not walk on the Forever tiles |
| `zulfarrak` | dungeon | 209 | 44-48 | `refused` | 2026-11-25: plan-export --forever refuses twelve fields (gossip, hold, unit wait_for, area_trigger, loot_item, leftover_entries) and leg 13's companions_loot false; ForeverCore has no map-209 spawn of 7273/7275/62495/62496/62497/62498/62500/7796 and 35 of 254 points fail on the Forever tiles |
| `maraudon_orange` | dungeon | 349 | 45-50 | `refused` | 2026-11-25: 50 of 427 unit rows miss a ForeverCore map-349 spawn of their entry within the row's radius and 3 of 212 segments do not walk on the Forever tiles |
| `sunken_temple` | dungeon | 109 | 47-52 | `refused` | 2026-11-25: plan-export --forever refuses legs[0].steps[6].wait_for (a unit wait) and legs[0].boss.hold; the module's plan table has no row for either, and 2 of 401 points fail on the Forever map-109 tiles |
| `hateforge_quarry` | dungeon | 808 | 50-55 | `n/a` | Turtle custom content |
| `blackrock_depths` | dungeon | 230 | 52-57 | `refused` | 2026-11-25: the Turtle entry 61139 Shan-xi Watershaper has no ForeverCore map-230 spawn and 24 of 490 segments do not walk on the Forever tiles |
| `dire_maul_east` | dungeon | 429 | 53-58 | `refused` | 2026-11-25: 32 of 334 unit rows miss a ForeverCore map-429 spawn of their entry within the row's radius; every exported segment walks |
| `lower_blackrock_spire` | dungeon | 229 | 55-60 | `refused` | 2026-11-25: 10 of 413 unit rows miss a ForeverCore map-229 spawn of their entry within the row's radius and 22 of 287 segments do not walk on the Forever tiles |
| `stratholme_live` | dungeon | 329 | 55-60 | `refused` | 2026-11-25: 18 of 231 unit rows miss a ForeverCore map-329 spawn of their entry within the row's radius (10813 Balnazzar is Dathrohan's update entry; 16101 and 16102 are summoned) and 2 of 79 segments stop short on the Forever tiles |
| `stratholme_undead` | dungeon | 329 | 56-60 | `refused` | 2026-11-25: plan-export --forever refuses legs[4].boss.hold (Baron Rivendare); the module's plan table has no hold rows |
| `dire_maul_north` | dungeon | 429 | 57-60 | `refused` | 2026-11-25: 12 of 273 unit rows miss a ForeverCore map-429 spawn of their entry within the row's radius and 2 of 93 segments stop short on the Forever tiles |
| `dire_maul_west` | dungeon | 429 | 57-60 | `refused` | 2026-11-25: plan-export --forever refuses five pull-group holds (legs[0].pull_groups[7] and [23], legs[2].pull_groups[8], legs[4].pull_groups[9] and [14]); the module's plan table has no hold rows |
| `scholomance` | dungeon | 289 | 57-60 | `refused` | 2026-11-25: the Turtle boss 14695 Lord Blackwood has no ForeverCore map-289 spawn and 40 of 410 segments do not walk on the Forever tiles |
| `blackwing_lair` | raid | 469 | 58-60 | `refused` | 2026-11-27: 135 of 273 pack unit rows miss a ForeverCore map-469 spawn of their entry within the row's radius — the Corrupted Whelps 14023/14024/14025 (108 units) have no static spawn, Firemaw 11983 stands 112 yd from its only one and Nefarian 11583 has none; plan-export --forever refuses the two gossip steps and the two boss holds, and no leg is remapped; plan-check fails 0 of 309 points with 106 lints (34 errors), plan-scout fails 32 of 120 steps |
| `temple_of_ahnqiraj` | raid | 531 | 58-60 | `refused` | 2026-11-27: 18 of 521 pack unit rows miss a ForeverCore map-531 spawn of their entry within the row's radius (5.7-87.2 yd off), Princess Huhuran 15509 stands 34.3 yd from its only spawn and Ouro 15517 has no static spawn; plan-export --forever refuses legs[9].boss.hold and no leg is remapped; plan-check fails 3 of 688 points with 721 lints (162 errors), plan-scout fails 157 of 750 steps |
| `upper_blackrock_spire` | raid | 229 | 58-60 | `refused` | 2026-11-27: all 202 pack unit rows match a ForeverCore map-229 spawn, but Turtle's Lord Valthalak 16042 has none; plan-export --forever refuses the draft and, with --allow-draft, writes no rows because no leg is remapped; plan-check fails 0 of 212 points with 335 lints (99 errors), plan-scout fails 82 of 230 steps |
| `zul_gurub` | raid | 309 | 58-60 | `todo` | 2026-11-26 offline-checked on ForeverCore data: leg 0 passes plan-scout and plan-check with 17 allowed joiners, one-body aggro and step-17 cut-row findings and 52 warnings; legs 1-8: 694 lints (125 errors, 569 warnings) and 3 failed points; leg 0's module plan rows are exported with `plan-export --forever --last-leg high_priestess_jeklik`, and since the other legs are not remapped a player-led run reads them from the plan |
| `black_morass` | dungeon | 269 | 59-60 | `n/a` | Turtle custom content |
| `karazhan_crypt` | dungeon | 800 | 59-60 | `n/a` | Turtle custom content |
| `lower_karazhan_halls` | raid | 532 | 59-60 | `n/a` | Turtle custom content |
| `naxxramas` | raid | 533 | 59-60 | `refused` | 2026-11-27: plan-export --forever refuses the two gm_cross vias (legs 12 and 14), so plan-scout refuses the plan and plan-check has no lints; 3 of 719 pack unit rows miss a ForeverCore map-533 spawn of their entry within the row's radius and Sapphiron 15989 has no static spawn; plan-check fails 12 of 776 points (3 unreachable, 9 look points too far) |
| `ruins_of_ahnqiraj` | raid | 509 | 59-60 | `refused` | 2026-11-27: 16 of 218 pack unit rows miss a ForeverCore map-509 spawn of their entry within the row's radius (14.9-87.7 yd off); plan-export --forever refuses the Andorov gossip, Rajaxx's unit wait and nested hold, and no leg is remapped; plan-check fails 51 of 331 points with 262 lints (63 errors), plan-scout fails 57 of 350 steps |
| `emerald_sanctum` | raid | 807 | 60-60 | `n/a` | Turtle custom content |
| `molten_core` | raid | 409 | 60-60 | `refused` | 2026-11-27: the Turtle-only Basalthar/Incindis/Thaurissan wing's 65020, 52145, 52150-52152 and 57642 have no ForeverCore map-409 spawn (36 of 235 pack unit rows), and Majordomo 12018 and Ragnaros 11502 have no static spawn; plan-export --forever refuses the three Majordomo gossip steps and no leg is remapped; plan-check fails 49 of 362 points with 524 lints (88 errors), plan-scout fails 82 of 508 steps |
| `onyxias_lair` | raid | 249 | 60-60 | `refused` | 2026-11-27: the map has 7 ForeverCore spawns; the Turtle-only 49016/49017 have none and 11 of 14 pack unit rows miss a core spawn of their entry within the row's radius (one Warder 61 yd off); plan-export --forever refuses legs[1].boss.phase_entries and no leg is remapped; plan-check fails 0 of 28 points with 28 lints (1 error), plan-scout fails 1 of 34 steps |
| `stormwind_vault` | dungeon | 35 | 60-60 | `n/a` | Turtle custom content |
| `timbermaw_hold` | raid | 819 | 60-60 | `n/a` | Turtle custom content |
| `upper_karazhan_halls` | raid | 814 | 60-60 | `n/a` | Turtle custom content |
| `wailing_caverns-extra` | dungeon | 43 | — | `refused` | 2026-11-25: plan-export --forever refuses 17 fields (15 unit waits, the return gossip and legs[11].boss.hold); the module's plan table has no row for any of them |

## Batches of issue #223

| Batch | Plans | Result |
|---|---|---|
| 1 | `deadmines`, `shadowfang_keep`, `blackfathom_deeps`, `gnomeregan`, `stormwind_stockade` | `stormwind_stockade` exported; the other four refused. |
| 2 | `razorfen_kraul`, `scarlet_graveyard`, `scarlet_library`, `scarlet_armory`, `scarlet_cathedral` | all refused (Turtle-only entries and the cathedral's Mograine hold). |
| 3 | `razorfen_downs`, `uldaman`, `sunken_temple`, `zulfarrak`, `maraudon_purple` | all refused (Turtle-only entries, refused plan fields, missing spawns and segments). |
| 4 | `maraudon_orange`, `lower_blackrock_spire`, `blackrock_depths`, `scholomance`, `dire_maul_east`, `dire_maul_north`, `dire_maul_west`, `stratholme_live`, `stratholme_undead`, `wailing_caverns-extra` | all refused (missing spawns, refused fields and segments that do not walk). |
| re-run round 1 | `deadmines` and `shadowfang_keep` re-run (same refusals, and deadmines' Turtle 3450 count corrected to its 8 units); `wailing_caverns` newly checked | `wailing_caverns` refused on 3 missing unit spawns and one off-mesh point. |
| re-run round 2 | `blackfathom_deeps`, `razorfen_kraul`, `gnomeregan`, `scarlet_graveyard`, `scarlet_library` | all refused. `blackfathom_deeps` is checked with the Turtle wing in the plan (25 units without a core spawn, 40 of 203 segments that do not walk, 43 of 244 points that fail); the other four reproduce their earlier numbers. `plan-scout --apply-fixes` was refused on `blackfathom_deeps` (the edits add 30 plan-check issues) and `razorfen_kraul` (a `move_stand` fix names a leg without a boss); `gnomeregan` and the two Scarlet plans got a patch (approach points) that was not applied: the shared plan's points stay as they are. |
| raids | `upper_blackrock_spire`, `molten_core`, `onyxias_lair`, `blackwing_lair`, `ruins_of_ahnqiraj`, `temple_of_ahnqiraj`, `naxxramas` | all refused: Turtle-only bosses and trash, spawns the core lacks or stands far from, plan fields the module's table cannot carry (gossip, holds, a unit wait, `phase_entries`, `gm_cross`), and no leg remapped for ForeverCore, which a raid export needs. `plan-scout --apply-fixes` wrote a patch for six of the seven (the Naxxramas scout refuses the `gm_cross`); none is applied, so the shared plans' points stay as they are. |

The re-run rounds repeat the level-ordered sweep from the start, five 5-player plans at a time, with the spawn list
re-exported from the core (`tools/dungeongen/export-forever.sh`) and the Forever v16 tiles. A Forever check writes a
plan's `forever_status` and notes only: where `plan-scout --apply-fixes` offers an edit, the plan keeps its points and
the analysis stays the refusal the value names.

The 14 `n/a` plans are the Turtle-custom content (`black_morass`, `crescent_grove`, `dragonmaw_retreat`,
`emerald_sanctum`, `frostmane_hollow`, `gilneas_city`, `hateforge_quarry`, `karazhan_crypt`, `lower_karazhan_halls`,
`stormwind_vault`, `stormwrought_ruins`, `timbermaw_hold`, `upper_karazhan_halls`, `windhorn_canyon`).

What is left: `zul_gurub` is the one plan still `todo`; its leg 0 is offline-checked and exported
(`plan-export --forever --last-leg high_priestess_jeklik`), and its other legs are not remapped. The seven raids
checked here (`upper_blackrock_spire`, `molten_core`, `onyxias_lair`, `blackwing_lair`, `ruins_of_ahnqiraj`,
`temple_of_ahnqiraj`, `naxxramas`) are `refused` with the reason their value names. A raid exports the rows of its
remapped legs only, and none of the seven has a leg a Forever check has remapped, so `plan-export --forever` writes
no rows and `plan-probe --forever` and `trash-check --forever` (which read an exported migration) do not run; the
rest of a raid check reads the plan file directly. The instances are not missing on Forever: each map has its own
spawns (map 249 has Onyxia and her Warders, map 409 the classic Molten Core bosses), so none of the seven is `n/a`.

## Note on shared plan files

A plan file serves both stacks. A Forever check never deletes a Turtle spawn, leg or step: where ForeverCore lacks
content the plan uses, the plan keeps it and says `refused` with the missing content named. `alt_entries` is the only
per-server plan field (this server's object entries at one position).
