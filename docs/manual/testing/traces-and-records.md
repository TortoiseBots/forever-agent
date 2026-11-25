---
type: Concept
title: Traces and records
description: What a run writes (trace, summary, combat log, run record, trials and scenario folders) and how trace-score re-reads a trace.
tags: [testing, traces, records, formatting]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T20:35:15Z }
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
  - id: capture-events
    resource: cmd/forever-agent/forever_capture_events.go
  - id: capture-stats
    resource: cmd/forever-agent/forever_capture_stats.go
  - id: decisions-cmd
    resource: cmd/forever-agent/decisions_cmd.go
  - id: decisions-read
    resource: internal/forever/command/tbcmd/decisions.go
  - id: decisions-record
    resource: internal/forever/driver/decisions.go
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
| `frame` | about once a second | the bots (health, power, position, movement, order, casting, the facing `fa` and the orientation `o`, auras) and the mobs (health, position, threat list); a player run's frames also carry its own character in `self` (`x`, `y`, `z`, `map`, `al`), so the distance from the party to its leader is in the record |
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

A `forever dungeon-watch` run writes four more files beside its trace, and the [report card](report-cards.md) can be rebuilt from the last of them:

| File | Contents |
|---|---|
| `<run_id>.combatlog.jsonl` | the session's combat log, one row per line in the trials runner's raw row shape (`seq`, `kind`, `time`, `mono_ms`, `source`, `target`, `spell_id`, `amount`, plus `reason` on a spell fail) |
| `<run_id>.forever-run.json` | the run record: end reason, the module's counters, per-leg and per-step evidence, deaths, pauses, encounter packets and the `watch` block |
| `<run_id>.report-card.txt` | the card the watch prints and writes after every run |
| `<run_id>.decisions.jsonl` | the module's decision rows of the run, with `--decisions-pull`: one JSON object per line (see [the decisions file](#the-decisions-file)) |

The combat log is bounded by `--combat-max-mb` (default 64) and the record says so when it stops early.

# The decisions file

A watch can read the decision rows the bot module records for its run: `forever dungeon-watch` takes
`--decisions-pull` (off by default, needs `--out`). Only the watch pulls; a player-led run has no capture to read.
The module keeps a run's capture only while the run is there and erases it when the run ends, so the rows go beside
the record as `<run_id>.decisions.jsonl`, one JSON object per line and oldest first, the same one-object-per-line form
the combat log uses, written while the run lives: the watch reads the new rows after every status poll, at least 2 s
apart, and once more just before it stops the run. A run that was not watched by this watch cannot be read afterwards;
its rows are in the module's own folder. A run that ends by itself is one poll ahead of the last read, and the module
erases the capture with the end: the rows after the last one read are gone, so at most one poll's rows are missing,
and the summary's `note` says so and names the last `seq` that was read, or says that no row was read. A read that
fails or runs into its 30 s bound widens that window (the rows after the last good read go with the capture too): the
watch spaces its next tries out after failures (2 s, then 4 s, 8 s and so on up to a minute), so a module that hangs
on every read costs one bound now and then, not one on every status poll. The summary keeps the failure in `note`. The summary's
`decisions` block counts them (`rows`, `pages`, `bytes`, `skipped`) or says why none came (`note`), and says
`page_bound` when one read stopped at its own 25-page bound: the next read continues above the cursor then, so nothing
is lost. A gap that the module's own window made (a watch that attaches to a run already past its 512 rows) is not
detected, and `rows` counts only what the file holds.

| Key | Type | Meaning |
|---|---|---|
| `seq` | number | the row's cursor; it rises with every new decision |
| `t` | number | the module's clock at the decision, in unix milliseconds |
| `dt` | number | milliseconds since the previous row |
| `state` | word | the run's state: `running`, `paused` or `done` |
| `phase` | word | the plan phase: `begin`, `gather`, `walk`, `pull`, `task`, `rest`, `wait` or `hold` |
| `step` | number | the plan step |
| `record` | word | what the module did with the record behind the row: `tick` (written), `stub` (cut to fit) or `dropped` (a refusing sink) |
| `order`, `note`, `say` | text | the decision's own texts |
| `trunc` | `true`, when present | the module cut a free text of the row at its stored bound; the row is still one whole line |

A key the module adds later is kept with the text it came as, so a newer module loses nothing in an older agent. A
row that breaks the shape is refused instead of stored: a missing key, a state word outside the list, two rows with
one sequence, or an end line whose count disagrees with the lines. The one row-level loss is a note or say text that
carries a tab or a newline: the shared reply grammar cannot carry the escape, so that one row is left out, `skipped`
counts it, and the rest of the page is kept.

The read is bounded and paged, and every page fits the shared 64 KiB reply bound. The agent asks for the rows after
a sequence 25 at a time by default and follows the module's own next cursor; a page the collector refuses for its
length is asked again with half the rows, down to one, so a run whose notes are unusually long still reads whole. It
stops on the module's "no more" answer, when a page brings no row, or at its own bound of 25 pages (the block says
`page_bound` then, and the next read goes on above the last row). A failed read never fails the run: the block's note
names the reason (`not supported by this module build`, a refusal such as `capture-off`, `no-capture` or `no-run`, the
unconfirmed command permission, or a lost link), and the run's own stop reason and exit code stand; a later read that
answers clears that note, so a run whose reads recovered carries no failure note. A newer run of the same leader
answering in the watched run's place is refused: the watch stops reading rather than write another run's rows under
this run's file.

The read is also a command of its own, for a live run:

```bash
forever-agent decisions pull --leader NAME --out DIR
```

`--run ID` names the run by its id instead of its leader. The command logs in, reads the rows through the command
channel and writes them into `DIR` as `<run id>.decisions.jsonl` (a run with no row writes no file, and the command
says so); `--since-seq N` starts above an earlier cursor and `--limit N` (1-200, 25 by default) sets the page asked
for. The run must be live: the module keeps no capture for a stopped run and answers `no-capture`, as it does for a
player-led one; the file's stem is the module's run id, which is a number, and any other answer is refused. It needs
the account's command permission, like every `.tb` read, and a module build without the command answers `not
supported by this module build`. Without `--leader` or `--run` the command keeps its other mode: the Turtle dashboard
pull of decision captures (`--dash-url`).

# Folder shapes of the other tools

| Tool | Folder | Files |
|---|---|---|
| trials run | one run folder | `RUN.json`, `ledger.jsonl`, one `<trial>/` per trial with `trial.json`, one `repN/` per repeat with `setup.json`, `raw-ta.jsonl`, `samples.jsonl`, `marks.jsonl`, `events.jsonl`, `verdicts.json`; `card.md`, `card.json` |
| scenario run | `scenario-out/<scenario>-<UTC>` by default | `raw.txt`, `norm.txt`, `transcript.jsonl`, `result.json` |
| bug hunter | `--out DIR` | `hits.jsonl`, `counts.json`, `tickets.json`, `tickets.md` |
| forever-data-check | `--out FILE` | the report JSON (`ok`, `unknown`, `differs`, `counts`, `leftover`) and a `.run` folder holding its `ledger.jsonl` |

# Scoring a trace again: trace-score

`forever-agent help` marks `trace-score` `[Turtle only; Forever: none]`. It reads files only, so it refuses no
server; the trace files it reads are described below.

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

# Replaying a capture

A `forever serve --capture-dir DIR` capture holds the run's raw server packets. Two commands read one back offline;
neither opens a socket and neither writes to the capture. Read
[Privacy and captures](../developers/privacy-and-captures.md) before sharing what they print: the payloads are the
server's, so they carry whatever the run carried.

## `forever capture-events`

```text
forever-agent forever capture-events [--kinds a,b] [--out FILE [--force]] <dir>
```

It replays one capture directory (one run, or a parent holding several runs) through the world session and prints one
JSON event per line: the same keys the serve `event_log` method answers (`seq`, `time`, `kind`, ...). The times are
synthetic - the run directory's clock plus 100 ms per record - and the summary on stderr says so.

| Flag | Default | Meaning |
|---|---|---|
| `--kinds a,b` | every kind | keep only the named event kinds |
| `--out FILE` | stdout | write the events to a file (mode `0600`) instead of stdout |
| `--force` | off | let `--out` replace an existing file; never a capture file, and `--out` may not be the capture itself |

Opcodes the writer never captures (time syncs, movement relays, the map change's own traffic) are counted as skipped
rather than dispatched, because a capture holding one was edited. The last lines on stderr count the records handled,
skipped, unparsed and sent, list the unknown opcodes by name, and break the events down per kind. Exit `0` on success,
`2` for a usage error or an unreadable capture.

```sh
forever-agent forever capture-events --kinds chat,party --out ./events.jsonl ./captures
```

## `forever capture-stats`

```text
forever-agent forever capture-stats <dir>
```

It prints one row per opcode - its name, the packet count and the payload bytes - then the totals. It reads the runs'
`manifest.tsv` files only, never a payload, so it still answers after the payloads were deleted and it never prints a
captured byte.

```text
opcode                              packets        bytes
SMSG_UPDATE_OBJECT                      812      1043321
TOTAL                                  2910      2910504
```

Exit `0` on success, `2` for a usage error or an unreadable directory.

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
