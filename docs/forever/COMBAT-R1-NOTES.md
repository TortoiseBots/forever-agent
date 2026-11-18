# Forever combat, round 4 notes

Scope: from "the client knows where the objects are and can walk" (round 3) to "the client targets a creature, walks
into reach, attacks it in melee, casts a spell, and knows what happened" — damage dealt and taken, casts started and
finished or failed, auras gained and lost, and the death. No items, no chat, no groups.

Code: `internal/forever/combat` (the combat wire layer, both directions, and the reason-code tables),
`internal/forever/objects` (the world model's combat state: auras, casts, who attacks whom, health and power, death),
`internal/forever/world` (the session wiring and the fighter: `Attack`, `Cast`), `cmd/forever-agent`
(`forever fight-check`).

Everything below was checked in ForeverCore at commit 6517af05 and against the four private packet captures.

## The one thing the brief expected that does not exist

The brief says the real client's `CMSG_ATTACK_SWING`, `CMSG_CAST_SPELL` and `CMSG_SET_SELECTION` payloads in
`3-combat.pkt` are the reference for the writers. **No capture contains a client combat packet.** All four capture
files were scanned by opcode: the client side of `3-combat.pkt` is login, chat channels, movement, name queries and
`CMSG_SET_ACTION_BUTTON` (353 of them), and the fight in that file is fought by the server-side bots around the real
client — the client's own character walked to a spot, stopped, and its only later packets are at the logout. The
server's combat packets in that file are genuine and are used as the parser oracle (every one of them is decoded to
its last byte below); the writers are established from the server's own readers instead, which is what the server
accepts, and they are proven live by the fight happening. No writer is guessed: each one cites the reader that
defines it.

## The three recurring mistakes, checked in every parser of this round

1. **`append(ptr, count)` writes `count * sizeof(T)` bytes.** Sites on this round's paths:

   | site | element | verdict |
   |---|---|---|
   | `SpellHistory.cpp:504` ClearCooldowns `append(SpellID.data(), SpellID.size())` | `int32` | 4 bytes each; the parser reads a `uint32` count and that many `int32` (`ParseClearCooldowns`) |
   | `SpellPackets.cpp:385` RuneData `Cooldowns` | `uint8` | 1 byte each; read after its own `uint32` count |
   | `SpellPackets.cpp:131/134` aura `Points`/`EstimatedPoints` | `float` | 4 bytes each, counts are 6 bits (`BitsSize<6>`); read as floats |
   | `CombatLogPackets.cpp:399` AttackerStateUpdate `_worldPacket.append(attackRoundInfo)` | bytes | a `ByteBuffer` overload, whole buffer; read as the declared `uint32` size |
   | `Unit.cpp:5968` landed swing `append(round->data(), round->size())` | bytes | a byte span, not an element count; read as bytes |

2. **Sized strings.** The only sized string on this round's path is the spell target's `Name`: `SizedString` with a
   7-bit length, and the bytes follow the optional blocks, flushed (`SpellPackets.cpp:186-200` writer,
   `:174-203` reader, `PacketOperators.h`). It carries no NUL. Empty means length 0 and no bytes; the parser reads
   exactly that many bytes (`readSpellTarget`). No `SizedCString` is involved in the combat packets.

3. **A fixture built by the test's own writer proves nothing.** The parser fixtures are cut from the capture and
   rewritten (see "Fixtures"); the parsers are then required to consume each one to its last byte, and every
   capture file's every combat packet is additionally parsed by the private cross-check
   (`FOREVER_CAPTURE_PKT`). The hand-built fixtures (no capture has the case) are labelled as such in the fixture
   file and in "Fixtures".

## Client packets (out)

All four are read by the server with `operator>>` on `ObjectGuid`, i.e. the packed 16-byte-mask form
(`ObjectGuid.cpp:1113-1134`, `wire.ReadPackedGUID`/`WritePackedGUID`).

| packet | core opcode | layout | server reader |
|---|---|---|---|
| `CMSG_SET_SELECTION` | 0x3E00CA | packed GUID `Selection` | `MiscPackets.cpp:126-129` |
| `CMSG_ATTACK_SWING` | 0x3D011C | packed GUID `Victim` | `CombatPackets.cpp:23-26` |
| `CMSG_ATTACK_STOP` | 0x3D011D | empty | `CombatPackets.h:53-59` (`Read` is empty) |
| `CMSG_CANCEL_CAST` | 0x3D0177 | packed GUID `CastID`, `uint32 SpellID` | `SpellPackets.cpp:871-875` |
| `CMSG_CAST_SPELL` | 0x3D0174 | `SpellCastRequest`, below | `SpellPackets.cpp:243-293` |

`SpellCastRequest` (`SpellPackets.h:247-261`), field order of the reader:

```
packed GUID CastID
uint8       SendCastFlags
int32       Misc[0], Misc[1], Misc[2]
int32       SpellID
SpellCastVisual { int32 SpellXSpellVisualID, int32 ScriptVisualID }
SpellTargetData Target
MissileTrajectoryRequest { float Pitch, float Speed }
packed GUID CraftingNPC
uint32      ExtraCurrencyCosts count
uint32      CraftingReagents count
uint32      RemovedReagents count
uint8       CraftingCastFlags
--- ResetBitPos, then bits ---
1 bit       ReceiveTime present
1 bit       MoveUpdate present
2 bits      Weight count
1 bit       CraftingOrderID present
[ReceiveTime uint32] [MovementInfo] [weights: 2 bits Type, int32 ID, uint32 Quantity] [uint64 order id]
```

`SpellTargetData` as read (`SpellPackets.cpp:174-203`):

```
uint32 Flags
packed GUID Unit, Item, HousingGUID
1 bit HousingIsResident
1 bit SrcLocation present
1 bit DstLocation present
1 bit Orientation present
1 bit MapID present
7 bits Name length
flush
[SrcLocation: packed GUID Transport, 3 floats]
[DstLocation: same]
[Orientation float] [MapID int32]
Name bytes
```

This client sends: `SendCastFlags` 0, `Misc` all zero, the spell's visual (0,0 — the client does not know the
server visual and the server overwrites `m_SpellVisual` when it creates the spell), `Target` with
`TARGET_FLAG_UNIT` (0x2, `SpellDefines.h:313`) and the unit's GUID for a targeted cast, or `Flags` 0 and an empty
unit for a self cast (the server's `SpellCastTargets` then uses the caster as the implicit target, `Spell.cpp:104-110`
and `:7600-7604`), empty missile trajectory, empty crafting block, and the four absent optionals. The `CastID` is a
well-formed `HighGuid::Cast` GUID built the way `ObjectGuidFactory::CreateWorldObject` lays it out
(`ObjectGuid.cpp:922-931`: type 47 in bits 58-63, map id in 29-41, spell id in 6-28, subtype in 0-5, counter low);
the server ignores the value it receives (it creates its own, `Spell.cpp:562`) and only echoes it in a pre-cast
failure.

## Server packets (in)

### Swings

| packet | opcode | layout |
|---|---|---|
| `SMSG_ATTACK_START` | 0x4B0017 | `packed GUID Attacker`, `packed GUID Victim` (`CombatPackets.cpp:28-34`) |
| `SMSG_ATTACK_STOP` | 0x4B0018 | `packed GUID Attacker`, `packed GUID Victim`, 1 bit `NowDead`, flush (`:36-44`) |
| `SMSG_ATTACK_SWING_ERROR` | 0x4B0026 | 3 bits `Reason`, flush (`:89-95`); `AttackSwingErr`: 0 NotInRange, 1 BadFacing, 2 CantAttack, 3 DeadTarget (`UnitDefines.h:413-419`) |
| `SMSG_ATTACKER_STATE_UPDATE` | 0x4B002C | log bit, then `uint32` round size, then the round |
| `SMSG_ATTACK_SWING_LANDED_LOG` | 0x4B0027 | `uint32 0`, then the *full* log packet of the same swing (`Unit.cpp:5946-5972`) |

`SMSG_ATTACKER_STATE_UPDATE` (`CombatLogPackets.cpp:369-425`, `Unit::SendAttackStateUpdate` `Unit.cpp:5916-5944`):

```
1 bit   log data present (the basic packet writes false; the live client never enables advanced logging)
flush
uint32  size of the round buffer
round:
  uint32 Flags                       (HitInfo, UnitDefines.h:385-410)
  packed GUID Attacker, Victim
  int32 Damage, OriginalDamage, OverDamage
  uint8 SubDmg present
  if present:
    int32 SchoolMask
    float FDamage
    int32 Damage
    if Flags & (FULL_ABSORB|PARTIAL_ABSORB 0x20|0x40): int32 Absorbed
    if Flags & (FULL_RESIST|PARTIAL_RESIST 0x80|0x100): int32 Resisted
  uint8  VictimState                 (Unit.h:48-59)
  uint32 AttackerState
  uint32 MeleeSpellID
  if Flags & HITINFO_BLOCK       0x2000: int32 BlockAmount
  if Flags & HITINFO_RAGE_GAIN   0x800000: int32 RageGained
  if Flags & HITINFO_UNK1        0x1: uint32 ArmorReduction, then 11 floats
                                      (CritRollNeeded, CombatRoll, MissChance, DodgeChance, ParryChance,
                                       BlockChance, GlanceChance, CrushChance, MinDamage, MaxDamage), uint32 SinceLastSwing
  if Flags & (HITINFO_BLOCK|HITINFO_UNK12 0x1000): float BlockRoll
  ContentTuningParams (always written by this writer, 45 bytes + 4 bits)
```

`ContentTuningParams` (`CombatLogPacketsCommon.cpp:182-205`): `float PlayerItemLevel, float TargetItemLevel,
int16 PlayerLevelDelta, int32 ScalingHealthItemLevelCurveID, int32 Unused1117, int32 ScalingHealthPrimaryStatCurveID,
uint8 TargetLevel, uint8 Expansion, int8 TargetScalingLevelDelta, uint32 Flags, int32 PlayerContentTuningID,
int32 TargetContentTuningID, int32 TargetHealingContentTuningID, float PlayerPrimaryStatToExpectedRatio`, then
4 bits `Type` flushed. The captured rounds carry `Flags = 3` (`NO_LEVEL_SCALING|NO_ITEM_LEVEL_SCALING`) and three
zero content-tuning ids.

The landed-log packet repeats the same round with the *full* log data (`LogData` bit **true** and the
`SpellCastLogData` present, `CombatLogPacketsCommon.cpp:150-172`: `int64 Health, int32 AttackPower, SpellPower,
Armor, Versatility, Avoidance`, 1 bit `HideFromCombatLog`, 9 bits power count, flush, then per power
`int8 PowerType, int32 Amount, int32 Cost`). `3-combat.pkt` has no such packet (the repack's build predates the
comment at `Unit.cpp:5946`), so the landed-log parser is exercised by a hand-built fixture and by the live run; it
is a duplicate of the swing, not a second hit, and the model ignores its damage (the basic packet already had it).

The capture's 319 `SMSG_ATTACKER_STATE_UPDATE` packets all decode: log bit false, size = payload − 5, sub-damage
present with `SchoolMask = 1`, `FDamage == Damage`, `OverDamage = −1` while the victim lives, and `RageGained`
wired to the attacker's rage (43 on a 10-damage swing of a level-1 warrior-type). VictimState 1 (hit) and 6 (evades)
both occur.

### Spells

`SMSG_SPELL_START` (0x67002D) and `SMSG_SPELL_GO` (0x67002C) both carry `SpellCastData`
(`SpellPackets.cpp:415-464`):

```
packed GUID CasterGUID, CasterUnit, CastID, OriginalCastID
int32 SpellID
SpellCastVisual { int32 SpellXSpellVisualID, int32 ScriptVisualID }
uint32 CastFlags, CastFlagsEx, CastFlagsEx2
uint32 CastTime
SpellTargetData Target            (the same block as the request, on the wire side)
MissileTrajectoryResult { uint32 TravelTime, float Pitch }
int32 AmmoDisplayID
uint8 DestLocSpellCastIndex
CreatureImmunities { int32 School, int32 Value }
SpellHealPrediction { int32 Points, uint32 Type, packed GUID BeaconGUID }
16 bits HitTargets count
16 bits MissTargets count
16 bits HitStatus count
16 bits MissStatus count
9 bits  RemainingPower count
1 bit   RemainingRunes present
16 bits TargetPoints count
flush
packed GUID hit targets, packed GUID miss targets
hit status: uint8 Reason each
miss status: uint8 Reason, plus uint8 ReflectStatus when Reason == SPELL_MISS_REFLECT (11)
remaining power: int8 Type, int32 Cost each     (SpellPackets.cpp:372-377)
remaining runes: uint8 Start, uint8 Count, uint32 n, n bytes (1 per rune)
target points: packed GUID Transport, 3 floats each
```

`SMSG_SPELL_GO` then writes its log-data bit (`false` for a client that has not enabled advanced logging,
`SpellPackets.cpp:473-483`) and flushes: one zero byte, no data. That trailing bit byte is why a GO parser that stops
at the hit lists leaves one byte unread.

The captured GOs decode to real abilities (836, 772, 133, 430, 100, 7922, 6136), hit lists of one or two targets,
`RemainingPower` one entry (`Rage` for the warrior spells, `Mana` for the casters), no miss lists, no runes, no
target points, and 47 of them carry `CAST_FLAG_POWER_LEFT_SELF` (0x800) — the parser reads the list by its own count
either way.

Failure packets:

| packet | opcode | layout | writer |
|---|---|---|---|
| `SMSG_CAST_FAILED` | 0x67004A | `packed GUID CastID, int32 SpellID, visual, int32 Reason, int32 Arg1, int32 Arg2, packed GUID FailedBy` | `SpellPackets.cpp:556-567` |
| `SMSG_SPELL_FAILURE` | 0x670046 | `packed GUID CasterUnit, packed GUID CastID, int32 SpellID, visual, uint16 Reason, packed GUID FailedBy` | `:532-542` |
| `SMSG_SPELL_FAILED_OTHER` | 0x670048 | `packed GUID CasterUnit, packed GUID CastID, uint32 SpellID, visual, uint8 Reason, packed GUID FailedBy` | `:544-554` |

The reason is `SpellCastResult` (`SharedDefines.h:1712-1900`); `Spell::SendCastResult` sends `SMSG_CAST_FAILED`
(`Spell.cpp:4688-4711`), and `Spell::SendInterrupted` sends the pair `SMSG_SPELL_FAILURE` + `SMSG_SPELL_FAILED_OTHER`
to everyone including the caster (`Spell.cpp:5383-5406`). The names this round prints come from the core's values
(118 OUT_OF_RANGE, 91 NOT_READY, 75 MOVING, 71 LINE_OF_SIGHT, 157 UNIT_NOT_INFRONT, 13 BAD_TARGETS, 130
TARGETS_DEAD, 126 SPELL_IN_PROGRESS, 108 NO_POWER, 125 SILENCED, 129 STUNNED, 63 INTERRUPTED, 64
INTERRUPTED_COMBAT, 27 CASTER_DEAD, 87 NOT_KNOWN, 2 ALREADY_AT_FULL_HEALTH, …); anything else prints its number.

### Combat log

| packet | opcode | notes |
|---|---|---|
| `SMSG_SPELL_NON_MELEE_DAMAGE_LOG` | 0x670020 | `CombatLogPackets.cpp:46-83` |
| `SMSG_SPELL_HEAL_LOG` | 0x67000A | `:180-222` |
| `SMSG_SPELL_PERIODIC_AURA_LOG` | 0x670008 | `:236-281` |
| `SMSG_SPELL_ENERGIZE_LOG` | 0x670009 | `:300-315` |
| `SMSG_SPELL_DELAYED` | 0x670032 | `packed GUID Caster, uint32 ActualDelay` (`SpellPackets.cpp:1128-1134`) |

The combat-log packets share `CombatLogServerPacket` (`CombatLogPacketsCommon.h:117-150`): the basic packet carries a
**false** log-data bit (one byte after flush, no data), and only the full packet (advanced logging) carries the data.
Every parser here ends at the log-data bit and requires the packet to be consumed. Damage log layout
(`SpellNonMeleeDamageLog::Write`): `Me, CasterGUID, CastID` GUIDs, `int32 SpellID`, visual, `int32 Damage,
OriginalDamage, Overkill`, `uint8 SchoolMask`, `int32 Absorbed, Resisted, ShieldBlock, ReflectingSpellID, Flags`,
`uint32` world-text-viewer count and supporter count, the viewers (GUID + two 1-bit optionals + their bytes) and the
supporters (`GUID, int32 SupportSpellID, int32 AmountRaw, float AmountPortion`), then bits `Periodic`, `false`
(debug), log data bit, `ContentTuning` optional, flush, log data, then `ContentTuning` when present.

`SMSG_SPELL_PERIODIC_AURA_LOG` writes each effect as seven `int32`s, a `uint32` supporter count with its supporters,
then bits `Crit`, `DebugInfo` optional, `ContentTuning` optional, flush, and the optional bodies.

### Auras

`SMSG_AURA_UPDATE` (0x670011), `AuraUpdate::Write` (`SpellPackets.cpp:154-164`):

```
1 bit  UpdateAll
9 bits count
(byte writes flush the two bytes)
packed GUID UnitGUID
per aura:
  uint16 Slot
  1 bit AuraData present
  flush
  if present: AuraDataInfo
```

`AuraDataInfo` (`:94-140`, built by `AuraApplication::BuildUpdatePacket`, `SpellAuras.cpp:236-296`):

```
packed GUID CastID
int32 SpellID
SpellCastVisual
uint16 Flags          (AFLAG_*: 0x1 self cast, 0x2 positive, 0x4 duration, 0x8 scalable, 0x10 negative)
uint32 ActiveFlags    (effect mask)
uint16 CastLevel
uint8  Applications   (stacks, or charges)
int32  ContentTuningID
3 floats DstLocation
1 bit CastUnit present
1 bit CastItem present
1 bit Duration present
1 bit Remaining present
1 bit TimeMod present
6 bits Points count
6 bits EstimatedPoints count
1 bit ContentTuning present
flush
[packed GUID CastUnit] [packed GUID CastItem] [uint32 Duration] [uint32 Remaining] [float TimeMod]
[Points floats] [EstimatedPoints floats] [ContentTuningParams]
```

`UpdateAll` is the unit's whole set: the model drops every slot it does not name in such an update (the same
`AuraUpdate::UpdateAll` gate). Without `UpdateAll` a record changes only its own slot.

An **aura removed** is the same packet without the `AuraData` (`BuildUpdatePacket(…, remove=true)` returns before
filling it, `SpellAuras.cpp:232-240`): `Slot` then a zero present bit. `Duration`/`Remaining` are present exactly
when `AFLAG_DURATION` is set (`:251-254`, `:272-276`); several captured auras carry it (durations of 10000-20000 ms)
and several do not (permanent buffs), which is what the parser keys on — the bits, not the flags, decide.

### Cooldowns

None of the four captures contains a cooldown packet; these layouts are from the writers alone and are exercised by
hand-built fixtures.

| packet | opcode | layout |
|---|---|---|
| `SMSG_SPELL_COOLDOWN` | 0x670005 | `packed GUID Caster`, `uint8 Flags`, `uint32` count, per entry `uint32 SpellID, uint32 ForcedCooldown, float ModRate` (`SpellPackets.cpp:671-689`) |
| `SMSG_COOLDOWN_EVENT` | 0x450165 | `int32 SpellID`, 1 bit `IsPet`, flush (`:620-627`) |
| `SMSG_CLEAR_COOLDOWN` | 0x450166 | `uint32 SpellID`, 1 bit `ClearOnHold`, 1 bit `IsPet`, flush (`:641-649`) |
| `SMSG_CLEAR_COOLDOWNS` | 0x670015 | `uint32` count, that many `int32` (4 bytes each), 1 bit `IsPet`, flush (`:629-639`) |

`SPELL_COOLDOWN_FLAG_INCLUDE_GCD = 0x1` is sent with the weapon-switch cooldown (`Player.cpp:12600`). **The GCD is
not a packet of its own**: the classic client derives it from the spell's own data, and the server enforces it in
`Spell::CheckCast` (`Spell.cpp:6089` `HasGlobalCooldown`, and the 750-1500 ms clamp at `:9598-9624`). A cast that
hits the GCD fails with `SPELL_FAILED_NOT_READY` (91), which is what the fighter treats as "wait a little".

### Power, experience, death

* `SMSG_POWER_UPDATE` (0x45017E): `packed GUID Guid`, `uint32` count, per entry `uint8 PowerType, int32 Power`
  (`CombatPackets.cpp:97-107`). Power types: 0 mana, 1 rage, 2 focus, 3 energy (`SharedDefines.h:292-300`).
* `SMSG_LOG_XP_GAIN` (0x450190): `packed GUID Victim`, `int32 Original`, `uint8 Reason` (0 kill, 1 no kill,
  `Player.h:1098-1099`), `int32 Amount`, `float GroupBonus` (`CharacterPackets.cpp:881-890`,
  `Player::GiveXP` `Player.cpp:2232-2290`).
* Death is not a packet for a unit: `Unit::setDeathState(JUST_DIED)` sets health 0 and power 0
  (`Unit.cpp:9584-9587`), which arrive as values updates in `SMSG_UPDATE_OBJECT`; the creature's respawn later sets
  health again (`Creature.cpp:2411-2415`). A player's *own* death additionally sends `SMSG_DEATH_RELEASE_LOC`
  (0x45017F: `int32 MapID`, 3 floats) when it releases to a graveyard (`Player.cpp:4749-4758`, `:5255-5264`).
  `SMSG_CANCEL_COMBAT` (0x4B0025) is empty (`CombatPackets.h:135-141`) and ends the client's combat state.
  `UNIT_FLAG_IN_COMBAT` (0x00080000, `UnitDefines.h:186`) and `UNIT_FLAG_DEAD` are in the unit's values, and the
  model derives `Dead` from health ≤ 0 and `InCombat` from the flag plus the attack relations.

### Known spells

`SMSG_SEND_KNOWN_SPELLS` (0x670019) is `SendKnownSpells::Write` (`SpellPackets.cpp:48-63`), **not** `LearnedSpells`
(which belongs to `SMSG_LEARNED_SPELLS`):

```
1 bit   InitialLogin            (flushes into a whole byte before the first count)
uint32  KnownSpells count
uint32  FavoriteSpells count
count x uint32 known ids
count x uint32 favourite ids
```

The first reading used the `LearnedSpells` layout and failed identically on the live server and in the capture
("known spell count 11904"): both carry the `InitialLogin` bit as a whole leading byte (`0x80`), then 41 known
spells and 0 favourites in the capture's 173-byte packet, 1+4+4+164 = 173. The cross-check now covers it in all
four captures.

## The melee range the server checks, and the facing rule

`Unit::GetMeleeRange` (`Unit.cpp:749-753`) is
`max(caster.CombatReach + target.CombatReach + 4/3, NOMINAL_MELEE_RANGE = 5.0)`, and the swing gate is
`IsWithinMeleeRange == IsWithinMeleeRangeAt(pos, target)` with `distsq <= maxdist²` over the full 3D distance
(`Unit.cpp:734-747`). Both combat reaches come from the model's `UnitData` (round 3 decodes them). The arc rule:
`!IsWithinBoundaryRadius(victim) && !HasInArc(120°, victim)` is `AttackSwingErr::BadFacing`, checked at the swing
tick (`Unit.cpp:2250-2270`); the boundary radius is `max(boundingRadius, 2.0)` (`:755-762`). A spell adds
`SPELL_FAILED_UNIT_NOT_INFRONT` under the same 180° front arc when its `FacingCasterFlags` requires it and the
target is outside the caster's boundary radius (`Spell.cpp:7640-7647`).

The fighter therefore (1) walks to a point `0.9 × reach` from the target, (2) turns with `CMSG_MOVE_SET_FACING`
immediately before a swing or cast, and (3) re-checks the distance from the model every heartbeat: beyond the reach
it walks again, inside it only turns.

## Fixtures and their source

`internal/forever/combat/testdata/combat.json` — payloads cut from the private captures by
`fixture_gen_test.go` (one-off, `FOREVER_CAPTURE_PKT` + `FOREVER_WRITE_FIXTURES=1`; the committed file needs
neither), one per packet kind:

* from `3-combat.pkt`: `attack_start`, `attack_stop`, `attacker_state_update` (a hit), `attacker_state_update_evade`
  (victim state 6), `aura_update` (an aura with a duration), `aura_update_multi` (three auras on one unit, including
  the own character), `spell_start`, `spell_go`, `spell_failure`, `spell_failed_other`,
  `spell_non_melee_damage_log`, `spell_periodic_aura_log`, `spell_energize_log`, `spell_delayed`,
  `power_update`;
* hand-built, labelled in the file, where no capture has the case: `attack_swing_error` (3-bit reason),
  `attack_swing_landed_log` (uint32 0 + a full log packet: `Unit.cpp:5946-5972`), `cast_failed`
  (`SpellPackets.cpp:556`), `spell_heal_log` (`:180`), `spell_cooldown`, `cooldown_event`, `clear_cooldown`,
  `clear_cooldowns`, `log_xp_gain`, `death_release_loc`, `send_known_spells`, `aura_removed` (the same unit's aura
  slot without data), `spell_go_miss` (a miss list with a reflect entry, 2-byte status).

Every captured payload is rewritten the way round 3's fixtures were, and the `rewritten` list in the file records
what was replaced (no offsets, no deltas):

* every packed GUID the parse decodes (block GUIDs, casters, victims, cast ids, aura casters, power updates) is
  rewritten **at its own offset**, which the generator gets from `wire.Buffer.SetGUIDAtHook` — never a look-alike
  inside a damage value or a count, which a byte-pattern search would have hit (the first `power_update` candidate
  proved it). The synthetic GUID keeps the mask length and the type byte (bit 15) and makes every other payload byte
  deterministic;
* the run's clock values: the GO's `CastTime` (the server's `getMSTime`, `Spell.cpp:4985`), an aura's
  `Duration`/`Remaining` and `SMSG_SPELL_DELAYED`'s delay become 960000 ms;
* nothing else moves: the captured combat packets carry no positions (their spell targets are units and the auras'
  destination is the zero vector), so the fixtures need no position rewrite; spell ids, damage numbers and flags are
  game values and stay;
* no run identifiers of any kind: the sweep (`TestFixtureGUIDsAreSynthetic`) proves every GUID in every fixture is
  one the generator wrote, and the hygiene test keeps the file clean of addresses, accounts, timestamps and
  coordinate triples.

The parser fixtures are also consumed to the last byte, and the private cross-check
(`TestCaptureCombatPackets`) parses every combat packet of all four capture files and fails on any packet that is
not consumed exactly. The committed fixtures carry no capture path.

## Bounds

| structure | key/length source | bound |
|---|---|---|
| `objects.Object.Auras` | server slot (uint16) | `maxAurasPerUnit` 300 (`MAX_AURAS`, `SpellAuraDefines.h:24-25`); a new slot past it is dropped, an existing one still updates |
| `combat.SpellCast.HitTargets`/`MissTargets`/`HitStatus`/`MissStatus` | four 16-bit counts | each `maxTargets` 256; a GUID is at least 3 payload bytes |
| `combat.SpellCast.TargetPoints` | 16-bit count | `maxTargets` 256 |
| `combat.SpellCast.RemainingPower` | 9-bit count | `maxPowers` 16 |
| `combat.SpellCast.Runes` | uint32 count + uint8 count | `maxRunes` 6 |
| `combat.AuraData.Points`/`EstimatedPoints` | two 6-bit counts | `maxAuraPoints` 32 each |
| `combat.AuraUpdate.Auras` | 9-bit count | `maxAurasPerUnit` 300 |
| `combat.Cooldowns.List` | uint32 count | `maxCooldowns` 128 |
| `combat.PowerUpdate.Powers` | uint32 count | `maxPowers` 16 |
| `combat.SpellTarget.Name` | 7-bit length | 127 bytes (the field's own width) |
| `objects.Object.Cooldowns` | server spell ids | `maxCooldownsPerUnit` 128; a new spell past it is dropped |
| `combat.KnownSpells.Known`/`Favorite` | two uint32 counts | `maxKnownSpells` 1024 each |
| `world.fightLog.events`/`casts`/`auras` | one per observed packet | `maxFightRows` 4096 rows (the fight's own events), then counts only |
| `world` target candidate list | units in the model | one pass over `maxObjects` 20000; `maxTargetAttempts` 5 attacked |
| target selection | creatures in the model | nearest of at most `maxObjects` 20000 units, level 1-3 filter |

Nothing else is keyed or lengthened by server data in this round's code.

## What the live run also showed (2026-11-18)

* The realm accepts the same `--build 70707` claim as rounds 1-3, and the whole fight fits the round-2/3 login: no
  new handshake, no new setting.
* The live burst carries `SMSG_SEND_KNOWN_SPELLS` with 46 spells for the level-1 warrior, among them 78 (Heroic
  Strike) and 2457 (Battle Shout rank 2); Charge (100) and Battle Shout rank 1 (6673) are **not** in that list, so
  the plan picked 78 then 2457.
* The server also sends, for player-involved swings, `SMSG_ATTACK_SWING_LANDED_LOG` (39 in one run) — the packet the
  capture's older revision does not have (see "The landed-log packet" above). Every one parsed.
* `SMSG_SPELL_PREPARE` (0x67002B, `SpellPrepare::Write`, `SpellPackets.cpp:309-315`: two packed GUIDs, the client's
  cast id and the server's) arrives before each of this client's own casts; it is counted by name and not decoded (no
  state it would change).
* A warrior's own create carries `MaxPower = [1000 5 0 ..]` with display power 1. The reading first taken here —
  slot 1 as the rage bar — was wrong: the arrays are indexed by the class's power SLOT, and for a warrior slot 0 is
  rage (1000 tenths = the 100-point bar) and slot 1 the 5 combo points (see "Fix round 1: power is a TYPE, slots are
  the class's" below). The client now resolves the display power type through the class's slot order.
* Target choice and the server's refusal: a White Stallion (entry 5403, level 2, faction 35) passes the geometric
  filter and is refused with an immediate `SMSG_ATTACK_STOP`; the command moves on and reaches a Young Wolf. This is
  the client-side shape of a fact only the faction table knows.
* The client rests: at login the character was at 1/60 health (its own save, from a previous run that ended with
  wolves on it), and a player at that health does not pick a fight. `fight-check` waits for at least half health
  before choosing a target and rests again after the fight, before the logout; a unit that attacks during the rest is
  fought back (`session.rest`, `fightcheck.go`). The regeneration is the server's (`Player::RegenerateHealth`, the
  Classic spirit formula at `Player.cpp:1831-1890`), not something the client does.
* Kill experience of 50 for a level-1 player and a level-1 creature: `Trinity::XP::BaseGain` gives
  `round(playerBase * (1 + 0.05 * levelDiff))` with `playerBase = level * 5 + 45` (`Formulas.h:139-155`), i.e.
  5 + 45 = 50, and `XP::Gain` multiplies it by the creature's `ModExperience` and `Rate.XP.Kill` (both 1 here,
  `:185-235`). The server-side proof of the kill is this column's change (see below).

### The fight, live (2026-11-18)

One `forever fight-check --entry 299` run against the audit realm, quoted without identifiers (the character is
`<character>`, distances are yards from where it stood):

```
fight-check: the character knows 46 spell(s) (46: ... 2457 ... 78 ... )
fight-check: plan: Heroic Strike (next swing, rage), then Battle Shout rank 2 (rage, self)
fight-check: target unit entry 299 level 1 health 42/42 at 3.5 yd
fight-check: it is Young Wolf
fight-check: spell 78 at 184 power: success
fight-check: spell 2457 at 184 power: success
fight    0.4s cast   spell 78 started (0 ms)
fight    0.4s cast   spell 78 went off (1 hit, 0 miss)
fight    0.4s spell  spell 78 hit the target for 18
fight    2.4s swing  hit the target for 6
fight    2.5s swing  miss against the target (victim state dodge)
fight    6.2s swing  hit the target for 12
fight    6.5s swing  miss against the target (victim state intact)
fight    8.1s xp     kill: 50 experience
fight    8.1s swing  the target stopped attacking you
fight    8.2s death  the target died
fight-check: the target died after 8.155s: 6 swing(s), 2 miss(es), 2 cast(s), 5 aura event(s)
fight-check: damage dealt 46, damage taken 4, experience 50
fight-check: you are at 56/60 health, 271/5 rage
```

That run decoded all of its combat traffic: 9 `SMSG_ATTACKER_STATE_UPDATE`, 8 `SMSG_ATTACK_SWING_LANDED_LOG`,
2 `SMSG_SPELL_START`, 4 `SMSG_SPELL_GO`, 2 `SMSG_SPELL_PREPARE`, 1 `SMSG_SPELL_NON_MELEE_DAMAGE_LOG`,
14 `SMSG_AURA_UPDATE`, 18 `SMSG_POWER_UPDATE`, 1 `SMSG_LOG_XP_GAIN`, 2 `SMSG_CANCEL_COMBAT` — no parse error.

The server's own view of the kill, one `saveall` and a read-only query of `characters.characters`:

| | level | xp | health |
|---|---|---|---|
| before the run | 1 | 307 | 60 |
| after the kill (one `saveall`) | 1 | **357** | 60 |
| change | | **+50** | |

50 is the core's own number: `Trinity::XP::BaseGain(1, 1) = round(1 * 5 + 45) = 50` (`Formulas.h:139-155`) with
`ModExperience` and `Rate.XP.Kill` = 1 (`:185-235`), and `Player::GiveXP` credits exactly `Amount` (`Player.cpp:2232`
and the `SMSG_LOG_XP_GAIN` it sends, `:2263-2268`).

A second run found a living creature of the same entry again (at 24.9 yd from where the character stood then),
killed it (+50), and its 50 crossed the level-1 threshold: `characters.characters` then read level 2, xp 7, and the
client saw `SMSG_LEVEL_UP_INFO` and a max health of 79. The killed creature's own spawn was not seen respawning
inside the run window (a level-1 creature's respawn is minutes away, `Creature.cpp:2300-2333`), so what the second
run proves is the entry is back in the world and attackable, not the same spawn id.

## What is approximated or skipped

* **A cast's server-side visual** (`SpellXSpellVisualID`, `ScriptVisualID`) is not known to the client before the
  server's `SMSG_SPELL_START`; the request sends zeros, which the server overwrites with its own
  (`Spell::m_SpellVisual`).
* **Cooldown state** is modelled from the packets only; the classic client also starts a cooldown locally from its
  DBC, which this client does not have. A missing cooldown packet therefore shows as an immediate retry, and the
  retry is what the server validates.
* **The cast's own range window** is not known from packets either (it is in Spell.dbc). `Cast` takes the window
  from the caller; `fight-check` walks into melee before a hostile cast, so any spell whose minimum range allows
  melee is in range.
* **Spell damage is logged, not simulated**: the model never changes a unit's health from a damage log; health comes
  from the values updates, which is where the server puts it. The log is what the fight report prints.
* **Target choice** uses no faction table (the client has none): the geometric filter skips critters
  (`CreatureType 8`), units with NPC flags and units above level 3. A friendly faction the geometric filter cannot
  see is discovered the way a client does: the server answers `CMSG_ATTACK_SWING` with an immediate
  `SMSG_ATTACK_STOP` (`HandleAttackSwingOpcode`'s `!IsValidAttackTarget` path, `CombatHandler.cpp:33-38`), and the
  command moves to the next candidate. The live run met exactly this with the White Stallion (entry 5403, faction
  35) before reaching the wolves.
* **Skipped packets**: `SMSG_SPELL_EXECUTE_LOG` (needs the whole effect table), `SMSG_SPELL_MISS_LOG`,
  `SMSG_SPELL_INTERRUPT_LOG`, threat updates, `SMSG_LEVEL_UP_INFO` (counted, not decoded), and every packet this
  round does not name. They are counted by opcode name and never fail the run.
* **Aura expiry** is wall-clock from the last record's `Remaining`; a re-sent aura restarts it.
* **Death of another unit** is health 0 in a values update; the client does not wait for a corpse packet.

## Fix round 1 (2026-11-18)

### Power is a TYPE, slots are the class's

`SMSG_POWER_UPDATE` names a power TYPE (`Unit::SetPower` builds `PowerUpdate` with `power`, `Unit.cpp:10515-10520`),
while `UnitData::Power`/`MaxPower` are indexed by the class's SLOT: `Unit::SetPower` and `SetMaxPower` call
`GetPowerIndex` (`Unit.cpp:10497-10561`), which is `Player::GetPowerIndex` = `DB2Manager::GetPowerIndexByClass`
(`StatSystem.cpp:345-347`, `DB2Stores.cpp:2216-2222`), and that map is built from the `ChrClassesXPowerTypes` rows
of the class in ascending `PowerType` order (`DB2Stores.cpp:1171-1184`, comparator `:3580-3585`). `DisplayPower` is
the power TYPE too (`Unit::SetPowerType` stores the `Powers` value, `Unit.cpp:6006`), so reading `Power[DisplayPower]`
was reading a slot by a type.

The rows this realm serves (the Classic store `WOWSTATIC_1_60_1_70399`, read on the test server on 2026-11-18) give
these slot orders, carried in `objects/powerTypesByClass`:

| class | slot 0 | slot 1 | slot 2 | slot 3 |
|---|---|---|---|---|
| 1 warrior | rage | combo points | | |
| 2 paladin, 3 hunter, 5 priest, 7 shaman, 9 warlock | mana | | | |
| 4 rogue | energy | combo points | | |
| 8 mage | mana | combo points | | |
| 11 druid | mana | rage | energy | combo points |

Two capture blocks pin the transcription: the mage `create_player` (`MaxPower[0] = 333` mana, `[1] = 5` combo) and
the warrior create seen live (`[0] = 1000` tenths of rage, `[1] = 5` combo). `TestPowerSlotTable` fails if the table
drifts. Creatures use `Creature::GetPowerIndex` instead (`StatSystem.cpp:1207-1231`): their display power is slot 0,
alternate power 1, combo points 2, alternate quest/encounter/mount 3-5, so a type is never assumed to be its slot
there either. A type that neither layout places is dropped, never written into a slot.

Rage and runic power are stored in tenths (the warrior's `MaxPower[rage]` is 1000), so the report prints them as
`13.8/100.0` (`combat.PowerText`); every other power as whole points.

### Casts: which packet may move the wait

* `SMSG_SPELL_START` names a server-owned cast id: `Spell::SendSpellStart` writes `m_castId`
  (`Spell.cpp:4758-4800`), which `Spell::prepare` generates with `SPELL_CAST_SOURCE_NORMAL` (`Spell.cpp:562`,
  `Spell.h:168`). The client's `CMSG_CAST_SPELL` id (`SPELL_CAST_SOURCE_PLAYER`, `packets.go`) is only echoed by the
  immediate "cannot request a cast" refusal (`SpellHandler.cpp:255`). So the wait adopts the first start whose spell
  id matches `Cast`'s, ignores every later start (another cast id is another cast), clamps the cast time to 30 s, and
  has an absolute 38 s bound per `Cast` that no packet moves.
* A failure only closes the wait when its spell id matches the pending cast. `SMSG_CAST_FAILED` is sent straight to
  the caster; its cast id is the client's only on the cannot-request path — a cast that fails in `Spell::prepare`
  carries the server's id (`Spell.cpp:4709`), and the live 2026-11-18 capture's no-power refusal for Heroic Strike
  arrived that way with no start before it, so the id cannot be the matcher. `SMSG_SPELL_FAILURE` names the caster
  and the server's id; when a start was seen, its id must match that start's. An unrelated failure now changes
  nothing at all.

### Attack exits, stops and evades

One deferred cleanup ends the server's attack state on every exit after the swing was sent (context cancellation,
a hook, a walk or facing failure, a wait failure, the deadline): `CMSG_ATTACK_STOP`, then up to 2 s for the server's
own `SMSG_ATTACK_STOP` (`Unit::AttackStop` answers with it, `Unit.cpp:6262-6288`), with a failure recorded in the
fight log instead of replacing the exit's reason. A stop the server sent itself (the counter `attackStops`) skips
the cleanup.

A server stop while the target is alive after contact is `ErrTargetEvaded` ("the server stopped the attack and the
target is alive: target alive at N health"): after a 300 ms grace in which a trailing death values update still
counts as a kill, `fight-check` reports "the target got away" and exits non-zero. Success is only the target's
authoritative death. **How a real evade shows to this client:** the core keeps evade as a server-side unit state
(`UNIT_STATE_EVADE`, `Unit.h:300`, set in `CreatureAI::EnterEvadeMode`, `CreatureAI.cpp:236`), not as a `UnitData`
field or unit flag — there is no `UNIT_FLAG_EVADE` in this core. What the client sees is the attack stop while the
unit is alive, the unit running home (`HomeMovementGenerator`, `Unit.cpp` spline), and its health climbing back in
the values. That timeline *is* the only signal, which is why the stop is a distinct outcome.

### Before anything is sent

`Attack` resolves the GUID, requires a unit object with unit fields, refuses a corpse and a unit whose own flags
`IsValidAttackTarget` refuses on (`UNIT_FLAG_NON_ATTACKABLE` 0x2, `NOT_ATTACKABLE_1` 0x80, `NON_ATTACKABLE_2`
0x10000; `Object.cpp:2429-2443`), and only then sends `CMSG_SET_SELECTION`; the target is re-validated after the
approach, immediately before the swing. `Cast` requires a unit as well. The model cannot tell friend from foe — it
has faction TEMPLATES but no faction or reputation table — so a friendly target is still discovered from the
server's refusal, exactly as before. `rest` only hands `Attack` an attacker the model holds as a live unit.

The distance printed on "attack started" is measured after the approach, and the range is re-checked before the
swing; a walk that stopped short closes in again instead of leaning on the server's "not in range" answer, which
remains the fallback.

### Cooldowns, cast logs, engagement

* `SMSG_SPELL_COOLDOWN`'s `ForcedCooldown` 0 is not "no cooldown": for the server it means the spell's own cooldown
  (`SpellHistory.cpp:544-547` sends the real duration; `Player.cpp:12595-12601` sends 0 for the weapon-switch GCD).
  The model has no `Spell.dbc`, so 0 is stored as "active, unknown end" (a zero `EndsAt`) until a clear packet.
* A cast log naming more power entries than the power types is now an error, not a half-read struct.
* `Engaged` means a round landed (or a cast went off), not that a swing was sent.
* After a teleport during the approach the fighter waits one tick before re-approaching.

### Fixtures from real packets

`forever fight-check --capture-dir DIR` writes every received combat packet's payload to `DIR/packets.jsonl` (a
scratch directory outside the repository). `--provoke` produces the refusals a normal fight does not: a self cast
twice (the second inside the global cooldown) and a swing from outside melee reach.

From the 2026-11-18 live run (`--provoke`, 20.4 yd from the target) these fixtures are now real, rewritten the same
way as round 3's (GUIDs synthetic, no clock values in them): `attack_swing_error` (reason 0, not in range),
`cast_failed` (reason 108, no power, the server's cast id and no start) and `cast_failed_not_ready` (reason 91).
Still hand-built from the server's writer, because no player action on a level-2 warrior produces them: the heal log
(no food, bandage or potion the character can use), `death_release_loc` (we do not release a spirit), the four
cooldown forms (Heroic Strike and Battle Shout start none, and the GCD is not a packet) and the reflected-miss branch
of `SMSG_SPELL_GO` (no level 1-3 creature reflects).
## Fix round 2 (2026-11-18)

### A forced approach is a real step

The server's "not in range" answer to a swing the client believes reaches is ground truth, but the old forced point
(60% of the reach) could sit inside the mover's 0.5-yard `StopDistance`: `Mover.MoveTo` then returned without moving
or facing and the fighter repeated the refused swing until the fight deadline. `approachAfterSwingError` keeps the
ordinary 85%-of-reach point when that is a real walk, and otherwise steps straight at the target 0.8 yard past the
stop distance (or onto its own point when the two are already closer). A step the mover refuses as a no-op sends a
fresh `CMSG_MOVE_SET_FACING` instead. One fight gets at most `maxForcedApproaches` (8) forced approaches and then
`Attack` returns `ErrStillOutOfRange`, so a target the server never lets the client reach ends the fight instead of
spinning in it. The fake server's `refuseCloserThan` knob is the client's belief versus the server's reach.

### Contact is with the target only

`fightLog.swing` counts contact only on rounds between the fighter and the current target. Any other round that
involves the fighter (a mob that pulled it) is `attackedByOthers`/`damageFromOthers` and its own log line
("attacked by another unit"), so the server's attack-stop for an unattackable target stays `ErrCannotAttack` even
when something else hit the character first.

### Death ends the run at once

A defensive fight that returns `ErrSelfDead` (and a rest that finds the character dead) returns at once: no second
rest cycle, no target pick while dead, no retreat, no cast — nothing is sent for a corpse but the logout. The command
maps `world.ErrSelfDead` to its own exit code (`exitDead` = 11).

### One clock

The fighter, the mover and the fight log read the link's clock (`objects.Sender.Now`), which is `time.Now` in
production and the fake's clock in the tests. `session.handleCombat` and the model updates it applies now stamp with
that clock too, so a cast start's deadline and `Cast`'s wait are the same clock. Remaining `time.Now()` calls in
`internal/forever` are wall-clock I/O: `worldconn` socket deadlines, `bnet` RPC deadlines, the session's settle/stay/
ping/logout waits and `next`'s read timer (they wait on real sockets with real timers), `playerLoginStart` (the
movement time field is a monotonic uptime, not a deadline clock), and `session.Now` itself, which is the production
link clock.

### Positions after the retreat, distances from now

`fight-check` re-reads the mover's position after the retreat and the defensive fight before it builds the candidate
list, and measures each candidate from `target.Position(now)` instead of the coordinates of its last update.

### Tests

The fake server parses the whole `CMSG_CAST_SPELL` request with a reader mirroring `SpellPackets.cpp:243-288`
(target, trajectory, counts, tail bits, exact consumption), and the cast answers are serialized server packets fed
through the session's combat handler instead of calls into the fight log. New cases cover the forced step, its
bound, a third-party round, death in the fight and in the defensive fight, the command's outcomes, the mover's
position, the retreat, the between-swings hook, and hand-built non-empty optional blocks (viewers, supporters, crit
rolls, debug, content tuning, log data) each followed by a sentinel value.

Live, one `forever fight-check --entry` run against the audit realm after these fixes (outcome lines only):

```
fight-check: plan: Heroic Strike (next swing, rage), then Battle Shout rank 2 (rage, self)
fight-check: target unit entry 299 level 1 health 42/42 at 3.0 yd
fight-check: spell 78 at 5.2/100.0 rage: no power
fight-check: spell 2457 at 5.2/100.0 rage: success
fight-check: the target died after 6.213s: 6 swing(s), 0 miss(es), 2 cast(s), 7 aura event(s)
fight-check: damage dealt 65, damage taken 6, experience 44
fight-check: the target is at 0/42 health
fight-check: you are at 78/79 health, 5.9/100.0 rage
world logout: SMSG_LOGOUT_RESPONSE (result 0, instant false) and SMSG_LOGOUT_COMPLETE
```

## Fix round 3 (2026-11-18)

### The failed live run: a 60 s wait on a link that had gone quiet

The lead's live run of `f72cf957` walked to a Young Wolf, started the swing, and then saw nothing for 60 s: no
swing, no packet, not even the server's own time syncs; the command waited out its fight deadline and the abort
logout wrote into a socket the server had already dropped (its `Server.log` carried `Prevented sending of ... to non
existent socket 0 / 1` for this character, and the client never reported a read error). Two halves are fixed, and
both were real defects of the client.

**Why the server dropped the sockets.** The log that would name the first close cannot be read any more: the run
predates the worldserver restart that began the `Server.log` I found, and the server writes no per-session close
reason. Its logging does not help either: `Logger.root=5` (ERROR) with every `Logger.network*` line commented out
(`/run/etc/worldserver.conf`), so `AntiDOS: Player kicked!` (WARN, `WorldSession.cpp:1652`) is dropped and the only
network lines that reach the file are the ERROR ones. Each candidate in the brief, checked against the core:

| candidate | verdict |
|---|---|
| packet throttle (`WorldSession::DosProtection`, `:1622-1675`, per-opcode limits `:1676-1925`) | ruled out: `CMSG_MOVE_SET_FACING` is in the no-limit group, `CMSG_MOVE_HEARTBEAT` allows 200/s, and `CMSG_MOVE_START_FORWARD` / `CMSG_MOVE_STOP` / `CMSG_ATTACK_SWING` fall to the default 100/s. The burst was 13 pairs in 2.6 s ≈ 5 packets/s per opcode, 20x under the smallest limit; `PacketSpoof.Policy = 1` (log + kick) never fired. |
| movement time / position validation | ruled out: `WorldSession::ValidateMovementInfo` (`MovementHandler.cpp:49-...`) only removes violating flags, and the two kicks in the movement path (`:730` "Incorrect speed", `:860` "Incorrect magnitude") answer speed-change acks this client never sends. |
| a malformed packet in the new path | ruled out for the swing: the server answered it with `SMSG_ATTACK_START`, so it parsed. |
| the two-socket rule | ruled out: every gameplay packet goes through `session.Send` (the instance socket); the realm socket carried only the loading-screen notice. |
| a server-side socket error (a frame that failed to decrypt or was read as malformed) | not excluded: it would close that socket at once (`WorldSocket.cpp:411-417` `CloseSocket` on a bad GCM tag, `:325` on a malformed header) and log an ERROR line. The client's frames are sealed with a per-direction counter under a mutex, so a desync is only possible if a write was split or lost. |
| the session's own idle rule | not excluded, and it explains the sequence that follows: `WorldSession::Update` (`:372-373`) closes the realm socket once `m_timeOutTime` (last received client packet + `SocketTimeOutTimeActive` = 60000, `/run/etc/worldserver.conf:360`) has passed, then (`:606-616`) closes and resets **both** sockets and keeps the player online for `expireTime` (60 s, `:146`) before removing it. A client that goes quiet for 60 s therefore loses both sockets, and every packet the session still tries to send is logged exactly as the lead saw (`:268-275`). |

So what the evidence pins is the state (both sockets gone, the session still holding the player and trying to send,
no read error seen by the client), and what remains unnamed is the first trigger: a socket-level close or the idle
rule's 60 s of client silence. What the client can control — and the two defects this round fixes — is that it went
silent for 60 s in the first place (the start/stop storm, then nothing to send) and that it did not notice the link
was gone. Four reproductions of the old client against the live server (a 22-yard walk and three runs with 1-3
micro-legs) all killed the target and logged out cleanly, so the trigger is environment-dependent (a target that
keeps moving while the fighter is just outside reach); the failure mode it caused, a silent 60 s, is now impossible
to miss.

1. **The micro-walk burst.** `Attack` asked the mover for one straight leg per look at the world, and the look was
   the 500 ms swing tick. Against a wandering target every sample produced a fresh approach point, so the client sent
   a `CMSG_MOVE_SET_FACING` + `CMSG_MOVE_START_FORWARD` + `CMSG_MOVE_STOP` triple for steps of 0.5-1.8 yards, 13
   pairs in 2.6 s, with no heartbeat in between (a leg shorter than `HeartbeatInterval` has none). That is not how a
   player walks, and it is a burst of packet churn the server never sees from a real client.
2. **No liveness check.** Every wait (`WaitUntil`, `Attack`, `Cast`, the rest and stay loops, the logout) trusted the
   socket to report a close. A connection the peer has dropped (or a peer that simply stops sending, which is what
   the world thread did here) was only discovered when the wait's own deadline expired — 60 s in the fight, 30 s in
   the logout.

#### The chase

`objects.Mover.ChaseTo` (mover.go) follows a moving point in one continuous leg: one `CMSG_MOVE_START_FORWARD` at
the start, a heartbeat every `HeartbeatInterval` (500 ms) with the interpolated position, a re-aim only when the
destination has moved `chaseRetarget` (0.3 yd) or the direction has turned `chaseTurn` (0.2 rad) — a
`CMSG_MOVE_SET_FACING` and a heartbeat on the new line — and one `CMSG_MOVE_STOP` on arrival. Each re-line restarts
the leg's timeout, so a target that keeps moving is followed leg by leg rather than abandoned. Re-planning happens
at most once per `HeartbeatInterval` (500 ms) — the minimum re-plan interval — and never per position sample. The
packet rate is at most `maxMovementPacketsPerSecond` (4): two per 500 ms tick (the heartbeat and at most one
re-aim), enforced by a four-slot one-second ring of the movement packet times (`Mover.movementsInLastSecond`), which
is the real client's own movement tick — the private captures hold five movement packets in total (two heartbeats 138 s
apart), so the client's 500 ms tick is the cadence to match. The live failure's 13 start/stop pairs in 2.6 s
(≈7.5 packets/s of pure churn) cannot happen any more: `Attack` has one start and one stop per approach.
`TestChaseIsOneLegWithABoundedPacketRate` wanders the target every 250 ms through a 30-yard approach and asserts one
start, one stop, at least two heartbeats, and no 4-packet/1-second window.

#### The dead link

* `session.setLinkDead` is called by each socket reader as soon as its read fails (and by `sendOn` when a write fails
  on the socket), closes that socket's channel and records the side, the error and the last server packet
  (`SMSG_...` name and when). `session.next` selects on both channels, so `WaitUntil`, `Attack`, `Cast`, `MoveTo`,
  `ChaseTo`, the rest and stay loops and `waitFor` all end **at once** with `ErrLinkLost` ("the connection is lost"),
  naming the socket and the error. Nothing waits out its own deadline on a dead link.
* A peer that stops sending while its socket stays open is the same loss after `linkSilenceLimit` (30 s): the server
  sends a time sync at least every 10 s (8 in the 75 s live run) and a pong for every ping, so 30 s of silence is a
  hung or half-open peer. `next` shortens a wait to that limit and returns `ErrLinkLost` ("no server packet for 30s").
* Sends refuse at once once the link is dead, so the abort logout cannot write into a socket the server has dropped.
  `abortLogin` still sends `CMSG_LOGOUT_REQUEST` + `CMSG_LOG_DISCONNECT` when the link is alive, and prints
  `world abort: logout not possible: connection lost` when it is not.
* `forever fight-check` prints `fight-check: connection lost on the <side> socket (<error>); the last server packet
  was <SMSG_...>, <n> before the end` and exits with `exitDisconnected` (6), its own code, before the summary.
* The logout keeps one documented exception: once `SMSG_LOGOUT_RESPONSE` arrived, the instance socket's close is
  expected (the server closes it right after the response) and the completion is awaited on the realm socket.
* `TestEnterNoticesADeadSocket` runs the fake server dropping each socket (realm and instance) at each stage (right
  after the login burst, 150 ms into the stay, on the logout request), as a FIN and as a RST: 12 cases, each ending
  in `ErrLinkLost` at once with the socket named, the last packet printed in the report and the impossible-logout
  line. `TestLinkDeathEndsEveryWait` covers each wait primitive, `TestLinkSilenceIsDeath` the quiet peer.

#### A cast's generation and its quarantine

`fightLog` gives every `Cast` a generation (`castGen`). When a Cast times out, `quarantineCast` quarantines the spell
for `castQuarantineWindow` (3 s) and records the cast ids that generation named. While the window runs, any
`SMSG_SPELL_START` / `SMSG_SPELL_GO` / `SMSG_CAST_FAILED` / `SMSG_SPELL_FAILURE` for that spell — or any packet
naming one of those ids inside the window — is dropped and counted (`staleDrops`, printed as "late cast packet(s)
dropped"). `Cast` does not send a quarantined spell inside the window (it waits it out first), so no pending Cast can
own a quarantined packet and the drop rule is safe; the id is time-bounded because a server may reuse one.
`TestStaleCastDoesNotCompleteTheNextCast`: A times out, its late START, GO and CAST_FAILED arrive (3 drops counted,
nothing reopened), then B is sent and completed by its own answers.

#### Death before anything optional

`fightCheck` now settles first (`session.settle`, which ends at once with `ErrSelfDead` if the character dies during
it) and only then pings, queries names and summarises; a dead character is `ErrSelfDead` (the command's `exitDead`,
11), never `nil`, and nothing optional is sent after the death. `TestDeathDuringSettleEndsAtOnce` and
`TestFightCheckStopsAtOnceOnADeadCharacter` cover the settle death and the already-dead start.

### The three live runs (2026-11-18, after the fixes)

`forever fight-check --build 70707 --character <the test character>` three times in a row from wherever the character
stood, on the fix tree, no entry forced. Each run: the walk lines before the swing and during the fight, then the
outcome:

| run | walk lines | target | fight | swings | casts | result | xp before → after |
|---|---|---|---|---|---|---|---|
| 1 | none (4.5 yd away) | entry 299, level 1 | 7.7 s | 8 hit, 0 miss | 2 | kill, +44 | 0 → 44 |
| 2 | 1 `chase` (24.0 yd, 5 heartbeats) | entry 299, level 1 | 11.2 s | 6 hit, 2 miss | 2 | kill, +44 | 44 → 88 |
| 3 | 1 `chase` (24.6 yd) + 1 re-approach (1 heartbeat) | entry 299, level 1 | 12.3 s | 8 hit, 0 miss | 2 | kill, +44 | 88 → 132 |

(44 rather than 50 because the character had reached level 2 during the old-client reproduction runs: the server's
`Trinity::XP::BaseGain` for a level-2 player and a level-1 creature gives 44.) Run 3's outcome lines:

```
fight-check: target unit entry 299 level 1 health 42/42 at 24.6 yd
chase: start forward from <pos> after <pos> (7.24 yd, run 7.0 yd/s)
chase: stop at <pos> (1 heartbeats)
fight-check: spell 78 at 5.2/100.0 rage: no power
fight-check: spell 2457 at 5.2/100.0 rage: success
fight-check: spell 78 at 10.4/100.0 rage: no power
fight-check: spell 78 at 15.4/100.0 rage: success
fight   12.1s xp     kill: 44 experience
fight   12.3s death  the target died
fight-check: the target died after 12.324s: 8 swing(s), 0 miss(es), 2 cast(s), 8 aura event(s)
fight-check: damage dealt 46, damage taken 7, experience 44
fight-check: the target is at 0/42 health
fight-check: you are at 72/79 health, 0.8/100.0 rage
world logout: SMSG_LOGOUT_RESPONSE (result 0, instant false) and SMSG_LOGOUT_COMPLETE
```

Every run ended with one start and one stop per approach (one `chase` pair; run 3 re-approached once after the target
moved during the fight, one pair) — the lead's 13 pairs are gone. No run printed a connection-lost line, each logged
out cleanly, and `account onlinelist` answered "No online players." after each (one `saveall` before the read).

The test character was recreated for these runs with the server's own `.tb create <the test account> <the test
character> 1 1` (a level-1 human warrior) after the previous one was gone from `characters.characters`; the fight then
levelled it to 200 experience at level 1.

## Fix round 4 (2026-11-18)

### The race CI found

The `test` job of `5dd298aa` (`go test -race -count=1 -timeout 20m ./...`) failed in
`TestEnterNoticesADeadSocket/instance/login/reset` and `/realm/stay` with three pairs of racing accesses:

| pair | where | verdict |
|---|---|---|
| write `lastPacketName`, read in `setLinkDead` | `session.go:1055` (handle, session goroutine) vs `session.go:416` (reader goroutine) | production |
| write `lastPacketAt`, read in `setLinkDead` | `session.go:1059` vs `session.go:417` | production |
| write/read `deliberateClose` | `fake_world_test.go:548` (the dropping goroutine) vs `:464` and `:682` (both socket readers) | test harness |

**Cause.** The dead-link state has one mutex (`deadMu`), and `setLinkDead` (called by each socket reader when its
read fails) correctly read the last packet under it to fill the report. The session goroutine wrote those two
fields in `handle` without the mutex, because when they were added they were only session state. `deadErrorExcept`
also read `lastPacketName` after unlocking, and `nextExcept` read `lastPacketAt` unlocked, so the silence rule had
the same bug on the session side.

**Fix.** `deadMu` now guards both fields with a stated invariant (comment on the field block, `session.go:224-227`):
the session goroutine is the only writer, and every access goes through the lock. `handle` writes through
`notePacket` (`session.go:424`), `nextExcept` reads through `lastPacket` (`session.go:433`, used at `:1606`), and
`deadErrorExcept` takes the name inside the same critical section as `deadStale` (`session.go:465`). The
harness's `deliberateClose` moved under the fake server's own `mu`: `dropLink` sets it at `fake_world_test.go:556`
and both readers call `deliberate()` (`:538`).

**Shared state between the socket readers and the session goroutine** (`internal/forever/world`, `worldconn`;
`objects` and `combat` have no goroutine of their own):

| state | written by | read by | protection |
|---|---|---|---|
| `session.deadChs[2]` | `setLinkDead` closes one | `nextExcept` selects on both | closed under `deadMu`; created in `Enter` before `startPump` |
| `session.deadClosed`, `deadErrs` | `setLinkDead` | `setLinkDead`, `deadErrorExcept`, `deadDetail` | `deadMu` |
| `session.deadAny`, `deadSide`, `deadErr` | `setLinkDead` | `setLinkDead` (`deadSide`/`deadErr` are not read elsewhere) | `deadMu` |
| `session.deadStale` | `markStale` | `deadDetail`, `deadErrorExcept` | `deadMu` |
| `session.logoutAnswered` | `logout` (session goroutine) | `setLinkDead` | `deadMu` |
| `session.lastPacketName`, `lastPacketAt` | `notePacket` (session goroutine, only writer) | `setLinkDead` (reader), `lastPacket`, `deadErrorExcept` | `deadMu` |
| `report.LinkLost`, `LinkLostSide`, `LinkLostDetail`, `LastPacket`, `LastPacketAgo` | whichever goroutine calls `setLinkDead` | `abortLogin` (after a `deadMu` acquire), `cmd/forever-agent` after `Enter` | written under `deadMu`; the readers are waited for in `shutdown` |
| `session.events` | both readers send | session goroutine receives | buffered channel (happens-before) |
| `session.pumpCtx`, `pumpCancel`, `pumpDone` | `Enter` | readers read the context and close their done; the session stops the context and waits | context and channels; no field is written after `startPump` |
| `worldconn.conn.recvSeq`, `crypt`, `inflater` | the socket's receiving reader (the handshake that sets them runs on the session goroutine before `startPump`) | same | single owner |
| `worldconn.conn.handshakeNames` | the handshake only, frozen by `handshakeDone` before `startPump` | `HandshakeNames` (session goroutine) | written before any reader exists |
| `worldconn.conn.sendSeq` and the seal counter | every sender | every sender | `sendMu` (counter and write as one operation) |
| `worldconn.conn.encrypted` | the handshake, before the pump starts | both directions | written once, read-only after |
| `worldconn.conn.nc` | both directions, `Close`, the deadline watchers | all of them | `net.Conn` is safe for concurrent use |
| `worldconn.conn.closeOnce`, `closeErr` | `Close` callers | `Close` callers | `sync.Once` |
| `objects.World`, `objects.Mover`, `fightLog`, the rest of `Report` | session goroutine only | session goroutine; `cmd` after `Enter` returns | single owner (`World`'s own `RWMutex` covers its read accessors) |
| `fakeServer.deliberateClose` (harness) | `dropLink` | both socket readers | `fakeServer.mu` via `deliberate()` |

### Gates

The pre-merge gate of this repository includes the race run. On this branch, before a merge:

| gate | command |
|---|---|
| the whole suite under the race detector (the CI `test` job) | `go test -race -count=1 -timeout 20m ./...` |
| the forever packages, ten times, under the race detector | `go test -race -count=10 ./internal/forever/...` |
| format, vet, no-cgo build | `make fmt-check vet nocgo` |
| static analysis | `staticcheck ./...` (2026.2.2) |

A single `-race` run is not enough for this code: the failing test starts its goroutines 12 times, and `-count=5`
on the focused link tests reproduced all three pairs on the laptop.
