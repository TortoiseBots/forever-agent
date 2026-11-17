# Trials scoring half in Go

`internal/trials` is a faithful translation of the scoring half of the Python trials bench
(tortoise-trials), and `forever-agent trials` exposes it. Nothing here runs a trial: the live
runner, leases, slots, setup/teardown and `build_data.py` are **not ported**.

The reference is tortoise-trials `main` at `645642c`; the translation keeps function and rule
names recognisable (Go naming style), every rule citation string and "expects" text byte for
byte, and the same JSON and text output.

## Modules

| python (lines) | go package | go lines | state |
|---|---|---|---|
| `trl/events.py` (453) | `internal/trials/events` | 520 | translated |
| `trl/scenario.py` (1025) | `internal/trials/scenario` | 1310 | translated (validate, variants, selectors) |
| `trl/rules.py` (1467) | `internal/trials/rules` | 1560 | translated |
| `trl/score.py` (548) | `internal/trials/score` | 900 | translated |
| `trl/report.py` (446) | `internal/trials/report` | 800 | translated (card, compare, pr-block) |
| `trl/_wr/stats.py` (71) | `internal/trials/wr` | 130 | translated |
| `trl/_wr/compare.py` (62) | `internal/trials/wr`, `internal/trials/report` | — | `note_for` in `wr`, `compare` in `report` |
| `trl/naming.py` (6) | `internal/trials/naming` | 12 | translated |
| `trl/spells.py` (99) | `internal/trials/spells` | 150 | translated, reads the embedded `data/spells.json` |
| `trl/gear.py` (117) | `internal/trials/gear` | 240 | translated, reads the embedded `data/gear_kits.json` |
| `trl/talents.py` (111) | `internal/trials/talents` | 190 | translated |
| `trials.py` score/report/compare | `cmd/forever-agent/trials_cmd.go` | 170 | translated |

Supporting pieces with no Python counterpart:

| go package | why |
|---|---|
| `internal/trials/pyjson` | Python JSON semantics: sorted keys, `indent=2`, `sort_keys` lines, int/float kept apart, Python float repr and banker's rounding, Python 3.12 compensated `sum()`. The card is compared byte for byte with the Python tool, so Go's `encoding/json` float spelling (`1` for `1.0`) is not enough. |
| `internal/trials/data`, `internal/trials/trialdefs` | `go:embed` of the reference `data/*.json` and `trials/<group>/*.json` (plus `trials/_fixtures`), copied unchanged. |
| `internal/trials/testdata/live1..3` | the reference `tests/fixtures-live1..3`, copied unchanged. |
| `tools/trialsparity` | the equality harness (see below). |

Python lines are `wc -l` of the pinned reference; Go lines are `wc -l` including tests-free
production files.

## Subcommands

```
forever-agent trials score <run dir>                    # like `trials.py score`
forever-agent trials report <run dir>                   # re-render card.md/card.json, print the card
forever-agent trials compare <run dir> --baseline <dir>  # like `trials.py compare`
```

`score` prints `scored <dir>: N FAIL, M INVALID` and exits 1 on any FAIL, 3 on any INVALID.
`compare` prints the same table as the Python `compare`. Python has no `report` subcommand (its
`score` writes the card): `report` calls the same `write_card` over the run's stored
`trial.json` files, without re-scoring, and prints `card.md`, so the parity harness can diff the
text report on its own. `trials run` and `trials cleanup` are not ported (the help says so).

## Python test -> Go test

Every Python test of the scoring modules is translated; the live-runner tests are listed as not
translated (they exercise `trl/live/*`, out of scope for this round).

| python test file | go test | state |
|---|---|---|
| see the table at the end of this file | | |

## Parity

`tools/trialsparity --runs DIR --python-root DIR` copies each finished run folder twice, scores
one copy with the Python tool and the other with `internal/trials`, and diffs `card.json` and
`card.md` (and, when those match, every `rep*/verdicts.json` and `trial.json`). A folder without
`RUN.json`, or with a `RUN.json` newer than 10 minutes, is skipped. Run folders are read only.

Result on the recorded nightly runs (paths passed on the command line, never in the repo):

| runs directory | compared | equal |
|---|---|---|
| full nightly set, one night | 58 | 58 |
| a second nightly set | 50 | 50 |
| the third (short) set | 6 | 6 |

Also compared with `events.jsonl` deleted from the copies (so the scorer must normalise
`raw-ta.jsonl` itself): the run stays equal, and `events.jsonl`, `samples.jsonl` and
`events_summary.json` are byte-identical to the Python ones.

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
- The Python bench's `_tool_dir` glob (`trials/`, `trials/_fixtures/`) is replaced by the embedded
  file sets; a run folder is otherwise read exactly as before.
- `validate` prints values with `%r`; the Go port reproduces Python's repr for the scalars and
  containers the validator prints.
