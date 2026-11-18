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
| `trl/events.py` (453) | `internal/trials/events` | 719 | translated |
| `trl/scenario.py` (1025) | `internal/trials/scenario` | 2045 | translated (validate, variants, selectors) |
| `trl/rules.py` (1467) | `internal/trials/rules` | 2452 | translated |
| `trl/score.py` (548) | `internal/trials/score` | 992 | translated |
| `trl/report.py` (446) | `internal/trials/report` | 977 | translated (card, compare, pr-block) |
| `trl/_wr/stats.py` (71) | `internal/trials/wr` | 128 | translated |
| `trl/_wr/compare.py` (62) | `internal/trials/wr` (`note_for`), `internal/trials/report` (`compare`) | — | translated |
| `trl/naming.py` (6) | `internal/trials/naming` | 14 | translated |
| `trl/spells.py` (99) | `internal/trials/spells` | 135 | translated, reads the embedded `data/spells.json` |
| `trl/gear.py` (117) | `internal/trials/gear` | 263 | translated, reads the embedded `data/gear_kits.json` |
| `trl/talents.py` (111) | `internal/trials/talents` | 148 | translated |
| `trials.py` score/report/compare/validate | `cmd/forever-agent/trials_cmd.go` | 208 | translated |

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
forever-agent trials validate [files...]                # like `trials.py validate`
```

`score` prints `scored <dir>: N FAIL, M INVALID` and exits 1 on any FAIL, 3 on any INVALID.
An input the Python raises on (a non-object JSONL line, an unhashable `seq`, a null walk mean,
trailing bytes after the snapshot) exits 1 with the error, naming the file and line where the
input is a file: never a silent verdict. `validate` prints one line per file (`path: ok` or the
errors) and exits 1 if any file fails; it adds the two checks `cmd_validate` has on top of
`scenario.Validate`: trial id == file stem, and no duplicate rule id across the set. `compare` prints the same table as the
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
| `test_trials.py` | `tests/trialscore/` `trials_test.go`, `r3_test.go` | 93 / 101 | 8 |
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
| `test_bench_fix_p1.py` | `tests/fixes/fix_p1_test.go` | 8 / 11 | 3 (live autogear, build-table pin) |
| `test_bench_fix_p2.py` | `tests/fixes/fix_p2_test.go` | 2 / 2 | — |
| `test_bench_fix_p3.py` | `tests/fixes/fix_p3_test.go` | 4 / 4 | — |
| `test_bench_fix_p3b.py` | `tests/fixes/fix_p3b_test.go` | 2 / 5 | 3 (live damage step/top-up) |
| `test_bench_fix_p3c.py` | `tests/fixes/fix_p3c_test.go` | 1 / 1 | — |
| `test_bench_fix_p3d.py` | `tests/fixes/fix_p3d_test.go` | 6 / 11 | 5 (live energy drain/guard) |
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
| `test_live1.py` | `tests/rough/live1_scoring_test.go` | 5 / 27 | 22 (live collector/setup) |
| `test_live2.py` | `tests/rough/live2_scoring_test.go` | 1 / 22 | 21 (live selection/summon/cleanup) |
| `test_live3.py` | `tests/rough/live3_scoring_test.go` | 8 / 77 | 69 (live visibility/governor/pacing) |

423 tests translated. The test files outside this table are pure live-runner tests and are not
translated at all: `test_live.py` (81), `test_arena_lock.py` (7), `test_bench_pin.py` (11),
`test_cleanup_wiring.py` (9), `test_orphans.py` (6), and the `trl/live/*` cases of the
`test_live*` files above.

Why the rest is not translated: every one drives `trl/live/*` — leases, slots, the GM guard,
`Situation`/`Governor`/ladder, setup and teardown reads, the login pacer, or a fake turtle-agent
process. None of those modules is ported in this round, so a translation would test a test
double, not the port.

Adaptations made where the Python test's mechanism has no Go counterpart (cases and expectations
unchanged):

- The spell-table swap in `test_trials.py::test_interruptible_each_condition_alone` has no Go
  test hook: the test picks one real id per condition from the embedded table and evaluates the
  same `spell_interruptible` rule through `rules.Evaluate`, asserting the rule's value (1/0).
- `test_bench_fix_1116.py::RepOrderTest` patches `score_rep`; Go passes the per-repeat scorer
  into `scoreTrialWith`, so the white-box test `internal/trials/score/order_test.go` observes
  the calls themselves, in order.
- `test_variants.py::SnapshotTest` writes `trial.effective.json` itself (the snapshot writer is
  runner code); the two `inspect.getsource` assertions on the Python runner are dropped.
- `test_rough.py::FloorTest.test_the_four_shield_trials_set_floor_12...` is red on the pinned
  reference (the trial set grew after the test); the Go test uses the shipped data.
- Env-gated Python tests (`TRIALS_BOT_SRC`, `TRIALS_DBC_DIR`, `TRIALS_RULE_BRIEFS`) keep the same
  gates: they skip when the data is absent and pass when it is supplied.

## Parity

`tools/trialsparity --runs DIR --python-root DIR` copies each finished run folder twice, deletes
every file either scorer generates from BOTH copies (`card.json`, `card.md`, `trial.json`,
`verdicts.json`, `events_summary.json`, and `events.jsonl` where the repeat has a raw trace), then
scores one copy with the Python tool and the other with `internal/trials`. `--verbose` prints the
deleted list. It compares:

- every regular file in the two trees, in both directions: a file only one side wrote differs;
- the result class: scored / FAIL / INVALID / error (the Go error against Python's exit code and
  missing card);
- that every expected output exists after the run (`trial.json`, `verdicts.json`, `card.*`, and in
  raw mode `events.jsonl`, `samples.jsonl`, `events_summary.json`).

Deleting `events.jsonl` when the raw trace exists makes the default proof exercise the
normaliser: both scorers rebuild it, and `samples.jsonl`/`events_summary.json`, from `raw-ta.jsonl`
and the marks. A run with no raw trace falls back to its recorded normalised files and is
counted separately in the summary line. A folder without `RUN.json`, or with a `RUN.json` newer
than 10 minutes, is skipped. Run folders are read only: every score writes into a temporary copy.
A difference exits 1 and prints the first differing run with a short diff.

Result on the recorded nightly runs (paths passed on the command line, never in the repo):

| runs directory | summary line |
|---|---|
| `2026-11-15-t3-1115b-full` | compared 58 runs, equal 58, differ 0, skipped 0, from raw input 58 (pre-normalised 0) |
| `2026-11-17-t3-1115b-test4` | compared 56 runs, equal 56, differ 0, skipped 2, from raw input 56 (pre-normalised 0) |

## Differential table

`internal/trials/tests/differential` runs small hand-made inputs through BOTH scorers: whole run
folders (compared file for file, plus class and trial-level verdict/value/k/n) and single
`rules.evaluate` calls (verdict, value with int/float kept apart, k, n, and raise-vs-not). It
skips when `TRIALS_PYTHON_ROOT` is unset, so CI stays green without Python:

```
TRIALS_PYTHON_ROOT=~/wt-trials-ref go test ./internal/trials/tests/differential
```

Result with the reference: 35 cases (23 whole-run, 12 direct rule evaluations), all equal:
`ok github.com/pjerra/forever-agent/internal/trials/tests/differential 1.8s`. The table covers
B1-B6, plus empty/one-event runs, a latency tie at exactly half, an equal count boundary, negative
times, duplicate raw rows, `seq` int vs string, unicode names, a three-rep majority, a trial with
no rules, `min_n` masking, `min_alive_s` 0 and `"0"`, an integer `first` value, and an excluded
span.

## Fix round 1

The first review's findings and what now pins each one:

| finding | go change | test |
|---|---|---|
| A1 the harness could compare a scorer that wrote nothing | delete every generated file in both copies; delete `events.jsonl` too when a raw trace exists, so both normalise; fallback counted separately | `tools/trialsparity` (`--verbose` shows the list) |
| A2/A4 one-sided or missing outputs | recursive two-way manifest, every file; expected outputs must exist after the run | `tools/trialsparity` |
| A3 ignored error | Go error class compared with Python's exit/card class | `tools/trialsparity` |
| A5 summary | runs/equal/differ/skipped plus raw-input count | summary line below |
| B1 row identity | `sameRow` is the map's header pointer (`cand is trig`) | `parityfix` B1, differential `b1_twin_rows_are_two_rows` |
| B2 float count / null mean | `walk_metric` uses Python `int()`/`float()` and raises where Python raises (`Result.Err`) | `parityfix` B2, differential `b2_walk_*` |
| B3 huge GUIDs | `events.NormGUID` uses `big.Int` and Python's `%016x` sign width | `parityfix` B3, differential `b3_guid_*` |
| B4 pyjson precision/EOF | out-of-int64 integers are `*big.Int`; conversion errors propagate; trailing data is an error; `1e999` is `inf` | `pyjson` unit tests, differential `b4_*` |
| B5 non-object JSONL | a non-object line is an error naming file:line, never dropped | `parityfix` B5, differential `b5_*` |
| B6 list seq | unhashable `seq` is an error naming file:line | `parityfix` B6, differential `b6_seq_list_unhashable` |
| B7 weak tests | interruptible through `rules.Evaluate`; rep order through the real scorer calls; `trials validate` (id/stem, duplicate rule id); kit entries < 20000 | `r3_test.go`, `score/order_test.go`, `trials_cmd_test.go`, `fix_p1_test.go` |
| rule `min_n` 0 masking `check.min_n` | `minNOf` treats 0 as falsy at both levels | `parityfix`, differential `edge_rule_min_n_zero_masks_check` |
| `lowerOf(nil)` | `str(value or "").lower()`: `""`, not `"none"` | `parityfix`, differential `b2b_lower_of_nil_is_empty` |
| `min_alive_s` 0 / `first` int value | 0 means the 10 s floor; `first` keeps the row's int `t` | `parityfix`, differential `edge_rate_min_alive_*`, `edge_first_integer_t` |

## Known differences

- Python's `round()` and 3.12's compensated `sum()` are reproduced exactly; `%g`/`%f` go through
  Go's `strconv`, which rounds the same way C's printf does.
- An integer outside int64 decodes to `*big.Int`, which the scorer keeps where Python keeps it
  (serialisation, `seq`, `spell_id`, GUIDs). A conversion to `float64` beyond its range returns
  "not a number" instead of Python's `OverflowError`; no recorded run reaches it.
- Fixed in this round and no longer a difference: non-object JSONL lines, unhashable `seq`,
  content-vs-identity row comparison, float counts and null means, huge GUIDs, trailing JSON.
- The Python bench's tool-dir globs (`trials/`, `trials/_fixtures/`) are replaced by the embedded
  file sets; a run folder is otherwise read exactly as before.
- Validation messages reproduce Python's `%r` quoting via `scenario`'s `pythonRepr`.
