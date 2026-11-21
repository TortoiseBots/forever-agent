# CLI rounds K2-K6: what ran, what was skipped

Round CLI-K2-K6 of `FOREVER-AGENT-REST-SPEC.md` (sections 2, 3 K2-K6, 6). Base: `origin/main` 9f72f496.
Round K4 ran; K2, K3, K5 and K6 are skipped because a dependency they name is not on `origin/main` (never stubbed).

## 1. Dependency check (before any work)

`git log origin/main --oneline -1` = `9f72f496 Forever: a typed client for the session daemon (#47)`.

| Round | Needs | On `origin/main`? | Evidence |
|---|---|---|---|
| K2 | W2 (serve teardown + capture), L1 | **W2 no**, L1 yes | `internal/forever/world/serve_session_methods.go` `notPortedMethods` still lists `"teardown"`; `internal/forever/world/serve.go`'s `ServeConfig` has no `CaptureDir`. The W2 commits exist only on another branch (not merged). |
| K3 | W3 (`event_log`, `act`), L2 | **W3 no, L2 no** | No `event_log` method or `internal/forever/world/eventlog.go` on `origin/main`; `internal/scenario` has no `"server"` file key or Forever mode. |
| K4 | L1, L3 | **yes** | `internal/forever/serveclient/` (#47) and `internal/worldspawn/foreverdb.go` (#45). |
| K5 | L4, L5 | **L5 no** (L4 yes) | `internal/db2/` and `internal/minimap/forever.go` are on `origin/main` (#46); `internal/dashboard/` has no `forever_card.go`/`forever_poll.go`. |
| K6 | module decision-capture API | **blocked** | The brief: skip unless the spawn message says the module landed its `/api/v1/decisions` API; it did not. |

K2, K3, K5 and K6 are therefore skipped with no code change. The one runnable round, K4, is below.

## 2. K4: plan-scout on Forever

Files: `cmd/forever-agent/plan_scout_cmd.go`, new `cmd/forever-agent/plan_scout_forever.go` (new test beside it),
`cmd/forever-agent/plan_scout_allow.go`, `cmd/forever-agent/plan_check_cmd.go` (the one fix the draft's acceptance
needed), `internal/forever/serveclient/serveclient.go`.

### Offline draft

`plan-scout <dungeon> --forever --spawns DIR --mmaps DIR [--parent MAP=PARENT]`:

- opens the v16 planner (`navmesh.NewForeverPlanner(mmaps, parents)`); `--parent` is repeatable `MAP=PARENT`, the
  server's parent-map rule (`MMapManager.cpp:353-357`, already modelled by the planner);
- loads the world with `worldspawn.LoadForeverDB(spawns, map)` (levels in the core's order, 60 + note when unknown);
- keeps `planscout.Offline`, the lints, the fix file, `--apply-fixes`, `--sql` and the exit codes unchanged;
- probes the mesh with `Plan` instead of the 1.12-only `ServerPath` (a v16 tile set refuses `ServerPath` by design);
- the report passes `plan-check --forever` (tested).

### plan-check --forever (the fix the acceptance needed)

`plan-check --forever` passed a `*navmesh.Planner` to `dungeon.CheckPlanWith`, which type-asserts the optional
`ServerQuerier` and asks `ServerPath`; a v16 planner refuses that query (`navmesh: ServerPath replays the 1.12 core's
own query`), so **every** `plan-check --forever` run with a plan exited 2 before this round. The check now passes a
`foreverNav` wrapper that offers `Plan`, `Raycast`, `Nearest` and `SetAvoid` only: the v16 planner plans each hop and
the 1.12 run-leader server walk is skipped, as the path lints already did (they drop a `ServerPath` error). Bug fix.

### Live pass (`--live --slot DIR`)

For the Forever pass, `--live` is a bool and `--slot` is a serve dir, so `foreverLiveArgs` splits them out before the
flag package reads the turtle pass's `--live <server>` / `--slot N`. The pass reads the slot's own `slot.json`
(`slotdir.Read`/`Check`), opens the slot with `serveclient.Open(dir)` and, in order:

1. waits for `status` state `online` (60 s), then requires the session's `status.character` to equal the character
   `slot.json` names (case-insensitive), so a mistaken `--slot` cannot enable GM mode, unbind or teleport another
   character; a manifest that names no character is refused;
2. sends `.gm on` and `.instance unbind all` (`.instance unbind all` resets the session player's own instance locks:
   `ForeverCore cs_instance.cpp:93-111`) and checks each answer: the core's success line (`LANG_GM_ON` "GM mode is ON",
   `LANG_COMMAND_INST_UNBIND_UNBOUND` "Unbound N instance(s).") and its error lines (`LANG_INVALID_TARGET_COORD`, the
   trinity syntax/unknown-command refusals); a refusal stops the phase with the server's own words;
3. per stop group (`planscout.LiveStops(planscout.LiveSpots(...))`): `.go xyz X Y Z MAP`, a status wait for that map
   and within 10 yd, then `units {within:40}`; every unit is converted to `world.Unit` and each spot of the group gets
   its own copy of the slice; the report notes `live stops: N of M read`;
4. per primary hop: `.go xyz` to the hop's own origin row (`hop.From`) first, because the server plans from the
   session's position; then `path_to` (recorded as `live_path`), and for the risky hops (`climb_anchor`, `water`,
   `arrival`, `drop`, a live path that is not ok: the turtle walk list) a `walk_path` whose context deadline is the
   walk budget's end, so the budget is spent once however many walks are attempted; under 2 s of budget left no walk
   starts and the hop is `walk budget`; a walk RPC error is recorded as the hop's skipped row with the error (never a
   measured stop) and aborts the pass (28); a refused route (status partial/none, no error) still records where the
   session stopped;
5. returns `planscout.LiveData`; exit codes stay 0 done, 28 aborted part-way, 18 phase refused.

`--max-minutes` is the pass's overall ceiling: the pass runs under `context.WithDeadline`, so every online wait, units
read, path query and walk inherits it and the pass aborts (28) at the ceiling with the reason in the report note.

Every GM line goes through the allow-list first. `scoutCanonicalGM` parses it into tokens with a strict grammar and
returns the canonical text that is sent: single ASCII spaces (the line must equal `strings.Join(strings.Fields(line),
" ")`), numbers matching `^-?(0|[1-9][0-9]*)(\.[0-9]+)?$` and finite within ±10 000 000 (no exponent, hex, `+`, `_`,
`.5`, `inf`/`nan`, no leading zero), map ids with the same rule. The map must be the plan's or the portal's, and map 0
is allowed only when the plan itself is on map 0. `.instance unbind` takes only the plan's map or `all`. Everything
else is refused: `.npc delete`, `.npc add`, `.die`, another map's `.go xyz`, a `.bot` form with an extra field
(`TestScoutForeverAllowlist`).

### Levels from the live units read

`LoadForeverDB` has already replaced an empty `level_max` with 60 when the draft is built, so the unknown set is read
back from `creatures.tsv` (`foreverUnknownLevels`). After the live pass, `scoutForeverFillLevels` gives an unknown
spawn the level of the one live unit it can stand for: type `creature`, the spawn's low-24-bit guid **and** the spawn's
entry. The old entry fallback is gone because the core rolls `level_min..level_max` per spawn, so one unit's level
says nothing about its siblings; a player or pet whose guid collides is not trusted. The offline report is rebuilt
(`offline()`) before `planscout.MergeLive`, so aggro and joiner numbers use the live level; the report notes
`live levels: N spawn(s) ...`. `planscout` itself is unchanged.

### serveclient additions

`Path` (serve's `ServePath`), `PathTo` and `WalkPath`; `walk_path` waits up to 5 minutes for the walk to finish. The
daemon's unit JSON carries no hostility, so a unit's hostiles come from the export spawn `liveAggroCheck` joins it to
(the `serveclient` type stays field-for-field equal to `ServeUnit`, pinned by `TestServeClientTypesMatchTheDaemon`).

## 3. Tests

- `TestScoutForeverLevelsFromLiveUnits`: fixture export map 700, spawn 101 has no level offline (60 assumed) and
  lives at `0x...65`; the fake serve names a player on the same guid (level 60), a creature of another entry (60) and
  the spawn's own creature (30). The stand's `live_aggro` reach is 5 yd (level 30); the old by-guid fill made it 20.
- `TestScoutForeverLevelFillTrustsOnlyTheSpawnsOwnCreature`: spawn 100 (same entry, no live unit) keeps the assumed 60
  (no entry fallback), spawn 101 takes 30 from its own creature.
- `TestScoutForeverPathsStartAtHopOrigins`: every path query starts at the positioned row before its target, never at
  the last stop; every sent GM line is its canonical allow-listed form.
- `TestScoutForeverWalkBudgetIsNotExceeded`: a walk that never answers is cut at the budget's end and only one walk is
  started; every later risky hop is `walk budget`.
- `TestScoutForeverWalkFailureIsAnErrorRow`: a walk RPC error aborts (28) and its `live_walk` row is a skip carrying
  the error, never a measured shortfall.
- `TestScoutForeverGMRefusalStopsThePhase`: a refused `.gm on` is phase-refused (18) with nothing else sent; a refused
  `.go xyz` aborts (28) with the core's line.
- `TestScoutForeverMaxMinutesAborts`: a units read that never answers is cut at the `--max-minutes` ceiling and the
  report records it.
- `TestScoutForeverUnitsAreCopiedPerStep`, `TestScoutForeverSlotIdentityRefused`, `TestScoutForeverSlotManifest`.
- `TestScoutForeverAllowlist`: the canonical forms allowed; map 0, leading zeros, exponents, hex, `inf`/`nan`, `+`,
  `_`, `.5`, extra fields, tabs and doubled/spaced padding refused.
- `TestScoutForeverDraftPassesPlanCheck`: `plan-scout --forever` on a flat v16 fixture exits 0 and the same plan passes
  `plan-check --forever` (exit 0). `TestForeverParents`: `--parent` parsing and refusals.

Each fix was shown failing before: reverting it in turn made its test fail (`path 1 asks from 40, want 10`,
`2 walks started, want 1`, `live reach 20.0, want 5`, `filled 0 spawns, want 1`, `the abort does not name the
ceiling`, the refusal tests, the allow-list grammar test).

## 4. Live checks for the lead's live queue (not run here)

On a running `forever serve` slot for a real ForeverCore test dungeon:

1. `plan-scout <dungeon> --forever --spawns <export> --mmaps <v16 mmaps> --out <dir>`: exit 0, report's counts, and
   `plan-check --forever <dungeon> --mmaps <v16 mmaps> --spawns <spawns TSV>` exit 0 (this command was broken on main).
2. `plan-scout <dungeon> --forever ... --live --slot <slot dir>`: record the `status` state at start, the two phase
   lines, the stops' `.go xyz` landings (status x/y), the units read per stop, how many spawns `live levels` filled,
   `path_to`/`walk_path` counts and the walk budget left. Then `teardown <slot dir>` when W2 is merged.
3. Confirm the allow-list never sent a line outside the list (the slot's log) and no `.tb`/toolkit line went out.

## 5. Gates (on the final code commit)

Hygiene: `git diff origin/main | grep -nE '100\.|192\.168|forever-test|tortoise-test|TortoiseBots-audit|/home/|@gmail'`
printed nothing.

Gate commit: `ca8d0607` (clean tree; before it, `a4cc18f2` failed the VM gate on `TestServeClientTypesMatchTheDaemon`
because this round briefly added `Hostile/Faction/NPCFlags` to `serveclient.Unit`; the type stays field-for-field with
the daemon's `ServeUnit`, and the fields are gone).

- `make fmt-check`: clean (exit 0).
- `make cross`: clean (exit 0).
- `staticcheck ./...`: clean (exit 0).
- `go test -race ./cmd/forever-agent ./internal/forever/serveclient` (on the VM, same tree):
  `ok  	github.com/pjerra/forever-agent/cmd/forever-agent	648.867s` and
  `ok  	github.com/pjerra/forever-agent/internal/forever/serveclient	1.278s`, `VM-TEST-END rc=0 seconds=1937`.
- `vm-test ta ~/wt-fa-cli`: `RESULT: PASS  kind=ta wt=wt-fa-cli sha=ca8d0607 dirty=0 files=2337`, `rc=0  vm_duration=3007s  laptop_wall=3021s`.

## 6. Fix round: the K4 review findings

Two reviews of the K4 branch (`claude-cli-k`, `codex-cli-k`) found the live pass measuring from the wrong origins,
converting walk failures into pass data, ignoring its runtime ceilings, not checking GM replies, a loose allow-list and
an unfiltered level fill. One go fixed all of them; each fix has a test that failed before it (the failures are quoted
in section 3).

| Finding | Fix |
|---|---|
| `path_to`/`walk_path` ran from the last stop | `.go xyz` to `hop.From` (allow-listed, landing checked on the plan's map) before each hop's path query |
| the walk budget could be exceeded by a full walk per hop | each walk runs under `context.WithDeadline(walkEnd)`; under 2 s left no walk starts; the remaining budget is spent once |
| `--max-minutes` parsed but ignored | the whole pass runs under `context.WithDeadline(max-minutes)` and aborts (28) naming the ceiling |
| a failed walk recorded as a measured stop | the walk's error is the hop's skipped row and aborts (28); a completed walk records the status stop |
| GM replies ignored | `foreverGM` checks the core's success line and its error lines; a refusal stops the phase/hop with the server's words |
| map 0 allowed through the portal sentinel, loose matching | map 0 only when the plan is on map 0; strict tokens and canonical re-render before sending |
| the level fill trusted any unit and fell back to the entry | only `creature` with the spawn's low-24-bit guid and entry; no entry fallback |
| every step shared one units slice | each spot gets its own copy |
| the report mixed partial levels after an abort | the report notes `live stops: N of M read` |
| the slot's identity was never checked (codex) | `slot.json`'s character must equal the session's `status.character` before any GM line |
| tests mirrored the code | fakes track the session position, fail, refuse and block; the canonical sent lines are asserted |
