# Forever vendor and quests, rounds R3 and R5 notes

Scope: R3, the vendor from the player's side (list, buy, sell, sell junk, buyback, repair) and R5, the quests
(status, hello, list, details, accept, request items, offer reward, complete, choose reward, abandon, credit
progress). Code: `internal/forever/items` (`vendor.go` builders, `vendor_parse.go` the server's answers),
`internal/forever/quest` (the quest wire layer), `internal/forever/world` (`vendor.go` and `quest.go` the session
glue, `vendor_act.go` and `quest_act.go` the act steps), `internal/forever/wire` (the opcodes of both rounds),
`cmd/forever-agent` (no new flags: the verbs run through `forever act`).

Everything below was checked in ForeverCore at d02645dd21 (`git show d02645dd21:<path>`). The private captures
(`set2` vendor and quest logs) were read once to check the layouts; the capture checks print counts only, and no
capture byte, GUID, name or file name is in the repository. Fixtures are hand-made from the core's writers.

Status: (filled in below when the rounds are done)

## R3: the vendor

### The client packets

Each writer follows the server's reader, which is what defines the bytes a real client may send.

| Packet | Wire | Body | Core |
|---|---|---|---|
| `CMSG_LIST_INVENTORY` | 0x3F0038 | the vendor's packed GUID | `Hello::Read`, NPCPackets.cpp:94-97; `HandleListInventoryOpcode`, ItemHandler.cpp:619-645 |
| `CMSG_BUY_ITEM` | 0x3F003B | vendor GUID, container GUID, ItemInstance, quantity i32, Muid u32, slot u32, item type i32 | `BuyItem::Read`, ItemPackets.cpp:29-38 |
| `CMSG_SELL_ITEM` | 0x3F0039 | vendor GUID, item GUID, amount u32 | `SellItem::Read`, ItemPackets.cpp:133-138 |
| `CMSG_SELL_ALL_JUNK_ITEMS` | 0x3F003A | vendor GUID | `SellAllJunkItems::Read`, ItemPackets.cpp:140-143 |
| `CMSG_BUY_BACK_ITEM` | 0x3F003C | vendor GUID, slot u32 | `BuyBackItem::Read`, ItemPackets.cpp:23-27 |
| `CMSG_REPAIR_ITEM` | 0x3F0089 | npc GUID, item GUID, 1-bit guild-bank flag flushed | `RepairItem::Read`, ItemPackets.cpp:126-131 |

Three facts decide the flow:

* **A vendor has no direct open packet.** Classic opens vendors and trainers only through a gossip option
  (Player.cpp:15160-15181): the core adds "Let me browse your goods." ("Let me browse your goods.", Player.cpp:15180)
  when the menu lacks a Vendor option, and selecting it sends `SMSG_GOSSIP_COMPLETE` and
  `SMSG_NPC_INTERACTION_OPEN_RESULT` (Merchant) — not the list (Player.cpp:15286-15300).
* **The list needs a second ask.** For a vendor without the gossip flag the first `CMSG_LIST_INVENTORY` only starts
  the interaction and answers the open result; the second returns `SMSG_VENDOR_INVENTORY`
  (ItemHandler.cpp:619-645, F2). `vendorOpen` therefore sends at most two `CMSG_LIST_INVENTORY`s, 5 s each: one to
  open (or none, when the gossip option did) and one to list.
* **A purchase names the row by `Muid`, which is the row's slot +1** (`item.MuID = slot + 1`,
  ItemHandler.cpp:600), and the character's own GUID as `ContainerGUID` means the backpack
  (ItemHandler.cpp:589-593). The step sends a `NULL_SLOT` (255) destination so the server picks the free slot
  (Player.cpp:25072-25074, :25264), and the push result names where it went.

### The server's answers

| Packet | Wire | Body | Core |
|---|---|---|---|
| `SMSG_VENDOR_INVENTORY` | 0x47005C | vendor GUID, reason i32, count u32, then per row: price u64, MuID u32, type i32, stack count i32, quantity i32, extended cost i32, failed condition i32, ItemInstance, then 3 flag bits flushed | `VendorInventory::Write`, NPCPackets.cpp:154-164; `VendorItem`, :137-152 |
| `SMSG_BUY_SUCCEEDED` | 0x470172 | vendor GUID, MuID u32, new quantity i32, quantity bought u32 | ItemPackets.cpp:40-48 |
| `SMSG_BUY_FAILED` | 0x470173 | vendor GUID, MuID u32, reason i32 | ItemPackets.cpp:50-56 |
| `SMSG_SELL_RESPONSE` | 0x470171 | vendor GUID, count u32, reason i32, item GUIDs | ItemPackets.cpp:249-257 |

The failing case is `TestParseVendorItemBitsAfterItemInstance`: a row's three flag bits come after the row's whole
`ItemInstance` — the item id, its seven-bit modifier list (flushed) and its optional bonus block — and are flushed
once per row. A reader that takes the bits for part of the modifier list's byte, or reads them before the item,
loses the next row's price.

A sale is **silent** when it worked (F11): the item's object moves into a buyback slot 72-83 and the only
`SMSG_SELL_RESPONSE` is an error (ItemPackets.cpp:249, Player::SendSellError, Player.cpp:14188-14197). A repair sends
no packet at all: its evidence is the coinage the server took and the durability of the item updates (NPCHandler.cpp:
421-461).

### The act steps

| Step | Sends | Reads back |
|---|---|---|
| `vendor` | selection, then the gossip option (or `CMSG_LIST_INVENTORY`), then the list ask | one line per row (`vendor item muid 2: entry 118, 250 copper for 5, stock 3`) and the step line |
| `buy entry=<id> n=<count>` / `buy muid=<row> n=` | `CMSG_BUY_ITEM` | `SMSG_BUY_SUCCEEDED`/`SMSG_BUY_FAILED` by MuID, then the push result's slot; the coinage as an estimate |
| `sell entry=<id> [n=]` / `sell slot=<pos> [n=]` | `CMSG_SELL_ITEM` | the item leaving the carried rows for a buyback slot, or its count falling by `n`; a refusal names the item |
| `sell-junk` | `CMSG_SELL_ALL_JUNK_ITEMS` | the rows that left (the sweep answers nothing); a refusal fails it |
| `buyback slot=<72-83>` | `CMSG_BUY_BACK_ITEM` | the item coming back into the carried rows, its new place; a refusal is a sell response |
| `repair` | `CMSG_REPAIR_ITEM` (empty item GUID = all) | the durability that rose and the coinage that fell |

A money change is printed as `est.` (`1000 -> 995 copper (est., 5 spent)`): the coinage arrives in the same burst as
the slot updates (items.Tracker), and the step reads it as soon as the purchase's own answer came, so the number is
what the client had decoded at that moment, not a value the server named.

The `vendor` step needs the current NPC from an `npc` step, checks the vendor flag (any of the five vendor bits,
UnitDefines.h:327-331) before sending anything, and keeps the list on the act state, so `buy` cannot run against a
vendor nobody opened. `repair` checks the repair flag (UnitDefines.h:332) instead.

## R5: the quests

### The client packets

| Packet | Wire | Body | Core |
|---|---|---|---|
| `CMSG_QUEST_GIVER_STATUS_QUERY` | 0x3F0033 | the giver's packed GUID | `QuestGiverStatusQuery::Read`, QuestPackets.cpp:44-47 |
| `CMSG_QUEST_GIVER_HELLO` | 0x3F002D | the giver's packed GUID | R0's `npc.BuildQuestGiverHello` (QuestGiverHello::Read, QuestPackets.cpp:69-72) |
| `CMSG_QUEST_GIVER_QUERY_QUEST` | 0x3F002E | giver GUID, quest id, respond-to-giver bit flushed | `QuestGiverQueryQuest::Read`, QuestPackets.cpp:600-605 |
| `CMSG_QUEST_GIVER_ACCEPT_QUEST` | 0x3F002F | giver GUID, quest id, start-cheat bit flushed | `QuestGiverAcceptQuest::Read`, QuestPackets.cpp:607-612 |
| `CMSG_QUEST_GIVER_COMPLETE_QUEST` | 0x3F0030 | giver GUID, quest id, from-script bit flushed | `QuestGiverCompleteQuest::Read`, QuestPackets.cpp:466-471 |
| `CMSG_QUEST_GIVER_REQUEST_REWARD` | 0x3F0032 | giver GUID, quest id | `QuestGiverRequestReward::Read`, QuestPackets.cpp:594-598 |
| `CMSG_QUEST_GIVER_CHOOSE_REWARD` | 0x3F0031 | giver GUID, quest id, QuestChoiceItem | `QuestGiverChooseReward::Read`, QuestPackets.cpp:443-448; the choice at :304-327 |
| `CMSG_QUEST_GIVER_CLOSE_QUEST` | 0x3F00EF | the quest id only | `QuestGiverCloseQuest::Read`, QuestPackets.cpp:473-476 |
| `CMSG_QUEST_LOG_REMOVE_QUEST` | 0x3F00D3 | the u8 log slot | `QuestLogRemoveQuest::Read`, QuestPackets.cpp:614-617 |

The reward choice is the round's shape-sensitive packet: two `LootItemType` bits and the context bit come first — the
server's reader resets its bit position before them (QuestPackets.cpp:316-321) — then the item's `ItemInstance`
(whose leading int32 flushes those three bits into their own byte), then the quantity, then the optional context
as bytes. **`TestChooseRewardBitsBeforeItemInstance`** is the failing case: a builder that writes the item first,
or the bits after it, cannot be read back by the core's own reader.

### The server's answers

| Packet | Wire | Content | Core |
|---|---|---|---|
| `SMSG_QUEST_GIVER_STATUS` | 0x67001B | giver GUID, uint64 mask | `QuestGiverStatus::Write`, QuestPackets.cpp:49-55; `QuestGiverStatus`, QuestDef.h:154-205 |
| `SMSG_QUEST_GIVER_QUEST_LIST_MESSAGE` | 0x67001A | giver, greet emote, quests, 11-bit greeting | `QuestGiverQuestListMessage::Write`, QuestPackets.cpp:619-635; `ClientGossipText`, NPCPackets.cpp:74-92 |
| `SMSG_QUEST_GIVER_QUEST_DETAILS` | 0x670012 | the details block | `QuestGiverQuestDetails::Write`, QuestPackets.cpp:478-543 |
| `SMSG_QUEST_GIVER_REQUEST_ITEMS` | 0x670013 | what a turn-in wants | `QuestGiverRequestItems::Write`, QuestPackets.cpp:546-592 |
| `SMSG_QUEST_GIVER_OFFER_REWARD_MESSAGE` | 0x670014 | the reward screen | QuestPackets.cpp:411-441, `QuestGiverOfferReward` :387-409, `QuestRewards` :342-385 |
| `SMSG_QUEST_GIVER_QUEST_COMPLETE` | 0x670003 | the rewards handed out | `QuestGiverQuestComplete::Write`, QuestPackets.cpp:450-464 |
| `SMSG_QUEST_GIVER_INVALID_QUEST` | 0x670005 | the reason and text | QuestPackets.cpp:684-696 |
| `SMSG_QUEST_GIVER_QUEST_FAILED` | 0x670006 | quest id, reason | QuestPackets.cpp:705-711 |
| `SMSG_QUEST_UPDATE_ADD_CREDIT` | 0x67000C | victim, quest, objective, count/required | QuestPackets.cpp:263-273 |
| `SMSG_QUEST_UPDATE_ADD_CREDIT_SIMPLE` | 0x67000D | quest, objective, type | QuestPackets.cpp:275-282 |
| `SMSG_QUEST_UPDATE_COMPLETE` | 0x670009 | quest id, one bit | QuestPackets.cpp:637-644 |
| `SMSG_QUEST_LOG_FULL` | 0x670007 | nothing (0 bytes) | QuestPackets.h:618-624 |

Four layouts decide the parsers, and each is a trap for a plausible reading:

* **The details' strings share one bit block.** The seven lengths (title 9, description 12, log description 12,
  portrait giver text 10, portrait giver name 8, portrait turn-in text 10, portrait turn-in name 8) and the six flag
  bits are written into one block, then one flush, then the bytes (QuestPackets.cpp:522-538). A reader that reads one
  length, flushes and reads its text before the next length loses the next length's bits. The same shape recurs in
  the request-items screen (two lengths) and the reward screen (six).
  **`TestQuestDetailsClassicInts`** is the second failing case: it pins the details' fixed head — two packed GUIDs,
  then the int32 run (quest id, package, four portraits), then the sixteen flag bytes — and the objectives and
  conditional texts before that one bit block. (F8's two extra Classic int32 are in
  `SMSG_QUERY_QUEST_INFO_RESPONSE`, QuestPackets.cpp:89-96, a packet no verb of this round sends; the details writer
  is the pin's own and matches retail, so the test pins the classic reading of the details: the four-uint32 flag
  block and the int32 order.)
* **The request-items screen writes `QuestGiverCreatureID` twice** — before the quest fields and again after the two
  flag bits (QuestPackets.cpp:556, :582). A reader that skips the second one shifts every conditional text.
* **The reward screen's `QuestRewards` always carries six choices, four items, five reputations and four currencies**,
  filled or not (QuestDef.h:49-54; `Quest::BuildQuestRewards`, QuestDef.cpp:527-600). A reader that follows the
  `ChoiceItemCount` field instead of the fixed arrays misreads every screen whose choices are fewer than six.
* **`SMSG_QUEST_GIVER_QUEST_COMPLETE` never flushes its four flag bits** (QuestPackets.cpp:461-464): the core's own
  packet ends after the item. The parser accepts both shapes and `Complete.HasFlags` says which arrived.

### The log model

`quest.Log` holds the slots the login create block carried (`items.Inventory.QuestLog`, i.e. PlayerData's 175-slot
array) and the changes this round sees. An accept takes the first slot whose quest id is zero (Player::FindQuestSlot,
Player.cpp:16150) — the server sends no accept answer, so the client's own record is what makes the next step
possible, and a relog or map change re-seeds it from the create block. A credit names the objective's **ObjectID**,
not its progress index, so it lands in `Progress[i]` only when the quest's details were parsed and named that
objective; otherwise the credit is reported and kept apart rather than guessed into a slot. A completion notice fills
the progress and marks the slot complete; a failure marks it failed; a completion removes it.

### The act steps

| Step | Sends | Reads back |
|---|---|---|
| `quest-status` | `CMSG_QUEST_GIVER_STATUS_QUERY` | the uint64 mask, named (`quest`, `reward`, ...) |
| `quests` | nothing | the log: slot, quest, state, every credit |
| `quest-accept id=` | `CMSG_QUEST_GIVER_ACCEPT_QUEST` | a refusal (`INVALID_QUEST`, a failed quest, a full log) within 2 s; on silence the quest is recorded in the first free slot |
| `quest-progress [id=]` | nothing | the log's state and credits |
| `quest-complete id=` | `CMSG_QUEST_GIVER_COMPLETE_QUEST`, then `CMSG_QUEST_GIVER_REQUEST_REWARD` when the screen says ready | the request-items screen (its wants, or its "not ready" status) and/or the reward screen (money, experience, every choice) |
| `quest-reward id= choice=entry` | `CMSG_QUEST_GIVER_CHOOSE_REWARD` | `SMSG_QUEST_GIVER_QUEST_COMPLETE` and the reward's push result; the quest leaves the log (the completion does that) |
| `quest-abandon id=` | `CMSG_QUEST_LOG_REMOVE_QUEST` (the slot) | nothing; the log drops it |

The request-items screen's `StatusFlags` is 0xFF when the quest can be handed in and 0xFD when it cannot
(GossipDef.cpp:723); only 0xFF sends the reward request, because a real client would not press the button on an
unfinished quest.

### Where the round deviates from the brief, and why

`QuestListMessage`'s parser lives in the **quest** package (`quest.ParseQuestList`, all six fields of every
`ClientGossipText` and the greeting) and the session's NPC dispatch uses it; round R0's duplicate parser in `npc` was
deleted in the same round rather than kept beside it (the cutover is one line in `world/npc.go`, and R0's act tests
still pass). `CMSG_QUEST_GIVER_HELLO` stays R0's `npc.BuildQuestGiverHello`: it is already built, tested and used by
the `open` step, and a second builder would be a second convention.

## Live checks for the lead's queue

No live run was made in this round (the fake server and the private captures are the evidence below). One test
character on the GM account, one `forever act` run per stage, `.gm on` for the setup lines only, and the server's
own rows read between the stages.

### R3, the vendor

1. **Open.** `.additem` two items to sell; `npc entry=<a vendor>`; `vendor`: the step line names the row count, and
   the packet order is seen in the run log. A vendor with a gossip menu is opened by its option (one
   `CMSG_LIST_INVENTORY`); a vendor without one takes two (F2).
2. **Buy.** `buy entry=<food> n=5`: the line names `muid`, the entry and the count; `inv` shows the stack and the
   coinage fell by about the price (`est.`). A second buy of the same row at a stock limit shows `not_enough_money`
   or the sold-out name.
3. **Sell, partly and whole.** `sell entry=<x> n=1` shows the remaining count; `sell entry=<x>` shows
   `in buyback slot <n>`; `inv` no longer lists it.
4. **Sell junk.** `.additem` a poor-quality item, `sell-junk`: the line counts it and the coinage rose.
5. **Buyback.** `buyback slot=<n>` from step 3: the item is back in the carried rows at a new slot.
6. **Repair.** A worn item, `repair`: the coinage fell and the durability rose (the `inv` rows carry it).
7. **A non-vendor.** `npc entry=<an innkeeper>`; `vendor` fails with "not a vendor" and sends nothing.
8. **Relog.** A relog's `inv` matches the server's `character_inventory` joined to `item_instance`.

### R5, the quests

1. **Status and list.** `npc entry=<a starting-zone giver>`; `open` prints the quest list; `quest-status` names the
   mask (an exclamation-mark quest is `quest`).
2. **Accept.** `quest-accept id=<a kill quest>` records it in a slot; `quests` shows the slot and `incomplete`.
   A quest the character cannot take fails with the server's reason (`failed_low_level`, ...).
3. **Progress.** Kill the required creatures: one `quest 123 objective <id>: 2/4` line per credit, and
   `quest-progress id=123` shows the same numbers.
4. **Turn-in.** `quest-complete id=123`: either the reward screen directly or the wants list followed by the reward
   screen; a quest whose items are missing reads the wants list and fails with `not ready`.
5. **Reward.** `quest-reward id=123 choice=<an entry from the screen>`: the completion line (xp, copper, item), an
   `item push` line for the reward, and `inv` shows it; the quest left the log. A choice the screen did not offer is
   refused before any packet.
6. **Abandon.** `quest-accept id=<a second quest>`, then `quest-abandon id=`: the slot frees, and the server's
   `character_queststatus` no longer has the quest.
7. **Relog.** `quests` after a relog matches the server's quest rows; a quest completed while offline appears with
   its real state.

## Gate lines

(filled in)
