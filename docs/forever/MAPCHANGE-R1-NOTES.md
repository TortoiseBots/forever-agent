# Forever map changes, round A7 notes

Scope: the client follows a map change. A far teleport on the same continent pair, a teleport to another
continent, and entering and leaving an instance all end with the character on the new map, in a fresh world model,
able to walk and fight there. Code: `internal/forever/objects` (the transfer parsers, the world model's reset and the
mover's between-maps refusal), `internal/forever/world` (the session's transition, `forever map-check`),
`internal/forever/wire` (the new opcode numbers). Everything below was checked against the server core's teleport path and
a local capture of a real session (a dungeon entry and a teleport between maps).

Status: complete against the live server (see "The live run").

## The sequences

### A far teleport (the same map or another one)

`Player::TeleportTo` (`Player.cpp:1305-1530`) takes the far path when the destination map or instance differs from
the current one (`:1404-1452`):

```
Server: SMSG_TRANSFER_PENDING         MovementPackets.cpp:755-778   map (int32), the destination XYZ, three optional bits
        (the player leaves the old map here: RemovePlayerFromMap, Player.cpp:1516)
        SMSG_SUSPEND_TOKEN             MovementPackets.cpp:1112-1119 uint32 sequence, 2 bits reason (1 = normal)
Client: CMSG_SUSPEND_TOKEN_RESPONSE    MovementPackets.h:681-690     the sequence, uint32
        CMSG_LOADING_SCREEN_NOTIFY     (map = the destination, the Showing bit set; sent on the realm socket)
Server: SMSG_UPDATE_LAST_INSTANCE      InstancePackets.cpp:23-28     only for a dungeon destination
        SMSG_NEW_WORLD                 MovementPackets.cpp:799-811   map, destination XYZO, floor difficulty/index,
                                                                     reason (16 normal, 21 seamless), movement offset,
                                                                     counter, instance id
Client: CMSG_WORLD_PORT_RESPONSE       MovementPackets.h:323-327     empty (sent on the realm socket)
Server: SMSG_RESUME_TOKEN              MovementPackets.cpp:1126-1133 same layout as the suspend token
        SMSG_TIME_SYNC_REQUEST, SMSG_BIND_POINT_UPDATE, the spell/action/faction burst,
        SMSG_WORLD_SERVER_INFO         MiscPackets.cpp:224-249       difficulty, four GUIDs, six bits, the group size
        SMSG_MOVE_SET_ACTIVE_MOVER
        SMSG_UPDATE_OBJECT            the new map's create burst, this character's own create included
        SMSG_INIT_WORLD_STATES, SMSG_INSTANCE_INFO
```

The capture's dungeon entry shows that shape: the transfer pending names the dungeon, the suspend token carries
reason 1, the new world carries reason 16, and the world is followed by the login-like burst; leaving is the same
sequence back to the open map with difficulty 0. The capture's other teleport has the same sequence without
`SMSG_UPDATE_LAST_INSTANCE` (not a dungeon). The client's own packets have exactly the writers' shape: the
suspend-token response is the sequence (4 bytes), the world-port response is **empty**, and the loading-screen
notification is the map plus one bit byte with the Showing bit **set** on a map change (the login's is clear).

The wire numbers the client uses (the build's group shift applied to the core's table, pinned by the fake server): `CMSG_SUSPEND_TOKEN_RESPONSE` 0x460008,
`CMSG_WORLD_PORT_RESPONSE` 0x440025, `CMSG_AREA_TRIGGER` 0x3E008A; `SMSG_TRANSFER_PENDING` 0x470071,
`SMSG_SUSPEND_TOKEN` 0x470041, `SMSG_RESUME_TOKEN` 0x470042, `SMSG_NEW_WORLD` 0x47002B,
`SMSG_TRANSFER_ABORTED` 0x4701AE, `SMSG_UPDATE_LAST_INSTANCE` 0x470135, `SMSG_WORLD_SERVER_INFO` 0x470046 and
`SMSG_INSTANCE_INFO` 0x4700DE.

### The socket question

No socket moves during a map change. `SendConnectToInstance` is called only while the character is logging in
(`CharacterHandler.cpp:1371`; the retries of `WorldSocket.cpp:1121-1130` and the bot hand-off of `WorldSession.cpp:418`),
never from the teleport path, so the instance connection and its byte order continue across the change. The two
`SMSG_*_COMMS` packets of the authentication round
(`AuthenticationPackets.h:275-320`) are the login handshake; this build uses `SMSG_SUSPEND_TOKEN`/`SMSG_RESUME_TOKEN`
for a teleport instead, and the opcode table pins them to `CONNECTION_TYPE_INSTANCE` (`Opcodes.cpp:2349`, `:2486`).
The transfer packets themselves (transfer pending, new world, transfer aborted, update-last-instance, instance info)
are `CONNECTION_TYPE_REALM` (`Opcodes.cpp:2129`, `:2509-2510`, `:2544`, `:1859`), and the capture shows exactly that
split: the transfer pending and the new world on the realm socket, the suspend/resume tokens on the instance socket, the
suspend-token response on the instance socket and the world-port response on the realm socket. `SMSG_WORLD_SERVER_INFO`
is the exception among the informational packets: its table entry is `CONNECTION_TYPE_INSTANCE` (`Opcodes.cpp:2586`),
so the difficulty arrives on the gameplay socket. Both sockets belong to
the same `WorldSession`, and the handlers are `STATUS_TRANSFER`, which refuses a packet while the player is still in
the world (`WorldSession.cpp:491-499`) — so the order matters, not the socket we send on. The client follows the
capture's sockets anyway so a future build that does care is not surprised.

### The abort

`SMSG_TRANSFER_ABORTED` (`MovementPackets.cpp:779-787`): map (uint32), argument (uint8), the map-difficulty
condition id (int32), then the reason in **six bits**. The reasons are `TransferAbortReason` (`Map.h:90-115`); this
round names all of them. An abort is sent from `Player::TeleportTo` before the transfer pending (the enter checks,
`Map.cpp:1829-1882`) or from the worldport ack when the map can no longer be entered (`MovementHandler.cpp:236-244`).
A dungeon with a level requirement fails inside `Player::Satisfy` (`Player.cpp:21380-21474`), which reports
`TRANSFER_ABORT_ERROR` unless the map difficulty has a message, in which case the reason is
`TRANSFER_ABORT_DIFFICULTY` with the difficulty as its argument. Either way the character stays on its map: nothing
moved it, so the client only has to report the reason and go on.

No capture contains an abort, so its fixture is hand-built from the writer and the live run is the proof.

### Entering an instance without a teleport

`CMSG_AREA_TRIGGER` (`AreaTriggerPackets.cpp:23-28`): the trigger id (int32), then two bits, Entered and FromClient,
flushed. The handler (`MiscHandler.cpp:538-...`) refuses a trigger the character is not standing in
(`IsInAreaTrigger`), so a walker must walk to the portal first; the reply is a far teleport and the sequence above.
Captured client packets: id 2230 with both bits set, and the same id with Entered clear on the way out — 5 bytes
each.

## What the client does at each step

| packet | client |
|---|---|
| `SMSG_TRANSFER_PENDING` | records the destination map, marks the session *between maps*, prints the transition, sends `CMSG_LOADING_SCREEN_NOTIFY` (the destination). The mover refuses every movement (`ErrMapChange`); a walk or a fight in progress ends with that error at its next step, so no movement packet goes out from the old map's coordinates. |
| `SMSG_SUSPEND_TOKEN` | answers with `CMSG_SUSPEND_TOKEN_RESPONSE` carrying the same sequence. Without this answer the server never sends the new world. |
| `SMSG_UPDATE_LAST_INSTANCE`, `SMSG_INSTANCE_INFO` | parsed and logged (map and locks); nothing else to do. |
| `SMSG_NEW_WORLD` | takes the map and the position from the packet, sends the empty `CMSG_WORLD_PORT_RESPONSE`, clears the world model (every object of the old map; the name caches, the party model and the chat log stay), resets the mover to the new position and drops the navmesh planner so the next route builds one for the new map, clears the between-maps mark, and accepts the following create burst as after a login. |
| `SMSG_RESUME_TOKEN` | logged; no answer (the real client sends none). |
| `SMSG_WORLD_SERVER_INFO` | the difficulty (and the instance group size, for the log) is kept for the transition line and the `map-check` output. |
| `SMSG_TRANSFER_ABORTED` | keeps everything where it was and reports the abort with the reason's name, the argument and the map. Every abort this build sends comes before any transfer pending, so that is the legal shape; an abort while the character is between maps is counted and ignored (round 2). |
| later `SMSG_MOVE_SET_ACTIVE_MOVER` | answered again (the capture acks one per map); the create block that follows moves the mover, as for a second login burst. |

The world model keeps its cumulative counters across the reset (they are run totals); `objects.World.ResetForMap` drops
the objects and their eviction queue and sets the map. The party model and the chat log are outside the model and are
untouched, which is what the capture shows: the party update and the channel notices follow the new world.

## The three recurring mistakes, checked per parser

Each new parser and the writer is listed with its verdict; the code cites the server's lines.

| site | element | verdict |
|---|---|---|
| `TransferPending` optional block | three one-bit `OptionalInit` bits before the optional values (`MovementPackets.cpp:761-763`) | one flushed bit byte, then ship/spell/taxi in that order; the capture's 17-byte packet is 4 + 12 + 1 |
| `NewWorld` | `TeleportLocation` is XYZO + floor difficulty/index (`:792-798`), then reason, the movement offset, the counter and the instance id | 56 bytes in the capture; the instance id is zero live |
| `TransferAborted` | the reason is `Bits<6>` after an int32 (`:784`) | read as six bits, not a byte |
| `SuspendToken`/`ResumeToken` | uint32 then `Bits<2>` (`:1112-1119`, `:1126-1133`) | 5 bytes in the capture |
| `WorldServerInfo` | the four empty GUIDs are 2-byte packed forms, not 4-byte values (`MiscPackets.cpp:225-230`) | the capture's 15-byte packet parses to difficulty, four empty GUIDs, six bits and the group size |
| `InstanceInfo` | `Size<int32>` then per lock the map, int16 difficulty, uint64 instance id, int32 time left, uint32 mask and two bits (`InstancePackets.cpp:34-61`) | 4 bytes empty, 27 bytes with one lock in the capture |
| `BuildWorldPortResponse` | the client's own bytes | empty, equal to the captured packet |
| `BuildSuspendTokenResponse` | `uint32` (`SuspendTokenResponse::Read`, `MovementPackets.cpp:1121-1124`) | the captured 4 bytes |
| `BuildAreaTrigger` | int32 + two bits (`AreaTriggerPackets.cpp:23-28`) | the captured 5 bytes |

Sized strings do not occur in these packets, so `SizedString`/`SizedCString` has nothing to check here; the fixture
that exercises the writer is the captured client packet itself, not a packet this round's writer produced. The sweep
over every `append(T const*, size_t)` in the server's writers of this family finds none: each field is fixed-width or
gated by a bit, so there is no count to multiply. Every bit field is read with the same width the writer used
(`ReadBits(2)` for the tokens' reason, `ReadBits(6)` for the abort's, `ReadBit` for the optionals), and each test
compares the parse against the captured byte count as well as the values.

## Fixtures and their source

`internal/forever/objects/testdata/transfer.json` holds the server packets of the two captured map changes (transfer
pending, suspend token, update-last-instance, new world, resume token, two world server infos, the empty instance
info) and the five captured client packets (suspend-token response, world-port response, loading-screen notification
and the two area triggers, one entered and one left). `transfer_gen_test.go` cuts them out of a local capture of a real session (one-off:
`FOREVER_CAPTURE_PKT` + `FOREVER_WRITE_FIXTURES=1`); the committed file needs neither. Rewritten before commit:
every position becomes the synthetic (1000, 2000, 50) with orientation 0.5 and the transfer pending's old-map
position likewise; the area-trigger and transfer maps stay (they are map numbers, not run values). No GUID in these
packets is non-empty, but the generator still runs every parse through the wire buffer's GUID hook and the audit
fails if a captured GUID survives. The abort has no captured fixture: `TestFixtureAbortReasonInSixBits`
(`transfer_test.go`) builds it from the server's writer, field by field, and the live run is its proof.

## What the tests pin

In CI, without the capture and without a server:

* `TestFixtureTransferPending`, `TestFixtureNewWorld`, `TestFixtureTransferTokens`, `TestFixtureWorldServerInfo`,
  `TestFixtureInstancePackets` — every captured server packet of the family decodes to its pinned fields and is
  consumed exactly.
* `TestFixtureTransferWriterMatchesTheCapturedClient` — the four writers reproduce the captured client packets byte
  for byte (the suspend-token response, the empty world-port response, the loading-screen notification with its
  Showing bit, and both area triggers), and `TestMapCheckLoadingScreenMatchesTheCapture` checks the loading-screen
  builder the world package uses. `TestTransferWriterSizes` states the sizes.
* `TestFixtureAbortReasonInSixBits` — the hand-built abort (no capture carries one): map, argument, the difficulty
  condition and a reason of 33 read from six bits, and the reason names.
* `TestFixtureTransferPositionsAreSynthetic` and `TestFixtureTransferGUIDsAreSynthetic` — the audit: the positions
  are the synthetic point and the four GUIDs of the world server info are empty; the negative subtest proves the
  sweep reports a GUID put back.
* `TestTransferPacketsRefuseTruncation` and `FuzzTransferPackets` — a packet cut by one byte is refused by every
  parser, and a mutated one never panics.
* `TestBoundsRefuseHostileTransferCounts` — a forged instance-lock count is refused before it allocates.
* `TestMoverRefusesMovementBetweenMaps` and `TestMoverWalksTheOldLineNoFurther` — the mover's side: between maps no
  walk, turn, chase or stop sends anything and each says `ErrMapChange`; a leg interrupted by the transfer sends no
  stop. `TestWorldResetForMap` — the reset drops the objects, keeps the names and sets the map.
* `TestMapCheckFollowsAFarTeleport` and `TestMapCheckFollowsAGroupIntoAnInstance` (package `world`, against the fake
  server) — the whole transition: the client answers the suspend token and the world-port response on the captured
  sockets, sends no movement in between, and ends on the new map with the new create only.
* `TestMapCheckReportsAnAbortedTransfer` — the abort keeps the map and the position and reports the reason by name.
* `TestMapChangeDuringAWalkEndsIt` — a walk interrupted mid-leg ends with `ErrMapChange`, no heartbeat or stop after
  the transfer pending, and the transition still completes inside the leg's wait.
* `TestMapChangeDuringAFightEndsIt` — a fight chasing its target returns `ErrMapChange` and no movement packet
  follows the pending. `TestTeleportDuringWalkStartsTheNextPacketAtTheDestination` now also pins that a same-map
  teleport is not a map change.
* `TestLinkLostDuringTheTransition` — a socket that dies after the suspend token ends the run with `ErrLinkLost`,
  quickly, and without claiming the new map.
* `TestNewWorldDropsTheNavmeshPlanner` — the planner is freed at the new world and rebuilt for the new map with the
  same parent rule.

## Bounds

| count | bound | why |
|---|---|---|
| instance locks | a lock is 23 bytes, so the count cannot exceed the packet; past 4096 the parser refuses | a forged count must not allocate |
| transfer pending optionals | fixed (three bits) | no count |
| world server info GUIDs | four, packed | no count |

## The live run

Three `forever map-check` runs against a live server, one character, taking the console teleports in turn and one
area trigger. Positions, object counts, difficulty condition ids and the character's name are not kept in this file;
`<pos>` stands for the position the client printed.

**A same-map teleport** is the near path, not a map change: the client answered `world: SMSG_MOVE_TELEPORT answered`,
the mover moved there, and no transfer appeared.

**A teleport to another map** proved the far sequence in the order above:

```
world: SMSG_SUSPEND_TOKEN (sequence <n>, reason 1) answered
world: map change: SMSG_TRANSFER_PENDING for map 1 (from map 0); the mover refuses movement until the new world
world: map change: SMSG_NEW_WORLD map 1 at <pos> (reason 16, counter <n>); the world model is cleared, the mover is at the new position, CMSG_WORLD_PORT_RESPONSE sent
world: SMSG_RESUME_TOKEN (sequence <n>, reason 1); the character is on the new map
world: SMSG_WORLD_SERVER_INFO difficulty 0, instance group size 0
world: SMSG_MOVE_SET_ACTIVE_MOVER for <guid>, CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE sent
world: own create again: position reset to <pos>
map-check: map change 0 -> 1 (reason 16), difficulty 0, own position <pos>, <n> object(s) 5s after the change
```

The suspend token was answered before the transfer pending appeared in the log: the two arrive on different sockets
and the session handles whichever its readers deliver first. The order does not matter (the response is only the
sequence), and the new world cannot overtake the pending — both travel on the realm socket.

**Back to the first map** printed the reverse transition after the same five lines.

**The abort** (a console teleport into a dungeon the character could not enter):

```
world: map change aborted: map 389 refused, reason 8 (DIFFICULTY), argument 1, difficulty condition <id>; the character stays on map 0 at <pos>
map-check: map change aborted: map 389 refused with reason 8 (DIFFICULTY), argument 1; the character is still on map 0 at <pos> (<n> objects)
```

So the core takes the `mapDiff->Message` branch of `Player::Satisfy` (`Player.cpp:21458-21468`), not the bare level
branch: the reason is `TRANSFER_ABORT_DIFFICULTY` with the difficulty as its argument and the failing difficulty
condition as the condition id. The client kept its map, its position and its objects.

**The server's own view**: one `saveall` was sent while the character stood on map 1 — the `characters` table then
held map 1 and the arrival position the client printed — and one at the end, after the return teleport: the start
map and position. `account onlinelist` answered "No online players" after the logout, once the session was torn down.

**The area trigger**: after a teleport to the outside entrance (map 1), `--area-trigger 2230` sent
`world: CMSG_AREA_TRIGGER 2230 (entered, from the client)` and the run stayed healthy. The server's answer was
nothing: the entrance refused the character through `Map::PlayerCannotEnter`, which the area-trigger handler only logs
under `TRINITY_DEBUG`, so an entry and a leave could not be measured in that run. The client's packet is byte-for-byte
the captured one, and `HandleAreaTriggerOpcode` is the same code path the teleport uses once the map is allowed.

## Round 2 (fix round): the sequences, the phase machine, the instance stream boundary

### The legal sequences, read from the server

| sequence | server |
|---|---|
| normal far teleport | `SMSG_TRANSFER_PENDING` (realm) -> `SMSG_SUSPEND_TOKEN` (instance) -> [client answer] -> [`SMSG_UPDATE_LAST_INSTANCE` for a dungeon] -> `SMSG_NEW_WORLD` (realm) -> `SMSG_RESUME_TOKEN` (instance) -> the burst. `Player.cpp:1501-1522` sends the pending unless the teleport is seamless or the player is logging out; `:1526-1527` removes the character from the old map; `:1541-1545` sends the suspend token (reason 1); `HandleSuspendTokenResponse` (`MovementHandler.cpp:397-421`) sends update-last-instance and the new world only in answer to it; `HandleMoveWorldportAck` (`:189-307`) sends the resume at `:254-258`, before the initial packets at `:259-260` and `:307`. |
| seamless teleport | `SMSG_SUSPEND_TOKEN` (reason 2) -> [client answer] -> `SMSG_NEW_WORLD` (reason 21) -> `SMSG_RESUME_TOKEN`, no transfer pending. Seamless is only kept when the old and new map entries share a cosmetic parent (`Player.cpp:1437-1444`); the server then runs `HandleMoveWorldportAck` itself (`MovementHandler.cpp:420-421`), so the new world and the resume are generated together and — different sockets — can be read in either order. The pending alone is skipped; the suspend token is always sent (`Player.cpp:1501` vs `:1541-1545`). On the instance stream the resume is still before every new-map packet. |
| abort | `SMSG_TRANSFER_ABORTED` and nothing else: all `SendTransferAborted` call sites are the checks before a teleport takes the character out (`Player.cpp:1317`, `:1346`, `:1422`, `:1434`, `:19814`; `MiscHandler.cpp:698-729`). An abort AFTER the pending is not something this build sends: a worldport ack that fails teleports the character to its homebind instead (`MovementHandler.cpp:229-245`), which starts a new transfer. |
| login | no `SMSG_NEW_WORLD` at all. `HandleSuspendTokenResponse` is the only writer of the packet, and a login never sends a suspend token; a character logging into an instance gets the ordinary create burst. |

Consequences the handlers implement:

* an abort with no transfer open is LEGAL and is reported (it is the live level-gate refusal); it changes no state,
* an abort between maps is not sent and is counted and ignored,
* a `SMSG_TRANSFER_PENDING` read after a FAR suspend token (reason 1) is the same transfer (the realm and instance sockets race) and is accepted where it is; after a seamless token (reason 2) it belongs to no transfer of ours and is counted and ignored — the server writes no pending for a seamless teleport (`Player.cpp:1501-1522`), and accepting it would name the wrong map for the real new world and restart the 60 s window,
* `SMSG_NEW_WORLD` is only legal once the transfer is suspended, or — seamless — once its resume token was read first,
* the new world's map must equal the pending's map, and its counter must not repeat the last applied one.

### The phase machine

| phase | accepted | then |
|---|---|---|
| idle | `PENDING` | pending |
| idle | `SUSPEND_TOKEN` (seamless) | suspended |
| idle | `ABORT` | idle (reported) |
| pending | `SUSPEND_TOKEN` | suspended |
| pending | `RESUME_TOKEN` of the resolved transfer (the fallback's pending) | pending (logged, not an error) |
| pending | `PENDING`, `NEW_WORLD`, `ABORT` | counted and ignored |
| suspended | `NEW_WORLD` | resumed (map applied) |
| suspended | `RESUME_TOKEN` reason 2 (seamless) | resumed (new world may follow) |
| suspended | `PENDING` after a far token (reason 1) | suspended, the map named; the window's start is not moved |
| suspended | `PENDING` after a seamless token (reason 2) | counted and ignored |
| suspended | `RESUME_TOKEN` reason 1, `SUSPEND_TOKEN`, `ABORT` | counted and ignored |
| resumed | `RESUME_TOKEN` | idle |
| resumed | `NEW_WORLD` (seamless, resume read first) | resumed (map applied) |
| resumed | `PENDING`, `SUSPEND_TOKEN` (a new transfer from the fallback) | pending / suspended |
| resumed | `NEW_WORLD` again, `ABORT` | counted and ignored / reported |

Everything else — a new world in idle, a duplicate new world for the same counter, a suspend token while suspended, a
resume token with nothing open or with a sequence that does not follow the suspend token's (both are
`m_movementCounter`, `Player.cpp:1543`, `MovementHandler.cpp:255`), an abort between maps — is counted
(`Report.UnsolicitedTransfers`) and logged at most once a second, and changes nothing: no model clear, no mover move,
no answer.

### The boundary: the instance connection's own stream

The realm and instance sockets are read concurrently into one event channel, so a packet can be handled after a
packet the other reader read later (or earlier). A counter shared between the readers cannot be a boundary: whichever
reader sees the new world first, an old-map packet read just after it would be stamped fresh, and a new-map packet
read just before it would be stamped old and dropped. The rule is in band instead, on the one connection that carries
the map's content:

| fact | server |
|---|---|
| the tokens live on the instance connection | `SMSG_SUSPEND_TOKEN` and `SMSG_RESUME_TOKEN` are `CONNECTION_TYPE_INSTANCE` (`Opcodes.cpp:2486`, `:2349`); `SMSG_NEW_WORLD` is `CONNECTION_TYPE_REALM` (`:2129`) |
| map content is instance-only | `SMSG_UPDATE_OBJECT` (`:2545`), the movement relays (`:2074`, `:2137`), the auras (`:1273`) and the combat packets are instance-only, so the content the boundary has to classify never travels on the realm socket |
| the character is off its map when the token is written | `Player::TeleportTo` writes the pending (`Player.cpp:1501-1522`), then removes the player from the old map (`:1526-1527`), then writes the suspend token (`:1541-1545`) |
| the new map's content starts after the resume token | `HandleMoveWorldportAck` writes the resume at `MovementHandler.cpp:254-258` before `SendInitialPacketsBeforeAddToMap` (`:259-260`), `AddPlayerToMap` (`:276`) and `SendInitialPacketsAfterAddToMap` (`:307`); the server writes a session's socket in order |
| the instance socket is not reconnected | `SendConnectToInstance` is only on the login path (`CharacterHandler.cpp:1371`, `WorldSocket.cpp:1121-1130`, `WorldSession.cpp:418`) |

So the instance reader's stream position is the boundary, and the session implements it as a gate:

* the accepted suspend token closes the gate. It was written after the character left its map, so map content read
  after it on this connection is either still in flight from the old map or belongs to no map; the server writes no
  new-map packet before the resume.
* a map-content packet read while the gate is closed (`SMSG_UPDATE_OBJECT`, the movement relays, the combat, spell and
  aura packets — `describesMapContent`) is held, bounded at `maxHeldMapPackets`; past the bound it is dropped and
  counted.
* the accepted resume token resolves the marker: every held packet was read before it, so it is not the new map's and
  is dropped and counted (`Report.StaleMapPackets`). If the new world's clear is already applied the gate opens;
  the packets read after the resume are the new map's.
* the new world's clear, the mover placement and the world-port answer run before any such packet is applied. In the
  seamless order the resume is read first and its following packets are held until `SMSG_NEW_WORLD` (realm) is
  applied; `applyNewWorld` then replays them in stream order.

No new-map create can be dropped: the server writes the resume token before the first one on the same connection, so a
create is always read after the marker and is held (not dropped) until the clear. The residual window is only the
reverse: a packet the server wrote before the resume but read after it is dropped — for this core there is none,
because the player is off the map and the only instance write in between is the resume; a server that sent map content
without a resume would have its packets held until the 60 s cap ends the run rather than applied to a map the client
has not seen. Time syncs, pongs and the informational packets are never held or dropped: they do not describe a map.

### The bound

The window is closed by the pending (or a seamless suspend token) and reopened by the new world. If no new world
arrives within 60 s on the session's clock, the next wait ends the run with `ErrMapChangeTimeout` ("map change did
not complete"): movement is never reopened on a guess. The gate has the same bound on the other side: on the far path
the new world's clear leaves the gate closed until the resume token, and a resume that never comes ends the run with
the same error after 60 s instead of silently holding the new map's content forever. The server's own handshake is
one round trip, so 60 s is a dead link or a lost packet, not latency.

### The new world's content

`objects.NewWorldPacket.Validate` (called by `ParseNewWorld` and again by the transition, before any state changes)
refuses a map outside 0..65535 — `SMSG_UPDATE_OBJECT` carries the map in 16 bits (`update.go:232`), so a larger id
would be truncated into the model (65537 would become map 1) — and a non-finite or out-of-range position. The bound is
the server's own `Trinity::IsValidMapCoord` (`GridDefines.h:197-204`): `|x|,|y|,|z| <= MAP_HALFSIZE - 0.5 =
17066.165`, computed from `MAP_SIZE = SIZE_OF_GRIDS * MAX_NUMBER_OF_GRIDS = 533.3333 * 64`; the orientation only has to
be finite (the server normalises the position with the same function). The session keeps the map id as `int32`/`uint32`
from the packet through the world model (`objects.World.mapID` is `uint32`); nothing narrows it to 16 bits.

### The first movement packet after a map change

At the far teleport the server resets the player's movement flags to `MOVEMENTFLAG_MASK_HAS_PLAYER_STATUS_OPCODE` and
clears the jump (fall) block (`Player.cpp:1358-1361`), and removes the character from any transport (`:1373-1376`).
`HandleMovementOpcodes` then takes the flags from the client's own packet (`MovementHandler.cpp:487-660`); the
anti-cheat only removes flags it cannot justify, it does not require a particular word. The mover therefore reports
the ground baseline after `EndMapChange`: the status bits (root, gravity, water walk, feather fall, hover, can fly,
collision) plus `MOVEMENTFLAG_TOUCHING_GROUND`, with no moving/falling bit, no fall block and no transport block — a
standing character's first packet on the new map.

### What the round-2 tests pin

* `TestMoverCompletesATransferInsideOneWait` (objects) — pending, suspend and new world inside one wait: no packet
  after the transition, the mover at the new position, the next walk starts there.
* `TestMoverWalksTheOldLineNoFurther`, `TestEndMapChangeResetsTheOldMapState` (objects).
* `TestTransferHandlersFollowTheServersSequences`, `TestTransferHandlersIgnoreIllegalPackets`,
  `TestTransferHandlersIgnoreAResumeFlood`, `TestNewWorldValidationRefusesBadContent`,
  `TestOldMapUpdateRacingTheNewWorldIsNotApplied`, `TestNewMapCreateRacingTheNewWorldIsNotDropped`,
  `TestLatePendingFollowsOnlyAFarSuspendToken`, `TestHeldMapPacketsAreBounded`,
  `TestTenThousandMapChangesDoNotGrow`, `TestMapChangeTimeoutEndsTheRun`, `TestPendingDoesNotOverwriteAnUnreportedRow`
  (world). The two race tests drive the session's real two readers over fabricated streams and stop one reader at a
  chosen packet, so both interleavings of each race run: an old-map update is never applied, and a new-map create is
  never dropped.
* `FuzzTransferHandlers` feeds every transfer handler arbitrary bytes; `FuzzTransferPackets` still covers the parsers.

### The live instance entry (round 2)

The entrance rule for the realm's Ragefire Chasm is a level gate (a console teleport below it was refused with
`DIFFICULTY`, one above it entered; the difficulty condition of the refusal is the level condition). The character
was raised for these runs only (nothing else on the server was changed), and the runs were:

1. `forever map-check` on the outside map, then a console teleport into the instance: `SMSG_SUSPEND_TOKEN` first,
   then the transfer pending for map 389 (the sockets race; the late pending is accepted), the new world at the
   inside position, the world-port answer, the resume token, `SMSG_WORLD_SERVER_INFO difficulty 1, instance group
   size 5`, the instance info and the new create.
2. `forever walk-check` while logged in inside: a start, one heartbeat, a stop and back, and the walk was not
   refused.
3. A console teleport out while on map 389: the reverse transition to the outside map, the character's start
   position again.
4. `forever walk-check --navmesh` to the portal's own point: a partial route with three corners, the mesh floor below
   the trigger, no hostile unit attacked, no guard aggro.
5. `forever map-check --area-trigger 2230` from that spot: `CMSG_AREA_TRIGGER 2230 (entered, from the client)`, then
   the transfer pending for map 389, the new world at the inside point the server's `areatrigger_teleport` row names,
   and the new create. This is the real entrance trigger path, not a console teleport.
6. A console teleport back to the starting zone while inside: the reverse transition, the character's start position.

The entrance's answer to the area trigger arrives as the transfer pending of its own teleport; there is no separate
"enter" packet. There is no exit area trigger in the server's `areatrigger_teleport` table (the only Ragefire row is
2230, whose destination safe loc is inside), so the way out of the instance in this round was the console teleport.
One `saveall` after the last logout left the `characters` row on the start map and position; `account onlinelist`
answered "No online players".

## Open questions for the dungeon driver

* The trigger id is the caller's (`EnterAreaTrigger`), as the round asks; the plan files carry the entrance trigger
  (Ragefire Chasm's is 2230), but no table of triggers is built here.
* A seamless teleport (`NEW_WORLD_SEAMLESS`, reason 21, and the resume token's reason 2) skips the transfer pending
  and the loading screen; round 2's phase machine accepts its suspend-first order and the new world applied without a
  pending. None was observed live yet: it needs two maps that share a cosmetic parent (`Player.cpp:1437-1444`).
* The server may send the new world's creates before or after the instance info, in either socket; the client applies
  them in arrival order and does not wait for any particular one.

## Fix round 4 (2026-11-19): the self movement-state packets are map content

The gate's list (`describesMapContent`) held objects, movement relays, combat and auras but not the four self
movement-state packets the death flow handles and answers: `SMSG_MOVE_SET_RUN_SPEED`,
`SMSG_MOVE_SET_SWIM_SPEED`, `SMSG_MOVE_SET_WATER_WALK` and `SMSG_MOVE_SET_LAND_WALK`. A copy the server wrote for
the old map could therefore be read between the suspend answer and the resume, mutate the mover (its run speed, or
the water-walking status bit), satisfy the release's ghost-speed wait, and make the client answer with the new side's
movement info — `CMSG_MOVE_FORCE_RUN_SPEED_CHANGE_ACK` / `..._SWIM_...` (`HandleForceSpeedChangeAck`,
`MovementHandler.cpp:662-733`) or `CMSG_MOVE_WATER_WALK_ACK` (`MovementHandler.cpp:777-784`). `EndMapChange` keeps
the status bits, so a stale water-walk would also survive into the new map.

They are now gated like the rest: read before the resume they are dropped at it; read after it they are applied in
instance-stream order after the clear, exactly as every other held packet. The other movement-state packets in the
dispatch table are not in the set: `SMSG_MOVE_ROOT`/`UNROOT` is handled without an acknowledgement
(`OBJECTS-R1-NOTES.md`), and `SMSG_MOVE_SET_ACTIVE_MOVER` only names the mover and is answered with
`CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE`; neither carries a position or a speed for the old map.

What the server restates for the new map, so dropping the old copy loses nothing that is still true:

* Water walking and the other status flags: the player's own create block is sent at every add to map
  (`Map::SendInitSelf`, `Map.cpp:1882-1907`), and its movement block writes the unit's movement flags
  (`BaseEntity.cpp:296`). The far teleport keeps exactly the status mask, which includes water walking
  (`Player.cpp:1357-1358`, `MOVEMENTFLAG_MASK_HAS_PLAYER_STATUS_OPCODE`, `MovementInfo.h:114-115`).
* Aura-derived states: `SendInitialPacketsAfterAddToMap` sends them as `SMSG_MOVE_SET_COMPOUND_STATE`, water walking
  among them (`Player.cpp:26654-26695`), and re-sends every visible aura to the character
  (`SendAurasForTarget(this)`, `Player.cpp:26699`; the writer at `:27078-27094`). The ghost's aura 8326, with its run
  and swim speed effects, is therefore re-applied on the new map.
* Speeds: no `SMSG_MOVE_SET_RUN_SPEED`/`_SWIM_SPEED` is re-sent at a worldport — `Unit::SetSpeedRate` writes only on
  a rate change (`Unit.cpp:9326-9332`), and neither initial-packets function sends one. The teleport does not reset
  the rate either (only the movement flags are masked, `Player.cpp:1357-1358`), and `EndMapChange` keeps the rate the
  mover last applied, so a rate that was already in effect stays in effect. Only a change the old map announced
  after the suspend is lost, and that is the copy the boundary exists to refuse.

### Tests

`TestMovementStatePacketsAreGatedMapContent` (package `world`, fabricated events) injects each of the four between
the suspend answer and the resume and checks, for each: it is held, the mover's run speed and flags do not change,
the release's ghost setup is not completed, and no acknowledgement is sent; after the new world and the resume one
stale packet is counted and none of that changed either. On the round-3 code the same test fails at the first
assertion (nothing is held).
