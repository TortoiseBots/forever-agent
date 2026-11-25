---
type: Concept
title: Loot and gossip
description: Who loots what in each mode, and who talks to dungeon NPCs.
tags: [turtle, dungeon, loot, gossip, concept]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-05T20:50:00Z }
sources:
  - id: remote-objects
    resource: internal/dungeon/remote_objects.go
  - id: lootuse
    resource: internal/dungeon/lootuse.go
  - id: remote-gossip
    resource: internal/dungeon/remote_gossip.go
  - id: commander-hands
    resource: cmd/forever-agent/commander_hands.go
---

This page describes the Turtle 1.12 stack. These commands refuse a ForeverCore server.

# Loot and gossip

Dungeon plans mix fighting with objects: chests to open, boss corpses to loot, items to use, levers to pull, NPCs to talk to. Who does that depends on the mode.

## Commander mode: the carrier bot

The carrier (the `carrier` option, else the first living bot that is not lead, tank, or healer) does every object step on `cmd` orders. Before a batch of opens the run switches the group to free-for-all loot (`cmd lootmethod ffa`) and restores it after (`loot_restore`: `group` by default, or `rr`, `master`, `nbg`), even on failure. Boss loot goes to the carrier by item (`cmd loot <corpse> <carrier> <item>`); rituals and object states go by their own verbs. Orders that run long answer `ACTION_PENDING` first and the run waits for the final line.

When the carrier's order fails twice (`fallback_tries`), your own character steps in by default (`object_fallback: "agent"`): it opens the object itself and trades the item to the carrier. `"off"` fails the step instead. Every run starts by sending `.bot behavior <bot> loot on` to the whole party, undoing a `loot off` a crashed run left in place. Batched grave openings (`graves_batch` 1–20, or the `graves_ramp` start/step/max ramp) open back to back before the party fights what came out; `graves_open_by` picks who opens (`carrier` by default, or `agent` with a GM account).

## Leader mode: your own hands

Your character loots corpses and objects with the ordinary session calls. Boss loot under round-robin, group, or need-before-greed hides sub-threshold items from members whose turn it is not, so the run briefly flips the group to free-for-all for your character's loot and restores it after (you must lead the group).

## Gossip: who talks

A plan's gossip step names the NPC, a point near it, and either an option number or matching text, plus who talks (`by`: `agent` by default, or `carrier`/`tank` in commander runs). By bot: the party walks to the NPC and the bot gets `cmd gossip <npc> <bot> <option>`; when the module has no gossip verb, your character talks instead and the report notes the fallback. By agent (always, in leader mode): your character walks up, says hello, picks the option, and waits for the close. The option match is by number first, else case-insensitive substring; a text matching two options fails the step rather than guessing.

## Entry targets and view

A remote run with object steps probes once with `cmd state entry:<id>@x,y,z`: any answer proving the module knows the verb (`ack`, `not-found`, `too-far`) turns entry targets on (`remote.entry_targets: "on"`, with `state_polls` counted). Object steps then name the object by entry within 30 yd of the point; the agent's own GUID is the retry after `not-found`. A game-object wait polls `cmd state` every 2 s instead of needing the object in view. When every object step has an entry, the agent is not kept near the party (no `.appear`: `run_gm_assists` stays 0). An older module (`invalid`) keeps GUIDs and keep-near. A `not-found` names entry and point; a `too-far` walks the party once, then retries.
