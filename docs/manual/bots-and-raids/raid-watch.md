---
type: Command
title: forever raid-watch
description: Watch a raid the server's own bot module leads, from outside, with budgets and a raid summary at the end.
tags: [bots-and-raids, raid, watch, bots]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T23:40:00Z }
sources:
  - id: watch-cmd
    resource: cmd/forever-agent/forever_watch.go
  - id: raid-watch
    resource: internal/forever/driver/raid_watch.go
  - id: driver-run
    resource: internal/forever/driver/run.go
  - id: driver-record
    resource: internal/forever/driver/record.go
  - id: reportcard
    resource: internal/forever/reportcard/card.go
  - id: preflight
    resource: cmd/forever-agent/forever_preflight.go
  - id: plan
    resource: internal/dungeon/plan.go
---

> **Not yet proven on a live server.** The watch is exercised against the scripted world; no live raid run has been made with it yet, and the first live run is what proves the live path end to end. See [what has been proven](../getting-started/what-is-forever-agent.md#what-has-been-proven-and-what-has-not).

`forever raid-watch` brings a raid party in as a raid plan asks, starts the
server module's own run, and then only reads: the module's progress, its events
and its instance report. It never orders a bot during the run. It is the raid
version of the dungeon watch: before the party is prepared the module's commander
card must show a raid of at least the plan's size, and at the end the watch writes
`summary.json` in the shape the raid tools and [raid-autopilot](raid-autopilot.md)
read.

Use it when the module (not this client) runs the fights and you want the run
observed, bounded, and recorded — for example overnight, or while you watch the
first pulls of a new plan.

# Synopsis

```
forever-agent forever raid-watch --leader NAME --plan FILE [--layout FILE]
    [--rest host:port --rpc host:port --ca FILE --account EMAIL] --character NAME
    [--enter tele|portal|none] [--max-minutes N] [--max-wipes N] [--practice=true]
    [--watchdog-minutes N] [--pause-minutes N] [--poll SEC] [--attach [--attach-wait SEC]]
    [--frame-every SEC] [--combat-max-mb N] [--combatlog-watch group|unit|off]
    [--repulls N] [--member-deaths N] [--slow-factor X] [--decisions-pull] [--out DIR] [--map]
    [--navmesh DIR] [--dry] [--no-preflight | --preflight-only]
    [--serve-dir DIR] [--export FILE] [--soap-user NAME --soap-password-file FILE]
```

# What it does

1. Loads the plan. It must be a raid plan (`kind: raid`) with its `raid` block:
   `{"size": 10|20|40, "tanks": n, "healers": n, "dps": n}`, whose sum must be the
   size. See [dungeon plans](../dungeons/plans.md).
2. Logs in and checks the module's commander card before `PREPARE`: the card must
   show a raid with at least the plan's size of members. When it does not, the group has
   at least the plan's size and this character leads it (the card's player-led
   flag), one `convert_raid` goes out and the card is read again.
3. Applies `--layout`: each line moves one bot to its raid subgroup with
   `raid_move`, read back by the group layer. A member already in its subgroup gets
   no packet.
4. Sends `PREPARE` and enters (`--enter`), starts the module's run, then polls the
   module's run status and event ring at `--poll` seconds and writes trace frames.
5. Applies the budgets: the watch's time, the wipe budget, the watchdog on new
   events, the pause budget, and the practice rescue when a run stays paused on a
   wipe. The stop reasons are the dungeon watch's, plus the raid ones below.
6. Writes the run's files and a report card, and prints the end line.

# Flags

| Flag | Default | Meaning |
|---|---|---|
| `--plan` | required | Raid plan file (version 1, with its `raid` block). |
| `--leader` | required | The bot that leads the module's run. |
| `--character` | required | The character that logs in and watches. |
| `--layout` | none | Raid layout file: one `name subgroup` or `name = subgroup` per line, subgroups 1-8, at most 40 entries, `#` comments. |
| `--enter` | `tele` | How the party enters: `tele`, `portal`, or `none` (already placed). |
| `--max-minutes` | `120` | The watch's budget (1-1440). |
| `--max-wipes` | `3` | The wipe budget: the (n+1)th wipe ends it; `0` allows no wipe, `-1` is no budget (-1 to 20). |
| `--practice` | `true` | Use the practice rescue when the run stays paused on a wipe. |
| `--watchdog-minutes` | `10` | Pause and end the watch when no new run event arrives for this long (1-120). |
| `--pause-minutes` | `5` | End the watch as paused-stuck when the module's pause stands this long (1-120). |
| `--poll` | `2` | Status and event poll interval in seconds (1-30). |
| `--attach` | off | Attach to a run started elsewhere instead of starting one: no entry step, and the run must be this plan's. |
| `--attach-wait` | `300s` | How long `--attach` waits for that run (1s-24h). |
| `--frame-every` | `2` | Frame interval in seconds: each tick reads `.tb state` and `.tb threat` and records a frame; `0` turns frames off (2-60). |
| `--combat-max-mb` | `64` | The combat-log file's size bound; a page that would cross it is not written and the summary says the log is truncated (1-4096). |
| `--combatlog-watch` | `group` | The core's GM combat-log watch: `group`, `unit` or `off`. A core without the command logs one warning and records the in-range log. |
| `--decisions-pull` | off | Read the live run's decision rows on a stated interval, and once more just before the stop, into `<run_id>.decisions.jsonl` beside the record. The module erases a run's capture when it ends, so only a watched, live run can be read. Needs `--out`; a failed read is a warning in the summary. See [traces and records](../testing/traces-and-records.md#the-decisions-file). |
| `--repulls` | `2` | Report card: flag a step re-pulled more than this many times (0-100). |
| `--member-deaths` | `3` | Report card: flag a member with at least this many deaths and two above the next (1-20). |
| `--slow-factor` | `2` | Report card: flag a leg over this many times its plan's recorded time (1-20). |
| `--out` | empty | Folder for the watch's trace and summary; empty writes none. |
| `--map` | off | After the summary, write the run's map (SVG and HTML replay) into `--out`. Needs `--out`; a failure is one log line and never fails the watch. |
| `--navmesh` | empty | mmaps folder for the live entry walk. |
| `--dry` | off | Run the watch machine against the scripted world (no network). |

The connection flags are the ones every Forever command takes: `--rest`, `--rpc`,
`--ca`, `--server-name`, `--build`, `--game-account`, `--locale`,
`--allow-instance-redirect` and `--world-checks-build-auth-key`. The password
comes from `FOREVER_PASSWORD`, never a flag.

The preflight flags: `--no-preflight` starts without the checks (serve slot, lease,
plan export, module link, SOAP and the bots), `--preflight-only` runs them and
stops, `--serve-dir` names the `forever serve --dir` slot whose lease and module
link are checked, `--export` the plan's exported SQL, and `--soap-user` with
`--soap-password-file` (or `--soap-password-env`) enables the SOAP checks. See
[dungeon preflight](../dungeons/preflight.md).

# Raid setup

The card check happens before anything else is sent to the module, so a wrong
group never costs a run. The watch stops with one of these names:

| Stop reason | What it means |
|---|---|
| `raid:card` | The commander card could not be read, or was gone after `convert_raid`. |
| `raid:convert` | `convert_raid` was refused; the module's own text is the line before. |
| `raid:roster` | The card shows fewer members than the plan's raid size. |
| `raid:not-raid` | The card shows the size but no raid flag. |
| `raid:layout` | A layout name is not on the card, or a `raid_move` was not confirmed. |

A layout that names a bot the card does not hold is a layout for someone else's
raid: nothing is sent for it.

# What you see

```
watch: stop done, events 214, rescues 0, deaths 3, wipes 1, pauses 1, clear=true
summary: ./out/r-20261014T093012-a1b2.forever-run.json
raid summary: ./out/summary.json
trace: ./out/r-20261014T093012-a1b2.jsonl
card: CLEARED (...)
card files: ./out/r-20261014T093012-a1b2.report-card.txt ./out/r-20261014T093012-a1b2.report-card.json
```

With `--out`, the watch writes:

| File | Contents |
|---|---|
| `<run_id>.jsonl` | The trace: frames and events. |
| `<run_id>.forever-run.json` | The run summary: clear, bosses, wipes, deaths, practice rescues, stop reason, last leg, instance, pulls, and the `watch` block (module state, step, leg, pulls, deaths, wipes, pauses, events, rescues, frames, combat rows). |
| `<run_id>.combatlog.jsonl` | The combat log it drained, bounded by `--combat-max-mb`. |
| `summary.json` | The raid summary, for `raid-autopilot` and the Turtle report tools. |
| `<run_id>.report-card.txt`, `<run_id>.report-card.json` | The report card. |

`summary.json` is the tb-raid-walk version 1 shape, and only carries bot names —
never a host, an account or a path:

| Field | Meaning |
|---|---|
| `format`, `version` | `tb-raid-walk`, `1`. |
| `plan`, `map`, `character` | The run's identity. |
| `server`, `slot` | Part of the shape for the Turtle tools; a Forever watch leaves them empty. |
| `started`, `ended`, `end_reason`, `exit_code` | The watch's clock and its stop. |
| `steps_reached`, `steps_total` | The module's own step counters. |
| `roster` | `mt`, `ot` and `rest`, from the roles the party was prepared with. |
| `layout` | `mt`, the `tanks`, and warnings. |
| `boss_kills` | One row per boss the module's instance report knew: `entry`, `step`, `killed`, and `evidence` (the module's own state; the watch credits no kill of its own). |
| `deaths` | The module's death counter. |
| `notes` | `forever raid-watch (the module's own run)`. |

# Exit codes

| Code | Meaning |
|---|---|
| 0 | The run cleared (or the watch ended cleanly). |
| 2 | The plan was refused (not a raid, a bad `raid` block, a field this watch cannot run). |
| 3 | The login server rejected the account. |
| 4 | The realm is missing or offline. |
| 5 | The world server rejected the session, or the watch ended without a clear. |
| 6 | The server closed the connection. |
| 7 | The link was lost during the run. |

# Examples

```sh
# A dry run of a raid plan against the scripted world: no login, no network.
forever-agent forever raid-watch --plan ./plans/raid/example.json --leader Tankone \
  --character Mychar --layout ./plans/raid/layout.txt --out ./out --dry

# A live watch with a place for the map and replay, keeping the default budgets.
forever-agent forever raid-watch --plan ./plans/raid/example.json --leader Tankone \
  --character Mychar --layout ./plans/raid/layout.txt --out ./out --map --navmesh ./mmaps \
  --rest 127.0.0.1:8081 --rpc 127.0.0.1:8082 --ca ./ca.pem --account ACCOUNT

# Watch a run another client started: no entry, no start.
forever-agent forever raid-watch --plan ./plans/raid/example.json --leader Tankone \
  --character Mychar --attach --attach-wait 10m --out ./out
```

Example layout file:

```
# main tank, off-tank, healers, then damage
Tankone 1
Tanktwo 1
Healerone 2
Healertwo = 2
Dpsone 3
```

# Common problems

| Symptom | What to do |
|---|---|
| `raid:not-raid` while the group has the plan's members | The character does not lead the group, so no `convert_raid` is sent: convert the raid yourself, or watch from the leader's character. |
| `raid:roster` | The party is short; hire up to the plan's size before the watch. |
| `raid:layout` naming a bot | The card does not hold that name; check the layout file against `/rpc status` of the party. |
| The watch ends `paused-stuck` | The module's pause stood past `--pause-minutes`; read the run's event ring for why. |
| `watch: the module did not confirm the stop` | The run had not reached `stopped` when the watch ended; the run may still be in the world. |
