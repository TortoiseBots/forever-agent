# Trials live core, offline (round A2)

`internal/trials/live` is the offline half of the Forever trials runner: the
slot lease, the typed serve client, the GM allowlist, the ledger, the
monotonic marks clock, the 1 Hz collector and the dry-run trial loop. Real
setup (A3) and situations (A4-A5) arrive as `Setup`/`Situation` interfaces;
this round ships only the `--dry` scripted ones. `trials run` and `trials
cleanup` are ported; every other `trials` subcommand is unchanged.

## Layout

| file | mirrors | what |
|---|---|---|
| `slot.go` | `trl/live/lease.py` + slots file | `forever.serve_dir_root` block, `AcquireSlot`, 60 s renewal, release in defer |
| `client.go` | `trl/live/agent.py` | typed `status`/`units`/`unit`/`events`/`gm`/`chat`/`tbm`/`cast`; `-32005` retried 2 s up to 60 s, `-32002` ends the repeat |
| `guard.go` | `trl/live/gm_guard.py` | 17 ForeverCore templates (`npc_add_temp`, `go_xyz`, …); raw text classified and refused otherwise; the one `gm` call site |
| `ledger.go` | `trl/live/ledger.py` | `ledger.jsonl`, `trials cleanup` replays it |
| `marks.go` | `collect.py: Marks` | anchor wall + monotonic time (never steps back); `t` rounded to 2 decimals |
| `collect.go` | `collect.py: Collector` | pump `events` ≤ 900 ms (dedupe by seq, `events_dropped` mark), 1 Hz sampler with the exact `_rows` keys incl. `auras`, `mono_ms`; ≤ 4 calls/s |
| `run.go` | `trl/live/run.py` | `RunTrial`: `trial.effective.json`, per repeat setup → situation → collect → teardown, `RUN.json`, then `events.Normalise` + `score` unchanged |
| `dry.go` | — | `--dry` scripted setup (valid guids, one ok row per check id; `config_read` false without a Forever source, as Python) and situation (fight window, `complete`) |
| `cleanup.go` | `trl/live/teardown.py` | `CleanupRun`: kill ledgered summons per serve dir, skip rows with no dir |

## Row shapes

- Samples carry the Python keys exactly (`t wall unit guid hp max_hp power
  max_power auras casting in_combat target x y z`, plus `mono_ms` when the
  status row has it). `player` falls back to the `unit` rpc (units never
  lists self); `in_combat` prefers the status flag, then the own row, then a
  targeted hostile.
- `RUN.json` has `trial_snapshots: true`, `trials`, `repeats_run` and the
  `reps` rows, so `ScoreRunE` scores the run without today's trial files.
- `pyjson.Marshal` rejects typed slices (`[]map[string]any`); run docs
  convert to `[]any` before dumping. A dump failure is silent (`_ =`), so a
  round-trip test asserts every file exists.

## Decisions

- `client.go` matches the production `*slotRPCError` by shape (Code/Message
  fields) instead of importing the command: the live package stays decoupled
  from `cmd/forever-agent`.
- The dry setup writes `config_read` ok=false (no Forever source), mirroring
  the Python non-fatal default 20/50/70 path rather than inventing config.
- `writeRUN` counts the runner's own scored repeats; ladder/lock/foreign
  summons (A4 concerns) are not modelled.

## Tests

`fakeserve_test.go` is an `httptest` JSON-RPC server driven by a script
(queued answers, queued error codes, canned status/units/events). New tests:

| test | pins |
|---|---|
| `TestCollectorRoundTrip` | raw rows + samples → `events.Normalise` kinds `cast damage heal aura_on aura_off death`, then `ScoreRepE` |
| `TestGuardRefusesRaw` / `TestGuardOneCallSite` | closed set; the single `gm` call site |
| `TestLeaseHeldTwice` / `TestLeaseRenewAndRelease` | store semantics through the real `slots.Store` |
| `TestPumpDedupeAndDropped` | seq dedupe + `events_dropped` mark |
| `TestRunDryFull` / `TestRunDryScoresCard` | dry repeat writes every file (incl. `verdicts.json`) and scores |
| `TestRepeatLostOnDisconnect` | `-32002` ends the repeat |
| `TestSlotNoServeRoot` / `TestSlotAcquireRelease` | no `serve_dir_root` refuses; a dead serve releases the lease |
| `TestCleanupIdempotent` | ledger replay, skips for dir-less rows, repeatable |
| `TestHoldRenewErrSurfaces` / `TestSameHolderHeld` / `TestCleanupReleasesLeases` | renewal failure stays on `RenewErr`; same-holder re-run refused; cleanup frees leases |
| `TestMultiTrialRUNCard` | two trials land in one `RUN.json` and one card |
| `TestVariantMergeReal` | `trial@variant` merges through `scenario.MergeVariant` |
| `TestTrialsCleanupNoSlotsFileFailsLoud` (cmd) | cleanup without `--slots` fails loud on lease rows |

## Out of scope (A3-A5)

Setup toolkit and provisioning, `tbcmd/testkit.go`, `specs.json`;
tank_damage and other situations, arenas/mobs overlays, teardown kills
through `gm_kill` selection; live runs (needs a `forever serve` per slot,
which the runner does not start).

## Fix round A2 (review findings)

Codex's five and Gemini's nineteen, each verified against the code and the
Python runner, with a failing test first where the finding was real.

| # | finding | fix/reject (one line) |
|---|---|---|
| C1 | `cleanup` never releases slot leases | FIX: `CleanupRun` keeps killing summons through the guard; `ReleaseLedgerLeases` frees every ledgered lease or reports a failed row, and `trials cleanup --slots F` threads the file through (`cleanup.go`, `trials_cleanup_cmd.go`). |
| C2 | one renewal error silently stops protection | FIX: `Hold.RenewErr` surfaces the first failure; `Runner.Abort` cancels the repeat and the guard refuses further GM, and a failed `Release` fails the run (`slot.go`, `run.go`, `trials_run_cmd.go`). |
| C3 | typed `Send` skips argument validation | FIX: closed arity table, no whitespace/control/`{}` in args, rendered line must classify back to its kind; extra args refused (`guard.go`). |
| C4 | multi-trial card scores only the last trial | FIX: `noteTrial` accumulates every finished trial and `writeRUN` rewrites the full set each time; `TestMultiTrialRUNCard` pins two trials in one card (`run.go`). |
| C5 | run docs non-atomic, write errors dropped | FIX: `dumpDoc` is temp+fsync+rename and every `writeSetup`/`writeRUN` error aborts the repeat (`io.go`, `run.go`). |
| G1 | `trials run` ignores `signalContext` | FIX: `trialsCmd`/`trialsRunCmd` take the run `ctx`; interrupt aborts repeats and refuses GM (`main.go`, `trials_cmd.go`, `trials_run_cmd.go`). |
| G2 | `HolderPID` left 0, reap treats holder unknown | FIX: `store.HolderPID = os.Getpid()` on the run path, so a crashed runner reaps at once (`trials_run_cmd.go`). |
| G3 | `defer hold.Release()` races `KeepRenewed` | FIX: `done` closes before release (release defers first) and `RenewOnce`/`RenewErr` hold the mutex, so no renewal writes mid-release (`slot.go`, `trials_run_cmd.go`). |
| G4 | same-holder re-acquire silently renews | FIX: `SameHolderHeld` refuses a second `trials run --slot` under the same holder with exit 15; `--any` is unaffected (`slot.go`, `trials_run_cmd.go`). |
| G5 | `go_xyz` 4th arg optional-vs-required mismatch | REJECT: Python `gm_guard.py` makes the map optional too (`(?: (\d{1,2}))?`); the Go regex mirrors it and `additem_self` was never ported, so there is no divergence to fix. |
| G6 | `Guard.Send` skips classify/arg checks | FIX: same as C3, plus `SendText` requires the exact untrimmed line and `classifyArgs` revalidates (`guard.go`). |
| G7 | `cleanup` sends raw `.die`, bypasses guard | FIX: `killOne` goes through `Guard.Send("die", summon)`; `TestGuardOneCallSite` now greps `.GM(` so any new direct call fails it (`cleanup.go`, `live_test.go`). |
| G8 | `writeRUN` overwrites per trial | FIX: same as C4. |
| G9 | `itoa` breaks at rep 10+ | FIX: `strconv.Itoa` (`collect.go`). |
| G10 | `cleanup --slots` discarded | FIX: same as C1. |
| G11 | `ClassifyGuard` accepts wrapped newlines | FIX: exact-line match only; leading/trailing whitespace and control characters refuse (`guard.go`, pinned in `TestGuardRefusesRaw`). |
| G12 | dry `config` contradicts scorer | FIX: dry writes `config {ok false, critical/low/medium nil}` so the card reports `thresholds_assumed: true` like the Python path (`dry.go`). |
| G13 | dry situation resets clock, omits ended, 3 s default | FIX: no `Marks.Start` reset, `fight_end` carries `ended: complete`; default stays 3 s (REJECT the 120 s demand: dry has no real fight to score, `min_valid_s` comes from each trial file). |
| G14 | `applyVariant` ignores name/override | FIX: delegates to `scenario.MergeVariant` against the real trial shapes; `TestVariantMergeReal` pins it (`run.go`). |
| G15 | `dumpDoc` non-atomic, errors dropped | FIX: same as C5. |
| G16 | `Marks.Add`/ledger/collector races | FIX: file writes serialized (`fileMu`), hooks/`stop` under the collector mutex, sampler survives transient errors and only disconnects on `-32002`/pattern/state (`marks.go`, `ledger.go`, `collect.go`). |
| G17 | `TestGuardRefusesRaw` misses bypass vectors | FIX: prefixes, case, extra args, newlines, chained commands and `Send` arg injection all asserted (`live_test.go`). |
| G18 | `TestPumpDedupeAndDropped` injects the mark | FIX: the fake serves a dropped page and the test asserts the pump writes the mark (`fakeserve_test.go`, `live_test.go`). |
| G19 | `TestRunDryScoresCard` asserts no verdicts | FIX: asserts PASS on every rule verdict, and `TestCollectorRoundTrip` asserts PASS per rule (`live_test.go`). |
| G20 | `TestRepeatLostOnDisconnect` `&&` | FIX: asserts `collect.Lost() != ""` directly (the `&&` wording was wrong: the old test already failed when neither fired; the hook var is gone). |
| G21 | no `run`/`cleanup` execution tests | FIX: `TestCleanupReleasesLeases` and `TestTrialsCleanupNoSlotsFileFailsLoud` cover cleanup+release end to end (a live `run` needs a serve per slot, out of scope). |

## Fix round A2-R2 (Codex re-check findings)

| # | finding | fix (one line) |
|---|---|---|
| R1 | `KeepRenewed` is never started | FIX: the command starts `go hold.KeepRenewed(done)` before the run body and `finish` closes `done` before the explicit `Release`, so runs past the 30 min TTL keep the slot and a renewal failure aborts through `Runner.Abort` (`trials_run_cmd.go`; pinned by `TestTrialsRunStartsRenewal` + `TestTrialsRunRenewalFailureAborts`). |
| R2 | explicit same-holder acquire replaces the live lease | FIX: `Store.acquireLocked` refuses an explicit same-holder acquire atomically with an already-leased refusal, so the first run's lease ID (and its renewals/release) survives (`lease.go`, `live/slot.go`, `trials_run_cmd.go`, `lease_cmd.go`; pinned by `TestSameHolderReacquireRefusedAtomically` + `TestExplicitSameHolderAcquireKeepsFirstLease`). The `Acquired.Renewed` flag and the `already yours` renew-on-acquire path are gone: only `Renew` extends a live lease. |
| R3 | release failure cannot change the exit status | FIX: every return below the acquire goes through `finish`, which releases explicitly and returns 1 on a release failure (`trials_run_cmd.go`; pinned by `TestTrialsRunReleaseFailureFails`). |
| R4 | race: `Release` reads `h.Acq.Lease` beside `RenewOnce` | FIX: both read the lease identity under the hold mutex; `Release` runs only after `done` closes (`slot.go`; caught by the `-race` run). |
| R5 | slots file with a `forever` block fails to load | FIX: `slots.Config` decodes (and ignores) the top-level `forever` map, so `LoadSlotsFile` keeps reading `serve_dir_root` beside strict slot validation (`config.go`). |
