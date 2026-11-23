# Forever agent #96: the trade methods

What this round adds: the trade window on a ForeverCore server. `internal/forever/trade` holds the packets,
`internal/forever/world/trade.go` the state a session keeps and the two packet handlers, `trade_flow.go` the flows,
`serve_trade_methods.go` the methods, and `trade_cast.go` the cast at the other player's item. Fake world only: no live
run yet (the live trade is queued for the next live session).

## The protocol (ForeverCore at the pin, Classic 1.60)

Every layout below is read from the core's own writer or reader. The turtle-agent flow (`internal/session/trade.go`) is
the reference for what a call does; the packets are not the same (the Turtle 1.12 core sends `SMSG_TRADE_STATUS_EXTENDED`
and has a scam-prevention delay, ForeverCore has neither).

| Packet | Layout | Source |
|---|---|---|
| `CMSG_INITIATE_TRADE` | the other player's packed GUID | `TradePackets.cpp:33-36`, handler `TradeHandler.cpp:612-738` |
| `CMSG_BEGIN_TRADE`, `CMSG_BUSY_TRADE`, `CMSG_UNACCEPT_TRADE`, `CMSG_CANCEL_TRADE` | no body | `TradePackets.h`, `TradeHandler.cpp:49-60, 574-610` |
| `CMSG_ACCEPT_TRADE` | u32 state index of the OTHER side | `TradePackets.cpp:23-26`, `TradeHandler.cpp:266-273` |
| `CMSG_SET_TRADE_ITEM` | u8 trade slot, u8 container, u8 slot | `TradePackets.cpp:48-51`, `TradeHandler.cpp:750-796` |
| `CMSG_CLEAR_TRADE_ITEM` | u8 trade slot | `TradePackets.cpp:28-31`, `TradeHandler.cpp:798-811` |
| `CMSG_SET_TRADE_GOLD` | u64 copper | `TradePackets.cpp:43-46`, `TradeHandler.cpp:740-748` |
| `SMSG_TRADE_STATUS` | bit (same Battle.net account), 5 bits status, then a tail per status (failed: bit, i32 bag result, i32 item; initiated: u32 id; proposed: two packed GUIDs; wrong realm and not on taplist: u8 slot) | `TradePackets.cpp:53-86`, statuses `SharedDefines.h:7114-7141` |
| `SMSG_TRADE_UPDATED` | u8 which player (1: the other side), u32 id, u32 client index, u32 server index, u64 gold, i32 currency type and quantity, i32 enchant, u32 count, then per item u8 slot, ItemInstance, u32 count, packed gift creator GUID, an "unwrapped" bit and for an unwrapped item the enchants, creator, charges, durabilities, gems | `TradePackets.cpp:88-136`, `TradeHandler.cpp:62-132` |

Opcodes are the core's (`Opcodes.h:42, 135, 145, 176, 251, 522-524, 961-962, 1032, 2428-2429`), none in the Classic client
override table. Distances: `TRADE_DISTANCE` 11.11 yd (`ObjectDefines.h:27`); the calls require 10.

What the core does, which the flows follow:

- A proposal makes the trade on BOTH sides and sends `proposed` to both players, each naming the other
  (`TradeHandler.cpp:728-738`). The proposer's real client answers its own `proposed` with `CMSG_BUSY_TRADE`, which the
  core swallows once (`:49-60`, `:734`); the agent sends it too. The proposed player answers `CMSG_BEGIN_TRADE` and both
  get `initiated` (`:583-593`).
- Every effective change un-accepts both sides: each gets `unaccepted` (`TradeData.cpp:135-148`; the turtle-agent name is
  `back_to_trade`), and the other player gets `SMSG_TRADE_UPDATED` with the changed side. A change that changes nothing (the
  same item in the slot, the same gold) is silent, so the calls skip it before sending.
- An accept names the other side's current state index, the one its last `SMSG_TRADE_UPDATED` carried, or 1 for a side that
  has not changed (`TradeData.h:39`). A stale one answers `state_changed` (`TradeHandler.cpp:266-273`).
- `too_far` and `failed` (no money, no room, a bound item) after an accept refuse the accept and leave the window open;
  `not_on_taplist` refuses a bound item the same way. `cancelled` closes it. A `too_far` or `failed` before the proposal
  came means the proposal itself was refused, which closes it (`tradeCloses`, `trade.go`).
- A refused set (bad slot, no item, a duplicate in two slots) answers `cancelled` and leaves the trade standing on the core's
  side (`TradeHandler.cpp:757-782`); a real client closes its window, and the flow sends `CMSG_CANCEL_TRADE` so the other player
  is not left in it.
- A cancel whose closing status does not come within a second (a lagging link) is remembered (`staleCancel`): the next trade ignores a `cancelled` that arrives before its own `proposed`. The status carries no trade id, so a late one that arrives after the new proposal cannot be told apart.
- Items and gold change hands after the `complete` status, in the update flush that follows, so the verification waits up
  to 2 s for them.

## The answer shape

`trade`, `trade_open`, `trade_expect` and `trade_accept` answer `ServeTradeResult`: `since` (the event-log cursor before the
call's first packet), `ok`, `result` (`completed`, `open`, `cancelled`, `timeout`, `busy`, `too_far`, `failed`,
`state_changed`, `changed`, `ignored`, `no_target`, `wrong_faction`, `you_dead`, `target_dead`, ...), `status` (the last
status by name), `partner`, `partner_name`, `gave[]` (`item`, `count`, `trade_slot`, `split`), `gold`, `received[]` (the
other side's window), `received_gold`, `partner_accepted`, `verified`, `detail`, `event` (the last `trade_status` row after
`since`) and, for a step call that left the window open, `trade` (the state `trade_status` shows).
`trade_set_item` and `trade_set_gold` answer `ServeTradeSetResult`. `trade_status` answers the state at any time, also after
the link dropped. Parameter names are turtle-agent's (`to`, `from`, `items [{item, count}]`, `gold` in copper, `trade_slot`,
`item_entry`, `bag`, `slot`, `wait_ms`, `open_wait_ms`).

Preconditions are RPC errors raised before any packet: not in the world, no such player in view, a creature or yourself, more
than 10 yd away, dead, more gold than the purse holds, an item the bags lack (an equipped item cannot be traded), more than
six stacks, an item already in another trade slot, a slot above 6, no window open for a step call, another trade running or a
`trade_expect` waiting, a bad `wait_ms` (0 is the default, at most 60000). Once a packet went out a refusal is a result with
`ok: false`; only a stop (the caller's cancel, a dead link) or an answer that never came stays an error. A set the core never
answered within 3 s is an error that says the request went out.

## The methods

| Method | What it does |
|---|---|
| `trade {to, items?, gold?, wait_ms?, open_wait_ms?}` | One call: plan from the bags first (a request is met by a whole stack of exactly that count, else a split off the smallest bigger stack, else whole stacks largest first), propose, wait up to `open_wait_ms` (8 s) for the window, split, fill it, accept, and wait up to `wait_ms` (15 s) for the other side's accept. When the other side ADDS to its window under the accept it accepts again (up to four times, naming the new state index); when it takes value out `state_changed`, the trade is not accepted again by itself: the window is cancelled and the result is `changed` or `state_changed` (an accept is consent to the window as it was; `trade_open` and `trade_accept` let the caller decide each change). A trade that does not complete is cancelled (`CMSG_CANCEL_TRADE`) and answered with `ok: false`. `verified` says the bags and purse show the items and gold gone. |
| `trade_open {to, open_wait_ms?}` | Propose and return once the window is open, which it leaves open. |
| `trade_expect {from, wait_ms?}` | Arm the accept of one incoming proposal from that player (a guid, or a name: one in view arms on its guid, any other name on the cached name when the proposal comes). The agent answers it with `CMSG_BEGIN_TRADE` and returns once the window is open (default 15 s). Every other proposal is declined with `CMSG_CANCEL_TRADE` (a `trade_status` event with `declined: true`), as when nothing is armed: an idle agent declines every proposal. |
| `trade_set_item {trade_slot, item_entry or bag+slot, clear?}` | Put an item into slot 0-6 (6 is the slot that is not traded, where an enchant is cast; it also takes an equipped item), or empty the slot. Returns on the `unaccepted` that answers an effective change; the same item again answers `unchanged` and sends nothing. A bound item is `not_on_taplist` and the window stays open; a refused one is `cancelled` and closes it. |
| `trade_set_gold {gold}` | Put copper into the window (0 takes it back). More than the purse holds is an error; the core's `failed` for gold it does not think we have is a result. |
| `trade_accept {wait_ms?}` | Accept the open step trade and wait up to `wait_ms` (15 s). It does not accept again by itself: a change after the accept is the result `changed` with the window in `trade`; the other side not accepting in time withdraws the accept (`CMSG_UNACCEPT_TRADE`), leaves the window open and answers `timeout`; the core's own refusals (`state_changed`, `too_far`, `failed`) are results with the window open. |
| `trade_cancel {}` | Close the trade (`CMSG_CANCEL_TRADE`, up to a second for the closing status), drop an armed accept, answer `{status}`. Safe twice. |
| `trade_status {}` | The state: `active`, `open`, `partner`, `status`, `their_items`, `their_gold`, `my_items`, `my_gold`, `their_accept`, `my_accept`, `armed_from`. |
| `cast {spell_id, trade_slot: 6}` | An enchant cast at the other player's item in the non-traded slot: `CMSG_CAST_SPELL` with `TARGET_FLAG_TRADE_ITEM` (`SpellDefines.h:324`) and the item GUID `ObjectGuid::TradeItem` (`ObjectGuid.cpp:1085`), which the core reads as the other player's slot 6 (`Spell.cpp:413-418`, `:7205-7226`). The core can name no other slot, so any other `trade_slot` is refused before a packet. It needs an open step window. |

## Events

Every `SMSG_TRADE_STATUS` is a `trade_status` event: `data.status` (`proposed`, `open`, `back_to_trade`, `accepted`,
`complete`, `cancelled`, `state_changed`, `too_far`, `failed`, `busy`, `ignored`, `no_target`, ...), `data.code` (the core's
number) and the other player as `source`. `failed` adds `bag_result` (the inventory result's name), `for_you` and
`item_entry`; `accepted` and `complete` add `their_items` and `their_gold`; a declined proposal adds `declined: true`; a `cancelled` that is the late answer to an earlier cancel (it came before the new proposal's own `proposed`, so it cannot be about the new trade) adds `stale: true` and changes nothing. The
names follow turtle-agent's where the state is the same (`proposed` is its `begin`, `open` its `open`, `back_to_trade` the
un-accept).

## Not live-proven

Everything here is the handler-boundary fake, which plays the core and a bot partner from `TradeHandler.cpp`. The live
proof (the next live session) hands a TortoiseBots bot one item and receives one back with `trade_open`, `trade_set_item`,
`trade_accept`; the module's bot trade (the module ticket "Port bot inventory, item, trade and loot-log commands") has to
exist on that build. Open questions for it: whether the bot answers `CMSG_BEGIN_TRADE` itself for its master and party
members (the Turtle bots do), what its `TradeStatusAction` does with a gift it has no use for, and whether the enchant cast's
`SPELL_GO` arrives for a trade-slot cast (the fake sends one).
