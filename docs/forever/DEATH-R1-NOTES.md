# Forever death, round 6 notes

Scope: the client detects that its character died and stops (round 4 leaves it there with `ErrSelfDead`). This round
makes it do what a player does next: release the spirit, learn where its corpse is, run back to it and reclaim it, or
accept a resurrection that another player or a spirit healer offers; and know the same about other units (a party
member is dead, a ghost, was resurrected).

Code: `internal/forever/death` (the death wire layer: parsers and writers, both directions),
`internal/forever/objects` (the ghost and death state of every unit in the world model),
`internal/forever/world` (the death flow: `Release`, `CorpseLocation`, `RunToCorpse`, `ReclaimCorpse`,
`AcceptResurrect`, `DeclineResurrect`, `SpiritHealer`, and `forever death-check`), `cmd/forever-agent`.

Everything below was checked in ForeverCore at 6517af05 and against a local capture of a real session (not
committed, and not named here).

## How death is detected today, and what changes

Round 4 detects death as health 0 in the values update, or an attack stop whose `NowDead` bit is set. That is the
*corpse* state (`Unit::setDeathState(JUST_DIED)` sets health 0, `Unit.cpp:9584-9587`). Releasing the spirit is a
different state: `Player::BuildPlayerRepop` calls `setDeathState(DEAD)` and then **`SetHealth(1)`**
(`Player.cpp:4714-4715`), so a ghost has 1 health and health 0 alone would call it alive. What marks a ghost on the
wire is the aura 8326, `SPELL_AURA_GHOST` (`Player.cpp:4688` applies it, `:4767` removes it on resurrection,
`SpellAuraEffects.cpp:1913-1930` sets the server-side visibility):

* the aura record arrives as `SMSG_AURA_UPDATE` on the ghost;
* its removal (or an `UpdateAll` list without it) is the resurrection.

The client cannot read `PLAYER_FLAGS_GHOST` (in `PlayerData`, which round 3 leaves to its declared size), and the party
round's state carries `StatusGhost` for party members; the aura is the unit-level signal both can see, so the
model keys the ghost state on aura 8326. A unit is dead when its health is 0 **or** it has the ghost aura; it is
resurrected when the ghost aura goes away and its health is above 0 again.

## The flows, with the server's own lines

### Death

`Player::KillPlayer` (`Player.cpp:4842-4870`): `setDeathState(CORPSE)` (health 0, stand state stand,
`Unit.cpp:9549-9590`), rooted, the release timer, `CalculateCorpseReclaimDelay` and `SMSG_CORPSE_RECLAIM_DELAY`
(`:4865-4868`). No corpse object yet: the corpse is created on release.

The client sees: health 0 in the values, `SMSG_STAND_STATE_UPDATE` (state 0, `MiscPackets.cpp:342-346`),
`SMSG_MOVE_ROOT`, `SMSG_CANCEL_COMBAT`, `SMSG_CORPSE_RECLAIM_DELAY`.

### Release

`WorldSession::HandleRepopRequest` (`MiscHandler.cpp:63-91`): refuses when alive or already a ghost, or while
teleporting, or with `SPELL_AURA_PREVENT_RESURRECTION`; if the death timer has not run yet it calls `KillPlayer`
first; then `RemovePet`, `BuildPlayerRepop`, `RepopAtGraveyard`.

`Player::BuildPlayerRepop` (`Player.cpp:4679-4734`):

1. `SMSG_PRE_RESSURECT` with the player's GUID (`:4681-4683`, writer `MiscPackets.cpp:292-297`),
1. aura 20584 for the wisp racial if known, then **aura 8326** (`:4688`),
1. quests failing on death, `RemoveAurasWithInterruptFlags(Release)`,
1. a corpse object at the player's location (`CreateCorpse`, `:4885-4936`; `_corpseLocation` = the player's
   position),
1. `setDeathState(DEAD)`, **`SetHealth(1)`**, `SetWaterWalking(true)`, unrooted (`:4714-4719`),
1. `CalculateCorpseReclaimDelay` and `SMSG_CORPSE_RECLAIM_DELAY` (`:4724-4728`),
1. `corpse->ResetGhostTime()` — the reclaim delay starts here (`:4730`).

`Player::RepopAtGraveyard` (`Player.cpp:5211-5270`) picks the closest graveyard (battlefield, instance entrance,
then `GetClosestGraveyard`), teleports there (`TeleportTo`), and when the player is dead sends
`SMSG_DEATH_RELEASE_LOC` with the graveyard's map and location (`:5255-5264`). **A graveyard on another map is a map
change**: the teleport becomes `SMSG_TRANSFER_PENDING`/`SMSG_NEW_WORLD` on the client. This round handled only a
same-map graveyard; the map-change round has since merged, and `Release` follows the far transfer (see "Fix round 3"
below).

The capture's release:

```
C->S CMSG_REPOP_REQUEST (1 byte: the CheckInstance bit, false)
S->C SMSG_PRE_RESSURECT (the character)
S->C SMSG_SPELL_GO spell 8326 (the ghost aura)
S->C SMSG_AURA_UPDATE slot 2, spell 8326, self cast | negative
S->C SMSG_MOVE_SET_RUN_SPEED 8.75
S->C SMSG_MOVE_SET_SWIM_SPEED 5.90
S->C SMSG_MOVE_SET_WATER_WALK
S->C SMSG_MOVE_UNROOT
S->C SMSG_CORPSE_RECLAIM_DELAY 30000
S->C SMSG_MOVE_TELEPORT to the graveyard (same map)
S->C SMSG_DEATH_RELEASE_LOC map 0, the graveyard location
```

`CheckInstance` is written as one bit (`MiscPackets.cpp:304-307`) and the capture's real client sends 0 outdoors.
The dungeon case (where the client may set it) is in a local capture of a dungeon session, which holds no death at
all (see below), so the bit is sent false and its dungeon meaning is an open question.

### The ghost's movement rules

* Speed: the ghost's 8.75 yd/s is the speed aura inside 8326 (the comment at `CharacterHandler.cpp:1649-1650`:
  "auras SPELL_AURA_GHOST, SPELL_AURA_INCREASE_SPEED(why?), SPELL_AURA_INCREASE_SWIM_SPEED(why?)"). The server
  announces it as `SMSG_MOVE_SET_RUN_SPEED` (`MoveSetSpeed::Write`, `MovementPackets.cpp:697-704`: packed GUID,
  uint32 sequence, float speed) and `SMSG_MOVE_SET_SWIM_SPEED`; the client must apply it to its mover or it walks
  back at the living 7.0 yd/s and the run takes 25% longer than it should.
* Water: `BuildPlayerRepop` sets water walking (`Player.cpp:4717`) and the server sends
  `SMSG_MOVE_SET_WATER_WALK` (`Unit::SetWaterWalking`, `Unit.cpp:14233-14267`, a `MoveSetFlag`: GUID + sequence).
  The aura is what makes the flag legal (`ValidateMovementInfo` strips water walking *without* the aura,
  `MovementHandler.cpp:49-141`); a ghost has it. The client tracks the flag and includes it in its movement packets.
* Nothing else differs: the server has no speed or rate check in `HandleMovementOpcode` (round 3 notes: the
  position in the packet is the truth), so a ghost's walk is the same straight line or navmesh route at the
  announced speed. There is no collision in this client at all.
* Falling below the map's minimum height sends the player to the graveyard (`MovementHandler.cpp:600-620`): a ghost
  that cuts a corner under the terrain is released *again*, which is why the run uses the navmesh when a directory
  is given and a straight line otherwise, never an invented height.

### The corpse's location

`WorldSession::HandleQueryCorpseLocation` (`QueryHandler.cpp:140-190`): answers a `CorpseLocation` only when the
queried player exists, has a corpse, and `_player->IsInSameRaidWith(player)`; for the player itself that is true
(`Player::IsInSameRaidWith` is `p == this || ...`, `Player.cpp:2183-2185`). The answer is
`SMSG_CORPSE_LOCATION` (`CorpseLocation::Write`, `QueryPackets.cpp:393-405`):

```
1 bit   Valid; flush
packed GUID Player
int32  ActualMapID        (the corpse's map, or the dungeon entrance map for a dungeon corpse)
int32  MapID
packed GUID Transport     (empty; transport corpses are a TODO in the handler)
float X, Y, Z             (TaggedPosition<XYZ>)
```

The client knows where it died, so the query is a confirmation, not the only source: the flow records the death
position and updates it from this answer. `HasCorpse()` is the `_corpseLocation` the server keeps for the current
map; after a same-map release it is the corpse on the death map.

### Reclaim

`WorldSession::HandleReclaimCorpse` (`MiscHandler.cpp:472-503`):

* refuses silently when alive, in an arena, not a ghost, without a corpse, **before `corpse->GetGhostTime() +
  GetCorpseReclaimDelay(...)`** (`:486-488`), or **farther than `CORPSE_RECLAIM_RADIUS` = 39 yards**
  (`Corpse.h:38`, `:490-491`);
* otherwise `ResurrectPlayer(0.5)` and `SpawnCorpseBones` (`:497-500`).

The delay the server announces (`SMSG_CORPSE_RECLAIM_DELAY`, `MiscPackets.cpp:277-282`, one uint32 of milliseconds)
is the one to respect: `CalculateCorpseReclaimDelay` (`Player.cpp:28245-28284`) returns
`corpseReclaimDelay[count]` from `{30, 60, 120}` seconds (`:175`, `DEATH_EXPIRE_STEP` 5 min and
`MAX_DEATH_COUNT` 3 at `:167-168`). The packet arrives at the death and again at the release; the release one is the
deadline (`ResetGhostTime` at `:4730` is what the handler compares against), so the client takes the last one.
The reader ignores the corpse GUID entirely (`ReclaimCorpse& /*packet*/`, `:472`), but a real client sends its
corpse's GUID; this client sends the corpse object it saw (owner == self) or an empty GUID when the corpse is not
in its model.

A refusal is **silent**: no packet, no error. The client therefore treats "no resurrection within a bounded wait"
after sending the reclaim as `ErrReclaimRefused`, and it never sends before the announced delay or from outside 39
yards (both checks are the server's own). The core's Classic hardcore check refuses the same way
(`RefuseHardcoreResurrect`, `MiscHandler.cpp:495`): a hardcore character cannot reclaim, and the client cannot see
that beforehand either, so it is the same `ErrReclaimRefused` and never a hang.

### A resurrection another player offers

`Spell::EffectResurrectNew` / `EffectResurrect` / the area-aura form
(`SpellEffects.cpp:485-492`, `:3825-3834`, `:5652-5666`) call `Player::SetResurrectRequestData`
(`Player.cpp:25470-25480`) and `Spell::SendResurrectRequest` (`Spell.cpp:5554-5588`), which sends
`SMSG_RESURRECT_REQUEST` (`SpellPackets.cpp:997-1011`):

```
packed GUID ResurrectOffererGUID
uint32      ResurrectOffererVirtualRealmAddress
uint32      PetNumber
int32       SpellID
11 bits     Name length; 1 bit UseTimer; 1 bit Sickness; flush; name bytes
```

For a player caster the name is empty (the client looks the guid up) and `Sickness` is false; for a creature caster
the name is sent. The Classic build clears `UseTimer` when there is no sickness (`Spell.cpp:5574-5579`), so the
offer waits for the client without a countdown.

`WorldSession::HandleResurrectResponse` (`MiscHandler.cpp:507-535`): `Response` 0 accepts, 1 declines, 2 times out
(`:513-518`); an accept requires `IsResurrectRequestedBy(Resurrecter)` (`:520-521`) and then
`Player::ResurrectUsingRequestData` (`Player.cpp:28079-28126`): **teleport to the resurrecter's location first**
(`TeleportTo(_resurrectionData->Location)`), then `ResurrectPlayer(0.0f, false)`, the health and mana the offer
carried, and `SpawnCorpseBones`. A resurrecter on another map is therefore a map change (reported, not handled);
on the same map it is an ordinary teleport the client already acks.

The answer is `CMSG_RESURRECT_RESPONSE` (`ResurrectResponse::Read`, `MiscPackets.cpp:321-325`): packed GUID
`Resurrecter`, uint32 `Response`.

### The spirit healer

`GossipOptionNpc::SpiritHealer = 4` (`GossipDef.h:43`). The option is only shown to a dead player
(`Player.cpp:15045-15049`); choosing it makes the creature cast spell 17251
(`Player.cpp:15316-15319`). The client then confirms with `CMSG_SPIRIT_HEALER_ACTIVATE`
(`SpiritHealerActivate::Read`, `NPCPackets.cpp:242-245`), and `WorldSession::HandleSpiritHealerActivate`
(`NPCHandler.cpp:280-294`) checks `UNIT_NPC_FLAG_SPIRIT_HEALER` (0x4000, `UnitDefines.h:334`) and the 5-yard
interaction distance (`GetNPCIfCanInteractWith`, `Player.cpp:1936-1990`, `INTERACTION_DISTANCE` 5.0,
`ObjectDefines.h:24`) and calls `SendSpiritResurrect` (`NPCHandler.cpp:296-324`): `ResurrectPlayer(0.5f, true)`,
25% durability loss, bones, and a teleport to the corpse's graveyard when it differs from the ghost's.

The capture shows exactly this flow:

```
C->S CMSG_SET_SELECTION (the healer)
C->S CMSG_TALK_TO_GOSSIP (the healer)
S->C SMSG_GOSSIP_MESSAGE: one menu, one option id, OptionNPC 4 (SpiritHealer), "Return me to life."
C->S CMSG_GOSSIP_SELECT_OPTION: the healer, that menu and option id, empty promotion code
S->C SMSG_SPELL_GO: the healer casts 17251
C->S CMSG_SPIRIT_HEALER_ACTIVATE (the healer)
C->S CMSG_CLOSE_INTERACTION (the healer)
S->C SMSG_DEATH_RELEASE_LOC map -1, zero location    (ResurrectPlayer's "you are alive" marker)
S->C the aura 8326 removed, run speed 7.0, land walk, unroot, health restored
```

The client therefore parses only the gossip header and the option list (enough to find the SpiritHealer option and
its id) and the quest-text list, and sends the four client packets above. That is the minimum the server needs; no
gossip text is displayed.

### The server reviving the character directly

`Player::ResurrectPlayer` always sends `SMSG_DEATH_RELEASE_LOC` with **MapID -1 and a zero location**
(`Player.cpp:4756-4758`) before it removes the ghost aura. That is what a console revive, a spirit healer and an
accepted resurrection have in common, and the capture's revive marker is exactly that packet. The client treats
`MapID == -1` as "the server revived me": the ghost phase ends, the phases it entered recover on the health and
aura updates that follow, and the command reports `revived by the server` when no client action of its own caused it.

### Stand state

`SMSG_STAND_STATE_UPDATE` (`StandStateUpdate::Write`, `MiscPackets.cpp:342-346`): uint8 state, uint32 anim kit. The
death packet is state 0 (stand). It is parsed for the log and changes no state: the stand state is already decoded
from the values.

## The three recurring mistakes, checked in every parser of this round

1. **`append(ptr, count)` writes `count * sizeof(T)` bytes.** Sites on this round's paths:

   | site | element | verdict |
   |---|---|---|
   | `NPCPackets.cpp:80` gossip text `QuestFlags` | `int32[4]` | fixed four int32 (16 bytes), read as four `int32`; not a count |
   | `GossipMessage::Write` `Size<uint32>` on the option and text lists | — | explicit uint32 counts, each bounded |
   | `SpellPackets.cpp:1008` resurrect name | `SizedString::Data` | bytes, not elements |

   Nothing else on the paths is an `append(ptr, count)`.

2. **Sized strings.** Three forms here:
   * `SMSG_RESURRECT_REQUEST`'s `Name`: `SizedString` with an **11-bit** length, written after the option bits
     (`SpellPackets.cpp:1003-1008`); no NUL; the parser reads exactly that many bytes.
   * `CMSG_GOSSIP_SELECT_OPTION`'s `PromotionCode`: `SizedString` with an **8-bit** length in the bit block, then
     the bytes (`NPCPackets.cpp:196-203`). The capture's option carries an empty code (length 0).
   * `SMSG_GOSSIP_MESSAGE`'s option strings: `Text` and `Confirm` are 12-bit `SizedString`s and
     `FailureDescription` is an 8-bit `SizedCString` (its length counts the NUL and an empty one writes no bytes),
     read in that order after `FlushBits` (`NPCPackets.cpp:52-69`). The client's gossip parse must keep those three
     kinds apart.
   The quest-text title is a 9-bit `SizedString` (`NPCPackets.cpp:87-92`).

3. **A fixture built by the test's own writer proves nothing.** The parser fixtures are cut from a local capture
   of a real session and rewritten; the parsers must consume each to its last byte, and the private cross-check
   parses every such packet of a capture. The hand-built fixtures (no capture has the case) are labelled as such in
   the fixture file: the resurrect request, the corpse location answer, the corpse transport answer, the zero-delay
   reclaim and the declined response.

## What was built

* `internal/forever/death`: the parsers (`SMSG_DEATH_RELEASE_LOC`, `SMSG_CORPSE_RECLAIM_DELAY`,
  `SMSG_PRE_RESSURECT`, `SMSG_RESURRECT_REQUEST`, `SMSG_CORPSE_LOCATION`, `SMSG_CORPSE_TRANSPORT_QUERY`,
  `SMSG_STAND_STATE_UPDATE`, `SMSG_GOSSIP_MESSAGE`, `SMSG_MOVE_SET_RUN_SPEED`/`SWIM_SPEED`) and the writers
  (`CMSG_REPOP_REQUEST`, `CMSG_RECLAIM_CORPSE`, `CMSG_RESURRECT_RESPONSE`,
  `CMSG_QUERY_CORPSE_LOCATION_FROM_CLIENT`, `CMSG_QUERY_CORPSE_TRANSPORT`, `CMSG_TALK_TO_GOSSIP`,
  `CMSG_GOSSIP_SELECT_OPTION`, `CMSG_SPIRIT_HEALER_ACTIVATE`, `CMSG_CLOSE_INTERACTION`), each citing its server
  reader or writer.
* `internal/forever/objects`: the ghost flag (aura 8326 on a player kills nothing by itself, but keeps the unit
  dead), the derived dead flag, `ResurrectedAt` and the model's resurrection counter; `CorpseGUID` finds the
  character's own corpse object when the server sends it; `BuildSpeedAck`/`BuildMovementAck` write the
  acknowledgements of the movement changes a released spirit is given.
* `internal/forever/world`: `deathWatch` (phase, corpse, reclaim deadline, offer, gossip, steps), `observeSelf`,
  `handleDeath` (one dispatch entry, at the end of the session's list), and the actions `Release`, `CorpseLocation`,
  `RunToCorpse`, `ReclaimCorpse`, `AcceptResurrect`, `DeclineResurrect`, `SpiritHealer`, plus `deathCheck` and
  `forever death-check`; `--accept-resurrect-from NAME` is the stricter offer rule (fix round 2).
* The fighter and the walker keep their own death behaviour: `ErrSelfDead` (exit 11); nothing about their exits
  changed.

## Bounds

| structure | key/length source | bound |
|---|---|---|
| `death.GossipMessage.Options` | declared uint32 count | `maxGossipOptions` 64 |
| `death.GossipMessage.Texts` (walked, not kept) | declared uint32 count | `maxGossipTexts` 64 |
| a gossip option's treasure list | declared uint32 count | `maxTreasureItems` 32 |
| `ResurrectOffer.Name` | 11-bit length | `maxResurrectNameLen` 255 |
| a gossip option's `Text`/`Confirm` | 12-bit lengths | `maxGossipTextLen` 1024 each |
| a gossip option's failure text | 8-bit `SizedCString` length | `maxGossipFailureLen` 255 |
| a gossip quest title | 9-bit length | `maxGossipTitleLen` 255 |
| `CMSG_GOSSIP_SELECT_OPTION` promotion code | 8-bit length | `maxPromotionCodeLen` 255 (the writer sends none; a longer code is not sent) |
| `world.deathWatch.steps` | one per flow step | `maxDeathSteps` 128 |
| `deathWatch.offer` | one pending offer | one (a later offer replaces it) |
| `deathWatch.offer.Expires` | the server's `UseTimer`, else this client's | `offerTimerLife` 10 s, capped by `offerLifetime` 5 min |
| `world` corpse lookup | objects in the model | one pass over `maxObjects` 20000 |
| `SMSG_CORPSE_RECLAIM_DELAY` | uint32 ms | `maxReclaimDelay` 5 min (the server's own table is at most 120 s) |
| `SMSG_MOVE_SET_RUN_SPEED`/`SWIM_SPEED` | float yd/s | `maxMoveSpeed` 100, finite and > 0 |
| `SMSG_CORPSE_LOCATION`, `SMSG_DEATH_RELEASE_LOC` | three floats | `maxCoord` 100000 yd, finite |

Nothing else in this round is keyed or lengthened by server data: the death state is a fixed set of fields, and the
gossip menu is the only packet whose lists a server can grow.

## Fixtures and their source

`internal/forever/death/testdata/death.json` — payloads cut from a local capture of a real session by
`fixture_gen_test.go` (a one-off: `FOREVER_CAPTURE_PKT` + `FOREVER_WRITE_FIXTURES=1`; the committed file needs
neither), one per packet kind, plus the client packets the capture carries (`repop_request`, `talk_to_gossip`,
`gossip_select_option`, `spirit_healer_activate`, `close_interaction`). The rewrites are the same as earlier rounds':

* every packed GUID the parse decodes is rewritten through `wire.Buffer.SetGUIDAtHook`, same mask and length, the
  type byte kept, every other byte deterministic;
* the gossip menu's id and its option id are the server's data: the generator rewrites both to the synthetic 4242
  through the offsets the audit parse reports, in the menu and in the select that answers it (the fixture audit
  fails on a built-in value, `TestFixtureGossipIDsAreSynthetic`);
* the reclaim delay is the game value the server chose (30000 ms) and stays; the graveyard position is rewritten to
  the synthetic point (1000, 2000, 50) through the position components the parse reports;
* no names, accounts, coordinates or times: the hygiene test and the fixture audit cover the file.

The file holds sixteen packets: `death_release_loc`, `death_release_loc_revived`, `corpse_reclaim_delay`,
`pre_ressurect`, `stand_state_update` and `gossip_message` from the capture; the five captured client packets above;
and the hand-built ones the capture has no case for: `resurrect_request` (a player's offer), 
`resurrect_request_healer` (a creature's, with its name and sickness), `corpse_location`, `corpse_location_invalid`
and `corpse_transport_answer`. The sentinel the earlier rounds put after a hand-built block is here the parsers'
exact-consumption rule: every hand-built payload must be consumed to its last byte, so a length that is one field
off leaves bytes and the fixture test fails (`TestFixtureConsumedExactly` proves the rule bites).

The writers' oracle is the capture's client packets themselves: `TestWritersMatchTheCapturedClient` rebuilds each of
the five captured client payloads byte for byte from the parsed fields. The same test pins the fake world server's
hand-written wire numbers against `wire.NewOpcodeTable(70707, 70707)` in both directions (`TestDeathWireNumbers`),
which is how this round found that `SMSG_AURA_UPDATE`'s wire number is `0x690011` and `SMSG_CANCEL_COMBAT`'s is
`0x4D0025` for this realm (the core numbers are not the wire numbers).

## The live run (2026-11-19)

The live runs used a test server. The server console is enabled for moving a character and reviving it and for
saving all characters (`cs_tele.cpp:58`, `cs_misc.cpp:113-114`). No console command available here kills a player
(`cs_misc.cpp:87-90`), so the character was moved into a hostile camp and the mobs did it.

One `forever death-check --stay 4s --death-wait 4m --navmesh <mmaps>` run, quoted without
identifiers (distances are yards, times are the flow's clock):

```
death-check: the character is alive
death-check: waiting up to 4m0s for the character to die (a console command or a mob does it)
chat: CHAT_MSG_SYSTEM: You are being teleported by server console command.
death-check:  22.73s the corpse reclaim delay is 0s
death-check:  22.81s the character died at <death point> (map 0)
death-check:  22.81s released the spirit (CMSG_REPOP_REQUEST)
death-check:  22.82s the server answered the release with SMSG_PRE_RESSURECT
death-check:  22.82s the server set the run speed to 8.75 yd/s
death-check:  22.82s the server set the swim speed to 5.90 yd/s
death-check:  22.82s the graveyard is on map 0 at <graveyard>
death-check:  22.82s the corpse reclaim delay is 0s
world: SMSG_MOVE_TELEPORT answered (map 0, client position <graveyard>)
death-check:  22.91s at the graveyard on map 0, <graveyard>, run speed 8.75 yd/s
death-check:  22.91s asked the server where the corpse is
death-check:  22.92s the corpse is on map 0 at <corpse>
death-check:  23.02s running to the corpse: 209.2 yd
death-check:  23.03s navmesh route to the corpse: complete, 3 corner(s), 212.9 yd
path: stop at <corpse> (213.42 yd in 24.392s)
death-check:  47.44s arrived at the corpse: 0.2 yd away
death-check:  47.44s reclaimed the corpse (CMSG_RECLAIM_CORPSE)
death-check:  47.45s the server says the character is alive again (corpse)
death-check:  47.45s the server set the run speed to 7.00 yd/s
death-check:  47.45s the server set the swim speed to 4.72 yd/s
death-check:  47.54s alive again by the corpse (corpse)
```

The corpse run took 24.4 s for 213.4 yd at the ghost's 8.75 yd/s, and the character arrived 0.2 yd from the point
the server named. The server **announced a reclaim delay of 0** on this realm, so no wait was needed; the 30 s and
400 ms delays the tests use are the other branch (`CalculateCorpseReclaimDelay`, `Player.cpp:28211-28213`, and the
`{30, 60, 120}` seconds at `:175`).

**The server's own proof**: the server's own save, taken right after the reclaim, showed the character alive at the
corpse position, with the ghost flag cleared and the corpse row gone (`MiscHandler.cpp:497-500`).

Then the character was left where it stood. Its 50-yard walk out of combat (see "Fix round 1" below) was not
enough: the low-level character, at half health after the reclaim, was killed by the mobs 7 s later. That is the
environment (higher-level mobs around the corpse), not the flow; the save above is the proof taken at the moment it
was alive.

The server-revive path, one `--accept-resurrect --resurrect-wait 2m` run ended with a revive from the console while
the client waited for an offer:

```
death-check:  22.80s the character died at <death point> (map 0)
death-check: waiting up to 2m0s for a resurrection offer
death-check:  23.86s the server says the character is alive again (server)
world logout: CMSG_LOGOUT_REQUEST sent
world logout: SMSG_LOGOUT_RESPONSE (result 0, instant false) and SMSG_LOGOUT_COMPLETE
death-check: alive, revived by server, corpse <corpse> on map 0, reclaim delay not announced
death-check: 9 step(s), the model saw 1 resurrection(s)
```

(The summary read `not announced` because the build that ran still omitted its announced-delay field; the code after
the fix prints `reclaim delay 0s` here, the delay the server announced.)

An online revive calls `ResurrectPlayer(0.5f)` (the console has no full-health permission) and
`SpawnCorpseBones` (`cs_misc.cpp:705-710`), and `ResurrectPlayer` sends `SMSG_DEATH_RELEASE_LOC` with map id -1, the
ghost aura removed, the health restored (`Player.cpp:4749-4758`). The client recognised it as "the server revived
it", not as an answer to any action of its own, and logged out with result 0. (`OfflineResurrect` is the other
branch: it only sets the character's `AT_LOGIN_RESURRECT` flag, which is why the offline cleanup login came back
alive.)

At the end the character was revived offline and restored to its saved starting point, and one short `world-check`
login showed it alive there. No other character or account was touched.

## Fix round 1 (2026-11-19)

### The graveyard teleport's empty transport GUID

The first live release failed on `world: teleport onto a transport: not handled (transport true, vehicle false)`,
which aborted the run before the corpse run. The teleport is `RepopAtGraveyard`'s
(`Player.cpp:5254-5259`): it passes `closestGrave->TransportSpawnId ? ObjectGuid::Create<...>(...) :
ObjectGuid::Empty` into `TeleportLocation::TransportGuid`, an `Optional<ObjectGuid>`. An `Optional` constructed from
an ObjectGuid is **engaged**, even when the GUID is empty, so `MoveTeleport` sets the transport bit and writes a
packed empty GUID (`MovementPackets.cpp:811-822`; the capture's release teleport is 30 bytes of exactly that shape).
Round 3 refused *any* teleport with the transport bit, so the release could never be acked.

Fixed: a transport teleport is one whose GUID is non-empty (`Transport != nil && !Transport.IsZero()`), in
`session.go`'s `SMSG_MOVE_TELEPORT` case. `TestTeleportWithAnEmptyTransportIsAcked` sends the graveyard's shape
through the fake world server and requires the ack and the destination; the old transport test was itself wrong
(it sent an empty GUID and called it a transport), so `TestTeleportOntoATransportIsNotAcknowledged` now carries a
real (non-empty) transport GUID.

### Walking a revived character out of combat

The server refuses a logout in combat (`LogoutResult` 1, `MiscHandler.cpp:229-233`). A character that reclaims its
corpse in a hostile camp is in combat the moment it stands up, so `death-check` walked 50 yards toward the known
graveyard (the direction a released spirit stands in, where nothing waits) and waited up to 15 s for the combat flag
to clear before the logout; a flow that stayed in combat let the logout's own refusal be the error. Fix round 2
replaces the walk: only a navmesh route is walked, and only after a revival of this flow's (see below).

## Fix round 2 (2026-11-19)

Two independent reviews found the packet layouts, the narrow teleport change (an engaged but empty transport GUID),
the wait structure and the reclaim radius sound; they stay as they are. These are the changes.

### Only the character's own speed packet completes the ghost setup

`SMSG_MOVE_SET_RUN_SPEED` set `deathWatch.ghostSpeed` whatever mover it named, so another unit's speed packet
completed the release's ghost setup and the release stopped waiting for the real one. Only a packet whose mover is
the character sets it now, and only that packet changes the mover's speed. `TestAnotherUnitsSpeedDoesNotCompleteTheRelease`.

### A resurrection during the corpse run

The run walked on as a living character when the server revived it mid-run, and the reclaim that followed failed
with `ErrNotGhost` although the character was alive. The run's walk now has its own context and the death state
cancels it on any transition to alive (the revive marker, or the model's aura/health updates through
`observeSelfIfTracking`): the mover sends its stop at once (the new cancellation check in `Mover.MoveTo`/`WalkPath`
stops before the next heartbeat), no reclaim is sent, and `RunToCorpse` returns success with the revival's source
(`server`, or the action whose answer it was). `TestReviveDuringTheCorpseRunEndsTheRun` (straight line and navmesh
path: the stop is the last movement packet), `TestCancelledWalkStopsImmediately`, and a run that starts after the
revival is a success too, with no movement and no reclaim (`TestCorpseRunOfARevivedCharacterSucceeds`).

### Accepting an offer needs a death, and an offer expires

`AcceptResurrect` required no phase: an offer stored while alive was answered without any server confirmation. It
now requires corpse/ghost/resurrecting (`ErrNotDead` otherwise), `handleDeath` ignores an offer that arrives while
the character is alive, and an offer's expiry is on the session clock — the server's ten-second timer when
`UseTimer` is set, this client's five minutes otherwise. `TestOfferWhileAliveIsIgnored`, `TestOfferExpiry`.

### Bounded server numbers

* `SMSG_CORPSE_RECLAIM_DELAY`: the server's own table is at most 120 s (`Player.cpp:175`); a packet over the 5-minute
  cap is not believed and the reclaim reports `ErrReclaimTooEarly` instead of waiting (0xFFFFFFFF ms is 49 days).
* `SMSG_MOVE_SET_RUN_SPEED`/`SWIM_SPEED`: only a finite speed in (0, 100] yd/s is applied; NaN, an infinity, a zero,
  a negative and anything above the bound are ignored and counted (`deathWatch.ignoredNumbers`). NaN used to pass the
  mover's `RunSpeed <= 0` guard, and +Inf made a walk take zero time.
* `SMSG_CORPSE_LOCATION` and `SMSG_DEATH_RELEASE_LOC`: a position must be finite and inside ±100000 yd. A NaN corpse
  position used to make every distance comparison false, so the reclaim went out from anywhere.
  `TestReclaimDelayBeyondTheCapIsRefused`, `TestUnusableSpeedsAreIgnored`,
  `TestCorpsePositionNotUsableIsNotBelieved`, `TestGraveyardPositionNotUsableIsNotBelieved`.

### A revival forgets the death

`deathPoint` set the corpse only when none was known, and nothing reset it: a second death kept the first corpse. A
transition to alive (one place, `deathWatch.revive`) clears the live corpse, the reclaim deadline and the pending
offer, and keeps only the report's history; a second death records its own point. `TestASecondDeathRecordsItsOwnCorpse`.

### Leaving combat

The walk out ran after a timeout or an error before a death, and it walked a fixed 50 yd at the current Z — a
straight line at an invented height, which these notes forbid. It now runs only when this flow revived the character
(`deathWatch.revivedThisRun`), and only on a navmesh: the candidates (the graveyard's direction, away from the
threat, then the four cardinal directions and four diagonals) are planned first and one with a route is walked; with
no navmesh, or no route, the character does not move at all and the existing 15 s wait decides, with the logout's own
refusal reported if combat does not end. `TestLeaveCombatWithoutANavmeshDoesNotMove`,
`TestLeaveCombatWithANavmeshPlansAnExit`.

### The release's ghost state, and a login that is already a ghost

`Release` waited only for `SMSG_PRE_RESSURECT`; the ghost state visible on the character (health 1 and aura 8326,
which `observeSelf` turns into the ghost phase) is accepted as the answer too
(`TestReleaseIsAnsweredByTheGhostStateItself`). A character that logs in already a ghost no longer gets its own
position — the graveyard it stands at — recorded as its corpse: the corpse stays unknown until the server answers
the corpse query. The command then asks the query, and if the server reports no corpse
(`TestLoginGhostHasNoCorpseUntilTheServerAnswers`) `CorpseLocation`, `RunToCorpse` and `ReclaimCorpse` all stop with
`ErrCorpseUnknown`, which the command prints; nothing is run to a guessed point and no reclaim is sent.

### `--accept-resurrect-from`

`--accept-resurrect` answers an offer from any GUID, which is the default: the server decides who may offer
(`IsResurrectRequestedBy`, `MiscHandler.cpp:520-521`). The stricter form is `--accept-resurrect-from NAME`: only an
offer whose offerer the model knows by that name (or whose packet carries that name, as a creature caster's does) is
answered; anything else is `ErrResurrectOfferer` and the wait goes on. On an offer from a player the model has not
named yet, the death handler asks the server for the name (`CMSG_QUERY_PLAYER_NAMES`), so the answer arrives while
the flow waits.

### The movement acknowledgements

Yes, the server expects them. `Unit::SetSpeedRate` increments `m_forced_speed_changes[move_type]` for a player before
the `SMSG_MOVE_SET_*_SPEED` goes out (`Unit.cpp:9352-9357`), and `WorldSession::HandleForceSpeedChangeAck`
(`MovementHandler.cpp:662-733`) consumes it, compares the speed the client echoes with the server's own, and kicks the
player when the client's value is higher. `Unit::SetWaterWalking` sends `SMSG_MOVE_SET_WATER_WALK`/`LAND_WALK`
(`Unit.cpp:14233-14267`) and `HandleMovementAckMessage` reads the answer (`MovementHandler.cpp:777-784`). Nothing
times out when an ack is missing — the counter accumulates and a later ack is "skipped" — but a real client answers,
and the capture holds the three answers twice (after the release and after the revive). The client did not send them;
it does now, for the character's own packets only: `handleDeath` sends `CMSG_MOVE_FORCE_RUN_SPEED_CHANGE_ACK` /
`..._SWIM_...` (`objects.BuildSpeedAck`) and `CMSG_MOVE_WATER_WALK_ACK` (`objects.BuildMovementAck`), each with the
ack index the server's packet carried. The writers are pinned byte for byte against the captured client packets
(`testdata/objects.json`'s `force_run_speed_ack`, `force_swim_speed_ack` and `water_walk_ack`,
`TestFixtureMovementAckWriters`), and the full fake-server death run requires them before its logout.

## What is skipped or open

* **A release that changes map** (a dungeon corpse whose graveyard is on the continent, or the reverse) is followed
  through the map-change round's transfer (see "Fix round 3").
* **The corpse transport query** is parsed and written (`QueryPackets.cpp:393-425`, `HandleQueryCorpseTransport`
  `QueryHandler.cpp:249-266`) but no flow uses it: a corpse on a transport is not modelled (the server's own handler
  answers a zero position for it).
* **`SMSG_STAND_STATE_UPDATE`** is logged, not modelled.
* **A resurrection offer's expiry** is on the session clock. The server keeps one request per player
  (`SetResurrectRequestData` asserts there is none, `Player.cpp:25470-25472`) and replaces it with a newer offer.
  When `SMSG_RESURRECT_REQUEST`'s `UseTimer` bit is set the server asks for its countdown, which the Classic client
  shows as ten seconds and ends with Response 2 (`MiscHandler.cpp:513-518`), so that is the offer's life; without
  it the client's own 5-minute maximum applies. Either way an unattended bot does not answer a stale request.
* **`CheckInstance`** in the repop request is sent false (the capture's real client, outdoors). Its dungeon value is
  unknown: a second local capture of a dungeon session was meant to contain a death and a release inside an
  instance but has neither (a scan of all its records finds no `SMSG_DEATH_RELEASE_LOC`, `SMSG_PRE_RESSURECT`,
  `SMSG_CORPSE_RECLAIM_DELAY`, `CMSG_REPOP_REQUEST`, `CMSG_RECLAIM_CORPSE` or `SMSG_STAND_STATE_UPDATE`).
* **Ghosts and other players' flags**: the ghost state of another unit is the aura 8326; `PLAYER_FLAGS_GHOST` is in
  the player block this client leaves to its declared size, and the party model's `StatusGhost` covers party
  members. A grave marker the client cannot see is not invented.
* **The spirit healer's cast 17251** is not required by the flow: the server resurrects on
  `CMSG_SPIRIT_HEALER_ACTIVATE` (`NPCHandler.cpp:280-324`), so the client sends the activation right after the
  option and does not model the animation. The capture's real client waits for the healer's `SMSG_SPELL_GO` first;
  waiting would add a packet dependency with no effect on the server's answer.
* **A wipe inside an instance** (the dungeon driver's case): a party of bots dies in an instance, releases, and
  the graveyard is the instance's entrance on the open-world map — a transfer, not a same-map teleport, and the
  server revives the character at that entrance (`RepopAtGraveyard`'s shouldResurrect for a non-raid dungeon and a
  raid, `Player.cpp:5211-5231`). `death-check` follows the transfer and ends as `revived by the server` on the
  entrance's map; there is no ghost to run back. A character that is still a ghost with its corpse inside an
  instance (a relogin, or a corpse whose map is the instance) is told exactly that: `RunToCorpse` refuses with
  `ErrCorpseOnAnotherMap` and "the corpse is on map N: walk to its entrance". Entering the instance by its own
  entrance while dead makes `HandleMoveWorldportAck` resurrect the character at the entrance after the add to map
  (`MovementHandler.cpp:333-345`: `ResurrectPlayer(0.5f)` and `SpawnCorpseBones`), so a reclaim inside the instance
  is not the path: `HandleReclaimCorpse` also refuses a corpse farther than 39 yards (`MiscHandler.cpp:490-491`).
  The client sends no area trigger for the corpse run: it has no table of entrances, and the map change it would
  need is the operator's call. The `CheckInstance` bit of the repop request may be the client's way to ask for the
  instance's own graveyard, but no capture shows it set.
* **The ghost aura's wire layout** is round 4's (`SMSG_AURA_UPDATE`, `SpellPackets.cpp:154-164` and the
  `AuraDataInfo` at `:94-140`), already proven by that round's capture cross-check. The death tests build the two
  records (spell 8326 in a slot, then the slot without data) from that writer, because `death.json` holds only the
  death flow's own packets.

## Fix round 3 (2026-11-19): merged onto the map-change round

The command channel and the map-change rounds landed on main while this branch waited, so this round was merged into
them (a merge commit) and the places where the two meet were handled, not joined textually.

### The release follows a transfer to another map

`Release` no longer stops at a graveyard on another map. After `CMSG_REPOP_REQUEST` it waits for `SMSG_PRE_RESSURECT`
(or the ghost aura), then for `SMSG_DEATH_RELEASE_LOC` **and** the transfer that carries the character there
(`RepopAtGraveyard` calls `TeleportTo`, `Player.cpp:5254-5264`). The session's map-change handlers do the rest:
pending, the suspend answer, the new world, the world-port answer and the resume. The wait is
`waitDeathAcrossMaps`: the session's own wait ends with `objects.ErrMapChange` when a transfer opens inside it (the
leg's line belongs to the old map), and that is the step's answer rather than the end of the wait, so the helper
continues on the transfer's clock. Every step the flow itself may get answered with a transfer uses it: the release's
answer, the graveyard, an accepted resurrection (the server teleports to the resurrecter first,
`Player.cpp:28079-28126`) and the spirit healer (`SendSpiritResurrect` teleports to the corpse's graveyard,
`NPCHandler.cpp:296-324`).

The map change clears the world model, and the death flow's state is not in it: the ghost phase, the corpse point,
the announced reclaim delay, the ghost speed flag and the offer live in `session.death` (`deathWatch`), which the
reset does not touch (`docs/forever/SESSION-STATE.md`). So the character arrives as a ghost with its death state
intact; the own create burst on the new map cannot end the ghost phase either (health 1 is the release's
`SetHealth(1)`, and a health of 1 without the resurrection marker is not a revival).

A graveyard the server revives at (a non-raid dungeon's or a raid's entrance graveyard, `Player.cpp:5211-5231`; the
worldport ack's `ResurrectPlayer`, `MovementHandler.cpp:333-345`) ends the release as `revived by the server`: there
is no ghost to run back. That marker is part of the arrival and normally comes with the resume's burst (the decoded
dungeon capture puts it 30 ms after the resume); because a slow link may deliver it later, the release gives it the
same short bounded wait the ghost's speed gets (`arrivalReviveWait`, 2 s) before it goes on as a ghost.

### The corpse on another map, and the corpse inside an instance

A corpse whose map is not the character's is not walked to or reclaimed: `RunToCorpse` and `ReclaimCorpse` name it
(`ErrCorpseOnAnotherMap: the corpse is on map N: walk to its entrance (this client does not change maps by
walking)`). A corpse inside an instance is that case from the entrance map; entering the instance by its own entrance
while dead makes the server resurrect the character at the entrance (`HandleMoveWorldportAck`,
`MovementHandler.cpp:333-345`), and `HandleReclaimCorpse` refuses a corpse farther than 39 yards anyway
(`MiscHandler.cpp:490-491`). The client keeps no table of entrances, so the corpse run reports the map instead of
inventing a route.

### The mover: a ghost keeps its ghost state across a map change

The map-change reset (`EndMapChange`) keeps the mover's run speed and the status bits
(`MOVEMENTFLAG_MASK_HAS_PLAYER_STATUS_OPCODE`, `MovementInfo.h:114-115`, which includes water walking) and clears the
moving, falling and transport state: a ghost that changes maps keeps its 8.75 yd/s and its water-walking flag, and its
next packet starts from the new world's position. The between-maps rules stay the mover's: no walk, chase, turn or stop
sends anything while the window is open, and a cancelled leg (this round's `ctx` check) sends no stop on the old map
either, because the same rule refuses the packet.

### A death during the between-maps window

The server removes the character from its map before it writes the suspend token (`Player.cpp:1526-1527`), so nothing
on that map can damage it between the pending and the new world: a death there is not a sequence the core produces.
The client cannot be corrupted by a stale copy either: `SMSG_UPDATE_OBJECT` and `SMSG_AURA_UPDATE` describe map
content, so the instance-stream gate holds them from the suspend answer to the resume token and drops them at the
resume rather than applying them to the fresh model.

### What the merge's tests pin

* `TestReleaseFollowsAGraveyardTransfer`, `TestReleaseToAnInstanceEntranceRevives`,
  `TestGhostKeepsItsStateAcrossAMapChange`, `TestDeathDuringTheTransferWindowIsNotApplied` (package `world`, the death
  fake at the handler boundary): the far release lands as a ghost with the corpse, the delay and the speed intact; a
  revived-at-teleport release says so; a map change while a ghost keeps every bit of that state; a held death packet
  is dropped.
* `TestDeathCheckReleasesToAGraveyardOnAnotherMap` (the fake world server over sockets): the whole far release — the
  pending, the suspend answer, the new world, the world-port answer, the resume, the new map's create — with no
  movement packet in between, and the corpse run then naming map 0.
* `TestGhostMapChangeKeepsTheGhostsMovement` (objects): the mover keeps the ghost's speed and water walking across
  `EndMapChange`, and the next leg runs at 8.75 yd/s from the new position.

### The dungeon death capture, decoded offline

One more local session (a death inside an instance, a release to a graveyard outside it, and a revival there) was run
through the offline decoder (the packet log read as the generator reads it, every payload through this round's and the
map-change round's parsers) before the merge, and again on the merged tree. It was not turned into fixtures: every
parser agreed with it.

What it shows, in its order: health 0, `SMSG_STAND_STATE_UPDATE`, `SMSG_CORPSE_RECLAIM_DELAY` 30000; the release's
`CMSG_REPOP_REQUEST` (one zero byte, `CheckInstance` false), `SMSG_PRE_RESSURECT`, aura 8326 in slot 4,
`SMSG_MOVE_SET_WATER_WALK` (sequence 4) and the reclaim delay again; then, with no transfer attempt on the same map,
`SMSG_TRANSFER_PENDING` for map 1 (the graveyard outside), `SMSG_SUSPEND_TOKEN` (reason 1) and
`SMSG_DEATH_RELEASE_LOC` **before** the new world, since `RepopAtGraveyard` writes it as soon as `TeleportTo`
returns; the client's suspend answer, `SMSG_NEW_WORLD` (reason 16), the empty world-port answer and
`SMSG_RESUME_TOKEN`; the new map's aura burst with 8326 still in slot 4; then, 30 ms after the resume,
`SMSG_DEATH_RELEASE_LOC` with map -1 and the ghost aura's removal — the server's revive at the teleport, not a
resurrection offer (the session carries no `SMSG_RESURRECT_REQUEST`, and `CMSG_RESURRECT_RESPONSE` and
`CMSG_RECLAIM_CORPSE` never appear). `SMSG_MOVE_SET_LAND_WALK` and the run/swim speed announcements follow the revival
and the real client acks them; a `SMSG_CORPSE_LOCATION` answer later says `Valid=false` (the corpse is bones).

What matched: all 540 `SMSG_AURA_UPDATE`s, the five `SMSG_TRANSFER_PENDING`, `SMSG_SUSPEND_TOKEN`,
`SMSG_NEW_WORLD` and `SMSG_RESUME_TOKEN`s, both `SMSG_DEATH_RELEASE_LOC`s, the reclaim delays, the stand state, the
corpse location, the speed/water packets, every client packet of the flow, and the three area triggers parse to the
last byte (no parser reports a leftover byte). The pending of the release is the shape the death round's empty
transport GUID produces on the far path: the engaged empty `TransportGuid` makes the writer add the ship block, so
the packet is map, position, three bits with **Ship** set, then a transport id 0 and origin map -1. The parser
consumes that block and nothing treats it as a transport.

What did not match: nothing in the wire; one thing in the reading of the session. The ghost exists for about 30 ms
(the revive marker follows the resume), so the release ends with the character alive at the graveyard — the flow's
"the server revived the character at the graveyard teleport" path, not a corpse run. A client whose waits are longer
than those 30 ms would take the other success path (the corpse run cancelled by the revival), which is tested. The
release's `SMSG_DEATH_RELEASE_LOC` arriving before the new world is handled by the same wait: it records the map and
keeps waiting for the landing.

## Fix round 4 (2026-11-19): a corpse on a transport, and the movement-state boundary

### The corpse on a transport

`SMSG_CORPSE_LOCATION` carries the corpse's transport GUID next to the position (`CorpseLocation::Write`,
`QueryPackets.cpp:393-405`). When it is not empty the coordinates are the transport's offsets, not a world point;
the server's answer pairs them with the GUID. Recording them as a walkable corpse gave the ghost a bogus place to
run to and the reclaim a distance it could pass. The handler now records an explicit state instead
(`deathWatch.corpseOnTransport` and the transport's GUID), keeps the corpse unknown (`hasCorpse`/`corpseValid` are
false), and the three actions name it instead of using it: `CorpseLocation`, `RunToCorpse` and `ReclaimCorpse` return
`ErrCorpseOnTransport` (`world: the corpse is on a transport (not supported)`), so no walk starts and no reclaim is
sent. The report carries `CorpseOnTransport`, and the summary prints `corpse on a transport (not supported)` instead
of a point.

The transport answer (`SMSG_CORPSE_TRANSPORT_QUERY`, `QueryPackets.cpp:413-420`, sent by
`HandleQueryCorpseTransport`, `QueryHandler.cpp:249-270`) names the transport's own world position and facing. It is
recorded (`deathWatch.transportAnswer`) and logged, but it cannot turn transport-relative offsets into a walkable
point, so the state stays. A later answer without a transport (or `Valid=false`) clears the state; a release and a
revival reset it with the rest of the corpse state.

This server build's corpse query always writes an empty transport (`QueryHandler.cpp:185`), so the path is not
reachable against it; the client must still not interpret a non-empty one as a world point, and a build that
implements the field would otherwise corrupt the run.

### The movement-state packets at the between-maps boundary

The map-change gate now also holds `SMSG_MOVE_SET_RUN_SPEED`, `SMSG_MOVE_SET_SWIM_SPEED`,
`SMSG_MOVE_SET_WATER_WALK` and `SMSG_MOVE_SET_LAND_WALK`, the four self movement-state packets this round handles and
answers (`docs/forever/MAPCHANGE-R1-NOTES.md`, "Fix round 4": the server's re-send facts and the ACK rules). A copy
written for the old map can no longer move the mover, satisfy `ghostSpeed`, or make the client send an
acknowledgement with the new side's movement info; `EndMapChange`'s kept status bits can no longer carry a stale
water-walk into the new map.

### Tests

* `TestCorpseOnATransportIsNotWalkable` (package `world`, the death fake at the handler boundary): a same-map
  transported corpse and its transport-query answer; the query, the run and the reclaim each return
  `ErrCorpseOnTransport`, the mover does not move, no packet is sent by the refused actions and no reclaim goes out;
  the report says `HasCorpse=false`, `CorpseOnTransport=true`.
* `TestMovementStatePacketsAreGatedMapContent` (`docs/forever/MAPCHANGE-R1-NOTES.md`, "Fix round 4").
