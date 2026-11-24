# PROOF-3 notes: the pull from the stand, the whole plan for a cut run, and dungeon-watch --attach

Spec: `SPEC-forever-agent-proof-3.md`. Milestone: #88, proof-3. No host, account, private path, capture name or
byte is in this change or its tests.

## Part A: the pack pull is made from the plan's stand, with the party beside the agent

- `pull()` (pack pulls only, never the boss pull) now calls `pullApproach` before its first `engage`:
  - the agent walks to `Group.Stand(p.self)` (the group's last approach point, else the driver's default standoff)
    unless it already stands within `PlayArriveYd`; `pullWalk` is set for that walk, and `walkSegments` skips
    `packInWay` and `patrolHold` while it is set, so the walk to the stand never starts a nested pull;
  - `pull_stand` {group, x, y, z, yd} is recorded. **Decision:** x/y/z are the stand point the walk was given and
    `yd` is the stand's distance to the group's centre (the pull's reach: RFC's road packs stand ~21 yd off), not the
    distance the agent walked; the trace's own walk frames already carry that.
  - then `waitNear(PlayPullGatherYd = 10, FollowAt, SummonAt, StopAt)`: the marks and `.bot attack` wait for the tank.
- A first attempt with no aggro (`engage` false, no stop) steps closer before the retry: `pullStep` walks to the point
  `PlayPullStepYd = 5` yd along the line to the skull, never nearer than `PlayPullMinYd = 10` yd to it, records
  `pull_step` and gathers the party again. An agent already inside 10 yd does not move. The loop still ends as
  `pull-failed` after two unanswered attempts. `pullWalk` is set for the step walk too: a pull is in flight, so a
  pack met on the few yards of the step must not overwrite the pull.
- Before the first order the agent checks what the module will check for the tank (`closeToSee`): the module's rti
  mark is found only within 60 yd and in line of sight of the TANK's own position (`RtiTargetValue.h:89,92`,
  `PossibleAttackTargetsValue.cpp:387`; a miss whispers "I dont see my rti attack target", `AttackAction.cpp:163`, and
  the tank never moves). The tank's position is its live unit in the view (the roster's row on the run's map stands in
  for one the view lacks), the line is the navmesh walkability ray (the `Sight` port; none or unknown leaves the
  judging to the module). A skull the tank cannot see makes the agent walk to the nearest point of the navmesh path
  to the skull whose ray is clear (`sightStand`, sampled every `PlaySightStepYd` = 2 yd and never nearer than
  `PlaySightTouchYd` = 5 yd) — a straight line toward the skull can keep the party on a ledge whose lip hides it
  (the trogg cave's shelf, 2026-11-23: four straight steps to 9.7 yd and still no sight, the skull 4 yd below on the
  lower floor) — or, when its own stand already has the line and the tank has not yet come beside it, wait for the
  tank there. It then waits up to `PlayTankWait` = 15 s for the tank to come within `PlayTankBesideYd` = 5 yd of
  that stand and looks again from where the tank then stands — the module reads the line from the tank, not the
  agent. With no path or ray answer it steps toward the skull like `pullStep` (5 yd, never nearer than
  `PlayPullMinYd` = 10 yd). At most `PlayCloseTries` = 4 steps and `PlayCloseTime` = 90 s from the first look;
  `pull_sight` {group, seen, known, try, yd} is recorded at each look. Spent, the order is made anyway and an
  unanswered pull stops `pull-no-sight` (not `pull-failed`). The boss pull does the same. Never a GM line.
- Walk stops (avoid, walk, time) end the pull exactly as they end a walk.

## Part B: `--last-leg` keeps the whole plan and ends the run at a leg

- `forever dungeon-play --last-leg <leg name>` → `driver.PlayConfig.LastLeg`.
- The name is checked before anything is sent: the command answers exit 2 and lists the plan's legs; the machine
  (`Play`) resolves it before its first state and stops `last-leg` with no line out (a plan-driven caller's guard).
- `legWalk` goes to `StateDone` after the last leg's boss (or after the leg, when it has none); `done()` counts only
  the bosses of the legs up to and including that one. `knownEntries`, `strangerNear`, `packInWay` and `AllGroups`
  keep reading the full route: a later leg's unit met on the way is the plan's own (pulled when a pack claims it, not
  a `patrol_hold` stranger).
- `last_leg` is in the run summary (already a field) and now also in the report card (`last_leg`, omitted when empty).
  Without the flag nothing changes: every leg runs and every boss is the clear's.

## Part C: `dungeon-watch --attach`

- Flags `--attach` (bool) and `--attach-wait` (duration, default `300s`) → `driver.Config.Attach` / `AttachWait`.
- With `--attach` the watch runs no entry at all: the party is already in and another client commands it, so no GM
  line, no move and no PREPARE, TRAVEL or ENTER step goes out (the CLI's default `--enter tele` never fires). The
  roster is still read, by PREPARE's own rules, for the party's five names, their roles and the commander
  (`adoptWatchParty`, the read-only half of PREPARE). The same-map check (`enter:party-map`) is skipped too: the
  attach checks the run itself instead.
- With `--attach`, `watchStart` sends no `.tb run start`: `watchAttach` polls `.tb run status <leader>` at `cfg.Poll`
  until exactly one run of the leader is `running` (a `paused` one is attached too, with its state applied), records
  `run_start` with `"attached": true`, applies the record, copies `legSeen`/`FirstS` from `HasLeg` and `lastSeq` from
  `HasEv`, and logs `watch: attached state=<state>`. On timeout: `start:attach-timeout`.
- The attached run's plan must be the watch's own (`--plan`): a run of another plan is refused before anything is
  applied, with both plans named (`watch: attach refused: the leader's run is plan %q, not --plan %q; …`) and the
  stop `start:attach-plan`.
- Without `--attach` the start path is unchanged; a `foreign-bot` refusal now also logs
  `watch: start refused foreign-bot; a run started elsewhere can be watched with --attach`.
- The fake gained `StartForeignRun(state)` (a run as another client's start would leave it) and an `OnStatus` hook
  (per status read), so the attach cases do not need a second client.

## Part D (2026-11-24): the patrol hold pulls its unit or goes on

Spec: the lead's decision on the 2026-11-23 milestone recheck (recheck 2, row 1 attempt 2: the RFC player run cleared
every pull and stopped `patrol` at the oggleflint leg). The run's own trace settles the cause:

- `patrolHold` (play.go) held the walk on a live hostile it treated as a patrol that would walk off: any live hostile
  within `PlayPatrolYd` (20 yd) whose entry no leg's pack, patrol, boss or boss add names and that no pack claims.
- The unit it waited on was the same spawn at one point on the ramp up to Oggleflint, 15 yd below the boss, through
  all 544 frames of the run (17.5 min), at full health, targeting nobody: a static spawn, never a patrol. The trace's
  `patrol_hold` event carried only the leg, so the run could not name its unit.
- The hold's budget ran out and `p.stop("patrol")` ended the run before the boss's pull.
- The RFC plan does not name the boss's two arranged guards (`boss.first` is empty in `dungeons/ragefire_chasm.json`),
  which is how an unknown hostile came to stand beside the boss's own approach.

`patrolHold` now:

- records the unit it waits on (`patrol_hold` {leg, guid, entry, x, y, z, yd}) and its end (`patrol_left` {guid, moved,
  seconds});
- classifies by movement: a unit that has not left a `PlayStillYd` (1 yd) circle in `PlayStillAfter` (20 s) is not a
  patrol that will walk off, so the hold ends there instead of waiting its 120 s for nothing (a unit that moves but
  never leaves is still waited for the whole budget);
- runs only on the plan's map (as `packInWay` does): a hostile met on the walk to the portal is the open world's,
  not the hold's;
- ends by `endHold`: a live hostile the navmesh reaches from where the agent stands is pulled as its own pull
  (`pullStranger`: the plan's sight stand via `closeToSee`, the skull mark, the tank's order, one step nearer and a
  second order — the pack pull's own bounds, and `pull-failed`/`pull-no-sight` after both), and the leg goes on to its
  boss; anything else (dead, friendly, unreachable, gone, or pulled `maxStrangerPulls` = 2 times already) is left
  behind with `patrol_skip` {guid, entry, moved, reason, x, y, z} and the walk continues. Nothing here stops the run
  but the pull's own failure rules.
- a stranger pull that ends the fight without the unit confirmed dead does not mark any plan group fought (`pulled`
  only for a pack id); the run summary/record's pull carries `group: -1`, `boss: false`.

## Tests

- `internal/forever/driver/play_fight_test.go`: `TestPullWalksToTheStandFirst`, `TestPullWaitsForTheTankAtTheStand`,
  `TestPullRetryStepsCloser`, `TestPullStepNeverInsideMinYd`, `TestPullStandWalkNoNestedPull`.
- `internal/forever/driver/play_test.go`: `TestPlayLastLegStopsAfterItsBoss`,
  `TestPlayLastLegKeepsLaterLegsKnown`, `TestPlayLastLegUnknownName`, `TestPlayNoLastLegUnchanged`.
- The patrol hold (Part D), `internal/forever/driver/play_test.go`: `TestPlayPatrolWalksOffTheWay` (a unit that walks
  off during the hold: no pull, no stop — the old behaviour), `TestPlayStationaryStrangerIsPulled` (a still unit is
  pulled after `PlayStillAfter` and killed, the skull marked, then the boss; a unit owned outside the party is pulled
  the same way), `TestPlayUnreachableStrangerIsLeftBehind` (a `RouteNone` path answers `patrol_skip`/unreachable and the
  boss is pulled), `TestPlayMovingStrangerWaitsTheBudget` (a unit that keeps moving is waited for the whole budget and
  then pulled), `TestPlayStrangerPulledTwiceIsLeftBehind` (an evading unit is pulled at most twice; the third hold
  records `pulled-twice` and the boss is pulled), `TestPlayStrangerPullFailureStops` (two orders no one answers end the
  run as `pull-failed`), `TestPlayStrangerOutsideTheInstanceIsNotHeld` (a hostile beside the portal walk on the open
  map is neither waited for nor pulled). `TestPlayStrangerStillStops` is gone: its pinned behaviour (a stranger stops
  the run) is the defect.
- `cmd/forever-agent/forever_play_test.go`: `TestPlayCmdLastLegChecksTheName` (the exit-2 usage error lists the legs),
  `TestPlayCmdLastLegDry` (a dry run cut at a leg clears).
- `internal/forever/driver/watch_test.go`: `TestWatchAttachSendsNoStart`, `TestWatchAttachWaitsForRunningRun`,
  `TestWatchAttachPausedRun`, `TestWatchAttachTimeout`, `TestWatchAttachRunsNoEntry`,
  `TestWatchAttachRefusesAnotherPlan`, `TestWatchForeignBotWithoutAttachStops`. The first four are the attach path; the
  two new ones are the review notes (no entry step, the plan check), and `TestWatchAttachRunsNoEntry` carries a
  control watch without `--attach` that does send its enter action.

## Mutations (each must fail the named test)

| Mutation | Test | Result |
|---|---|---|
| no stand walk (`if false` around the walk to the stand) | TestPullWalksToTheStandFirst | caught |
| gather at 35 yd | TestPullWaitsForTheTankAtTheStand | caught |
| no step clamp (`step := min(PlayPullStepYd, d)`) | TestPullStepNeverInsideMinYd | caught |
| `packInWay` not skipped during the pull walk | TestPullStandWalkNoNestedPull | caught (the test also checks the pack met on the way is fought at its own pull; with the mutation the interrupted pull marks it fought) |
| `knownEntries` from the cut legs only | TestPlayLastLegKeepsLaterLegsKnown | caught |
| `--attach` still sends start | TestWatchAttachSendsNoStart | caught |
| entry still runs on attach (`if false && r.cfg.Attach` in `Watch`) | TestWatchAttachRunsNoEntry | caught |
| no plan check on attach (the `st.Plan != r.route.Name` block removed) | TestWatchAttachRefusesAnotherPlan | caught |
| the hold still stops the run at its budget (`p.stop("patrol")`) | TestPlayMovingStrangerWaitsTheBudget | caught |
| no movement test (the `PlayStillAfter` exit removed) | TestPlayStationaryStrangerIsPulled | caught (the pull's timing moves to the budget) |
| no reachability check (`case !p.reachable` removed) | TestPlayUnreachableStrangerIsLeftBehind | caught |
| no pull bound (`case p.strangerPulls >= maxStrangerPulls` removed) | TestPlayStrangerPulledTwiceIsLeftBehind | caught |
| the stranger is never ordered (the engage loop removed) | TestPlayStationaryStrangerIsPulled | caught |
| the hold runs outside the instance (the map guard removed) | TestPlayStrangerOutsideTheInstanceIsNotHeld | caught |
| the hold's deadline still stops (`p.stop("patrol")` at the budget instead of `endHold`) | TestPlayMovingStrangerWaitsTheBudget | caught (the four still/evading cases leave on the 20 s movement test and never reach the deadline, so the moving case is the one that gates it) |

## Gates


- Full suite on the clean committed tree: `vm-test ta ~/wt-proof3` —
  `RESULT: PASS  kind=ta wt=wt-proof3 sha=38290af3 dirty=0 files=2757 runner=yulon-ubuntu2` (rc=0, 834 s; the run
  is `go test -count=1 -timeout 20m ./... && go vet ./...`).
- Round 2, rebased onto `origin/main` (e281d846) with the two review notes fixed: the PR's gate command,
  `vm-test ta ~/wt-proof3 -- 'go test -timeout 20m ./internal/forever/... ./cmd/... && go vet ./...'` —
  `RESULT: PASS  kind=ta wt=wt-proof3 sha=a7f6a06c dirty=0 files=2793 runner=yulon-ubuntu2` (rc=0, 872 s; the same
  command also passed at 294e5c13, 1124 s, before the two `gofmt`-only amend commits). Each new test's mutation was
  re-run on its own (mutation 1: rc=1, `TestWatchAttachRunsNoEntry` FAIL, 23 s; mutation 2: rc=1,
  `TestWatchAttachRefusesAnotherPlan` FAIL, 8 s).
- Dry smoke: `forever dungeon-play --dry --plan dungeons/ragefire_chasm.json --bots A,B,C,D --last-leg oggleflint`
  ends `play: stop clear ... clear=true` with one BOSS state (the legs after oggleflint are not walked). With a
  recording (`--out DIR --min-active 0`) the same run prints `card: CLEARED` and `verdict: PASS`, and the summary
  carries `"last_leg": "oggleflint"`, `"clear": true`. (A `--dry` run without `--out` has no record, so `playVerdict`
  answers `no-play-record` and the command exits non-zero — its long-standing behaviour, not this change.)

### Part D (round 3)

- Full suite on the clean committed tree 4c0ee21c: `vm-test ta ~/wt-fa-88` with the PR's gate command (`gofmt -l
  internal cmd`, `go vet ./...`, `staticcheck ./...`, `go test -count=1 -timeout 20m ./...`) —
  `RESULT: PASS  kind=ta wt=wt-fa-88 sha=4c0ee21c3 dirty=0 files=2894 runner=yulon-ubuntu2` (rc=0, 1168 s). The seven
  patrol-hold tests with the `internal/forever/bnet` hygiene test on the same tree: `RESULT: PASS ... sha=4c0ee21c3
  dirty=0 ...` (233 s). CI on the PR: lint, the nine race shards, `test` and the navmesh jobs all SUCCESS. Before the
  fix the same tests were red: `TestPlayStationaryStrangerIsPulled`, `TestPlayUnreachableStrangerIsLeftBehind` and
  `TestPlayStrangerPullFailureStops` each stopped `patrol`.
- Dry smoke: `forever dungeon-play --dry --plan dungeons/ragefire_chasm.json --bots A,B,C,D --character <name>
  --last-leg oggleflint` ends `play: stop clear, 14 pulls, 0 wipes, 0 deaths, 2.5 min, gm_lines=0, clear=true`, the
  states end `LEG_WALK BOSS PULL FIGHT LOOT REST LEG_WALK DONE LEAVE STOP`, and the card's verdict is PASS.
