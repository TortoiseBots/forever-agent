# Joint watch fixes: what the second watched run found

Scope: the second joint live run (this client watching a run a server module leads) ran a full watch and its report
card. Six agent bugs came out of it, and the review of that round found three more. Each is fixed with a test
that fails on the code before it. No character, account, host or path of the test environment is named here; the module
is "the server module" throughout.

## From the live run

1. **The plan was refused.** `internal/forever/world/dungeon_run.go` loaded the watch's plan with the plain route
   loader, and the plan's in-fight run fields (the module's) were refused. It now loads the full plan format
   (`LoadWatchRoute`). Test: the dungeon plan of the run loads unchanged.
2. **The setup reports were read as end-only.** `internal/forever/command/tbcmd/action.go` expected the end line to
   name each target per record and refused the module's `<verb>-begin`, per-bot lines and `target=` on the end line.
   It now reads that shape (see finding 1 of the review round below for what was still wrong).
3. **`.tb enter party` outside the entrance's trigger box** answers `no-trigger`. The driver now passes the plan's
   map and retries once as `enter party map <map>`, instead of failing the entry. No map id is hardcoded.
4. **`state-end ready=no resting=0 why=far:..;blocked:..` was refused** as a contradiction: the reasons were read as
   a list. `tbcmd/state.go` accepts that whole-state report and surfaces the reasons.
5. **The report card's death log named the pull's unit, not the member.** A death event carries the dead member in
   its `why=` and the fight's unit in `unit=`; the card now reads the member from `why=`. See finding 3 below for the
   names that match no member.
6. **A pause that never cleared sat until the time budget.** A run the module paused (a wipe's regroup, or its own
   reason) now has a pause budget (`--pause-minutes`, default 5): the watch reports it and ends as `PAUSED-STUCK`,
   with no automatic resume, and the watchdog covers a paused run too.

## Review round

- **fix** — a setup answer's whole shape is required (`tbcmd/action.go`): the `<verb>-begin` marker, a `status=` line
  per addressed character, the end line's `records=`, `target=` and its outcome counts, and a target that names what
  the command addressed. An end-only acknowledgement (an older module's `count=0`) is refused, never a success.
  Tests: `TestActionRepliesAreStrict`, `TestActionPartyTargets`.
- **fix** — a pause the practice rescue still owns is not the watchdog's (`driver/watch.go`): the watchdog and the
  pause budget stand aside until the rescue's threshold and, after a revive, for its 2×300 s resume window, and a
  `resume` that lands restarts the watchdog's event clock. Tests:
  `TestWatchWatchdogWaitsForTheWipeRescue`, `TestWatchdogWaitsForARevivedRunsResume`.
- **fix** — death attribution is checked against the current party (`driver/watch.go`, `reportcard/card.go`): a
  `why=` that names no member (a typo, an NPC, a stale roster, an empty value) is kept as it arrived with no role,
  the card flags it apart (`unattributed-deaths`) and no member's count or pack flag folds it in; the fight's unit id
  is evidence only. Tests: `TestWatchDeathNamesAreCheckedAgainstTheParty`, `TestCardDeathsNotAMember`.
