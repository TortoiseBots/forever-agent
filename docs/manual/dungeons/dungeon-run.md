---
type: Command
title: forever dungeon-run
description: Run one dungeon plan with the agent as commander of five bots, and read the order list, trace and summary it leaves.
tags: [dungeons, run, commander, command]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T21:05:00Z }
sources:
  - id: dungeon-run
    resource: cmd/forever-agent/forever_dungeon.go
  - id: driver-run
    resource: internal/forever/driver/run.go
  - id: driver-states
    resource: internal/forever/driver/states.go
  - id: recorder
    resource: internal/forever/driver/record.go
---

> **Not yet proven on a live server.** The run machine is exercised with `--dry` against the scripted world; its live mode is wired but no live dungeon run has been made. See [what has been proven](../getting-started/what-is-forever-agent.md#what-has-been-proven-and-what-has-not).

# Synopsis

```bash
forever-agent forever dungeon-run --plan FILE --party TANK,HEALER,DPS,DPS,DPS \
    [--rest host:port --rpc host:port --ca FILE --account EMAIL] --character NAME \
    [--enter tele|portal|none] [--from-leg N] [--legs a,b] [--reset] [--max-minutes N] \
    [--max-wipes N] [--practice=true] [--out DIR] [--navmesh DIR] [--dry] \
    [--no-preflight | --preflight-only] [--serve-dir DIR] [--export FILE] \
    [--soap-user NAME --soap-password-file FILE]
```

`dungeon-run` is the commander mode: the agent's own character does not fight. It orders the five bots of the
party with the bot module's orders and typed setup actions — GM lines, the entry, a revive, an instance reset —
and drives one registered plan from its first leg to its last boss, keeping its own safety budgets.

Choose the mode by who leads: `dungeon-run` for five named bots under the agent's orders, [dungeon-play](dungeon-play.md)
when the agent plays one real character next to four bots, and [dungeon-watch](dungeon-watch.md) when a server
module runs the dungeon by itself and the agent only watches.

# What it does

The run is a state machine over the plan. Each state does one thing and returns the next:

| State | Work |
|---|---|
| `PREPARE` | Turns GM mode on and GM visibility off, reads the party roster and checks it (five members, the party's leader is the tank, the commander is this character, every name present, everyone online and at least level 10, at least one tank, one healer and three damage dealers), brings the commander to the tank's side, then waits for the party to be rested and alive. |
| `TRAVEL` | Teleports the commander (its GM teleport, else its own move) and the party to the plan's portal on the outer map, orders the follow, and waits until every member stands there. |
| `ENTER` | Takes the party in: `tele` (the default) has the module bring the party in and then moves the commander to the tank; `portal` walks the commander into the entrance trigger; `none` expects the party placed. |
| `LEG_WALK` | Walks each leg's `via` points and `to` on the navmesh, keeping out of the avoid boxes, through the direct and exact waypoints, and the holds. |
| `PICK` / `POSITION` / `PULL` | Picks the next pack, walks to where the party fights it, sends the pull and waits for the tank's aggro. |
| `FIGHT` | Runs the fight: kill order, crowd control, runners, patrols that come by, adds, a long-fight note, a fight cap. |
| `REST` | Waits out the rest gates between packs. |
| `DEAD_WAIT` / `WIPE` | Waits for a dead member, then for a wipe's recovery; the practice revives are the last resort. |
| `BOSS` | Waits at the boss's point for him (a patrolling boss up to `wait_s`), kills `first` and then the boss, and fails the leg if `loot_item` is not in the leader's bags. |
| `LEAVE` / `DONE` | Leaves the instance (`--reset`: leave and reset, only from outside), then reports. |
| `STOP` | The run is over; the result carries the stop reason. |

Every line it sends goes through one gate: a movement (a teleport, the entry, the leave) needs a state report
taken immediately before, newer than the last order, with nobody in combat or dead; a revive needs the practice
budget and is counted; an instance reset needs the run's end and a fresh report showing the whole party outside.

# Flags

| Flag | Default | Meaning |
|---|---|---|
| `--plan` | required | The plan file. |
| `--party` | required | Five names, tank first, then healer, then three damage dealers. |
| `--character` | required | The agent's character: the commander. |
| `--enter` | `tele` | `tele` teleports the party in, `portal` walks it through the plan's portal, `none` expects it placed. |
| `--from-leg` | `0` | First leg, 0-based. |
| `--legs` | every leg | Leg names to run, comma separated, in plan order. |
| `--reset` | off | Leave and reset the instance after a clear. |
| `--max-minutes` | 120 | The run's budget, 1-1440. |
| `--max-wipes` | 3 | The wipe budget: the (n+1)th wipe ends the run; `0` allows no wipe, `-1` no budget. |
| `--practice` | on | Use the last-resort revives when the party cannot recover by itself. |
| `--out` | none | Folder for the run's trace and summary. |
| `--navmesh` | none | mmaps folder for the live walker (the dry walker walks straight lines). |
| `--dry` | off | Run the whole machine against the scripted world; no network, no login. |
| `--no-preflight` | off | Start without the [preflight](preflight.md). |
| `--preflight-only` | off | Run the preflight and stop; no login, no run. |
| `--serve-dir` | none | The `forever serve --dir` whose slot, lease and module link the preflight checks. |
| `--export` | none | The plan's exported migration, or its folder, for the preflight's export check. |
| `--rest`, `--rpc`, `--ca`, `--account`, `--server-name`, `--build`, `--locale` | — | The login connection, as on every Forever command that logs in. |
| `--soap-host`, `--soap-port`, `--soap-user`, `--soap-password-file`, `--soap-password-env` | SOAP defaults / env | The SOAP console the preflight and the party reads use; an empty `--soap-user` skips those checks. |

# Examples

Check the plan, the export and the party without logging in:

```bash
forever-agent forever preflight --mode dungeon-run --plan dungeons/ragefire_chasm.json \
    --character COMMANDER --party TANK,HEALER,DPS1,DPS2,DPS3 --serve-dir ./serve-slot
```

Play the whole run against the scripted world first:

```bash
forever-agent forever dungeon-run --plan dungeons/ragefire_chasm.json \
    --party TANK,HEALER,DPS1,DPS2,DPS3 --character COMMANDER --dry
```

Then run it live, writing the record:

```bash
forever-agent forever dungeon-run --plan dungeons/ragefire_chasm.json \
    --party TANK,HEALER,DPS1,DPS2,DPS3 --character COMMANDER \
    --rest example.invalid:8080 --rpc example.invalid:8081 --ca ca.pem --account ACCOUNT \
    --navmesh ~/mmaps-forever --out ./runs/ragefire --max-minutes 90 --max-wipes 2
```

# What you see

The order list, the run's counters, the states it passed and the files it wrote:

```text
orders:
      2.0s tank      gm        on
     95.4s tank      pull      ...
run: stop clear, 9 pulls, 0 wipes, 0 deaths, 0 practice revives, clear=true
states: PREPARE TRAVEL ENTER LEG_WALK PICK PULL FIGHT REST ... BOSS LEAVE STOP
summary: runs/ragefire/r-20261124T203000-ab12.forever-run.json
trace: runs/ragefire/r-20261124T203012-ab12.jsonl
```

With `--out` the run writes:

- `<run_id>.jsonl`: the trace, one JSON record per line (frames, orders, events).
- `<run_id>.forever-run.json`: the summary (`forever-run` version 1): `clear`, `stop_reason`, the bosses,
  `wipes`, `deaths`, `practice`, `duration_s`, `last_leg`, `instance` and the pulls.

A dry run with `--out` writes the same files. Without `--out` the run writes nothing.

# Exit codes

| Code | Meaning |
|---|---|
| `0` | The run cleared. |
| `2` | A usage error, a plan this build refuses, or a flag combination that cannot work. |
| `5` | The run did not clear, or the session ended without a run result. |
| `7` | The link to the server was lost. |
| `13` | The preflight failed; nothing was started. |
| `3`, `4`, `6` | Login errors: the realm rejected the login, the realm is missing, or the server closed the connection. |

# Common problems

- `busy: a run preflight holds the command channel, retry` — while `dungeon_run` starts, its own preflight holds the
  command channel for each read (the module probe and each `.pinfo`). A `bot`, `gm` or `commander` call (or another
  `dungeon_run`) arriving in that window is refused at once with `-32003` and nothing sent; `status` and every other
  method keep answering. Retry the call after the preflight; it never outlasts the channel's 35 s wait plus each
  read's own deadline. See [RPC basics](../daemon/rpc-basics.md#one-call-at-a-time).
- `the plan is refused: ...` — the loader's message names the field. Fix the plan, not the command line.
- `prepare:party` / `prepare:roster` / `prepare:names` / `prepare:offline` / `prepare:level` / `prepare:roles` /
  `prepare:leader` / `prepare:commander` — the party's roster does not match the five names: the module's roster read
  failed, a member is missing, offline or under level 10, a role is missing, or the leader or commander is not the
  tank. [Stop reasons](stop-reasons.md) lists each one.
- `prepare:gm` or `prepare:gm-visible` — the account may not turn GM mode on; the run stops before anything moves.
- `stuck` or `budget` — no progress within the plan's watchdog levels or the run's minutes are spent; the stop
  reasons page lists what each means.
- `boss-missing` — the boss was not at the plan's point within the state's wait (a patrolling boss may need a
  larger `wait_s` in the plan).

Every stop reason a run can end with is listed in [stop-reasons](stop-reasons.md).
