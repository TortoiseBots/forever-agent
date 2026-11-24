---
type: Method Reference
title: Vendors and gossip
description: Open a vendor, buy and sell items, and read and choose the options of an NPC's gossip menu.
tags: [player-actions, vendors, gossip, npc]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:00Z }
sources:
  - id: npc-methods
    resource: internal/forever/world/serve_npc_methods.go
  - id: vendor-act
    resource: internal/forever/world/vendor_act.go
  - id: method-frame
    resource: internal/forever/world/serve_method_frame.go
  - id: act
    resource: internal/forever/world/act.go
---

These methods talk to a creature: open a vendor and its list, buy and sell, and open a gossip menu and choose an
option. They share the session's interactive state with each other and with the quest and trainer methods, which
has two consequences worth reading once:

- The methods never walk. The creature must be in the world model and within about 4 yards; otherwise the call is
  refused with `the NPC is N yd away (walk_to it first)`.
- The vendor list, the open menu and the quest list belong to the NPC they were read from. A call that names a
  different unit starts that state over. So `vendor_list` then `buy` on the same vendor uses the list the first call
  read; `gossip_hello` then `gossip_select` uses the menu the hello opened.

Most calls take `wait_ms` (`0` is the default 15000 ms, the maximum is 60000). A precondition that fails before a
packet goes out is an error; once a packet went out a refusal is a normal answer with `ok: false` and an event that
says why.

# The methods

| Method | Parameters | Returns |
|---|---|---|
| `vendor_list` | `guid` | the vendor's rows |
| `buy` | `guid`, `item_entry` or `muid`, `count` | ok, event, the item row when it arrived |
| `sell` | `guid`, `item_entry` or `bag`+`slot`, `count`, `force` | ok, event, the item row as it stood before |
| `gossip_hello` | `guid` | ok, event, the open menu (options and quests) |
| `gossip_select` | `guid`, `option` | ok, event, the menu now open |

The shared answer object is `{since, ok, event, item, items, menu, lines}`: `since` is the event-log cursor before
the call's first packet, `lines` the act-style lines the call printed (menu options, vendor rows, refusals), and
`event` the event-log row that answered the call.

# vendor_list

Opens the vendor the way the client does - through its gossip option when the NPC has a gossip menu, otherwise by
asking for the list directly - and answers the rows the server sent:

| Row field | Meaning |
|---|---|
| `muid` | The row's id on this vendor's list; what a purchase names when an item is on several rows. |
| `item_entry` | The item id. |
| `price` | The price for one `stack_count` of the item, in copper. |
| `stack_count` | How many the price buys. |
| `stock` | How many are left; `-1` means unlimited. |

The creature must carry a vendor flag, or the call is refused:
`entry 1234 is not a vendor (NpcFlags 0x... has no vendor bit)`.

# buy

`buy` buys from the open vendor and waits for the server's answer.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `guid` | GUID | required | The vendor. |
| `item_entry` | number | - | Buy this item (only when exactly one row sells it). |
| `muid` | number | - | Or name the row by id. Exactly one of `item_entry` and `muid` is required. |
| `count` | number | 1 | How many items, 1-255. The price is the row's price for `count` items rounded up by stack. |

Purchases always go to the backpack: `bag` and `slot` are refused (`buy places the item in the backpack on this
server (bag and slot are not accepted)`). The list is read first when this vendor's list is not cached, and the
success answer only counts when the inventory then holds more of the item. A refusal is `ok: false` with a
`vendor_buy_failed` event carrying the server's reason (no money, full bags, sold out).

# sell

`sell` sells a stack, or part of one.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `guid` | GUID | required | The vendor. |
| `item_entry` | number | - | The item (only when it is in one stack). |
| `bag`, `slot` | numbers | bag 255 | Or the position: container (255 for the backpack, 30-33 for a bag) and slot. |
| `count` | number | 0 | How many to sell; 0 sells the whole stack. |
| `force` | bool | false | Sell an item a known quest needs. |

Worn items and the bags themselves are never sold (a sale would take them off the character), and an item that is
an objective of a quest in the log is refused unless `force: true`. A sale is silent when it works: the proof is
the stack leaving the carried inventory into a buyback slot (72-83).

# gossip_hello and gossip_select

`gossip_hello` talks to the NPC and answers its menu or its quest list. The NPC needs the gossip flag or the
quest-giver flag; otherwise the refusal names what it lacks. The answer's `menu` is
`{menu_id, options: [{id, text}], quests: [{quest_id, title}]}`. A quest-only NPC answers quests and no options; a
quest giver with exactly one quest and no gossip option opens that quest directly (the core does that itself), and
the call's event is then the quest's details or its turn-in screen.

`gossip_select` chooses one option (`option` is the option's `id` from the menu) and answers the menu that is open
afterwards - the same menu when the option opened a submenu, none (`ok: true`, no `menu`) when it opened nothing,
which is normal. A selection without a hello is refused (`no open menu for this NPC (gossip_hello comes first)`), as
is an option the menu does not have. Options that carry a text code (the "enter code" kind) are not ported:
`coded gossip options are not ported`.

# Examples

Open a vendor and read its list (the GUID is the creature's, from `units`):

```
forever rpc ./slots/ACCOUNT vendor_list '{"guid": "0x0000000000000a2c"}'
```

Buy five of the row with muid 12:

```
forever rpc ./slots/ACCOUNT buy '{"guid": "0x0000000000000a2c", "muid": 12, "count": 5}'
```

Sell the stack in the first backpack slot (35):

```
forever rpc ./slots/ACCOUNT sell '{"guid": "0x0000000000000a2c", "slot": 35}'
```

Talk to a quest giver and read its menu:

```
forever rpc ./slots/ACCOUNT gossip_hello '{"guid": "0x0000000000000a30"}'
```

Choose option 1 of that menu:

```
forever rpc ./slots/ACCOUNT gossip_select '{"guid": "0x0000000000000a30", "option": 1}'
```

From a script, the same flow is three act steps; see [act steps](act-steps.md):

```
forever act --account ACCOUNT --character Mychar --step 'npc entry=1234' --step 'vendor' --step 'buy entry=2589 n=5'
```

# What you see

A successful call answers `ok: true` with the rows it read. A refusal after a packet answers `ok: false`, an event
with the server's or the step's words, and the `lines` that led there. Errors (before any packet) are JSON-RPC
errors with the messages quoted above.

# Common problems

- `the NPC is 12.3 yd away (walk_to it first)` — the methods never walk; use `walk_to` or the act `goto`/`npc`
  steps.
- `buy needs exactly one of item_entry or muid`; `the vendor sells no item N`; `entry N is on 2 rows; name the one
  to buy with muid=<row>`.
- `the open vendor list is not this NPC's (a vendor step on entry N comes first)` — the cached list belongs to
  another NPC.
- `gossip_select needs option`; `the menu has no option N`.
- `entry N has neither a gossip menu nor quests (NpcFlags 0x...)` — `gossip_hello` on a creature with neither flag.
