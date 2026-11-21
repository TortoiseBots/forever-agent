# Serve W2-W4 notes: teardown, debug capture, event_log, act, mounts, follow

Rounds W2, W3 and W4 of FOREVER-AGENT-REST-SPEC.md, in that order, one commit each on `forever-serve-w2`
(off `origin/main` at 941a076a). Package `internal/forever/world` is shared, so the three rounds
land as three commits: `Forever: serve teardown, the debug capture and its stats`, `Forever: serve event_log and
act`, `Forever: mount, dismount and follow`.

Core citations are from `d02645dd21` (`git -C ~/ForeverCore show d02645dd21:<path>`). No live run was made in this
round; the live checks are at the end.

## W2. Serve operations: teardown and debug capture

### teardown

`teardown {"gm": "off"|"on"|"keep", "dry_run": bool}` (files `world/serve_session_methods.go`; types
`ServeTeardown`/`ServeTeardownStep`):

- A running follow loop is cancelled first (its own `follow` step, added when W4 landed) so no movement of ours can
  go out while the logout is in flight; `dry_run` plans that step.
- `gm` defaults to `off`. The line (`.gm off`/`.gm on`) is classified by the same guard a raw `gm` call uses
  (`serveGMUnitLine`) and then sent through `serveGM`, so the GM-mode probe and the permission chaining are the ones
  every other command line gets. `keep` sends nothing. An unknown value is refused before anything is sent.
- The full logout follows (`serveLogout` → `s.logout`, the protocol logout on both sockets).
- The answer is the old daemon's step shape: `{steps: [{step, status, detail}], failed, seconds, character,
  dry_run}`. `teardown_cmd.go`'s `teardownCallAgent` reads `status` (its `"fail"`/`"ending"` count as a failed
  step) — the brief's "result" is that status; the field name follows the reader.
- A refused GM line is a failed step and the logout still runs, as `runTeardown` does (`gm` step 9, logout step 10).
- `dry_run` plans the steps (`status: "plan"`), sends nothing and leaves the daemon serving.
- The daemon stops after the reply: `st.stopped = true` after the result is built, the loop's `st.run` has already
  written the answer to the caller (the same order `logout` uses).
- With the link reconnecting, a regular method call is refused before it is queued: `-32005`, nothing sent. This is
  the tested path (`TestServeTeardownDuringReconnect`).
- `slot.json` already carries `"server": "forever"` (`cmd/forever-agent/forever_serve.go` `serveManifest`, from the
  serve round); nothing changed there. `teardown` left `notPortedMethods`.

### Debug capture (`ServeConfig.CaptureDir`)

- `ServeConfig.CaptureDir` (world/serve.go) is `Options.CaptureDir` for a serving session: `serve` validates it and
  copies it into the session options before the RPC table is registered, so `loginAgain` (which is built from
  `st.opts`) gives every re-entry the same parent.
- The run directory is opened on the first captured packet (`s.captureRun`, per session), so a reconnect starts a new
  run directory by construction: the tests check one run before a drop and two after it, and that the first run's
  manifest is untouched.
- **A parent inside a git work tree is refused** (`captureRepoRefusal`, world/capture_run.go): the path is made
  absolute and every existing parent is checked for a `.git` (Lstat, so a worktree's `.git` file counts like a
  normal repository's directory). The check runs before `ensureCaptureDir`, so a refused parent creates nothing; and
  `Enter` and the `forever serve` command run the same check before any login (fix round, row 9), and `serve()`
  keeps it as a second guard.
- `capturedir.Stats(dir)` (internal/forever/capturedir) sums the manifests per core opcode: `[{opcode, name,
  packets, bytes}]`, named by `wire.ServerName` (an opcode this build does not name is `0x%06X`). It reads only
  `manifest.tsv`; the manifest parser is shared with `ReadRun` (`readManifest`), so the validation is the same and
  no payload is opened. `dir` may be one run directory or a parent of them, exactly as `ReadDir` takes it.

### Tests (W2)

- `TestCaptureRefusedInsideRepo` (failing case): a `.git` above the parent → refused, nothing created; the same
  parent outside a repository is accepted and gets its run directory.
- `TestServeTeardownGMOffThenLogout`: `gm off` → the steps are `gm ok`, `logout ok`; the wire order is `.gm off`
  before `CMSG_LOGOUT_REQUEST`; the daemon then ends cleanly.
- `TestServeTeardownGMKeepSendsNoLine`: `keep` sends no `.gm` line and still logs out.
- `TestServeTeardownDuringReconnect`: `-32005`, no `.gm` line and no logout request, and the daemon is still
  serving afterwards. The steps are found by name, not by position, because W4's `follow` step comes first.
- `TestServeTeardownStopsFollow` (W4): a started follow is stopped by the teardown (`follow ok` naming the loop's
  result) before the gm and logout steps.
- `TestServeCaptureNewRunOnReconnect`: one run before the drop, two after, the first run's manifest unchanged.
- `TestCaptureStatsManifestOnly` (capturedir): two runs, one payload file deleted before the read → the same counts;
  one run directory alone; a directory with no capture is `ErrNoRuns`.

The fake server gained one ordered marker (`serveOrder` gets `"logout"` on `CMSG_LOGOUT_REQUEST`), so the GM-line/
logout order is asserted rather than inferred. `TestServeNotPortedAndUnknown` now uses `teleport` instead of
`teardown` for its not-ported sample.

## W3. Serve `event_log` and `act`

### event_log

`event_log {since, limit<=1000, wait_ms<=2000}` → `{events, next, more, dropped}` (world/eventlog.go, registered in
world/serve_session_methods.go).

- The ring is `EventLog`: 20,000 events, a uint64 seq that only grows, pages of at most 1,000, and the ring's change
  channel read under the same lock as the page (so a long-poll cannot miss the event appended between page and
  wait). `dropped` counts the events that fell out between the caller's `since` and the oldest the ring holds.
- **It outlives a reconnect**: the ring hangs off the session (`s.evlog`) and `reconnect` hands `old.EventLog()` to
  `loginAgain`, which installs it before the re-entry's login dispatch runs (the same rule the combat ring follows),
  so a reader's `since` from before the drop still points into it and no seq is reused. The `session` events
  `disconnected` (on the old ring, before teardown) and `reconnected` mark the gap.
- Row shape: the `internal/events.Event` keys (`seq, time, mono_ms, kind, source, source_guid, target,
  target_guid, spell_id, amount, crit, party, text, data`), with a GUID as the `0x` + 32 hex text this client uses
  everywhere (a zero GUID is absent, so the key is omitted). `time` is the wall clock, `mono_ms` the steady axis.
- Kinds and their feed points (one row per packet effect, beside the model apply):
  - `session`: in-world (loginWorld), map changes (applyNewWorld, `data.from/to/reason`), reconnected/disconnected
    (serve.go).
  - `chat`, `whisper`, `system`: handleChat's `SMSGChat` (by chat type) and every `addNotice` line (the GM and `.tb`
    answers are system lines).
  - `party`: handleParty — invite, roster, member_stats, decline, kicked, destroyed, leader, command.
  - `addon`: offerAddon's accepted delivery (`data.prefix/channel`).
  - the combat ring's kinds copied (`combatRow` appends the same row to both rings: melee, spell_damage, spell_start,
    spell_go, spell_fail, periodic, heal, aura_applied, aura_removed, death; `data.reason` on a spell_fail).
  - `loot_open` (the window), `loot_roll_start`, `loot_roll`, `loot_roll_won`, `loot_all_passed` (handleLoot).
  - `gossip` (`data.menu_id/options`) and `gossip_complete` (handleNPC).
- `events` (the combat ring, the trials' raw rows) is unchanged: same ring, same shape, same page keys.
- The wait is spent off the session goroutine (`st.eventLogSnapshot` + the ring's `changed`), like `events`'s, and it
  answers while reconnecting (a read-only call).

### act

`act {steps, timeout_ms<=600000}` → `{lines, ok}`.

- Every step is parsed with `ParseActStep` first; one unparseable step refuses the whole call and nothing is sent.
- The steps run in order on the session goroutine through `RunActSteps` (exported from act.go), which the one-shot
  `act` command now calls after its settle. The lines are the ones `forever act` prints, but only the run's own: the
  step and menu lines go through `actLine` (which still forwards to the daemon's own sink) into a bounded list in the
  run's `actState` (see the fix round, row 4); `RunActSteps` no longer wraps `Options.Logf`.
- `ok` is false when a step fails: the fail line is already in `lines`, and the call is not an rpc error (a failed
  step is the script's result, exactly like the command's exit code). `timeout_ms` is the script's own budget; when
  it runs out the answer is `ok: false` with a last `act: the run ran out of its ... budget` line. A caller that goes
  away (`context.Canceled`) is the typed disconnect error.
- The act runner now also carries the round's verbs (W4 below).

### A vet find

Wrapping `Options.Logf` made `go vet`'s printf analyzer follow `Options.logf` into every caller, and it found a
latent `%.0f` fed an untyped `int` in `fightcheck.go`'s retreat line (it printed `%!f(int=25)`). Fixed with a
`float32(25)` in the same commit.

### Tests (W3)

- `TestEventLogKindsFromChatAndCombat` (failing case): one say, one GM reply and one on-demand melee reach the log in
  that order with kinds `chat`, `system`, `melee`; the melee carries both GUIDs as 0x+32 hex and the party flag. (A
  log fed only from the combat ring has neither the say nor the GM answer.)
- `TestEventLogPagingMoreDropped`: pages, `next`, `more`, `dropped` at the ring's overflow (100 rows lost from
  20,100 appends), and a reader ahead of the log.
- `TestEventLogSeqAcrossReconnect` (ring) and `TestEventLogAcrossReconnect` (daemon: history from seq 1 stays
  readable and the re-entry continues above the reader's seq).
- `TestEventShape`: the `internal/events` keys, the 0x+32 hex GUID and the omitted zero one.
- `TestServeEventLogPagingAndWaits`: the bounds, the login burst's rows, a waiting read answered by the next row.
- `TestServeActRunsSteps`: `wait` and `gm line=` answer their step lines, a step failure ends the run with `ok:false`
  and its fail line, and nothing after it runs.
- `TestServeActBadStepSendsNothing`: a script with a valid first step and an unknown verb sends nothing at all; the
  timeout and empty-script bounds are refused.

## W4. Mount, dismount and follow

### Opcodes (wire/opcodes.go)

| constant | core | wire (70707 and 70938) | core source |
|---|---|---|---|
| `CMSGCancelMountAura` | 0x3D0153 | 0x3E0155 | Opcodes.h:173; empty packet, SpellPackets.h:79-85; handled at SpellHandler.cpp:338 (`RemoveAurasByType(SPELL_AURA_MOUNTED)`) |
| `SMSGMountResult` | 0x45000F | 0x47000F | Opcodes.h:1891; one int32, 0 = success (SpellPackets.cpp:1156, `MountResult::Write`) |
| `SMSGDismountResult` | 0x450010 | 0x470010 | Opcodes.h:1444; `STATUS_UNHANDLED` in this core (Opcodes.cpp:1520): declared and parsed like MountResult, but this core never sends it |

Both tables gained a round-trip row (`TestClientOpcodeTranslation`, `TestServerOpcodeTranslation`), which also checks
`ServerName` for the two server opcodes. Mounted state is the character's own `UNIT_FIELD_MOUNTDISPLAYID` (retail
bit 61, decoded since OBJECTS-R1); speed changes while mounted are already acked by the death round's
`ackSpeed` (`SMSG_MOVE_SET_RUN_SPEED` and friends).

### mount / dismount (world/mount.go)

- `mount {"spell": N}` refuses a spell the character does not know, skips the cast when the field is already
  non-zero, then: ONE cast through `s.Cast` (the ordinary cast path), fail on a refused cast outcome, wait up to 4 s
  for the field to go non-zero or a non-zero `SMSG_MOUNT_RESULT` (which is the refusal and is named with its value).
  Not mounted after the wait is an error that says the spell was cast once. `attempts` is 1 (0 when no cast was
  needed). (This replaced a three-cast retry loop; see the fix round, row 13.)
- `mount {"entry": N}` is the item form: it needs `CMSG_USE_ITEM` (round R2, `items/packets.go BuildUseItem`), which
  is not on `origin/main` — `internal/forever/items` has only the R1 inventory snapshot (the `BuildUseItem` in
  `internal/game` is the 1.12 client library, not the Forever world). So it answers
  `mount: an entry mount needs item use, which this server does not port yet` and sends nothing.
- `dismount` sends the empty `CMSG_CANCEL_MOUNT_AURA`, then waits up to 2 s for the field to reach zero; a
  non-zero `SMSG_DISMOUNT_RESULT` (if a core ever sends one) is the refusal; a character on foot answers without a
  packet.
- `SMSG_MOUNT_RESULT`/`SMSG_DISMOUNT_RESULT` are parsed in the combat dispatch (`ParseMountResult`: exactly 4
  bytes) into a per-session `mountWatch`, reset before each cast/cancel.
- `unit` rows gained `mount_display_id` (the model's field for that unit), which is how `mount`/`dismount` and the
  live checks read the state; `status` is unchanged.

### follow (new internal/forever/follow, world/follow_session.go)

- `follow.Run(ctx, Game, Config)` — `Game{Self, Unit(name), Walk, Stop, Map}`, `Config{Lead, Near 3, Far 8, Lost
  30 s, Tick 250 ms}`. Beyond `Far` it walks once to `Near` behind the leader (`Behind`: opposite the leader's
  facing in the core's convention `(cos o, sin o)`, at the leader's height); inside `Near` it stops a walk that ended early; between the two it does nothing
  (the hysteresis the failing case pins). A leader on another map, or unseen for `Lost`, ends the run as `lost`; the
  caller's context ends it as `stopped`; a walk/stop that keeps failing for the retry window ends it as `failed` with the
  reason (a single refusal is retried, see the fix round, row 12). No chat, no GM.
- Serve: `follow_start {lead, near, far, lost_s, tick_ms}` refuses a second loop, validates the band, the window and
  the tick, and runs the loop on its own goroutine against a *queued* Game (`followQueuedGame`): reads go through
  `callDead` (they answer while reconnecting), `Walk`/`Stop` through `call`, so a walk is the same `walkToPoint` the
  `walk_to` method runs and a cancel reaches it. `follow_status` (read-only, answers while reconnecting) and
  `follow_stop` (cancel, bounded wait, the loop's result) report `{running, state, lead, near, far, lost_s, tick_ms,
  lead_seen, lead_yd, map, walks, stops, stopped_reason, detail, started, seconds}`.
- In serve, `Unit` resolves the leader the way `unit` resolves a name (cached player name, then every visible unit by
  name). This client's model is one map's, so a leader who changes map simply leaves it. The loop ends as `lost` at once
  when this client changed map or the roster zone of the leader changed after they went out of view (`MapEpoch`,
  `ZoneReader`; fix round, row 11), and otherwise after `lost_s`.
- Act verbs: `mount spell=N`, `mount entry=N`, `dismount`, and `follow lead= [near= far= lost_s= tick_ms= dur=]`,
  which runs the loop on the step's own goroutine (`followDirectGame`) until the leader is lost or `dur=`/the run
  deadline ends it; a lost or failed loop fails the step.

### Tests (W4)

- `TestFollowStopsInsideNearNotFar` (failing case): a leader 5 yd away after a catch-up sends no walk (and no stop);
  a loop that walks above `Near` fails. `TestFollowWalksBeyondFarToNearBehindLeader` pins the destination (0, 7, 0)
  for a leader at (0, 10) facing north, and `Behind` for a leader facing east.
- `TestFollowLostOnMapChange`, `TestFollowLostWhenUnseen`, `TestFollowWalkFailureEnds` (follow package).
- `TestMountSpellMounts` (cast → field update → mounted, display 14338, one attempt).
- `TestMountResultFailure` (a go followed by `SMSG_MOUNT_RESULT 3` is a failure naming the result).
- `TestMountEntryNeedsItemUse` (the entry form answers `needs item use`, sends no cast; both/neither is refused).
- `TestDismountSendsCancelMountAura` (mounted from the login burst: the cancel is sent and the field goes to zero; a
  second dismount sends nothing more).
- `TestServeFollowStartStatusStop` (start/status/refusal/stop and the bounds).
- `TestServeActMountDismount` (the act verbs' lines, the cancel packet, and a follow step failing as `lost`).
- Opcode round-trip rows for the three new constants.

## Fix round: the two reviews' findings

One round for all 15 findings of the cold review and the three of the Codex adversarial review. Each row names the fix and the test that fails on the code the reviews read
(the tests are in `world/serve_w2w4_fix_test.go` unless named otherwise; the runtime ones were run against the
reviewed tree and failed there, the ones that need a new symbol fail to compile on it).

| # | Fix | Test |
|---|---|---|
| 1 HIGH (Codex high) | `actGM` runs every line through `serveGMUnitLine("act", line)` when the session is serving, so a unit line is refused exactly where the raw `gm` method refuses it (`--allow-gm-units`, `trial_units`, own character only) and nothing, not even the GM probe, is sent. The one-shot `forever act` has no daemon and keeps the command guard. | `TestServeActGMRefusedWithoutTheFlag` (`act gm line=.die` on a selected unit refused, no line on the wire, `.tb about` still runs), `TestServeActGMObeysTheAllowlist` |
| 2 HIGH | `follow.Behind` is `(x - cos o * near, y - sin o * near)`, the core's `atan2(dy, dx)` convention (`objects.OrientationTo`, Position::GetAbsoluteAngle). Worked example from the core's math: a leader that walked (0,0) to (0,10) faces `atan2(10,0) = pi/2`, so behind them is (0,7); facing +X (o=0) at (0,10) it is (-3,10). The old test pinned the sin/cos convention and mirrored the code; it is rewritten. | `TestFollowWalksBeyondFarToNearBehindLeader`, `TestBehindUsesTheCoreConvention` (all four facings, orientation from `objects.OrientationTo`) |
| 3 HIGH | `serve()` copies `--navmesh`/`--parent` into the session options (`NavmeshDir`, `Parents`), which every re-entry inherits, so `walkToPoint` (follow, `act goto`, `act npc`) plans on the navmesh. `follow_start` and `act follow` without a navmesh are refused with `no --navmesh: ...` (act goto/npc already were). | `TestServeFollowUsesTheNavmesh` (leader off the mesh: no straight-line walk, the follow keeps running), `TestServeFollowWalksToTheLeader`, `TestServeFollowNeedsANavmesh` |
| 4 | `RunActSteps` no longer wraps `Options.Logf`. The step lines (`actOK`/`actFail`/menu options) go through `actLine`, which still feeds the caller's sink and keeps its own copy: at most 500 lines (`actLinesMax`, then one `act: N more line(s) were not kept`), each cut at 512 bytes. No other handler's line (chat, party, world) can reach the answer. | `TestServeActAnswerHasOnlyTheRunsLines`, `TestActLinesAreBounded` |
| 5 | `timeout_ms` 0 is the default budget `serveActDefaultBudget` (60 s), never none. A script's `follow` needs `dur=` (refused otherwise), and a follow step ended by the run's deadline fails the step, so the answer ends with the `ran out of its ... budget` line. | `TestServeActDefaultBudget` |
| 6 | `follow_stop` and `teardown` (not a dry run, valid `gm`, link up) call `cancelFollow()` from the RPC goroutine before they queue (`serveCallTypedFirst`); the walk in flight watches the loop's context, so it ends and the queued call gets its turn. The teardown step still reports `follow ok (follow stopped ...)` for a loop it cancelled (`run.cancelled`). The loop's context is derived from the daemon's (`st.followCtx`), cancelled and joined (2 s) on every `serve()` exit. | `TestServeFollowStopCancelsAWalkInFlight` (55 yd walk, the stop returns in under 3 s instead of about 7), `TestServeTeardownCancelsAWalkInFlight`, `TestServeTeardownDryRunLeavesTheFollow`, `TestServeFollowEndsWithTheDaemon` (Codex) |
| 7 | `event_log` reads `st.session.Load().EventLog()` directly under the ring's lock, not through the session queue; the ring is created in `newSession` (it was lazy, which would race). | `TestServeEventLogAnswersDuringAnAct` (a poll behind a 2 s act answers at once) |
| 8 | One cap per serve slot: `captureBudget` (shared by every link through `Options.captureBudget`) holds the same 8192 files / 32 MiB bounds for the whole slot plus 64 run directories (`maxCaptureRuns`); a spent slot creates no new run directory. A standalone session keeps the per-run bound. | `TestCaptureBudgetCoversTheWholeSlot`, `TestCaptureSpentBudgetCreatesNoRun` |
| 9 | `Enter` checks the parent and sets `Options.CaptureDir` (and the budget) before any session exists, so the first login's packets are captured; `forever serve` gained `--capture-dir DIR` and checks it before the password, the REST login and the listener are touched (`world.CheckCaptureDir`). `serve()` keeps its own check as the second guard. The notes' earlier claim that the refusal came before the login is now true. | `TestServeCaptureCoversTheFirstLogin` (the login burst's row is in the first run's manifest), `TestServeCaptureRefusedBeforeTheLogin`, `TestServeCaptureDirRefusedBeforeTheLogin` (cmd) |
| 10 | `follow.Config.Self`: a lead equal to the character (any case) is refused at once, by `follow_start`, `act follow` and the loop itself. **Mutual follow cannot be detected from one side** (each client only sees positions); it is documented and bounded: each walks to the other's back once and then both stand inside `far`, so the pair settles instead of chasing. | `TestFollowRefusesItselfOverServe`, `TestFollowRefusesItself`, `TestFollowMutualPairSettles` (follow) |
| 11 | The other-map rule now works in serve. A visible unit is always on this client's map (the model is one map's), so the loop gets two real signals instead of the fabricated `Unit.Map`: `MapEpoch` (this client's map-change counter, continuous across a reconnect through `serveState.mapEpochBase`) and `ZoneReader` (the party roster's last zone for the leader). A leader that has gone out of view after this client changed map, or whose roster zone changed, ends the loop as `lost` at once; a leader merely out of view in the same zone still gets the `lost_s` window. | `TestFollowLostWhenOwnMapChanged`, `TestFollowLostWhenTheLeaderLeftTheZone` (follow), `TestFollowEpochSurvivesAReconnect`, `TestFollowZoneFromTheRoster` |
| 12 | A walk or a stop that fails (a refusal, a link that is reconnecting) is retried, one attempt per `RetryGap` (1 s), the retry going to the leader's own position instead of the point behind them, until the failures have lasted `Retry` (default `lost_s`); only then `failed`. A caller cancel mid-walk is `stopped`, not `failed`. | `TestFollowRetriesARefusedWalk`, `TestFollowWalkFailureEndsAfterTheRetryWindow`, `TestFollowCancelDuringAWalkIsAStop` (follow), `TestServeFollowSurvivesAReconnect` |
| 13 | `mount` casts only a spell in the character's known list (`SMSG_SEND_KNOWN_SPELLS`), once, and waits 4 s (`mountWait`) for the display; the failure says `cast once and no mount display came ... (is it a mount spell?)`. An already-mounted character is answered without a cast (`attempts: 0`). A taxi flight shows a display too and is answered the same way: the model has no taxi state, so this is a documented limit. | `TestMountRefusesAnUnknownSpell`, `TestMountCastsOnce` |
| 14 | `ServeEventPage` carries `epoch` (a random id per ring life) and `reset`: a cursor above the ring's seq reads from the start with `reset: true`. The ring-level contract "a reader ahead of the log gets nothing" is replaced by this. | `TestEventLogRestartMarker`, `TestServeEventLogRestartMarker`; `TestEventLogPagingMoreDropped` updated |
| 15 | Tests for each case above, plus a real moving leader in the fake (`serveLeaderName`/`serveLeaderX`, a named creature on a navmesh strip) so follow is judged over a real walk, a stop, a teardown and a link drop; `serveKnownSpells` and a cast counter for mount. | the rows above |
| Codex 3 | The follow context is the daemon's (row 6); the leader's map is no longer fabricated (row 11). Codex also asked to cancel the loop on a link drop: this round keeps it running through a reconnect, as decided (row 12). | |

Existing tests that started a follow now give the daemon a navmesh strip (`followNavDir`), and the mount tests give
the fake a known spell.

## Gates (the original W2-W4 commits)

Run on commit `25b415f1` (the last code commit; a clean tree, `vm-test` reported `dirty=0`; the commit after it
changes only this notes file). The laptop gates:

```
$ make fmt-check
(no output)
$ make cross
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./cmd/... ./internal/forever/...
$ staticcheck ./...
(no output)
```

The full suite on the VM:

```
$ vm-test ta <worktree> -- go test -count=1 -timeout 20m ./...
RESULT: PASS  kind=ta sha=25b415f1 dirty=0 files=2334
rc=0  vm_duration=2245s  laptop_wall=2273s
    (in its log: `ok github.com/pjerra/forever-agent/internal/forever/world 595.106s`)
```

The touched packages under `-race` did not get a VM slot: `vm-test` has 3 and the box carried 8 queued jobs from
other worktrees (load average 11-43), so this run sat in the script's slot-wait loop from 04:10 to 05:26
(`vm-test.log` untouched since its header) and was cancelled rather than left orphaned. The same command ran on the
laptop against the same tree:

```
$ go test -race -count=1 -timeout 30m ./internal/forever/world ./internal/forever/follow ./internal/forever/wire ./internal/forever/capturedir
ok  	github.com/pjerra/forever-agent/internal/forever/world	597.864s
ok  	github.com/pjerra/forever-agent/internal/forever/follow	1.211s
ok  	github.com/pjerra/forever-agent/internal/forever/wire	1.144s
ok  	github.com/pjerra/forever-agent/internal/forever/capturedir	1.103s
```

**The full-suite gate runs with `-timeout 20m` instead of `vm-test`'s default command.** The default is `go test
-count=1 ./...`, whose per-package timeout is Go's 10 minutes; `internal/forever/world` alone took 570-578 s on this
VM before this round (the earlier logs of 2026-11-21) and 595.106 s now, so the default leaves five seconds of
margin on an idle VM and none on a shared one. The repository's own `make test` passes `-timeout 20m` for the same
reason; the command used here is that test step without `-race`, which the next row runs on the touched packages
instead of the whole tree.

## Gates (fix round)

Run on commit `d407f31b` (the last code commit; a clean tree, `vm-test` reported `dirty=0`; the commit after it only
changes this notes file). Laptop gates, on the tree before the last two small commits (the first changed nothing but
`eventlog.go`'s nil fallback, the second only the typed client and its tests; neither touches formatting-sensitive
code, and `vm-test` below ran `go vet ./...` on the final tree):

```
$ make fmt-check
(no output)
$ make cross
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./cmd/... ./internal/forever/...
$ staticcheck ./internal/forever/... ./cmd/...
(no output)          # after removing the unused followFinished that the first run reported
```

The full suite on the VM, default command (`go test -count=1 -timeout 20m ./... && go vet ./...`):

```
$ vm-test ta <worktree>
RESULT: PASS  kind=ta sha=d407f31b dirty=0 files=2346
rc=0  vm_duration=755s  laptop_wall=765s
```

Two earlier runs on this branch failed and led to fixes: `def25e47` panicked in a test that builds a `session` by
struct literal (the event ring had become create-in-`newSession` only; the nil fallback was restored for hand-built
sessions), and the next run (`a2cc8143`) failed `TestServeClientTypesMatchTheDaemon` and `TestServeClientMethodPin`
of the typed client (#47), whose pins said "when event_log, act, teardown land, this fails". They landed, so the client
now matches the daemon (`event_log` page `dropped` is a count, with `epoch` and `reset`; `unit.mount_display_id`;
teardown's `failed/seconds/character/dry_run`), key-compared against the daemon's own types, and the pin test now
decodes `event_log`, `act` and `teardown` against the real daemon (commander stays not-ported).

The race run, on the laptop, touched packages plus the client and the command package, same tree:

```
$ go test -race -count=1 -timeout 30m ./internal/forever/world ./internal/forever/follow ./internal/forever/wire \
      ./internal/forever/capturedir ./internal/forever/serveclient ./cmd/forever-agent
ok  	.../internal/forever/follow	2.378s
ok  	.../internal/forever/wire	1.308s
ok  	.../internal/forever/capturedir	1.631s
ok  	.../internal/forever/serveclient	1.753s
FAIL	.../internal/forever/world	703.560s    (TestRouteWalkDeviates, TestRouteWalkGPShidden, TestRouteWalkClientConfirmation)
FAIL	.../cmd/forever-agent	1771.532s       (TestTuneSetAndFileInARemoteRun)
```

No `DATA RACE` anywhere in the output. The four failures are the known load flakes (the box carried a load average
of 44 for the whole run: fake-server `broken pipe` / `no create block arrived` in the route-walk tests, a 10 s
timing in the tune test); each was rerun once, alone, under `-race` and passed:

```
$ go test -race -count=1 -run 'TestRouteWalkDeviates|TestRouteWalkGPShidden|TestRouteWalkClientConfirmation' ./internal/forever/world
ok  	.../internal/forever/world	12.565s
$ go test -race -count=1 -run TestTuneSetAndFileInARemoteRun ./cmd/forever-agent
ok  	.../cmd/forever-agent	4.577s
```

## Live checks for the lead's live queue (no live run in this round)

W2 — teardown and capture (one serve slot and a GM account):

1. `forever serve --dir D --account ... --character Testchar --allow-gm-units` (plus `--navmesh`), wait for
   `online`.
2. `forever rpc D teardown '{"gm":"off"}'` → `steps: [{gm ok}, {logout ok}]`, `failed: 0`; the process exits and
   `agent.pid` is gone. Then `teardown D` (the CLI) on a fresh serve slot → all steps ok, the lease released, and
   `slot.json` shows `"server": "forever"`.
3. `--capture-dir` (a `forever serve` flag since the fix round) pointed at a directory outside any repository: start, chat and fight
   30 s, kill the world socket once (or `.server shutdown`-free: drop via the network), see the reconnect log and a
   second run directory appear; `forever capture-stats DIR` (K2) or `capturedir.Stats` prints one row per opcode
   with packets and bytes and never a payload. Pointing the parent inside a checkout must refuse at startup.

W3 — event_log and act:

4. `forever rpc D event_log '{"since":0,"limit":200}'` after the login: `session` `in_world` with the map, the
   login's system lines; then `.tb about` through `act` and see the `system` rows; fight one mob and see `melee`/
   `spell_*` rows in the same stream in order.
5. `forever rpc D act '{"steps":["wait dur=1s","gm line=.gps"],"timeout_ms":30000}'` → `ok: true` and the same lines
   `forever act --step ...` prints. Repeat with a vendor/quest verb when R3-R9 land.
6. Long-poll: `event_log {"since":N,"wait_ms":2000}` while a bot says something → the answer carries the say at once.

W4 — mount, dismount, follow (X2):

7. A GM character with a learned riding spell: `mount {"spell":N}` → `mounted: true`, display non-zero; ride 50 yd
   (a `walk_to`/`walk_path` call) and watch the `unit` row's `mount_display_id` stay non-zero; `dismount` → mounted
   false within 2 s and the display zero; check the speed ack traffic once.
8. Two serve sessions: session B `follow_start {"lead":"<A's character>"}`, A walks 60 s (through A's own
   `walk_path`), then read `follow_status` on B (lead_seen, lead_yd inside the band, walks > 0) and `follow_stop`.
   Take A to another map and see B's loop end as `lost` (after `lost_s` if the model no longer holds A).
9. `act '{"steps":["follow lead=<A> dur=30s"]}'` on B → `ok: true`, walks counted, no chat or GM line on the wire.
