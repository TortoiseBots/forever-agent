---
type: Method Reference
title: Quests and loot
description: Take, share, hand in and abandon quests, answer shared quests, read the quest log, loot corpses, chests and game objects including group rolls, and run the quest-share, loot and object checks live.
tags: [player-actions, quests, loot, gameobjects, rolls]
status: stable
generated: { by: forever-agent docs, at: 2026-11-28T08:25:00Z }
sources:
  - id: quest-methods
    resource: internal/forever/world/serve_quest_methods.go
  - id: quest-share
    resource: internal/forever/world/quest_share.go
  - id: quest-act
    resource: internal/forever/world/quest_act.go
  - id: loot-methods
    resource: internal/forever/world/serve_loot_methods.go
  - id: loot
    resource: internal/forever/world/loot.go
  - id: method-frame
    resource: internal/forever/world/serve_method_frame.go
  - id: loot-check
    resource: cmd/forever-agent/forever_loot.go
  - id: quest-share-cmd
    resource: cmd/forever-agent/forever_quest.go
---

> **Not yet proven on a live server.** The quest, loot and game-object methods, their `act` steps and the live checks `loot-check` and `object-use` are exercised against the scripted world; a live run is the next step for each. See [what has been proven](../getting-started/what-is-forever-agent.md#what-has-been-proven-and-what-has-not).

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
| `quest_share` | `quest_id`, `wait_ms` | ok, per-member push results |
| `quest_share_accept` | `quest_id`, `wait_ms` | ok, the slot the shared quest took |
| `quest_share_decline` | `quest_id` | ok |

The quest id is required and must be positive everywhere it is named (`quest_id %d is not a positive quest id`), and
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

## quest_share

`quest_share` shares a quest in the character's quest log with its party or raid (`CMSG_PUSH_QUEST_TO_PARTY` by
`quest_id`). Preconditions before sending: a positive `quest_id`, a non-stale known quest log holding that quest, and
group membership (`not in a group` when solo).

It waits up to `wait_ms` (default 2,000 ms with other connected group members, 500 ms otherwise; capped at 60,000 ms)
for the server's `SMSG_QUEST_PUSH_RESULT` replies and returns `results`: one `{guid, name, result, reason}` row per
member (or a single self-result such as `not_allowed`, `not_daily` or `not_in_party`). `ok` is `true` when at least one
member answered `success` or `accepted` without a self-refusal. Each `SMSG_QUEST_PUSH_RESULT` (including later
`accepted` or `declined` answers) also writes a `quest_push_result` row to `event_log`.

## quest_share_accept and quest_share_decline

When another group member shares a quest (`SMSG_QUEST_GIVER_QUEST_DETAILS` carrying a non-zero `InformUnit`) or a
shared-on-accept quest opens a confirmation popup (`SMSG_QUEST_CONFIRM_ACCEPT`), the session records the pending offer
and emits a `quest_shared` event (`data.share_kind` is `push` or `confirm`).

- `quest_share_accept` takes the pending shared quest: it sends `CMSG_QUEST_GIVER_ACCEPT_QUEST` naming the sharing
  player (`InformUnit`) for a `push` offer, or `CMSG_QUEST_CONFIRM_ACCEPT` for a `confirm` offer, and waits for the
  quest to appear in the character's log (`quest_accepted`, `ok: true` with `slot`), or a `quest_error`. Passing
  `quest_id` verifies that the pending offer matches that id before sending.
- `quest_share_decline` declines the pending shared quest: it sends `CMSG_QUEST_PUSH_RESULT` with reason `4`
  (`declined`) naming the sharing player for a `push` offer, or dismisses the pending `confirm` offer, and emits
  `quest_share_declined`.

Both refuse before sending when no shared quest is pending (`no pending shared quest`) or when `quest_id` does not match
the pending offer (`pending shared quest is %d, not %d`); `quest_share_accept` also checks the quest log preconditions
(`already in slot`, `no free slot`, stale log).

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

`roll` votes in a group roll. `choice` is `need`, `greed` or `pass`, and `vote` is its alias (the examples in the
live notes spell it `vote`); give one, or both with the same word, and two different words are refused rather than
picked between. The roll is named by `looted` (the corpse or
object) and `item_slot`, or by neither when exactly one roll is open. `roll_id` is not accepted on this server:
`rolls are named by looted and item_slot`.

Refusals, before any packet: no roll open, more than one open (the message lists them), a roll that ended, one this
client already voted on, and a vote the roll does not allow. A roll started for another corpse stays open while
another window is looted.

# Live checks: forever loot-check and forever object-use

Two one-shot live checks, no daemon involved. `loot-check` settles, finds the nearest lootable thing (a dead creature
of `--entry`, or a chest), kills it first when it is still alive, opens the window, votes on a group roll when one was
asked for, takes the money and the items it may take, and releases the window. `object-use` finds the nearest game
object of `--entry` and reports its state before and after the use.

```bash
forever-agent forever loot-check [--rest host:port] [--rpc host:port] [--ca FILE] --account EMAIL \
    --character NAME [--entry N] [--roll need|greed|pass] [--take N] [--money=false] \
    [--at x,y,z] [--at-range 2] [--range 30] [--stay 8s] [--fight-for 60s]

forever-agent forever object-use [--rest host:port] [--rpc host:port] [--ca FILE] --account EMAIL \
    --character NAME --entry N [--at x,y,z] [--at-range 2] [--range 40] [--stay 8s]
```

| Flag | Default | Meaning |
|---|---|---|
| `--entry` | `0` (`loot-check`: the nearest dead creature or chest) / required (`object-use`: a game-object entry) | The creature or game-object entry to pick. |
| `--roll` | none | Vote on a group roll: `need`, `greed` or `pass`. `need` is never sent unless you ask for it. |
| `--take` | `0` | How many items to take from the window; `0` takes every item the window allows, at most 18. |
| `--money` | on | Take the window's money. |
| `--at` | none | `x,y,z` of the chest or object when several of that entry are in range; `--at-range` (default 2 yd, 0.5-100) sets how close it must be. |
| `--range` | `30` (`loot-check`) / `40` (`object-use`) | How far to look for a target, 1-200 yards. |
| `--stay` | `8s` | How long to settle in the world before choosing the target, 0-10m. |
| `--fight-for` | `60s` | `loot-check` only: how long a target that is still alive is fought before giving up, above 0 up to 5m. |

Both print the object's state (`open`, `lootable`, ...; `object-use`) or the loot window with one line per item slot and
per roll, then the packet counts. Neither prints an identifier; a roll's winner is a role (`you`, `another player`,
`nobody`).

Exit codes: `0` done; `2` a usage error; `5` a world refusal or a failed run after login; `6` the link was lost;
`11` the character died; `12` several objects of that entry are in range and `--at` named none — nothing was sent.

# Share a quest live: forever quest-share

One-shot live command to share a quest with the party or answer a quest shared to the character, with no daemon:

```bash
forever-agent forever quest-share [--rest host:port] [--rpc host:port] [--ca FILE] --account EMAIL \
    --character NAME (--quest N | --accept | --decline) [--from NAME] [--wait DURATION] \
    [--settle 4s] [--stay 2s] [--capture-dir DIR]
```

| Flag | Default | Meaning |
|---|---|---|
| `--quest N` | none | Quest id to share with the party, or expected quest id when combined with `--accept` or `--decline`. |
| `--accept` | off | Wait up to `--wait` for a shared quest (or confirm-accept popup) and accept it. |
| `--decline` | off | Wait up to `--wait` for a shared quest and decline it (exclusive with `--accept`). |
| `--from NAME` | none | With `--accept` or `--decline`: answer only a share from this character name. |
| `--wait DURATION` | `5s` (`--quest`) / `30s` (`--accept`/`--decline`) | How long to wait for push results or an incoming share, 0-2m. |
| `--settle DURATION` | `4s` | How long to settle in the world before acting, 0-1m. |
| `--stay DURATION` | `2s` | How long to stay in the world after acting before logging out, 0-2m. |
| `--capture-dir DIR` | none | Write the packets the handlers see into a new run directory under this path. |

Exit codes: `0` done; `2` a usage error; `5` a world refusal or timeout after login; `6` the link was lost.

# Examples

Accept a quest from the giver:

```
forever-agent forever rpc ./slots/ACCOUNT quest_accept '{"guid": "0x0000000000000a30", "quest_id": 783}'
```

Hand it in and read the reward screen:

```
forever-agent forever rpc ./slots/ACCOUNT quest_complete '{"guid": "0x0000000000000a30", "quest_id": 783}'
```

Take the first reward row:

```
forever-agent forever rpc ./slots/ACCOUNT quest_reward '{"guid": "0x0000000000000a30", "quest_id": 783, "choice": 0}'
```

Share a quest with the party, or accept a shared quest on another session:

```
forever-agent forever rpc ./slots/ACCOUNT quest_share '{"quest_id": 783}'
forever-agent forever rpc ./slots/OTHER quest_share_accept '{}'
```

Or run either side as a one-shot live check:

```
forever-agent forever quest-share --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem \
    --account ACCOUNT --character Mychar --quest 783
```

Loot a corpse and take everything:

```
forever-agent forever rpc ./slots/ACCOUNT loot '{"name": "Kobold Vermin"}'
forever-agent forever rpc ./slots/ACCOUNT loot_take '{"all": true}'
```

Open a chest and take what is in it:

```
forever-agent forever rpc ./slots/ACCOUNT open_object '{"guid": "0x0000000000000b10", "take": true}'
```

Vote need on the open roll:

```
forever-agent forever rpc ./slots/ACCOUNT roll '{"choice": "need"}'
```

# What you see

Windows are the objects above; `loot_take` answers the take lists plus the window as it stands. A refusal after a
packet is `ok: false` with an event whose text is the step's or the server's words (`loot_error` for loot). Errors
before a packet are JSON-RPC errors (unknown unit, alive unit, out of a reach a method checks, bad parameters).

# Common problems

- `quest_share: not in a group`; `quest 783 is not in the log (quest_log shows what is)`.
- `no pending shared quest`; `pending shared quest is 783, not 900`.
- `unit 0x... is alive: only a corpse has loot`.
- `no loot window is open`; `no loot window is open (loot comes first)` for `loot_take`.
- `the window has no slot 3 (taken already?)`.
- `slot(s) [2] cannot be taken (a roll, a lock or a master slot)`.
- `no roll is open`; `2 rolls are open, name one: ...`; `this client already voted on that roll`.
- `the server did not confirm the loot release` — the window is still held; `loot_close` can try again.
