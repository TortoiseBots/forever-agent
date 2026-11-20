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

## Out of scope (A3-A5)

Setup toolkit and provisioning, `tbcmd/testkit.go`, `specs.json`;
tank_damage and other situations, arenas/mobs overlays, teardown kills
through `gm_kill` selection; live runs (needs a `forever serve` per slot,
which the runner does not start).
