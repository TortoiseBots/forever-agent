# The plan's fight steps checked against the module's boss tactics rows

Issue #222: read the bot module's tactics data for a watch record and flag every plan step that contradicts it, so the
check is not a hand-written file.

## What was built

A plan's `legs[].boss.tactics` block is the agent's own (`internal/tactics`): kill an add first, interrupt a cast,
dispel an aura, leave a blast. It is never exported to the server module — `plan-export` drops it, and
`internal/planexport/unexpressed.go` says why: a bound party fights by the module's own boss tactics rows, the
`ai_boss_tactic` table (`ai/playerbot/BossTacticsMgr.cpp`, `docs/play/boss-tactics.md` of the module). So on a watched
run the plan's block is advice nobody executes, and a step the module's rows contradict is a step the fight does not
do: this round makes the watch report say which.

**The data channel.** The module's command link has no command that returns `ai_boss_tactic`: `.tb run status`,
`.tb run events` and `.tb instance <leader> info` carry progress, per-step events and boss states, and a run's files
(trace, summary, combat log) carry what the watcher saw, not the module's configuration. This round invents no module
command, so the rows arrive as a file on a new watch flag:

```
forever-agent forever dungeon-watch ... --module-tactics FILE
```

`FILE` is the table's rows as a JSON array, one object per row, keyed by the column names the check reads (`map`,
`boss_entry`, `kind`, `target_entry`, `spell_id`, `priority`, `param`, `hp_min`, `hp_max`; `comment` is kept and
unused). The health band is part of the table's primary key, so it is read (two phase bands of one boss are two rows)
but not compared: the check judges a fight's steps, not the phase it stands in. An export of
`SELECT map, boss_entry, kind, target_entry, spell_id, priority, param, hp_min, hp_max FROM ai_boss_tactic` (or
`JSON_ARRAYAGG` of it) gives the file. A column the check does not read is not refused — the table grows columns (the
aura, seek, anchor and burn kinds each added some), and a stricter reader would refuse every export after a module
change. A row's own shape is checked: map, boss and kind are required, an `interrupt` row needs a spell, a
`kill_first` row a target, an `avoid_cast` row a spell and its yards, a duplicate key and a wrong-typed field are
errors naming the file and the row; a file that is not a JSON array (including `null`) is refused too.

**The check** (`internal/tactics/module.go`, `internal/tactics/compare.go`, called from
`cmd/forever-agent/forever_watch_tactics.go`). A "fight step" is one instruction of a plan boss's tactics block: one
`interrupt` entry, one `kill_first` entry, one `never_target` entry, one `move_out` row. The watch takes the plan's
blocks from the plan it loaded before the run (the driver's route drops them), so a plan edited during a long watch
is not what the report judges. After the run, for every plan boss with a block, the report compares the steps with the
module's rows for that map and boss entry on the three axes the two vocabularies share:

| Axis | The plan says | The module's rows say | Conflict |
|---|---|---|---|
| interrupts | `interrupt` (ids or names) | `interrupt` spells | a plan spell no module interrupt row names; a name is resolved through the Forever spell table (all its ids) |
| kill order | `kill_first`, `never_target` | `kill_first` targets with priorities | an entry with no module row; the plan's order against the module's priorities (lower first, equal is one wave); a `never_target` that is a module `kill_first`; a `never_target` no module row contradicts is not checked (the table has no `never_target` kind) |
| avoid | `move_out` cast rows with yards | `avoid_cast` rows with yards | a spell with no module row; plan yards smaller than the widest row's (a name's every id is checked, and a spell with several rows must meet the widest) |

The block's `dispel` and `no_dispel` steps and a `move_out` ground rule are listed as not checked: the module's table
has a dispel rank, not a dispel instruction, and no ground-effect kind, so no row of it can judge those. A boss the
file does not name, a name with no spell table to resolve it, and a plan with no block at all are not-checked lines
too. A plan step that matches is counted and silent.

**The report.** The watch prints, after the card, one header line and one line per finding, and writes the same lines
beside the record as `<run_id>.tactics-check.txt`:

```
tactics: 3 plan fight steps checked against the module's 62 rows: 1 disagree
tactics: leg 1 "revelosh" boss 6910: interrupt "Lightning Bolt": the module's rows interrupt Chain Lightning (16006), Chain Lightning (15117) only
tactics: not checked: leg 2 "sentinel" boss 7023: move_out 10132 (ground): the module's rows have no ground-effect row the check reads
```

Without `--module-tactics` the section is one line — `tactics: not checked (no --module-tactics file): ...` — and a
plan with nothing to judge says not checked as well: the section is never an agreement it did not test.

## Tests

The fixtures are synthetic (map 999, boss 9001, entries 8001/8002, spells 7001-7004 and 999001/999002; a dry watch
writes the recorded run the command test reads):

- `TestLoadModule`, `TestLoadModuleHealthBands` (two phase bands of one boss are two rows; the same bands twice is a
  duplicate), `TestLoadModuleHostile` (thirteen hostile files: no map/boss/kind, an interrupt without a spell, a
  `kill_first` without a target, an `avoid_cast` without spell or yards, a negative priority, a string map, a negative
  spell, a duplicate key, a `null` file, a missing file), `TestLoadModuleCaps` (rows and bytes past the bounds).
- `TestCompareAgreeingPlan` (no conflict, the steps counted), `TestCompareInterruptNotInModule` (one extra interrupt,
  the module's own list in the line), `TestComparePartialBlock` (a one-axis block contradicts nothing on the others),
  `TestCompareKillOrder` (a reversed order, the module's priorities named), `TestCompareNeverTarget` (a plan that never
  attacks the module's first kill), `TestCompareNeverTargetWithoutModuleRow` (not checked, never a pass),
  `TestCompareMoveOut` (a smaller radius, a spell with no row, a ground rule not checked), `TestCompareMoveOutEveryID`
  (a name's second id and a spell's strictest row), `TestCompareMoveOutAgrees` (the row's radius met or exceeded),
  `TestCompareMoveOutMalformed` (neither cast nor ground is not checked), `TestCompareBossWithoutRows` (not checked,
  never a pass), `TestCompareWithoutSpellData`, `TestCompareDispelNotChecked`, `TestCompareEmptyPlanBlock`.
- `TestWatchTacticsContradictingStep` (the dry watch's report names the step and writes the check beside the record),
  `TestWatchTacticsLiveReport` (the live branch's report, with the session stubbed, runs the same check),
  `TestWatchTacticsAgreeingPlan`, `TestWatchTacticsNotChecked`, `TestWatchTacticsBadFile` (a usage error naming the
  flag), `TestWatchTacticsEmptyPlan`.

## Gates

Run on the build VM (`vm-test ta <worktree>`), one command gated stage by stage, the lines quoted from its log
(tree `2eae5d800`, dirty=0):

```
gofmt -l .                      -> (no output)
staticcheck ./...               -> rc=0 (staticcheck 2026.2.2, no finding)
CGO_ENABLED=0 go build ./...    -> rc=0
go test -count=1 -timeout 20m ./... -> ok
go vet ./...                    -> rc=0
RESULT: PASS kind=ta sha=2eae5d800 dirty=0 files=2958
```

## Known limits

- The rows come from a file a person exports. The watch cannot read `ai_boss_tactic` over the link (no module command
  returns it) and this round ships no module change; a `.tb` read of the table would retire the flag.
- The comparison reads three axes. The plan's `dispel`/`no_dispel` steps, a `move_out` ground rule, and the module's
  row kinds the plan's block has no field for (`behind`, `hold_kill`, `one_at_a_time`, `linked_death`, `seek_aura`,
  `anchor`, the aura kinds) are not checked and are listed as such where they are plan steps.
- A plan step that names a spell by name is resolved through the build's Forever spell table; a name the table does
  not hold is not checked (the plan loader refuses a misspelled name in a shipped plan already).
- The section is printed by the watch, not by `forever run-report`: the check needs the plan the watch loaded and the
  module's rows, neither of which a saved record carries. A record written with the check keeps the lines beside it.

## Live checks for the lead's live queue

None needed for the code (every test is synthetic, and the live branch's report is driven with a stubbed session). The
first session that carries a real export should check, on a test server:

1. **The file matches the server.** `SELECT ... FROM ai_boss_tactic` on the same slot the watch runs against, and a
   plan whose block was written from those rows (Uldaman's Revelosh, Wailing Caverns' Serpentis) reports the agreement.
2. **A real disagreement.** The Revelosh block names `Lightning Bolt` beside `Chain Lightning`; the module's rows
   interrupt only Chain Lightning (two ids), so the report should flag the filler bolt exactly once.
