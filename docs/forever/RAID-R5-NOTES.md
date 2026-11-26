# Raid R5 Notes — Zul'Gurub Leg 0 (`high_priestess_jeklik`) ForeverCore Offline Verification

## Scope and Target Choice

Zul'Gurub (`map 309`, 9 legs, 148 pulls, 8 bosses, `kind: "raid"`) was chosen for the first ForeverCore raid plan
remapping check:

- `molten_core` and `onyxias_lair` carry Turtle-only custom creature entries in their encounters and are expected to be
  refused on ForeverCore data.
- Leg 0 (`high_priestess_jeklik`, 16 pull groups plus boss `14517`) provides a complete self-contained entrance-to-boss
  path to test ForeverCore spawn matching, formation grouping, navmesh scouting and driver dry-run execution.
- `dungeons/zul_gurub.json` stays `todo (2026-11-26 offline-checked ...)`: leg 0 passes offline checks, legs 1–8 have
  not been remapped yet, and raid rows are not exported to SQL (`plan-export --forever` refuses `kind: "raid"`, `raid`,
  `phase_entries` and `summon_open_step`); a player-led raid run reads the plan JSON directly.

## Tooling and Driver Fixes

1. **`planscout` boss-approach `insert_via` (`internal/planscout/apply.go`)**: `insertVias` now handles `part: "approach"`
   with `group < 0` by editing `leg.Boss.Approach` instead of indexing `leg.PullGroups[-1]`.
2. **`plan-scout --forever --apply-fixes` navmesh check (`cmd/forever-agent/plan_scout_cmd.go`)**: added a Forever
   check-navmesh adapter implementing `Plan`, `Raycast`, `Nearest` and `SetAvoid` (without 1.12 `ServerQuerier`), so
   post-edit re-checking on ForeverCore mmaps no longer aborts on `ServerPath`.
3. **Post-apply plan validation (`internal/planscout/apply.go`, `cmd/forever-agent/plan_scout_cmd.go`)**: `ApplyFixes`
   validates each applied fix through `dungeon.Marshal` + `dungeon.Parse`, drops fixes that break plan invariants (such
   as exceeding 4 approach points or exceeding the 60 yd stand-to-pull leader sight cap), records them in
   `dropped.json` and reports each dropped fix to stderr and report notes.
4. **Per-server `alt_entries` on `PullGroup` (`internal/dungeon`, `internal/planexport`, `internal/forever/driver`,
   `cmd/forever-agent`)**: `pull_groups[].alt_entries` (`map[string]map[uint32]uint32`) substitutes server-specific
   creature entries via `PullGroup.EntriesFor(server)` in ForeverCore planning, export and driver routes while keeping
   Turtle `entries` unchanged.
5. **ForeverCore idle formation flags (`internal/worldspawn/foreverdb.go`)**: mapped `creature_formations` `groupAI &
   0x200` (idle follow/formation behaviour) to `FormationMove | AggroTogether` (`0x3`). Re-checking all 26 non-`todo`
   classic dungeon plans on ForeverCore data reduced `dire_maul_east` errors from `67` to `66` (`joiners` `58 -> 57`),
   with `delta 0` on the other 25 plans.
6. **Raid driver partial-run support (`internal/forever/driver/plan.go`, `cmd/forever-agent/forever_play.go`)**:
   - Raid mode accepts pull-group `stand_pull` / `clear_yd` (noted as plain pull) and `max_bodies` (noted as unread
     without enforcement).
   - Added `--last-leg <name>` (`LoadPlayRaidRouteUntil`) to stop at a named boss leg while keeping later legs loaded
     for unpulled-pack awareness.

## Zul'Gurub Leg 0 Plan Edits

- **PG 0 `alt_entries`**: added `"alt_entries": {"forever": {"11372": 11371}}` because ForeverCore pools those two spots
  with Razzashi Serpent `11371`; PG 3 and PG 4 already match `11372` within 0.00 yd and needed no override.
- **PG 10, PG 11, PG 12 radii**: widened `4.6 -> 7`, `1 -> 4`, `5.5 -> 9` to match ForeverCore spawn offsets.
- **Scout coordinate fixes**:
  - Applied `pull_groups[0].approach[0]` from the scout patch (moving clear of the entry pack), clearing step 3's
    aggro failure without regressing Turtle lints.
  - Kept `pull_groups[2].approach[0]` unchanged because the scout candidate sits on `NavWater`, trading a 1-body aggro
    for a water failure.
  - Kept `pull_groups[13].approach[0]` and `boss.approach[1]` unchanged because the scout candidates moved out of
    patrol-pull shot range (`TestZgW38JeklikPatrolStands`) and out of Jeklik reach-pull range
    (`TestZgJeklikBossApproachInPullRange`), whereas both original points only clip a single overhead Bloodseeker Bat.
  - Fixes `id` 3, 9, 11, 14 and 15 were dropped by A3 validation (`> 4` approach points or `63.2 yd > 60 yd` sight).

## Verification Gate (Before vs After)

Commands executed offline with ForeverCore map `309` exports and mmaps alongside TortoiseBots map `309` mmaps:

- `forever-agent plan-check --dir dungeons --mmaps <turtle-mmaps> --spawns tools/dungeongen/testdata/map309 --json zul_gurub`
- `forever-agent plan-scout zul_gurub --forever --dir dungeons --mmaps <forever-mmaps> --spawns <forever-spawns> --party-level 60 --until high_priestess_jeklik`
- `forever-agent plan-check --forever --dir dungeons --mmaps <forever-mmaps> --spawns <forever-spawns> --json zul_gurub`
- `forever-agent plan-export --forever --dir dungeons --report zul_gurub`
- `forever-agent forever dungeon-play --dry --character Leader --plan dungeons/zul_gurub.json --last-leg high_priestess_jeklik ...`

| Check | Before | After | Gate |
| :--- | :--- | :--- | :--- |
| Turtle `plan-check` | `exit=11`, `849` pts, `0` failed, `853` lints, `162` errs | `exit=11`, `849` pts, `0` failed, `853` lints, `161` errs | Pass (`<= before`) |
| Forever `plan-scout` (leg 0) | `54` steps: `12 ok, 23 warn, 19 fail` | `54` steps: `13 ok, 24 warn, 17 fail` (`step 3` cleared) | Pass (only `joiners`, 1-body `aggro`, step-17 cut row) |
| Forever `plan-check` leg 0 | `50 warn, 19 err`, `0` failed pts | `52 warn, 17 err`, `0` failed pts | Pass (`0` failed pts, allowed err kinds only) |
| Forever `plan-check` legs 1–8 | `125 err`, `3` failed pts | `694` lints (`569 warn, 125 err`), `3` failed pts | Recorded in `forever_status` |
| `plan-export --forever --report` | Refused (`kind; raid; phase_entries; summon_open_step`) | Refused (same fields; `alt_entries` accepted) | Pass (player-led reads JSON) |
| `forever dungeon-play --dry --last-leg high_priestess_jeklik` | Not runnable with `--last-leg` / `stand_pull` / `max_bodies` | `exit=0`, `stop raid-boss-reached, 16 pulls, 0 wipes, 0 deaths`, `verdict: PASS` | Pass |

## Remaining Live Gap

Live raid watch and execution remain offline: the server-side C++ dungeon module does not carry raid plans
(`plan-export --forever` refuses raid plans and fields), and live raid-watch command handling is outside the scope of
this offline leg-0 check.
