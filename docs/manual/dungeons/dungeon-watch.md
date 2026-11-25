---
type: Command
title: forever dungeon-watch
description: Watch a dungeon run that a server module leads by itself, keep its safety budgets, rescue a wipe and record the run.
tags: [dungeons, watch, module, command]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T21:10:00Z }
sources:
  - id: watch-cmd
    resource: cmd/forever-agent/forever_watch.go
  - id: driver-watch
    resource: internal/forever/driver/watch.go
  - id: driver-actions
    resource: internal/forever/driver/actions.go
  - id: driver-record
    resource: internal/forever/driver/record.go
  - id: tactics-check
    resource: cmd/forever-agent/forever_watch_tactics.go
  - id: tactics-module
    resource: internal/tactics/module.go
  - id: tactics-compare
    resource: internal/tactics/compare.go
---

> **Not yet proven on a live server.** The watch machine is exercised against the scripted module; its live mode is wired but has not been run against a real server. See [what has been proven](../getting-started/what-is-forever-agent.md#what-has-been-proven-and-what-has-not).

# Synopsis

```bash
forever-agent forever dungeon-watch --leader NAME --plan FILE \
    [--rest host:port --rpc host:port --ca FILE --account EMAIL] --character NAME \
    [--enter tele|portal|none] [--max-minutes N] [--max-wipes N] [--practice=true] \
    [--watchdog-minutes N] [--pause-minutes N] [--poll SEC] [--attach [--attach-wait SEC]] \
    [--frame-every SEC] [--combat-max-mb N] [--combatlog-watch group|unit|off] \
    [--repulls N] [--member-deaths N] [--slow-factor X] [--out DIR] [--map] [--navmesh DIR] \
    [--decisions-pull] [--dry] [--no-preflight | --preflight-only] [--serve-dir DIR] [--export FILE] \
    [--soap-user NAME --soap-password-file FILE] [--module-tactics FILE]
```

`dungeon-watch` is for a run the bot module leads itself: its tank bot is the run leader, its party is the module's
own, and this character only observes. The watcher brings the party into the instance the same way a run does,
starts the module's run with one line, and from then on sends **no order** — an order would pause the module's run.
Its job is to poll, print, keep the safety budgets, rescue a wipe when the practice switch allows it, and record
everything.

For the two other modes see [dungeon-run](dungeon-run.md) (the agent commands five bots) and
[dungeon-play](dungeon-play.md) (the agent plays one character).

# What it does

1. **Placement.** The party's roster is read from the module and must be the plan's size with the roles the plan
   wants, every member online, at least level 10, already commanded and a host bot; the module's run leader must be
   the party's tank or its flagged leader. The commander the module names (one of its own bots, never this
   character) is brought to the tank's side before `PREPARE`, because the module blocks a bot whose master stands
   on another map (`stuck=blocked:bot in an instance`).
2. **Entry.** `PREPARE` sends the GM lines and waits for a rested, living party; `TRAVEL` and `ENTER` bring the party
   in exactly as a run does. With `--enter none` the party must already stand on the plan's map in one instance;
   that same-map check is the one placement proof the watch has.
3. **Start.** One typed action sends `.tb run start <leader> <plan>`. The answer must carry exactly one run record
   for that leader and plan; anything else stops the watch. With `--attach` no start is sent: the watcher waits for
   the run another client started (up to `--attach-wait`) and attaches to it, refusing a run of another plan.
4. **Watch.** Every `--poll` seconds it reads the module's run status and event ring, prints the progress (leg,
   step, pulls, deaths, wipes, pauses with their reasons) and applies no order. Every `--frame-every` seconds it
   also reads `.tb state` and `.tb threat` and writes one trace frame plus the new combat-log rows.
5. **Budgets.** It ends the watch on the time budget, on the wipe budget, when no new event arrived for
   `--watchdog-minutes` (a running run is then paused with a reason and reported), and when a pause has stood for
   `--pause-minutes` (`paused-stuck`).
6. **Rescue.** A run paused on a wipe for 300 s gets one `.tb revive party` (counted as practice) and one
   `.tb run resume`, at most 8 times. The rescue runs before the pause budget, so a wipe pause it can clear is not
   reported as stuck.
7. **Stop.** The stop path sends `.tb run stop`, waits (bounded) for the module's state to reach stopped, and
   writes the summary. A lost link ends the watch at once and nothing is sent after it.

The GM combat-log watch (`--combatlog-watch`) is started on the tank once the party is in: `group` (the tank and its
party, the default), `unit` (the tank only) or `off`. A core without the command logs one warning and the run goes
on; the summary then says `unsupported` and the log is in range only.

# Flags

| Flag | Default | Meaning |
|---|---|---|
| `--leader` | required | The bot that leads the module's run. |
| `--plan` | required | The plan file; its name must be the plan the module runs (or, with `--attach`, the plan of that run). |
| `--character` | required | The observing character. |
| `--enter` | `tele` | `tele`, `portal` or `none` (already placed). |
| `--max-minutes` | 120 | The watch's budget, 1-1440. |
| `--max-wipes` | 3 | The wipe budget: ends once the module's record is past it; `0` allows no wipe, `-1` no budget. |
| `--practice` | on | Allow the practice rescue when a run stays paused on a wipe. |
| `--watchdog-minutes` | 10 | End the watch when no new run event arrives for this many minutes (1-120). |
| `--pause-minutes` | 5 | End the watch as paused-stuck when the module's pause stands this long (1-120). |
| `--poll` | 2 | The status and event poll interval in seconds (1-30). |
| `--attach` | off | Attach to the leader's run started elsewhere instead of starting one. |
| `--attach-wait` | 300 s | How long `--attach` waits for that run (1 s - 24 h). |
| `--frame-every` | 2 | The frame interval in seconds (2-60); `0` turns frames off. |
| `--combat-max-mb` | 64 | The combat log's size bound in MiB (1-4096); a page that would cross it is not written and the summary says the log is truncated. |
| `--combatlog-watch` | `group` | `group`, `unit` or `off`. |
| `--decisions-pull` | off | After the run, read the module's decision rows of that run and write them beside the record as `<run_id>.decisions.jsonl` (see [traces and records](../testing/traces-and-records.md#the-decisions-file)). Needs `--out`; a failed read is a warning in the summary and never fails the watch. |
| `--repulls` | 2 | Report card: flag a step re-pulled more than this many times (0-100). |
| `--member-deaths` | 3 | Report card: flag a member with at least this many deaths and two above the next (1-20). |
| `--slow-factor` | 2 | Report card: flag a leg over this many times its plan's recorded time (1-20). |
| `--out` | none | Folder for the watch's trace and summary; empty writes none. |
| `--map` | off | After the summary, write the run's map (SVG and HTML replay) into `--out`; a failure is one log line and never fails the watch. |
| `--navmesh` | none | mmaps folder for the live entry walk. |
| `--dry` | off | Run the watch machine against the scripted module; no network. |
| `--module-tactics` | none | The module's `ai_boss_tactic` rows as JSON (one object per row: `map`, `boss_entry`, `kind`, `target_entry`, `spell_id`, `priority`, `param`, `hp_min`, `hp_max`) to judge the plan's fight steps against in the report; without it the report says the check was not made. |
| `--no-preflight`, `--preflight-only`, `--serve-dir`, `--export`, `--soap-*` | | As on [dungeon-run](dungeon-run.md); the preflight mode is `dungeon-watch` and checks the leader bot. |

# Examples

Walk the watch's own machine first, with no server:

```bash
forever-agent forever dungeon-watch --leader LEADER --plan dungeons/ragefire_chasm.json \
    --character OBSERVER --dry
```

Watch a module-led run and record it:

```bash
forever-agent forever dungeon-watch --leader LEADER --plan dungeons/ragefire_chasm.json \
    --character OBSERVER --rest example.invalid:8080 --rpc example.invalid:8081 --ca ca.pem \
    --account ACCOUNT --serve-dir ./serve-slot --export ./export/plan_ragefire_chasm.sql \
    --out ./runs/ragefire-watch --max-minutes 90 --map --navmesh ~/mmaps-forever
```

Follow a run another client already started:

```bash
forever-agent forever dungeon-watch --leader LEADER --plan dungeons/ragefire_chasm.json \
    --character OBSERVER --attach --attach-wait 10m --out ./runs/ragefire-watch
```

# What you see

Progress lines while it runs, then the watch's end and its report card:

```text
watch: started plan=ragefire_chasm state=running step=1/214
watch: state=running step=41/214 pulls=4/31 deaths=0 wipes=0 pauses=0 elapsed=212s leg=2 hall
watch: practice rescue 1/8: paused on a wipe for 305s; revived the party
watch: resumed after the rescue
watch: run done plan=ragefire_chasm elapsed=3241s pulls=31 deaths=4 wipes=1 pauses=1
watch: stop done, events 412, rescues 1, deaths 4, wipes 1, pauses 1, clear=true
summary: runs/ragefire-watch/r-20261124T200500-cd34.forever-run.json
trace: runs/ragefire-watch/r-20261124T200500-cd34.jsonl
```

With `--out` the watch writes:

- `<run_id>.jsonl`: the trace (frames, events, the module's own progress).
- `<run_id>.forever-run.json`: the summary (`forever-run` version 1), extended with the `watch` block: the module's
  state, leg and step, `pull`/`pulls`, `deaths`, `wipes`, `pauses`, `elapsed_s`, `why`, the boss map, `rescues`,
  `events`, the frame and combat counters, `pause_log`, `legs`, `step_log`, `death_log`, `loose_pulls`,
  `rescue_log` and the instance's `encounters`.
- `<run_id>.combatlog.jsonl`: the combat log rows, when the session has a log.
- `<run_id>.decisions.jsonl`: the module's decision rows of the run, with `--decisions-pull`: one JSON object per
  line, oldest first. The summary's `decisions` block counts them (`rows`, `pages`, `bytes`, `truncated`) or says why
  none came (`note`).

The report card (`card: ...`) names the verdict `CLEARED`, `STOPPED AT <leg/step> (<reason>)` or `PAUSED-STUCK AT
<leg/step> (<pause reason>)`, or `FAILED (<reason>)`, plus the flags (re-pulled steps, member deaths, slow legs)
and the evidence for each.

# The tactics check

A plan's boss fight steps (`bosses[].tactics`: interrupt this cast, kill that add first, leave that blast) are the
agent's own and are never exported to the server module; on a watched run the bots fight by the module's own boss
tactics rows, its `ai_boss_tactic` table. So a plan step the module's rows contradict, or do not carry, is a step
nobody runs. After the watch, `--module-tactics FILE` judges every such step against those rows and names each
disagreement.

`FILE` is the table's rows as a JSON array, one object per row with the column names the check reads (`map`,
`boss_entry`, `kind`, `target_entry`, `spell_id`, `priority`, `param`, `hp_min`, `hp_max`; a column it does not read
is ignored). The health band is part of the table's key, so two phase-banded rows of one boss are two rows; it is read
but not compared. An export of this query gives the file:

```sql
SELECT map, boss_entry, kind, target_entry, spell_id, priority, param, hp_min, hp_max FROM ai_boss_tactic
```

The file is read before the run. A file that is not a JSON array, a row without a map, a boss entry or a kind, a row
of the wrong shape (an `interrupt` without a spell, a `kill_first` without a target, an `avoid_cast` without its
yards), a duplicate key or more than 20000 rows is a usage error naming the file and the row, and the watch exits
`2` before it touches the server.

The check counts one fight step per `interrupt` entry, per `kill_first` entry, per `never_target` entry and per
`move_out` row of each boss block, and compares the three axes the plan and the module share:

| Axis | The plan says | The module's rows say |
|---|---|---|
| interrupts | `interrupt`, by id or name | `interrupt` rows: a plan spell no such row names is a disagreement; a name is resolved to every id the spell table gives it |
| kill order | `kill_first`, `never_target` | `kill_first` rows with priorities: an entry with no row disagrees, and so does a plan order against the module's priorities (lower first; equal priorities are one wave); a `never_target` the module kills first disagrees |
| avoid | `move_out` cast rows with yards | `avoid_cast` rows with yards: a spell with no row disagrees, and so does a plan that leaves fewer yards than the widest row for any of the spell's ids |

The block's `dispel` and `no_dispel` steps, a `move_out` ground rule, a `never_target` the module has no row for, a
boss the file does not name and a spell name the build cannot resolve are listed as not checked: the module's table
has no row that could answer them, so they are never counted as agreement.

The lines print after the report card, and when the watch wrote a record they are also written beside it as
`<run_id>.tactics-check.txt`:

```text
tactics: 3 plan fight steps checked against the module's 62 rows: 1 disagree
tactics: leg 1 "revelosh" boss 6910: interrupt "Lightning Bolt": the module's rows interrupt Chain Lightning (16006), Chain Lightning (15117) only
tactics: not checked: leg 2 "sentinel" boss 7023: move_out 10132 (ground): the module's rows have no ground-effect row the check reads
```

Without `--module-tactics` the section is one line, `tactics: not checked (no --module-tactics file): the plan's
fight steps are only judged against the module's boss tactics rows`; a plan with no block, or a block the file cannot
judge, says not checked too. The section is never a pass it did not establish. `--dry` writes the same section.

# Exit codes

| Code | Meaning |
|---|---|
| `0` | The run cleared. |
| `2` | A usage error or a plan this build refuses. |
| `5` | The watch ended without a clear, or the session ended without a watch result. |
| `7` | The link to the server was lost. |
| `13` | The preflight failed; nothing was started. |
| `3`, `4`, `6` | Login errors. |

# Common problems

- `the plan is refused: ...` — the plan file does not load; fix it before the watch costs a session.
- `start:<code>` — the module refused the run start. `start:foreign-bot` means another client leads that run: use
  `--attach`. `start:bad-report` means the answer did not carry exactly one run record.
- `enter:party-map` — the party's members are not all on the plan's map in one instance.
- `paused-stuck` — the module paused the run and did not resume by itself (it never does); the watch does not
  resume it on its own either, except through the practice rescue of a wipe pause.
- `status:records` — the module's status answer carried not exactly one run; the watch cannot tell what it is
  watching and stops.
- `blind` — three status or event reads failed over 30 s; the last error is printed.
