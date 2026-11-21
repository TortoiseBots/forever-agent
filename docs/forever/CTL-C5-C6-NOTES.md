# CTL-C5/C6 NOTES: `.tb cmd` order and task verbs

Rounds C5 and C6 of `FOREVER-AGENT-CONTROL-REPLAY-SPEC.md` (sections 2.3, 2.4, 4 C5/C6, 7 D3/D4). Base: forever-agent
`origin/main` `4e0aa2c0` (round C3, "Forever: the session daemon commands the bots (#62)"). Module base: the module's
`tbf/main` `02328c542`, whose P3 (`.tb cmd`, `forever/docs/CMD-P3-NOTES.md`) and P6 (task verbs,
`forever/docs/P6-NOTES.md`) packages are merged and were read; `forever/docs/TB-COMMANDS.md` section 14 and the two
handlers (`forever/src/host/cmd/TbCmdOrders.cpp`, `forever/src/host/cmd/TbCmdTasks.cpp`) were the source of the
fixtures' shape.

## What was done

1. **`internal/forever/command/tbcmd/cmd.go` (new): `Client.Cmd(ctx, verb, args...)`.** Builds
   `.tb cmd <verb> [args...]` and decodes the module's real answers into the slice-3 `OrderReport`:
   - an order verb (P3) answers one `cmd` record `verb= status=accepted bots=<n> result=<scope> <n>` and an end
     line `cmd-end count=1 records=1 verb= lead= bots=`;
   - a task verb (P6) answers `cmd verb= bot= status=accepted target=<32-hex>` and `cmd-end ... ok= refused=`;
   - `lootmethod` answers `cmd verb=lootmethod status=ok method= threshold=`;
   - `state` answers `task bot= busy= status=none|running|done|failed [verb=] [entry=] [text=]` records;
   - a refusal is the module's one-line `error code=... cmd=cmd [msg=]`.
   The typed report keeps the whole-call words (`OrderReport.Status`, `.Bots`, `.Result`, `.Lead`, `.Fields`) and
   turns `accepted` into the per-bot `queued` the slice-3 path uses, so a caller's `cmdctl.Result` does not depend
   on which path the verb's row picked. A named bot's target, and a task's `busy`/`entry`, stay typed/decoded. A
   `0x<32 hex>` guid the agent's own model writes is sent as the bare 32 hex digits (the module's grammar).
2. **`internal/forever/cmdctl/verbs.go`: `cmdPath`.** `cmdPath` joins `slice3Path` on the `OrderPath` interface
   (`Send` -> `tbcmd.Client.Cmd`). Adding the module's package stayed a verb-table change: the rows that already
   carried `Package: "P3"`/`"P6"` now send on their path. `NewTB` installs both paths.
3. **Probe flipped to real output (D3).** `cmdctl.TB.probe` asks once per package per session:
   - P3 present iff `.tb cmd` (no verb) answers `error code=missing-verb cmd=cmd`; absent on `unknown-verb`.
   - P6 present iff `.tb cmd state` answers a report; absent on `unsupported` or `unknown-verb` (see "The probes,
     after review").
   - Only definite answers are remembered, until a reconnect's `Reset()`; everything else is undecided and asked
     again. A caller that cancels the call gets its own context error back and nothing is remembered.
   A verb of a package that is not there answers `cmdctl.NotAvailableError` -> RPC `-32003` with
   `not-available: <verb>: needs module package P3|P6` (an undecided probe is the same error with `Retry` set). No
   probe line is sent for a cut verb, and no order line is sent for an unavailable one.
4. **Result mapping.** A `.tb cmd` whole-party answer (`result=party <n>`) is expanded from the card the run guard
   read, so `Result.Bots` names the bots the order went to (only when the module's count matches the card; a
   mismatch keeps exactly the module's own lines). A cmd answer never reports a refused per-bot line (the module
   refuses with the `error` line), so an arrived cmd report is accepted even when it names no bot (`lootmethod`).
   `cmdctl.BotResult` gained `Target`/`Extra`; `cmdctl.Result` gained `Fields` (whole-order keys such as
   `lootmethod`'s `method`/`threshold`); serve carries them as `target`, `extra` and `fields`.
5. **Kept.** An order is sent once: a timeout is reported, never resent. The run guard (D4) still reads the card
   first and refuses `run-active` unless `pause_run=true`; a cmd answer carries no per-bot `seq=`, so `wait_ms`
   cannot match `ord=<seq>` for a cmd order (`final` stays `{}`; the module's own `ord=` is visible in `.tb state`).
   Never control and watch on one character (unchanged, watch-side).

## Tests

- `tbcmd/cmd_test.go`: the pull/rest/revive/pullback accepted answers and their lines; the move `direct` flag
  (`TestMoveDirectFlag`); guid `0x` stripping (`TestTargetGuidStrips0x`); the module's refusal; the P6 task answer
  (bot + target + ok/refused); `lootmethod`'s fields; the `state` records (done/running/failed, text, busy, entry);
  bad reports (record verb vs end verb, count, task status); word injection (`#`, space, `=`, over-long) and the
  object-target `@,` grammar.
- `cmdctl/cmdctl_test.go`: round C5's failing case `TestSameVerbSameResultFromOrderAndCmd` (stay, follow, attack,
  pull through `.tb order party` and through the same table row moved to `.tb cmd`: same card, same one bot
  `queued`, one line out, no wait); round C6's `TestP6ProbeP3OnlyModuleMeansNotAvailable` (a P3-only module
  answers `unsupported` to the P6 probe: `loot`/`gossip` are `not-available: ... needs module package P6`, the P3
  verb still sends, one probe); the probe memory (`TestProbeCachedTwoCallsOneProbe`, `TestProbeResetOnReconnect`,
  `TestProbeMissAndReset`, `TestProbeVerdicts`) and what is never remembered (`TestProbeTimeoutIsNotRemembered`,
  `TestProbeTransportFailureIsNotRemembered`, `TestProbeCallerDeadlineIsNotRemembered`,
  `TestProbeCancelledCallIsNotRemembered`, `TestProbeOtherModuleErrorsAreNotRemembered`,
  `TestP6ProbeNotBoundIsNotCached`, `TestP6UsageTextIsNotAProbeAnswer`); `TestP6VerbsWithPackage`,
  `TestLootMethodFields`; the review round: `TestCmdOrderMustNameTheLeadItMeant`,
  `TestCmdOrderAnswerWithNoLeadIsAnError`, `TestCmdBotArgumentsAreCheckedBeforeSending`,
  `TestCmdOwnBotAndNumberArgumentsGoOut`, `TestStateAnswerIsOwnershipChecked`,
  `TestStateEntryZeroThroughTheController`, `TestCmdAnswerForAnotherVerbIsAnError`,
  `TestCmdRefusedRecordIsAnError`, `TestSentIsTheWireLine`, `TestGuardRunsAfterScopingToTheLead`,
  `TestVerbArityMatchesTheModule` (every cmd row against a table copied from the module's handlers) and
  `TestVerbArityIsEnforced`; plus `TestCmdOrderHeldByARun`, `TestCmdOrderNamesTheParty`, `TestAvailable`.
- `tbcmd/cmd_test.go` adds the reply binding: another verb's answer, no record, `status=refused`, an unknown status,
  disagreeing `bots=`/`ok=`/`refused=` tallies, `entry=0`; the fixtures are built from the module's reply format.
- `internal/forever/world/serve_fake_test.go`: `TestServeCommanderNotAvailable` now drives the real P6 probe and
  asserts the `-32003` message; `TestServeCommanderCmdTask` round-trips a P6 `loot` answer's `target` and
  `lootmethod`'s `fields` through the RPC (the probe line is `.tb cmd state`).

## The probes, after review

The first version of round C6 probed P6 with a bare `.tb cmd loot` and counted the `invalid` usage text of the task
package as "P6 present". That is wrong: the run grammar's own loot parser (`TbRunOrdersPure.h`, `ParseOrder` into
`ParseTaskOrder`) answers a bare `loot` with the same `invalid` shape on a P3-only module, so the text proves
nothing. The probes now are:

- P3: `.tb cmd` with no verb. `error code=missing-verb cmd=cmd` is present (`HandleCmd` checks the verb before the
  sender and the binding, `TbCmdOrders.cpp`); `unknown-verb` and `engine-not-built` are absent.
- P6: `.tb cmd state <bot>` with the first bot of the character's card (a bare `state` answers a record per bot,
  which a raid-sized party's answer would overflow in the probe; the bare verb itself is sent with the collector's
  full line cap), read only. A P6 module answers a `cmd-end verb=state` report (`HandleState`,
  `TbCmdTasks.cpp`); a P3-only module's order grammar lists `state` among the verbs it does not carry out
  (`ParseOrder`) and answers `error code=unsupported`. `unsupported`, `unknown-verb` and `engine-not-built` are
  absent. A `loot` usage text is never an answer.
- Only those definite answers are remembered (until the session is re-entered). A deadline or transport timeout,
  `not-bound`, `not-in-world`, `needs-player-sender` (the module checks the binding before it reads the verb), any
  other code and a reply of an unexpected shape leave the package undecided: the call answers
  `not-available: <verb>: the module's answer to the <package> probe is not known (...)` and the next call probes
  again. A caller whose own context ended gets its own error back. The probe has its own budget (3 s, or half the
  caller's remaining time), so a module that never answers does not use the caller's whole deadline. A P6 verb
  asked after P3 is known absent answers not-available without a probe.

## What the module's `.tb cmd` handlers fix, and what the controller checks

Module source facts (`forever/src/host/cmd/TbCmdOrders.cpp`, `TbCmdTasks.cpp`; `forever/src/host/run/TbRunOrdersPure.h`,
`TbRunTasksPure.h`):

- `.tb cmd` takes no lead. `HandleCmd` resolves the caller's own binding (`ResolveBinding`) and acts on it; the end
  line of a P3 answer names the lead it used (`lead=`). P6 answers name no lead; they name the bot (`bot=`) or, for
  `state`, one `task` record per bot. The controller therefore scopes a call to the named lead before the run
  guard reads anything, refuses a P3 answer whose `lead=` is not that lead (`lead-mismatch`, no bot reported), and
  accepts a card with no lead when the answer names one of the card's own members (the module's fallback, `ResolveBinding`),
  and checks every bot a cmd answer names, whatever its status, against the card, before and after the line.
- Argument counts (words after the verb): `move` 3-4 (`x y z [direct]`), `pullback` 1, `focus` 1, `rest` 0, `revive`
  1 (only `party` is carried out), `loot` 1-3 (`target [bot] [itemId]`), `open` and `use` 1-2 (`target [bot]`),
  `gossip` 1 and up (`target [bot] [option]`; the option is the rest of the line, the table takes up to 8 words),
  `state` 0-1 (`[bot]`), `lootmethod` 1-2. A test pins every cmd row against a table copied from the handlers.
- Bot words are checked against the character's cards before anything is sent: `state`'s bot, `loot`'s and
  `gossip`'s second word when it is not a number, `open`'s and `use`'s second word, `revive`'s word when it is not
  `party`. A gossip option text needs the bot named first, so the word is never read as a foreign bot's name.
- Replies are bound to the verb sent: the end line's `verb=` must be the verb, an order verb answers exactly one
  `cmd` record, the record's `status` is `accepted` or `ok` (`refused` is a refusal with the module's reason, any
  other word a bad report), the record's `bots=` must equal the end line's, and `ok=` must equal the record count
  with `refused=0`. `entry=0` in a `state` record is accepted.

## Live checks for the lead (live queue; no live run in this round)

Setup: the agent's GM character bound to a five-bot party (`.tb master party <lead> <self>`), engines on.
1. `.tb cmd move <x> <y> <z>` (or via `forever rpc DIR commander '{"op":"cmd","verb":"move","args":["<x>","<y>","<z>"]}'`):
   expect `sent` = `[".tb cmd move <x> <y> <z>"]`, `bots` = the five members `queued`; then `.tb state party` shows
   `ord=<seq>:ok` for each (the cmd answer itself has no seq, so `final` stays `{}`).
2. `.tb cmd loot my <lead>` on a fresh corpse: expect `status=accepted`, `bots[0].target` = the corpse's 32 hex;
   `.tb cmd state <lead>` a few seconds later shows `status=done verb=loot`, and repeating the loot shows
   `status=failed ... Nothing is left to loot`.
3. A pre-P3 module: `commander` with `{"op":"cmd","verb":"move",...}` answers `-32003
   not-available: move: needs module package P3` after one `.tb cmd` probe; the P3 verbs never send a line.
4. P6 on the merged module: the first P6 call probes `.tb cmd state` once (expect a `cmd-end verb=state` report) and
   then sends the real task line. A P3-only module answers `unsupported` to the probe: the P6 verbs are
   not-available while P3 verbs still work. Live-check the P3-only answer's code on a real pre-P6 build.

## Gates

All run on the clean committed tree (`dirty=0` in the VM run's own line).

- `make fmt-check`, `make cross` and `staticcheck ./...`: rc 0, no output.
- `go test -race -count=1 ./internal/forever/command/tbcmd/ ./internal/forever/cmdctl/` and the `world` package's
  commander tests: ok.
- The VM run (`vm-test ta`) on the committed code: `RESULT: PASS kind=ta sha=e52d3e83 dirty=0`.
