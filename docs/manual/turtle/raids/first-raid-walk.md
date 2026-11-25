---
type: Guide
title: Walk your first raid
description: Run one trash walk on a test server from the first command to the summary.
tags: [turtle, raids, raid-walk, tutorial]
status: stable
generated: { by: forever-agent docs, at: 2026-11-05T20:35:00Z }
sources:
  - id: walk-cmd
    resource: cmd/forever-agent/raid_walk_cmd.go
  - id: tiers
    resource: internal/raidwalk/tiers.go
---

This page describes the Turtle 1.12 stack. These commands refuse a ForeverCore server.

# Walk your first raid

> **Turtle only.** The raid walk commands (`raid-walk`, `raid-autopilot`, `raid-suite`, `walk-preflight`) run
> against the Turtle stack. Against a Forever server, these commands refuse to run; `forever raid-watch` and `raid-autopilot --forever` are the Forever path.

You walk a raid plan the server knows, with hired bots and the TortoiseBots run leader, and stop before the first boss. This takes one command plus reading the summary.

## Before you start

You need a slot on a test server, a folder with the raid plan JSON (`<walk-dir>/<plan>.json`), and the deployed migration (`--sql`) so step numbers come from the server's rows. Some raids already have a sourced hire tier in the committed table; any other raid needs `--tier N` (see [Hire gear tiers](tiers.md)).[^tiers]

## 1. Dry-run the walk

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --dry-run
```

The dry run validates everything and prints the phase plan without leasing or sending anything. Fix every usage error it reports before you go on.

## 2. Walk the trash

```sh
turtle-agent raid-walk <plan> --server <server> --slot <slot> \
    --dir <plans-dir> --sql <world-sql> --speed 3.0 --hold-minutes 5
```

`--until` defaults to `boss`: a trash walk that stops before the first boss step wakes it. `--speed 3.0` marks a GM test run in the summary. The walk leases the slot, sets it up, logs the agent in, hires and gears the roster, binds the raid to its main tank, enters, starts the run and watches it.

## 3. Read the result

The output dir (default `./raid-walk-<plan>-<stamp>`) holds `summary.txt` and `summary.json`. Exit 0 means the run ended by its `until`. Exit 19 means a wipe or boss engagement happened; the notes say which (`wipe resume skipped: ...`) and `summary.json` counts `wipe_resumes`.

`summary.txt` ends with the result line, for example with an assisted walk `... ASSISTED`. A shortfall below the hire cap fails the walk with exit 18.

## 4. See every pull

```sh
turtle-agent raid-walk report <walk-dir> --clock-offset 1h
```

This reads the walk's files only (no server, no login) and prints the per-pull table: step, fight, engaged units, deaths, contacts before the pull order and neighbours that joined mid-fight (see [`raid-walk report` reference](report-reference.md)).

Next: [Run a raid walk](run-a-raid-walk.md) for the full `until` choice and the other flags.
