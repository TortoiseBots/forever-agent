# Joint watch fixes: what the first watched run found

Scope: the first joint live run (agent watching a run a server module leads) found five blockers before any watch
step ran. Each is fixed with a test that fails on the old code. No character, account, host or path of the test
environment is named here; the module is "the server module" throughout.

## 1. Empty optional values in the state record

The command reference marks exactly two state numbers "empty when none": `tgtentry=` and `tgthp=` (the engine's
target; the module writes them empty when it has none). Every other unknown number is an omitted key, so an empty
cursor or counter (`seq=`, `ev=`, `leg=`, `step=`, `last=`, `manapct=`, `corpsemap=`) is a malformed report, never
"not known".

* `internal/forever/command/tbcmd/v2.go:87` — `optionalInt` stays strict: an empty value is `ErrBadValue`.
* `internal/forever/command/tbcmd/v2.go:100` — `optionalIntEmpty` is the allow-empty variant, used only at the two
  documented state keys: the empty value is "not known" (present=false, value 0 is never acted on).
* `internal/forever/command/tbcmd/state.go:284` — `tgtentry` and `tgthp` use it; `manapct` and `corpsemap` stay
  strict.
* `internal/forever/command/tbcmd/state.go:466` — `optionalPos` reads `corpse=` empty as absent, so the
  `corpse=`/`corpsemap=` pair rule holds on presence.
* Tests: `TestStateEmptyTargetNumbers`, `TestStateEmptyNumbersRefused`, `TestStateEmptyTextValues`,
  `TestEmptyCursorsRefused` (tbcmd) — the two target numbers read as absent, a present out-of-range `tgthp=101`
  still refused, empty `manapct=`/`corpsemap=` refused, empty text keys (`dl=`, `dm=`, `ord=`, `lastorder=`) read
  as absent, and empty `seq=`/`ev=`/`leg=`/`step=` refused.

## 2. A bot with the engine off

A bot whose engine is off (`stuck=noengine`, `engine=no`) carries no `co=`/`nc=` keys. The record was refused for
the missing lists and the watch ended `blind` with no reason.

* `internal/forever/command/tbcmd/state.go:56` — `BotState.NoEngine` is the typed state (`engine=no` or
  `stuck=noengine`); `state.go:338` accepts the absent `co=`/`nc=` only there. With the engine on both lists are
  still required.
* `internal/forever/driver/run.go:598` — `ready()` returns `noengine:<member>`, not a map/engine/blank reason.
* `internal/forever/driver/states.go:73` — PREPARE logs each changed not-ready reason (`prepare: not ready: ...`),
  and the refusal at the deadline.
* Tests: `TestStateEngineOff` (tbcmd), `TestWatchEngineOffIsNotReady`, `TestReadyNoEngineReason` (driver) — a member
  with the engine off ends `prepare:noengine` with the logged reason, never `blind`; the same record with
  `engine=yes` is still refused.

## 3. Plans with the module's in-fight run fields

The plan reader refused `kill_first`, `cc_priority`, `cc_always`, `no_cc`, `summoned_kill_first`, `tank_min_hp` and a
boss's `tactics`, so the plan of the run could not be named to the watch at all.

* `internal/forever/driver/plan.go:157` — `NewRoute` no longer refuses those fields: they are part of the documented
  plan format and belong to the run's fight, which the module's bot runs. The plan loader already validates them
  (ranges, `kill_first` against `cc_priority`, the summoned subset), so nothing is accepted unvalidated. The fields
  the client's own machine would silently drop (steps, holds, ambushes, optional legs, skip rules) stay refused.
* Tests: `TestPlanFiles` (driver) — `dungeons/ragefire_chasm.json` loads unchanged with 9 legs, and the run-field
  refusals are gone from every plan's table; `TestRouteRefusals` lists only the fields still refused.

## 4. Every end of the watch logs its reason

A `blind` end could be logged with no reason: the state read that blinded a gate was not printed.

* `internal/forever/driver/run.go:528` — a state read that keeps failing logs its first error and, at the blind
  bound, the error that ended the run.
* `internal/forever/driver/watch.go:667` — the watch's blind rule keeps the last read error and logs the count, the
  span and that error before it stops.
* `internal/forever/driver/watch.go:823` — every watch result logs `watch: end reason=<reason>` (including the
  refusal before the first send).
* Tests: `TestWatchBlindNamesItsReason`, `TestWatchConfigRefusalIsLogged`, plus the end-reason assertions in
  `TestWatchObserverOutsideTheInstance` and `TestWatchEngineOffIsNotReady`.

## The commander: an outside observer, the module's bot in charge

The character the agent runs is only an outside GM observer: it is never in the party and never its commander. The
party keeps the module's own bot commander, and its tank leads.

* `internal/forever/driver/states.go:149` — `adoptRoster` builds the party from the roster with the tank first and
  accepts the party when the bot the run names is its tank or its leader; it refuses the observer named as
  the commander (`observer-commander`).
* `internal/forever/driver/states.go:384` — the entry check compares the party's map with the plan's (one instance),
  never with the observer's; PREPARE's map rule (`run.go:588`) compares the party with itself, so an observer
  elsewhere does not block it.
* `internal/forever/driver/states.go:409` — the observer teleports to the tank's side after the party is in, on its
  own: a shut gate or a refusal is logged and the watch goes on. A watched party is never ordered to follow the
  observer (`states.go:246`): its master is the module's bot.
* `internal/forever/driver/watch.go:113` — before the watched run starts, the party must stand on the plan's map in
  one instance (`enter:party-map`), which is also the `--enter none` placement check.
* `internal/forever/command/tbcmd/action.go:60` — a setup answer's per-target record is typed: `target=party:<tank>`
  and `target=party:commander:<name>` are the party it resolved, a bare name is the one target, and any other shape
  is refused. The port carries the labels to the driver (`internal/forever/world/dungeon_session.go:154`).
* Tests: `TestActionPartyTargets` (tbcmd) — the answer with the `party:commander:<name>` record parses as a success
  with its labels, an acknowledging answer is still whole, malformed labels are refused; `TestWatchObserverOutsideTheInstance`,
  `TestWatchBotCommanderRoster`, `TestWatchRejectsTheObserverAsCommander` (driver).

## Docs

`docs/forever/WATCH-R1-NOTES.md` (the watch's steps, the budgets' blind rule, the tests, the live steps) and the
`README.md` paragraph on `forever dungeon-watch` (the observer, the entry answers, the plans that load).

## Gates

On the code-and-docs commit `7f4a4c66`'s tree with a clean worktree (the notes commit `85a60918` changes no code),
each in the foreground and waited for:

```
make fmt-check            -> clean (no file listed)
make cross                -> CROSS-OK (windows/amd64 and darwin/arm64 builds, then their vet)
vm-test ta ~/wt-fa-joint  -> RESULT: PASS  kind=ta wt=wt-fa-joint sha=85a60918 dirty=0 files=2160
                             rc=0  vm_duration=579s  laptop_wall=680s
staticcheck ./...         -> rc=0 (no finding)
go test -race -count=1 -timeout 20m ./... -> every package ok, no FAIL
```

This notes commit changes no code; it was gated again with the same commands, and the final report carries those
lines.

## Commits

```
fec11857 Forever: read the state records the live module writes
3c4b76c0 Forever: accept the plan format's in-fight run fields
7f4a4c66 Forever: watch the party the module commands
85a60918 Forever: joint watch fix notes
```

## Review round 2

The review of the fixes found three refusals too loose. Each is fixed with a test that fails on the old code.

* **The empty-value allowance was too wide.** Item 1 above: `optionalInt` accepted every empty value as absent,
  which also made an empty `seq=`, `ev=`, `leg=` or `step=` "not known". It is strict again
  (`internal/forever/command/tbcmd/v2.go:91`); `optionalIntEmpty` (`v2.go:104`) is the allow-empty variant and is
  used only at `tgtentry` and `tgthp` (`state.go:286`), the two state numbers the command reference marks "empty
  when none". Tests: `TestEmptyCursorsRefused`, `TestStateEmptyNumbersRefused`, `TestStateEmptyTargetNumbers`.
* **An instance id of 0 counted as an instance.** `partyInInstance`
  (`internal/forever/driver/states.go:393`) now requires a fresh report (`run.go:577`), five members on the plan's
  map, and an instance id that is not 0: map 0 is the open world, never the instance a plan names. Tests:
  `TestWatchZeroInstanceRefused`, `TestPartyInInstanceNeedsAnInstance`.
* **A start answer with no run record counted as a started run.** `watchStart`
  (`internal/forever/driver/watch.go:143`) now needs exactly one run record whose leader and plan are the ones the
  command named and whose state is running; zero or several records, or another leader, plan or state, stop the
  watch with `start:bad-report` instead of logging it started. The typed start answer checks the same
  (`internal/forever/command/tbcmd/run.go:137`). Tests: `TestWatchStartNeedsOneRunRecord`, `TestStartRecord`, and
  the `zero start records`/`other plan`/`not running` rows of `TestRunTargetMismatchAndRefusals`.

## Review round 3

The last review found five more. Each is fixed with a test that fails on the old code; none was rejected.

* **A watched PREPARE measured the party by its master.** `ready()` (`internal/forever/driver/run.go:626`) checked
  every bot's `dm=` (distance to its master) even in a watch, where the master is the module's own bot commander,
  outside the party and possibly outside its instance: `dm=othermap` or a far `dm=` timed PREPARE out. A watch now
  measures the party by `dl=` (the party leader, its tank); a run keeps `dm=` (its commander is in its party). Tests:
  `TestWatchReadyUsesTheLeaderDistance` (an `othermap` master passes; a member 30 yd from the tank is `prepare:range`).
* **The placement trusted a report that failed, and the observer's own poll could undo a verified entry.**
  `partyInInstance` (`states.go:394`) now requires a whole report (`reportComplete`: no read error, counts agree,
  five lines, every party member); `partyPlaced` (`watch.go:148`) polls again a few times before the watch refuses a
  placement, because `observerIn`'s teleport polls the state after ENTER verified the party; and the watch's fight
  gate (`actions.go:427`) refuses `party-map` when the party is no longer in the plan's instance, so a retried start
  cannot go out on a party that zoned out together. Tests: `TestPartyInInstanceNeedsAWholeReport`,
  `TestWatchObserverPollFailureKeepsTheEntry`, `TestWatchStartRechecksThePlacement`.
* **The run's plan loader had started accepting fight rules it does not run.** `NewRoute` refuses the in-fight run
  fields again (`plan.go:182`, `plan.go:337`) and only the watch opts in: `LoadWatchRoute`/`NewWatchRoute`
  (`plan.go:141`, `plan.go:167`), used by `dungeon-watch` (`cmd/forever-agent/forever_watch.go:76`). `dungeon-run`
  keeps `LoadRoute` and refuses such a plan as before. Tests: the in-fight rows of `TestRouteRefusals`,
  `TestWatchRouteAcceptsTheFightFields`, and the watch-loads/run-refuses pair in `TestPlanFiles`.
* **Setup answers read continuation lines as extra targets, and bare `target=party` as a character.** `parseAction`
  (`internal/forever/command/tbcmd/action.go:137`) merges `<verb>+` lines into the record before them (an orphan or a
  repeated key is `ErrBadReport`), and `actionTarget` (`action.go:67`) refuses `target=party`, the command's keyword.
  Tests: `TestActionContinuationLines`, the `target=party` row of `TestActionPartyTargets`.
* **A watched PREPARE accepted a `--leader` that could not lead the run.** `adoptRoster`
  (`internal/forever/driver/states.go:198`) now requires the watch's leader to be the party's tank: the module's
  start refuses a leader that does not lead its party, so the client caught it at PREPARE instead of sending a start
  the module refuses. Test: `TestWatchLeaderMustBeTheTank`.

## Review round 4

Two findings; both fixed, each with a test that fails on the old code.

* **A `--leader` that holds the party flag was refused.** Round 3's guard required the run's leader to be the party's
  tank; the module starts the run on the bot that leads the party, so a healer or dps that holds the flag is a
  supported `--leader`. `adoptRoster` (`internal/forever/driver/states.go:205`) now refuses only a name that is
  neither the party's tank nor its leader. Test: `TestWatchLeaderMayBeThePartyLeader` (`--leader` and the roster's
  leader both name the healer; the run starts on it). The scripted module (`fake.go:690`) starts a run on the party's
  leader as well as the tank, so the test reaches `done`.
* **The roster's summary leader was not reconciled with the member flags.** A malformed, partial or version-skewed
  report could name one leader in `party-end leader=` while another member line carried `leader=yes`, and a
  `--leader` the party does not follow passed PREPARE: the entry and its teleports ran before `run start` was
  refused. `adoptRoster` (`states.go:201`) now requires exactly one member flag, its name being the summary's
  leader and one of the five adopted members. Test: `TestWatchLeaderFlagsMustMatchTheSummary` (a second flag, no
  flag, and another member's flag) ends `prepare:leader` with no `enter party` and no `run start`; the fake's new
  `OnParty` hook supplies the report.
