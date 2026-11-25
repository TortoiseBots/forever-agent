---
type: Concept
title: How a run is judged
description: The four ways the agent turns a recording or a test run into a verdict, and which one answers which question.
tags: [testing, overview, verdicts, traces, trials]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T20:35:15Z }
sources:
  - id: tracecheck
    resource: internal/tracecheck/tracecheck.go
  - id: bughunt
    resource: internal/bughunt/model.go
  - id: trials-score
    resource: internal/trials/score/score.go
  - id: spelljudge
    resource: internal/forever/spelljudge/spelljudge.go
  - id: reportcard
    resource: internal/forever/reportcard/card.go
  - id: trace-cmd
    resource: cmd/forever-agent/trace_cmd.go
---

The agent never asks you to read a log to find out whether a run went well. Every run it records can be re-read offline by one of four judges, each with its own input, its own vocabulary and its own exit code. They complement each other: a trace says how the party moved and whether it cheated, the bug hunter says which behaviours wasted time, the trials bench says whether a bot did a specified job, and the report card summarises a whole dungeon run in one line.

# The four judges

| Judge | Command | Input | Answers |
|---|---|---|---|
| Trace scorer | `forever-agent trace-score` | one or more run traces (`<run_id>.jsonl`) | did the run keep its party fed, moving and out of GM shortcuts, and what did each bot cast |
| Bug hunter | `forever-agent bughunt` | a watch folder, a watch trace, or a trials repeat | which bots misbehaved, for how long, and which findings are new against a baseline |
| Trials bench | `forever-agent trials score` (or `trials run`) | a trials run folder | did each spec'd behaviour of a hired bot pass its rule |
| Report card | `forever-agent forever run-report` | a saved `<run_id>.forever-run.json` | did the dungeon run clear, stop or fail, and where was it weak |

Two more readers score a fight instead of a run: [`forever spell-score`](spell-score.md) judges a Forever class fight from a watch's combat log, and [`fight-score`](fight-score.md) judges a finished fight JSON from the dashboard.

# The vocabulary

Each judge has its own words, and mixing them up is the usual source of confusion.

| Word | Where it appears | Meaning |
|---|---|---|
| `PASS` / `FAIL` | trials rules, scenario checks | the measured value met / missed the rule's `pass_if` |
| `INVALID` | trials repeats and trials | the recording cannot be judged (setup failed, the repeat was cut short, no scored time) |
| `INCONCLUSIVE` | trials | the rule had too little evidence (no opportunity, `min_n` not reached, unknown attack state); not a pass and not a failure |
| `pass` / `fail` / `na` | trace checks, fight oracle lines | the check held, broke, or had nothing to measure |
| `ok` / `warn` / `fail` | fight-score rows | the tank check or oracle reading met its threshold, missed a warning threshold, or failed |
| `new` / `known` / `reopened` / `gone` | bug-hunt tickets | the ticket's life against a previous night's `tickets.json` |
| `CLEARED` / `STOPPED` / `FAILED` | report cards | the run cleared its plan, stopped early, or failed |

The rule behind every judge is the same: **unknown stays unknown**. A check whose input a recording does not carry is reported as skipped or `na` with its reason, never as a pass and never as a hit from a guess.

# Which judge to reach for

- "Did this dungeon run end well, and why?" — the report card (`forever run-report`) of the watch record, or the card the watch wrote itself. See [report cards](report-cards.md).
- "What did the bots do wrong during that night's runs?" — the bug hunter, against yesterday's tickets as a baseline. See [bug hunter](bug-hunter.md).
- "Does the bot perform the behaviour we specified?" — the trials bench. See [trials](trials.md).
- "Was this run honest and healthy: rested, fed, not GM-teleported, no tank threat escapes?" — `trace-score`. See [traces and records](traces-and-records.md).
- "Did the warrior tank press Sunder Armor, and did the paladin cast a warrior ability?" — [`spell-score`](spell-score.md) or [`fight-score`](fight-score.md).

# Where verdicts come back

| Command | Exit 0 | Non-zero |
|---|---|---|
| `trace-score` | no check failed | `11` a check failed, `2` usage |
| `bughunt` | the hunt ran (hits are not an error) | `2` usage, bad input or an unreadable rule input |
| `trials score` | no FAIL and no INVALID | `1` any FAIL, `3` any INVALID (no FAIL), `1` a scoring error |
| `trials run` | every trial ran and the run scored | `1` run or score error, `5` disconnected, `15` slot refused |
| `trials sweep` | every planned trial ran and PASSed | `1` otherwise |
| `forever spell-score` | no member failed | `1` a member failed, `2` usage or load error |
| `fight-score` | always, warnings never fail | `2` usage |
| `scenario run` | every check passed | `14` a check failed, `15` the run errored |

# Further reading

- [Trials](trials.md): the behaviour bench, its trial files and its run folder.
- [Bug hunter](bug-hunter.md): the episode rules and the ticket board.
- [Traces and records](traces-and-records.md): what a run writes and how to re-score it.
- [Scenarios](scenarios.md): scripted live tests with built-in checks.
- [Report cards](report-cards.md): the trials card and the dungeon-run card.
- [CI and local checks](ci-and-local-checks.md): what runs on every push and how to run it locally.
