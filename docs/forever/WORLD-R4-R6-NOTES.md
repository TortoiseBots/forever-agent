# Forever trainer and flight paths, rounds R4 and R6 notes

Scope: R4, the trainer from the player's side (list and learn) and R6, the flight paths (node status, the node window,
one flight and the landing watch). Code: `internal/forever/npc` (`trainer.go`, the trainer's wire layer),
`internal/forever/taxi` (the flight wire layer), `internal/forever/world` (`trainer.go` and `trainer_act.go`,
`taxi.go` and `taxi_act.go` the session glue and the act steps), `internal/forever/wire` (both rounds' opcodes),
`cmd/forever-agent` (no new flags: the verbs run through `forever act`, and `serve`'s `act` method reaches them
unchanged).

Everything below was checked in ForeverCore at d02645dd21 (`git show d02645dd21:<path>`). Fixtures are hand-made from
the core's writers and readers; no capture byte is in the repository, and the capture checks print counts only.

Status: both rounds are implemented and tested against the fake world, one commit each. **No live run yet**: the live
checks for the lead's queue are at the end.

## R4: the trainer

### The client packets

| Packet | Wire | Body | Core |
|---|---|---|---|
| `CMSG_TRAINER_LIST` | 0x3F0045 | the trainer's packed GUID | `Hello::Read`, NPCPackets.cpp:94-97; `HandleTrainerListOpcode`, NPCHandler.cpp:97-110 |
| `CMSG_TRAINER_BUY_SPELL` | 0x3F0046 | trainer GUID, int32 `TrainerID`, int32 spell id | `TrainerBuySpell::Read`, NPCPackets.cpp:253-263 |

A trainer is opened the Classic way: its gossip menu's Trainer option (`GossipOptionNpc::Trainer`), which the core
adds as "Train me." when the menu lacks it and the trainer's class mask fits the character (Player.cpp:15160-15181);
selecting it sends the list (Player.cpp:15304-15312). `CMSG_TRAINER_LIST` is the Hello-shaped ask a trainer without a
gossip menu can answer, and the step sends it only for such a creature. A purchase is refused unless this session
started a trainer interaction with that GUID under that `TrainerID` (NPCHandler.cpp:134-152), so `learn` names the id
from the list the step read.

### The server's answers

| Packet | Wire | Body | Core |
|---|---|---|---|
| `SMSG_TRAINER_LIST` | 0x47018A | trainer GUID (packed), int8 type, uint32 `TrainerID`, uint32 count, then 33 bytes per spell, then an 11-bit greeting length, a flush and the greeting | `TrainerList::Write`, NPCPackets.cpp:158-194 |
| `SMSG_TRAINER_BUY_FAILED` | 0x47018B | trainer GUID (packed), int32 spell, int32 reason | `TrainerBuyFailed::Write`, NPCPackets.cpp:260-268 |
| `SMSG_LEARNED_SPELLS` | 0x690040 | uint32 count, uint32 specialization, int32 minimum action-bar slot, then per spell an int32 and a flushed four-bit block with its optional int32s, then two trailing bits | `LearnedSpells::Write` and its `LearnedSpellInfo` reader, SpellPackets.cpp:482-519 |

One row is 33 bytes: `SpellID` int32, the writer's constant 0 (int32), `MoneyCost` uint32, `ReqLevel` uint8,
`ReqSkillLine` uint32, `ReqSkillRank` uint32 and `ReqAbility` as three int32 (NPCPackets.h:191-200,
NPCPackets.cpp:174-186). The writer does **not** write the row's usability byte, because the client derives it (a
known spell is used); the parser therefore reports no state and the print names the spell, price and requirements
only.

The failing case is **`TestTrainerListThirtyThreeBytesPerSpell`**: two spells, and the second spell's id must parse
after exactly 33 bytes of the first. A reader that walks the retail 38-byte row (or reads the 11-bit greeting before
the rows) loses it. A second fixture pins the whole body's length (the packed GUID, the type, the id, the count, two
rows and the flushed 11-bit length), so a row size change fails by arithmetic too.

The purchase's success is only a learned-spell answer that names the spell; a refusal is
`SMSG_TRAINER_BUY_FAILED` with the core's two reasons (`unavailable`, `not_enough_money`, Trainer.h:47-51). A learned
spell joins the session's spellbook (`session.known` and the report's copy), which is what the mount and cast checks
read (`SMSG_SEND_KNOWN_SPELLS` still replaces the list when it arrives).

### The act steps

| Step | Sends | Reads back |
|---|---|---|
| `trainer` | selection, then talk and select the Trainer option (or `CMSG_TRAINER_LIST` for a trainer without a gossip menu) | the list: the greeting, one line per spell (`trainer spell 1234: 250 copper, level 5, skill 171/75`), then the step line with the entry, trainer id and count |
| `learn spell=<id>` | `CMSG_TRAINER_BUY_SPELL` with the list's `TrainerID` | `SMSG_LEARNED_SPELLS` naming the spell within 3 s (success, and the spell enters the spellbook) or `SMSG_TRAINER_BUY_FAILED` with its reason (failure) |

`learn` refuses before sending when no `trainer` step opened this NPC's list, when the spell is not on it, or when the
trainer id does not fit the packet's int32. It also refuses a spell the character already knows and a row whose
required level or ability the character does not have, and a row whose price is above the coinage. Those tests fail
closed: a level the model does not hold yet, or an inventory it has not read or has lost a change for, refuses the
purchase instead of skipping the guard. The coinage is the model's own value, set by the login create and by every
later `ActivePlayerData` values update (a purchase, a sale, a quest reward, a GM money change), so a change after login
is seen. After the learned answer the step waits for the following values update's debit, prints the coinage before
and after (`1000 -> 750 copper (est., 250 spent)`), and treats an answer that names the spell without that debit as
not this purchase (another source may have taught it). A new `npc` step clears the list, so a purchase cannot run
against a trainer the script moved away from.

## R6: the flight paths

### The client packets

| Packet | Wire | Body | Core |
|---|---|---|---|
| `CMSG_TAXI_NODE_STATUS_QUERY` | 0x3F0040 | the flight master's packed GUID | `TaxiNodeStatusQuery::Read`, TaxiPackets.cpp:30-33 |
| `CMSG_TAXI_QUERY_AVAILABLE_NODES` | 0x3F0042 | the flight master's packed GUID | `TaxiQueryAvailableNodes::Read`, TaxiPackets.cpp:76-79 |
| `CMSG_ENABLE_TAXI_NODE` | 0x3F0041 | the flight master's packed GUID | `EnableTaxiNode::Read`, TaxiPackets.cpp:70-73 |
| `CMSG_ACTIVATE_TAXI` | 0x3F0043 | vendor GUID, uint32 node, uint32 `GroundMountID` (0), uint32 `FlyingMountID` (0) | `ActivateTaxi::Read`, TaxiPackets.cpp:89-96 |

`EnableTaxiNode` is built and tested because the round's brief names it, but no verb sends it: the recorded flight
flow is talk, select the taxi option, show nodes, activate, reply, with no enable. A capture of a client sending it
decides otherwise in C7.

The window opens through the flight master's Taxinode gossip option (Player.cpp:15287-15289), or — for a flight
master whose menu has no such option, or none at all — through `CMSG_TAXI_QUERY_AVAILABLE_NODES`, which the handler
answers with the same window (TaxiHandler.cpp:72-116). The capture's flight ended with `SMSG_ACTIVATE_TAXI_REPLY`, so
the reply is the answer a step waits for; a reply of 0 only says the server accepted, and the flight itself is the
character's own `UNIT_FLAG_ON_TAXI`.

**A first visit learns the node instead of opening the window.** The core learns a menu's taxi node while it prepares
the menu and returns before sending it (Player.cpp:15036-15043), and the query handler does the same before
`SendTaxiMenu` (TaxiHandler.cpp:84-88): the first hello or query at a flight master whose node is new answers
`SMSG_NEW_TAXI_PATH` and a learned `SMSG_TAXI_NODE_STATUS` and no menu. A player's second click is the second hello.
`taxi-nodes` therefore asks at most twice: the node-status answer to its first ask is the discovery, it says so in a
`taxi learned the flight master's node` line, and the second ask opens the window. That is the only packet pair a
real client sends in this situation; the enable is not needed for it.

### The server's answers

| Packet | Wire | Body | Core |
|---|---|---|---|
| `SMSG_SHOW_TAXI_NODES` | 0x47017A | the window's optional bit and a flush, uint32 land-mask blocks, uint32 use-mask blocks, the optional window (packed GUID, int32 current node), then both masks (eight bytes per block) | `ShowTaxiNodes::Write`, TaxiPackets.cpp:52-74 |
| `SMSG_TAXI_NODE_STATUS` | 0x470129 | flight-master GUID (packed), two status bits, flushed | `TaxiNodeStatus::Write`, TaxiPackets.cpp:38-44 |
| `SMSG_ACTIVATE_TAXI_REPLY` | 0x47012A | four reply bits, flushed | `ActivateTaxiReply::Write`, TaxiPackets.cpp:112-120 |

The failing case is **`TestShowTaxiNodesWindowInfoOptionalBit`**: without the window the masks begin right after the
two uint32 counts (byte 9), so a reader that always reads the GUID shifts both masks by twelve bytes and names no
node. Node ids are 1-based and node N is bit N-1 of its mask (PlayerTaxi.h:53-56); the mask's block count is the
`size()/8` the writer sends, checked against a bound before any allocation.

The reply's four bits are the `ActivateTaxiReply` enum (SharedDefines.h:7161-7176): 0 accepted, 1-12 are the named
refusals (`not_visited`, `not_enough_money`, `too_far_away`, `same_node`, ...). The two status bits are
`TaxiStatus` (SharedDefines.h:7178-7184): `none`, `learned`, `unlearned`, `not_eligible`.

### In flight

The client sends nothing while the character's own `UNIT_FLAG_ON_TAXI` (UnitDefines.h:187) is set: the server flies
the spline and clears the flag when the character lands (FlightPathMovementGenerator.cpp:79, :169 and the teleport to
the destination node's position in `DoFinalize`). The session records the flag's setting and clearing from every
`SMSG_UPDATE_OBJECT` that carries it (not a 50 ms poll of the flag), so a flight short enough to set and clear it
inside one wait is still read, and a flight across maps is followed through the change instead of ending the step: the
`fly` wait tolerates `ErrMapChange` while the others keep refusing it. The wait is bounded at 15 minutes. After the
clearing the step waits up to two seconds for the landing teleport (the core clears the flag and *then* teleports, so
reading the position at the clearing would print the takeoff point), then reports the landing position, the duration,
the distance from the takeoff point, the node, and the coinage the inventory read back.

Every movement verb refuses while the flag is set, or while an accepted flight's landing has not been seen (a flight
the caller's own clock cut short): `goto`, `npc`'s walk, `follow`, serve's `face`, `walk_to`, `walk_path` and
`follow_start`, the fight and the GM walk. The refusal lives on the mover itself (`objects.Mover.Refuse`, called by
every movement packet), so a caller that forgot one path cannot send one; the act verbs also name it in their fail
line. The guard is released by the flag's clearing or by a teleport of the character (the landing's own shape), and it
never stays stuck if one landing update is lost.

The node a flight may take must be one the window marks usable and must not be the current node: a real client picks
from that window, and the server refuses a node it did not offer (`not_visited`) or the current one (`same_node`).
The step refuses that before sending, so a script's failure is the script's, not the server's.

`fly` cannot pre-check the fare: the window carries no price and the client has no taxi DBC, so the only client-side
money test is a character with no copper at all; the server's `SMSG_ACTIVATE_TAXI_REPLY` is the rest. The landing line
prints the coinage before and after, so the fare the first leg took is visible even though it is not predicted.

A flight must fit the run's budget. `forever act` sizes its deadline from the script (`world.ActBudget`): the
baseline of a login and the settle, plus a flight's full ceiling for every `fly` step, so the run's own clock never
cuts a flight mid-air. `serve`'s `act` does the same: a script with a `fly` step gets a default budget of one flight
budget (the 15-minute ceiling and its waits) plus the steps' slack, a caller's `timeout_ms` may be up to that figure
and may not be below what the flights need, and any other figure is refused before anything is sent. The old fixed
caps (60 s by default, 10 minutes at most) are gone from the flight path.

### The `taxi` method and the flight state (forever-agent #107)

`forever serve` answers `taxi` (the old daemon's name, `taxi_serve.go`). `{guid}` alone opens the flight master's
window and answers the current node and both masks' node ids (status `window`). `{guid, node}` takes one flight:
the same window and guards as `fly` (the node must be known and usable and not the current one, no copper at all is
refused, a character already in the air is refused), then one `CMSG_ACTIVATE_TAXI`. The call returns at takeoff
(status `flying`, `on_taxi` true), so the session stays free for `status`, `chat` and the rest while the flight goes
on; `wait_landing` holds it to the landing instead (status `landed` with the position and the landing line). The
old method's `nodes` list (source first) is accepted: its first node must be the flight master's own, its last is the
destination, and the hops in between are the server's.

**There is no express packet.** ForeverCore's `HandleActivateTaxiOpcode` reads one destination node and routes the
hops itself with `TaxiPathGraph::GetCompleteNodeRoute` (TaxiHandler.cpp:207-208, the request layout
TaxiPackets.cpp:67-73), and the opcode table has no `ACTIVATE_TAXI_EXPRESS`. The old multi-hop request is therefore
one `CMSG_ACTIVATE_TAXI` for the last node of the list.

A refusal after the request went out is an answer (`ok` false, `status` `refused`, the reply's name, the `taxi_reply`
event); a call that is wrong before anything is sent is an RPC error.

**Node discovery** is `SMSG_NEW_TAXI_PATH`: one int32 node, sent when a flight master's node is new
(`SendLearnNewTaxiNode`, TaxiHandler.cpp:129-140) or a spell discovers one (TaxiHandler.cpp:153-156,
SpellEffects.cpp:5202). The session keeps the nodes it learned and logs each as a `taxi_node` event. The flight's own
state is `taxi_flight` events (`takeoff`, `landed`, from the character's own `UNIT_FLAG_ON_TAXI` changing) and
`status.on_taxi`, true while the flag is set or an accepted flight's landing has not been seen.

**Mount and dismount around a flight.** The core removes the mount aura itself when a taxi starts
(`ActivateTaxiPathTo`, Player.cpp:24517), so the method sends no dismount before a flight. In the air the model's
mount display is the flight's own: `mount` and `dismount` refuse while `on_taxi` (nothing is sent), and work again
after the landing.

### The act steps

| Step | Sends | Reads back |
|---|---|---|
| `taxi-nodes` | selection, then the taxi gossip option (or `CMSG_TAXI_QUERY_AVAILABLE_NODES`), asked again when the first ask answered the node's discovery | the window: `taxi current node 3`, `taxi known nodes: 1, 3, 5`, `taxi usable nodes: 1, 5` (bounded lists), then the step line with both counts; a first visit adds `taxi learned the flight master's node` |
| `taxi-status` | `CMSG_TAXI_NODE_STATUS_QUERY` | `SMSG_TAXI_NODE_STATUS`: the step line names `learned`, `unlearned`, `not_eligible` or `none` |
| `fly node=<id>` | selection and the gossip or query from `taxi-nodes`, then one `CMSG_ACTIVATE_TAXI` | the refusal's name, or the flight: `ON_TAXI` set, then cleared; `landed at X Y Z after 4m2s, node 5, 310 yd from takeoff` |

### The world summary

`ObjectSummary.OnTaxi` counts the **other** units whose unit flags carry `UNIT_FLAG_ON_TAXI`, and the world summary
prints it when it is non-zero (`world: 2 other unit(s) on a taxi flight`). That is the W2 check that a bot travels by
flight path rather than by teleport: the character itself is not counted, and a bot's flag is the server's own state.

## Documented limits

The round-2 review's remaining lows, kept as limits rather than code changes:

* **The flight's fare is not predicted.** The window carries no price and the client has no taxi DBC, so `fly` can
only refuse a character with zero copper and read the server's reply; the landing line prints the coinage before and
after so the debit is visible after the fact.
* **The flight guard arms when the flag is seen, not when the reply is 0.** `taxiFlight.pending` is set after
`UNIT_FLAG_ON_TAXI` arrives, so a reply of 0 followed by a late flag leaves a short window in which the mover's guard
does not yet refuse movement. The client sends nothing between the request and the reply, so the window is small, but
it is not zero.
* **The landing wait costs two seconds without a teleport.** A path whose last node needs no teleport lands in place;
the step still waits the full `actTaxiLandingWait` for one, and a teleport later than that leaves the mover at the
takeoff point until its own update arrives (the next step then reads the stale position).
* **The target and open helpers are duplicated.** `trainerTarget`/`taxiTarget` repeat `vendorTarget`/`questGiver`'s
reach and flag tests, and `taxiOpen`/`taxiGossipMenu` repeat the talk/select flow `waitActAnswer` offers. Behaviour is
the same; folding them into one helper is maintainability work, not a round fix.

## Where the rounds deviate from the brief, and why

* **`CMSG_ENABLE_TAXI_NODE` has no caller.** The brief lists its builder; the recorded flight flow has no enable, so
  no verb sends one. Keeping the builder and its opcode row means C7 can compare it without a new round.
* **One shared wait helper.** The vendor's and the quests' 50 ms poll helpers were the same function; this round
  replaced them with `actUntil` (`world/act.go`), which also fails a wait on a dead link. The vendor and quest tests
  pass unchanged, and a flight's 15-minute wait cannot outlive its connection.
* **The trainer's usability byte is not parsed.** The 1.60 writer does not write it (NPCPackets.cpp:174-186); the
  client derives it. Reporting a state would mean inventing one.

## Live checks for the lead's queue

No live run was made in this round (the fake server is the evidence above). One test character on the GM account,
one `forever act` run per stage, `.gm on` for the setup lines only, and the server's own rows read between stages.

### R4, the trainer

1. **A trainer for the character's class.** `npc entry=<a starting-zone class trainer>`; `open` prints a menu with a
   trainer option; `trainer` prints the greeting and one line per spell, and the step line names the trainer id and
   count. A menu without the option fails with "the menu has no trainer option" and sends nothing.
2. **A trainer for another class.** `trainer` fails the same way; the core added no option for this character
   (Player.cpp:15169-15172).
3. **One spell.** `learn spell=<a low-level spell from the list>`: the step line says the spell was learned within 3 s
   and names its price and level; the character's spellbook (the next login's `SMSG_SEND_KNOWN_SPELLS`) contains it.
4. **A refusal.** `learn spell=<an unaffordable spell>` on a character with little money fails before any packet with
   the list price and the coinage the login or a values update set; a spell not on the list is refused the same way.
   The server's own `not_enough_money` answer is the case where the purse fell between the check and the purchase.
5. **A trainer without a gossip menu**, if the server has one: `trainer` sends `CMSG_TRAINER_LIST` and prints the
   list; this is the one path a real client's own trainer ask covers.
6. **A relog** shows the learned spell in the spellbook; the step line's coinage (before and after) matches the money
   the server's own row shows.

### R6, the flight paths

1. **The window.** `npc entry=<a flight master>`; `taxi-nodes`: the step prints the current node and both masks' node
   ids, and the packet log shows the taxi gossip option (or the query, for a flight master with no gossip menu). At a
   flight master this character has never used, the first hello answers the node's discovery and the step says
   `taxi learned the flight master's node`, asks again and prints the window; the second ask is the expectation, not
   a timeout.
2. **Node status.** `taxi-status` names `learned` at the character's own faction's flight master; a flight master of
   the other faction answers `not_eligible`.
3. **One flight.** `fly node=<an id from the usable list>`: exactly one `CMSG_ACTIVATE_TAXI` goes out, no movement
   packet follows while the character is on the taxi, and the landing line names the position, the duration and the
   distance from the takeoff (the live check expects the landing within 30 yd of the destination flight master's
   position; the client has no taxi DBC, so the lead reads the distance to the master from the server's rows).
4. **A refusal.** `fly node=<the current node>` is refused before sending; `fly node=<a node the character has not
   visited>` fails with `not_visited` from the server's reply.
5. **While flying**, another unit that takes a flight shows `ON_TAXI` in the world model, and the next run's world
   summary counts it (`N other unit(s) on a taxi flight`): the W2 check that a bot flies rather than teleports.
6. **A relog** at the destination shows the character there, with the money spent on the first leg.
7. **The `taxi` method (#107).** `taxi {guid}` at a flight master prints the window; `taxi {guid, node}` returns at
   takeoff with `on_taxi` true, `status` shows `on_taxi` until the landing, `chat`/`gm` calls answer during the flight
   (the TBF #203 check: `.bot summon` with the owner on a taxi is refused in-flight), `mount` and `dismount` refuse in
   the air, and `taxi_flight` events read takeoff and landed. Not covered by a fake: the zeppelin (a transport, not a
   taxi) and a flight across maps through this method.

## Gate lines

All on the clean committed tree `2569f200` (`git status` empty before each run); this section is written by the
notes-only commit that follows it, so the code state the lines name is unchanged.

* `make fmt-check`: no output, `rc=0` (its success line is silence).
* `make cross`: the Windows and Darwin cross builds and the Windows vet of `./cmd/... ./internal/forever/...`
  passed, `rc=0`.
* `staticcheck ./...`: `rc=0`, no findings.
* `go vet ./...`: `rc=0`.
* the full turtle-agent suite on the build VM: `RESULT: PASS kind=ta sha=2569f200 dirty=0` (`rc=0`, 1333 s; the
  suite ran `go test -count=1 -timeout 20m ./...` and `go vet ./...` on a clean tree of 2556 files).
