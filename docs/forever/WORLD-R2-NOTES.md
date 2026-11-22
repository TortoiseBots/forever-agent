# Forever item actions, round R2 notes

Scope: the character's item actions from the player's side (equip, store, swap, split, destroy, use and open an
item), and an inventory that follows them without a relog. Code: `internal/forever/items` (`packets.go` builders,
`parse.go` the two server answers, `tracker.go` the live inventory), `internal/forever/objects` (the one decoded
region of `ActivePlayerData::WriteUpdate`), `internal/forever/world` (`inventory_events.go` the two packet handlers,
`inventory_act.go` the act steps), `internal/forever/wire` (the nine client opcodes and the one server opcode).

Everything below was checked in ForeverCore at d02645dd21. The packet capture cross-check was run once against the
private sets; it prints counts only and no capture byte, GUID, name or file name is in the repository.

## The client packets

Each writer follows the server's reader; the reader is what defines the bytes a real client may send. Positions are
the player's own packed form: a container byte (255 for the player's own slots, 30-33 for a bag) and the slot in it
(Player.cpp:10486-10500).

| Packet | Wire | Body | Core |
|---|---|---|---|
| `CMSG_AUTO_EQUIP_ITEM` | 0x400004 | InvUpdate(1 entry), PackSlot, Slot | ItemPackets.cpp:217-222; ItemHandler.cpp:195-201 |
| `CMSG_AUTO_STORE_BAG_ITEM` | 0x400005 | InvUpdate(0), ContainerSlotA, ContainerSlotB, SlotA | ItemPackets.cpp:231-240; ItemHandler.cpp:780-786 |
| `CMSG_SWAP_ITEM` | 0x400006 | InvUpdate(2), ContainerSlotB, ContainerSlotA, SlotB, SlotA | ItemPackets.cpp:208-215; ItemHandler.cpp:150-193 |
| `CMSG_SWAP_INV_ITEM` | 0x400007 | InvUpdate(2), Slot2, Slot1 | ItemPackets.cpp:201-206; ItemHandler.cpp:89-95 |
| `CMSG_SPLIT_ITEM` | 0x400008 | InvUpdate(0), FromPackSlot, FromSlot, ToPackSlot, ToSlot, Quantity | ItemPackets.cpp:191-199; ItemHandler.cpp:37-42 |
| `CMSG_DESTROY_ITEM` | 0x3D016A | Count, ContainerId, SlotNum | ItemPackets.cpp:242-247; ItemHandler.cpp:333-361 |
| `CMSG_USE_ITEM` | 0x3D016F | PackSlot, Slot, CastItem (packed), SpellCastRequest | SpellPackets.cpp:301-308, 243-288; SpellHandler.cpp:50-64 |
| `CMSG_OPEN_ITEM` | 0x3D0211 | container byte, slot byte | SpellPackets.cpp:877-881; SpellHandler.cpp:66-72 |

Three things the core's comments decide:

* **InvUpdate** is `BitsSize<2>` — the count in two bits, then `ResetBitPos`, then one (ContainerSlot, Slot) pair per
  entry (ItemPacketsCommon.cpp:237-248). Count 1 is therefore the byte `0x40`, not `0x01`; a builder that writes the
  count as a byte fails `TestBuildAutoEquipItemInvUpdateOneEntry`. The server checks only the count (exactly one
  entry for AutoEquip, two for both swaps, none for AutoStore and Split).
* **AutoStoreBagItem is Classic order**: source bag, target bag, source slot. The core's own comment records what
  reading it the other way round did live (the item was never found and stayed locked until a relog).
* **SwapInvItem sends the destination first**: `Slot2` is read before `Slot1`, and the handler moves `Slot1` to
  `Slot2`. A client that writes source-then-destination swaps the wrong way.

Every `ObjectGuid` in these packets is the packed form (ObjectGuid.cpp:1087-1111), so `CMSG_USE_ITEM`'s
`CastItem` is packed too.

## The server's two answers

* **`SMSG_ITEM_PUSH_RESULT`** (0x4500CC, ItemPackets.cpp:260-297): player GUID, `Slot` (the container), `SlotInBag`
  (`-1` when the gain merged into a stack, Player::SendNewItem), an `ItemInstance` (item id, seven-bit modifier
  count, modifiers, optional bonus block), the proxy id and quantities, the encounter and battle-pet fields, the
  item's GUID, the toast list, then the flag bits and two optional fields. The 1.60 code sets neither optional
  (Player.cpp:14964-15020), so a packet that carries crafting data is refused rather than misread.
* **`SMSG_INVENTORY_CHANGE_FAILURE`** (0x680005, ItemPackets.cpp:161-189): the result, two item GUIDs, a
  bag-subclass byte and one of three tails — the required level for `CANT_EQUIP_LEVEL_I` and
  `PURCHASE_LEVEL_TOO_LOW`, the source/destination of `EVENT_AUTOEQUIP_BIND_CONFIRM`, the limit category for the
  three `ITEM_MAX_LIMIT_CATEGORY_*` results. The names come from ItemDefines.h:26-160. `Player::SendEquipError`
  never sends a success packet (Player.cpp:14131-14144).

## The live inventory

Decision 1 stands: the login snapshot (round R1) plus events. The inventory has **one source of truth per slot**:
the server's own words, which are its update fields (the player's own `InvSlots` and a bag's `ContainerData`
slots, both merged into the world model) and its item push results. `items.Tracker` is an overlay on `Snapshot`,
never a second model; it holds two small things:

* **Push placements.** An item's own block names its owner and container but never its slot, so a gain's place comes
  from its push result (`Slot`, `SlotInBag`; a merged gain names no slot and moves nothing). A placement fills a row
  the snapshot cannot place, and only such a row: the server's slots always win. It is dropped when a slot update
  names its position or its item, when the item is destroyed, and after a minute. The 513th live placement is an
  error (`items.ErrPlacementsFull`) and marks the inventory stale: the gain is somewhere the client cannot name.
* **Own moves, keyed by their sequence.** Every item request is registered (`Tracker.Begin`) with the items it asks
  about, the destinations the client knows, the items whose place the server chooses, and the positions whose
  content changes. A move only bridges the gap until the server confirms it: for `MoveWindow` (1 s) the snapshot's
  places stand; until `ConfirmWindow` (2 s from the request) the client's guess stands; after that the items it
  touched are **unknown** (slot 0xFF), never silently at the guess. All of that time the rows carry
  `Unconfirmed`. A move is confirmed, and dropped, when the server's slot updates named every position and item it
  touched; a failure that names one of its items refuses it at once (a failure is matched by the item GUIDs the
  request asked about, an unmatched one refuses nothing). A later unrelated slot update never touches a move it
  does not name and never reverts one the server confirmed, because the model already holds the server's slots.

What is decoded of `ActivePlayerData::WriteUpdate` (the fourteen-block Classic changes mask,
UpdateFields.cpp:1374-1399): the InvSlots run (UpdateFields.cpp:7195-7203), `Coinage` (classic bit 58, group 32)
and `AccountBankCoinage` (bit 59), which a purchase, sale, loot or quest reward sends in the same update as the
slots (values written before TransmogMetadata and InvSlots, UpdateFields.cpp:6751-6755, 7150-7203), the
fixed `TransmogMetadata` block a main-hand or off-hand swap stamps (Player.cpp:12847-12852, UpdateFields.cpp:5377-5386),
and the three fields a purchase's own update names next to the slot and the money, which are **stepped over**:
Transmog (classic bit 18, a dynamic field of u32 appearance ids whose change mask is written in the second bit run),
LocalFlags (101) and TransportServerTime (145, or 146 from build 70425); a present group 134 also writes the four
optional-field presence bits and their flush before the slots (UpdateFields.cpp:7153-7163). The structure's **group
bits** are recognized as such and never counted as fields the walk does not know. Any other set field means the walk
cannot find the slot values, and such an update is **never silent**: when it also
names inventory slots the block says so (`Info.InvSlotsUndecoded`), the session counts `inventory_stale` in the
report, logs the first one and the tracker marks the inventory **stale** (`inv` prints a `STALE` line, `destroy`
and `split` refuse by slot, and the server `inv` method carries `stale`). Stale clears on the next own create (a
relog or a map change). There is no in-session resync request in this protocol round, so that is the resync.

**An equipment move's answer is still not readable.** The server writes a `PlayerData` update first (VisibleItems,
AvgItemLevel, PlayerFlags) and the `ActivePlayerData` block after it, so reaching the InvSlots mask would mean
skipping PlayerData's update values, the remap decision 1 keeps out of this round. A self update with both blocks is
left to its declared size and reported as `Info.ActivePlayerSkipped`: a live own move explains it (the equip's
answer); with none, the inventory is stale. The consequence is the decision the owner made: **an equipment move is
never confirmed**, so the equipped item and the one it displaced are unknown after `ConfirmWindow`, and a
destroy, split, use or open of either waits for confirmation, then refuses.

* **The mask bits move with the realm build**: the group bit and the first entry are 168/169 up to build 70290 and
  170/171 from 70425 (ClassicActivePlayerDataBit, UpdateFields.cpp:5823-5867). `ParseUpdateObjectForBuild` takes
  the realm's build; the session passes `Options.RealmBuild`. A build this round has no layout for (below 70291)
  decodes no ActivePlayerData at all and reports the block as skipped.
* **The agreement check**: a player-slot placement needs the item's `ContainedIn` to be the character (or unset), a
  bag placement needs it to be something else.
* **Clearing**: every own create (login burst, map change) resets both overlays and the stale mark; a destroy forgets
  that item's overlay entries. The world model is still the source of the rows (an item whose object is gone leaves
  the inventory). At most 64 requests are unconfirmed at once; the oldest expired one is dropped to make room and
  marks the inventory stale. Known limit: a request that moves nothing (destroy, split, use, open) is never "confirmed" and is only dropped this
  way, so the 65th item request of one session, with no relog or map change between, marks the inventory stale.

## The act steps

| Step | Sends | Notes |
|---|---|---|
| `equip entry=<id> dst=<0-18>` / `equip slot=<pos> dst=<0-18>` | `CMSG_SWAP_INV_ITEM` (or `CMSG_SWAP_ITEM` from a bag) | the destination is exact; the displaced item moves into the source; an occupant of the same entry is refused (the core merges or fills same-entry items, Player::SwapItem, Player.cpp:13795-13840) |
| `equip entry=<id>` / `equip slot=<pos>` (no `dst`) | `CMSG_AUTO_EQUIP_ITEM` | the server picks the slot; the item and every equipped item are unknown until the next login |
| `store entry=<id> [bag=<30-33>]` | `CMSG_AUTO_STORE_BAG_ITEM` | `bag` defaults to the first bag the character carries; the bag's own slot updates (and the player's, for a backpack source) confirm it; a bag-to-bag store shows the new slot only |
| `swap a=<pos> b=<pos>` | `CMSG_SWAP_INV_ITEM`, or `CMSG_SWAP_ITEM` when either position is a bag slot | two items of the same entry are refused (the server merges or fills them) |
| `split from=<pos> [to=<pos>] n=<count>` | `CMSG_SPLIT_ITEM` | `to` defaults to 255/255 (autostore); `from == to` is refused (the server drops it silently); the line is the state read back (`entry 117, 2 left; new stack entry 117 at slot 38`), and a split that changed no count fails |
| `destroy entry=<id>` / `destroy slot=<pos> [n=<count>]` | `CMSG_DESTROY_ITEM` | `n` absent or 0: the whole stack; `n` over the stack is refused; an entry in several stacks needs `slot=`; the step checks the item is gone (or its count is down by `n`) |
| `use entry=<id> spell=<id> [target=self\|npc]` | `CMSG_USE_ITEM` | see below |
| `open entry=<id>` / `open slot=<pos>` | `CMSG_OPEN_ITEM` | a bare `open` keeps its R0 meaning (the NPC menu) |

A position is `36` (the player's own slot, container 255) or `30:2` (slot 2 of the bag in bag slot 30). Positions are
range-checked before any packet goes out: the player's own 0-18, 30-33 and 35-50 (the 16 real backpack slots,
`INVENTORY_DEFAULT_SIZE`), a bag index below 36, and 255/255 as the autostore marker; the bank (63+) and the buyback
(72+) are refused because the server answers some of those with silence. Every step prints where the item ended up
and whether the server has said so: `step 2 equip ok entry 26 at slot 15 (unconfirmed)`, and a refusal is the step's
failure with the server's own name: `step 1 equip fail equip 25: the server refused it: cant_equip_level_i (needs
level 12)`.

**An item whose place the server has not confirmed is never addressed by a destroy, split, use, open or move.** The
step waits up to `ConfirmWindow` for the confirmation and then re-reads the item by its GUID and entry; if it is
still unconfirmed the step fails (`the server has not confirmed where entry 26 is`). `destroy` always sends the slot
of a row it read back, never a slot guess.

`equip` with a destination sends the swap a real client's targeted equip sends: the core's own
`CMSG_AUTO_EQUIP_ITEM_SLOT` handler ends in `Player::SwapItem` (ItemHandler.cpp:134-190), and `CMSG_SWAP_INV_ITEM`
moves the same two items, so that is what this client sends; the slot-specific opcode is not used (see the live
section for what was and was not established about it). Without a destination the plain AutoEquip is sent and the step
says the slot is unknown, because the server's answer is an ActivePlayerData update behind a PlayerData one this round
does not walk (see above).
`store` needs a real bag (30-33): the bag's slots update names the slot, the backpack is only ever named in the
player's own ActivePlayerData update.

`use` needs its spell named because the packet carries the item's on-use spell (the server looks it up before it
reads the item at all, SpellHandler.cpp:50-57) and this repository has no item template data: the client would read
it from the game's own item cache. An item's on-use spell id is a property of the item template, not of the item the
character holds, so it cannot be derived from the packets this client sees. The step is otherwise complete: the
bag/slot, the item GUID and the cast request (with the target block) are exactly what a real client sends. `use` fails
on a refusal of the move and on the server's cast failure (`SMSG_CAST_FAILED` / `SMSG_SPELL_FAILURE` for the spell,
with the reason's name), and says whether it saw `SMSG_SPELL_GO` (`entry 118 spell 433 (the server cast it)` or
`(no cast seen)`). `open` fails on a loot refusal (`SMSG_LOOT_RESPONSE` with `Acquired` false and its reason) and
releases a loot window it opened (`CMSG_LOOT_RELEASE`; the release is sent, its confirmation is not waited for).

## Tests

* `items/packets_test.go`: every builder's exact bytes, then a hand-written server-side reader (the core's own
  reading) decodes them. The failing case pins the two-bit InvUpdate count and its flush.
* `items/parse_test.go`: hand-built push results and refusals with sentinels; the failing case is the level tail of
  `CANT_EQUIP_LEVEL_I`, which a reader that stops at the base layout leaves behind. Trailing and truncated bodies
  are refused, and the result names are pinned.
* `objects/activeplayer_update_test.go`: the InvSlots region against masks written by hand; Coinage (and
  AccountBankCoinage) next to the slots decode for both; a purchase's own update (Transmog, LocalFlags and
  TransportServerTime next to the slot and the money, the live mask of 2026-11-22) decodes to the slot and the
  money; an update with another field set decodes nothing and reports that slots were lost; a block with PlayerData
  and ActivePlayerData reports the second as skipped; the 70291 layout and an unknown build; the bag slots a
  ContainerData update names.
* `items/tracker_test.go`: a move bridges (snapshot, guess, unknown) and the server's slots confirm it, a refusal is
  matched by item GUID, a partial slot update keeps unrelated placements, placements expire and the cap is an error,
  a bag-to-bag store shows the new slot only, undecoded updates mark the inventory stale, the move list is bounded.
* `world/inventory_actions_fake_test.go` and `world/inventory_r2fix_test.go`: the handler boundary. Each fix-round
  finding has a test that fails on the code before the round (equip then an unrelated slot update; destroy after a
  bare equip; a bag-to-bag store; Coinage plus slots; an undecoded field next to slots; a same-entry swap and equip;
  a failed `use`; a refused `open`; a split onto itself and a dropped split; an ignored destroy; 513 placements;
  out-of-range positions; an equipment update with no move of its own).
* `items/capture_check_test.go` (skipped unless `FOREVER_CAPTURE_PKT`/`_DIR` is set): every update object, push
  result and refusal in the private captures is consumed to its last byte.

## What works live

The first live run, before the fix round (so its step lines lack the `(unconfirmed)` marks): 2026-11-21 on the test realm
(client and realm build 70959), a level-40 warrior test character on the GM account, one `forever act` run per stage. Items: entry 25 (a one-hand sword) and entry 117 (a stackable food, "Tough
Jerky", already carried as a stack of 4).

Gains, with no relog: `.additem 25 1` arrived with a push result naming slot 255/37 (`item push: entry 25 count 1 at
slot 255/37, 2 in the inventory`), and `.additem 117 5` merged into the stack (`merged into a stack, 9 in the
inventory`) — both listed by the next `inv` step at their new counts and slots.

```
step 2 equip ok entry 25 at slot 15          # equip slot=37 dst=15; the displaced sword moved to 37
step 4 swap ok entry 25 at slot 37; entry 25 at slot 15
step 6 split ok entry 117, 2 left; entry 117 at slot 38
step 8 swap ok entry 117 at slot 35; entry 6948 at slot 36
step 10 destroy ok entry 117, 2 left
```

The `inv` after every step showed the moved items at their new slots, and the run's last `inv` was byte-identical to
the next login's (`inv: 8 item(s), 0 copper`, the same eight rows with the same GUIDs and the same free slot 38):
the inventory matched the server without a relog. The server's own rows (`characters.character_inventory` joined to
`item_instance`) were read before the run, after the equip, after the swap and at the end, and matched the client's
rows every time.

The capture cross-check was run over the private capture sets: every update object, push result and refusal parsed to
its last byte, including the InvSlots updates and the push results (counts only in the log line).

What was and was not settled about the slot-specific `CMSG_AUTO_EQUIP_ITEM_SLOT`: the opcode this client defines is 0x400009
(`wire.CMSGAutoEquipItemSlot`), the earlier text said the wire value is 0x410009 (`clientopcodes_gen.go` lists
0x420009 -> 0x410009), and an unknown opcode is also accepted without an error. The earlier run's packet was therefore
not shown to be the right one, and the claim that the opcode is "accepted but inert" is **withdrawn**: it is unproven.
`CMSG_SWAP_INV_ITEM` (0x400007) did move the same two items exactly as asked, so `equip dst=` sends that swap (the one
`Player::SwapItem` path the core's own slot-equip handler ends in) and the slot-specific opcode is not used by this
client. To prove the claim, one run must send 0x400009 with the packet the core reads (InvUpdate with one entry, the
item's GUID, the destination slot) and compare the server's rows; that is on the live list below.

## Live checks to run for the fix round

For the lead's live queue (one test character, the GM account, the test realm; read the server's rows
(`character_inventory` joined to `item_instance`) before, after each step and after a relog):

1. **Equip, then a later delta, then relog.** `.additem` two weapons of different entries, `equip slot=<a> dst=15`,
   `.additem` a potion, `inv`: the potion's slot update must not move the equip back; after 2 s `inv` shows both
   weapons `slot unknown (a move of it was not confirmed by the server)`. A relog's `inv` must match the server's rows.
2. **Destroy after an equip.** After step 1 `destroy` of either weapon fails with `the server has not confirmed where`
   and the server's rows are untouched; `destroy` of an unrelated backpack item still works; after a relog both
   weapons destroy normally.
3. **Bare equip.** `equip entry=<id>` (no `dst`): the item and the equipped items are unknown; then `destroy` of the
   old backpack slot's item by `slot=` of an unrelated item still passes; nothing at the old slot is destroyed.
4. **Same-entry swap and equip.** Two stacks of one food: `swap` refuses; `split` then `store`/swap other entries.
5. **Purchase (Coinage plus slots).** Buy an item from a vendor: `inv` must show the new coinage and the item in the
   slot the server chose, with no relog (and `inventory_stale` 0 in the report).
6. **Bag-to-bag store.** `.additem` into bag 1, `store entry=<id> bag=<other bag>`: `inv` shows the new bag slot only, and
   a relog agrees.
7. **Split.** Split a stack: the line reads back the new counts and the new stack's slot; a split with `from == to`
   is refused with no packet.
8. **Use and open failures.** `use` of an item with a spell out of range or with a wrong spell id reports the cast
   failure; `open` on a locked box without the key reports the loot error; `open` on a lootable item prints the
   window and releases it.
9. **The opcode claim.** Send 0x400009 with the core's layout and compare the server's rows (see above).
10. **Range checks.** `swap a=35 b=63`, `split ... to=30:99`: refused with no packet.

## Gate lines

Full `vm-test ta` on the clean tree 8a367cd8: `RESULT: PASS  kind=ta sha=8a367cd8 dirty=0 files=2335`.

Race run (`go test -race -count=1 -timeout 20m ./internal/forever/... ./internal/objects/...`): `VM-TEST-END rc=0 seconds=780`.
