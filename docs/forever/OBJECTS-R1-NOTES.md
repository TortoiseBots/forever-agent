# Forever objects and movement, round 3 notes

Scope: from the login burst of round 2 to "the client understands the objects the server puts around the character
and can walk it so the server and other players see the movement". No combat, spells, items or chat. Code:
`internal/forever/objects` (update objects, values, movement in and out, the world model, the walker),
`internal/forever/world` (session wiring, `forever walk-check`), `internal/forever/wire` (the byte layer, the new
opcode numbers).

Status: complete against the live server. `forever-agent forever walk-check` logs in, enters the world, prints what
it decoded, walks 8 yards east, waits, walks back and logs out; the server's own saved position followed the client
exactly on every leg (numbers below). Everything below was checked in ForeverCore at 6517af05 and against the four
private captures.

## The update-field layout this build receives

The core serves a Classic 1.60.1 client from a master code base, so the layout is chosen by *build predicates*, not
by the file's generated shape:

* `ClassicOpcodes::IsBuild70291OrLater()` is `ExtraGroupShift() != 0`, i.e. the **realm's** build ≥ 70291
  (`ClassicOpcodes.cpp:101-117`). `IsBuild70425OrLater()` is the realm build ≥ 70425 (`:120-126`).
* The live realm is build 70707 (`auth.realmlist.gamebuild`), so **both are true**: the 70291 owner-only list, the
  70425 three `int32` after `TaxiNodesID`, the 70425 changes-mask renumbering and the 70425 group ranges all apply.
* The per-client build (`Classic.Build70755.*`, `Classic.Build70790.*`) only moves *message numbers*, not field
  layouts; a 70707 claim gets neither.

So `UpdateFields.cpp` is read with `IsBuild70291OrLater == IsBuild70425OrLater == true`; the client build number
never enters the parsing.

## The packet

`SMSG_UPDATE_OBJECT` (`UpdateData::BuildPacket`, `UpdateData.cpp:61-79`):

| field | type | note |
|---|---|---|
| map | `uint16` | always 0 on the live realm's Eastern Kingdoms |
| block count | `uint32` | |
| unk | 1 bit | always true |
| has lists | 1 bit | destroy/out-of-range lists follow on true |
| destroy count | `uint16` | |
| list count | `uint32` | destroy + out-of-range |
| destroy GUIDs | packed `ObjectGuid` | |
| out-of-range GUIDs | packed `ObjectGuid` | |
| data size | `uint32` | the blocks, byte for byte |
| blocks | | |

Every block starts with a `uint8` update type and a packed GUID (`:96-129`, `:213-220`):

* `UPDATETYPE_VALUES` (0): `uint32` size, then the values body.
* `UPDATETYPE_CREATE_OBJECT` (1) / `CREATE_OBJECT2` (2): `uint8` object type, the movement block, `uint32` values
  size, the values body. Type 2 means the client already knew the object (`m_isNewObject`).
* `UPDATETYPE_OUT_OF_RANGE_OBJECTS` (3) has **no block**: the type is never written; those GUIDs only appear in the
  packet's list. A parser that expects a third block kind desynchronises on the first removal.

Both layouts declare their own size, so the parser reads each block inside a reader bounded by that size and
requires it to be consumed to the last byte (`ParseUpdateObject`); a fragment this round does not decode stops the
walk and the rest of the block is skipped by its size (`WorldFragment.ValuesComplete == false`,
`ValuesStart/ValuesLen/UndecodedFrom`).

## The create-object flag table

`BaseEntity::BuildMovementUpdate` (`BaseEntity.cpp:252-262`) writes 21 flag bits, most significant first:

| bit | flag | this round |
|---|---|---|
| 0 | HasEntityPosition | read |
| 1 | NoBirthAnim | read |
| 2 | EnablePortals | read |
| 3 | PlayHoverAnim | read |
| 4 | ThisIsYou | read (it marks the character's own create) |
| 5 | MovementUpdate | read; the movement info, speeds and spline follow |
| 6 | MovementTransport | read; the transport block is skipped |
| 7 | Stationary | read; the XYZO is decoded as the position |
| 8 | CombatVictim | read; the victim GUID is read |
| 9 | ServerTime | read; `uint32` game time |
| 10 | Vehicle | read; `uint32` RecID and a facing float |
| 11 | AnimKit | read; three `uint16` |
| 12 | Rotation | read; the packed local rotation (`uint64`) |
| 13 | GameObject | read; WorldEffectID and the transport/path-progress bits |
| 14 | SmoothPhasing | read; the replace-object GUID |
| 15 | SceneObject | read; two bits |
| 16 | ActivePlayer | read; the scene-instance and rune blocks |
| 17 | Conversation | read |
| 18 | Room | never set (the writer is commented out) |
| 19 | Decor | never set |
| 20 | MeshObject | never set |

Then `uint32` pause times. Two things about it: the count is written at the top (`BaseEntity.cpp:281`) but the
values are appended at the **end** of the whole movement block (`:694-696`), after every flag-gated block; and the
append goes through `append(T const*, size_t)`, which writes `count * sizeof(uint32)` = **four bytes per entry**
(`ByteBuffer.h:601-609`). A trailer read at the count's position, or read as `count` bytes, desynchronises the
block framing, which is why the pause-times fixture checks the values size behind it.

The values block's flag byte is `UpdateFieldFlag` (`UpdateField.h:36-45`): Owner 0x01, PartyMember 0x02, UnitAll
0x04, Empath 0x08. Owner and Empath change the UnitData layout (see "Each decoded field"); UnitAll would add the
power-regen block (`UpdateFields.cpp:1081`) but `Unit::GetUpdateFieldFlagsFor` never sets it (`Unit.cpp:15015-15026`).

## The movement block of a create

With `MovementUpdate` set (`BaseEntity.cpp:265-398`), in this order:

| field | note |
|---|---|
| MoverGUID | packed |
| movement flags | `uint64` |
| move time | `uint32` |
| X, Y, Z, O | four floats |
| pitch, step-up elevation | two floats |
| removed movement forces | `uint32` count (always 0 here) and `uint32` move index |
| gravity modifier | float |
| 9 bits | HasStandingOnGameObjectGUID, HasTransport, HasFall, HasSpline, HeightChangeFailed, RemoteTimeValid, HasInertia, HasAdvFlying, HasDriveStatus, flushed |
| optional blocks | standing-on GUID, transport (GUID, XYZO, seat, time, two bits), fall (time, z-speed, direction bit, three floats), inertia, advanced flying, drive status |
| 9 speeds | walk, run, run back, swim, swim back, flight, flight back, turn rate, pitch rate |
| movement forces | `uint32` count + float mod magnitude |
| 17 advanced-flying speeds | |
| one bit | HasSpline, flushed |
| spline | `WriteCreateObjectSplineDataBlock` when the bit is set |
| pause times | at the very end: `count` × `uint32` (`:694-696`), not next to the count |

The captured creature creates and the client's own create all have `HasSpline` clear; the parser implements the
spline block anyway (see "What is approximated").

## The values block structure for this build

A **create** body (`BaseEntity.cpp:122-128`): `uint8` field flags (`UpdateFieldFlag`: Owner 0x01, PartyMember 0x02,
UnitAll 0x04, Empath 0x08), the fragment id list (`uint8`
each, `0xFF` ends it; every non-tag id is the retail value + 1, `Object.cpp:194-205`), then every *updateable*
fragment in id order, each indirect one preceded by its `uint8` exists byte. For a creature, a player and a game
object the only updateable fragment is `CGObject` (id 3 on the wire); the type tags (204 unit, 205 player, 206 game
object) are listed but carry no data. A vendor adds `FVendor_C` (18), which this round does not decode.

The `CGObject` fragment is virtual (`Object.h:174-175`), so the object type decides which WriteCreate methods follow
(`Creature.cpp:4124-4128`, `Player.cpp:3955-3962`, `GameObject.cpp:4283-4287`, `Corpse.cpp:240-244`,
`DynamicObject.cpp:252-256`):

* object type: `ObjectData` — entry `int32`, dynamic flags `uint32`, scale float.
* unit / player / active player: `ObjectData` + `UnitData` (+ `PlayerData` + `ActivePlayerData` for the receiver
  itself). This round decodes `ObjectData` and the whole `UnitData` walk and leaves `PlayerData`/`ActivePlayerData`
  to the block size (they are the tail; nothing this round needs is in them, and their account GUIDs are why the
  player fixture's tail is zeroed).
* game object: `ObjectData` + `GameObjectData` — display id, visual ids, created-by and guild GUIDs, flags, parent
  rotation, faction, the Classic-only level `int32`, state `int8`, type `int8`, percent health, art kit, doodad set
  count, custom param, anim group, three UI-widget values, then the doodad sets and world effects.
* corpse and dynamic object: minimal (owner, display id / caster, spell id).

A **values update** body (`BaseEntity.cpp:164-194`): `uint8` field flags, `uint8` "fragment ids changed" (when set:
one `uint8` serialization type, then the id list), `uint8` content mask, then for each changed updateable fragment
its own body. The content-mask bits follow `WowCSEntityDefinitions.cpp:66-84`: each updateable fragment gets one
bit and each **indirect** fragment two (an exists bit and the changed bit), in sorted id order, so `CGObject` owns
bits 0 and 1 and the first vendor-style fragment would own bit 2. The `CGObject` body is `uint32` object-type mask
then the changed type blocks in ascending type order (`Creature.cpp:4130-4139`, `Player.cpp:3964-3979`).

### The masks, and what the Classic client actually gets

`ObjectData::WriteUpdate` writes four mask bits (bits 3..0), flushed (`UpdateFields.cpp:62-83`). `UnitData` and
`GameObjectData` do **not** ship the retail mask: `WriteClassicChangesMaskHeader` (`UpdateFields.cpp:1361-1386`)
renumbers every set retail bit through `ClassicUnitDataBit` and writes

```
WriteBits(blockPresentMask, 8)                     // which of the eight 32-bit blocks follow
for each present block: WriteBits(block, 32)
[70291+] one bit (the full/partial list flag)
```

and recomputes each of the ten Classic group bits from its children. The client therefore sees the mask at *classic
bit numbers*, which is why a parser must invert `ClassicUnitDataBit` (below) and rebuild the group gates from the
children instead of reading the group bit.

`ClassicUnitDataBit` (`UpdateFields.cpp:1318-1348`), with the 70291 and 70425 steps on:

```
70009(bit): 230->141, 231->86, 232->87, 70<->71,
            <=71 same, <=84 +1, <=92 +3, <=95 +4, 96 same,
            <=101 +3, <=105 +4, <=122 +5, <=127 +6, 128 same,
            <=133 +5, 134->140, else +9
then if result >= 140: +1      (70291 inserted the owner-only list at 140)
then if result >= 132: +3      (70425 put the three int32 after TaxiNodesID at 132..134)
```

The ten group ranges of the 70425 layout are `{0,31} {32,63} {64,95} {96,127} {128,151} {152,192} {193,196}
{197,199} {200,220} {221,242}`; the retail group bits the generated code gates on are 0, 32, 64, 96, 128, 139, 180,
184, 187, 208.

`GameObjectData` (`:7571-7750`) writes 28 Classic bits: retail 0..15 stay, retail 21 (Level) becomes classic 16,
retail 16..26 become classic 17..27; the writer then emits the Level `int32` **twice** when bit 21 is set (once as
the Classic-only `+0x74` field, once as `Level`), and the assist-action flag when classic bit 16 is set.

`VisibleItem::WriteUpdate` renumbers its 12-bit mask by hand (`:876-935`): retail bits 0..5 stay, retail 6..11
become classic 7..12 (the twelfth falls off the 12-bit write), bit 0 is the "present" gate, bits 1/2 are the
HasTransmog/HasIllusion **bits** (written only when set, not their values), and the data fields follow in order —
ItemID int32 (3), Secondary (4), Conditional (5), ItemAppearanceModID uint16 (7), ItemVisual uint16 (8),
ItemModifiedAppearanceID uint32 (9), TransmogSlotOption uint8 (10), SheatheCategory uint8 (11). A visible item is
therefore *not* a fixed 27 bytes in an update; the old fixed skip (and the old unconditional Has bits) was wrong.
`UnitData`'s assist-action bit is written whenever the Classic block-4 group bit is set, not per field
(`:2035-2040`) — followed.

## Each decoded field: mask path and proof

| field | create | update | proof |
|---|---|---|---|
| object entry | `ObjectData::EntryID` | bit 1 under bit 0 | creature fixture: entry 1; live: game objects 44-46 with entries from `creature_template`/`gameobject_template` for the Stormwind area, and the nearest-units rows below |
| dynamic flags, scale | bits 2, 3 | | |
| display id | `UnitData::DisplayID` | bit 6 | live: 57899 for the character, matching `chr_races`-derived model; the player fixture decodes 57899 |
| npc flags, npc flags 2 | 7, 8 | | live: vendors/quest givers appear with nonzero flags |
| charm/summon/charmed by/summoned by/created by | 14..19 | same bits | the live create carries zeros except `created by` for game objects |
| target | 22 | 22 | `values_target` fixture: the GUID arrives and `flags` (bits 48..51) change with it; the live `values_target` traffic sets it when a bot is targeted |
| race, class, sex, creature type, display power | 30..36 | 30..36 | player fixture: race 1, class 8, level 4; creature fixture: creature type 1 (beast) and power type 0 |
| health, max health | 38, 39 | 38, 39 (`int64`) | `values_health` fixture 884/884; live: character 60/60, Deputy Willem 42/42, Rabbit 1/1 (see the database checks) |
| power, max power | arrays under bit 139, **interleaved** Power[i], MaxPower[i] (`:1089-1093`) | bits 140..179 | creature fixture: mana 0/0 for a beast; player fixture (a level-4 mage) 244/333; the owner create 2868/2868; the display power selects the pair (`Info.PowerValue`) |
| level | 40 | 40 | creature fixture level 63; player fixture level 4; live character level 1 |
| faction template | 47 | 47 | |
| flags, flags2, flags3, flags4 | 48..51 | 48..51 | live: the character's create carries 0x8 (the `PLAYER_FLAGS`-style group) |
| bounding radius, combat reach, display scale | 54..56 | | |
| stand state, vis flags, anim tier | 68, 70, 71 (AnimTier is written before VisFlags for this client) | | |
| guild | 126 | 126 | |
| current area id | 134 (inside the 128 gate) | 134 | `values_area` fixture: 3428; live: the Stormwind area id |
| combo target | 230 (inside the 128 gate) | 230 | |
| game object state, type, display, flags, faction | `GameObjectData` create order | classic bits 17/18, 4, 12, 15 | game object fixture: entry 176231, state 1, type 15 |
| corpse owner/display, dynamic object caster/spell | create only | not decoded | |

Everything else in `UnitData`/`GameObjectData` is walked (sizes only) and dropped; `PlayerData` and
`ActivePlayerData` are the tail of a player block and are skipped by the declared size.

## SMSG_ON_MONSTER_MOVE

`MonsterMove::Write` (`MovementPackets.cpp:672-680`) is: the mover's packed GUID, `MovementMonsterSpline` and the
spline's start position (three floats). `MovementMonsterSpline` (`:388-396`) is the spline id, `MovementSpline`, and
three flushed bits (CrzTeleport, StopUseFaceDirection, StopSplineStyle). `MovementSpline` (`:319-393`) is flags,
face, elapsed, move time, fade time, mode, transport GUID, vehicle seat, the face payload (spot: three floats;
target: an **angle float then the GUID**; angle: one float), the packed path size (16 bits), two bits, the packed
delta size (16 bits), six optional bits, then the points, the packed deltas (11/11/10-bit signed quarters) and the
optional blocks.

The destination is the path's **last** point for an uncompressed path and the **first** one otherwise (the server
writes the destination first and keeps the intermediates as deltas, `:640-670`). The four fixtures cover: a stop (no
points), facing spot, facing target and facing angle; a fifth has four packed deltas.

## The MovementInfo layout, in both directions

`MovementPackets.cpp:72-155` (write) and `:156-215` (read), identical field order:

```
packed GUID
uint64 flags
uint32 time
float X, Y, Z, O
float pitch
float stepUpStartElevation
uint32 removed movement forces count
uint32 move index
float gravity modifier
bits: standingOnGameObjectGUID, hasTransport, hasFall, hasSpline, heightChangeFailed, remoteTimeValid,
      hasInertia, hasAdvFlying, hasDriveStatus      (nine bits; the ninth starts the next byte)
flush
[standing-on GUID] [transport] [fall: uint32 time, float z-speed, one direction bit,
                                  then sin, cos and the horizontal speed] [inertia] [advflying] [drive]
```

**The nine bits are why the numbers look wrong at first.** Eight bits fill the byte after the gravity modifier;
the ninth goes into the *next* byte, and the writer's flush then closes that byte. So a plain movement packet is
57 bytes (55 fixed + the two bit bytes), the client's fall-land is 57, and the client's falling heartbeat is 78 =
57 + the 21-byte fall block. The client's `CMSG_MOVE_FORCE_ROOT_ACK` is 61 = the 57-byte status + the 4-byte ack
index. There is **no trailing byte**: the first reading of this round's packets that assumed one was an arithmetic
slip, and the byte-for-byte test (below) is what settled it.

Every captured client movement packet is a byte-for-byte round trip through `ReadMovementInfo` and
`WriteMovementInfo` (`TestMovementWriterMatchesTheCapturedClient`): seven kinds from the four captures
(START_FORWARD, START_BACKWARD, STOP, HEARTBEAT with a fall direction, SET_FACING, SET_WALK_MODE, SET_RUN_MODE,
FALL_LAND). That is the independent oracle: the real client wrote those bytes.

## The heartbeat cadence

**It cannot be measured from the captures.** All four files together hold five movement packets from the real
client: one heartbeat and one fall-land in the login file (996 ms apart, the jump), one heartbeat and one fall-land
in the movement file (235 ms apart), one heartbeat, one fall-land and five walk-mode packets in the combat file,
and two fall-lands in the party file. The two heartbeats that exist are 138 s apart, with no other movement packet
between them: the sessions were idle, and the movement file's own timeline shows the client idle from 62 s to 455 s
(only time-sync answers every 10 s).

The client therefore sends heartbeats every 500 ms — its standard movement tick — and every packet carries an
absolute position, which is what the server uses (`HandleMovementOpcode` stores `movementInfo.pos`,
`MovementHandler.cpp:562-563`). The value is not measurable from this capture set; a capture of a walking client
would be needed to replace it. There is no test that compares the constant to itself (the old one could not fail);
the walker's cadence and arrival are tested against a fake clock instead. The core's own
`HEARTBEAT_INTERVAL = 5.2 s` (`Object.h:79`) is the *server's* spline heartbeat, not the client's.

## The server-side movement checks (what a walk must satisfy)

`HandleMovementOpcode` (`MovementHandler.cpp:487-660`):

* The GUID must be the unit the session is moving (`ValidateAndGetUnitBeingMoved`, `:152-176`) — the player itself
  when no unit is charmed.
* `ValidateMovementInfo` (`:49-141`): the position must be valid; a client-sent ROOT is removed; ROOT with a moving
  flag removes the moving flags; hover without the aura, ascending with descending, left with right, strafe both
  ways, pitch both ways, forward with backward, water-walking without the aura, falling-slow without the aura,
  flying without the aura are each removed; `SPLINE_ELEVATION` is added when the step-up elevation is nonzero.
  **None of this rejects a packet**; it only strips flags.
* The character must not be teleporting and its spline must be finalised (`:504-511`); a walker's own splines are
  not used.
* The position is then applied (`mover->UpdatePosition`, `:564`) and broadcast to the other players as
  `SMSG_MOVE_UPDATE` (`:566-568`).
* Falling below the map's minimum height (with no floor within 2 yards) sends the player to the graveyard
  (`:600-620`) — the one hard consequence of a bad Z, and why the walker's Z is interpolated between two points the
  server already had.
* There is **no rate or speed check** in this handler: the position in the packet is taken as the truth. The
  anti-cheat surface here is the flag validation above, the map height and `movespline->Finalized()`.
* Time: `movementInfo.time` is the client's millisecond clock and is mapped with the time-sync delta
  (`AdjustClientMovementTime`, `:169-179`); a delta of 0 or a negative sum falls back to the server clock. The
  client's time base is its own process uptime (`world.clientTimeMs`), exactly what the server's time-sync exchange
  expects.

## Root, unroot, teleports

* `SMSG_MOVE_ROOT`/`SMSG_MOVE_UNROOT` (`MoveSetFlag`, `MovementPackets.cpp:741-746`) carry the mover's packed GUID
  **and a `uint32` movement counter** in this build: the captured unroot is 11 bytes, and the live login sends the
  same. The client does not ack them: an ack's movement info is only validated (`HandleMovementAckMessage`,
  `:777-784`), and rounds 1-2 left login roots unanswered with a healthy session.
* `SMSG_MOVE_TELEPORT` (`:811-833`) carries the mover, a sequence, XYZO + facing, three flushed bits and optional
  transport/vehicle blocks. It is answered with `CMSG_MOVE_TELEPORT_ACK` (packed GUID, ack index, move time,
  `MoveTeleportAck::Read`, `:901-906`); the client sends it for its own GUID only. Before the ack the client applies
  the destination, the facing and the teleport sequence to its own mover, so the first packet after the ack starts
  from the server's destination. A teleport that carries a transport or a vehicle seat is **not** acked: the client
  cannot honour the seat this round, so it reports `teleport onto a transport: not handled` and ends the walk with
  that error instead of sending an ack the server would accept as a landing on the transport.
* `SMSG_MOVE_UPDATE` (`:748-752`) is one `MovementInfo`; `SMSG_MOVE_UPDATE_TELEPORT` (`:856-897`) adds the movement
  forces and nine optional speeds. Both are applied to the world model.
* `SMSG_NEW_WORLD` / `SMSG_TRANSFER_PENDING` are only detected and reported as "map change: not handled in this
  round".

## Name lookups

* `CMSG_QUERY_PLAYER_NAMES` (`QueryPackets.cpp:146-151`): `uint32` count, then that many packed GUIDs (the server's
  reader takes an `Array<ObjectGuid, 50>`). The answer (`:276-289`, `:200-232`) is a `uint32` count and per result a
  result byte, the GUID, three flushed option bits, and — when the full data is present — a bit block (deleted, name
  length in 6 bits, surname length in 9, one more 9-bit string, then `MAX_DECLINED_NAME_CASES = 5` sizes of 7 bits,
  `UnitDefines.h:421`), the declined-name bytes, three packed GUIDs, the club member id, the virtual realm, race, sex,
  class, level, PvP faction, the timerunning season, then the name and surname bytes.
  **The declined-name case list has five entries, not four.** With four the answer of a multi-name query desyncs by
  bytes (`TestFixtureNames` and the capture cross-check pin it).
* `CMSG_QUERY_CREATURE` (`:64-73`): a 6-bit count, flushed, then the `uint32` entries. The answer (`:75-142`) repeats
  the 6-bit count, then per entry the entry, a result byte (0 = stats follow), the bit-packed lengths, the name
  data, the fixed fields and finally the titles. Its strings are `SizedCString`s: a length that counts the NUL and
  **no bytes at all for an empty string**, which is what makes the fixed fields start where they do.
* The client caches names per entry and per GUID, asks at most four batches per run (50 players, 11 creatures), and
  waits at most 3 s for each answer.

## What the walk-check prints, live (2026-11-18)

```
world: in world on map 0 at <position>, reason 0
world: own create block: position <position>, run speed 7.00 yd/s, walk 2.50 yd/s
world: CMSG_QUERY_PLAYER_NAMES for 1 player(s)
world: CMSG_QUERY_CREATURE for 11 entr(y/ies)   (four batches)
world: 109 object(s) in the model after 17 update(s):
  active player    1
  game object      47
  item             7
  unit             54
world: 115 created, 83 updated, 0 destroyed, 6 out of range
world: 14 object(s) partial: a block naming them was not decoded to its declared end
  nearest: active player entry 0      level 1   health 60/60    0.0 yd  <character>
  nearest: unit        entry 823    level 1   health 42/42   16.9 yd  Deputy Willem
  nearest: unit        entry 721    level 1   health 1/1   24.8 yd  Rabbit
  nearest: unit        entry 721    level 1   health 1/1   25.5 yd  Rabbit
  nearest: unit        entry 721    level 1   health 1/1   30.2 yd  Rabbit
walk: start forward from <spawn> to <spawn + 8 yd east> (8.00 yd, facing 0.00, run 7.0 yd/s)
walk: stop at <spawn + 8 yd east> (8.00 yd in 1.144s, 2 heartbeats)
walk: start forward from <spawn + 8 yd east> to <spawn> (8.00 yd, facing 3.14, run 7.0 yd/s)
walk: stop at <spawn> (8.00 yd in 1.143s, 2 heartbeats)
walk-check: back at the spawn, the client believes it is at <spawn> (orientation 3.14)
world logout: SMSG_LOGOUT_RESPONSE (result 0, instant false) and SMSG_LOGOUT_COMPLETE
walk-check: <character> on map 0: 109 objects (115 created, 83 updated, 0 destroyed, 6 out of range) in 17 updates
walk-check: 14 partial object(s), 0 unparsed block(s) (skipped by size)
walk-check: by type: active player 1, game object 47, item 7, unit 54
walk-check: from <spawn> to <spawn + 8 yd east> and back to <spawn>
walk-check: client position <spawn> (orientation 3.14), 656 monster moves, 0 movement relays, 0 teleports, 0 map changes, 1 names asked
server packets: 881 in 80 opcodes
```

The northward run (`--dx 0 --dy 8`) printed the same shape with the server's convention for +Y:

```
walk: start forward from <spawn> to <spawn + 8 yd north> (8.00 yd, facing 1.57, run 7.0 yd/s)
walk: stop at <spawn + 8 yd north> (8.00 yd in 1.144s, 2 heartbeats)
walk: start forward from <spawn + 8 yd north> to <spawn> (8.00 yd, facing 4.71, run 7.0 yd/s)
walk: stop at <spawn> (8.00 yd in 1.143s, 2 heartbeats)
walk-check: back at the spawn, the client believes it is at <spawn> (orientation 4.71)
walk-check: <character> on map 0: 111 objects (112 created, 66 updated, 0 destroyed, 1 out of range) in 7 updates
walk-check: 14 partial object(s), 0 unparsed block(s) (skipped by size)
walk-check: by type: active player 1, game object 49, item 7, unit 54
walk-check: client position <spawn> (orientation 4.71), 400 monster moves, 0 movement relays, 0 teleports, 0 map changes, 1 names asked
server packets: 599 in 74 opcodes
```

The runs received 656 and 400 `SMSG_ON_MONSTER_MOVE` (the Stormwind NPCs walking their patrols), six and one
out-of-range list entries, 47/49 game objects and no unparsed block. The 14 partial objects are the item and player
creates whose tails this round leaves to the declared size. Positions are in this file's live notes, not here: the
committed text carries no run values (the `<spawn>` placeholders are where they were).

### The server's own view of the walk

The chain that proves the walk is real: the world console may run `.saveall` (`cs_misc.cpp:114`, `Console::Yes`),
which writes every online player's live position into `characters.characters`. **One** `.saveall` was sent during
each run's 25 s pause (never in a loop), and the character's row was read back with a read-only query:

| run | server position, offset from the spawn (x, y) | client belief at that moment (x, y) | difference | online |
|---|---|---|---|---|
| +X, during the pause | (+8.00, 0.00) yd | (+8.00, 0.00) yd | 0.00 yd | 1 |
| +Y, during the pause | (0.00, +8.00) yd | (0.00, +8.00) yd | 0.00 yd | 1 |

The Z offset never moved (0.00 yd). After each run `account onlinelist` answered "No online players". The server log
has no movement line at all: its root level is 5 (error) and `ValidateMovementInfo` only logs its stripped flags
under `TRINITY_DEBUG`, so "no rejection in the log" is a statement about the configured level, not evidence; the
saved positions are the evidence. Every leg's arrival was the computed 8/7 s (1.144 s and 1.143 s including the
stop packet), not the next 500 ms heartbeat.

### Database checks of the nearest-units table

Read-only queries on the test host:

| client row | `world.creature_template` | verdict |
|---|---|---|
| entry 823, "Deputy Willem", level 1, health 42/42 | `entry=823 name='Deputy Willem' type=7 (humanoid) faction=12`, `HealthModifier=1` | name, type and faction match; 42 health is the level-1 base times the modifier |
| entry 721, "Rabbit", level 1, health 1/1 | `entry=721 name='Rabbit' type=8 (critter) faction=31`, `HealthModifier=0.01` | name and type match; 0.01 of the 42 base is the 1 health the client shows |
| entry 823 again (second nearest row) | same row | consistent |

The *level* itself is not in the SQL databases: this build computes it from content tuning (`ContentTuningID` 73 and
81 in `creature_template_difficulty`), whose level data is a client DB2 store, not a table. The client's level 1 is
consistent with the level-1 zone and with the health the modifier produces.

## What is approximated, what is skipped

* **Monster move interpolation is linear** between the start position and the destination. The server's path can
  have intermediate points (the packed deltas), which the client would follow; this round's `PositionAt` ignores
  them and the server's CatmullRom maths entirely. A creature therefore appears to cut corners between waypoints,
  and a cyclic spline (`MOVEMENTFLAG`/`SplineFlag::Cyclic`) is reported at its destination.
* **A create's spline block** is parsed (id, destination, flags, face, elapsed, duration, points, the optional
  blocks). The packet's position is where the spline is *now*, so interpolation starts there and runs for the
  remaining `duration − elapsed`; subtracting the elapsed time from the stored start (the old code) counted the
  progress twice.
* **The walker** turns to face the target with the server's `atan2(dy, dx)` convention (`Position.h:136-141`, so +X
  is 0), starts forward, carries the movement flags the create block gave it, wakes at `min(next 500 ms heartbeat,
  computed arrival)`, sends absolute positions and stops with the arrival's timestamp and position. It does not
  model acceleration, the client's pending-stop flags, swimming, jumping or transports, and it has no pathfinding:
  Z is interpolated between the two endpoints, so a walk over a slope puts the character below or above the ground
  and the server's under-map check would send it to the graveyard. On a hard deadline it stops where the
  interpolation is and returns an error (it never snaps to the target); a teleport in the middle of a leg stops the
  leg at the teleport destination (`ErrTeleported`).
* **Skipped**: `PlayerData` and `ActivePlayerData` (the tail of a player block), items and containers (object values
  only, counted), the vendor fragment, and the `FVendor_C`/housing fragments. Inertia, advanced flying, drive
  status, removed/active movement forces, a game object's assist action data and a monster move's spell-visual data
  are now *walked by their exact size* instead of refused (item 9 of the fix round), so a packet that carries one
  still applies and keeps the rest of its blocks in sync. `SMSG_MOVE_UPDATE_TELEPORT`'s movement forces are skipped
  while its speeds are read.
* **Not handled**: map changes (`SMSG_NEW_WORLD`, `SMSG_TRANSFER_PENDING`), flight paths and taxi splines as a
  passenger, vehicle seats, transports (the client never sends a transport block), and the auras a movement flag
  would need (hover, water walking, feather fall) — the client never sets those flags.

## How the fixtures were made

`internal/forever/objects/testdata/objects.json` holds 25 payloads (without the 4-byte opcode) cut from the four
captures by `fixture_gen_test.go` (a one-off: `FOREVER_CAPTURE_PKT` + `FOREVER_WRITE_FIXTURES=1`; the committed
fixtures need neither the capture nor that variable). Each one was rewritten as follows, and the fixture's `rewritten`
list records it — **without any offset**: a delta would let anyone recompute the captured position from the
committed synthetic one, so the notes say only that positions were replaced. Three layouts no capture exercises are
built by hand in `handbuilt_test.go` from the server's writer, field by field, with a sentinel (see "What the tests
pin").

* **Every packed GUID the parse decodes** — the block GUIDs, the destroy and out-of-range lists, the movement
  blocks' GUIDs, every GUID inside the values blocks and the monster moves' transport, face-target and spell-effect
  GUIDs, including the fields this round drops — becomes a synthetic packed GUID of the *same mask and length*, with
  the type byte (bit 15) kept so the object type a parser reads is still right and every other payload byte set to a
  deterministic value. Both the generator and the audit collect them through `wire.Buffer.SetGUIDHook` (the round-2
  seam, `wire/buffer.go:32-47`), never through a per-parser field list: the parse reports even the GUIDs it drops
  (`ReadPackedGUID` → `reportGUID`).
* **The non-GUID identifiers** of a name answer — `GuildClubMemberID` (8 bytes) and the two `VirtualRealmAddress`
  fields — are not GUIDs, so the hook cannot see them. The parser keeps them and records their payload offset
  (`NameIdentifier`, `query.go:34-50`); the generator rewrites the club id to 0 and both realm addresses to 4242 at
  those offsets, and the audit checks every one.
* **The sweep** (`TestFixtureGUIDsAreSynthetic`): all **25 fixtures** are walked through their parser's hook; **42
  non-empty GUIDs** are seen and all are synthetic, and the **2 non-GUID identifiers** of `player_names` are the
  synthetic 0/4242. The four negative subtests (`TestFixtureAuditCatchesRealIdentifiers`) put a different account
  GUID, a non-zero club id, a real realm address and a different face-target GUID back into the payload and each
  must be rejected.
* **The real identifiers are gone, byte for byte**: every packed GUID and account-side number the four captures
  carry (851 distinct values, collected through the same parsers) was grepped against the committed payload hex of
  all 25 fixtures: **0 hits**. The list of real values was kept outside the repository (scratch file) and is not
  committed.
* **Positions** become (1000, 2000, 50), applied to every position component the parse reported (each movement
  block's XYZ, the spline's destination and points), and the orientation becomes 0.5. A component that is exactly 0
  is left alone (rewriting a zero float would also catch every zero `uint32` in the packet). The committed note
  records no offset.
* **Movement times** become 960000 (rewritten with the spline header in front of them, so the pattern is unique even
  when the captured value is 1) and the spline id 4242. `monster_move_face_spot` therefore has a real duration
  (960000, so `MoveTime/4` and `MoveTime*3/4` are 240000 and 720000, not both 0) and its destination is asserted
  (1000 2000 50) like every other move's, not skipped.
* **The undecoded tail** of a player create (PlayerData, then ActivePlayerData for the receiver's own create) is
  zeroed: this round does not parse it, and it carries the account GUIDs of the captured session. The same applies
  to the ItemData tail of the item creates in the owner fixture.
* **Names** in the two name-answer fixtures become runs of `N`/`S` (player name/surname) and `C`/`T` (creature
  name/title) of the same length, so the layout stays identical.
* One fixture is not from a captured server packet: `move_update_other_player` is the captured client's own
  `MovementInfo` under `SMSG_MOVE_UPDATE`, because no capture contains another player's relayed movement (the
  sessions had no other players); it is the client's own bytes, checked by the byte-for-byte round trip.
  `move_teleport` is written from `MoveTeleport::Write` because **no capture contains a teleport** (all four files
  were scanned for `SMSG_MOVE_TELEPORT`); its header says so and it is a structure test only, not traffic.

The opcodes in the fixture file are the core's numbers, before the Classic wire translation.

## What the tests pin

In CI, without the capture and without a server:

* `TestFixtureUpdateObjects` — the four create blocks (creature, player, the character's own ActivePlayer create,
  game object), the three values updates (a health change, a target change, an area change) and the
  destroy/out-of-range lists, to pinned entries, levels, health, speeds, positions and consumption to the last byte.
  The owner create pins the UnitData tail the 80-byte AttackPower fix recovered (guild, area 3428, combo target,
  flags) and the interleaved power pair (2868/2868); the player fixture pins 244/333.
* `TestFixturePauseTimes` — a create with two pause times, and the values size behind them, read as four bytes per
  entry at the end of the movement block.
* `TestFixtureEmpathCreate` — four hand-built unit creates (Empath only, Owner+Empath, Owner only, neither) with the
  same BaseMana sentinel: the Empath-gated MinDamage and Resistances blocks are walked, and the interleaved power
  pair decodes, in all four.
* `TestFixtureVirtualItemsUpdate` — a hand-built unit values update whose visible items change by mask (ItemID in
  one, ItemVisual in the next) with a sentinel field after them; the block must be consumed exactly.
* `TestFixtureQueryCreatureSplits` — 130 entries become 63/63/4, because the count is six bits.
* `TestFixtureGUIDsAreSynthetic` — the hook sweep above: every GUID the parse decoded, including the account GUIDs
  of a name answer that no field keeps, is one the generator wrote, and the name answer's non-GUID identifiers are
  the synthetic 0/4242. `TestFixtureAuditCatchesRealIdentifiers` proves the sweep can fail (four doctored payloads).
* `TestWorldModelMergesEveryDecodedField` — one values update per decoded field (43 rows: ObjectData, UnitData by
  retail bit, GameObjectData by Classic bit), applied to the model, must leave that field's value in the object. It
  found that `readUnitUpdate` stored the six GUID fields (Charm, Summon, CharmedBy, SummonedBy, CreatedBy, Target)
  and Guild without setting their presence flags, so `mergeInfo` dropped all seven; both are fixed.
* `TestSecondSelfCreateResetsTheMover` — a later ThisIsYou create replaces the mover's position (and
  `Mover.SetPosition` abandons a leg in progress, `TestMoverSetPositionAbandonsTheLeg`).
* `TestFixtureMonsterMoves` — the four move kinds plus a compressed path, their destinations and the interpolation's
  endpoints and monotonicity.
* `TestFixtureNames` — both name answers with their synthetic text, and the five declined-name sizes.
* `TestMovementWriterMatchesTheCapturedClient` — `ReadMovementInfo` then `WriteMovementInfo` reproduces seven
  captured client movement payloads byte for byte (and the SMSG_MOVE_UPDATE one); the teleport fixture parses to its
  writer's fields and `BuildTeleportAck` has the size its reader expects.
* `TestWorldModelFeedsFromFixtures` — creates, values, the destroy and out-of-range lists, a seeded spline's
  half-way and arrived positions, a teleport, and the orphan accounting; a spline for an unknown GUID is counted,
  not created.
* `TestFixtureAllocationBound` — a stated allocation bound per parse, measured on this parser (24 for the creature
  create, 24 for the player create, 56 for the 37 KB owner packet, 8 for a values update), and the parse must still
  produce every declared block.
* `TestWorldBoundsStreamedGUIDs` — 25,000 unique creates across as many packets keep the model at `maxObjects`
  (20,000), keep the active mover, and cap both name caches at 10,000; 3,000 movement packets for unknown GUIDs are
  counted and create nothing.
* `TestUnparsableBlockIsSkipped`, `TestUnmodelledMovementBlocksAreWalked`, `TestOuterStructureIsFatal` — a values
  body the parser cannot decode is skipped by its declared size (the next block still parses) and counted by type
  and reason; inertia, advanced flying, drive status, a movement force and a game object's assist data are walked,
  not refused; only an outer count or size that does not fit is fatal.
* `TestUnparsedSummaryIsBounded` — 100,000 skipped blocks whose declared counts are all distinct (so the parser's
  text differs every time) leave one summary key per (type bucket, reason code), keep only the first text of each
  key, and do not grow after the first pass.
* `TestPartialObjectMergesOnlyTheDecodedHead` — the player create's unit head (before the PlayerData tail) is merged
  and the object is marked partial; an undecodable values update for the same object changes no field.
* `TestCreateSplineStartsWhereThePacketSays` — a create spline with elapsed 400 of 1000 ms starts at the packet's
  position and reaches its destination 600 ms later.
* `FuzzUpdateObject` / `FuzzMonsterMove` — the seed corpus is every fixture; `-fuzz` mutates them; neither may panic
  and an update's block count must equal its parsed blocks.
* `TestMoverWalksStraightAndStops`, `TestMoverShortWalkIsASingleLeg`, `TestMoverRefusesWithoutSpeed`,
  `TestMoverFacingMatchesTheServer`, `TestMoverDeadlineStopsWhereTheInterpolationIs`,
  `TestMoverSetPositionAbandonsTheLeg` — the walker against a fake clock and a fake sender: the packet sequence, the
  position advance bounded by speed × elapsed, the arrival `8/7 s` for 8 yards at 7 yd/s within 20 ms, the four axis
  facings against the server's own formula, an interrupted (cancelled) leg stopping at the interpolated position, and
  a position the server puts the character at mid-leg ending the leg there (stop at the new position, not the old
  target). `TestHeartbeatIntervalIsTheClients` was a constant compared
  to itself and is gone; the cadence's provenance is stated above instead.
* `TestWalkCheckAgainstFakeServer` (package `world`) — the walk end to end against round 2's fake world server,
  extended with a create block and a movement reader: the server checks the packet kinds, that every position moves
  toward the target and that each stop lands on it.
* `TestTeleportDuringWalkStartsTheNextPacketAtTheDestination`,
  `TestTeleportOntoATransportIsNotAcknowledged` (package `world`) — a teleport in the middle of a leg is acked only
  after the mover's position, facing and sequence changed, and the first packet after the ack is at the destination;
  a transport/vehicle teleport is reported and not acked, and the run ends with that error.

Outside CI (the private capture, `FOREVER_CAPTURE_PKT=...`): `TestCaptureUpdateObjects`, `TestCaptureMonsterMoves`,
`TestCaptureNameQueries`, `TestCaptureMoveUpdateTeleport` and `TestCaptureClientMovementPackets` parse every such
packet of a capture file and fail on any packet that is not consumed exactly. All four files pass: 1095 update
objects (1990 blocks) in the login file, 6804 monster moves, five player-name answers and one creature answer. After
the fix round the same four files still parse every update and movement packet to its end (1-login: 1095 updates,
1990 blocks, 1810 walked completely and 180 tails skipped by size; 2-movement: 1324/1338/1207/131; 3-combat:
232/332/289/43; 4-party: 633/739/664/75).

## What the live run also showed

* The realm accepts the same `--build 70707` claim as round 2 and the login, world auth and instance socket are
  unchanged.
* The login burst's `SMSG_UPDATE_OBJECT` is enough to model the whole visible area: 109/111 objects (47/49 game
  objects, 54/54 units, 7 items, the character) in 17/7 updates, with six/one out-of-range list entries. 14 objects
  are partial (the item and player creates' undecoded tails), and no block was unparsed.
* `SMSG_MOVE_ROOT` arrives again at logout (as in round 2) and is ignored.
* `CMSG_QUERY_CREATURE` is answered for every requested entry (four batches of 11) and
  `CMSG_QUERY_PLAYER_NAMES` for the one player in range; the creature answers carry the names the nearest-units
  table prints. All 44/45 entries were answered, as in the earlier run.
* Facing is the server's convention now: the +X leg prints 0.00 and the +Y leg 1.57 (the earlier run printed 1.57
  for +X, which was the old `atan2(dx, dy)`).

## Review round 1 fixes

Every item was recounted against ForeverCore at 6517af05 before the change; the code cites the lines. Two reviews
agreed on most findings and the brief's line numbers were re-checked (one is off by 17 lines, noted under item 5).

### Wire

**1. Pause times (HIGH).** `BaseEntity::BuildMovementUpdate` writes the count at `BaseEntity.cpp:281` and appends the
values at `:694-696` through `data.append(PauseTimes.data(), PauseTimes.size())`. The pointer is `uint32 const*`, and
the overload is `append(T const* src, size_t cnt)`, which appends `cnt * sizeof(T)` (`ByteBuffer.h:601-609`). So the
trailer is **4 × count bytes, at the end of the whole movement block** (after Conversation, the last flag-gated
block) — the old code read `count` bytes right after the count. Both are fixed (`update.go:701-711`, values read
after the Conversation block). `TestFixturePauseTimes` puts two entries behind a valid values body: any wrong size
or position makes the values size (and so the block framing) wrong.

The same sweep over every `append(ptr, count)` in the paths this round parses:

| site | element | count | verdict |
|---|---|---|---|
| `BaseEntity.cpp:696` PauseTimes | `uint32` | `size()` | **was wrong**, now 4 × count at the block's end |
| `MovementPackets.cpp:464` create-spline path | `float` (TaggedPosition<XYZ> is 3 floats) | `points × 3` | correct: 12 bytes per point, read as three floats |
| `QueryPackets.cpp:104` creature Flags | `uint32` | `size()` | correct: 4 bytes each; the parser skips 12 for `Flags[3]` |
| `QueryPackets.cpp:108` ProxyCreatureID | `uint32` | `size()` | correct: 4 bytes each; the parser skips 8 for `[2]` |
| `QueryPackets.cpp:137/140` QuestItems/QuestCurrencies | `int32` | `size()` | correct: 4 bytes each |
| `ObjectGuid.cpp:1108` packed GUID | `uint8` | `outputByteIndex` | correct: one byte each |
| `UpdateData.cpp:80` | `ByteBuffer` overload | — | whole buffer, no count semantics |
| `QueryPackets.cpp:337/338/375/383/448/471` | — | — | outside this round's parsed packets (NPC text, game-object query, quest POI); not walked |

**2. Owner AttackPower block (HIGH).** `UpdateFields.cpp:1189-1211` writes twenty values: AttackPower,
AttackPowerModPos/Neg, Multiplier (float), ModSupport, RangedAttackPower and its ModPos/Neg/Multiplier/ModSupport,
the Classic zero, MainHandWeaponAttackPower, OffHandWeaponAttackPower, RangedWeaponAttackPower, SetAttackSpeedAura,
the second Classic zero, Lifesteal (float), MinRangedDamage, MaxRangedDamage, ManaCostMultiplier. Every one is four
bytes: **20 × 4 = 80**, not 88 (`values.go:357-363`). The eight extra bytes shifted GuildGUID, the 70425 three
int32, CurrentAreaID and ComboTarget; the new `create_owner` fixture (block 8 of the login burst's largest update,
ThisIsYou, TYPEID_ACTIVE_PLAYER) now decodes guild (empty), area 3428, combo target (empty), flags 0x8/0x40800 and
native display 56658. The unit tail is decoded before the PlayerData/ActivePlayerData tail the parser leaves to the
declared size; the fixture's tail is zeroed by the generator and the block's declared size covers it.

**3. Power arrays (HIGH).** `UpdateFields.cpp:1089-1093`:
`for i in 0..9 { data << Power[i]; data << MaxPower[i]; }` — interleaved, not ten then ten (`values.go:277-283`).
The old code read the first ten wire int32 as Power (so Power[1] was MaxPower[0], Power[2] was Power[1], …) and the
next ten as MaxPower (P5..M9); both arrays were wrong from index 1 on, and an owner create's Maximum was shifted a
further 40 bytes into the regen block. Fixed. Evidence: the player fixture (level-4 mage) is 244/333, the owner create 2868/2868, the creature
0/0 — `TestFixtureUpdateObjects` pins those real values; the old `len(c.Info.Power) == 0` assertion on an array
could never fail and is gone. 244/333 is the capture's own mana pair, and the owner's 2868/2868 is the character's.

**4. Visible items in an update (HIGH).** `VisibleItem::WriteUpdate` (`UpdateFields.cpp:880-935`) writes a 12-bit
Classic mask (retail bits 6..11 → classic 7..12; bit 0 is the present gate, bits 1/2 are the HasTransmog/HasIllusion
*bits*), then only the changed fields. It is not a fixed 27 bytes. `readVisibleItemUpdate` (`values.go:859-902`)
parses it by mask, and `readUnitUpdate` calls it for each of the three arrays. No capture carries a virtual-item
change (all four update streams were scanned: none has a classic unit bit at the item positions), so
`TestFixtureVirtualItemsUpdate` is hand-built from the server's writer: item 0 changes ItemID (mask 0b1001), item 1
changes ItemVisual (present + classic bit 8), and an attack-round-base sentinel follows. The old 27-byte skip
consumed the sentinel and left the block unconsumed.

**5. The Empath flag (MED).** `Unit::GetUpdateFieldFlagsFor` sets Empath (0x08) for a unit carrying an Empathy aura
cast by the receiver (`Unit.cpp:15015-15026`). The create writes MinDamage/MaxDamage/MinOffHandDamage/MaxOffHandDamage
under `Owner | Empath` (`UpdateFields.cpp:1124-1130`) and Resistances[7] under `Owner | Empath` (`:1166-1172`); the
brief's `:1141` is PetNextLevelExperience — the MinDamage gate is at 1124. Both gates are implemented
(`values.go:324-327`, `:346-350`), and UnitAll (0x04) now also selects the power-regen block it gates at `:1081`
(never set by the server, but the flag byte is parsed). `TestFixtureEmpathCreate` builds four unit creates (Empath
only, Owner+Empath, Owner only, neither) with the same BaseMana sentinel: all four consume exactly, so the gate is
proven in both directions.

**6. Facing (MED).** `Position::GetAbsoluteAngle` is `NormalizeOrientation(atan2(dy, dx))` (`Position.h:136-141`),
so +X faces 0 and +Y faces π/2. `OrientationTo` and its comment now use `atan2(dy, dx)` (`movement.go:264-270`).
`TestMoverFacingMatchesTheServer` derives the four axis values from that formula; the live run now prints 0.00 for
+X and 1.57 for +Y (the first run printed 1.57 for +X because the old code measured from north).

### Robustness

**7. Bounds.** `maxObjects = 20000` and `maxNames = 10000` (`world.go:101-106`). A create for a new GUID at the bound
evicts the longest-unseen object that is not the active mover (`evictLocked`, with a lazy touch-order queue that is
rebuilt when it outgrows the model, so the eviction cost stays amortised and the queue bounded). `ApplyMonsterMove`,
`ApplyMoveUpdate` and `ApplyTeleport` no longer create objects for unknown GUIDs: they count `UnknownMoves` and
return. Both name caches refuse a *new* key at their bound (an existing key still updates). `SetActiveMover` names
the object a walk drives. `TestWorldBoundsStreamedGUIDs` streams 25,000 unique creates and 3,000 movement packets
for unknown GUIDs.

**8. Teleports.** `handle` now applies the destination, facing and sequence to the Mover *before* the ack
(`session.go:855-885`), sets `report.WalkClientPos`, and refuses transport/vehicle teleports with
`teleport onto a transport: not handled` and an error (no ack). A teleport during a leg stops the leg at the
destination (`Mover.SetTeleport` + `ErrTeleported`, `mover.go:62-118`, `:187-193`). Both paths are tested against the
fake server.

**9. Parse errors are not session-fatal.** A values body that cannot be decoded is skipped by its declared size and
carried as `WorldFragment.Unparsed` with the reason code (round 3: a finite `ParseReason`); the block after it still
parses (`update.go:290-340`). The walk summary counts them by object type and reason and prints the partial-object
count. Inertia, advanced flying, drive
status, removed/active movement forces, a game object's assist data and a monster move's spell-visual data are now
walked by their exact size (`movement.go:102-165`, `update.go:538-600`, `values.go`, `movement_in.go`), so they no
longer stop the run; only an outer count or size that does not fit the packet is fatal.

**10. Trust.** `mergeInfo` merges only the presence bits the parser set. Those bits are set strictly in wire order and
decoding stops at the first region it cannot decode, so every set bit is a field that ended before that region — that
is the proof the rule asks for, and it is stated at the call site (`world.go:225-232`) and above `mergeInfo`. An
unparsable block merges nothing and marks the object partial; an object with any skipped tail is partial
(`Counts.Partial`, printed by walk-check). The fixtures' tails are now decoded wherever the layout allows: the owner
create's unit tail decodes (item 2), the player's unit head decodes before its PlayerData tail, and the virtual-item
update is consumed exactly (item 4).

**11. Walker timing.** `MoveTo` wakes at `min(next heartbeat, arrival)`, interpolates by elapsed × run speed, sends
the stop with the computed arrival's position and timestamp, and never snaps. The flag word comes from the create
block (`Mover.Flags`, base = create flags without the moving bits). A deadline stops at the interpolated position and
returns an error. Tests: 8 yd at 7 yd/s arrives at 8/7 s within 20 ms; no packet moves farther than speed × elapsed;
the four axis facings; an interrupted leg stops short of the target.

**12. Create spline and query count.** The create spline now interpolates from the packet's position for
`duration − elapsed` (`world.go:243-262`). `BuildQueryCreature` caps at 63 and splits (`movement_in.go:321-345`);
`queryNames` sends each packet and waits for its answer.

### Hygiene

**13.** The fixture generator no longer records a position delta (the committed notes say only that positions were
replaced), and the live walk table now uses offsets from `<spawn>` (`<spawn + 8 yd>`), with Y/Z offsets 0.00. The
hygiene test gained a coordinate-triple pattern with an allow-list of the synthetic fixture points and a negative
subtest (`internal/forever/bnet/hygiene_test.go`).

### Tests the review found empty

* `TestFixtureAllocationBound` was a bound of 256/512/128 against measured 14/15/37/3: tightened to 24/24/56/8 and
  made to assert that every declared block parsed.
* `TestHeartbeatIntervalIsTheClients` compared a constant to itself: deleted. The cadence's provenance (not
  measurable from these captures; 500 ms is the client tick; the two captured heartbeats are 138 s apart) is stated
  in "The heartbeat cadence".
* The `Health != 97 &&` clause in `TestWorldModelFeedsFromFixtures` accepted either fixture and proved neither: it
  is replaced by exact per-GUID health assertions for the creature (12191), the player (97) and the values update
  (884).
* `move_teleport`: no capture contains a `SMSG_MOVE_TELEPORT` (all four files scanned), so the fixture header says
  it is hand-built from the writer and is a structure test only. Recount: `MoveTeleport::Write`
  (`MovementPackets.cpp:811-816`) writes `MoverGUID`, the sequence, `Pos` as `TaggedPosition<Position::XYZ>` (three
  floats) and a *separate* `Facing` float (`MovementPackets.h:345`, filled from the destination's orientation in
  `Unit.cpp:13699`); the old fixture (and parser) carried four position floats plus facing. Fixed, and the test now
  asserts the facing (and `O == Facing`).

## Review round 2 fixes

Recounted against ForeverCore at 6517af05. Items that repeated round 1 are not repeated here.

**1. Private identifiers in the committed fixtures (HIGH).** `SMSG_QUERY_PLAYER_NAMES_RESPONSE` reads and drops
`AccountID`, `BnetAccountID` and `GuidActual` as packed GUIDs and `GuildClubMemberID`/`VirtualRealmAddress` as plain
numbers (`query.go:88-99`), so the old generator — which collected only `PlayerName.GUID` — left the capture's real
account id, bnet account id, club member id and realm address in the committed `player_names` payload. Fixed at the
seam the round-2 rule names: every parser (updates, splines, name answers, teleport, relays, client movement) has an
unexported audit variant that installs `wire.Buffer.SetGUIDHook` (`update.go:142-147`); the generator and the audit
collect GUIDs through it (`fixture_gen_test.go`, `objects_test.go:279`). For the non-GUID identifiers the parser now
keeps the values and records their offset (`NameIdentifier`, `query.go:34-50`), the generator patches them at those
offsets (club id 0, both realm addresses 4242; `fixture_gen_test.go:65-70`, `:352-366`), and the audit checks them
(`objects_test.go:246-277`). Negative subtests: a different account GUID, a non-zero club id, a non-synthetic realm
address and a replaced face-target GUID each fail the audit (`objects_test.go:304`).

**2. Monster-move GUIDs (HIGH).** `MonsterMove` now keeps `TransportGUID`, `FaceGUID` and `SpellEffectTarget`
(plus `FaceSpot`), which are the fields the old parser skipped (`movement_in.go:25-42`, `:67-79`, `:118-123`). The
generator rewrites all of them through the hook, and the audit sees them. Mirror check in the schema:
`MovementSpline::TransportGUID`, the face-target branch's `FaceGUID` and
`MonsterSplineSpellEffectExtraData::TargetGUID` (`MovementPackets.cpp:271-280`, `:331-342`).

**3. The sweep.** See "How the fixtures were made": 25 fixtures, 42 GUIDs, all synthetic, 2 non-GUID identifiers,
and 0 hits when the 851 distinct real identifiers of the four captures are searched in the committed payload bytes.

**4. `move_teleport` (HIGH).** Fixed to the server's layout and asserted; see "Tests the review found empty".

**5. The values merge (MED).** `mergeInfo` was right; the update decoder was not. `readUnitUpdate` stored Charm,
Summon, CharmedBy, SummonedBy, CreatedBy, Target and Guild but never set their presence flags, so a values update
changed the struct and `mergeInfo` dropped every one of them — `values_target`'s target never reached the model.
Fixed (`values.go:571-589`, `:766-768`), and `TestWorldModelMergesEveryDecodedField` (`objects_test.go:677`) is the
one-row-per-field proof. `TestWorldModelFeedsFromFixtures` now also reads `values_target`'s target back out of the
model.

**6. `monster_move_face_spot` (LOW).** The generator rewrites the spline's movement time (uniquely, with the header
as context) to 960000, so the interpolation checks use 240000 and 720000 instead of 0/0; the fixture's destination is
1000 2000 50 and is asserted like every other move's (`objects_test.go:390-402`).

**Live run after the fixes.** One `forever walk-check` (build 70707, 8 yd east and back, 3 s pause, 2026-11-18):
`walk-check: from <spawn> to <spawn + 8 yd east> and back to <spawn>` and
`walk-check: client position <spawn> (orientation 3.14), 293 monster moves, 0 movement relays, 0 teleports, 0 map
changes, 1 names asked`; 65 objects (13 partial, 0 unparsed blocks) in 9 updates. Coordinates are offsets from
`<spawn>`; the run's real positions are not committed. The private test stack was stopped before the run and stopped
again afterwards, and the temporary character was erased (`character erase`), leaving the two pre-existing rows.

**7. Mover (MED).** The timeout and cancellation branches already call `advance` and stop at the interpolation
(`mover.go:183-186`, `:203-207`) and the deadline test covers the cancellation; `SetPosition` now abandons a leg in
progress (`mover.go:75-83`, tested by `TestMoverSetPositionAbandonsTheLeg`), and a later ThisIsYou create resets the
mover's position instead of keeping the first create's (`session.go:923-955`, `TestSecondSelfCreateResetsTheMover`).

## Review round 3 fixes

**1. The skipped-block summary was keyed by parser text (HIGH).** Every skipped values block incremented
`World.unparsed[unparsedKey(b)]`, and the key embedded the parser's error text. The text was truncated to 120 bytes,
but its cardinality was not: seven parser errors carry a server-controlled count (`values.go:246`, `:394`, `:530`,
`:960`, `:985`, `:1032`, `:1131`), so a server that varies one number per block minted a new key per block while the
object and name maps stayed capped.

Fixed by classifying the failure into a finite reason set before counting. `ParseReason`
(`update.go:31-98`) has seven codes — short read, count over limit, unknown flag combination, mask too large, size
mismatch, unsupported fragment, other. The parser returns the code with the error (`parseError`, `parseFail`,
`reasonOf`: the tag, `wire.ErrShort`, else other), and `WorldFragment.Unparsed` is now that code, with the text in
`UnparsedDetail` (`update.go:117-121`). The codes the values path returns today are short read, count over limit and
mask too large; size mismatch and unknown flag combination are tagged on the outer block framing and the movement
block, which are fatal to the packet and never reach the summary; `unsupported fragment` is reserved for a fragment
this round would learn to reject (today an updateable fragment without a layout is skipped as a partial tail instead,
`ValuesComplete`), and `other` is the fallback for an untagged error. The summary key is `(unparsedKind, reason)`
(`world.go:289-311`): the 15 object types `TypeName` names, one `values update` bucket (a values block carries no
type) and one `unknown type` bucket for any other type byte, times the seven codes — `maxUnparsedKeys = 17 * 7 = 119`.
The first text of each key is kept in `World.unparsedDetail` for the walk summary (`world.go:229-240`); later blocks
of the same key keep no text at all, so the summary's memory is fixed after at most 119 blocks. The walk-check line
prints it after the count (`session.go:603-609`). The mask check is new (`update.go:483-499`): the content mask is one
byte, so a fragment list that needs more than eight bits is now skipped whole (reason `mask too large`) instead of
being silently misread.

### Every server-keyed map and slice in internal/forever, and what bounds it

| Structure | Key or length source | Bound |
|---|---|---|
| `objects.World.objects` | GUIDs from creates, values updates, destroys | `maxObjects` 20000; a new GUID at the bound evicts the longest-unseen non-mover (`world.go:107-108`, `evictLocked`) |
| `objects.World.players`, `.entries` | name-answer GUIDs, creature entries | `maxNames` 10000 each; a new key at the bound is refused, an existing one still updates (`world.go:612-631`) |
| `objects.World.order` | one entry per touch | `4*len(objects)+1024`, rebuilt from the live objects past that (`touchLocked`, `rebuildOrderLocked`) |
| `objects.World.unparsed` | (object type bucket, reason code) | `maxUnparsedKeys` 119 — closed sets, this fix |
| `objects.World.unparsedDetail` | one parser text per summary key | one string per key ≤ 119; only the first block of a key keeps one |
| `objects.Update.Blocks` | declared block count | `maxBlocks` 512 (`update.go:26`, checked before the loop) |
| `objects.Update.Destroy`, `.OutOfRange` | declared destroy/out-of-range counts | total ≤ `maxBlocks*4` = 2048 (`update.go:240-243`) |
| `objects.Update.GUIDs` | every packed GUID the packet carries (blocks, movement, values) | one entry per packed GUID read; a packed GUID is at least one payload byte and a frame is at most `wire.MaxFrame` (4 MiB), so ≤ 4.2M per packet, freed with the `Update` |
| `objects.MoveBlock.GUIDs` | mover, standing-on, transport, removed/active forces, victim, phasing | the count-driven list is capped at `maxDynamicSize` 4096; the rest is a fixed handful per block, and each GUID is one payload byte |
| `objects.MoveBlock.PauseTimes` | declared pause-time count | `maxDynamicSize` 4096 entries (16 KiB) |
| `objects.CreateSpline.Points`, `objects.MonsterMove.Points`, `.PackedDeltas` | declared point/delta counts | 16-bit counts, then `maxDynamicSize` 4096 (the field is 16 bits, so the check is belt and braces) |
| `objects.Info.audit` | one entry per GUID field decoded | a fixed few dozen per block (the layout has a fixed GUID list), so bounded by the block, not by any count |
| `objects` name-answer results | declared player/creature counts | 50 players, 32 creatures (`query.go`); each name is length-capped (player 64 bytes) or payload-capped |
| `world.Report.Counts` | opcode names | the static name table plus `maxUnknownReportKeys` 64 unknown wire numbers, then one `otherUnknownKey` (`session.go:162-178`) |
| `world.Report.Characters`, `.Regionwide`, race unlock/appearance/limit/warband lists | declared counts in the character list | 100 / 1000 / 128 / 4096 / 256 / 64, refused above (`character.go:63-70`, `:112-115`) |
| `world.ObjectSummary.Unparsed` | copy of the world summary | ≤ `maxUnparsedKeys` 119 |
| `world.Report.Units` | nearest units | limit 5 |
| `worldconn` frame body | header size | `wire.MaxFrame` 4 MiB (`frames.go:138`) |
| `worldconn` `handshakeNames` | one name per handshake packet | `maxHandshakeNames` 64 (`frames.go:53-55`) |
| `wire` inflater output and pending chunk | declared uncompressed size | `MaxUncompressed` 16 MiB (`inflate.go:21-23`, `:110`) |

No other map or slice keyed or lengthed by server data was found unbounded.

**Test.** `TestUnparsedSummaryIsBounded` (`internal/forever/objects/robustness_test.go:219`) streams 100,000 create
blocks over four object types, each declaring a distinct over-limit count (4097, 4098, …) so the parser's text
differs every time. It asserts the summary holds exactly the four (type, count over limit) keys, no more than
`maxUnparsedKeys`, that each key keeps the first block's text, and that after 99,000 further blocks both the key
count and the total text bytes are unchanged; all 100,000 blocks are still counted.

## Open questions for the combat round

1. **Values updates for links.** `PlayerData`, `ActivePlayerData` and the vendor fragment are not decoded; a combat
   round that needs combo points, spell visuals, or a vendor's item list has to add them (and the player tail is the
   largest table left).
2. **Item and container values.** Items were skipped by size; equipment display ids (`VisibleItem`) and item
   enchantments are what a loot or gear view needs.
3. **The movement relays for other players** were never exercised live: the audit server has no other players, so
   `SMSG_MOVE_UPDATE` is pinned by the fixture only.
4. **Direction of a falling jump.** One captured heartbeat carries `hasFallDirection` with sin 1/cos 0; the sign
   convention of the client's fall direction is not settled (it never matters for a walk).
5. **Heartbeat cadence** could be measured from a capture of a client that actually walks (the four here are idle).
6. **Map changes and flight paths.** `SMSG_NEW_WORLD`, `SMSG_TRANSFER_PENDING` and taxi splines are the next
   movement surface; a bot that quests has to travel between zones.
7. **Movement time drift.** The server maps the client's clock with a time-sync delta whose fallback is the server
   clock; a long run's heartbeat times should be checked against `AdjustClientMovementTime` once the client runs for
   hours.
