---
type: Guide
title: Run the brain
description: Start, watch and stop the combat brain over RPC, with a rotation and a role.
tags: [turtle, combat, brain, rpc]
status: stable
generated: { by: muse-spark-1.3/omp, at: 2026-11-05T20:40:00Z }
sources:
  - id: brain-methods
    resource: cmd/forever-agent/brain_methods.go
  - id: brain-go
    resource: internal/brain/brain.go
---

This page describes the Turtle 1.12 stack. These commands refuse a ForeverCore server.

# Run the brain

The brain is off until you start it. You name at least one strategy and optionally a role, plus a rotation file that replaces the class strategy when you play data instead of Go.

## 1. Check the character is in the world

`brain_start` needs the character logged in and in the world. Anything else is refused:

```json
{"method": "brain_start", "params": {"strategies": ["frost_mage"]}}
```

`brain_start needs the character in the world (state <state>)` means log in first.

## 2. Start it

The smallest start names strategies and lets the brain pick the role from them:

```json
{"method": "brain_start", "params": {"strategies": ["frost_mage"]}}
```

To play a data rotation instead of the Go strategy, name it:

```json
{"method": "brain_start", "params": {"strategies": ["frost_mage"], "rotation": "my_frost"}}
```

Useful extras: `"role": "dps"` pins the role when the strategies could play more than one; `"tank": "<name>"` names the tank you follow; `"no_move": true` keeps your feet still (you cast, you do not walk); `"run_id": "my-run"` names the trace; `"trace": false` writes no trace file.

The answer is the brain status: whether it runs, the run id, and the strategies it resolved. While a dungeon run owns the brain, `brain_start` and `brain_stop` are refused (`<method>: a dungeon run owns the brain (dungeon_stop it first)`); `brain_status` always answers.

## 3. Watch it

```json
{"method": "brain_status", "params": {}}
```

For the play-by-play, read the event log from the returned cursor: `brain_decision` events name the winning entry (`trigger`, `action`, `result`), `brain_idle` marks ticks with nothing to do, and `cooldown_start` / `cooldown_end` track cooldowns.

## 4. Stop it

```json
{"method": "brain_stop", "params": {}}
```

Starting twice without stopping is refused (`the brain is running: brain_stop it first`).

`dungeon_run` with a `brain` option starts the brain for the run and stops what it started; a brain you started by hand is left alone when the leg ends.
