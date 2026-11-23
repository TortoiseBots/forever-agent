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

## Tests

- `internal/forever/driver/play_fight_test.go`: `TestPullWalksToTheStandFirst`, `TestPullWaitsForTheTankAtTheStand`,
  `TestPullRetryStepsCloser`, `TestPullStepNeverInsideMinYd`, `TestPullStandWalkNoNestedPull`.
- `internal/forever/driver/play_test.go`: `TestPlayLastLegStopsAfterItsBoss`,
  `TestPlayLastLegKeepsLaterLegsKnown`, `TestPlayLastLegUnknownName`, `TestPlayNoLastLegUnchanged`.
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
