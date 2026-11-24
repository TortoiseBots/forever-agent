---
type: Method Reference
title: Quests and loot
description: Take, hand in and abandon quests, read the quest log, and loot corpses, chests and game objects, including group rolls.
tags: [player-actions, quests, loot, gameobjects, rolls]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:00Z }
sources:
  - id: quest-methods
    resource: internal/forever/world/serve_quest_methods.go
  - id: quest-act
    resource: internal/forever/world/quest_act.go
  - id: loot-methods
    resource: internal/forever/world/serve_loot_methods.go
  - id: loot
    resource: internal/forever/world/loot.go
  - id: method-frame
    resource: internal/forever/world/serve_method_frame.go
---

This page covers talking a quest through from a quest giver, reading the quest log, and everything around loot:
corpse and object windows, taking items and money, opening chests, using game objects, finding them, and group
rolls.

Quest methods share the session's interactive state with the vendor and trainer methods (see
[vendors and gossip](vendors-and-gossip.md)): the giver must be in the world model within about 4 yards, and the
methods never walk to it.

# Quest methods

| Method | Parameters | Returns |
|---|---|---|
| `quest_hello` | `guid` | the giver's menu or its quest list |
| `quest_query` | `guid`, `quest_id` | the quest's details or its turn-in screen |
| `quest_accept` | `guid`, `quest_id` | ok, the slot the quest took |
| `quest_complete` | `guid`, `quest_id` | the reward screen, or the turn-in screen when not ready |
| `quest_reward` | `guid`, `quest_id`, `choice` | ok, the item that arrived |
| `quest_abandon` | `quest_id` | ok |
| `quest_log` | none | the character's quest log |

The quest id is required and must be positive everywhere it is named (`id %d is not a positive quest id`), and
every method that names a giver requires the quest-giver flag.

## quest_hello and quest_query

`quest_hello` is the hello the gossip method sends, with the quest-giver flag as the precondition: a gossip menu, or
the giver's quest list, or the one quest the core opens by itself.

`quest_query` asks one giver about one quest. The answer is the quest's details (`quest_details` event with
`quest_id`, `title`, `objectives`) or, for a turn-in, the request-items screen (`quest_request_items` with
`completable` true or false). A giver that answers nothing for that quest produces a `quest_error` event whose text
says it offers or takes no such quest, or that this character cannot take it.

## quest_accept

`quest_accept` takes a quest from the giver. The server's own quest log is the proof: the call answers ok only when
the quest shows in the log, and otherwise a `quest_error` - silence and a closing gossip are not proof of anything.
Before any packet the call refuses a quest already in the log, a log with no free slot, and a log that lost an update
it could not decode (log in again or change map to re-read it).

## quest_complete and quest_reward

`quest_complete` hands the quest in. Three answers are possible:

- the reward screen opened: `ok: true` and a `quest_offer_reward` event with the choice item ids and the money;
- the quest wants items the character is not carrying: `quest_request_items` with `completable: false` and the
  reason;
- the giver refused: a `quest_error`.

`quest_reward` takes the reward of a screen `quest_complete` opened. `choice` is either the screen row's index
(0-5) or the item entry of one of its choices; it is required when the screen offers choices and must be omitted
when it offers none. The call refuses a screen that is not this giver's and this quest's, an empty row, and an entry
that is on two rows. It is done only when the inventory holds more of the chosen item.

## quest_abandon

`quest_abandon` drops a quest by id. The packet names the log slot, so the call refuses a quest that is not in the
log and a quest log that is stale. The server answers nothing; the proof is the quest leaving the log. Some quests
cannot be abandoned once begun, and then the quest is still there when the wait ends and the call fails.

## quest_log

`quest_log` answers one row per occupied slot:

| Field | Meaning |
|---|---|
| `slot` | The log slot. |
| `quest_id` | The quest. |
| `state_flags` | The raw state mask. |
| `complete`, `failed` | The two bits of that mask. |
| `progress` | One counter per objective the quest's details named, or up to the last non-zero counter when they were not seen. |

`stale` and `stale_reason` mean the client lost a log update: the rows may be out of date until the next login or
map change.

# Loot methods

| Method | Parameters | Returns |
|---|---|---|
| `loot` | `guid` or `name`; none: the open window | the window |
| `loot_take` | `slot`, `all`, `money`, `wait_ms` | taken, left, skipped, money, window |
| `loot_close` | `wait_ms` | closed |
| `open_object` | `guid`, `take`, `wait_ms` | window, taken, left |
| `use_object` | `guid`, `wait_ms` | object, outcome, hint |
| `gameobjects` | `within`, `entry`, `name`, `type`, `limit` | rows |
| `roll` | `looted` + `item_slot`, `choice` | the roll |

A loot window is held by the session between calls: `loot` (or `open_object`/`use_object`) keeps the window the
server granted, `loot_take` takes from it without releasing it, and `loot_close`, a take that empties it, the next
open, a teardown or a logout give it back.

## loot

`loot` opens a corpse's window. The unit must be dead (`unit ... is alive: only a corpse has loot`). Unlike the NPC
methods, it walks to the target when it is out of loot range. A held window is released first. The window is:

| Field | Meaning |
|---|---|
| `guid` | The creature or game object. |
| `loot_guid` | The loot's own id. |
| `type` | `unit` or `game_object`. |
| `money` | Coppers in the corpse. |
| `items` | `[{slot, item_entry, count, slot_type}]`. |

`slot_type` is what this client may do with the slot: `allow loot`, `owner` (takeable), `roll ongoing`, `locked`,
`master`, or a numbered slot it does not know. An item that needs a roll cannot be taken directly.

Called with no `guid` or `name`, `loot` answers the window that is open now (`no loot window is open` when none is).

## loot_take

`loot_take` takes from the held window: `slot` (one window slot), `all` (every slot, money included) or `money`, and
`wait_ms`. `slot` and `all` cannot be combined, and one of them or `money` is required. The answer lists the slots
that were `taken`, the ones `left`, the ones `skipped` because this client cannot take them (a roll, a lock, a master
slot), and the `money` taken. A window emptied by the take is released and `window` is null.

`ok` means everything asked for happened; under `all`, slots this client cannot take are reported, not failures. A
take that the server did not confirm (a full bag, for instance) answers `ok: false` with the missed counts in the
event text.

## loot_close

`loot_close` gives the held window back and waits for the server's confirmation. With no window it answers
`ok: true, closed: false`.

## open_object

`open_object` opens a game object - a chest, a door, a lever, a quest object. It walks to it like `loot`, uses it,
and holds whatever window it produced. `take: true` takes every takeable slot and the money, then releases the
window and answers `taken` and `left`. `spell_id` is refused on this server: objects open with the use packet, not
with a spell.

A door or lever answers `ok: true` with no window and a state change event.

## use_object

`use_object` sends the use (and the report-use the client follows it with) and answers the first visible outcome:

| `outcome` | Meaning |
|---|---|
| `loot_open` | A loot window opened; it is held, and `window` says what is in it. |
| `state_changed` | The object changed state (a door opened, a lever moved). |
| `gossip` / `quest_list` | The object opened a menu or a quest list. |
| `none` | Nothing came back within `wait_ms` (default 4000 ms). |

When the outcome is `none`, `hint` says why it may have failed: the object is too far (this call never walks) or it
is locked. A summoning ritual (object type 18) answers `none` on purpose when it waits for its other users; the
result marks it with `ritual: true`.

## gameobjects

`gameobjects` lists the game objects near the character: `within` (yards; `radius` is accepted as an older name),
`entry`, `name` substring, `type`, `limit` (default 100, never negative). Rows are sorted by distance and carry
`guid`, `entry`, `name`, `type`, `type_name`, position, `distance`, `distance_2d`, `state` and `flags`.

## roll

`roll` votes in a group roll. `choice` is `need`, `greed` or `pass`; the roll is named by `looted` (the corpse or
object) and `item_slot`, or by neither when exactly one roll is open. `roll_id` is not accepted on this server:
`rolls are named by looted and item_slot`.

Refusals, before any packet: no roll open, more than one open (the message lists them), a roll that ended, one this
client already voted on, and a vote the roll does not allow. A roll started for another corpse stays open while
another window is looted.

# Examples

Accept a quest from the giver:

```
forever rpc ./slots/ACCOUNT quest_accept '{"guid": "0x0000000000000a30", "quest_id": 783}'
```

Hand it in and read the reward screen:

```
forever rpc ./slots/ACCOUNT quest_complete '{"guid": "0x0000000000000a30", "quest_id": 783}'
```

Take the first reward row:

```
forever rpc ./slots/ACCOUNT quest_reward '{"guid": "0x0000000000000a30", "quest_id": 783, "choice": 0}'
```

Loot a corpse and take everything:

```
forever rpc ./slots/ACCOUNT loot '{"name": "Kobold Vermin"}'
forever rpc ./slots/ACCOUNT loot_take '{"all": true}'
```

Open a chest and take what is in it:

```
forever rpc ./slots/ACCOUNT open_object '{"guid": "0x0000000000000b10", "take": true}'
```

Vote need on the open roll:

```
forever rpc ./slots/ACCOUNT roll '{"choice": "need"}'
```

# What you see

Windows are the objects above; `loot_take` answers the take lists plus the window as it stands. A refusal after a
packet is `ok: false` with an event whose text is the step's or the server's words (`loot_error` for loot). Errors
before a packet are JSON-RPC errors (unknown unit, alive unit, out of a reach a method checks, bad parameters).

# Common problems

- `unit 0x... is alive: only a corpse has loot`.
- `no loot window is open`; `no loot window is open (loot comes first)` for `loot_take`.
- `the window has no slot 3 (taken already?)`.
- `slot(s) [2] cannot be taken (a roll, a lock or a master slot)`.
- `no roll is open`; `2 rolls are open, name one: ...`; `this client already voted on that roll`.
- `the server did not confirm the loot release` — the window is still held; `loot_close` can try again.
