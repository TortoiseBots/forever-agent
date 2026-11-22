# Forever agent #92 round D3: the vendor and gossip methods

What this round adds: `vendor_list`, `buy`, `sell`, `gossip_hello` and `gossip_select` in
`serve_npc_methods.go`, on the method frame D2 built, plus the frame helpers they first call
(`methodNPC`, `methodCtx`, `methodStep`, `firstEventSince`, `methodEvent`). Fake world only: no
live run yet.

## The answer shape

Every method answers `ServeNPCResult`: `since` (the event-log cursor before the call's first
packet), `ok`, `event` (the row that answers the call, `null` when none did), `item` or `items`,
`menu`, and `lines` (the act steps' own lines). The method names and parameter names are the old
daemon's (`guid`, `item_entry`, `count`, `bag`, `slot`, `wait_ms`) plus `muid`, `force`, `option`,
`code`. The old methods returned at once with the cursor; these wait for the server's own answer
and return it, because the act steps underneath already do.

A call that fails before any packet is an RPC error: a bad parameter, a unit that is not in the
world model, an NPC further than 4 yards (methods never walk), the wrong NPC flags, an option the
open menu does not have. Once a packet went out the call answers a result: `ok: false` with the
failure event, whose `text` is the act's error. A cancel by the caller and a dead link stay
errors. `wait_ms` bounds the whole call (default 15000, at most 60000). Running out of it is an RPC
error, not a failure result: after the purchase or sale went out the message says the outcome is unknown and
names the event-log cursor the call started from (the server may still complete it, and a caller that retried a
reported failure would do it twice); while the list was still being read it says no purchase was sent. The
method does not track a timed-out request to a later event: a caller that needs the outcome reads `event_log`
from that cursor and the inventory. The act steps keep
their own 5 s answer waits, and what they report after those (no answer, a success the inventory
did not show) is still `ok: false` with the act's text; a caller that sees one of those reads
the inventory before retrying.

## The methods

| Method | What it does |
|---|---|
| `vendor_list {guid}` | The vendor flag is checked, then the act `vendor` step runs (it asks the server afresh every time). `items` are the list rows; the event is `vendor_list {entry, count}`. A refusal is `vendor_sell_failed` with the reason, as the old daemon did (the server answers a refused open with the sell response). |
| `buy {guid, item_entry or muid, count?}` | `bag` and `slot` are refused: a purchase lands in the backpack on this server. `count` 1-255 (default 1). When this NPC's list is not cached the `vendor` step runs first. A row the cached list lacks is an RPC error; after the implicit list read it is reported as `vendor_buy_failed`, because a packet has gone out. Success is `vendor_buy {item_entry, muid, count, copper_before, copper_after}` and `item` is the new row when one appeared (a purchase that merged into a stack has none). |
| `sell {guid, item_entry or bag+slot, count?, force?}` | The item is resolved before any packet (one stack; an entry in two stacks needs a slot). `item` is the row before the sale. Success is `vendor_sell {item_entry, count}`; the act's refusals (equipped, a quest item without `force`, the server's reason) are `vendor_sell_failed`. |
| `gossip_hello {guid}` | The act `open` step: gossip when the NPC has the gossip flag, a quest hello otherwise. The event is the first `gossip` or `quest_list` row after `since`; a quest-only NPC has no handler event, so the method writes `quest_list {quests: [{quest_id, title}]}` itself. `menu` is the open menu (`menu_id`, `options` as `{id, text}`, `quests`). |
| `gossip_select {guid, option}` | `code` is refused ("coded gossip options are not ported"). The NPC must have an open menu from the last hello or select, and the menu must hold the option, both checked before any packet. The event is the first `gossip`, `gossip_complete` or `vendor_list` row after `since` (nil is normal: most options open nothing); `menu` is set when a submenu came. |

## Decisions

The shared `methodAct` is reset the way an `npc` step resets it when a call names another NPC: the
vendor list, the menu and the quest list belong to the NPC they were read from. A `guid` the
world model has no unit for is an error, never a search by entry.

`vendor_buy`, `vendor_sell` and their failures, and the quest list of a quest-only NPC, are
written by the method (`methodEvent`) because no packet handler emits them. They share the event
log's seq axis, so `event_log` from `since` shows them with the gossip rows.

## Not live-proven

Everything here is the handler-boundary fake. The live proof (L1) buys from a vendor through a
gossip option and sells back, and reads the answers.
