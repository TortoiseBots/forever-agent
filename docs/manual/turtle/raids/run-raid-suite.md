---
type: Guide
title: Regression-check plans with the suite
description: Walk every raid plan and judge each walk against its last good walk.
tags: [turtle, raids, raid-suite]
status: stable
generated: { by: forever-agent docs, at: 2026-11-05T20:40:00Z }
sources:
  - id: suite-cmd
    resource: cmd/forever-agent/raid_suite_cmd.go
  - id: compare
    resource: internal/raidwalk/compare.go
---

This page describes the Turtle 1.12 stack. These commands refuse a ForeverCore server.

# Regression-check plans with the suite

> **Turtle only.** The raid walk commands (`raid-walk`, `raid-autopilot`, `raid-suite`, `walk-preflight`) run
> against the Turtle stack. Against a Forever server, these commands refuse to run; `forever raid-watch` and `raid-autopilot --forever` are the Forever path.

`turtle-agent raid-suite` walks a list of raid plans one after another, each with `raid-walk`, and judges each walk against the last good walk of the same plan in a history dir. It is the regression check for the raid plans.[^suite-cmd]

## 1. Describe the raids

Use a suite file (walked in file order; `dir` is required, `sql` and `hires` are optional):

```json
{"format":"tb-raid-suite","version":1,"raids":[
  {"plan":"<plan>","dir":"<plans-dir>","sql":"<world-sql>","hires":""}]}
```

Or take every raid plan in a folder, sorted by plan name, with `--dir DIR`. `--only p1,p2` filters by plan name; `--gap` (default 60s, 0 to 10m) is the pause between two raids.

## 2. Run the suite

```sh
turtle-agent raid-suite --suite <file> --history <history-dir> --server <server> --slot <slot>
```

Each walk lands in `<history>/<plan>/<stamp>/` (walks put there by hand count as history too). Each raid is walked to its first leg's end: a boss in leg 0 stops at the boss, else the walk stops when leg 2 starts, else it runs to the end. The suite never takes `--tier`: a raid's tier comes from the committed table or its `hires` file, and a raid with no sourced tier is refused, not walked.

`--dry-run` prints `plan tier (source) until out` per raid, or `plan: refused: <reason>`; nothing is sent and no out dir is made. `--compare-only` walks nothing: each plan's newest walk in the history is compared with the last good one before it, and a plan with no walk is `skipped`.

## 3. Read the verdicts

Each walk is `ok`, `regression`, `new` (no baseline, exit 0), `failed` (did not exit 0), `refused` or `skipped` (after Ctrl-C the rest are skipped with `interrupted`). A walk is compared with the newest good walk of its plan (exit 0, ended by its `until` or `plan done`, no tier comparison, the same tier, until, speed and test speed, no injected fault) and is a `regression` when its run time is more than 20 % and more than 60 s above the baseline, its deaths are above `base + max(2, 25 % of base)`, its pauses or stops are above the baseline, its end reason changed, or its fights are more than one above it. `faster` and `fewer fights` are info only.[^compare]

The report dir (default `./raid-suite-<stamp>`, refused if it exists) gets `suite.json` (format `tb-raid-suite-report` v1) and `suite.txt`. `--json` prints the JSON on stdout instead of the text; the walks' own output goes to stderr. Exit 0 when no raid regressed or failed; exit 24 when one did; exit 15 for `--slot 3`; exit 2 for usage (including `--slot A,B`: the suite walks one raid at a time).
