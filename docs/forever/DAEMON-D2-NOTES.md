# TBF #170 round D2: the method frame and the item methods

What this round adds: the shared method frame (`serve_method_frame.go`) that D3-D5 build on,
and the four item methods (`inventory`, `equipment`, `equip`, `use_item`) in
`serve_item_methods.go`. D1's `itemInfos` supplies names. No live run yet: the fake world is
the proof.

## The frame

`methodAct` is one `actState` the methods share for the life of the session, so a vendor list or
a menu one call read can serve the next; a reconnect starts empty. `runMethodStep` clears the line buffer, builds
the canonical `verb k=v` Raw form and calls `actStep`. `EventLog.Last` is the event helper D3-D5
use. The NPC reach check, the wait bound and the other event helpers were removed as unused (staticcheck U1000) and come back with the round that first calls them.

## The methods

| Method | Answer |
|---|---|
| `inventory {bags?, names?}` | placed backpack rows (container 255, slots 35-62) in slot order, the bag contents (30-33) when `bags` is true, then the items the snapshot cannot place (255/255, `placed:false`). Unknown inventory is an RPC error; a stale one still answers (the `inv` method carries the stale flag). `names:true` fills name/quality with one DB batch per table; a failed fetch leaves them unnamed. |
| `equipment {names?}` | occupied slots 0-18 with the core's names (`Player.h:739-757`: head .. tabard; slot names as the old daemon (`main_hand`, `off_hand`); no `temp_enchant` (the model does not track it)). |
| `equip {item_entry? | bag?+slot?}` | the act `equip` step (AutoEquip; the server chooses the slot) and the item's row after the move. Neither selector is an RPC error before any packet. |
| `use_item {item_entry? | bag?+slot?, target?, spell_id}` | `spell_id` is required: the on-use spell is in ItemEffect, not a queryable record. A non-zero target is resolved from the world model and passed through a fresh `actState`, never `methodAct`. The answer is the used item's row; a server refusal is the act's cast-failure error. |

The slot numbers are the core's (Player.h:719-841), not Turtle's: the backpack is 35-62 (16 real),
the bags 30-33, and the container byte 255 means the character's own slots. A caller written for
Turtle's 23-38 backpack must change.

The D2 methods keep TA's result shapes, which have no `ok`/`event` fields: a refusal after the packet
goes out is the act's error returned as the call's error. D3's `ServeNPCResult` adds the
`ok`/`event` shape; the item methods here predate it.

`equip` returns the row as the client knows it after the move. Equipment changes are never
confirmed by this client round (the answer is a PassivePlayerData update behind a PlayerData one;
see WORLD-R2-NOTES.md), so after AutoEquip the row may be `placed:false` until a relog or a map
change. The method does not invent a slot.

## Not live-proven

Every answer here is fake-world only. The live proof (L1) equips a bought item and reads it back
through `equipment`; the server's own slot answer is what that run will show.

## Removed from the not-ported list

`inventory`, `equipment`, `equip`, `use_item` now answer; `trade`, `teleport` and `map_snapshot`
stay not ported, as `TestServeNotPortedAndUnknown` pins.
