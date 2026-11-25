# Round B1: the watch's frames and the combat log

The dungeon watch records the fight it only used to summarize. Two reads every frame interval (`.tb state party`
and `.tb threat party`) become one trace frame, and every tick drains the session's own combat log into a file
beside the trace. Both inputs are the bug hunter's: **F** is the frames, **C** is the combat log.

## What was built

**The combat log lives in the world session** (`internal/forever/world/combatlog.go`), not in serve: the serve
`events` method, `forever dungeon-watch` and the trials runner read one ring with one `seq` axis. Decision 2.

- `CombatLog` is a 4,096-row ring of `CombatRow`. A row is the trials runner's raw row — `seq kind time mono_ms
  source{guid,name} target{guid,name} spell_id amount`, plus `reason` on a `spell_fail` — so `events.Normalise`,
  `trials.py` and the bug hunter parse one shape. GUIDs are `0x` and 32 hex digits; `time` is the wall clock with
  milliseconds and its offset; `mono_ms` is the process's steady axis (`events.MonoMS`).
- `Since(seq, limit) (rows, next, oldest, dropped)`: the rows above `seq`, oldest first, at most 1,000. `next` is
  the last row's seq (or the caller's own when nothing is above it, so a caught-up reader never re-reads a page),
  `oldest` the lowest seq still held, `dropped` how many rows fell out of the ring above `seq`.
- The ring is a **value in the session**, so a reconnect keeps writing above the old maximum: a seq never goes back.
- Fed beside each model apply in the combat dispatch, one row per packet effect: `spell_start`; `spell_go` (one row
  per hit target, the cast's single target when the list is empty); `spell_fail` with the core's reason in words
  (`combat.SpellFailureName`); `melee`; `spell_damage`; `periodic`, or `heal` for the two heal aura types
  (`PeriodicAuraEffect.PeriodicTickIsHeal`); `heal`; `aura_applied`/`aura_removed`; `death`.
- **Auras**: `objects.World.ApplyAuraUpdate` now returns `[]AuraChange` (`added`, `refreshed`, `removed`), so a
  refresh is no row. A full update (`UpdateAll`) applies the slots it lists first and removes the ones it leaves
  out afterwards: a listed slot is a refresh, never a remove-and-re-add, which would forge a buff flip-flop.
- **Deaths**: `objects.World` keeps the guids a merge just took to dead (`applyDeathLocked` reports the transition);
  the session drains them after each values update and writes one row. That merge does not name the killing blow,
  so a death row's source is empty and the damage row right before it is the blow.

**Frames** (`internal/forever/driver/watch.go`, `record.go`): `--frame-every` (default 2 s, 2-60 or 0 for off)
takes `.tb state party <leader>` and `.tb threat party <leader>` on the watch's own tick, independent of the status
poll, and writes one `frame` record. Both are reads; the watch still sends no order after the run start.

New `trace.Bot` keys: `role`, `mvm` (the module's move word; `mv` stays the movement boolean), `stuck`, `eat`,
`drink`, `rest`, `life`, `dl` (yards to the leader; absent for self or another map), `auras` (spell ids the client's
model holds, from `driver.World.Auras`). New `trace.Mob` keys: `vic` (the threat victim, a guid), `top`
(`[guid, threat]` pairs, highest first), `fl` (fleeing), and `np` when the module sent no position — 0,0,0 is a real
place. Entries from threat lines are written as `mobs` records when a mob is first seen, and the party's first frame
writes the `party` records (`g name role class`): a watch's roster comes from the module, so the header cannot carry
it, and the previous header rows had no class.

**The combat-log file**: every tick (frames on or off) drains the ring into `<run_id>.combatlog.jsonl`, one JSON
row per line, pages of 1,000 until caught up. A ring that dropped rows is one log line per growth of the loss. The
file opens on the first row, so a watch whose log never answers leaves none.

**`forever dash-bots --watch --record FILE`** appends one JSON line per good read: `{"at": ..., "bots": [...],
"heartbeat": ...}` — the read's wall time and the roster exactly as the dashboard decoded it.

## What this round fixed on the way

`Recorder.Frame` never set the record's `t` (the writer does not, its doc says the caller does), so every frame of
a run had `t=0`: the writer's sampling saw a zero-length gap after the first frame and **silently dropped every
frame after the first**. The watch would have written one frame per run. `Recorder.Frame` and
`Recorder.WatchFrame` now stamp `t`. The run's trace has frames it never had.

## Decisions taken here

- Frame interval is the watch's own; the poll interval is unchanged. At the defaults a tick is four reads (run
  status, run events, then state and threat), each one round trip, and still no order.
- A failed frame read is one line and the next tick retries; it never ends the watch (the status and event polls
  still own the blind rule). A failed threat read writes the frame with the party only and says so once.
- The frames' `role` is the party slot's (`tank`, `heal`, `dps`), the same vocabulary as the party record, not the
  module's `healer`; the module's own word is in `.tb state` and the two agree once the roster is adopted.
- Names never enter a frame: the threat victim and the top list are mapped from party names to guids, and a unit
  outside the party maps to nothing. A party member whose guid the model has not resolved is a row with no `g`, not
  its name. The combat log keeps the module's names — it is a runtime record, not the repository.

## What to check on the first live watch

1. The module's tick cost of `.tb state party` + `.tb threat party` every 2 s (decision 4). If the run's own
   progress slows, raise `--frame-every` to 4 or 5 before dropping fields.
2. That the threat lines carry `pos` and `flee` (slice 4). Without `pos` the frames say `np`.
3. That a frame tick's two reads never delay a status poll long enough to trip the watchdog on a quiet run: the
   watchdog's 10 minutes leaves room, but a module that takes seconds per read should show in the log first.
4. That `<run_id>.combatlog.jsonl` and the run's trace join: the frame `g` and the row `source.guid` are the same
   text, `0x` and 32 upper-case hex digits.

## Tests

- `TestWatchWritesFrames`: a scripted 60 s run at a 2 s frame interval writes 29-31 frames (one per tick), each
  with five bots
  (role, move word, distance, auras, meal, life), one threat mob (health, position, victim, top) and the party and
  mob records; the summary's `frames` agrees.
- `TestWatchFramesAreReadsOnly`: with frames on, no order and no action after the run start reaches the fake; the
  package's source test (`TestOnlySendFunctionsReachTheChannel`) still proves no `.tb` line is built in the driver.
- `TestCombatLogFile`: 1,137 scripted rows cross the 1,000-row page bound and land in `<run_id>.combatlog.jsonl`
  byte for byte, in seq order.
- `TestCombatLogSincePages`, `TestCombatLogOverflow`, `TestCombatLogSeqAcrossReconnect`, `TestCombatRowShape`: the
  ring's paging, its overflow and loss count, its seq across a reconnect, and the row's keys.
- `TestAuraUpdateReportsChanges`, `TestAuraUpdateAllReportsItsClearances`, `TestAuraUpdateUnknownUnitChangesNothing`:
  add, refresh (no row), removal, the full update's clearances.
- `TestDashRecordLines`, `TestDashRecordNeedsWatch`: one line per good read, each with a parseable time and the
  roster; `--record` without `--watch` is a usage error.

## Fix round (review findings)

The adversarial review of this round raised three findings. The second review (a second model) produced none. All three were
real; each has a test that fails on the code before the fix.

1. **Combat rows were skipped after a write failure** (`watch.go:413-419`, `record.go`): `Recorder.Combat` answered
   `0` on a failed open or write and `drainCombat` advanced the cursor anyway. Now it answers `(rowsWritten, error)`;
   a page is all or nothing (a short write is cut back to the file's last complete row), the cursor advances only
   past a page the file took, a failure is one line and the next tick retries the same page, and the last error is
   in the summary as `combat_error`, without the operator's path. Tests: `TestCombatWriteFailureHoldsTheCursor`,
   `TestWatchSummarySaysTheCombatLogDidNotWrite`.
2. **A missing GUID turned a character name into the frame's `g`** (`record.go:366-368`): `botFrame` fell back to
   the character name. Removed: an unresolved member is a frame row with no `g` (its role still names the slot) and
   writes no party record until the model knows it, so its identity never changes under the trace. Every guid in the
   watch's record is now one text, `0x` and 32 upper-case hex digits (frames, party rows, threat entries, targets
   and combat rows), so a frame and a combat row join directly. Tests: `TestBotFrameNeverUsesAName`,
   `TestWatchFrameUnresolvedMember`, and the canonical checks in `TestWatchWritesFrames`.
3. **The combat log had no size bound** (`record.go:500-522`): `--combat-max-mb` (default 64, 1-4096) bounds it.
   A page that would cross the bound is not written (all or nothing, so no partial page and the cursor holds), the
   watch stops draining it, and the summary carries `combat_truncated`, `combat_bytes` and `combat_rows`. A bound
   refused on the very first page leaves no file, as a log that never answered does. Tests:
   `TestCombatLogBoundStopsTheDrain`, `TestWatchSummarySaysTheCombatLogIsTruncated`.

`reject`: none; all three were real and are fixed above.

## Second fix round (review finding)

The re-check of the fix round found one more finding: a later, smaller combat-log hole disappeared from the
diagnostics. `CombatRows` reports `dropped` against the current cursor, but `drainCombat` compared it with
`combatLost`, which held the cumulative loss. After a first 100-row overflow, a later independent 50-row overflow
returned 50, which was not greater than 100, so neither the log nor `combat_dropped` recorded the second loss — the
truncated record looked more complete than it was.

4. **Repeated combat-log holes are underreported** (`watch.go:422-427`): the watch now keeps a watermark,
   `combatLostThrough`, the highest seq whose loss is already in `combatLost`. A read's hole is
   `combatSeq+1 .. oldest-1`; only the part past the watermark is added, so each hole counts exactly once whatever
   its size, and a cursor the writer retried after a failed write reports the same hole and adds nothing. Every new
   part is one log line and goes into `combat_dropped`. Test: `TestCombatEachGapIsCounted` (100 lost, then 50, then
   20, with the first page refused once and retried).
