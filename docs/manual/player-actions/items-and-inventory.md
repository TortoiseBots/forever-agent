---
type: Method Reference
title: Items and inventory
description: Read the character's inventory and equipment, move items between slots and bags, use an item, and mount or dismount.
tags: [player-actions, items, inventory, equipment, mounts]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:00Z }
sources:
  - id: inventory
    resource: internal/forever/world/inventory.go
  - id: item-methods
    resource: internal/forever/world/serve_item_methods.go
  - id: inventory-act
    resource: internal/forever/world/inventory_act.go
  - id: mount
    resource: internal/forever/world/mount.go
  - id: mount-item
    resource: internal/forever/world/mount_item.go
  - id: items
    resource: internal/forever/items/inventory.go
---

These methods read and change what the character carries: the inventory snapshot, the worn equipment, moving an
item between slots, using an item, and mounting. They work on the world model the session keeps from the items the
server sent, so a call never wakes the server for a read.

Slot numbers are the client's own: equipment 0-18, the four bag slots 30-33, the backpack 35-62 (a character
really has 35-50), the bank 63+ and the buyback 72-83. A position is either a bare backpack slot (`45`) or
`container:slot` for an item inside a bag (`30:3` means the fourth slot of the bag in bag slot 30).

# The methods

| Method | Parameters | Returns |
|---|---|---|
| `inv` | none | the full inventory snapshot (read-only) |
| `inventory` | `bags`, `names` | array of item rows |
| `equipment` | `names` | array of worn items |
| `equip` | `item_entry` or `bag` + `slot` | the item's row after the move |
| `use_item` | `item_entry` or `bag` + `slot`, `spell_id`, `target` | the item's row |
| `mount` | `spell` or `entry` (+ `spell`) | mount state |
| `dismount` | none | mount state |

# inv

`inv` is the raw snapshot: `known`, `coinage`, `items`, `quest_log`, `free_slot`, `stale`, `stale_reason`. Each item
row is `guid`, `container`, `slot`, `entry`, `count`, `durability`, `max_durability`, `placed`, `unconfirmed`.
`free_slot` is the first empty backpack slot (0 when the backpack is full or not known yet).

`stale` means the client lost an inventory change it could not decode: positions may be wrong until the next login
or map change, and `stale_reason` says what was lost. Destructive steps refuse to run on a stale inventory rather
than act on a guessed position.

`placed: false` marks an item that arrived after the login snapshot (a purchase, a loot): the item's own block names
no slot, so its position is unknown until the push result that follows the gain, or the next login. `unconfirmed`
marks an item a move just touched whose new position the server has not confirmed yet; the position shown is the
client's guess, and after about two seconds without confirmation the guess expires and the position goes unknown.

# inventory

`inventory` answers the placed backpack rows in slot order, then the bag contents (bag, index) when `bags` is true,
then the items with no known position. `names: true` also fills `name` and `quality` from the item database (one
batch query; a failed lookup leaves the names off rather than failing the call).

| Row field | Meaning |
|---|---|
| `bag` | The container: 255 for the player's own slots, 30-33 for a bag. |
| `slot` | The slot inside that container (255 when unknown). |
| `guid` | The item's GUID. |
| `item_entry` | The item id. |
| `count` | Stack count. |
| `durability`, `max_durability` | Durability, when the item has any. |
| `placed`, `unconfirmed` | See `inv` above. |
| `name`, `quality` | Only with `names: true`. |

The call is refused with `inventory: the character's own create has not arrived yet` until the session is in the
world.

# equipment

`equipment` lists the occupied equipment slots, lowest slot first:

| Slot | Name | Slot | Name |
|---|---|---|---|
| 0 | head | 10 | finger1 |
| 1 | neck | 11 | finger2 |
| 2 | shoulders | 12 | trinket1 |
| 3 | body | 13 | trinket2 |
| 4 | chest | 14 | back |
| 5 | waist | 15 | main_hand |
| 6 | legs | 16 | off_hand |
| 7 | feet | 17 | ranged |
| 8 | wrists | 18 | tabard |
| 9 | hands | | |

Each row is `slot`, `slot_name`, `guid`, `item_entry`, and with `names: true` also `name` and `quality`.

# equip

`equip` moves one item to a worn slot. The item is named by `item_entry` (the first stack whose position the server
confirmed) or by `bag` (default 255, the player's own slots) with `slot`. The step uses the server's auto-equip, so
the server chooses the slot: the answer is the item's row after the move and its slot is usually still unknown.

Two limits from the protocol are worth knowing: equipping a two-handed weapon can send the off-hand item to a free
slot the client cannot name, and an equip is never confirmed by this server's answer in the current version, so the
item and any displaced item read `unconfirmed` for a moment and then unknown. Read the equipment again after a
moment to see the result.

# use_item

`use_item` uses an item's on-use effect. The on-use spell must be given: `spell_id` is required, because the item's
effect row is not a record the client can look up. `target` is an optional unit GUID for a targeted use (a hearth
stone needs none); a unit target must be in the world model.

The call fails with the cast failure's name when the server refuses the use, and answers the item's row otherwise.

# mount and dismount

`mount` mounts the character. Give `spell` (a mount spell the character knows) or `entry` plus `spell` (a mount item
and its on-use spell). A mount item is a learn item: the use teaches the mount spell and the agent then casts it, so
the result's `learned_spell` names what the item taught. `attempts` is 1 when one use or cast was spent, 0 when the
character was already mounted.

`dismount` cancels the mount aura and answers once the mount display is gone. A character already on foot is
answered without sending anything.

Both are refused on a taxi flight (the display belongs to the flight) and `mount entry=N` alone is refused: the
item's own spell is not guessed.

# Examples

Read the money and every item:

```
forever rpc ./slots/ACCOUNT inv '{}'
```

Backpack rows with names, bag contents included:

```
forever rpc ./slots/ACCOUNT inventory '{"bags": true, "names": true}'
```

Equip the item in the first backpack slot:

```
forever rpc ./slots/ACCOUNT equip '{"slot": 35}'
```

Use a potion whose on-use spell is 4390 (the item is in backpack slot 40):

```
forever rpc ./slots/ACCOUNT use_item '{"slot": 40, "spell_id": 4390}'
```

Mount with a spell the character knows:

```
forever rpc ./slots/ACCOUNT mount '{"spell": 458}'
```

Dismount:

```
forever rpc ./slots/ACCOUNT dismount '{}'
```

# What you see

`inv` answers the snapshot object, `inventory` and `equipment` arrays, and `equip`/`use_item` one item row. `mount`
answers `{mounted, display_id, spell, entry, attempts, learned_spell}`; `dismount` the same shape with only the
mount state filled. Errors are JSON-RPC errors; a server refusal arrives as the message the server gave.

# Common problems

- `equip needs item_entry=<item id> or bag=<container> with slot=<slot>` — the item selector was empty.
- `bag 34 is neither 255 nor a bag slot 30-33` — the container byte is outside the four bag slots.
- `entry 1234 is in 2 stacks; name the one to act on with slot=<position>` — an entry alone is ambiguous.
- `use_item needs spell_id on this server (the item's on-use spell)`.
- `mount: an item mount needs spell= as well (the item's on-use spell)` — see `mount` above.
- `mount: spell N is not one this character knows` — the spell must be in the character's spellbook.
- `entry N is already equipped at slot S` — an item on the character is not equipped again; unequip it first
  (mail, auction and trade also refuse worn items and worn bags).
