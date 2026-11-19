# Forever loot, group rolls and game objects, round LOOT-1 notes

Scope: from the object and combat rounds to "the client can open a corpse's or a chest's loot window, read it, vote
on a group roll, take money and items, release, and use a game object and see it open or close". Code:
`internal/forever/loot` (every packet), `internal/forever/world` (the session's loot state, `forever loot-check` and
`forever object-use`) and `internal/forever/wire` (the opcode numbers). No live run was made in this round: the
layouts are checked against the two private packet logs of the round, and the flows against a fake world server that
speaks the core's own bytes. The live steps are at the end.

## Status

| Part | State |
|---|---|
| window (items, money, currencies, slots, roll flags) | parsed exactly; every window in the private logs consumes to its last byte |
| money and coin removal | parsed; taken with `CMSG_LOOT_MONEY` |
| take and release | sent and their answers read; a unit's window can only be opened within the server's loot distance |
| group rolls | start, vote, broadcast, winner, all-passed and completion parsed; a vote is only sent when asked and only inside the start's own mask |
| game objects | use and report-use sent; the open/closed state is read from the object's value update |
| live run | **not made in this round**; the steps are below |

## The packed GUID of this build

This core is a master code base serving a 1.60.1 client, and its GUIDs are 128-bit. The packed form is a **uint16**
mask followed by one byte per set mask bit, in ascending byte order (`ObjectGuid.cpp:1087-1111` write,
`:1113-1134` read). `wire.WritePackedGUID`/`wire.ReadPackedGUID` already own that form (the objects round introduced
it); every loot packet below uses it. A client that writes the old 8-bit mask desynchronises every following field.

## The loot window

`SMSG_LOOT_RESPONSE` (`LootResponse::Write`, `LootPackets.cpp:53-77`):

| field | type | note |
|---|---|---|
| owner | packed GUID | the creature or game object the window belongs to |
| loot object | packed GUID | the `Loot` instance; `CMSG_LOOT_ITEM` and the rolls name **this**, not the owner |
| failure reason | `uint8` | `LootError` (`Loot.h:137-153`); its default is 17 and it is sent even on a window, so only `Acquired` decides |
| acquire reason | `uint8` | `GetLootTypeForClient(loot_type)` (`Loot.h:116-134`) |
| loot method | `uint8` | `LootMethod` (`Loot.h:87-96`) |
| threshold | `uint8` | quality threshold |
| coins | `uint32` | the window's copper |
| item count | `uint32` | |
| currency count | `uint32` | |
| items | | `LootItemData`, below |
| currencies | | `uint32` id, `uint32` quantity, `uint8` list id, 3-bit UI type, flush |
| acquired, AE looting, suppress error | 3 bits | flush after |

`LootItemData` is shared by the window, the roll start, the roll broadcast and the roll result
(`LootPackets.cpp:25-37`):

| field | type |
|---|---|
| type | 2 bits (`LootItemType`: item 0, currency 1, tracking quest 2) |
| UI type | 3 bits (`LootSlotType`, `Loot.h:156-163`: allow loot 0, roll ongoing 1, locked 2, master 3, owner 4) |
| can trade to tap list | 1 bit, then a flush |
| quantity | `uint32` |
| LootItemType | `uint8` (the core writes it as 0 in a window) |
| list id | `uint8` — the slot every take and every vote names |
| item | `ItemInstance` |

`ItemInstance` (`ItemPacketsCommon.cpp:173-186`) is the item id, a 7-bit modifier count with a flush, the modifiers
(`uint8` type and `int32` value each), a bit for the item-bonus block with a flush and, when set, its context byte
and `uint32` bonus ids (`ItemPacketsCommon.cpp:109-117`). The window of this core never sets a modifier or a bonus
block, but the parser walks both because a later round's DB or a chest's item can.

The other window packets:

* `SMSG_LOOT_REMOVED` (`LootPackets.cpp:105-111`): owner, loot, `uint8` list id.
* `SMSG_LOOT_RELEASE` (`LootPackets.cpp:152-158`): loot, player.
* `SMSG_LOOT_RELEASE_ALL`: no payload (`LootPackets.h:159-164`); the server closes every window at once.
* `SMSG_COIN_REMOVED` (`LootPackets.cpp:128-133`): the loot GUID; sent to every player looting when the money goes.
* `SMSG_LOOT_MONEY_NOTIFY` (`LootPackets.cpp:118-126`): `uint64` money, `uint64` modifier, one `SoleLooter` bit.
* `SMSG_LOOT_LIST` (`LootPackets.cpp:160-178`): owner, loot, two presence bits, then the master looter and the
  round-robin winner when their bits are set.

## The group roll packets

`SMSG_START_LOOT_ROLL` (`LootPackets.cpp:185-200`): the loot GUID, `int32` map id, an item block (its UI type is
`ROLL_ONGOING`), `uint32` roll time in ms (`LOOT_ROLL_TIMEOUT` is one minute, `Loot.h:34`; the logs show 60000), the
`uint8` valid-vote mask, the five `uint32` ineligibility reasons (`LootRollIneligibilityReason`, `Loot.h:165-174`),
the `uint8` loot method and the `int32` dungeon encounter.

The valid-vote mask is `RollMask` (`Loot.h:74-85`): pass 0x01, need 0x02, greed 0x04, disenchant 0x08, transmogrify
0x10. `LootRoll::TryToStart` clears need for an item that can only be rolled greed and always clears disenchant
unless the group's best enchanter has the skill (`Loot.cpp:501-506`); `SendStartRoll` also clears need for a
need-before-greed roll the player cannot use the item in (`:298-300`). A vote outside the mask would be refused by
the server, so the client checks the mask itself and stays silent instead (the command logs why). **The client never
elects need**: `--roll` names one vote for the run, and with no `--roll` no vote is sent at all.

The vote is `CMSG_LOOT_ROLL` (`LootPackets.cpp:141-150`): loot GUID, `uint8` list id, `uint8` vote (pass 0, need 1,
greed 2, disenchant 3; `RollType`, `Loot.h:53-61`).

The answers:

* `SMSG_LOOT_ROLL` (`LootPackets.cpp:202-219`): loot, player, item, `int32` number, `uint8` vote, `int32` encounter,
  two bits (autopassed, off-spec). The number is -1 for a pass or a greed vote at selection time and the real roll
  only in the winner packet, which is why the report keeps the start, the broadcasts and the result apart.
* `SMSG_LOOT_ROLL_WON` (`LootPackets.cpp:221-236`): loot, item, winner, number, vote, encounter, `MainSpec` bit.
* `SMSG_LOOT_ALL_PASSED` (`LootPackets.cpp:238-245`): loot, item, encounter.
* `SMSG_LOOT_ROLLS_COMPLETE` (`LootPackets.cpp:247-254`): loot, list id, encounter. This core never sends it (no
  caller in `src/server/game`), but the parser accepts it as a roll's end.

`LootRoll::Finish` (`Loot.cpp:684-737`) sends the winner (or all-passed) at the end, and `UpdateRoll` (`:568-578`)
ends a roll when every player voted or its time ran out, so the command waits for the start's own time plus three
seconds and reports a roll that never ended as exactly that.

## The client packets

* `CMSG_LOOT_UNIT`: one packed GUID, the creature (`LootPackets.cpp:48-51`). The handler refuses a target that is
  not a creature or is out of the 30-yard AE-loot distance, and answers a corpse with nothing to loot with
  **silence** (`LootHandler.cpp:213-259`).
* `CMSG_LOOT_ITEM` (`LootPackets.cpp:79-90`): a `uint32` count, then per request the **loot** GUID and the list id,
  then one `IsSoftInteract` bit. The core looks the GUID up in the player's open loot view, which is keyed by the
  loot object's GUID (`LootHandler.cpp:77-104`).
* `CMSG_LOOT_MONEY`: one bit (`LootPackets.cpp:113-116`).
* `CMSG_LOOT_RELEASE`: the **owner** GUID (`LootPackets.cpp:136-139`), because `GetLootByWorldObjectGUID` compares
  each open loot's owner (`Player.cpp:9927-9934`).
* `CMSG_GAME_OBJ_USE` and `CMSG_GAME_OBJ_REPORT_USE`: one packed GUID each (`GameObjectPackets.cpp:21-29`).

## The game object path

`HandleGameObjectUseOpcode` resolves the object with `GetGameObjectIfCanInteractWith` and calls `Use`
(`SpellHandler.cpp:201-211`). The check is the client's own interaction range: 5 yards
(`ObjectDefines.h:24`, `GameObject.cpp:4405-4437`) and a "Point" icon is refused; the command walks to 3.2 yards
first, inside both rules.

Doors and buttons (`GAMEOBJECT_TYPE_DOOR`, `BUTTON`) go through `UseDoorOrButton` (`GameObject.cpp:2497-2508`):
`SwitchDoorOrButton` sets `GO_FLAG_IN_USE` and flips the state — ready (1, closed) to active (0, open) and back
(`:2677-2688`); `SetLootState` does not touch the state field for a door. `SetGoState` writes
`GameObjectData::State` (`:4112-4122`), which travels to nearby players as a values update; the client's own state
report comes from the object model the objects round already decodes (classic bit 17, `UpdateFields.cpp:7581-7585`).

A chest (`GAMEOBJECT_TYPE_CHEST`) builds or reuses its loot, sets `GO_ACTIVATED` and sends the window
(`GameObject.cpp:2740-2872`); its state stays ready while it is open, so the window, not the state, is the answer.
A quest object or goober may show a page or cast instead of a window; the command reports "no window, the state did
not change" for those.

## What the checks proved

Against the private packet logs of this round (both read with the same capture reader as rounds 3-6, the paths only
in the environment):

| packet | result |
|---|---|
| `SMSG_LOOT_RESPONSE` | every one parsed, zero trailing bytes (45 in the group-loot log, 11 in the game-object log) |
| `SMSG_LOOT_REMOVED`, `SMSG_LOOT_RELEASE`, `SMSG_LOOT_MONEY_NOTIFY`, `SMSG_COIN_REMOVED`, `SMSG_LOOT_LIST` | every one parsed, zero trailing bytes |
| `SMSG_START_LOOT_ROLL`, `SMSG_LOOT_ROLL`, `SMSG_LOOT_ROLL_WON` | every one parsed, zero trailing bytes (the group-loot log's three rolls: need, greed and a roll nobody clicked) |
| `CMSG_LOOT_UNIT`, `CMSG_LOOT_ITEM`, `CMSG_LOOT_MONEY`, `CMSG_LOOT_RELEASE`, `CMSG_LOOT_ROLL` | every one consumed exactly |
| `CMSG_GAME_OBJ_USE`, `CMSG_GAME_OBJ_REPORT_USE` | every one is one packed GUID and nothing else |

The roll votes in the log are exactly the shape the command sends (need on the first item, greed on the second,
none on the third), and the client's own take batches carry the loot object GUID with 1-5 slots, the release carries
the owner. The game-object log also shows the real client's order: use, then report-use in the same tick, then (for a
chest) the window; the command sends the same two packets back to back for every use.

## What the fake world server learned

`fake_loot_test.go` speaks the bytes above on the real sockets: a corpse or a game object create, the window, the
roll start, the broadcasts and the winner (or an immediate all-passed), the money notice and coin removal, the
removal per item, the release, and a door's state update. It checks every GUID the client sends (the open, every
item request, the vote, the release) and fails the run on a mismatch or an unexpected packet. The scenarios cover:

* a corpse's window with two items and copper;
* a roll with `--roll need` (the fake answers the vote, the report names this session as the winner with the number);
* a roll with no `--roll` (no vote is sent, the report says everybody passed);
* a roll whose mask has need clear with `--roll need` (no vote is sent, and the report names the mask);
* a chest: use, report-use, window, item, release;
* a door: use, report-use, state 1 (ready/closed) before and 0 (active/open) after, no window;
* a living creature with `--entry`: the fight round's attack, death, then the loot;
* link death in each wait: the window, the use, the money answer, the item answer, the release answer and the roll
  result — every run ends with the lost link in under a second and none waits out its own timeout.

The committed fixtures of earlier rounds are unchanged; this round's packet bytes are all built in the tests from
the core's writers, so no capture byte reached the repository.

## Commands

`forever loot-check`:

```
forever-agent forever loot-check --rest H --rpc H --ca F --account A --character NAME \
    [--entry N] [--roll need|greed|pass] [--take N] [--money=false] [--range Y] [--stay D] [--fight-for D]
```

Settle, pick the nearest target (dead creature of the entry, a chest when no entry, a living creature of the entry
to kill first), walk into interaction range, open, read, vote (only if asked and allowed), take the money, take up to
`--take` items the window allows (`allow loot` and `owner` slots; a roll-ongoing, locked or master slot is not
taken), release. It prints one line per step and a final summary. Exit codes: 0 done, the login codes, the
disconnected and dead codes, the world code for anything else.

`forever object-use --entry N`: settle, find the nearest object of that entry, walk to it, print state and flags,
use, print state and flags after the update, release a window it opened without taking anything.

## What is approximated, and what is not done

* Only the window's `allow loot` and `owner` slots are taken. Master-loot giving, round-robin ownership and
  free-for-all per-player rows are **read** (their slot types are named) but not acted on; group loot and
  need-before-greed are the methods this round drives.
* Currency rows are parsed and counted but not taken: this core's game-object and creature loot fills items, and a
  currency row would need the currency packet path, which no round has.
* Disenchant and transmogrify votes are named and their mask bits known, but never offered: the command's choices
  are the three Classic ones.
* The item instance's modifier and bonus blocks are walked; nothing in this core's loot tables sets them yet.
* The window's answer to a take is the removal packet; the item-push packet is counted by the session's packet
  counter but not parsed, because the removal is the authoritative "left the window" (and the core sends no push
  for a loot take that already reports the item elsewhere).
* The interaction walk stops at 3.2 yards (inside the server's 5). A locked door or a chest with a lock is not
  picked: the client has no lockpicking spell path in this round, and the server answers with no window.
* A roll that the server never ends (no votes, and the core's own timer somehow not firing) is reported as ended
  "no result before the client stopped waiting" after the start's time plus three seconds; the window is released
  anyway, which is what the client would do when it closes the roll frame.
* The corpse must be visible to the client (the objects round's update model) and within 30 yards for
  `CMSG_LOOT_UNIT`; the command walks to it first, so a server-side "too far" is reported rather than retried.

## Live steps (not run in this round)

The server's own log lines are the oracle: `Loot::LogTable` prints one `[loot_table]` line when a window is first
opened and `Loot::LogGiven` one `[loot_given]` line per item (`Loot.cpp:843-868`), both on the `playerbot.v2`
logger, with the source's entry, the map, the method, the gold and the items, then the receiver and the item.

1. **A corpse.** With the character in a starting zone, find a low-level hostile creature's entry (the object model
   prints nearest units and entries): `forever-agent forever loot-check ... --entry E --money`. It kills it, the
   window's copper and items are taken, the release is confirmed, and the server log shows one `[loot_table]` and
   one `[loot_given]` per item with this character as the receiver.
2. **A chest.** Find a chest's game-object entry near the character (a starting-area chest, or a quest object that
   gives loot) and run `--entry E`. The server log shows the table and the given lines; the summary shows the same
   items and copper, and the release.
3. **A group roll.** With two characters grouped and the group's loot method group loot or need-before-greed, kill a
   creature whose drop is above the threshold (a boss drop in a dungeon the party is already in): run
   `loot-check --entry <boss entry> --roll greed` on one client and `--roll pass` on the other. The first report
   must name both votes in its roll rows, the winner and its number, and the loser must lose the item (the server
   `[loot_given]` line names the receiver). A second run with no `--roll` must send no vote at all: the roll ends
   on its own timer and the report says everybody passed.
4. **A door.** In an instance with a door or lever of a known entry, stand near it and run
   `forever-agent forever object-use ... --entry E`: the summary must print ready (closed) before and active (open)
   after, with the flags changing, and a second run must flip it back. A door that the server opens by a script (a
   cannon blast) changes without a use: the command reports the state it finds and does not send a use for it if it
   is already open.
5. **A quest object.** A usable quest object that opens a page or a gossip menu answers with no window; the command
   must report that and not hang, and the object's state must be unchanged.
