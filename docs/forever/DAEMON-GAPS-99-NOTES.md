# Daemon gaps (#99): teleport, evade, respawn, item mounts, the fall, create on login

What each piece does, which core code it follows, and what is left for a live server. Core references are ForeverCore
at the pin the repository's contract names (`src/server/...`).

## `teleport {x, y, z, map?}`

Sends `.go xyz x y z [map]` (`HandleGoXYZCommand`, `scripts/Commands/cs_go.cpp:376-399`; it prints only a refusal) through
the command channel, so the GM guard, the core's GM log and the teleport confirmation (`gm_silent.go`: the server's own
`SMSG_MOVE_TELEPORT` or new world, and the character within 2 yd of the point on the named map) are the `gm` method's.
All three coordinates are required: the two-coordinate form picks the terrain height itself, and a fall is only
computed from a stated one. A coordinate beyond 1e6 or a negative map is refused before anything is sent. The answer
carries the line, the map, where the client stands afterwards, `teleport_seq`, and `fell` / `drop_yd` when a fall was
flown. The character is the session's own, so no allowlist is involved (the same as the raw `gm` path).

## The fall (`fall.go`)

`.go xyz` relocates to the height given. A real client falls from rest; the server only learns it from the client's
packets and runs its fall bookkeeping on `CMSG_MOVE_FALL_LAND` (`Handlers/MovementHandler.cpp:585-586` `HandleFall`,
`:603` `UpdateFallInformationIfNeed`). After a near teleport (`SMSG_MOVE_TELEPORT`, answered with the ack as before) the
session asks the navmesh (`--navmesh`) for the highest floor at the point (600 yd down, 1 yd up). Ground at least 1 yd
below starts the fall: a heartbeat at the teleport's position at once, then one on the 500 ms beat, then
`CMSG_MOVE_FALL_LAND` at the floor. The arc is the core's own fall model (`Movement::computeFallElevation`,
`MovementUtil.cpp:62-86`, gravity 19.29 yd/s2, terminal velocity 60.15 yd/s) already used for knockbacks. The movement
bits are the new core's 64-bit flags: `MOVEMENTFLAG_FALLING` from the first beat and `FALLING_FAR` (a fall from rest is
descending at once), no knockback bit, no ground bit; the landing block carries the ground bit and no fall bit and no
jump block. The server's fall damage applies as for any client (`HandleFall`).

Limits, chosen on purpose:

* Under 1 yd above the floor is standing (the mesh and the server's terrain height differ by up to about a yard), so a
  teleport to a recorded stop height does not make the next walk wait.
* No navmesh, no floor in reach or a failing query leaves the height as stated.
* A floor flagged water (the format's own water bit, `Planner.WaterFlag`) is not a fall-land (the client swims): no
  flight, and `z_unknown` is set.
* A far teleport (new world) starts no fall: the new map's content is held until its resume token and no movement
  packet may go out before that.
* A second teleport ends the fall in progress (the arc's positions never go out after the server moved the character).
* Ordinary movement is refused (`ErrAirborne`) while the character is in the air; `teleport` returns after the landing.

## `gm_evade {guid|name}` and `gm_respawn {guid|name}`

Both resolve the unit, hold it to the `gm_kill` boundary (`--allow-gm-units`, the `trial_units` allowlist, never a
player), refuse a unit that is not a creature, select it and wait for the server's echo in the character's target
field, then send one line. `.npc evade` (`cs_npc.cpp:1140-1161`) needs a selected non-pet creature with AI and prints
only a refusal. `.respawn` (`cs_misc.cpp:1964-1983`) with an explicit selection respawns that creature if it is dead;
with none it respawns every dead creature around the player, which is why `gm_respawn` refuses a unit the client shows
alive and always confirms the selection first. The raw `gm` path's classifier is unchanged (a bare `.respawn` through
`gm` still works as before).

## `mount {entry, spell}` (item mounts)

`mount_item.go`: the bag item `entry` is used once through the item-use path (`CMSG_USE_ITEM`) with the on-use spell the
caller names, and the existing display wait decides (non-zero `UNIT_FIELD_MOUNTDISPLAYID`, or a refusing
`SMSG_MOUNT_RESULT`, `SpellPackets.cpp:1156`). The item's on-use spell lives in its ItemEffect row, which is not a
queried record here (`use_item` says the same), so an entry alone is refused by name instead of guessed.

The live re-check (#99) showed what that row holds for a ForeverCore mount item: the on-use spell is a learn spell
(55884) and the mount spell is the item's ON_LEARN effect (458 for the Brown Horse Bridle; `ItemTemplate.h:126,132`).
`Player::CastItemUseSpell` (Player.cpp:9707-9744) casts the on-use spell with the item, `Spell::EffectLearnSpell`
(SpellEffects.cpp:2170-2214) then learns the ON_LEARN spell, and no display follows. The call therefore uses the item
once, reads the spell `SMSG_LEARNED_SPELLS` names during the use, casts it when it is a mount spell
(`spells.IsMount`) and waits for the display from that cast. A spell already known teaches nothing
(`Player::LearnSpell` sends no packet for a known spell, Player.cpp:3521-3536), so a named spell the character knows
and that is a mount spell is cast without the item, and the spell an earlier use of the same item taught is reused
the same way (`itemLearn.taught`). A use that teaches several spells, a spell that is not a mount, a use that
teaches nothing, a refused cast and a dropped use each name their own step. The act verb `mount entry=N spell=M` does
the same and its line names the taught spell. Taxi and mount guards for flights are #107's.

## Create on login

`forever serve --create RACE,CLASS[,SEX] (--create-choice OPTION:CHOICE ... | --create-no-appearance)`: before the real
login, one character-screen login reads the list and creates the character only when the name is not on it
(`CreateSpec.OnlyIfMissing`; the name match is the server's normalisation, `nameMatchesCharacter`). A refusal ends serve
with the core's code name; an existing character sends no create packet. The create and the serving session are two
logins (a realm join ticket is spent by the first), so a serve that creates takes two turns at the login gate.

## Live rows (not run here)

See the PR body: teleport to a point above the ground and watch the landing, evade a pulled mob, respawn a killed one,
an item mount from a bag, a serve that creates a missing character.
