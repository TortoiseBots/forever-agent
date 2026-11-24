---
type: Method Reference
title: Trainers and flight paths
description: Read a trainer's list and buy spells, and read a flight master's nodes and take a taxi flight.
tags: [player-actions, trainers, taxi, flight-paths, npc]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:00Z }
sources:
  - id: trainer-methods
    resource: internal/forever/world/serve_trainer_methods.go
  - id: trainer-act
    resource: internal/forever/world/trainer_act.go
  - id: taxi-serve
    resource: internal/forever/world/taxi_serve.go
  - id: taxi-act
    resource: internal/forever/world/taxi_act.go
---

Two NPC services that both start from a creature within about 4 yards (the methods never walk): a trainer and a
flight master. Both share the session's interactive state with the vendor and quest methods, so a call that names a
different NPC starts that state over.

# Trainer methods

| Method | Parameters | Returns |
|---|---|---|
| `trainer_list` | `guid`, `wait_ms` | the trainer's list |
| `train` | `guid`, `spell_id`, `wait_ms` | ok, the learned spell's cost, the list again |

The NPC must carry the trainer flag, or the gossip flag (Classic opens a trainer through its "Train me." option; the
agent selects it for you). Otherwise the refusal names both bits.

## trainer_list

`trainer_list` opens the trainer and answers the list it read:

| Field | Meaning |
|---|---|
| `trainer_type` | 0 class, 1 mounts, 2 tradeskills, 3 pets. |
| `trainer_id` | The id a purchase names on the wire. |
| `greeting` | The trainer's greeting. |
| `spells` | `[{spell_id, state, cost, level, req_skill, req_skill_rank, req_ability}]`. |

`state` is what this client can tell about a row:

- `known`: the spellbook already holds it;
- `unavailable`: the character's level is too low, or a required ability is missing (not checked for pet
  trainers, whose rows name pet abilities);
- `learnable`: nothing known rules it out;
- `unknown_level`: the character's own level has not arrived in the world model yet.

`cost` is the price in copper, `level` the required level, `req_skill`/`req_skill_rank` the skill requirement when
there is one, and `req_ability` the spell ids one of which must be known first.

## train

`train` buys one spell from the trainer by `spell_id`. The list is read first when this trainer's list is not
cached, and a spell the cached list does not hold is refused before any packet. The call refuses, in the same way a
real client's window greys the row out:

- a spell the character already knows;
- a level the character has not reached;
- a required ability the spellbook does not hold (not for pet trainers);
- a price above the purse, or an inventory that is not known or has lost an update (the agent fails closed here
  rather than pay blind).

Success is a learned-spell answer that names the spell, and the fare must leave the purse in the following update; a
learned-spell answer without the charge is an error, not a purchase. The answer's event carries `cost`,
`copper_before` and `copper_after`.

# Flight-path methods

| Method | Parameters | Returns |
|---|---|---|
| `taxi` | `guid`, `node` or `nodes`, `wait_landing`, `wait_ms` | the window or the flight |

`taxi` opens the flight master's window. With no destination it only opens the window; with `node` it also takes
that flight.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `guid` | GUID | required | The flight master. |
| `node` | number | 0 | The destination taxi node. |
| `nodes` | array | - | The old shape: source node first, destination last; the last is the destination. |
| `wait_landing` | bool | false | Hold the call until the flight lands instead of returning at takeoff. |
| `wait_ms` | number | 30000 | How long to wait for the window and the takeoff. |

The answer:

| Field | Meaning |
|---|---|
| `status` | `window` (no destination), `flying`, `landed`, `refused` (the server refused the request), or `no_takeoff` (the request went out and neither a refusal nor a takeoff came within the wait; a late flight may still start). |
| `unit`, `current_node` | The flight master and the node it stands at. |
| `known`, `usable` | The nodes it can reach (known) and the ones this character may fly to (usable). |
| `node`, `reply` | The destination and the server's reply name. |
| `on_taxi` | The character's own on-a-taxi flag as the call ends. |
| `x`, `y`, `z`, `detail` | Where the flight landed, and the landing line, when it landed inside the call. |

A flight is the server's: the call returns when the character is in the air unless `wait_landing` is set, and the
session stays usable (`status` shows `on_taxi`, chat works) while the flight goes on. Movement calls are refused
while a flight is in the air or until its landing is seen. The flight's takeoff and landing are `taxi_flight`
events.

`fly` needs money for the fare and a character that is not already flying; a source node in `nodes` that is not the
flight master's own node is refused.

# Examples

Read what a trainer teaches:

```
forever-agent forever rpc ./slots/ACCOUNT trainer_list '{"guid": "0x0000000000000d10"}'
```

Buy one spell:

```
forever-agent forever rpc ./slots/ACCOUNT train '{"guid": "0x0000000000000d10", "spell_id": 19740}'
```

Open a flight master's window:

```
forever-agent forever rpc ./slots/ACCOUNT taxi '{"guid": "0x0000000000000d20"}'
```

Fly to node 4 and wait for the landing:

```
forever-agent forever rpc ./slots/ACCOUNT taxi '{"guid": "0x0000000000000d20", "node": 4, "wait_landing": true}'
```

From a script:

```
forever-agent forever act --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem \
  --account ACCOUNT --character Mychar \
  --step 'npc entry=1234' --step 'trainer' --step 'learn spell=19740'
```

# What you see

`trainer_list` answers `{since, ok, event, trainer, lines}`; `train` the same with the list re-read after the
purchase. `taxi` answers the object above and never fails on a refusal: a refused flight is `ok: false` with
`status: "refused"` and the reply name. Errors are JSON-RPC errors (no such unit, out of reach, wrong NPC flags, bad
parameters, already flying).

# Common problems

- `entry 1234 is not a trainer (NpcFlags 0x... has neither the trainer bit nor a gossip menu)`.
- `the menu has no trainer option (3 option(s))`.
- `the trainer's list has no spell 19740`.
- `the character already knows spell 19740`; `the character is level 4 and the trainer wants level 6 for spell 19740`.
- `the list price is 100 copper and the character has 40`.
- `entry 1234 is not a flight master (NpcFlags 0x... has no flight-master bit)`.
- `fly: no taxi window (a taxi-nodes step comes first)`; `fly: the taxi window is another flight master's`.
- `status: "no_takeoff"` — the request is out but no flight started within the wait; check `status.on_taxi` before
  retrying.
