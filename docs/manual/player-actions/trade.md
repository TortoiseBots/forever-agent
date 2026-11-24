---
type: Method Reference
title: Trade
description: Trade items and gold with another player, in one call or step by step through the trade window.
tags: [player-actions, trade, players]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:00Z }
sources:
  - id: trade-methods
    resource: internal/forever/world/serve_trade_methods.go
  - id: trade-flow
    resource: internal/forever/world/trade_flow.go
  - id: trade-cast
    resource: internal/forever/world/trade_cast.go
---

There are two ways to trade with another player:

- `trade` does the whole exchange in one call: open the window, put the items and gold in, accept, and wait for the
  other side's accept.
- `trade_open`, `trade_expect`, `trade_set_item`, `trade_set_gold`, `trade_accept`, `trade_status` and
  `trade_cancel` do it step by step, which is what a script needs when the other side is a bot it is also driving.

The other player is named by `to` (name or GUID) on the calls that start a trade. Both players must be standing
within about 10 yards when the trade opens and again when it completes.

# The one-call trade

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `to` | string | required | The other player: a name in view, or a GUID. |
| `items` | array | empty | `[{"item": <entry>, "count": <n>}]`; `count` 0 means one. |
| `gold` | number | 0 | Copper to hand over; it may not be more than the purse holds. |
| `wait_ms` | number | 15000 | How long to wait for the trade to complete. |
| `open_wait_ms` | number | 8000 | How long to wait for the window to open. |

The call plans everything before it changes anything: if the bags cannot cover the request (not enough of an item,
more than six stacks needed, more gold than the purse holds) it is refused without a packet. Stacks are taken
largest first and split when that gives the exact count. It then opens the trade, fills the window, accepts and waits
for the other side. A trade that does not complete is cancelled first and answered with `ok: false`.

| Result field | Meaning |
|---|---|
| `result` | `completed`, or why not: `cancelled`, `busy`, `too_far`, `failed`, `state_changed`, `changed`, `ignored`, `not_on_taplist`, `timeout`, and the two players' states. |
| `status` | The server's own last status name, when there was one. |
| `partner`, `partner_name` | The other player. |
| `gave` | `[{item, count, trade_slot, split}]`: what went into the window from this side. |
| `gold` | Copper put in from this side. |
| `received`, `received_gold` | What the other side put in (from the last window update). |
| `partner_accepted` | Whether the other side had accepted when the trade ended. |
| `verified` | True when the inventory readback confirms the exchange (items and money landed). |
| `detail` | A sentence on a failure, e.g. why the server cancelled it. |
| `trade` | The window as it stood, for a trade that did not complete. |

# Step-by-step trade

| Method | Parameters | Meaning |
|---|---|---|
| `trade_open` | `to`, `open_wait_ms` | Offer a trade and answer once the window is open. The window stays open. |
| `trade_expect` | `from`, `wait_ms` | Arm an accept for one incoming proposal and answer once the window is open. Every other proposal is declined. Nothing is sent until the proposal comes. |
| `trade_set_item` | `trade_slot`, `item_entry` or `bag`+`slot`, `clear` | Put an item of the bags in a trade slot (0-6), or empty the slot with `clear: true`. |
| `trade_set_gold` | `gold` | Put copper in the window. |
| `trade_accept` | `wait_ms` | Accept and wait for the other side. |
| `trade_cancel` | none | Close the window; safe to call twice. |
| `trade_status` | none | Read the window. |

Slot 6 is the non-traded slot: it is where an enchant is cast at the other player's item, and it is not exchanged.
`trade_set_item` answers `unchanged: true` when the slot already held that item, without sending anything.

`trade_accept` does not accept again for you. If the window changes after the accept (the server un-accepts both
sides), it answers `result: "changed"` with the window in `trade`, so the caller decides what to do. If the other
side does not accept within `wait_ms`, the accept is withdrawn and the window stays open: `result: "timeout"`. A
refusal by the server is `result` from the server's status with a `detail`.

`trade_status` answers:

| Field | Meaning |
|---|---|
| `active`, `open` | A trade is in progress and its window is open. |
| `partner`, `partner_name`, `status` | The other player and the window's status name. |
| `their_items`, `their_gold`, `their_spell` | The other side's window as last sent. |
| `my_items`, `my_gold`, `my_spell` | This side's window. |
| `their_accept`, `my_accept` | The two accept flags. |
| `armed_from` | The player `trade_expect` is armed for, when it still is. |

# Enchanting in the window

`cast` with `trade_slot: 6` casts a spell at the other player's item in the non-traded slot while the trade window
is open. The trade must complete for the enchant to apply. See [spells and combat](spells-and-combat.md).

# Examples

Hand over 5 linen cloth and 50 copper:

```
forever rpc ./slots/ACCOUNT trade '{"to": "Otherchar", "items": [{"item": 2589, "count": 5}], "gold": 50}'
```

Step by step, the other player offering the trade:

```
forever rpc ./slots/ACCOUNT trade_expect '{"from": "Otherchar", "wait_ms": 20000}'
forever rpc ./slots/ACCOUNT trade_set_gold '{"gold": 500}'
forever rpc ./slots/ACCOUNT trade_status '{}'
forever rpc ./slots/ACCOUNT trade_accept '{"wait_ms": 20000}'
```

Step by step, this side offering the trade:

```
forever rpc ./slots/ACCOUNT trade_open '{"to": "Otherchar", "open_wait_ms": 20000}'
forever rpc ./slots/ACCOUNT trade_set_item '{"trade_slot": 0, "item_entry": 2589}'
forever rpc ./slots/ACCOUNT trade_set_gold '{"gold": 500}'
forever rpc ./slots/ACCOUNT trade_accept '{"wait_ms": 20000}'
```

There are no trade act steps: a script that trades uses the `act` method's sibling calls, or drives the trade from
its own code over the daemon.

# What you see

Both the one-call trade and the step calls answer the result object above; `trade_status` answers the window
object, `trade_cancel` answers `{"status": "<last status name>"}`. Refusals that needed no packet (a player not in
view, no money, too far to start, no window open) are JSON-RPC errors.

# Common problems

- `trade needs items or gold to hand over`; `trade: have 3 of item 2589 in the bags, 5 wanted`.
- `trade: that takes 7 trade slots and the window has 6: ask for fewer kinds of item or whole stacks`.
- `trade: N copper wanted, the purse holds M`.
- `no trade window is open: trade_open or trade_expect first`.
- `trade_set_item needs trade_slot 0-6`.
- `cast: trade_slot 2 is not castable: the core addresses only the other player's slot 6`.
- `result: "too_far"` — the other player moved out of range; walk back and trade again.
