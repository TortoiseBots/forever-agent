# Forever pets, round P1 notes

Scope: the pet packets and the pet fields of the unit block, parsed only. No method, no state and no wiring: round
P2 builds the `pet` and `pet_action` methods on top of this.

Code: `internal/forever/pet` (the seven packets, both directions: `ParseSpells`, `ParseMode`, `ParseCastFailed`,
`ParseFeedback`, `BuildAction`, `BuildSpellAutocast`, `BuildStopAttack`, the button packing and the name helpers),
`internal/forever/wire` (the seven opcodes), `internal/forever/objects` (`PetNumber`, `CreatedBySpell`,
`CreatureFamily`, `PetFlags` decoded in the create and the update walk, with `Fields` flags and the merge).

## The layouts, as the core writes them

Checked in ForeverCore 0.1.39 (`d3358c21dc`), the core the agent targets; the numbers match the older 6517af05 spec
read, so nothing is behind a per-build switch.

- `SMSG_PET_SPELLS_MESSAGE` (`PetPackets.cpp:23-66`): packed GUID, u16 family, u16 specialization, u32 time limit,
  u8 command, u8 flag, u8 react, then **18** int32 buttons, then u32 action/cooldown/history counts and the three
  arrays. The 18 are the ten action buttons followed by the Classic 1.60 tail: buttons 0-3, the fixed `0x16000000`
  and buttons 7-9 (`:36-41`). A body with the ten buttons of another build puts the count words where buttons 11-13
  belong; the parser refuses it (test 2) instead of reading the array counts out of the cooldown bytes.
- `Player::SendRemoveControlBar` (`Player.cpp` at the pinned core) sends the same body with an empty pet GUID. The
  body is still parsed exactly and `Cleared` is the GUID rule; the core leaves `0x16000000` in the fifth tail slot
  even then, so the tail is kept raw rather than required to be zero.
- `SMSG_PET_MODE` (`PetPackets.cpp:239-247`): packed GUID, then **command, flag, react**. The Turtle client's
  `SMSG_PET_SPELLS` carries them in another order, so this parser writes them down in the core's order and the
  tests pin it.
- `SMSG_PET_CAST_FAILED` (`SpellPackets.cpp:569-578`): packed cast id, i32 spell, i32 reason, i32 arg1, i32 arg2.
  The reason is a `SpellCastResult`; the combat package already names those.
- `SMSG_PET_ACTION_FEEDBACK` (`PetPackets.cpp:198-204`): i32 response, i32 spell (`PetDefines.h:102-109`: none,
  dead, no_target, invalid_target, no_path).
- `CMSG_PET_ACTION` (`:140-148`): packed pet, u32 action, packed target, three floats. Only `COMMAND_MOVE_TO` reads
  the position and it is not ported, so the builder writes 0,0,0.
- `CMSG_PET_SPELL_AUTOCAST` (`:173-178`): packed pet, u32 spell, one bit. The core reads it as `Bits<1>` and the
  packet ends there, so the bit is the MSB of its own byte: `0x80` on, `0x00` off.
- `CMSG_PET_STOP_ATTACK` (`:150-153`): the packed pet alone. The builder exists for a later round; `pet_action`
  does not gain a `stop` (the Turtle client had none).

Unit fields (`UpdateFields.cpp`, `UnitData::WriteCreate` and `WriteUpdate` at the pinned core):

- `CreatureFamily` is an int32 written after `DisplayScale`, before `OverrideCreatureType` (mask bit 57).
- `PetNumber` is the first of the four pet words (uint32, mask bit 72); the timestamp and the two experience
  counters stay skipped.
- `CreatedBySpell` is an int32 before `EmoteState` (mask bit 83).
- `PetFlags` is the third of the four state bytes `SheatheState, PvpFlags, PetFlags, ShapeshiftForm` (mask bit 89).

Every skip keeps its byte count: the create and update fixtures end with a sentinel field after the region, and the
existing objects tests and capture checks still consume whole blocks.

## What is not proven

- **No live run yet.** Nothing here has been exercised against the realm; the capture cross-check
  (`FOREVER_CAPTURE_PKT` / `FOREVER_CAPTURE_DIR`) parses every pet packet of a private log to its last byte but the
  lead runs it, not the round.
- **The client's own pet bar has not been seen in a recording yet.** The 18-button reading is the core's writer and
  its Classic 1.60 comment; it is not yet confirmed from a session where the character owns a pet.
- The pet GUID's type nibble is not used to classify a unit as a pet; `PetNumber != 0` and the owner GUIDs are the
  round P2 signals. Happiness and loyalty are not decoded: this model has no power mapping for a hunter pet.
