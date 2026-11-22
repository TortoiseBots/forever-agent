# The agent's player run, round P4: the plan's steps

Scope: the four step kinds a player run's own character can do between a leg's packs and its boss (forever-agent #88,
§3.11): `use_object`, `gossip`, `loot_object` and `wait_for`. `NewPlayRoute` stops refusing a leg's `steps`; the step
kinds a player run cannot do are refused by their plan path instead of dropped. No live adapter and no command: P5a
and P5b build those. `after_boss` stays refused (it is not this round's; the WC extra plan's leg 11 carries both an
after-boss step and a boss hold, so it still does not load for a player run, D-12).

## The machine

`STEPS` sits between a leg's packs and its boss in `legWalk`, the place the module's own stepper gives its steps
(walk, pull groups, steps, boss, after-boss). One step runs per visit, so a pack in the way returns `StatePull`, the
fight runs, and the step begins again afterwards — `stepIdx` only advances when the step is done.

| Step | What it does |
|---|---|
| `use_object` | walks to the plan's `stand` (or `PlayStepStandYd` short of the object when the plan gives none), uses the object by entry, and the step is done when the object's state differs from what it was before the use, at most `PlayObjectUseWait` (10 s). A use that changes no state stops the run as `step:use_object`. |
| `loot_object` | walks to the stand and opens the object's window by its guid (`PlayerPort.Loot`), taking everything (the port answers the window's rolls greed, D-9). `ErrCannotLoot` stops the run as `step:loot_object`. |
| `gossip` | the character talks itself (`PlayerPort.Gossip(entry, option)`) and the step is done at the menu's close. `ErrNoGossipOption` is asked again every poll until the plan's `retry_s` (15 s default), then `step:gossip`; a dead NPC or one not in view stops at once. |
| `wait_for` | polls the plan's one condition to its `timeout_s` (60 s default): a game object's state (with `gone_ok`: missing after the run saw it counts, never for `ready`), no living hostile within a radius of the point (or of the agent), or a creature's `seen`/`hostile`/`dead`/`gone`. The timeout stops the run as `step:wait_for` with the plan's `fail_text` in the trace. |

Every step records `step`, `step_done` (with its seconds) or `step_failed` (with the leg, the index, the kind and the
reason) in the trace, so a report can name the step that ended the run.

## The route a player run reads

`playStep` (play_steps.go) reads a plan step into the route's own shapes and refuses, by plan path, what a player run
cannot do:

* refused: `use_item`, `ritual`, `area_trigger` (P4 is the four kinds), a `gossip` by its `text` line (the menu is not
  read by this round, so the step names its `option`), a chest's `expect_item` and `batch` (the bags are not read), an
  object named by its `spawn` (a remote run's resumed-run readback), a `use_object`'s `via_item` and `requires_item`,
  and any step that names no kind or several.
* accepted and noted once (`Route.Notes`, D-8's rule): a gossip step's `by` ("the gossip steps are the agent's own
  talk in player runs v1: a plan's by (a bot's order) is not run"). Wailing Caverns' extra segment gives the Disciple's
  menu to the tank; bot-commands v1 has no order for a bot's talk, and the agent standing there talks itself.
* accepted unchanged: an object's `near`/`range`/`alt_entries` (a reader that knows no server uses `entry`), a `proof`
  (the agent's own driver does not read it), a wait's `hold` and `agent_only` (both are the module's rows; the agent
  runs the wait as written), and a `stand` point.

`PlayerPort` gained one read for the steps: `Objects() []PlayObject` (guid, entry, position, state), the client's view
of the game objects a `use_object` and a `wait_for` read back. The rest of the port was already there (`UseObject`,
`Gossip`, `Loot`); `Loot` now also opens a game object's window.

## Decisions the spec's list left unnamed

* A step's object is the nearest one of its entry to the plan's `near` point inside the plan's `range` (8 yd
  default), never any other object of the same entry elsewhere in the instance; with no `near` the nearest to the
  character counts.
* A stand the plan does not give is `PlayStepStandYd` (4 yd) short of the object (or the NPC) on the line to where the
  character stands: inside the client's use and gossip range without walking into the object's own collision.
* A `use_object` whose object is not in view after the walk fails the step before the use: the client has nothing to
  send the use for, and a silent 10 s wait would read as "the door never opened".
* Stop reasons are `step:<kind>` (`step:use_object`, `step:gossip`, `step:loot_object`, `step:wait_for`); the detail
  (which object, which option, the plan's `fail_text`) goes into the trace's `step_failed` event so the stop reason
  stays one stable word for a report.
* A gossip step is the agent's own talk whatever the plan's `by` says, and the difference is noted once at load.
* A step a pack interrupted (a pack in the way during the step's walk) is begun again after the fight: the step's
  action is idempotent for a door, a lever or a menu, and claiming it on a half-done walk would be worse.
* A use_object's proof is the object's change: an object already in its state when the use goes out (a door a previous
  attempt opened) changes nothing, and the step fails. The plan's own `wait_for` after the step is the author's way to
  pass an object a run finds open.
* The object must be in the character's view after the step's walk, because the use and the readback name it by entry
  and the client sends a use for an object it sees: an object out of view fails the step before the use instead of
  waiting the 10 s out.

## Tests

The round's first case is `TestPlayStepUseObjectWaitsForState` (the door that opens, the walk on the plan's stand, the
`STEPS` state), then `TestPlayUseObjectWithoutStateChangeStops` (the 10 s wait, `step:use_object`),
`TestPlayGossipOptionAndRetry` (the plan's option only, two menus without it), `TestPlayGossipRetryRunsOut`,
`TestPlayGossipDeadNPCStops`, `TestPlayLootObjectTakesTheChest`, `TestPlayWaitForSeesTheUnit`,
`TestPlayWaitForTimeoutStops`, `TestPlayWaitForHostilesGone` (a living one holds, a corpse does not),
`TestPlayGossipLegOfTheWCExtraPlan` (the extra segment's first leg's step list on the fake, its `by` noted) and
`TestPlayStepsRunBeforeTheBoss`. `TestPlayRouteRefusals` gained the step refusals (`use_item`, `ritual`,
`area_trigger`, `gossip.text`, `use_object.spawn`, `loot_object.expect_item`) and lost its `steps` case.

## Mutations

Each applied by hand to `play_steps.go`, run against its test and reverted (one run, three tests):

| mutation | caught by |
|---|---|
| the use_object's proof is any state (`before := obj.State[:0]`) | `TestPlayUseObjectWithoutStateChangeStops`: `stop = "clear"; want step:use_object (the door never opened)` |
| the gossip sends `Option+1` | `TestPlayGossipOptionAndRetry`: `the talks = [1 1 1]; want three talks of option 0 (two menus without it, the third with it)` |
| a unit wait holds without reading the view | `TestPlayWaitForTimeoutStops`: `stop = "clear"; want step:wait_for` |

## Test first

The round's tests and the route change went in before the machine (`play_steps.go` moved aside, the spec's first test
run on that tree):

```
+ go test ./internal/forever/driver/ -count=1 -run TestPlayStepUseObjectWaitsForState
# github.com/pjerra/forever-agent/internal/forever/driver [github.com/pjerra/forever-agent/internal/forever/driver.test]
internal/forever/driver/plan.go:112:10: undefined: Step
internal/forever/driver/plan.go:304:18: undefined: playStep
internal/forever/driver/plan.go:466:29: undefined: playGossipByNote
internal/forever/driver/play.go:393:7: undefined: StateSteps
internal/forever/driver/play.go:394:12: p.steps undefined (type *playRun has no field or method steps)
internal/forever/driver/play.go:723:10: undefined: StateSteps
internal/forever/driver/play_steps_test.go:61:34: undefined: StateSteps
internal/forever/driver/play_steps_test.go:94:48: undefined: PlayObjectUseWait
internal/forever/driver/play_steps_test.go:95:72: undefined: PlayObjectUseWait
internal/forever/driver/play_steps_test.go:256:35: undefined: playGossipByNote
FAIL	github.com/pjerra/forever-agent/internal/forever/driver [build failed]
```

## Verification

Focused: `vm-test ta ~/wt-fa-prun4 -- 'go test ./internal/forever/driver/ -count=1 -run TestPlay'` ->
`ok github.com/pjerra/forever-agent/internal/forever/driver 0.199s`, rc=0. The full clean-tree run
(`vm-test ta ~/wt-fa-prun4`) is the round's last step; its RESULT line goes in the round's report to the lead.
