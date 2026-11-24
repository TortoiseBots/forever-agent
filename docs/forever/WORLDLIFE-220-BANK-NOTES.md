# Forever world life, issue #220: the character bank

Scope: `bank_list`, `bank_deposit`, `bank_withdraw` and `bank_buy_slot` on the Forever daemon, beside the mail and
auction layers (the last rows of the parity re-score's "Mail, auction house, bank" and ticket T2).
Code: `internal/forever/bank` (packets and the tab price table), `internal/forever/items` (the auto-store packet),
`internal/forever/wire` (opcodes), `internal/forever/world` (`bank.go` the session glue, `serve_bank_methods.go` the
methods), `internal/events` (one new kind).

Everything below was checked in ForeverCore at 6517af05. Fixtures are hand-built from the core's writers and readers;
no capture byte is in the repository. turtle-agent never had a bank (it was a vanilla client with no bank code), so
the method names and fields are this server's own, in the mail methods' style.

Status: implemented and tested against the fake world. **No live run yet**: the live checks are at the end.

## There is no bank window packet

A bank opens client-side: the client sends `CMSG_BANKER_ACTIVATE` for the banker, the server starts the interaction
and answers `SMSG_NPC_INTERACTION_OPEN_RESULT` (`SendShowBank`, BankHandler.cpp:312-320). Every later bank command is
answered only while that interaction is open: `CanUseBank()` resolves the interaction's source unit, checks the banker
flag and the range, and a command without it is dropped with a log line and no packet (ItemHandler.cpp:1229-1248,
BankHandler.cpp:107-116). That is why `bank_list` opens the bank and the other three methods refuse before any packet
when it is not open (`the bank is not open (bank_list at the banker first ...)`).

The bank itself has no packet either: the character bank is the player's own slots 63-71 (`BANK_SLOT_BAG_START`..
`BANK_SLOT_BAG_END`, Player.h:801-802) and the bags equipped in them, so the contents are read from the inventory model
(the InvSlots and ContainerData updates the item layer already keeps). The core grants the first tab free with its tab
bag at creation (`GrantClassicFreeBankTab`, Player.cpp:2397-2411): slot 63 holds `ITEM_CHARACTER_BANK_TAB_BAG`
(242709) and the bank's items are inside it. Buying a tab after the first unlocks one more bank bag slot (64-71) for a
bag of the player's own (BankHandler.cpp:203-206).

## The client's packets

| Packet | Core opcode | Body | Core |
|---|---|---|---|
| `CMSG_BANKER_ACTIVATE` | 0x3E0049 | banker (packed GUID), interaction type int32 | BankPackets.cpp:49-54; the type is `PlayerInteractionType::CharacterBanker` (67), the one the core accepts for a unit with `UNIT_NPC_FLAG_BANKER` (BankHandler.cpp:82-90) |
| `CMSG_AUTOSTORE_BANK_ITEM` | 0x400002 | an empty InvUpdate, the source container and slot | BankPackets.cpp:31-36; one packet both ways: a bank position (`Player::IsBankPos`: 255/63-71 or a bank bag 63-71) is taken into the bags, any other is banked (BankHandler.cpp:105-145) |
| `CMSG_BUY_ACCOUNT_BANK_TAB` | 0x3E0128 | banker (packed GUID), bank type byte | BankPackets.cpp:38-42; the character bank's type is `BankType::Character` (0), and the opcode's account name is the client version's single buy packet (Opcodes.h:146) |

The wire numbers are the `ClassicClientOpcodes.inc` group rows; nothing there overrides them, so the client build's
group shift translates all three (`wire/opcodes_test.go` pins the mechanism).

The failing case for the auto-store layout is **`TestBuildAutoStoreBankItemWire`**: a builder that writes an InvUpdate
with entries, or that leaves the container byte off, fails it.

## The server's answers

| Packet | Body | Core |
|---|---|---|
| `SMSG_NPC_INTERACTION_OPEN_RESULT` | the NPC (packed), the interaction type int32, the success bit | NPCPackets.cpp:99-107; the session records it for the bank as it does for the vendor (npc.go) |
| `SMSG_UPDATE_OBJECT` | the player's InvSlots and the bank bag's ContainerData slots, the item's own ContainedIn | `_StoreItem` sets the slot and `Bag::StoreItem` sets the item's container and sends it (Player.cpp:12446-12500, Bag.cpp:161-174) |
| `SMSG_INVENTORY_CHANGE_FAILURE` | the result and the item it names | the core's refusal of a move (`CanBankItem`/`CanStoreItem` → `SendEquipError(msg, item, nullptr)`, BankHandler.cpp:119-126, :134-141); the tab purchase's `EQUIP_ERR_BANK_FULL` names no item (BankHandler.cpp:200-205) |

A deposit or a withdraw has no result packet of its own: the proof is the item's place in the model. The client's own
guess never names a bank destination (the request names only the source), so an item in a bank slot can only be the
server's word; a move the server refused is a result with `ok: false` and the refusal's name, and one the server never
confirmed is a result with `ok: false` and reason `unconfirmed` (the item's row says where the model still has it).

## The methods

All four take `guid` (the banker) and `wait_ms` (default 15 s, at most 60 s).

| Method | Parameters | Answer |
|---|---|---|
| `bank_list` | — | opens the banker and answers `banker`, `items[]` (`guid`, `bag`, `slot`, `item_entry`, `count`): the tab bags in 63-71 and every item inside them |
| `bank_deposit` | `item_entry` or `bag` with `slot` (the mail methods' selector: 255 and a carried slot, or a bag 30-33 and its slot) | the item's bank row: `item` with its new `bag`/`slot`, `item_entry`, `count` |
| `bank_withdraw` | `item_entry` (one in the bank) or `bag` with `slot` (the bank's positions: 255/63-71, or a bank bag 63-71 and its slot) | the item's carried row |
| `bank_buy_slot` | `tab`: the character's tab count, 0-8 | `price` (copper, from the table below), `tab`, and on a read-back `copper` before and after |

Every answer carries `since` (the event-log cursor before the call's first packet) and `event` (a `bank` row:
`command`, `ok`, and `reason`/`error`, the item's entry and place, a tab and its price).

Refusals before any packet (RPC errors): no banker GUID, an unknown or distant unit, a unit without the banker flag, a
bank command with the bank not open, a missing or bad selector, an equipped item or a worn bag, a position that is not
a bank position, a withdraw of an entry that is in the bank more than once, and a tab outside 0-8 or whose price the
purse cannot pay ("`tab N costs X copper and the purse holds Y: Z short`").

## The tab price, and what a purchase can be read back as

`HandleBuyBankTab` charges `BankTab.db2`'s `(BankType::Character, OrderIndex = the character's tab count)` row
(BankHandler.cpp:186-196) and drops the request silently when the purse cannot pay it (BankHandler.cpp:193-195). The
table is in `internal/forever/bank/bank.go` (`CharacterTabCosts`), read off the character rows of BankTab.db2 (the
server's data pack carries the same file as the client's, byte for byte):

| Tab (OrderIndex) | 0 | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 |
|---|---|---|---|---|---|---|---|---|---|
| copper | 0 | 1000 | 10000 | 100000 | 250000 | 500000 | 1000000 | 2000000 | 4000000 |

Tab 0 is the free one the core grants at creation, so a character's first purchase is tab 1 at 10 silver. The caller
names the tab because **this client cannot read the character's tab count back**: `NumCharacterBankTabs` is
ActivePlayerData bit 132-133, next to the tab settings' dynamic array, and the ActivePlayerData walk stops after
`AccountBankCoinage` by decision 1 (WORLD-R2-NOTES.md). The server ignores the number and grants its own next tab; the
number is used only for the price and the refusal.

For the same reason a successful purchase often cannot be proven: the update that carries the money also carries the
tab count and the new tab's settings, fields this client does not walk, so the update is skipped whole and the purse in
the model does not move. `bank_buy_slot` therefore reports `ok: true` when the purse did move by the price (a decodable
answer), a refusal when the server sent `EQUIP_ERR_BANK_FULL`, and otherwise an error that says the outcome is unknown
and to check the bank window and the purse — the same shape the mail layer uses for a send that went out without an
answer. That is the limit this round leaves: a bank tab purchase is confirmed by the operator, not by the model.

## The live checks (one trip to a bank NPC)

1. `bank_list` at any banker: `ok: true`, the tab bag row at 255/63, the items inside it.
2. `bank_deposit slot=<a carried slot>`: the item's row moves into 63/`<index>`; `inv` no longer lists it carried.
3. `bank_withdraw bag=63 slot=<index>`: the row moves back to a carried slot; `bank_list` no longer lists it.
4. `bank_deposit` with the banker closed (before `bank_list`, or after `close`): the RPC error, no packet sent.
5. `bank_buy_slot tab=<the character's tab count>`: the price from the table, the purse short of it refused before any
   packet; with the gold, the purchase goes out and the client's bank window shows the new tab.

## Limits

- The bank tab count is not read back (see above), so `bank_buy_slot` needs `tab` and its success is confirmed by the
  client, not the model.
- The account bank (BankType::Account, slots 103-107 and `CMSG_ACCOUNT_BANK_*`) is not built: nothing in the parity list
  asked for it, and the core's account handlers are separate.
- The bank's money (none in Classic's character bank) and the deposit-all (`CMSG_AUTO_DEPOSIT_CHARACTER_BANK`) are not
  built.
