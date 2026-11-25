# Raid, round R1: the three raid packets, the raid roster and its serve fields (#108)

Scope: `wire`, `party` and the `party` serve method. No serve method sends a raid packet yet (`convert_raid` and
`raid_move` are round R2) and no command drives a raid (R3).

## The packets (`internal/forever/party/control.go`)

| Packet | Opcode | Layout (the realm build's readers) | Builder |
|---|---|---|---|
| `CMSG_CONVERT_RAID` | 0x43007A | one bit, `Raid`, flushed (PartyPackets.cpp:395-398) | `BuildConvertRaid(raid bool)` |
| `CMSG_CHANGE_SUB_GROUP` | 0x430078 | packed target GUID, u8 subgroup, optional-index bit, index (PartyPackets.cpp:633-640) | `BuildChangeSubGroup(target, sub)` refuses a subgroup outside 0..7 |
| `CMSG_SWAP_SUB_GROUPS` | 0x430079 | optional-index bit, two packed GUIDs, index (PartyPackets.cpp:642-649) | `BuildSwapSubGroups(a, b)` |

`MaxSubgroups` is 8: the wire subgroup is 0-based, the raid frame and every serve field number the subgroups 1-8.

## The roster (`internal/forever/party/model.go`)

- `State.Raid` is the roster's `FlagRaid` bit, whatever its size: a five-member roster that carries it is a raid, and
  a roster over five without it is a party.
- A roster up to `MaxRaidMembers` (40) is modelled like a party: the same `validateRoster` runs first and a valid one
  sets `RosterValid`, `Members` and `Order`. Over 40 is refused by name ("N members is over the raid limit of 40").
  `EventRaidNotHandled` is gone; `rejectRoster` clears `Raid` with the rest of the model.
- Each `Member` keeps `Subgroup` (the wire value, 0-based) and `Assistant` (`Flags&FlagAssistant`), set by the parser
  and re-derived by `apply`, so a hand-built `Update` carries it too.

## The serve fields (`internal/forever/world/serve_session.go`)

`party` gains `raid`, and `assistant` and `subgroup` for this character; every member row gains `assistant` and
`subgroup`. Both subgroup fields are the 1-based number (0 outside a raid), the same shape as the Turtle daemon's
`party` result. `serveclient.Party`/`PartyMember` mirror the keys.

## What changed around the model

- `party-check --leave`: a raid roster is valid now and `CMSG_LEAVE_GROUP` has no raid restriction, so leaving works
  from a raid too. The `printParty`/`party-check` output prints the raid roster (1-based subgroups, the assistant
  mark) instead of "raid: not handled in this round".
- Group control (`promote`, `assistant`, `loot_method`, `mark`, `disband`) still refuses a raid, now by its real
  reason: the raid ranks differ (an assistant may mark icons) and the raid operations arrive with the raid walk.
  Exception since #236: `assistant` is a raid operation, so it takes a raid (leader only, the next roster carrying the
  member's flag) and refuses a party instead.
- The fake world writes the raid flag and per-member subgroups (`groupRaid`, `groupSubgroup`).

Tests: `internal/forever/party/raid_test.go` (three layouts, a ten-member raid parsed and modelled, five-member flag
semantics, the 40 limit), `TestRaidRosterValidation`, `TestServePartyShowsSubgroups`.
