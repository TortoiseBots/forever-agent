---
type: Concept
title: Traces and records
description: What a run writes (trace, summary, combat log, run record, trials and scenario folders) and how trace-score re-reads a trace.
tags: [testing, traces, records, formatting]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:15Z }
sources:
  - id: trace-pkg
    resource: internal/trace/trace.go
  - id: trace-writer
    resource: internal/trace/writer.go
  - id: trace-prune
    resource: internal/trace/prune.go
  - id: tracerun
    resource: internal/tracerun/tracerun.go
  - id: trace-score
    resource: cmd/forever-agent/trace_cmd.go
  - id: tracecheck
    resource: internal/tracecheck/tracecheck.go
  - id: bughunt-tickets
    resource: internal/bughunt/tickets.go
  - id: live-run
    resource: internal/trials/live/run.go
  - id: scenario-cmd
    resource: cmd/forever-agent/scenario_cmd.go
---

Every run leaves files behind, and those files are the whole basis of the testing tools: nothing is judged from a live socket when a recording will do. A dungeon run or brain session writes a run trace and its summary; a watch adds its combat log and run record; the trials bench writes a run folder; a scenario writes its transcripts. This page says what each file is, what it holds, and how `trace-score` reads a trace again after the fact.

# The run trace

One JSONL file per run, in the trace folder (default `traces/`), named `<run_id>.jsonl`. The run id is letters, digits, dot, underscore and dash only (at most 64 characters); a generated one looks like `r-20261014T093012-a1b2`. The format is `bt-trace` version 1 and the producer's records carry `src: "agent"`.

Every line is a flat JSON object that starts with the same five fields:

| Field | Meaning |
|---|---|
| `v` | schema version (`1`) |
| `k` | record kind |
| `run` | the run id |
| `src` | the producer (`agent` here, `server` for the server recorder) |
| `t` | milliseconds since the run started |

The kinds, in the order a run writes them:

| `k` | When | Contents |
|---|---|---|
| `header` | once, first | scope, agent version and build, start time, sample interval, size caps, and the agent's own `mode`, `plan`, `options`, `meta`; also the capabilities (`caps`) the producer records, such as `facing`, `moving`, `threat`, `orders`, `walks`, `partylist` |
| `party` | once per member, when first seen | guid, name, class, role, level, leader flag |
| `mobs` | once per hostile, when first seen | guid, entry, name |
| `frame` | about once a second | the bots (health, power, position, movement, order, casting, facing, auras) and the mobs (health, position, threat list) |
| `event` | when it happens | `ev` plus the event's own fields: orders, rest gates, walks, pulls, deaths, boss events |
| `end` | once, last | `reason` and the counters |
| `oracle` | after the end | one record per check (the trace scorer's verdicts) |

A reader ignores record kinds, fields and events it does not know, and refuses a file whose `v` is higher than it reads. A check whose capability the header does not list answers `na`, not a guess.

## Size and retention

| Limit | Value |
|---|---|
| trace folder | `traces/` (configurable by the run's trace options) |
| frames stop at | 32 MiB; events may go on to a quarter more; the `end` record and the oracles are always written |
| frame interval | at least 1 s, tripled once half of the size cap is used |
| files kept | 200 generated-id traces per folder; a caller-supplied run id is never pruned |

The writer appends each record as it comes (no buffer), so a crashed run still leaves a readable file.

# The summary

Beside each trace is `<run_id>.summary.json`, a small file a dashboard can list without parsing the trace. Its fields:

| Field | Meaning |
|---|---|
| `v`, `schema`, `run`, `src` | format identity |
| `status` | `running`, `complete` or `aborted` (a write error) |
| `end_reason` | why the run ended |
| `scope`, `scope_name`, `requester` | who asked for the run and what it was |
| `agent_version`, `agent_sha` | the build that wrote it |
| `trace_file` | the trace's file name |
| `start_unix`, `end_unix`, `duration_ms`, `interval_ms`, `map`, `instance` | times, sample interval and place |
| `counters` | the run's own counters |
| `oracles` | one entry per check, with its verdict |
| `verdict`, `scored_unix`, `scorer` | the check verdicts, when the trace was scored |
| `trace_complete`, `bad_lines`, `bytes`, `truncated` | recording health |
| `source`, `mode`, `plan`, `result`, `error` | `dungeon_run` or `brain`; `group` or `remote`; the plan; `done`, `failed` or `stopped`; the error text |
| `party`, `meta`, `failed` | the party, the producer's metadata, the ids of failed checks |

`trace-score --summary` re-writes this file from the trace, which is how an old recording gets a summary after a threshold change.

# What a watch adds

A `forever dungeon-watch` run writes three more files beside its trace, and the [report card](report-cards.md) can be rebuilt from the last of them:

| File | Contents |
|---|---|
| `<run_id>.combatlog.jsonl` | the session's combat log, one row per line in the trials runner's raw row shape (`seq`, `kind`, `time`, `mono_ms`, `source`, `target`, `spell_id`, `amount`, plus `reason` on a spell fail) |
| `<run_id>.forever-run.json` | the run record: end reason, the module's counters, per-leg and per-step evidence, deaths, pauses, encounter packets and the `watch` block |
| `<run_id>.report-card.txt` | the card the watch prints and writes after every run |

The combat log is bounded by `--combat-max-mb` (default 64) and the record says so when it stops early.

# Folder shapes of the other tools

| Tool | Folder | Files |
|---|---|---|
| trials run | one run folder | `RUN.json`, `ledger.jsonl`, one `<trial>/` per trial with `trial.json`, one `repN/` per repeat with `setup.json`, `raw-ta.jsonl`, `samples.jsonl`, `marks.jsonl`, `events.jsonl`, `verdicts.json`; `card.md`, `card.json` |
| scenario run | `scenario-out/<scenario>-<UTC>` by default | `raw.txt`, `norm.txt`, `transcript.jsonl`, `result.json` |
| bug hunter | `--out DIR` | `hits.jsonl`, `counts.json`, `tickets.json`, `tickets.md` |
| forever-data-check | `--out FILE` | the report JSON (`ok`, `unknown`, `differs`, `counts`, `leftover`) and a `.run` folder holding its `ledger.jsonl` |

# Scoring a trace again: trace-score

```
forever-agent trace-score [--json] [--summary] [--set check=value]... <trace.jsonl>...
```

The command re-reads saved traces with the current checks, so a trace recorded before a threshold change can be scored under the new rule. It reads nothing but the files and never logs in.

| Flag | Default | Meaning |
|---|---|---|
| `--json` | false | print one JSON object per trace (`run`, `file`, `verdict`, `failed`, `checks`, `spells`) |
| `--summary` | false | write `<run_id>.summary.json` beside each trace with the fresh verdicts |
| `--set check=value` | none | change one threshold; repeatable |

The `--set` ids are `rest_share`, `stale_party`, `walk_stops`, `threat_off_tank`, `order_refusals`, `feed_stalls`, `stuck`, `facing`, and the warning thresholds `idle_share`, `interrupt_cover`, `taunt_latency`, `cc_hold`, `pet_threat`, `spell_oracle`, `oom_in_fight`. `deaths` and `gm_shortcuts` have no threshold.

## The checks

| Check id | Name | Default threshold | Fails when |
|---|---|---|---|
| `rest_share` | Time in rest gates | 40% | the party spent more than 40% of the run in rest gates |
| `stale_party` | Stale party data | 60 s | a member's mana did not change while others changed, for 60 s |
| `walk_stops` | Walk stop-and-go | 4 per 100 yd | more stops per 100 yd than the limit |
| `threat_off_tank` | Threat held by a non-tank | 4 s | a non-tank held a mob's top threat longer than 4 s inside a pull |
| `deaths` | Deaths and wipes | 0 | any death or wipe |
| `order_refusals` | Order refusals | 20% | more than 20% of orders were refused |
| `feed_stalls` | Feed stalls | 10 s | more than 10 s without a feed frame (group runs have no feed) |
| `gm_shortcuts` | GM shortcuts (honest run) | 0 | any GM command in the run |
| `stuck` | Stuck members | 5 s | a member that should move stood still longer than 5 s |
| `facing` | Facing the target | 2 s | a member in melee range stood turned away for more than 2 s |
| `spells` | Spell usage (report only) | warnings | never fails; reports each bot's casts and actions |
| `spell_oracle` | Spell oracle (report only) | 60 s active | never fails; warns about missing core spells after 60 s of active time |
| `oom_in_fight` | Mana dry in a fight (report only) | 10 s | never fails; warns about a mana user under 5% mana in combat |

A check answers `na` when the trace lacks what it needs: a run shorter than 60 s has no rest share, a producer that does not record facing has no facing check, a group run has no feed frames. The per-check warning thresholds (`idle_share` 15%, `interrupt_cover` 30%, `taunt_latency` 3 s, `cc_hold` 50%, `pet_threat` 5%) belong to the report-only spell usage check and produce warnings, never a failure.

## What you see

```
r-20261124T190001-a1b2  dungeon_run group zul_gurub  1832 s  PASS
  PASS rest_share       12.4 % (limit 40): ...
  FAIL gm_shortcuts     2 commands (limit 0): ...
```

Exit `11` when any check failed, `0` otherwise, `2` for a usage error or an unreadable trace. The `--json` form gives a script the same verdicts and the spell report in one object.

# Examples

Score a night's traces and refresh their summaries:

```sh
forever-agent trace-score --summary --set threat_off_tank=6 ./traces/*.jsonl
```

Read one old trace as JSON to see which check broke:

```sh
forever-agent trace-score --json ./traces/r-20261123T210000-ab12.jsonl
```

# Further reading

- [Overview](overview.md): all the judges and their vocabulary.
- [Bug hunter](bug-hunter.md): the other offline reader of the same trace and combat log.
- [Trials](trials.md): the trials run folder in full.
- [Scenarios](scenarios.md): the scenario transcripts.
- [Privacy and captures](../developers/privacy-and-captures.md): what must never be shared from a recording.
