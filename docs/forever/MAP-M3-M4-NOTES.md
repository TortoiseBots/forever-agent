# MAP-M3/M4: death labels from the combat log, and the live acceptance steps

Round M3 of the control and replay spec (section 3.3, rounds M3/M4). M1 is the
adapter (`runmap`), M2 the `run-map` command and `dungeon-watch --map`; M3 adds
the death's blow to the death markers. M4 is live only and was not run here:
its exact acceptance steps are below for the lead's live queue.

## What was added (M3)

* `internal/forever/runmap/combat.go`: reads the optional
  `<run_id>.combatlog.jsonl` beside the trace (rows `seq kind time mono_ms
  source{guid,name} target{guid,name} spell_id amount`; guids `0x` + 32 hex,
  keyed with `runmap.GUIDKey`, never the low word alone). Only damage rows
  (`melee`, `spell_damage`, `periodic`) are kept; a heal is ignored, not
  skipped. Rows are placed on the trace's clock (row wall time minus the
  trace's start); a row with no readable time, or a trace that does not know
  its start, is skipped and counted. `deathLabel` adds the last damage row on
  the dying member within 10 s before the death: `died: spell <id> <amount>`
  (`died: melee <amount>` for a melee row). Unknown spell ids stay
  `spell <id>`; no name is ever guessed (the log carries none).
* `runmap.LoadRun` reads the file with no new flag: a missing combat log is no
  rows and no error, and every death label stays as today. A malformed or
  unusable row is skipped and counted in one note line
  (`skipped N combat log rows: not a damage row with a readable time`).
* `appendMarkers` gains the damage rows and labels each death by the member's
  slot guid (`guidOfSlot`; an unattributed death keeps today's label).
* No `run-map` change was needed beyond `Load`: no new flag, and the file is
  optional there too. The SVG/HTML without `--names` hold slots, never
  character names, even though the combat log carries names
  (`TestHTMLHasNoCharacterNameWithoutNames`).

## Tests

`internal/forever/runmap/combat_test.go` over the M1/M2 fixture (the death is
the healer at module second 8, 13 s into the trace). Failing case first:
`TestDeathLabelUsesLastDamageRowBeforeDeath` (two damage rows on the dying
member, one row on another member plus a heal between them, and a later row on
a third member: the label carries the dying member's last row; a reader taking
the last row overall would take the third member's). Others:
`TestDeathLabelWithoutCombatLog`, `TestDeathLabelIgnoresRowsOlderThan10s`,
`TestCombatRowGUIDHighWord` (a low-word twin's row never labels the healer; a
reader keying by the low word would merge them),
`TestHTMLHasNoCharacterNameWithoutNames`,
`TestCombatLogSkipsMalformedRowsWithANote`. The old `testdata/combatlog.jsonl`
fixture is deleted: `fixtureWith` wrote it into every record while nothing
read it, and the death test that seemed to cover it (`TestLoadMarkers`
expecting the bare `healer` label) never exercised the join. Mutations checked
by hand: dropping the 10 s window fails
`TestDeathLabelIgnoresRowsOlderThan10s` (label `healer died: spell 133 40`,
want `healer`); keying by the low word fails `TestCombatRowGUIDHighWord`
(label `healer died: spell 222 20`, want `healer`).

## Limits

* A `-race` re-run of the touched packages passed in this round; the full
  suite still runs once via `vm-test` on the lead's queue (no live run here).
* Rows older than 10 s are not the death's blow even when no newer row exists;
  the label then stays bare rather than naming an earlier fight.

## M4 live acceptance (not run; exact steps for the lead)

1. One watched RFC run: `forever dungeon-watch --frame-every 2 --map --out DIR`.
2. The HTML in DIR shows five routes, pulls, bosses, a working slider, and
   death labels with spell and amount; quote counts only (no names, no
   private paths).
3. Copy the map files into the dashboard's agent-maps folder; the Agent maps
   tab lists the run and opens the HTML.
4. No live run happened in this round: the HTML was checked as text and by the
   golden files only.

## Gates

* `make fmt-check`: PASS (no output).
* `make cross`: PASS (no output).
* `staticcheck ./...`: PASS (no output).
* `go test -race -count=1 ./internal/forever/runmap/`: PASS.
* `go test -race -count=1 ./cmd/forever-agent/ -run 'TestRunMap|TestWatchMap|TestForeverWatchMap'`: PASS.
* `vm-test ta ~/wt-fa-map3`: `RESULT: PASS  kind=ta wt=wt-fa-map3 sha=a6530060 dirty=0 files=2318` (rc=0, vm_duration=1113s).
