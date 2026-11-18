# Trials scoring half in Go

`internal/trials` is a faithful translation of the scoring half of the Python trials bench
(tortoise-trials), and `forever-agent trials` exposes it. Nothing here runs a trial: the live
runner, leases, slots, setup/teardown and `build_data.py` are **not ported**.

The reference is tortoise-trials `main` at `645642c`. The translation keeps function and rule
names recognisable (Go naming style), every rule citation string and "expects" text byte for
byte, and the same JSON and text output.

## Modules

| python (lines) | go package | go lines | state |
|---|---|---|---|
| `trl/events.py` (453) | `internal/trials/events` | 660 | translated |
| `trl/scenario.py` (1025) | `internal/trials/scenario` | 1998 | translated (validate, variants, selectors) |
| `trl/rules.py` (1467) | `internal/trials/rules` | 2410 | translated |
| `trl/score.py` (548) | `internal/trials/score` | 905 | translated |
| `trl/report.py` (446) | `internal/trials/report` | 977 | translated (card, compare, pr-block) |
| `trl/_wr/stats.py` (71) | `internal/trials/wr` | 128 | translated |
| `trl/_wr/compare.py` (62) | `internal/trials/wr` (`note_for`), `internal/trials/report` (`compare`) | — | translated |
| `trl/naming.py` (6) | `internal/trials/naming` | 14 | translated |
| `trl/spells.py` (99) | `internal/trials/spells` | 135 | translated, reads the embedded `data/spells.json` |
| `trl/gear.py` (117) | `internal/trials/gear` | 263 | translated, reads the embedded `data/gear_kits.json` |
| `trl/talents.py` (111) | `internal/trials/talents` | 148 | translated |
| `trials.py` score/report/compare | `cmd/forever-agent/trials_cmd.go` | 169 | translated |

Supporting pieces with no Python counterpart:

| go package | why |
|---|---|
| `internal/trials/pyjson` | Python JSON semantics: sorted keys, `indent=2`, `sort_keys` lines, int/float kept apart, Python float repr and banker's rounding, Python 3.12 compensated `sum()`. The card is compared byte for byte with the Python tool, so Go's `encoding/json` float spelling (`1` for `1.0`) is not enough. |
| `internal/trials/data`, `internal/trials/trialdefs` | `go:embed` of the reference `data/*.json` and `trials/<group>/*.json` (plus `trials/_fixtures`), copied unchanged. |
| `internal/trials/testdata/live1..3` | the reference `tests/fixtures-live1..3`, copied unchanged. |
| `tools/trialsparity` | the equality harness (see below). |

## Subcommands

```
forever-agent trials score <run dir>                    # like `trials.py score`
forever-agent trials report <run dir>                   # re-render card.md/card.json, print the card
forever-agent trials compare <run dir> --baseline <dir>  # like `trials.py compare`
```

`score` prints `scored <dir>: N FAIL, M INVALID` and exits 1 on any FAIL, 3 on any INVALID
(checked against the Python tool on recorded runs). `compare` prints the same table as the
Python `compare`, Fisher p and notes included. Python has no `report` subcommand (its `score`
writes the card): `report` calls the same `write_card` over the run's stored `trial.json`
files, without re-scoring, and prints `card.md`; it is idempotent on a scored run.
`trials run` and `trials cleanup` are not ported and the help says so.

## Tests

All translated tests live under `internal/trials/tests/<group>/`, one Go file per Python file,
one Go test per Python test method (`Test<Class>_<Method>`), one package per group. The shared
`support_test.go` in each package has the file/JSON helpers; every other helper carries a
per-file prefix so the files of one package share one namespace.

| python test file | go package / file | translated | not translated |
|---|---|---|---|
| `test_trials.py` | `tests/trialscore/` `trials_test.go`, `r3_test.go` | 92 / 101 | 9 |
| `test_rule_options.py` | `tests/ruleopts/rule_options_test.go` | 21 / 21 | — |
| `test_victim_packets.py` | `tests/ruleopts/victim_packets_test.go` | 17 / 17 | — |
| `test_cc_gap.py` | `tests/ruleopts/cc_gap_test.go` | 2 / 4 | 2 (live `Situation.run`) |
| `test_cc_target.py` | `tests/ruleopts/cc_target_test.go` | 0 / 5 | 5 (live `Situation`/`engage_cc_targets`) |
| `test_followups.py` | `tests/ruleopts/followups_test.go` | 6 / 23 | 17 (live run/pacer/teardown) |
| `test_rules_healers.py` | `tests/classes/rules_healers_test.go` | 48 / 48 | — |
| `test_rules_tanks_dps.py` | `tests/classes/rules_tanks_dps_test.go` | 25 / 25 | — |
| `test_edge_trials.py` | `tests/edge/edge_trials_test.go` | 38 / 38 | — |
| `test_ooc_hurt.py` | `tests/edge/ooc_hurt_test.go` | 15 / 25 | 10 (live `run_ooc_hurt`) |
| `test_drink_settle.py` | `tests/edge/drink_settle_test.go` | 0 / 9 | 9 (live `run_ooc` meal wait) |
| `test_bench_fix_p1.py` | `tests/fixes/fix_p1_test.go` | 8 / 11 | 3 (live autogear) |
| `test_bench_fix_p2.py` | `tests/fixes/fix_p2_test.go` | 2 / 2 | — |
| `test_bench_fix_p3.py` | `tests/fixes/fix_p3_test.go` | 4 / 4 | — |
| `test_bench_fix_p3b.py` | `tests/fixes/fix_p3b_test.go` | 2 / 5 | 3 (live damage step/top-up) |
| `test_bench_fix_p3c.py` | `tests/fixes/fix_p3c_test.go` | 1 / 1 | — |
| `test_bench_fix_p3d.py` | `tests/fixes/fix_p3d_test.go` | 5 / 11 | 6 (live energy drain/guard) |
| `test_bench_fix_p3e.py` | `tests/fixesb/p3e_test.go` | 1 / 1 | — |
| `test_bench_fix_p4.py` | `tests/fixesb/p4_test.go` | 3 / 6 | 3 (live engage-marked) |
| `test_bench_fix_r2.py` | `tests/fixesb/r2_test.go` | 12 / 32 | 20 (live summon/visibility/prepull) |
| `test_bench_fix_r3.py` | `tests/fixesb/r3_test.go` | 8 / 8 | — |
| `test_sweep_fixes.py` | `tests/fixesb/sweep_fixes_test.go` | 0 / 21 | 21 (live guard/visibility/summon) |
| `test_bench_fix_1116.py` | `tests/fixesc/test_bench_fix_1116_test.go` | 6 / 12 | 6 (live `wait_rep_gap`) |
| `test_bench_fix_1116_dispel.py` | `tests/fixesc/` (comment only) | 0 / 9 | 9 (live setup/situations) |
| `test_addpickup_pooled.py` | `tests/pooled/addpickup_pooled_test.go` | 19 / 19 | — |
| `test_counterspell_pooled.py` | `tests/pooled/counterspell_pooled_test.go` | 3 / 3 | — |
| `test_json_safe.py` | `tests/pooled/json_safe_test.go` | 7 / 7 | — |
| `test_variants.py` | `tests/variants/variants_test.go` | 35 / 35 | — |
| `test_extra_bots.py` | `tests/variants/extra_bots_test.go` | 14 / 44 | 30 (live hire/setup/teardown) |
| `test_gear.py` | `tests/geartalents/gear_test.go` | 6 / 25 | 19 (live gear setup/strip) |
| `test_gear_proficiency.py` | `tests/geartalents/gear_proficiency_test.go` | 1 / 28 | 27 (live gear run, server deadlines) |
| `test_hire_talents.py` | `tests/geartalents/hire_talents_test.go` | 4 / 36 | 32 (live talent read-back) |
| `test_rough.py` | `tests/rough/rough_test.go` | 4 / 36 | 32 (live ladder/run/leases) |
| `test_live1.py` | `tests/rough/live1_scoring_test.go` | 4 / 27 | 23 (live collector/setup) |
| `test_live2.py` | `tests/rough/live2_scoring_test.go` | 1 / 22 | 21 (live selection/summon/cleanup) |
| `test_live3.py` | `tests/rough/live3_scoring_test.go` | 9 / 77 | 68 (live visibility/governor/pacing) |

419 tests translated (the packages hold 423 Go test functions: a few extra Go cases and split subtests). The test files outside this table are pure live-runner tests and are not
translated at all: `test_live.py` (81), `test_arena_lock.py` (7), `test_bench_pin.py` (11),
`test_cleanup_wiring.py` (9), `test_orphans.py` (6), and the `trl/live/*` cases of the
`test_live*` files above.

Why the rest is not translated: every one drives `trl/live/*` — leases, slots, the GM guard,
`Situation`/`Governor`/ladder, setup and teardown reads, the login pacer, or a fake turtle-agent
process. None of those modules is ported in this round, so a translation would test a test
double, not the port.

Adaptations made where the Python test's mechanism has no Go counterpart (cases and expectations
unchanged):

- The spell-table swap in `test_trials.py::test_interruptible_each_condition_alone` calls
  `spells.IsInterruptible` on the same four rows and `spells.SpellIsInterruptible` on an id the
  embedded table cannot know (the synthetic id 999 does exist in the real table).
- `test_trials.py` `cmd_validate` assertions became `scenario.Validate` assertions; the
  duplicate-rule-id scan lives in the unported `trials.py` CLI and is dropped.
- `test_bench_fix_1116.py::RepOrderTest` asserts rep order through `score.ScoreTrial`'s detail
  list instead of patching `score_rep` (no monkeypatching in Go).
- `test_variants.py::SnapshotTest` writes `trial.effective.json` itself (the snapshot writer is
  runner code); the two `inspect.getsource` assertions on the Python runner are dropped.
- `test_rough.py::FloorTest.test_the_four_shield_trials_set_floor_12...` is red on the pinned
  reference (the trial set grew after the test); the Go test uses the shipped data.
- Env-gated Python tests (`TRIALS_BOT_SRC`, `TRIALS_DBC_DIR`, `TRIALS_RULE_BRIEFS`) keep the same
  gates: they skip when the data is absent and pass when it is supplied.

## Parity

`tools/trialsparity --runs DIR --python-root DIR` copies each finished run folder twice, scores
one copy with the Python tool and the other with `internal/trials`, and diffs `card.json` and
`card.md` (and, when those match, every `rep*/verdicts.json` and `trial.json`). A folder without
`RUN.json`, or with a `RUN.json` newer than 10 minutes, is skipped. Run folders are read only:
every score writes into a temporary copy.

Result on the recorded nightly runs (paths passed on the command line, never in the repo):

| runs directory | compared | equal |
|---|---|---|
| full nightly set, one night | 58 | 58 |
| a second nightly set | 52 | 52 |
| the third, short set | 6 | 6 |

116 run folders compared, 116 equal, 0 differing; 1 folder skipped (no `RUN.json` yet: still
being written).

The normaliser path was checked the same way with `events.jsonl` deleted from the copies: the
card, every verdict file, `events.jsonl`, `samples.jsonl` and `events_summary.json` are
byte-identical to the Python ones.

## Known differences

- `readJSONL` skips a JSON line that is not an object. Python's `_read_jsonl` keeps it and would
  fail later on `.get`; no recorded run has such a line.
- `events.normalise` dedupes on the `seq` value as text, so the integer `3` and the string `"3"`
  collide (Python's tuple key keeps them apart). No run carries a string seq.
- `rules` compares rows by serialised content where Python compares object identity
  (`cand is not trig`); the normaliser dedupes identical rows, so only the row itself compares
  equal.
- Python's `round()` and 3.12's compensated `sum()` are reproduced exactly; `%g`/`%f` go through
  Go's `strconv`, which rounds the same way C's printf does.
- The Python bench's tool-dir globs (`trials/`, `trials/_fixtures/`) are replaced by the embedded
  file sets; a run folder is otherwise read exactly as before.
- Validation messages reproduce Python's `%r` quoting via `scenario`'s `pythonRepr`.
