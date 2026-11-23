# Group control, round G2: the calls, the serve methods and the party-check flags (#95)

G1 wrote the four client packets, the two mark parsers and the model's icon slots. G2 makes them usable: one call
per command that sends and then waits for the readback the command produces (D-7), six serve methods over them, the
loot and icon readback in `party`, and three `party-check` flags for the live walk.

## The calls (`internal/forever/world/groupctl.go`)

| Call | Packet | Rank the core asks | Readback (5 s) |
|---|---|---|---|
| `groupPromote(ctx, name)` | `CMSG_SET_PARTY_LEADER` | leader | `LeaderGUID` is the member |
| `groupAssistant(ctx, name, apply)` | `CMSG_SET_ASSISTANT_LEADER` | leader | the member's `Flags & 0x01` (MEMBER_FLAG_ASSISTANT) matches |
| `groupLootMethod(ctx, method, master, threshold)` | `CMSG_SET_LOOT_METHOD` | leader, not LFG | the roster's loot block is method/threshold/master |
| `groupMark(ctx, target, mark)` | `CMSG_UPDATE_RAID_TARGET` | any member of a party | the icon slot holds the target (`MarkUpdates` moved) |
| `groupMarks(ctx)` | `CMSG_UPDATE_RAID_TARGET`, symbol -1 | any member | `SMSG_SEND_RAID_TARGET_UPDATE_ALL` arrived (`MarkLists` moved) |
| `groupDisband(ctx)` | one `CMSG_PARTY_UNINVITE` per other member, roster order | leader | the party is gone |

Every call refuses before sending: no group or no valid roster ("not in a group", "no valid party roster (<why>)"),
the wrong rank ("only the leader can (the leader is <name>)"), an unknown member ("no member named <n> in the
roster"), an offline promote target, a value `party.BuildSetLootMethod`/`BuildUpdateRaidTarget` refuses, and a mark
outside 0..7. A command that waits out its 5 s fails with "the server did not confirm it within 5s (a refused group
command gets no answer)": this core answers a refused group command with nothing at all.

Ranks and limits the model cannot satisfy are refused by name rather than guessed: G2 predates the raid roster (G1's
model set `party.State.Raid` with `RosterValid` false), so every call answered "the party model keeps no raid
roster". R1 of #108 models the raid roster (docs/forever/RAID-R1-NOTES.md), so the refusal reads "raid group control
is not ported yet": the raid ranks differ (an assistant may mark icons) and the raid operations come with the raid
walk. R2 of #108 ports `convert_raid` and `raid_move` (docs/forever/RAID-R2-NOTES.md); the party surfaces above
still refuse a raid until the raid ranks are modelled for them.

`groupPromote` also refuses this character itself ("already leads the group"), so the leader readback always moves.

## The serve methods (`world/serve_session_methods.go`)

| Method | Params | Result |
|---|---|---|
| `promote` | `{name*}` | `{}` |
| `assistant` | `{name*, clear?}` | `{}` (`clear: true` clears the flag) |
| `loot_method` | `{method*, master?, threshold?}` | `{}` |
| `mark` | `{guid? \| name*, icon*}` | `{icon, guid}` once the icon slot confirms it |
| `marks` | `{}` | `{marks: [{icon, guid}]}`, set icons in slot order |
| `disband` | `{}` | `{}` |

`method` is `party.LootName`'s spelling (`group-loot`) or the short form the commands use (`group`, `need`, `master`,
`round`, `ffa`, `personal`) or a number 0..5; `threshold` is 2..6, absent meaning uncommon (2). `icon` is one of
`star circle diamond triangle moon square cross skull` (case-insensitive). `party` gains `loot_method`,
`loot_threshold`, `loot_master` and `marks`, read from the model's last roster and icon packets; the serve client
(`internal/forever/serveclient`) carries the same four fields and a `PartyMark` type, so the strict
client-decode test (`TestServeClientTypesMatchTheDaemon`) keeps matching both ways.

The five old names leave `notPortedMethods`: `promote`, `assistant`, `loot_method`, `mark`, `disband`. `marks` is
new. R2 of #108 then took `convert_raid` and `raid_move` off the list too (docs/forever/RAID-R2-NOTES.md); the
not-ported methods that remain are listed in `notPortedMethods`.

## `party-check` (G2)

`--promote NAME` promotes and waits for the roster that shows it; `--promote-back DURATION` (with `--promote`) then
waits that long for the new leader to promote this character back, the second half of the two-agent walk.
`--loot METHOD[:THRESHOLD]` sets the method and reads the block back. `--mark ICON` sets the icon on the nearest
living unit hostile to this character (the `units` method's `hostile=true` filter) and reads the icon list back,
failing when the list does not hold it. A run with any of the three ends without leaving the group: a leave is what
ends a two-member group, and these flags exist to act on a group that must survive. The summary prints the icons as
`mark <icon> on <name-or-kind>`; it never prints a GUID.

The flags are validated before login (`--loot` and `--mark` values, `--promote-back` bounds), so a typo is a usage
error, not a run that logs in and fails.

## Tests

`internal/forever/world/serve_group_test.go`, against the fake world server over real sockets:

- `TestServeLootMethodReadsBack` (the round's failing case: `loot_method` answered not ported): group loot,
  threshold 2, read back from `party`; the fake saw the packet's method, threshold and empty master.
- `TestServePromote`: ok (the roster names the member leader), not-leader (refused, nothing sent), no-confirm (a
  silent server is the readback error).
- `TestServeAssistant`: set and clear (the fake saw apply then clear), an unknown name refused, and a silent server
  failing the flag's readback.
- `TestServeMarkAndMarks`: mark by name and by guid, the list after each, a moved target leaving its old slot, the
  party method carrying the same list, and an unknown icon refused with no packet.
- `TestServeDisband`: one uninvite per other member in roster order, never this character, and the group gone; a
  member that does not lead is refused with nothing sent.
- `TestServeGroupCallsNeedAGroup`: every call names the missing group.
- `TestServeGroupMethodTable`: the CLI contract table (the five names ported, `marks` new, and after R2 of #108
  `convert_raid` and `raid_move` too) and both raid names' refusals over the socket.

The fake's side (`serve_group_test.go`, `fake_chat_test.go`, `serve_fake_test.go`, `fake_world_test.go`): a roster
the daemon is already in (`groupNames`, leader, member flags, loot block, eight icon slots), the five client
packets answered with the roster that shows the change, a mark single/all answer, the destroyed update when the
last uninvite leaves this character alone, and `groupSilent` for a server that answers nothing. The member GUIDs
are player GUIDs: a packed GUID whose high type byte is zero reads back empty (`wire.readPackedGUID` mirrors the
core).

## Live walk (LG, still to run)

Spec section 8: two non-GM accounts, two `forever serve` sessions, no bots. A invites B; A `promote B` (B leader in
both rosters); B `promote A`; A `loot_method group threshold 2` (both rosters show group loot, uncommon); A `mark`
the nearest creature skull, B's `marks` lists it; A `mark` it cross (skull empty, cross set); A `disband` (both
not in a group). Pass = every readback seen; record the run here.
