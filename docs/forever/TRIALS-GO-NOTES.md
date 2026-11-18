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
`scenario.Validate`: trial id == file stem, and no duplicate rule id across the set. `compare`
prints the same table as the Python `compare`, Fisher p and notes included. Python has no
`report` subcommand (its `score` writes the card): `report` calls the same `write_card` over the
run's stored `trial.json` files, without re-scoring, and prints `card.md`; it is idempotent on a
scored run.
`trials run` and `trials cleanup` are not ported and the help says so.

## Tests

All translated black-box tests live under `internal/trials/tests/<group>/`, one Go file per
Python file, one Go test per Python test method (`Test<Class>_<Method>`), one package per group.
The shared `support_test.go` in each package has the file/JSON helpers; every other helper
carries a per-file prefix so the files of one package share one namespace. The one white-box
test is `internal/trials/score/order_test.go`, which needs the unexported per-repeat scorer
seam to observe the rep order.

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
- that every expected output exists after the run and did not survive the delete step
  (`trial.json`, `verdicts.json`, `card.*`, and in raw mode `events.jsonl`,
  `events_summary.json`; `samples.jsonl` is an input the normaliser rewrites in place, so only its
  content is compared).

Deleting `events.jsonl` when the raw trace exists makes the default proof exercise the
normaliser: both scorers rebuild it, and `samples.jsonl`/`events_summary.json`, from `raw-ta.jsonl`
and the marks. A run with no raw trace falls back to its recorded normalised files and is
counted separately in the summary line. A folder without `RUN.json`, or with a `RUN.json` newer
than 10 minutes, is skipped. Run folders are read only: every score writes into a temporary copy.
A difference exits 1 and prints the first differing run with a short diff.

A run where either scorer errors is never equal: it is counted as `errored`, both messages are
printed, and the tool exits 1. `--expect-error RUN` names a run whose error is the expected
result (repeatable, comma-separated); no recorded run needs it. The summary prints total,
compared, equal, differ, errored and skipped, and the tool fails if
compared + skipped + not run != total and when compared == 0: "compared 0 runs" proves nothing.

What the harness cannot prove, stated in its help as well:

- `samples.jsonl` is both an input and an output: the normaliser rewrites it in place, so a
  difference in it can come from either side's read as well as its write.
- a repeat without a raw agent trace keeps its recorded `events.jsonl`, so those repeats do not
  exercise the normaliser; they are counted as pre-normalised.
- symlinks are not copied: only regular files and directories reach the temporary copies.

Result on the recorded nightly runs (paths passed on the command line, never in the repo):

| runs directory | summary line |
|---|---|
| `2026-11-15-t3-1115b-full` | total 58 runs, compared 58, equal 58, differ 0, errored 0, skipped 0, not run 0 |
| `2026-11-17-t3-1115b-test4` | total 58 runs, compared 58, equal 58, differ 0, errored 0, skipped 0, not run 0 |

## Differential table

`internal/trials/tests/differential` runs small hand-made inputs through BOTH scorers: whole run
folders (compared file for file, plus class and trial-level verdict/k/n and the per-repeat
`detail[*].value`), single `rules.evaluate` calls (verdict, value with int/float kept apart, k, n,
and raise-vs-not), and `trials validate` invocations (stdout byte for byte and the exit code). It
skips only when no reference is given at all; a reference path that is set but has no `trials.py`
FAILS the test, so a typo cannot turn the table green:

```
TRIALS_PYTHON_ROOT=~/wt-trials-ref go test ./internal/trials/tests/differential
go test ./internal/trials/tests/differential -args -python-root ~/wt-trials-ref
```

Result with the reference: 55 cases (39 whole-run, 14 direct rule evaluations, 2 validate
runs), all pass with no skips (fix round 2). Besides B1-B6 it covers the round-2 items: the NaN
token in a snapshot and a NaN/+Inf value reaching a writer, `min_n` `"abc"` on the rep path
(error) and the pooled path (guarded), `within_s` `"abc"`, `min_valid_s` `"abc"`, `int(inf)` in a
pre-scored n and in `min_reps`, `min_reps` beyond int64, the four odd GUID strings
(`0x-5`, `1_0`, `0x1_f`, `0x+5`), a spell id beyond int64 in a where filter, and validate's
trial-id prefix, `%r` quoting, int 1 vs `"1"` rule ids and non-object rule (exit 1 after the
earlier files printed).

An error case also names WHICH error: `errorPython` must appear in Python's stderr (or the rule
script's exception message) and `errorGo` in the Go error, plus the expected stage, so "both
sides raised something" is not enough to pass.

## Fix round 1

The first review's findings and what now pins each one:

| finding | go change | test |
|---|---|---|
| A1 the harness could compare a scorer that wrote nothing | delete every generated file in both copies; delete `events.jsonl` too when a raw trace exists, so both normalise; fallback counted separately | `tools/trialsparity` (`--verbose` shows the list) |
| A2/A4 one-sided or missing outputs | recursive two-way manifest, every file; expected outputs must exist after the run and not survive the delete step | `tools/trialsparity` |
| A3 ignored error | Go error class compared with Python's exit/card class | `tools/trialsparity` |
| A5 summary | runs/equal/differ/skipped plus raw-input count | summary line below |
| B1 row identity | `sameRow` is the map's header pointer (`cand is trig`) | `parityfix` B1, differential `b1_twin_rows_are_two_rows` |
| B2 float count / null mean | `walk_metric` uses Python `int()`/`float()` and raises where Python raises (`Result.Err`) | `parityfix` B2, differential `b2_walk_*` |
| B3 huge GUIDs | `events.NormGUID` uses `big.Int` and Python's `%016x` sign width | `parityfix` B3, differential `b3_guid_*` |
| B4 pyjson precision/EOF | out-of-int64 integers are `*big.Int`; conversion errors propagate; trailing data is an error; `1e999` is `inf` | `pyjson` unit tests, differential `b4_*` |
| B5 non-object JSONL | a non-object line is an error naming file:line, never dropped; a raw trace overrides a bad recorded events line exactly where Python's normalise condition ignores non-dict rows | `parityfix` B5, differential `b5_*` |
| B6 list seq | unhashable `seq` is an error naming file:line | `parityfix` B6, differential `b6_seq_list_unhashable` |
| B7 weak tests | interruptible through `rules.Evaluate`; rep order through the real scorer calls; `trials validate` (id/stem, duplicate rule id); kit entries < 20000 | `r3_test.go`, `score/order_test.go`, `trials_cmd_test.go`, `fix_p1_test.go` |
| rule `min_n` 0 masking `check.min_n` | `minNOf` treats 0 as falsy at both levels | `parityfix`, differential `edge_rule_min_n_zero_masks_check` |
| `lowerOf(nil)` | `str(value or "").lower()`: `""`, not `"none"` | `parityfix`, differential `b2b_lower_of_nil_is_empty` |
| `min_alive_s` 0 / `first` int value | 0 means the 10 s floor; `first` keeps the row's int `t` | `parityfix`, differential `edge_rate_min_alive_*`, `edge_first_integer_t` |

## Known differences

- Python's `round()` and 3.12's compensated `sum()` are reproduced exactly; `%g`/`%f` go through
  Go's `strconv`, which rounds the same way C's printf does.
- `pyjson` now accepts the non-standard `NaN`, `Infinity` and `-Infinity` value tokens, as
  `json.loads` does; the writers still refuse them (`allow_nan=False`), which the scorer turns
  into an error, never a verdict. Verified by differential `b1_*`, `c2_*`, `b4_raw_seq_inf_*`.
- An integer outside int64 decodes to `*big.Int`, which the scorer keeps where Python keeps it
  (serialisation, `seq`, `spell_id`, GUIDs, a where filter). Two places still hold an int64 and
  are pinned by `parityfix` instead of fixed: a `walk_metric` `n` beyond int64 (Go INCONCLUSIVE
  "walk n == 0"; Python PASS with the exact big n) and the pooled k/n sums beyond int64 (Go
  "does not fit int64" error; Python sums big integers and scores). Fixing either needs big
  integers through `rules.Result`/the pooled sums and the report.
- `int()` beyond float64's range (`int(inf)`) is an `OverflowError` in Python and is not caught
  by the bench's `except (TypeError, ValueError)` guards. `pyjson.PyInt` keeps the exception class
  and every guarded call site propagates only that class, so Go errors where Python errors.
- The Python bench's tool-dir globs (`trials/`, `trials/_fixtures/`) are replaced by the embedded
  file sets; a run folder is otherwise read exactly as before. `validate` falls back to the
  embedded `_fixtures` set when no file is found, as `cmd_validate` does.
- Validation messages reproduce Python's `%r` quoting via `scenario`'s `pythonRepr` (single
  quotes, or double when the text holds a single quote but no double quote, with `\n`/`\xNN`
  escapes). A `validate` load failure prints an OS error as Python's `str(exc)`; a malformed-JSON
  load message still uses the Go decoder's wording, not Python's `JSONDecodeError` text (the exit
  code and `path: cannot load: ` prefix match).

## Conversion errors behind ignored results

Fix round 2 replaced every ignored conversion error in `rules.go`, `score.go` and `events.go`
with the Python behaviour at the same line. The `, _ :=` / `ok`-swallowed sites and what Python
does there:

| go site | Python line | Python | Go now |
|---|---|---|---|
| `rules.go:112` `Num` (`pyjson.Num`) | `rules.py:54-56` `_num` (isinstance guard) | guarded | guarded by `IsNum` |
| `rules.go:1754` latency `within_s` | `rules.py:1021` `float(check.get("within_s") or 0.0)` | unguarded: raises | `floatOrZero`, error |
| `rules.go:1758` latency `debounce_responder_s` | `rules.py:1026-1030` try/except `(TypeError, ValueError)` | guarded | `guardedFloatOrZero`, only `OverflowError` escapes |
| `rules.go:2033-2034` `first` excluded span `from`/`to` | `rules.py:1173-1178` try/except, `continue` | guarded | `PyFloat`; `OverflowError` escapes, others skip the span |
| `rules.go:2050` `first` across-reps `debounce_responder_s` | `rules.py:1183-1190` try/except | guarded | `guardedFloatOrZero` |
| `rules.go:1555` walk `n` | `rules.py:926-929` try/except `(TypeError, ValueError)` | guarded | `PyInt`; `OverflowError` escapes, others are 0 |
| `score.go:549` `min_valid_s` | `score.py:304` `float(trial.get("min_valid_s") or 0)` | unguarded | `floatOrZero`, error |
| `score.go:595` `min_opportunity_s` | `score.py:324` and `333` `float(min_opp)` | unguarded where truthy and PASS/FAIL | `floatOrZero`, error |
| `score.go:612` rep `min_n` | `score.py:343-345` `int(min_n)` | unguarded | `PyInt`, error (differential `b2_rule_min_n_string_raises`) |
| `score.go:901` pooled `min_n` | `score.py:459-463` try/except | guarded | error only for `OverflowError` |
| `score.go:931` `min_reps` | `score.py:478-482` try/except | guarded; big ints compare | error only for `OverflowError`; big ints compared exactly |
| `score.go:879-884` pooled `k`/`n` sums | `score.py:450-451` `sum(int(...))` | unguarded | `intOrZero` error (big values pinned, see above) |
| `events.go:432` attack `seq` | `events.py:206-209` try/except | guarded | `PyInt`; `OverflowError` escapes, others mean "no seq" |
| `events.go:670`, `events.go:681` normalise `seq`, `spell_id` | `events.py:385-393` try/except | guarded | `PyInt`; `OverflowError` escapes, others mean null |

The remaining `, _ :=` conversions after the sweep are `pyjson.List`/`pyjson.Object` shape
checks (not numeric conversions) and `Num`'s `isinstance`-guarded case above.

## Fix round 2

| item | change | test |
|---|---|---|
| A1 either-side error | an errored run is counted `errored`, both messages printed, exit 1; `--expect-error RUN` names an expected one | `tools/trialsparity` run on the fabricated error run (summary `errored 1`) |
| A2 compared 0 | summary is total/compared/equal/differ/errored/skipped/not run; exit 1 when compared == 0 and when compared+skipped+not run != total | empty `--runs` dir exits 1 for both reasons |
| A3 harness limits | help and these notes name samples.jsonl as input+output, repeats without a raw trace, symlinks | tool help text |
| B1 NaN/Infinity tokens | `pyjson.Unmarshal` accepts the three tokens; writers still refuse them | `pyjson` `TestUnmarshalNonFiniteTokens`, differential `b1_nan_token_in_snapshot`, `b1_nan_value_in_prescore_errors`, `c2_inf_value_in_prescore_errors` |
| B2 `int(min_n)` | rep path errors on a value Python cannot convert; pooled path keeps the guard | differential `b2_rule_min_n_string_raises`, `b2_pooled_min_n_string_guarded` |
| B3 ignored conversions | every site above matches Python | differential `b3_latency_within_s_string_raises`, `b3_min_valid_string_raises`; table above |
| B4 int(inf), big ints, GUIDs | class-aware `PyInt`/`PyFloat`; `min_reps` big ints; Python `int(text, 16)` rules | differential `b4_prescore_n_inf_raises`, `b4_min_reps_inf_raises`, `b4_min_reps_beyond_int64`, `b4_pooled_min_n_beyond_int64`, `b4_raw_seq_inf_write_raises`, `b4_norm_guid_edges`; `events` `TestNormGUIDMatchesPython`; pinned `parityfix` big walk n / pooled k/n |
| B5 validate | trial-id prefix, `%r` quoting, fixtures fallback, int 1 vs `"1"` keys, non-object rule exit 1, OS error text | differential `b5_messages_rule_id_keys`, `b5_non_object_rule_raises` |
| B6 error identity | every error case names `errorPython`/`errorGo` (and the stage) | the differential table's error cases |
| C1 float int canonicalisation | `PyInt(5e18)` is the int64 5000000000000000000 | `pyjson` `TestPyIntMatchesPython`, differential `c1_walk_n_5e18` |
| C2 writers never panic | `verdicts.json`, `trial.json`, `card.md`, `card.json` return errors; `ScoreRepE`/`ScoreTrialE`/`ScoreRunE` propagate | differential `c2_*`, `b1_nan_value_in_prescore_errors` |
| C3 big spell id filter | `SpellIDs` keys by Python `int()` digits; `IntKey` compares | differential `c3_spell_id_beyond_int64` |
| C4 harness stale snapshot | stamps are taken before the delete and compare size+mtime; an unparseable RUN.json is an errored run | `tools/trialsparity` `TestExpectedOutputsUsesPreDeleteStamp`; a fabricated corrupt-RUN.json run reports `errored 1` and exits 1 |
| C5 test reference/values | a set-but-wrong reference fails; `verdictFields` reads `detail[*].value` | `go test ... -args -python-root /bad` fails; differential run cases compare values |
| C harness gate in CI | `.github/workflows/test.yml` job `differential` checks out the pinned reference and fails on `--- SKIP` | workflow YAML |
