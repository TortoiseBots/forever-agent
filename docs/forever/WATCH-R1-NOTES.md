# The run supervisor, round S7: watching a run the module leads

Scope: the client's job while a server module that runs dungeon plans leads a dungeon by itself. The tank bot leads
(`.tb run start <leader> <plan>` in the module's own words); the client brings the party in as the driver does,
starts the run, and from then on watches. It never orders a bot during the run: an order pauses the module's run.

Code:

* `internal/forever/command/tbcmd/run.go` — the typed calls `.tb run start|stop|pause|resume|status|events`, and the
  `run` / `event` records, read strictly by the same decoder as the rest of the layer (`v2.go`).
* `internal/forever/driver/actions.go` — four new typed actions (`run start`, `run stop`, `run pause`, `run resume`)
  in the setup vocabulary, with their classes and gates.
* `internal/forever/driver/watch.go` — the watch machine and its budgets.
* `internal/forever/driver/record.go` — `NewWatchRecorder` and the summary's `watch` block.
* `internal/forever/driver/fake.go` — the scripted module's own run for the tests.
* `internal/forever/world/dungeon_session.go`, `dungeon_run.go` — the two new read ports and the session's watch.
* `cmd/forever-agent/forever_watch.go` — `forever dungeon-watch`, live and `--dry`.

## The commands and the records

`tbcmd` builds every line from typed arguments and reads only a reply that ended with its own marker and a count that
agrees with its lines. The run record's keys: `lead plan state step steps [leg legname kind] phase note pull pulls
boss deaths wipes pauses elapsed [why] [ev]`; the event record's: `seq t kind [step] [leg] [unit] why`. `leg`,
`legname` and `kind` are absent past the last step; `why` when the record has one; `ev` when there is an event.
Unknown keys are kept in `Extra`; a bad state word, phase word, event kind, boss item, number, percent escape,
repeated key or a second record for one leader is an error, never a zero. The event ring is read with the collector's
line cap raised to 256 (the ring holds 200 events plus its markers); events out of sequence or a `last=` below the
newest event are refused. `status all` and `stop`'s `pending=`/`stayed=` follow the module's own end line.

## The watch

`forever dungeon-watch --leader NAME --plan NAME` takes the connection flags of `dungeon-run` and its
`--enter tele|portal|none`, `--max-minutes`, `--max-wipes`, `--practice`, `--out`, `--navmesh`, `--dry`; plus
`--watchdog-minutes` (default 10) and `--poll` (default 2 s, clamped 1-30 s).

1. **PREPARE**, then **TRAVEL/ENTER**, exactly as a run: the GM lines, the roster, then the party's own entry and the
   observer's own teleport. The party is the module's: its commander is one of the module's bots, outside the party,
   and the tank leads. This character is only an outside GM observer: it is never a member and never the commander,
   and a roster that names it as the commander is refused at PREPARE (`observer-commander`). For a watch the roster
   is where the five names come from (the tank first, then the one healer, then three damage dealers); it refuses a
   party that is not five host bots above the entry level, the tank leading or named as the module's run leader, and
   every member already commanded. That last check is the module's own `not-commanded` condition, caught before the
   entry instead of at the start. A party command is resolved by the module itself: `.tb enter party` and
   `.tb tele party` answer with a `target=` line naming what they resolved (`party:commander:<name>` for the bot
   commander's party, `party:<tank>` for the tank's), and the typed layer reads that line as the success it is
   instead of refusing an answer with a record in it. The observer teleports on its own after the party is in; a
   shut gate or a refusal leaves it outside and the watch goes on. The same-map check is the party's: every member on
   the plan's map in one instance (the observer's map is never compared), and with `--enter none` that check is what
   proves the party was placed as the watch expects (`enter:party-map`).
2. **`.tb run start <leader> <plan>`** as one typed action. It is fight-starting: a complete state report taken now,
   everyone ready, budget left. A module refusal ends the watch with `start:<code>`.
3. **The watch loop**, every `--poll` seconds: one `status` read and one `events since=<last>` read. No order ever
   goes out (two tests pin it). The loop prints a progress line when the record changes (state, step, pulls, deaths,
   wipes, pauses, leg, kind, phase, the pause reason), one line per new event, and the bosses from
   `.tb instance <leader> info` (once at the start, once per twenty events, once a minute at most, and at the end).
4. **The budgets**:
   * *watchdog*: a running run whose event ring has not grown for `--watchdog-minutes` gets `.tb run pause
     <leader> watchdog no new event`; the watch then reports and ends with `watchdog` (a pause is a player's pause:
     the module never resumes it by itself, so leaving it paused would only wait out the time budget).
   * *time*: `--max-minutes` from the watch's start; the watch ends with `budget` and stops the run.
   * *wipes*: a run record with more than `--max-wipes` wipes ends the watch with `wipes`.
   * *the practice rescue*: a run paused on a wipe (the `pause` event that follows a `wipe` since the last `resume`)
     for 300 s gets one
     `.tb revive party` with `Practice` set (the driver's PRACTICE path and its own budget) and then `.tb run resume`
     (fight-starting, the same gate as the start), at most `Limits.MaxPractice` (8) times. A spent budget ends the
     watch with `practice-budget`; a module refusal ends it with `revive:<code>` or `resume:<code>`; `not-dead` means
     the module raised the party itself and only the resume is sent.
   * a lost link ends the watch at once (exit 7) and nothing is sent after it; three failed reads and 30 s without a
     good one end it with `blind`. Every end of the watch logs its reason (`watch: end reason=<reason>`), and a
     blind end names the last read error that blinded it (`watch: blind: ... the last was: <error>`); a state read
     that fails in PREPARE or a gate logs its first error and, at the blind bound, the error that ended the watch. A
     member with the engine off is a whole, typed record (`engine=no` or `stuck=noengine`, and no `co=`/`nc=`), not
     a read failure: the gate reports it as not ready with `noengine:<member>` and PREPARE logs that reason.
5. **The end**: when the record says `done` or `stopped`, the watch ends there (a done run refuses `.tb run stop`
   with `not-running`, so no stop is asked). Otherwise it sends `.tb run stop <leader> <reason words>` — passive, and
   the one action allowed while a stop is pending — and waits for the state to reach `stopped`, polling as it waits:
   bounded by `Limits.WatchStop` (600 s) when the watch ended on its own, and by `InterruptedStopBudget` (60 s, one
   stop attempt and its confirmation polls) when the caller's interrupt or deadline ended it. The summary is written
   either way; a pending stop that outlives the wait is reported.

Every send goes through `Run.act` and its class's gate; every line leaves through `Run.sendLine`, the one function
that calls the module. A source test over the package finds exactly two sending call sites (`Act` in `actions.go`,
`Order` in `orders.go`, both inside `sendLine`'s caller) and no `.tb` line built in the driver. The watch calls
neither `send` nor `Order`: `TestWatchNoOrderFunction` reads `watch.go` for it, and the fake's scenario asserts no
order exists after the start action on the wire.

## The summary

`--out DIR` writes the same trace and `<run_id>.forever-run.json` as a run, with mode `watch`, plus a `watch` block:
the module's `state step steps leg leg_name kind phase pull pulls deaths wipes pauses elapsed_s why`, the `bosses`
it read as `entry: alive|dead`, the `rescues` the watcher sent, the `events` it read, and the pause log (time and the
module's reason). The run's own `clear` is the module's `done`; the top-level `wipes`, `deaths` and `practice` come
from the module and the watcher's rescues.

## Tests

`tbcmd/run_test.go`: every documented record key, the optional keys past the last step, `status all`'s tick counters
in `End`, the stop's reason words and `pending`, the event ring and `since=`, the target/verb checks and the strict
refusals (state, phase, kind shape, boss item, missing note/boss, two records for one leader, wrong verb, bad counts,
bad percent, repeated key, event kinds, out-of-sequence events, `last` below the newest).

`driver/watch_test.go` over the fake:

| Scenario | What it pins |
|---|---|
| clean run | PREPARE, one start, progress and event lines, `done`, no stop, the summary block; **no order after the start** |
| wipe rescue | the module wipes and pauses; at 300 s one `revive party` with PRACTICE and one `resume`; the pause log; one rescue in the summary |
| rescue budget | `MaxPractice=0` -> `practice-budget`, no revive, one stop |
| watchdog | a silent run -> one pause with the watchdog reason, one stop, state stopped |
| time budget + stop wait | a 1-minute budget; the stop is `pending` for 6 s; the watch waits for `stopped` |
| wipe budget | two wipes past `--max-wipes` -> `wipes`, no rescue |
| start refusal | the module's `not-commanded` -> `start:not-commanded`, no run created |
| roster refusal | an uncommanded member -> `prepare:not-commanded`, no start |
| link death in each wait | a drop on the start, the status read, the events read, the instance read, the stop, the revive, the resume and the watchdog's pause: exit 7 and nothing sent after it |
| teleport entry | the driver's own pre-run orders are allowed; after the start there is none |
| summary file | the JSON's mode and watch block, one file |
| source tests | the two sending call sites, the run actions' classes, `watch.go` without the order send |
| observer outside | the party in the instance and the observer outside: PREPARE passes (the party's map against the plan's), the start goes out, the end is logged |
| bot commander | a roster led by its tank with the module's bot as commander is adopted; a party led by anyone but the tank or the run's leader is `prepare:leader`; the observer as commander is `prepare:observer-commander` |
| engine off | a member with `engine=no` is `prepare:noengine` with a logged not-ready line, never `blind` |
| blind reason | a state read that keeps failing ends `blind` and the log names the read error and the end reason |
| config refusal | the refusal before anything is sent is logged with its reason |

`tbcmd` (the typed layer): the empty optional numbers (`tgtentry=`, `tgthp=`, `manapct=`, `corpsemap=`) and an empty
corpse read as "not known"; a bot with the engine off carries no `co=`/`nc=` and is a typed state, while the same
record with `engine=yes` is refused; a setup answer's `target=party:commander:<name>` line is parsed as the success
the module sent. `driver` (the plan reader): every plan of `dungeons/` loads with its in-fight run fields
(`kill_first`, `cc_priority`, `cc_always`, `no_cc`, `summoned_kill_first`, `tank_min_hp`, a boss's `tactics`).

`cmd/forever-agent/forever_watch_test.go`: the dry watch (start, progress, events, done; exit 0), a refused plan
(exit 2) and the argument checks before any network.

## The scripted run

`Fake.ScriptRun(FakeRunScript{Steps, Pulls, ProgressEvery, WipeAt, DoneAt, Silent})` scripts the module's own run
over the virtual clock: a start event, a step every interval (a pull every third), an optional wipe (the whole party
down, the run paused on it, `wipe` then `pause` events) and an optional end. `DropOn` kills the link on one named
call; `StopDelay` makes a stop wait; `RefuseActions`/`Uncommanded` produce the module's refusals; `InstanceBosses`
answers the instance report. A watch's party comes from the leader when `--dry` names only the leader.

## Gates (final commit)

On the last code commit `4dda600e` with a clean worktree, each in the foreground and waited for:

```
make fmt-check            -> clean (no file listed)
make cross                -> CROSS-OK (windows/amd64 and darwin/arm64 builds, then their vet)
vm-test ta ~/wt-fa-watch  -> RESULT: PASS kind=ta wt=wt-fa-watch sha=4dda600e dirty=0 files=2131
                             rc=0 vm_duration=980s
staticcheck ./...         -> rc=0 (no finding)
go test -race -count=1 -timeout 20m ./... -> every package ok, no FAIL
```

The commit that carries these notes changes no code; it was gated again with the same commands and the final
report carries those lines.

## Commits

```
0d2b4088 Forever: typed run commands and run/event records
2a166750 Forever: watch a run the module leads
bd5409e2 Forever: bind the watch to a session, ship dungeon-watch
4dda600e Forever: pair the rescue pause with its wipe
```

## Hygiene grep

(Recorded in the final report.)

## The live steps for later

No live run this round: the module build with `.tb run` is not on a shared server yet. In this order, each one a
line of output and a decision before the next:

1. **The module's own start.** The party is the module's own: its tank leads and every member is already bound to
   the module's bot commander (`.tb master party <tank> <bot commander>`), with the observer outside the party and
   never its commander. Read `.tb party <tank>` first: the roster's `leader=` is the tank, its `commander=` is that
   bot, and every member answers `commanded=yes`; the watch's PREPARE refuses anything else (`leader`,
   `not-commanded`, `observer-commander`). Then `.tb run start <tank> <plan>` as the watch does, and read the `run`
   record: does `state=running` and does `step` advance within a minute? If the reply is `not-commanded`, the
   binding above is missing.
2. **The entry answers.** `.tb tele party <map> <x> <y> <z>` and `.tb enter party`: the answer is the command's
   `-end` line, with one `target=` record before it when the module resolved the party (`party:commander:<name>` or
   `party:<tank>`). `forever cmd-check --tb-...` must show the answer parsed as a success, not `travel:error`; the
   watch reads the same labels, so a party command that resolves to a party other than the roster's is a finding.
3. **The first three events.** `events since=0` twice a minute apart: are there `step`/`pull` events at the pace the
   plan's legs imply? That is the watchdog's real budget: set `--watchdog-minutes` above the longest measured gap
   between pulls, not the default.
4. **A pause's reason.** Force one (`.tb run pause`), read `status`: the `why=` text is what the watch prints. A
   player's pause never resumes by itself; `.tb run resume` is the checked one.
5. **The wipe path.** `.tb kill party` while the run holds a pull. The module pauses on the wipe; confirm the wipe
   event precedes the pause (the watch's rescue trigger) and that `.tb revive party` + `.tb run resume` is what the
   policy accepts, then let the 300 s timer do it once by hand.
6. **The stop and the stay.** Out of combat, `.tb run stop <leader> <reason words>`: the state must reach `stopped`
   and the members must be released. In combat the reply is `pending=yes`; the watch keeps polling.
7. **The instance report.** `.tb instance <leader> info` during the run: the bosses' `entry: alive|dead` is what the
   watch prints; check that a boss seen dead stays dead in the summary.
8. **The poll cost.** Two reads every 2 s is the round's stated interval; if the module's tick cost rises, raise
   `--poll` before shortening any budget.

The order of the first three: a start that does not tick makes every later step meaningless; the event pace sets the
watchdog; only then a pause tells whether the reason text is useful to a person reading the watch's output.
