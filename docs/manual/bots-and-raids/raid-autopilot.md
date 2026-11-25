---
type: Command
title: forever raid-autopilot
description: Keep the Forever raids of a walk folder running and restarting without a person watching each one.
tags: [bots-and-raids, raid, autopilot, slots]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T23:40:00Z }
sources:
  - id: autopilot-cmd
    resource: cmd/forever-agent/raid_autopilot_cmd.go
  - id: autopilot-rules
    resource: internal/raidwalk/autopilot.go
  - id: raid-watch
    resource: internal/forever/driver/raid_watch.go
  - id: slots
    resource: internal/slots
---

> **Not yet proven on a live server.** The `--forever` supervisor is exercised against a fake binary; no live raid run has been made, so the slot configuration and the watch's summaries are not proven end to end yet. See [what has been proven](../getting-started/what-is-forever-agent.md#what-has-been-proven-and-what-has-not).

`raid-autopilot --forever` is a supervisor for several Forever raid runs at once.
It reads a targets file in a walk folder, launches `forever raid-watch` for each
target on its own slot, watches each attempt, and on every end, a stall it can see,
or a used-up budget tears the slot down and starts the goal again. It never uses
slot 3, never sends `.bot run stop`, never uses the placement and instance-clearing
flags of the Turtle walk, and stops cleanly on a `YIELD` file.

Use it when you want a raid farmed or re-run unattended: a target restarts until
its goal (the whole plan, a leg, or one boss) is met, and a target that needs a
person is escalated and left alone instead of looping.

It is the **only** supervisor a Forever walk has. The Turtle restarter
(`scripts/walk-super.sh`) rebuilds a restart from the walk's own placement flags, and a Forever watch has none, so it
refuses a command that starts with `forever` (exit 2, `use raid-autopilot --forever`) instead of restarting it.



# Synopsis

```
forever-agent raid-autopilot --forever [--slots FILE] [--dry-run] <walk-dir>
forever-agent raid-autopilot [--foreground] [--binary PATH] [--poll D] [--stall D]
    [--settle D] [--status-every D] [--instance-cap-per-account N] [--dry-run] <walk-dir>
```

The second form supervises Turtle `raid-walk` targets. This page covers
`--forever`, which supervises Forever plans with the watch of
[raid-watch](raid-watch.md); the two modes share the files, the decision rule, the
lease handling and the YIELD.

# Flags

| Flag | Default | Meaning |
|---|---|---|
| `--forever` | off | The targets are Forever raid plans: each attempt is `forever raid-watch` on the slot file's `forever` server. |
| `--slots` | the standard slots file | The slots file holding the Forever servers and their slots (`--forever` only). |
| `--foreground` | off | Run in this process, without detaching. |
| `--binary` | this executable | The `forever-agent` binary the attempts launch. |
| `--poll` | `10s` | How often the runs are read. |
| `--stall` | `15m` | No step or boss-HP change for this long re-places the run. Not used with `--forever`: the watch's own watchdog and budget end a stuck run, and a live watch counts as progress. |
| `--settle` | `20s` | Wait after a teardown before the next launch. |
| `--status-every` | `1m` | How often `status.txt` is written. |
| `--instance-cap-per-account` | `5` | The core's limit of fresh instance ids one account may enter in an hour, passed on to every Turtle walk; a Forever attempt is a watch, which has no placement, so the flag is not passed on. |
| `--dry-run` | off | Validate the targets, print one command per line, launch nothing. |

By default the supervisor detaches itself (a new session), writes its pid file and
logs, and returns. A second start in the same folder is refused while the first
holds the folder's lock, however the second was started.

# The targets file

`<walk-dir>/targets.txt` holds one goal per line:

```
<id> <server> <slot> <holder> <goal> -- <forever raid-watch args...>
```

- `<id>` names the target; lines may repeat it, and its lines are walked in file
  order. Blank lines and lines starting with `#` are skipped.
- `<server>` must be a server of the slots file with a complete `forever` block
  (`rest`, `rpc`, `ca`).
- `<slot>` is the slot id and must exist; slot 3 is refused outright.
- `<holder>` is the lease holder name recorded in the slots file.
- `<goal>` is `full`, `leg:<L>` or `boss:<ENTRY>`. `practice:<ENTRY>:<CYCLES>` is
  refused for a Forever target: the watch's `--practice` is the wipe rescue, not a
  mechanic loop.
- The arguments after `--` are a `forever raid-watch` command line. The first
  argument is the plan file and `--leader` is required. The autopilot adds the
  connection flags from the server's `forever` block, the account and character of
  the slot, the attempt's `--serve-dir` and `--out`, and the build when the block
  names one.

Flags that are refused at load, because the watch has no step for them:
`--place`, `--reuse-instance`, `--gm-instance-data`, `--from`, `--boss-wipe-max`,
`--boss-wipe-fresh`, `--wipe-end`, `--sql`, `--until`, `--account` and `--plan`;
so are `--server`, `--slot`, `--holder` and `--out`, which the autopilot manages.

Two targets may not name the same server and slot: one slot runs one attempt at a
time.

```
# two raids on one server, each until its own boss
z1 myserver 1 owner boss:14834 -- ./plans/raid/example-one.json --leader Tankone --layout ./plans/raid/layout.txt
z2 myserver 2 owner full -- ./plans/raid/example-two.json --leader Tanktwo
```

# What it does with one attempt

1. Resolves the target's slot in the slots file and takes its lease: the
   supervisor process is the lease's holder, so a dead supervisor frees the slot at
   once, and a live attempt renews it every five minutes.
2. Builds the attempt's slot folder with `slot setup --forever` and launches
   `forever raid-watch` with `<walk-dir>/<id>-<seq>/` as its output folder and
   `<walk-dir>/<id>-<seq>.out` as its log.
3. Reads progress from the watch's `summary.json` (step, steps, end reason, bosses,
   deaths) once the attempt exits. While a watch is alive, progress is assumed:
   the watch writes no file the autopilot reads mid-run, and its own watchdog and
   time budget end a stuck run.
4. Decides:

| Situation | Decision |
|---|---|
| The goal is met (boss killed, full plan reached its last step, or the leg ended) | Advance to the id's next line. |
| Exit 2 (the plan was refused) | Stop the id and escalate it for a person. |
| Exit 15 three times in a row (the walk found the slot's lease held elsewhere) | Stop and escalate. |
| Any other end | Tear the slot down and launch the same goal again, with the backoff of its no-progress count: 20 s, 1 m, 2 m, 5 m, then 10 m. |
| A launch or a teardown fails three times | Stop and escalate the id in `EVENTS.txt` and `status.txt`. |
| All of an id's goals are met | The id is done and holds no slot. |

A caught-up goal starts again from the beginning: the watch has no `--from`, so
every attempt starts the goal's run from its own start.

# Files in the walk folder

| File | Contents |
|---|---|
| `targets.txt` | The goals (yours to edit; a running supervisor re-reads it and picks up changed lines). |
| `EVENTS.txt` | One line per event: `time id action: detail`, plus `all` lines for global events. |
| `status.txt` | A snapshot, rewritten every `--status-every`: one line per id with its goal, attempt, folder and state, or an `ESCALATE` line. |
| `autopilot-state.json` | The durable checkpoint (per id: current goal, goals met, last step, attempt number, the child's pid and folder), rewritten on every transition. |
| `autopilot.log` | The detached supervisor's own log. |
| `autopilot.pid` | The supervisor's pid, informational only; the lock is `autopilot.lock`. |
| `YIELD` | Create it to stop: every attempt is torn down, every lease released, then `YIELD-DONE` is written. |
| `<id>-<seq>/`, `<id>-<seq>.out` | One attempt: the watch's files (trace, run summary, `summary.json`, `slot/`) and its log. |

# Running it

```sh
# Check the targets and see the commands without launching anything.
forever-agent raid-autopilot --forever --slots ./slots.json --dry-run ./walk-zg

# Start the supervisor on that folder.
forever-agent raid-autopilot --forever --slots ./slots.json ./walk-zg

# Watch progress from another shell.
tail -f ./walk-zg/EVENTS.txt

# Stop it cleanly.
touch ./walk-zg/YIELD
```

Status lines look like:

```
z1 goal=boss:14834 attempt=3 out=z1-3 running step=17 progress=4s ago reuse=false
ESCALATE z2: launch: slot setup myserver/2: ... (3 tries)
```

The supervisor exits 0 when every target's goals are met (or on a clean YIELD or
signal stop), and 31 when at least one id was escalated for a person.

# Common problems

| Symptom | What to do |
|---|---|
| `another autopilot runs in <dir>` | A supervisor holds the folder's lock. Use YIELD, or a different folder. |
| `server "x" has no complete forever block` | The slots file's server entry needs `rest`, `rpc` and `ca`. |
| `--place is raid-walk's; the forever raid-watch has no such step` | A target line carries Turtle-only placement, resume or GM flags; remove them. |
| A target escalates with `launch` three times | The `slot setup --forever` or the lease failed; read the line's detail and the attempt's `.out`. |
| `exit 2: ... the walk refused its arguments` | The plan is not a raid plan, its `raid` block is wrong, or a flag the watch cannot run is on the line. |
| Attempts keep restarting every few seconds | Check the attempt's log; a repeated immediate failure is usually a refused login, not the run. |
