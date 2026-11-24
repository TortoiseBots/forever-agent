# Session A world-reads fixes (issue #228)

The 24 Nov live world-reads session (see the private session notes) proved 8 of 10 rows and left six open points. This
round root-causes each from the code and the ForeverCore source, pins a fake-server test, and fixes it. Go on the build
VM only; the core source (read-only) and the 1.60.1.70959 client data (`SpellEffect`, `SpellTotems`) were read for it.

## 1. `dynamic_objects` empty while Blizzard or Flamestrike was active

**Root cause: the object type, not the parser.** In this core's data the two spells do not create a dynamic object:

* `SpellEffect.db2` (1.60.1.70959), spell 10 (Blizzard): effect index 1 = `Effect 179` = `SPELL_EFFECT_CREATE_AREATRIGGER`
  (misc value 41261); spell 2120 (Flamestrike): effect index 1 = 179 (misc value 41266). Both also cast a damage spell on
  pulse (`Effect 6` with aura 226), which is why `spell_damage` and the 8 s caster aura were seen.
* `Spell::EffectCreateAreaTrigger` (`SpellEffects.cpp:5669-5681`) builds an `AreaTrigger` at the spell's destination, an
  entity of `TYPEID_AREATRIGGER` (11), not a `TYPEID_DYNAMICOBJECT` (9). The dynamic object path
  (`Spell::EffectPersistentAA`, `SpellEffects.cpp:1543-1600`) is a persistent-area-aura spell's, which neither spell is.
* `objects.DynamicObjects` only listed `HasDynamicObject`/`TYPEID_DYNAMICOBJECT`, and
  `readObjectFragmentCreate` decoded no values for `TYPEID_AREATRIGGER`, so even the object's `Type` never reached a row.

**Fix.** `Info.readAreaTriggerCreate` (`internal/forever/objects/values.go`) decodes the head of
`AreaTriggerData::WriteCreate` (`UpdateFields.cpp:8644-8706`): the six override curves, `Caster`, `Duration`, the five
time-to-target fields, `SpellID`, `SpellForVisuals`, the cast visual and `BoundsRadius2D` (the create properties' shape
radius). The rest of the large create (visual animation, curve ids, facing, path/shape variants) is left to the block's
declared size, like every other undecoded tail. `DynamicObjects` lists an area trigger whose create carried a spell, with
a new `Kind` field: `dynamic object` or `area trigger`; the `dynamic_objects` JSON carries `kind`. A portal area trigger
(no spell) stays out of the list.

**Test.** `TestAreaTriggerGroundSpellListsAsAreaTrigger` (objects) builds the create block by hand from the core's
writers; `TestServeDynamicObjectsAreaTrigger` (world) drives it through `SMSG_UPDATE_OBJECT` and checks the JSON keys,
the kind, the spell, the caster, the radius and the distance. `TestDynamicObjectsList` now also pins the dynamic
object's kind.

## 2. A cast at a trade slot refused with spell failure 153

**Root cause: the tool requirement, not the trade target.** 153 is `SPELL_FAILED_TOTEMS`
(`SharedDefines.h:1868`). Pick Lock 1804 has a `SpellTotems` row in the 70959 data with `Totem1 = 5060` (Thieves' Tools),
and `Spell::CheckItems` (`Spell.cpp:7780-7960`, called from `CheckCast` at `:6364`) refuses the cast when the player has no
such item — long before `CheckCast`'s trade-slot block (`:7205-7214`). The agent's packet was already right: target flags
`TARGET_FLAG_TRADE_ITEM` (0x1000), the `ObjectGuid::TradeItem` GUID (uniq type, low 10), and only slot 6, which the core
reads as `TRADE_SLOT_NONTRADED` (`Spell.cpp:413-418`, `TradeData.h:27`). The live session set the lockpicking skill but
never gave the character Thieves' Tools, so the core refused every Pick Lock with 153 regardless of the trade window.

**Fix (agent side).** The refusal now names what was missing: `combat.SpellFailureResult(code, arg1)` appends the
`FailedArg1` the core fills (`FillSpellCastFailedArgs`, `Spell.cpp:4480-4640`) for `REAGENTS` (121), `REQUIRES_SPELL_FOCUS`
(123), `TOTEM_CATEGORY` (152) and `TOTEMS` (153); `castFail` and the `spell_fail` combat row use it. The live answer
becomes `totems (needs tool item 5060 in the bags)` instead of `spell failure 153`. `SPELL_FAILED_TOTEM_CATEGORY` and
`SPELL_FAILED_TOTEMS` are in the naming table now.

**Test.** `TestSpellFailureName` (combat) pins the argument forms; `TestCastOnTradeSlotNamesAMissingTool` (world, trade
fake) answers the trade-slot cast with `SMSG_CAST_FAILED 153/5060` and requires the tool in the outcome. The trade doc
below records the tool requirement.

## 3. `map_snapshot` with default arguments answered a reply the client could not parse

**Root cause: the CLI's reply bound.** `forever rpc` read at most 1 MiB of the reply body
(`io.LimitReader(resp.Body, 1<<20)`) and then parsed it as JSON. A `map_snapshot` with the navmesh background is larger
(the same call with `background: false` was 8.6 KB), so the body was cut mid-JSON and the error was
`invalid JSON: unexpected end of JSON input` — the agent had answered.

**Fix.** `readReplyBody` (`cmd/forever-agent/rpc_cmd.go`, also used by `teardown`) reads up to `rpcMaxReply` (64 MiB) and
refuses a longer body by name; `serveclient`'s bound rises to the same 64 MiB with the same named refusal. The encode
side was unbounded already.

**Test.** `TestRPCReadsAReplyOverOneMiB` reads a 2 MiB reply whole and an over-bound body as
`larger than the …-byte bound`.

## 4. Walking across an instance portal did not fire the area trigger

**Root cause: nothing sent the packet.** The server handles `CMSG_AREA_TRIGGER` (`MiscHandler.cpp:540-601`) and requires
the player to really be inside the trigger (`packet.Entered != player->IsInAreaTrigger(atEntry)` → "too far, ignore"),
but the agent only sent it from the explicit `area_trigger` method (`world/mapchange.go:683`). A real client keeps the
`AreaTrigger` data and sends as it steps into an entry; this agent keeps no table of triggers.

**Fix.** `objects.Mover` gained a `Cross` hook that sees the mover's own position after every movement packet a walk
sends (start, heartbeat, facing, stop in `MoveTo` and `WalkPath`), and `world/trigger.go` arms volumes:
`area_trigger_watch {id, map?, x?, y?, z?, radius?}` (the character's own position when no point is given, radius default
4 yd, `clear` empties the list). A volume that was not inside and is inside now sends `CMSG_AREA_TRIGGER` once
(`EnterAreaTrigger`, entered + from client); leaving re-arms it, so each crossing sends exactly once. A volume armed under
the character sends at once.

**Why 4 yd by default.** The volume is the caller's; the default is small enough to stand inside a classic entrance
sphere — the entries the 70959 client data still carries are 7 yd across a portal that sits exactly on the plan's point
(Stormwind Vault's 107) — and large enough that a walking leg (3.5 yd per heartbeat at 7 yd/s) samples it.

**Test.** `TestServeWalkCrossesArmedAreaTrigger`: arm a volume across the walk's line, walk through it (one packet), walk
back (a second), clear (none).

## 5. `status` never showed `falling` during a fall after a teleport

**Root cause: the state was set and cleared inside one call.** The fall exists (`fall.go`: a teleport above the ground
starts it, the session's waits fly it, `landFall` clears it), and `status` reports `falling` from the mover's airborne
state — but `teleport` waited for the landing before answering (`serveTeleport` → `waitFallLanded`), so no status call
could run while the fall was in the air.

**Fix.** `teleport` answers when the server's own move is confirmed, with the fall at its start: `ground: "falling"`,
`falling: true`, `drop_yd` and `fall_land_z` from the flight's own computation, `fall_ms` the flight left. The fall goes
on flying on every later wait (the idle loop included). `status` now also carries `fall_ms` and `fall_land_z` while a
fall is in the air, and drops to `falling: false` with the floor as `z` at the landing. The `gm` method (a raw
`.go xyz` line) still waits for the landing, so the call after it never meets an airborne mover.

**Test.** `TestServeTeleportAboveTheGroundFallsAndStatusShowsIt`: the answer comes before the flight's 1.76 s, says
`falling`, a status call during the flight says `falling` with the floor, the flight then lands (one fall-land, z = the
floor), and the raw gm path is still landed when it answers.

## 6. `item_info` took `entries` while the docs said `entry`

**Fix.** `item_info` accepts `entry` (one positive id) as well as `entries`; both at once, or an id that is not positive,
is refused. The docs (README, `DAEMON-D1-NOTES.md`) say so.

**Test.** `TestServeItemInfoEntryForm`.

## Live follow-ups (not changed here)

* Row 1 and row 4 need the live re-run to confirm on the server: `dynamic_objects` should now list Blizzard/Flamestrike
  as `area trigger` rows, and a walk into the Deadmines portal with `area_trigger_watch {"id":78}` armed should change
  the map with no explicit `area_trigger` call. A capture of the Blizzard create block would confirm the decoded
  radius equals the spell's (the core takes it from the area trigger's create properties, not from the spell row).
* The trade-slot cast needs Thieves' Tools 5060 in the bags first; with the tool the core reaches its lock check and the
  trade block, which is the path the row meant to test.
