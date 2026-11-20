# REVFIX-1: the merged-PR review's findings, checked against `main`

A review of the merged forever-agent PRs #13–#17 raised six findings. Each one below was checked against the code at
`origin/main` (109a2496, with #18) before anything was changed: what the code does, why the finding holds (or would
not), the fix, and the test that fails on `main` and passes with the fix. Every test was run on a worktree of
`origin/main` as well, so "fails before" is observed, not assumed.

## 1. (High) A cancelled session context broke the detached stop/cleanup — real

**Where.** `world/session.go`: `Enter` stored the caller's deadline-wrapped context in `s.ctx` and handed it, through
`context.WithCancel`, to the packet readers (`s.pumpCtx`, which became `s.ctx`'s child). Three paths deliberately
detach from it so a stop still reaches the server after an interrupt or an expired window:

* `driver/run.go`'s `Run.finish` (`context.WithoutCancel(ctx)`, the stop-fight wait and `.tb order party stay`);
* `driver/watch.go`'s `watchFinish` (the detached `.tb run stop` and its confirmation polls added in #16);
* `world/fb_dungeon.go`'s deferred `fbDungeonCleanupRun` (`context.WithoutCancel(ctx)` around `.bot lead off`,
  `.bot dungeon leave`/`cancel`, `.bot dismiss all`).

**Why it held.** The detached context only reached the driver's own poll loops. Every command still used the
session's:

* `SendCommand` → `Send` → `sendOn` → `sendOnCtx(s.ctx, …)`. `worldconn.armWrite` sees `ctx.Err() != nil` and sets the
  write deadline to `time.Unix(1, 0)`, so the frame is never written (and `contextError` reports the cancelled
  context, not a link error).
* `cmdLink.Pump` → `s.next` → `nextExcept`, whose select waits on `s.ctx.Done()` as well: with the caller's context
  cancelled, `next` returns `context canceled` before the reply can be read.
* the readers themselves: `startPump` read on `s.pumpCtx`, so a cancelled caller stopped both readers, and no packet
  could arrive for any later cleanup.

The existing deadline tests (`TestFBDungeonDeadlineDuringEntry`/`DuringWatch`) expired only the run's own inner
window (`fbDungeonRunContext` keeps a reserve), while `s.ctx` still had time; nothing cancelled the caller's context.

**Fix.**

* The readers run on `context.WithCancel(context.WithoutCancel(ctx))` and are ended by `shutdown` alone (which also
  closes the sockets), so the queue keeps filling for a detached cleanup.
* `command.Transport.SendCommand` now takes the caller's context; `cmdLink.SendCommand` → `session.SendCommandCtx` →
  `SendCtx` → `sendOnCtx`. The session's own `Send`/`SendCommand` pass `s.ctx`, so every existing path is unchanged.
* `cmdLink.Pump` reads with the run's context (`s.nextCtx`), and `nextExceptCtx` selects on that context. `drain`
  (the settle/poll wait behind the driver's clock and `fbWaitMapAway`) reads on its context too.
* `handle` answers the packets it owns on the wait's context: the time sync response, the active-mover complete and
  the teleport ack. A cleanup of up to a minute would otherwise die on the first time sync and stop reading replies.
* `logout` runs on a bounded detached context (`logoutTeardownWait`, 10 s) when the caller's context already ended,
  and reads on that context, so an interrupt still sends `CMSG_LOGOUT_REQUEST` and waits for the completion instead
  of failing on the first wait and relying on `abortLogin`.

**Tests.**

* `TestCmdLinkPumpWorksAfterTheSessionContextEnded` (world): the session's context is cancelled and a packet arrives
  afterwards; the pump's own context must be what decides. On `main` the pump returns `context canceled` at once.
* `TestDungeonRunInterruptStillStops` (world, real sockets, the D2 dungeon scene): the caller's context is cancelled
  from the second post-pull state report, while the run is in `FIGHT`. The stop path must still read its state
  reports and put `.tb order party stay` on the wire, and the run must end `interrupt`, not
  `interrupt:fight-unresolved`. On `main` the stay never reaches the fake server (and a cancelled-context send can
  tear a frame, which the fake also reports).
* `TestFBDungeonInterruptStillCleansUp` (world, real sockets, the A2 fb-dungeon scene): the caller's context is
  cancelled on the watch's first finder request, after the lead was handed to the tank. The deferred
  `fbDungeonCleanupRun` must still put `.bot lead off`, `.bot dungeon leave` and `.bot dismiss all` on the wire with
  no `CleanupErr`. On `main` every one of those sends fails on the cancelled session context.

## 2. (Medium) The wipe recovery bypassed `MaxPractice` and charged teleports — real

**Where.** `driver/states.go`'s `wipe` sent `Action{Kind: ActionRevive, Who: "party", Recovery: true}` with
`Practice: false`, then five `ActionTeleTo` actions with `Practice: true`.

**Why it held.** `actionStateGate` checks the budget only for `a.Practice`, so the party revive was free; each of the
five teleports carried `Practice`, passed `actionMoveGate` (which has no budget check at all) and pushed
`r.practice` past `MaxPractice`. A run with `MaxPractice = 0` still revived the whole party. DRIVER-R1-NOTES states
the intended accounting: one party revive that raises five bots costs five of the eight.

**Fix.** `Action.PracticeCost` (0 means one) is the number of bots a practice action raises. The gate refuses the
action when `r.practice + cost > MaxPractice` (so the raise never half-happens), `act` charges that cost, and the
wipe's party revive is `Practice: true` with `PracticeCost: len(cfg.Party)`. The recovery teleports are the same
rescue and carry no `Practice`. The watch's party revive keeps cost 1, which is what its own `rescues` counter
counts.

**Test.** `TestWipePracticeBudget` (driver): a budget of 4 refuses a five-member revive (`practice-budget`, no
revive, no teleport); a budget of 5 pays for exactly one rescue — a second wipe on another group is refused, one
party revive and five teleports went out, and the summary records 5. On `main` both cases run to `clear`.

## 3. (Medium) The final boss read was skipped inside the cooldown — real

**Where.** `driver/watch.go`: `watchBosses` returned at once when `now.Sub(w.bossAt) < time.Minute`, and
`watchFinish` called it unforced just before `watchSummary`. Its own doc comment already said "unless forced", but no
force parameter existed.

**Why it held.** A run ending within 60 s of the start read (or of the 20-event read) never read the final boss
states; the summary kept an earlier state, or none even for a `clear=true` run.

**Fix.** `watchBosses(ctx, w, force bool)`: the cooldown is skipped when `force` is true, which is what `watchFinish`
passes; the start read and the 20-event read pass `false`.

**Test.** `TestWatchFinalBossRead`: the boss is `alive` at the start read and `dead` after it; the watch ends at 30 s
(inside the minute). The final read must happen (2 instance reads) and the summary must record `dead`. On `main` only
one read happens and the summary says `alive`.

## 4. (Medium) `Run.finish` gave up on the fight after one failed report — real

**Where.** `driver/run.go`, the stop-fight loop: `if !r.pollDetached(fctx) { timedOut = fctx.Err() != nil && !r.lost;
break }`.

**Why it held.** A transient failure (`ErrBadReport`, one `FinishCallTimeout` 10 s `State` timeout) broke the loop
with `timedOut` false; the switch below then hit `case fightWasOn: timedOut = true`, marked the run
`fight-unresolved` and skipped the stay, with up to the whole `StopFight` budget (600 s) left. `watchFinish` already
sleeps and retries on the same error class.

**Fix.** The loop polls again after any failure that is not a lost link, sleeping the 2 s poll, until a valid report
says the party is out of combat, `Force` asks for the end, or either bound (the run clock's and real time) runs out.
Only the bounds set `timedOut`.

**Test.** `TestStopFightRetriesAFailedReport` (driver): the operator interrupts mid-fight and the stop path's first
state report fails; the stay must still go out once the fight ends (90 s), and the result must be `interrupt`. On
`main` the result is `interrupt:fight-unresolved` and no stay is sent.

## 5. (Low) `pbc.ParseEvent` accepted a non-numeric or negative bot id — real

**Where.** `pbc/pbc.go`: `id, _ := parseInt(fields[1])` and `uint32(id)`.

**Why it held.** `ParseLfg`, `ParseRoster` and `ParseDetail` return `ErrProtoMismatch` for an unreadable integer
field; `ParseEvent` turned `x` into bot 0 (which then never matches `req.BotID` in `fb_session.go`) and `-1` into
`4294967295`.

**Fix.** `err != nil || id < 0` returns `ErrProtoMismatch`. A literal `0` still means "no bot".

**Test.** `TestParseEvent` extended: `x`, `-1`, `1x` and `` are refused and `0` is kept. On `main` `x` parses
without error.

## 6. (Low) `Run.Watch` did not persist `r.instance` — real

**Where.** `driver/watch.go`: `watchResult` set `res.Summary.Instance = r.instance` and then replaced
`res.Summary` with `r.rec.sum` when a recorder exists; `watchSummary` copied the other fields into `r.rec.sum` but
not `Instance`, exactly as `syncSummary` does for a leading run.

**Why it held.** The written `.forever-run.json` always said `instance: 0`, whichever instance the party was in.

**Fix.** `watchSummary` sets `r.rec.sum.Instance = r.instance` before `SetWatch`.

**Test.** `TestWatchSummaryFile` now asserts the written summary's `instance` is the party's 1. On `main` it is 0.
