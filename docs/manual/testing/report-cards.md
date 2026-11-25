---
type: Reference
title: Report cards
description: "Read the two report cards: the trials card for a behaviour run and the dungeon-run card with its verdict, legs and flags."
tags: [testing, report-cards, trials, dungeon]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T20:35:15Z }
sources:
  - id: trials-report
    resource: internal/trials/report/report.go
  - id: trials-score
    resource: internal/trials/score/score.go
  - id: reportcard-card
    resource: internal/forever/reportcard/card.go
  - id: reportcard-compare
    resource: internal/forever/reportcard/compare.go
  - id: run-report-cmd
    resource: cmd/forever-agent/forever_report.go
  - id: sweep
    resource: internal/trials/live/sweep.go
---

Two different cards are called "report card". The **trials card** summarises a behaviour run: one line per trial rule with its verdict and measured value. The **dungeon-run card** summarises a whole watched dungeon run in one verdict and a list of weak-spot flags. They are written by different tools and answer different questions; both are plain Markdown for a person and JSON for a script.

# The trials card

`trials run` writes `card.md` and `card.json` into the run folder after scoring; `trials score <run dir>` does the same for a finished folder; `trials report <run dir>` re-renders them from the stored `trial.json` files without scoring again.

## Shape

```
# Tortoise Trials report card — <label> — <server> — <tb version>

Run: started <time>, slots <slots>, repeats <n>.

## Data quality
- <trial> <rep>: setup ok; failed=[...]; scored=180 s; excluded=0 s; cut-short: none; events ok; events_dropped=[none]; thresholds=live

| trial | rule | expected | verdict | metric k/n (summed over repeats) | value | pass_if | surprise |
|---|---|---|---|---|---|---|---|
| ... | PAL-HOLY-HEAL-LATENCY | PASS | PASS | 82/96 | 0.854 | >= 0.8 | |
```

The title, label, server and version come from `RUN.json`; the run line names the start time, the slots and the repeat count.

## Data quality

One line per repeat says whether the repeat can be trusted:

| Part | Meaning |
|---|---|
| `setup ok` / `INVALID (<reason>)` | whether provisioning passed every setup check |
| `failed=[...]` | the setup checks that failed |
| `scored=<n> s`, `excluded=<n> s` | scored fight time; time the normaliser excluded |
| `cut-short: ...` | the repeat's `INVALID` reason when it was cut short |
| `events ok` / `events missing` | whether the repeat has its `events.jsonl` |
| `events_dropped=[...]` | event kinds the normaliser dropped, with counts |
| `thresholds=live` | the health thresholds came from the server's config; `assumed 20/50/70` means `config_read` failed and the defaults were used |
| `fight_end missing` | the fight's end was not recorded |
| `; slot N; difficulty step S` | tags of the repeat |

A run with summons the watch could not prove dead or gone carries one more line and a `trials cleanup <run-dir>` reminder.

## The verdict table

| Column | Meaning |
|---|---|
| `trial` | the trial id |
| `rule` | the rule id inside the trial |
| `expected` | the rule's `expect_today` (`PASS`, `FAIL` or `?`) |
| `verdict` | `PASS`, `FAIL`, `INCONCLUSIVE` or `INVALID` |
| `metric k/n (summed over repeats)` | successes over opportunities, summed across valid repeats |
| `value` | the card value: the pooled k/n for ratio/latency/uptime rules, the mean for the others, plus each repeat's own value in parentheses |
| `pass_if` | the rule's comparison |
| `surprise` | `expected FAIL, got PASS` or `expected PASS, got FAIL` when the verdict went against the expectation |

`no match` means the rule's response pattern never matched; `[no match xN]` counts the repeats.

## Evidence

After the table, every `FAIL` or surprise prints its rule id and source, then up to three repeats' evidence lines (up to three each), so the card alone says what broke.

## card.json

```json
{
  "title": "Tortoise Trials report card — ...",
  "run": { "...": "the RUN.json object" },
  "rows": [
    {"trial": "...", "rule": "...", "expected": "PASS", "verdict": "PASS",
     "k": 82, "n": 96, "value": 0.854, "values": [0.85, 0.86],
     "metric": "latency", "pass_if": ">= 0.8", "surprise": "",
     "source": "...", "detail": [], "reason": ""}
  ]
}
```

## Comparing two runs

`trials compare <run dir> --baseline <before dir>` matches rows by (trial id, rule id) and prints the before/after table. Latency, ratio and uptime k/n are summed over valid repeats and compared with Fisher's exact p; the other metrics compare by value.

## The sweep sheet

`trials sweep` writes `sweep.md` over all its trials: the planned order, each trial's verdict and card path, and the trials the class filter skipped with the reason. Exit `0` only when every planned trial ran and PASSed; see [trials](trials.md).

# The dungeon-run card

A `forever dungeon-watch` run writes `<run_id>.report-card.txt` and `.json` and prints the card after every run. The same card can be rebuilt offline from the record, or from a saved card:

```
forever-agent forever run-report RECORD [--json] [--out DIR] [--repulls N] [--member-deaths N] [--slow-factor X]
forever-agent forever run-report --compare A B [...]
```

`RECORD` is a `<run_id>.forever-run.json` run record or a card JSON. `--out DIR` writes `<run_id>.report-card.txt` and `.json` there; `--json` prints the card instead of the text.

## Verdict

| Verdict | Meaning |
|---|---|
| `CLEARED` | the run cleared its plan |
| `PAUSED-STUCK` | a pause the watcher gave up on ended the run: `PAUSED-STUCK AT <leg/step> (<pause reason>)` |
| `STOPPED` | the run stopped at a position: `STOPPED AT <leg/step> (<stop reason>)` |
| `FAILED` | a failure ended the run: `FAILED (<reason>)` |

A record without a watch block still gets a card: the verdict and stop reason come from the record and the per-leg sections stay empty.

## What the text holds

```
report card: run <id> plan <plan> mode <mode>
verdict: CLEARED
thresholds: re-pulls >2 per step; one member >=3 deaths and 2 above the next; a leg over 2x its plan time
run: state done, leg 2, step 41/41, pulls 12/12, deaths 1, wipes 0, pauses 3, rescues 0, 1832s
legs:
  leg 1 "hall": 640s, pulls 5, re-pulls 1, steps 12, deaths 0, wipes 0, pauses 1
deaths:
  leg 2/step 30 t=1240s <member> (deathrecap lines follow)
pauses:
  leg 2/step 31 t=1300s not cleared: "<reason>"
flags:
  [repulls] leg 2/step 28 re-pulled 3 times (limit 2)
    pull seq=...
```

## The flags

| Flag id | Raised when |
|---|---|
| `repulls` | a step was re-pulled more than `--repulls` times (default `2`) |
| `pack-deaths` | the same member died twice at one step |
| `member-deaths` | one member had at least `--member-deaths` deaths (default `3`) and two above the next |
| `unattributed-deaths` | deaths the module's event did not name as a party member, flagged apart and counted for no one |
| `slow-leg` | a leg took more than `--slow-factor` (default `2`) times its plan's recorded time; quiet when the record carries no plan time |
| `watchdog-pause` | the watchdog paused the run |
| `uncleared-pause` | a pause that never cleared |
| `rescue` | a practice rescue |
| `stop-reason` | the run's stop reason, with its evidence |

Every flag prints its evidence lines (pull sequence numbers, times, member names as the run recorded them). The thresholds are recorded in the JSON, so a comparison knows what each side measured. The flags print worst spots first and the stop reason last.

## Comparing two cards

`--compare A B` prints one row per difference: the verdict change first, then per-leg numbers (seconds, pulls, re-pulls, deaths, wipes) and per-flag changes, each marked better or worse where the direction is clear:

```
compare: run <a> -> run <b>
  leg 1 "hall" seconds: 640 -> 588 (-52)
  - repulls leg:2/step:28: leg 2/step 28 re-pulled 3 times (limit 2)
rows: 2 better, 0 worse, 0 changed
```

# Common problems

- **`not a report card or a run record`** — the file's `schema` is neither the card's nor the run record's. A record begins with the run's own schema and version; a card with `forever-report-card` version 1.
- **`card version 3, want 1`** — an older or newer card; rebuild it from the run record.
- **A card with empty legs** — the record had no `watch` block (an aborted run, or a record from a run that never started its watch).
- **`--repulls is 0-100, --member-deaths 1-20 and --slow-factor 1-20`** — the thresholds are range-checked.

# Further reading

- [Trials](trials.md): the bench that writes the trials card.
- [Dungeon watch](../dungeons/dungeon-watch.md): the watcher that writes the run card.
- [Stop reasons](../dungeons/stop-reasons.md): the words a stop reason can take.
- [Overview](overview.md): the vocabulary across all judges.
