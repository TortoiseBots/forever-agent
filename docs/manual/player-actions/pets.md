---
type: Method Reference
title: Pets
description: "Read the character's pet and the state of its control bar, and give it orders: attack, follow, stay, react mode, cast and autocast."
tags: [player-actions, pet, hunter, warlock]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T20:35:00Z }
sources:
  - id: pet-methods
    resource: internal/forever/world/serve_pet_methods.go
  - id: pet
    resource: internal/forever/world/pet.go
  - id: pet-packets
    resource: internal/forever/pet/packets.go
---

A hunter's or warlock's pet is controlled through two methods: `pet` reads its state (including the control bar
the server sent), and `pet_action` gives one order and answers the state afterwards.

The pet is the one this character owns. The session keeps the bar as the server last sent it, so an order that
depends on the bar (`cast`, `autocast_on`, `autocast_off`) is refused until the bar has arrived.

# The methods

| Method | Parameters | Returns |
|---|---|---|
| `pet` | none | the pet's state |
| `pet_action` | `action`, `target`, `slot`, `spell_id`, `name` | the pet's state after the order |

# pet

| Field | Meaning |
|---|---|
| `present` | The character has a pet. Without one only `present: false` and `bar: false` are set. |
| `guid`, `entry`, `name`, `level` | The pet's identity. |
| `dead`, `health`, `max_health`, `power_type`, `power`, `max_power` | Its state. |
| `in_combat`, `target`, `distance`, `moving` | What it is doing. |
| `bar` | The session holds the pet's control bar (it names this pet). |
| `react` | `passive`, `defensive`, `aggressive` or `assist`. |
| `command` | `stay`, `follow`, `attack`, `abandon` or `move_to`. |
| `enabled` | False while the character is mounted (the server drops pet orders then). |
| `spells` | The bar's spells: `[{spell_id, name, slot, autocast, passive}]`. |
| `refused` | The reason of the pet's last refused cast. |

A spell's `slot` is its position on the bar (3-6); a spell the pet knows but that is not on the bar has `slot: -1`.

# pet_action

`action` is one of:

| Action | Needs | Meaning |
|---|---|---|
| `attack` | `target` (or a selected target) | Attack the unit. |
| `follow` | - | Follow the character. |
| `stay` | - | Stay where it is. |
| `passive`, `defensive`, `aggressive` | - | Set the react mode. |
| `cast` | `slot`, `spell_id` or `name` | Cast a bar spell. `target` is optional: the selected target otherwise. |
| `autocast_on`, `autocast_off` | `slot`, `spell_id` or `name` | Turn a bar spell's autocast on or off. |

A spell is named by `slot` (its bar position), by `spell_id`, or by `name` (the highest rank when several share the
name). The spell must be a castable button of the bar: a passive spell is refused, and the refusal lists the bar's
spells when the name matches none.

Refused before any packet: no pet (`pet_action: the character has no pet`), a dead pet, a mounted character (the
server drops pet orders while mounted), a bar that has not arrived, and an unknown action.

# Examples

Read the pet:

```
forever-agent forever rpc ./slots/ACCOUNT pet '{}'
```

Send the pet at the selected target:

```
forever-agent forever rpc ./slots/ACCOUNT pet_action '{"action": "attack"}'
```

Set it defensive and make it follow:

```
forever-agent forever rpc ./slots/ACCOUNT pet_action '{"action": "defensive"}'
forever-agent forever rpc ./slots/ACCOUNT pet_action '{"action": "follow"}'
```

Cast a bar spell by name at the selected target:

```
forever-agent forever rpc ./slots/ACCOUNT pet_action '{"action": "cast", "name": "Growl"}'
```

Turn autocast on for the spell in bar slot 4:

```
forever-agent forever rpc ./slots/ACCOUNT pet_action '{"action": "autocast_on", "slot": 4}'
```

# What you see

Both methods answer the pet object above; `pet_action` answers the state after the order. A refused order is a
JSON-RPC error (raised before any packet); the server's own refusal of a sent order shows later as the spell's
`refused` reason or in the combat log.

# Common problems

- `pet_action: the character has no pet`.
- `pet_action: the pet is dead`.
- `pet_action: the server drops pet orders while mounted`.
- `pet_action: the pet's bar has not arrived`.
- `pet_action: unknown action "sleep" (attack, follow, stay, passive, defensive, aggressive, cast, autocast_on,
  autocast_off)`.
- `pet_action attack: no target (give target or select one)`.
- `pet_action cast: Growl (2649) is not a castable button of the pet's bar`.
