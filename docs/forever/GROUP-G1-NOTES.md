# Forever group control, round G1 notes

Scope: the packets of #95 that send a group-control command and the two that read the raid targets back. No serve
method, no `party-check` flag and no readback wait yet: those are G2 and build on this round.

Code: `internal/forever/wire/opcodes.go` (the six opcodes), `internal/forever/party/control.go` (the builders, the
two parsers, the `Mark` set), `internal/forever/party/model.go` (the icon slots and their counters),
`internal/forever/world/chat_session.go` (the two SMSGs' dispatch) and `internal/forever/world/eventlog.go` (the
`raid_mark` rows).

Every layout was read in ForeverCore d3358c21dc (`PartyPackets.cpp`, `PartyPackets.h`, `GroupHandler.cpp`,
`Group.cpp`) and the expected bytes in the tests are computed by hand from those readers. There is no live run of
this round and none is claimed.

## The packets

| Packet | Opcode | Layout (this build) | The server's rule |
|---|---|---|---|
| `CMSG_SET_PARTY_LEADER` | 0x430076 | index bit, flush, packed target, u8 index | leader only, online member of the group (GroupHandler.cpp:314-328) |
| `CMSG_SET_ASSISTANT_LEADER` | 0x43007B | index bit, apply bit, flush, packed target, u8 index | leader only (GroupHandler.cpp:549-558) |
| `CMSG_SET_LOOT_METHOD` | 0x430074 | index bit, flush, u8 method, packed master, u32 threshold, u8 index | leader only, method 0-5, threshold 2..6, master loot needs a member (GroupHandler.cpp:395-427) |
| `CMSG_UPDATE_RAID_TARGET` | 0x43007C | index bit, flush, packed target, int8 symbol, u8 index | symbol -1 asks for the list; in a raid leader or assistant (GroupHandler.cpp:456-479) |
| `SMSG_SEND_RAID_TARGET_UPDATE_SINGLE` | 0x4500D8 | int8 index, int8 symbol, packed target, packed changed-by | sent for every set and every clear (Group.cpp:833-838) |
| `SMSG_SEND_RAID_TARGET_UPDATE_ALL` | 0x4500D7 | u8 index, u32 count, count x (packed target, u8 symbol) | all eight slots, empty target for an unset one (Group.cpp:841-852) |

The party index is present with value 0 in all four client packets (D-6), the shape `BuildInvite` and
`BuildUninvite` already send. Symbols: 0 star, 1 circle, 2 diamond, 3 triangle, 4 moon, 5 square, 6 cross, 7 skull;
-1 asks for the full list.

Disband needs no packet: the core has none, and the leader uninvites every other member (D-5). G2 does that with
`BuildUninvite`.

## Silent refusals, and why G2 has to wait

A refused group command answers with nothing: no `SMSG_PARTY_COMMAND_RESULT`, no error, no notice. The builders
therefore refuse the values the server would drop instead of sending them into silence: a loot method above
`PersonalLoot`, a threshold outside uncommon..artifact, master loot with an empty master, and a mark outside -1..7.
That is a client-side check of the same rule, not a promise that the command was accepted.

The readbacks are the only evidence: the leader in `SMSG_PARTY_UPDATE` (`Update.LeaderGUID`) or
`SMSG_GROUP_NEW_LEADER`, the loot block in the same update, the assistant bit in `party.Member.Flags & 0x01`, the
icons in the two packets above, and "not in a group" for a disband. So every G2 call sends once and waits at most
5 s for its readback (D-7); without one it reports "the server did not confirm it". This round only keeps those
readbacks in the model: `State.Marks`, `MarksKnown`, `MarkOf`, and the `MarkUpdates`/`MarkLists` counters.

The model clears the icons where the group ends or changes: `SMSG_GROUP_DESTROYED`, a kick, a confirmed leave, and
a roster naming a different party GUID. `ApplyMarkSingle` also clears a non-empty target from every other slot
before it stores it, as `Group::SetTargetIcon` does (Group.cpp:825-831), so a target never shows two icons even if
one of the server's own clear updates is lost.

`handleParty` routes both packets into the model and logs them; a malformed one goes through `noteParseError`
(counted, logged once per opcode) and does not end the session, because the icons are a readback nothing in the run
acts on yet. Each packet also writes one `raid_mark` row into the session event log: the single form names the
target, the changer and the symbol, the list form carries every slot.

## Tests

`internal/forever/party/control_test.go`: the four client layouts and the list request against hand-computed
bytes, the refusals, the two parsers (eight slots, a count of nine, a count larger than the payload, a trailing
byte, a symbol of eight), the model's move and clear rules, the icon names, and the six opcodes' wire numbers.

`internal/forever/world/marks_session_test.go`: the fake world's party roster, then a single update, a moved
target, the full list, and a truncated packet that is counted without failing the call.
