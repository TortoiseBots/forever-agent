# The agent's player run, round P2a: form the group, enter, walk

Scope: the first half of the play machine (forever-agent #88): PREPARE, FORM, TRAVEL, ENTER, LEG_WALK, DONE and
LEAVE over the ports this round defines, the route a player run can load, and the fake that runs the whole machine
without a server. No pulls, fights, deaths, wipes, loot or plan steps: rounds P2b-P4 build those.

The agent is a group member and the party leader, never a GM: every line it sends is a bot-commands v1 line or one of
the two whisper words of B-6, through one send gate (D-3), and the run never travels by GM. The gate is
`playRun.sendLine` (the spec's `sendBot(line)`); `playRun.sendBot(verb, bot)` builds a line with `botcmd.Line` and passes it
through the gate, so no port call can bypass it.

## The machine

`Play(ctx, route, ports, cfg)` runs one player run to STOP and returns the commander's `Result` (states, stop, clear,
notes, and the summary when the config names an output folder).

| State | What it does |
|---|---|
| PREPARE | the character is alive and on the plan's portal map within 300 yd of the portal (else `not-near-portal`), and leads no group or one whose members are the four named bots (`not-leader` otherwise). The run's clock starts here. |
| FORM | `.bot add` for tank, healer, dps, dps in that order; `ok` waits up to 60 s for the roster to list the bot; `refused already-online` is fine only inside this party (else `bot-busy`), any other refusal stops as `form-refused:<code>`. Then group loot at threshold uncommon (2) with its readback, and the tank's roster max health against the plan's `tank_min_hp` (`tank-too-weak`). |
| TRAVEL | walks to the plan's portal, then waits for every bot within 30 yd: one follow at 20 s, `straggler` at 60 s. |
| ENTER | `World.EnterAreaTrigger`, then the plan's map within 90 s (`enter`); a bot still outside after 60 s gets one summon, a summon refused `on-cooldown` is waited out once (300 s) and retried, and a bot still missing stops as `bot-not-entered`. |
| LEG_WALK | the leg's via points and its end in segments of at most 15 yd; after each segment a bot past 35 yd pauses the walk (follow at 30 s, summon at 90 s, `straggler` at 150 s), an avoid box stops it (`avoid`), and a live patrol of the leg near the agent holds it for 120 s (`patrol`). A map change re-plans the rest of the walk. |
| DONE | every plan boss dead (its unit's dead flag) is the clear; a boss left alive stops as `bosses-alive`, because the pull that would kill it is P2b's. |
| LEAVE | disbands when `--disband` (`Disband`), logs the four bots out, and ends the run `clear`. |

Every poll checks the time budget (`time`): the plan's by name (Ragefire Chasm 90 min, Wailing Caverns 150, else 120,
D-14) or the config's `MaxMinutes`.

## The route a player run loads

`NewPlayRoute` is `newRoute` with the player run's mode. It reads what the run uses and refuses what it cannot run:

* read: `kill_first` (the skull's order, P2b), `tank_min_hp` (FORM), a boss's `wait_s` (the walk to a patrolling
  boss, P2b), the avoid boxes, the patrols, the legs, the packs.
* loaded and noted once, not acted on (D-8): `cc_priority`/`cc_always`/`no_cc` ("no crowd control in player runs
  v1") and a boss's `tactics` ("the bosses' fight tactics are not run by player runs v1"). The notes go into
  `Route.Notes`, the run's trace (`note` events) and `Result.Notes`.
* read and kept on the leg: a via's `exact` and `direct` flags. Wailing Caverns' Serpentis leg needs both (stand on the
  foot of the jump, then hop the gap the map data has no link across), and the milestone's WC clear is that plan
  (D-12), so a player run keeps the flags and walks them itself: an exact via is stood on within `PlayExactYd` (1.5
  yd) before the walk goes on, and a direct via is walked as a straight line from where the run stands instead of
  through the navmesh. A GM hop (`via.gm_cross`) is still refused: a player run never travels by GM.
* refused by their plan path: `team`, a raid `kind`, `steps` (until P4), `after_boss`, `optional`, `needs_walk`, a
  hold, an ambush, a stand pull, a leg's `leave`/`leftover_entries`/`rest`/`companions_loot`, a boss's
  `loot_item`/`phase_entries`/`summon_open_step`/`skip_unreachable`/`stand_pull`, `summoned_kill_first`,
  `pull_ceiling` and the plan's `watchdog`.

The mode is `routeMode` on the existing loader: `modeRun` (dungeon-run), `modeWatch` (dungeon-watch) and `modePlay`,
so the three share one refusal walk instead of three copies. For that reason `NewPlayRoute` lives in `plan.go` beside
`NewRoute` and `NewWatchRoute`, not in `play.go` as the spec's file list sketched.

## Ports

* `Owner` (P1) is the module, unchanged: `Add Logout Follow Stay Attack Summon Who`.
* `GroupPort` is the party the run leads: `Roster`, `Leader`, `LootMethod`, `Mark`, `Disband`. Each command waits for
  the server's readback and answers an error when none comes (D-7): a refused group command is silent in this core.
* `PlayerPort` is the agent's own character: selection and attack, the fight tick, the rest, the death watch, loot and
  the two step actions. P2a uses only `Phase`; P2b-P4 use the rest.
* `World`, `Walker` and `Clock` are the commander's, reused unchanged.

`PlayFake` is the scripted world: `GoldenOwner` (the contract's golden lines) plus a roster derived from its bot
table, the agent's own character, a straight-line walker and the virtual clock. A bot stands at the agent's side once
it followed or was summoned (unless `Stuck` names it) and where `BotPos` puts it before that; `.bot add` puts it in
the party, a summon moves it to the agent's map, and a bot with `Move` `stay` does not follow through the portal.
It records every line with its time, the largest roster the run saw, and the loot method, so the tests read the
module's own view instead of the machine's.

## Decisions the spec's list left unnamed

* PREPARE's two stops: `not-alive` (a corpse or a ghost cannot start) and `not-leader` (a stranger in the group, or a
  leader that is not this character). The spec names only `not-near-portal`.
* A partial group of the named bots is accepted at PREPARE: §3.2's `already-online` rule covers a bot a previous run
  left in the party, and FORM completes the group. "Exactly the named bots" would contradict that rule.
* FORM's unnamed failures: `bot-not-online` (the add was ok and no roster line came in 60 s), `loot-method` (no
  readback), `form` (a name the contract cannot carry), `no-module` (the core's own unknown-command line: the module
  is not loaded). A lost link stops as `link-lost`.
* The leg's summon at 90 s goes out even for a bot on the same map, and the module answers `already-on-map`: the run
  keeps waiting and stops as `straggler` at 150 s. Only a bot on another map can be brought back by a summon.
* A walk to an exact via that does not end on the point stops the run as `exact`: the hop after it is only safe from
  the foot the plan names, and the remote run fails that step the same way when its stationary proof does not come.
* `bosses-alive` is this round's boundary at DONE: P2b replaces it with the pull of what is left.

## Tests

The round's first case is `TestPlayFormsGroupWithBotAdd` (four `.bot add` lines in order, a roster of five, group loot
at 2 read back, the states `PREPARE FORM TRAVEL ENTER LEG_WALK DONE LEAVE STOP`). Also `TestPlayDisbandsBeforeLogout`
(the disband comes before the logouts in the fake's one ordered log of port calls) and `TestPlayRefusesStartFarFromPortal`,
`TestPlayBotBusyStops`, `TestPlayTankTooWeak`, `TestPlayEntersAndSummonsMissingBot` (the summon comes at least 60 s
after the trigger), `TestPlaySummonCooldownWaits` (the retry comes ~300 s after the refusal), `TestPlayStragglerFollowThenSummon`
(the summon ~60 s after the follow, the stop ~120 s after it), `TestPlaySendsOnlyBotLines` (the gate refuses `.tb`,
`.gm`, `.tele`, a bare `.bot`, `.bot add` without a bot and an unknown verb; a whole run's lines are `.bot` lines),
`TestPlayLootMethodReadback`, `TestPlayWalksAnExactAndDirectVia` (the exact via is stood on, the direct one is walked
as a straight line), `TestPlayRouteAcceptsRFCAndWC`, `TestPlayTimeBudgetByPlan` and `TestPlayRouteRefusals`.

## Mutations

Each applied by hand to `play.go`, run against its test and reverted:

| mutation | caught by |
|---|---|
| the ENTER summon goes out before the bots' 60 s | `TestPlayEntersAndSummonsMissingBot`: "the summon came 2s after the trigger; want at least 60 s" |
| the send gate admits any line that starts with a dot | `TestPlaySendsOnlyBotLines`: "\".tb run ragefire\" passed the send gate" |
| FORM continues after a refusal | `TestPlayBotBusyStops`: `stop = "straggler"; want bot-busy` |
| the loot method is set without its readback | `TestPlayLootMethodReadback`: `stop = "clear"; want loot-method` |

## Test first

The tests were written before the machine: the focused command on the tree that carried them (and the route changes)
but not `play.go` failed to build, which is the round's first red:

```
internal/forever/driver/play_test.go:43:8: undefined: PlayConfig
internal/forever/driver/play_test.go:44:8: undefined: PlayPorts
internal/forever/driver/play_test.go:61:9: undefined: PlayConfig
internal/forever/driver/play_test.go:64:10: undefined: PlayPorts
internal/forever/driver/plan.go:273:30: undefined: playNoCCNote
internal/forever/driver/plan.go:277:18: undefined: playMinutes
internal/forever/driver/plan.go:432:29: undefined: playTacticsNote
FAIL	github.com/pjerra/forever-agent/internal/forever/driver [build failed]
```

## Verification

On the build VM (`vm-test ta <worktree> -- 'go test ./internal/forever/driver/ -count=1 && go test -count=1 -timeout 20m ./... && go vet ./...'`):

```
+ go test ./internal/forever/driver/ -count=1 && go test -count=1 -timeout 20m ./... && go vet ./...
ok  	github.com/pjerra/forever-agent/internal/forever/driver	1.068s
ok  	github.com/pjerra/forever-agent/internal/forever/driver	1.451s
ok  	github.com/pjerra/forever-agent/internal/forever/world	778.057s
...
RESULT: PASS  kind=ta sha=f2f6a110 (the mutation script, untracked)
```

Every package passes and `go vet` prints nothing. The mutation run and the first red are above.

## What is not there

P2b adds PULL, FIGHT, REST and BOSS (the mark rotation, the dps orders after the tank's aggro, the fight timeout);
P3 the deaths, wipes, ghost walk, bot whispers and loot; P4 the four step kinds; P5a the live adapters; P5b
`forever dungeon-play`. The spec's LG and L1 live proofs are not this round's.

Limits:

* The trace's party header carries the four bots only: the agent's own character name is the session's, which the
  live command (P5b) knows and this machine does not.
* The dry command (`--dry`) is P5b's; `PlayFake` is a normal file so that command can bind it without a test file.
