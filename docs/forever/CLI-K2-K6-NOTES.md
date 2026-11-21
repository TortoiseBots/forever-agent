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
flag package reads the turtle pass's `--live <server>` / `--slot N`. The pass opens the slot with
`serveclient.Open(dir)` and, in order:

1. waits for `status` state `online` (60 s);
2. sends `.gm on` and `.instance unbind all` (`.instance unbind all` resets the session player's own instance locks:
   `ForeverCore cs_instance.cpp:93-111`);
3. per stop group (`planscout.LiveStops(planscout.LiveSpots(...))`): `.go xyz X Y Z MAP`, a status wait within 10 yd,
   then `units {within:40}`; every unit is converted to `world.Unit`;
4. per primary hop: `path_to` (recorded as `live_path`), and for the risky hops (`climb_anchor`, `water`, `arrival`,
   `drop`, a failed live path: the turtle walk list) a `walk_path` while the `--walk-budget` lasts; a hop past the
   budget is `walk budget`;
5. returns `planscout.LiveData`; exit codes stay 0 done, 28 aborted part-way, 18 phase refused.

Every GM line goes through `scoutAllowedLine` first; the allow-list gains exactly `.instance unbind all` and keeps
refusing `.npc delete`, `.npc add`, `.die`, a `.go xyz` on another map and `.instance unbind <other map>`
(`TestScoutForeverAllowlist`).

### Levels from the live units read

`LoadForeverDB` has already replaced an empty `level_max` with 60 when the draft is built, so the unknown set is read
back from `creatures.tsv` (`foreverUnknownLevels`). After the live pass, `scoutForeverFillLevels` gives every unknown
spawn whose entry a unit names that unit's level, and the offline report is rebuilt (`offline()`) before
`planscout.MergeLive`, so aggro and joiner numbers use the live level; the report notes `live levels: N spawn(s) ...`.
`planscout` itself is unchanged.

### serveclient additions

`Path` (serve's `ServePath`), `PathTo` and `WalkPath`; `walk_path` waits up to 5 minutes for the walk to finish. The
daemon's unit JSON carries no hostility, so a unit's hostiles come from the export spawn `liveAggroCheck` joins it to
(the `serveclient` type stays field-for-field equal to `ServeUnit`, pinned by `TestServeClientTypesMatchTheDaemon`).

## 3. Tests

- `TestScoutForeverLevelsFromLiveUnits` (the round's failing case): fixture export map 700, spawn 101 has no level
  offline (60 assumed) and lives at `0x...65`; a fake serve says level 30. The stand's `live_aggro` reach is 5 yd
  (level 30), and with the fill disabled the test fails with `live reach 20.0, want 5 (level 30 from units); 20 means
  the pass kept the assumed 60`.
- `TestScoutForeverAllowlist`: `.gm on`, `.instance unbind all`, `.go xyz ... 700` allowed; `.npc delete`,
  `.npc add`, `.die`, another map's `.go xyz` and `.instance unbind` refused.
- `TestScoutForeverDraftPassesPlanCheck`: `plan-scout --forever` on a flat v16 fixture exits 0 and the same plan passes
  `plan-check --forever` (exit 0).
- `TestForeverParents`: `--parent` parsing and refusals.

## 4. Live checks for the lead's live queue (not run here)

On a running `forever serve` slot for a real ForeverCore test dungeon:

1. `plan-scout <dungeon> --forever --spawns <export> --mmaps <v16 mmaps> --out <dir>`: exit 0, report's counts, and
   `plan-check --forever <dungeon> --mmaps <v16 mmaps> --spawns <spawns TSV>` exit 0 (this command was broken on main).
2. `plan-scout <dungeon> --forever ... --live --slot <slot dir>`: record the `status` state at start, the two phase
   lines, the stops' `.go xyz` landings (status x/y), the units read per stop, how many spawns `live levels` filled,
   `path_to`/`walk_path` counts and the walk budget left. Then `teardown <slot dir>` when W2 is merged.
3. Confirm the allow-list never sent a line outside the list (the slot's log) and no `.tb`/toolkit line went out.

## 5. Gates (on the final commit)

- `make fmt-check`: clean (exit 0).
- `make cross`:
- `staticcheck ./...`:
- `go test -race ./cmd/forever-agent ./internal/forever/serveclient`:
- `vm-test ta ~/wt-fa-cli`:
