# Forever chat and party, round 5 notes

Scope: talk and group on the Forever (Classic 1.60.1) world socket. Chat out (say, yell, whisper, party, emote text),
`SMSG_CHAT` in every form this core sends, the notification/`MOTD` system lines, and party membership: invite,
invite received, accept/decline, leave, uninvite, `SMSG_PARTY_UPDATE`, `SMSG_PARTY_MEMBER_FULL_STATE`, group
destroyed/kicked/leader changed. No trade, guilds, raids, loot or channels joined.

Code: `internal/forever/chat` (packet layouts, the chat type table, the log, the sanitiser),
`internal/forever/party` (packets and the model), `internal/forever/world/chat_session.go` (the session side),
`cmd/forever-agent/forever_chat.go` (`forever chat-check`, `forever party-check`). The dispatch of `session.go`
gained two grouped cases at the end of its switch; no existing case changed.

Every layout below was read in ForeverCore at 6517af05 and then checked against the real client's bytes in the four
private captures (112 `SMSG_CHAT`, one `SMSG_PRINT_NOTIFICATION`, 23 `SMSG_EMOTE`, 95 `SMSG_PARTY_UPDATE`, 45
`SMSG_PARTY_MEMBER_FULL_STATE`, one `SMSG_PARTY_COMMAND_RESULT` and one `SMSG_GROUP_NEW_LEADER`; the party packets
are mostly the bot sessions' groups in the login capture). Every one of those packets is consumed to its last byte
by the committed parser.

## Fix round 1 (2026-11-18): the review's items

**1. The roster is validated before it is actionable.** `party.State.ApplyUpdate` (internal/forever/party/model.go)
refuses a roster that has an empty or duplicate GUID, does not list this character's own GUID exactly once, does not
list the leader, or is a raid (`> MaxPartyMembers`). The character's GUID comes from `SMSG_MOVE_SET_ACTIVE_MOVER`
(the server names it before any party packet) and from the `ThisIsYou` create block. An invalid roster is counted
(`State.InvalidRosters`), its reason is kept (`LastInvalid`) and printed as `party: invalid roster: <reason>`; it
changes nothing, so a hostile update can neither add members nor make the client leave its real group. (Fix round 2
changed the disposition of the previous roster: a rejected snapshot now ends the party and keeps the displaced
members only in the diagnostic `Stale` copy; see below.) The session gates party chat, `CMSG_REQUEST_PARTY_MEMBER_STATS` and
`CMSG_LEAVE_GROUP` on `State.RosterValid`. Tests: `TestRosterValidation` (the four refusals plus an empty GUID and an
unknown self) and `TestPartyCheckRejectsAnInvalidRoster` (the fake server sends each hostile roster after the accept;
the client sends no stats request, no party line, no leave, and names the reason).

**2. The sanitiser holds its bound.** `chat.Sanitise` (internal/forever/chat/chat.go) now has one `put` function that
every branch writes through, so a control byte or an invalid UTF-8 byte cannot push the output past `max`: 1000
control bytes through `SanitiseName` (max 64) give 61 spaces plus `...`, 2000 invalid bytes give 61 `?` plus `...`,
`len(out) <= max` on every branch (and `max <= 0` gives ""). Tests: `TestSanitiseHoldsItsBound` and the property
fuzz `FuzzSanitise` (valid UTF-8, no control rune, never longer than max, for random input and max).

**3. `/`, `.` and `!` are commands for every chat kind.** The server parses a command before it validates the message
and does it for every chat type including whisper (`Chat.cpp:183-200`, called at `ChatHandler.cpp:234`), so
`chat.ValidateText` now rejects all three with `ErrCommandText`. A server command sent on purpose has its own
function, `chat.ValidateCommandText`, and its own session method, `(*world.session).SendCommand`, which nothing in
this round calls: the two can never mix. Tests: `TestValidateText`, `TestValidateCommandText`.

**4. The language comes from the race.** `chat.LanguageForRace` maps the eight Classic races to the ChrRaces
`BaseLanguage` value (human/gnome/dwarf → Common/Gnomish/Dwarvish 7/13/6, orc/troll/tauren/undead →
Orcish/Troll/Taurahe/Gutterspeak 1/14/3/33, night elf → Darnassian 2). The server never looks the race up itself: it
loads `ChrRacesEntry::BaseLanguage` (DB2Structure.h:902, DB2LoadInfo.h:1252) and never reads it; it gates the
language the client sends on the skill that language's spell teaches (ChatHandler.cpp:164-173 through
LanguageMgr.cpp:58-70), so a Horde character that sends Common gets "not learned" and nothing is said. Whisper keeps
the sent language through those checks and only then has `Player::Whisper` force LANG_UNIVERSAL on the two packets it
sends (Player.cpp:23706-23715); party keeps the sent language
(`HandleChatMessage` CHAT_MSG_PARTY, ChatHandler.cpp:157-175, 388-402). Builders take the language;
`chat-check`/`party-check` read the race from the character list. Tests: `TestLanguageForRace` (the table, an unknown
race, and the bytes each builder carries).

**5. Names, text and the invite.**
* `ValidateName` caps at the server's `MAX_PLAYER_NAME` 12 (ObjectMgr.h:941) and at the invite's 6-bit length
  (63, PartyPackets.cpp:39-72), allows ASCII letters only (the server's `isBasicLatinString` over a name,
  ObjectMgr.cpp:9172-9200 and Util.h:230-235), and `NormaliseName` mirrors the server's normalisation (trim, first
  word, lower case then upper case the first letter, ObjectMgr.cpp:157-185). `--invite`, `--accept-from`,
  `--decline-from`, `--uninvite`, `--watch` and `--whisper` all normalize before use.
* `ValidateText` requires valid UTF-8 (the server's packet reader throws on invalid UTF-8, ByteBuffer.cpp:72-86 via
  PacketOperators.h:303-312) and refuses every `|` (`ErrTextHyperlink`): the server validates hyperlinks in every
  chat line and may kick on a malformed one (WorldSession.cpp:819-828, Hyperlinks.cpp:881-921). This round refuses
  all of them rather than re-implementing the link checker.
* Name comparisons are case-insensitive, the server's rule (`PlayerNameMapHolder::Find` normalises before the lookup:
  ObjectAccessor.cpp:89-97, ObjectMgr.cpp:157-185). The invite is cleared once answered (`State.ClearInvite`), and
  an invite whose `CanAccept` is false is refused with a named error instead of answered.

**6. The writer tests are hand-built from the server's readers.** `TestWritersShape` now compares `BuildInvite`,
`BuildInviteResponse`, `BuildUninvite` and `BuildRequestMemberStats` with literal bytes derived from
`PartyPackets.cpp:39-72`, `:126-136`, `:139-150` and `:168-179` (bit order, the reader's `ResetBitPos` after the
optional index, the packed-GUID mask), not with the client's own reader. `TestModelBounds` now feeds a duplicate
roster at the party size on every iteration (the raid branch would otherwise take a sixth member first).

**7. The live two-client proof and the fixtures** are in "Two clients, the fix round" below; `--capture-dir` writes every chat
and party payload the server sent to a directory outside the repository, and `forever chat-check --watch NAME`
prints another player's position from the world model once a second.

## Fix round 2 (2026-11-18, last): the re-review's items

**1. A rejected roster ends the party.** `State.ApplyUpdate` (internal/forever/party/model.go) now calls
`rejectRoster` on every roster that fails validation: `RosterValid` and `InParty` go false, `Members`, `Order` and
`LeaderGUID` are cleared, and the members the last accepted roster held are moved to `State.Stale`, a diagnostic
`*Roster` that nothing acts on. Party chat, `CMSG_REQUEST_PARTY_MEMBER_STATS`, `CMSG_PARTY_UNINVITE` and
`CMSG_LEAVE_GROUP` are all gated on `RosterValid`, so a rejected latest snapshot blocks every outbound party action
instead of leaving the previous roster actionable. The session prints `party: no group (invalid roster: ...)` plus
`party: the displaced roster had N member(s), leader X (diagnostic only)`. Tests: `TestRosterValidation` (each refusal
after a valid roster: not actionable, `Stale` holds the displaced two, and a later valid roster still works) and
`TestModelBounds` (5000 rejections leave the model empty and bounded).

**2. `validateRoster` accepts only rosters this core sends.** The added rules come from `Group::SendUpdateToPlayer`
(ForeverCore, Group.cpp): `PartyType` is `GROUP_TYPE_NORMAL` for a created group and `GROUP_TYPE_NONE` only in
`SendUpdateDestroyGroupToPlayer` together with the destroyed flag (Group.cpp:856, :930-936), so a non-destroyed
update with type 0 is refused; `PartyGUID` is `m_guid`, set in `Create` (Group.cpp:150, written at :858), so an empty
one is refused; `MyIndex` is the position of the member slot the player was handed (Group.cpp:866-875) and is -1
only when the player has no slot, in which case `SendUpdateToPlayer` returned before writing (Group.cpp:843-849), so
`MyIndex` must be in range and `Members[MyIndex].GUID` must be this character's GUID. The existing rules (non-empty
unique GUIDs, self present exactly once, leader listed) stay. A 6-to-40-member roster now goes through the SAME
validation BEFORE `Raid`/`InParty` are set or the member list is replaced. Tests: `TestRosterValidation` (the new
cases: type 0, empty party GUID, MyIndex out of range, MyIndex naming another member), `TestRaidRosterValidation`
(valid raid; raid with a duplicate GUID; raid without self) and the hand-built duplicate fixture (below).

**3. The roster is bound to the transition that produced it.** `State.AcceptInvite` (called by party-check instead of
`ClearInvite` on an accept) arms the check with the inviter's GUID; `ApplyCommandResult` with `PARTY_OP_INVITE` and
`ERR_PARTY_RESULT_OK` (GroupHandler.cpp:206-210) arms it with the invited name. The FIRST roster after the transition
is checked (the expectation is then consumed, and a decline of that invite drops it): after an accept its leader must
be the inviter, after a confirmed outgoing invite it must list the invited name (case-insensitively). One roster is
exempt from the outgoing-invite rule: a roster that lists this character alone. The live realm sends exactly that as
soon as the group is created for the invite (the two-client run's first roster to the inviter: type normal, MyIndex
0, one member, 109 bytes), before the invitee has answered; it is consistent with the transition, so the check stays
armed for the next roster, and a later roster that lists someone else without the invited name is unexpected. A roster that
fails is "unexpected roster": `UnexpectedRosters`, `LastUnexpected`, the party is ended by `rejectRoster` and the
session prints `party: unexpected roster: ...`; `waitPartyJoined` fails the run with that reason. This is a
consistency check (does the roster belong to the transition the client made?), not a defence against a lying server:
a server that lies can send a roster that matches. Tests: `TestRosterTransition` (accept with the inviter leading,
accept with a roster led by the client, confirmed outgoing invite with and without the name, case-insensitive name),
and the fake server's accept flow, whose roster now carries the inviter as leader.

**4. `--capture-dir` writes a new run directory.** `internal/forever/world/capture_run.go` replaces the old
`os.MkdirAll` + `os.Create` path: the option is now the parent, and each run creates
`<parent>/<UTC start clock>-<4 random bytes>/` (the link's start time, `session.start`). The directory and every file
in it (the manifest and each `<NNNN>_<NAME>.bin`) are created exclusively (`O_CREATE|O_EXCL` for the files), so an
earlier run's manifest or packets are never truncated and a name collision is an error, not an overwrite. A path
component that is a symlink or not a regular directory is refused (Lstat walk), so a capture cannot be redirected
through a link. One run is bounded: at most `maxCapturePackets` (8192) packet files and `maxCaptureBytes` (32 MiB) of
payload, each file one packet's payload; past a bound the run logs it once and stops capturing. Tests:
`TestCaptureRunIsNewAndNeverTruncates`, `TestCaptureRefusesASymlink`, `TestCaptureRefusesAFileComponent`,
`TestCaptureCollisionIsAnError`, `TestCaptureFilesAreExclusive`, `TestCaptureBounds`.

The merge round made this the session's **one** capture: the combat round's `captureWriter` (`packets.jsonl`) is
gone, `capturePacket` is called by the combat, chat and party handlers, and `report.Captured` counts all of them
(`TestCaptureIsOneForCombatChatAndParty`). One manifest format, read by one reader, `internal/forever/capturedir`,
which both fixture generators use (`FOREVER_CAPTURE_DIR` for chat/party and `FOREVER_LIVE_CAPTURE` for combat accept
a run directory or a parent of run directories).

**5. One name rule.** `ApplyLeaderChanged` now finds the member with `strings.EqualFold`, as the rest of the party
code compares names (the server normalises before its own lookup). The merge round deleted the world package's
hand-rolled `equalFoldASCII`, so `strings.EqualFold` is the module's one case-insensitive name comparison. Test:
`TestLeaderChangeIgnoresCase`.

**6. The fixture audit and the untested parser paths.** `party_test.go` now has the one `auditFixture` helper
`chat_test.go` has: every fixture has a case (a missing one is a finding), and it checks every packed GUID against
the fixture's `synthetic_guids`, the invite's two realm identifiers against 4242, and every audited string against
the generator's run. `group_new_leader` is no longer skipped. The parsers record every name with its offset and
length in `Audit.Strings` (member names, the inviter's name and surname, both realm names, the pet's name and the
command result's name and surname), so the generator rewrites each at its own offset instead of by value; the realm
names are now checked as names (a 2- or 4-byte realm name is no longer mistaken for a 4242 ident). The negative
subtests mutate a GUID, a realm identifier and a name and each must produce a finding. `syntheticGUID` carried
`n%5`, which gave two members of `party_update_full` the same synthetic GUID; it now carries `n` in base 64 over the
low mask bytes, the fixtures are regenerated, and `party_update_full`'s five members hold five distinct GUIDs. One
fixture is hand-built, `party_update_duplicate_members` (two member slots, one GUID): the model's negative case,
which parses and must be refused by `validateRoster` (`TestRosterValidation/the_duplicate_fixture`). The parser
paths no capture holds now have hand-built cases with sentinels: `TestUpdateOptionalBlocks` walks
`ChallengeModeData` and `PartyLFGInfo` before the loot and difficulty sentinels (PartyPackets.cpp:524-541, :543-558,
:565-577); `TestMemberStateOptionalBlocks` walks phases, conditional flags, one score run and a pet with one aura
(PartyPackets.cpp:180-215, :241-272), the pet's name `SENTINELPET` last.

**7. One dispatch line each.** The two handlers are still registered by the two grouped cases at the end of
`internal/forever/world/session.go`'s switch: chat is one grouped case (nine chat/notification opcodes, one
`return s.handleChat(p)`), party is the next (twelve party opcodes, one `return s.handleParty(p)`), both ahead of the
`default: s.handleCombat(p)`. No
existing case changed.

**8. The race detector.** Both gate runs are made on the final commit (clean worktree):
`vm-test ta ~/wt-fa-chat -- 'go test -race -count=1 -timeout 20m ./...'` and
`vm-test ta ~/wt-fa-chat -- 'go test -race -count=10 ./internal/forever/chat/ ./internal/forever/party/ ./internal/forever/world/'`.
The party model, the chat log and the capture run are per session and touched only by that session's packet loop;
the shared-state table is [`SESSION-STATE.md`](SESSION-STATE.md).

## Build predicates that matter

The live realm is build 70707, so `ClassicOpcodes::IsBuild70291OrLater()` is true on the server:
`SMSG_PARTY_UPDATE` carries the 70291 `int8` before the leader GUID, the 70291 `uint64` before the member count, and
the per-member `VoiceStateID` length in 9 bits (`PartyPackets.cpp:507-522`, `:584-607`, `ClassicOpcodes.cpp:101-117`).
A parser that reads the retail (70245) layout desynchronises on the first member and on the optional-block bits.

## `SMSG_CHAT` (`Chat::Write`, ChatPackets.cpp:196-238)

| field | type | note |
|---|---|---|
| SlashCmd | `uint8` | `ChatMsg`; the core writes `-1` (CHAT_MSG_ADDON) as `0xFF` |
| Language | `uint32` | `Language` (SharedDefines.h:1235-1275) |
| SenderGUID, SenderGuildGUID, SenderWowAccount, TargetGUID | packed GUID ×4 | `ObjectGuid` `operator<<` is the packed form (ObjectGuid.cpp:1087-1111) |
| TargetVirtualAddress | `uint32` | written **before** SenderVirtualAddress |
| SenderVirtualAddress | `uint32` | |
| AchievementID | `int32` | |
| ChatFlags | `uint32` | SharedDefines.h:6557-6571 |
| DisplayTime | `float` | |
| SpellID | `int32` | |
| SenderName, TargetName, Prefix, Channel, ChatText | bit sizes 11, 11, 5, 7, 12 | `SizedString` = length, no NUL |
| HideChatLog, FakeSenderName | 1 bit each | |
| BroadcastTextID, ChannelGUID, EncounterEventID | 3 presence bits | |
| flush | | |
| SenderName, TargetName, Prefix, Channel, ChatText | bytes | |
| BroadcastTextID | `uint32` if present | |
| ChannelGUID | packed GUID if present | |
| EncounterEventID | `uint32` if present | |

The four GUIDs and the optional channel GUID are passed through `wire.Buffer.SetGUIDHook`, so the fixture audit sees
them even though the round drops the guild and account GUIDs. The two virtual-realm addresses are not GUIDs; the
parser keeps them (`Line.SenderVirtualAddress`, `.TargetVirtualAddress`) so the fixture generator rewrites them and
the audit checks them, exactly like the name answer's club id and realm addresses in round 3.

### The chat type table

`ChatMsg` (SharedDefines.h:6479-6553) has 70 names this client decodes: `CHAT_MSG_ADDON` (-1, serialised as 0xFF)
and 0x00..0x44. Every name in the enum is in `chat.Types`; a type outside it is counted by its number
(`Log.Unknown[type]++`) and skipped, never an error. The captures hold only 0x00 SYSTEM, 0x02 PARTY, 0x07 WHISPER,
0x11 CHANNEL, 0x2E ACHIEVEMENT and 0x31 PARTY_LEADER; the rest of the table is names only, and the parser handles
all of them because the layout does not depend on the type.

## `SMSG_CHAT` in, per form this core sends

| what | writer | notes |
|---|---|---|
| say / yell / party / party leader / raid | `Chat::Write` | the sender receives their own say and party lines (`Player::SendChatMessageToSetInRange`, Player.cpp:23647-23658) |
| whisper | `Player::Whisper` sets SenderName and TargetName to the **sender's** name on the WHISPER packet and to the **target's** name on the WHISPER_INFORM packet (Player.cpp:23706-23731) | language is forced to LANG_UNIVERSAL |
| whisper inform | same | the sender's copy |
| system (MOTD, `SendSysMessage`, `announce`'s StringParam is a different packet) | `ChatHandler::SendSysMessage`, Chat.cpp:111-125 | `SMSG_CHAT` type 0, no sender |
| monster say/yell/whisper/emote | `CreatureTextTextBuilder`/`Talk` | same layout, SenderName filled for a creature |
| channel | `Channel::Say` | Channel name in the Channel field |
| addon | `Chat::Initialize(type, LANG_ADDON…)` with Prefix | LANG_ADDON (183) / LANG_ADDON_LOGGED (184) |
| emote / text emote (chat text) | `Player::TextEmote` | type 0x0A, SenderName empty, text only |
| achievement | `Chat::Initialize(CHAT_MSG_ACHIEVEMENT…)` | type 0x2E |

`SMSG_CHAT_PLAYER_NOTFOUND` (ChatPackets.cpp:284-292) is a 9-bit name and bytes.
`SMSG_CHAT_PLAYER_AMBIGUOUS` is the same. `SMSG_CHAT_RESTRICTED` is one `int32` reason
(`ChatRestrictionType`, WorldSession.h:933-940). `SMSG_CHAT_SERVER_MESSAGE` (ChatPackets.cpp:294-302) is
`int32` MessageID + 11-bit StringParam. `SMSG_PRINT_NOTIFICATION` (ChatPackets.cpp:274-282) is a 12-bit text.
`SMSG_MOTD` (SystemPackets.cpp:405-419) is a 4-bit line count then per line a 7-bit length and bytes. This client
decodes all of them as system text; the announce `MessageID` has no text in the packet (the client resolves it from
its own DB2), so it is reported as `server message #N` plus the StringParam.

`SMSG_EMOTE` (ChatPackets.cpp:240-250) is a packed GUID, `uint32` emote, `uint32` count, `int32` sequence variation
and `count` × `int32` spell visual kits (the append is four bytes per entry). `SMSG_TEXT_EMOTE` is three packed GUIDs
and two `int32`.

## What a client can hit in `HandleChatMessage` (ChatHandler.cpp:145-505)

| check | line | effect on the client |
|---|---|---|
| `LANG_UNIVERSAL` for say/yell/party/raid/whisper | 149-155 | `SendNotification(LANG_UNKNOWN_LANGUAGE)`, which is `SMSG_PRINT_NOTIFICATION`; nothing is said |
| language not in `sLanguageMgr` | 158-165 | same notification |
| language not learned (no skill, no comprehend aura) | 164-173 | `SMSG_PRINT_NOTIFICATION` (not learned) |
| muted | 211-217 | `SMSG_PRINT_NOTIFICATION` with the remaining seconds |
| GM silence aura | 222-227 | notification, except for whispers |
| `msg.size() > 511` | 227-228 | **nothing is sent**: the handler returns silently |
| empty message | 230-231 | returns silently |
| chat command (text starts with `/` or `.`, `ChatHandler::ParseCommands`) | 233-234 | the command runs; **no chat line** |
| control characters, or a newline | 62-90, 237-239 | returns, logs `network`; only a leading `\n`/`\r` is dropped by a cut, other crashes the check |
| malformed item/spell hyperlink | 241-242 | may kick |
| say/yell/emote and dead | 246-290 | implicit result, no line |
| say/yell/emote below `ChatLevelReq.Say/Yell/Emote` (default 1) | 246-290 | `SMSG_PRINT_NOTIFICATION` with the requirement |
| whisper to a name that is not connected | 293-320 | `SMSG_CHAT_PLAYER_NOTFOUND` with the name as sent |
| whisper, wrong faction | 324-335 | `SMSG_CHAT_PLAYER_NOTFOUND` (the server hides the reason) |
| whisper to yourself | 293-378 | **allowed**: the server sends type 0x07 WHISPER and type 0x09 WHISPER_INFORM to the sender, both with SenderName/TargetName = the sender's name (Player.cpp:23706-23746); the `FBTrade` self-whisper path only skips this for that prefix |
| party with no group | 369-389 | `ChatMessageResult::NotInGroup`, **no packet** |
| flood: more than `ChatFlood.MessageCount` (10) in `ChatFlood.MessageDelay` (1 s) | 218-219, Player.cpp:23104-23160 | muted `ChatFlood.MuteTime` (10 s); the next chat gets the mute notification |

The client enforces the parts it can check before sending, with named errors: empty text, text over 511 bytes,
control characters/newlines, and text that starts with `/` or `.` (a command, not chat). Language is LANG_COMMON (7)
for a human: the captured client sends 7 for say, party and whisper, and the handler refuses LANG_UNIVERSAL for
say/yell/party/whisper. The server substitutes: a whisper is always rewritten to LANG_UNIVERSAL
(Player.cpp:23711-23713); a say/party/yell keeps the sent language and `ChatPacketSender` sends the untranslated text
to a player who understands the language and `sLanguageMgr->Translate(text, language, locale)` to one who does not
(ChatTextBuilder.cpp:63-92). The packet's Language is the **spoken** language in both cases.

## Client packets (the writers)

| packet | reader | layout |
|---|---|---|
| `CMSG_CHAT_MESSAGE_SAY/YELL/PARTY` | ChatPackets.cpp:26-43 | `u32` language, 11-bit text length, 1 bit IsSecure only for say/party/raid/warning/instance, flush, text |
| `CMSG_CHAT_MESSAGE_WHISPER` | :45-60 | `u32` language, packed target GUID, `u32` target virtual realm, 9-bit Target (`SizedCString`), 9-bit unknown name, 11-bit Text, flush, then the three data strings (NUL only when non-empty) |
| `CMSG_CHAT_MESSAGE_EMOTE` | :126-132 | 11-bit text, flush, text (no language; the handler uses LANG_UNIVERSAL) |
| `CMSG_PARTY_INVITE` | PartyPackets.cpp:50-80 | 1 bit PartyIndex present, `ResetBitPos`, 6-bit name length, 9-bit realm length, `u32` realm address, packed target GUID, name bytes, realm bytes, optional `u8` index |
| `CMSG_PARTY_INVITE_RESPONSE` | :126-137 | optional-index bit, accept bit, optional-roles bit, then optional bytes |
| `CMSG_PARTY_UNINVITE` | :139-149 | optional-index bit, 9-bit reason length, packed target GUID, optional index, reason bytes |
| `CMSG_LEAVE_GROUP` | :327-332 | optional-index bit, flush |
| `CMSG_REQUEST_PARTY_MEMBER_STATS` | :168-179 | optional-index bit, `u32` count, optional index byte, packed GUIDs |

The real client's only captured party packet with the optional index is `CMSG_REQUEST_PARTY_MEMBER_STATS`: it sends
the index bit **set** and the index byte `0` after the count (`80 01 00 00 00 00 …`). The invite/response writers
send the index in the same shape; no capture contains those two packets, so the constant is taken from the
member-stats packet and the server's reader, and the fake server checks both ends. `CMSG_LEAVE_GROUP` is captured as
one `00` byte. Two details come straight from the captured client bytes: `CMSG_CHAT_MESSAGE_SAY` has `IsSecure` set
and `CMSG_CHAT_MESSAGE_PARTY` has it clear (the server reads the bit and does not use it), and the whisper's empty
Classic second name is written with a **zero** 9-bit prefix (the server's reader treats 0 and 1 alike, and the real
client writes 0).

## `SMSG_PARTY_UPDATE` (`PartyUpdate::Write`, PartyPackets.cpp:584-627), realm build ≥ 70291

```
uint16 PartyFlags, uint8 PartyIndex, uint8 PartyType, int32 MyIndex,
packed PartyGUID, uint32 SequenceNum, int8 BeforeLeader,
packed LeaderGUID, uint8 LeaderFactionGroup, int32 PingRestriction, uint64 BeforeMemberCount,
uint32 memberCount,
per member: 6-bit name length, 9-bit VoiceStateID length (SizedCString: length+1), 3 bits
            (Connected, FromSocialQueue, VoiceChatSilenced), packed GUID, uint8 Subgroup, Flags,
            RolesAssigned, RolesUnk_1210, Class, FactionGroup,
            LeaverInfo (packed BnetAccountGUID, float LeaveScore, u32 SeasonID, u32 TotalLeaves,
            u32 TotalSuccesses, i32 ConsecutiveSuccesses, i64 LastPenaltyTime, i64 LeaverExpirationTime,
            i32 Flags, 1 bit LeaverStatus, flush),
            name bytes, VoiceStateID bytes + NUL (when non-empty),
4 optional-init bits (ChallengeMode, LfgInfos, LootSettings, DifficultySettings), flush,
then each present block in that order: ChallengeMode, LfgInfos, LootSettings (u8 method, packed LootMaster,
u8 threshold), DifficultySettings (three int16)
```

`GROUP_FLAG_DESTROYED` (0x010) is the final update of a disband (`SendUpdateDestroyGroupToPlayer`,
Group.cpp:930-944): `PartyType` 0, `MyIndex` -1, no members. The capture has exactly that packet at leave.

## `SMSG_PARTY_MEMBER_FULL_STATE` (`PartyMemberFullState::Write`, PartyPackets.cpp:279-288 + 241-272)

```
1 bit ForEnemy, flush, packed MemberGuid,
2× uint8 PartyType, u32 Status, u8 PowerType, u16 PowerDisplayID,
i32 CurrentHealth, i32 MaxHealth, u16 CurrentPower, u16 MaxPower,
u16 Level, u16 SpecID, u16 ZoneID, u16 WmoGroupID, u32 WmoDoodadPlacementID,
i16 X, i16 Y, i16 Z, i32 VehicleSeat, u32 auraCount,
phases: u32 PhaseShiftFlags, u32 list count, packed PersonalGUID, count × (u8 flags, u16 id)
ChromieTime: u32 conditional count, i8 FactionGroup, u32 mask, count × u32 (append: four bytes each)
DungeonScoreSummary: 2 floats, u32 run count, per run i32/float/i32/i32/u8/1 bit + flush
auras: count × (i32 spell, u16 flags, u32 active, i32 points count, count × float)
1 bit has pet, flush, and if set the pet block (packed GUID, i32 model, i32/i32 health, auras,
8-bit name length, flush, name bytes)
```

The aura count is written **before** the phases/chromie/score blocks but the aura entries come **after** them; a
parser that reads the entries where the count is desynchronises on every captured state. This round keeps the count
and the spell ids only as a count (`MemberState.Auras`), per the brief.

`SMSG_PARTY_MEMBER_PARTIAL_STATE` has no writer in this core (`Opcodes.cpp:2149`, STATUS_UNHANDLED); it is named and
counted, and its payload is skipped. `SMSG_READY_CHECK_STARTED/RESPONSE/COMPLETED` are named and counted only.

## Party packet family

| packet | writer | this round |
|---|---|---|
| `SMSG_PARTY_INVITE` | PartyPackets.cpp:82-107 | parsed: inviter GUID, BNet account GUID, cfg realm, realm address and both names, proposed roles, lfg slots; no capture |
| `SMSG_GROUP_DECLINE` | :151-159 | parsed: 9-bit name; no capture |
| `SMSG_GROUP_UNINVITE` | :161-166 | parsed: `u8` reason (kicked); no capture |
| `SMSG_GROUP_DESTROYED` | Group.h:756 | parsed: empty; no capture |
| `SMSG_GROUP_NEW_LEADER` | :480-489 | parsed: `int8` index, 9-bit name |
| `SMSG_PARTY_COMMAND_RESULT` | :34-48 | parsed: 6/9-bit name and surname, 4-bit command, 6-bit result, `u32` data, packed GUID |
| `SMSG_PARTY_UPDATE` | :584-627 | parsed into the model |
| `SMSG_PARTY_MEMBER_FULL_STATE` | :279-288 | parsed, bounded; merged into an existing member only |
| `SMSG_PARTY_MEMBER_PARTIAL_STATE` | — | named and counted, skipped |

## Bounds

| structure | key or length | bound |
|---|---|---|
| `chat.Line` strings | server text | sender/target name ≤ 64 bytes, prefix ≤ 16, channel ≤ 128, text ≤ 512, each sanitised |
| `chat.Log.lines` | every chat line | `maxChatLog` 256, a ring |
| `chat.Log.Unknown` | `uint8` type | 256 keys (the wire type is one byte) |
| `chat.Log.Counts` | `uint8` type | at most 256 keys (the wire type is one byte) |
| `party.Update.Members` | `uint32` count | `maxRaidMembers` 40 (MAX_RAID_SIZE, Group.h:51); a count above it is a named error and the packet is skipped |
| `party.State.Members` | roster GUIDs | `maxPartyMembers` 5 (MAX_GROUP_SIZE, Group.h:50); a roster over 5 is parsed and bounded but reported `raid: not handled in this round` and not merged |
| `party.State` roster validation | the update's member list | at most 5 members, no empty or duplicate GUID, this character's GUID exactly once, the leader present; a rejected roster is counted (`InvalidRosters`), its reason kept (`LastInvalid`), and nothing else changes |
| `party.State.Invite` | one invite | replaced by the newest, cleared when answered; `LfgSlots` bounded by the bytes left |
| `--capture-dir` | one file per captured packet (combat, chat and party alike), plus the manifest | `maxCapturePackets` 8192 files and `maxCaptureBytes` 32 MiB per run; written outside the repository |
| member `Auras` | `uint32` count | kept as a count, capped at `maxAuras` 256; the entries are walked by their declared points count, each checked against the remaining bytes |
| member phases | `uint32` count | `maxPhases` 256, each check against remaining bytes |
| ChromieTime flags | `uint32` count | checked against remaining bytes (4 bytes each) |
| DungeonScore runs | `uint32` count | checked against remaining bytes (18 bytes each) |
| `party.State.Events` | group events | `MaxEvents` 128, a ring |
| `party.Invite.LfgSlots` | `uint32` count | checked against remaining bytes (4 bytes each) |
| `chat`/`party` parser allocation | declared counts | no slice is allocated from a count before it is checked against the bytes that are left |

## Three-mistakes checklist, per parser

1. **`append(ptr, count)` is `count * sizeof(T)` bytes.** In this round: `Emote::Write` appends
   `SpellVisualKitIDs.data(), size()` (int32 → 4 bytes each, ChatPackets.cpp:246-248); `CTROptions` appends
   `ConditionalFlags.data(), size()` (uint32 → 4 bytes each, PartyPackets.cpp:199-204). Both parsers read four bytes
   per entry. No other `append(ptr, count)` is on a path this round parses: `PartyMemberStats` writes every field
   with `<<`, and the phase/pet/score lists are written element by element.
2. **Sized strings.** The table above lists every string with its bit width. `SizedString` writes the length and
   no NUL; `SizedCString` writes length+1 and a NUL, and writes **no bytes at all** for an empty string
   (PacketOperators.h:322-400). The round's `SizedCString` users are the whisper's Target/unknown/Text and the
   party update per-member VoiceStateID. On the read side both 0 and 1 are legal and mean "empty": the server's
   `SizeReaderWriter` only reads bytes when the prefix is greater than 1 (`PacketOperators.h:341-352`), so a zero
   prefix is not malformed and our parsers accept 0 and 1 (the fake server's reader does the same).
3. **A fixture built by the writer proves nothing.** Every parser test that has a capture uses bytes the server
   sent to the real client (captured packets), rewritten only in their identifiers; the writer tests use the real
   client's captured CMSG bytes. Hand-built fixtures are labelled, carry a sentinel, and exist only where no capture
   has the case.

## Fixtures and their source

`internal/forever/chat/testdata/chat.json` holds 9 payloads cut from the capture by `fixture_gen_test.go` (a
one-off: `FOREVER_CAPTURE_PKT` with the four files + `FOREVER_WRITE_FIXTURES=1`; the committed fixtures need
neither). They are `chat_system`, `chat_party`, `chat_party_leader`, `chat_whisper_addon` (an addon whisper,
LANG_ADDON), `chat_whisper` (a player whisper with SenderName/TargetName filled), `chat_channel`,
`chat_achievement`, `print_notification` and `emote`. The generator replaces every packed GUID the parse decoded,
including the three the round drops, with a synthetic GUID of the same mask and type; sets both virtual-realm
addresses to 4242 by the offset the audit parse recorded; and replaces the sender name, the target name, the addon
prefix, the channel and the text with runs of the same length (the prefix is rewritten since the fix round; the
earlier generator left the real addon prefix in the payload while claiming every string was replaced). It then
re-parses the result and fails unless the structure is unchanged and every string is the synthetic run. The
committed `rewritten` list says what was replaced, never with an offset or an original value.

`internal/forever/party/testdata/party.json` holds 12 payloads. `party_update_small`, `party_update_full`,
`party_update_destroyed`, `member_state_2` and `group_new_leader` are cut from the private PKT capture; `party_invite`,
`party_update_joined`, `group_decline`, `group_destroyed`, `party_command_result` (the invite's ok answer) and
`member_state_1` come from the two-client `--capture-dir` runs, read from the manifest the capture writes.
`party_update_duplicate_members` is the one hand-built fixture (fix round 2): an update the core never writes, with
two member slots carrying one GUID, which parses and which `validateRoster` must reject. `group_uninvite` is the one
packet no capture can hold here: this core disbands a two-member group instead of writing it (Group.cpp:588-591,
610-611), and a third member was not available; it is covered by `TestHandbuiltGroupUninvite`. In every one, GUIDs
are synthetic; member, inviter, realm and pet names are runs of `P`, `N`/`S` and `R` at the offsets `Audit.Strings`
recorded (fix round 2), so a realm name is checked as a name and not as a 4242 ident; the invite's cfg realm id and
realm address are 4242; the member state's health, power, level, zone and position are set to fixed synthetic
numbers at the offsets the parser recorded (`health 100/200`, `power 50/100`, `level 1`, `zone 12`, position
`1000 2000 50`). `syntheticGUID` carries the GUID's audit position in the first mask bytes, so two distinct GUIDs
never share a synthetic one (fix round 2; `n%5` did, and `party_update_full` held duplicates).

The audit is one helper, `auditFixture`, and it covers every fixture: a missing case is a finding. It walks the
parser hook and fails on any GUID that is not in the fixture's `synthetic_guids` list, on a realm identifier that is
not 4242, and on an audited string that is not the generator's run. The negative subtests put a different player
GUID, a non-4242 realm identifier and a non-run member name into a payload, and each must produce a finding. No
original capture identifier appears in a committed payload; the only capture data kept is the packet structure.

The forms no capture holds are built in `chat/chat_test.go` (say, yell, whisper inform, monster yell, an addon
party line, the optional blocks, `MOTD`, `SMSG_CHAT_SERVER_MESSAGE`, player-not-found, ambiguous, restricted, text
emote, an emote with visual kits) and in `party/party_test.go` (a one-member update with a non-empty 9-bit
VoiceStateID, an invite with a non-empty LFG slot list, the challenge-mode and LFG optional blocks before loot
sentinels, and a member state with phases, conditional flags, a score run and a pet; the plain invite is the real
packet now), each labelled and carrying a sentinel (`SENTINEL-TEXT`, `SENTINEL-1`, `SENTINELPET`).

The client writers are proven by the real client's bytes: `TestCapturedClientWriters` reproduces the captured
say (both), party and whisper (both) payloads byte for byte, with only their names and text replaced by synthetic
runs of the same length; the layout bytes (language, bit widths, the IsSecure bit, the whisper's zero second-name
prefix and NULs) are the client's.

## What is skipped or approximated

* Raid groups: a roster over 5 members passes the same validation as a party roster (fix round 2), is then parsed
  and bounded and reported as `raid: not handled in this round`; no raid members are merged into the model.
* `SMSG_PARTY_MEMBER_PARTIAL_STATE`: never sent by this core; counted, not parsed.
* Ready checks: detected and counted by name, not answered.
* Channels are decoded (name, text) but not joined or managed; `CMSG_CHAT_MESSAGE_CHANNEL` is not sent.
* Addon messages are decoded as chat lines (prefix kept); the client does not register prefixes or send addon
  messages.
* `SMSG_CHAT_SERVER_MESSAGE` carries only a message id and a string parameter; the id's text lives in the client's
  ServerMessages DB2, which this client does not read.
* The party model merges a full-state packet only for a member the last update listed. A state for an unknown GUID
  is counted, not created (the roster is the authority).

## What the tests pin

* `TestFixtureChat` / `TestFixtureParty` — every capture-derived fixture parsed to its last byte, with the fields
  that identify the form (type, language, prefix, channel, member names, destroyed flags, synthetic health/level/
  position, command result) asserted.
* `TestFixtureIdentifiersAreSynthetic` — the sweep above, with negative subtests.
* `TestCapturedClientWriters`, `TestEmoteWriter` — the client packets that have captured bytes (say, party, whisper,
  emote). `TestWritersShape` — invite, invite response, uninvite, leave and member stats against literal bytes
  derived from the server's readers (PartyPackets.cpp:39-72, 126-136, 139-150, 168-179, 327-331).
* `TestHandbuiltChatForms`, `TestHandbuiltReplies`, `TestSizedCStringVoiceState`, `TestHandbuiltGroupUninvite` —
  the forms no capture holds (the last is the one-byte GROUP_UNINVITE, which a two-member group cannot produce).
* `TestSanitiseHostileStrings` — the table: NUL/ANSI/control bytes, a newline, DEL and a C1 control, valid and
  invalid UTF-8, colour codes, a terminated and an unterminated hyperlink, a one-megabyte string. Every output is
  printable, carries no control byte and is at most 512 bytes.
* `TestChatLogIsBoundedAndSanitised` — 10,000 lines keep 256, all sanitised; 300 distinct unknown types are counted
  by number and never an error.
* `TestModelFlow`, `TestRaidRosterIsNotModelled`, `TestModelBounds` — update → state → leader change → decline →
  leave → destroyed; a 6-member roster is reported, not modelled; 5,000 updates leave 5 members and 128 events.
* `TestParserBounds` — a 41-member update, a count that cannot fit, an aura count over the limit, a state count
  with no bytes, trailing bytes: all named errors, no allocation from the declared count.
* `TestParseAllocationBound` — chat 8 allocations per parse, a 5-member party update 24.
* `FuzzParseChat` / `FuzzParseParty` — the seeds are every fixture; no panic, and a chat parse that succeeds keeps
  a sanitised line.
* `TestChatCheckAgainstFakeServer`, `TestPartyCheckInviteAgainstFakeServer`,
  `TestPartyCheckAcceptFlowAgainstFakeServer` — the commands end to end against the fake world server: the say echo,
  a whisper to an offline name (`SMSG_CHAT_PLAYER_NOTFOUND`), the emote echo, the print notification and a system
  line; the invite name parsed by the server and answered OK; the invite/accept/update/two member states/party
  line/leave/destroyed flow, with the fake asserting each client packet it received.
* Outside CI, `TestCaptureChat` and `TestCaptureParty` parse every packet of the four capture files (112
  `SMSG_CHAT` + 24 other chat/system packets, 142 party packets) to its last byte.

## What works live (2026-11-18)

Both commands ran against the audit server with `--build 70707` and a level-1 human warrior created with the
server's own `tb create` for this round (`<character>` below). The combat worker's character was never logged in.

`forever chat-check --say "forever chat-check one" --whisper "<name>:forever whisper one" --emote wave --listen 14s`:

```
chat: CHAT_MSG_SYSTEM: Welcome to a TrinityCore server.
chat-check: said 22 byte(s) (language Common)
chat-check: emote text 4 byte(s)
chat-check: whispered 19 byte(s) to <name>
chat-check: listening 14s
chat: CHAT_MSG_SAY <you>: forever chat-check one
chat: CHAT_MSG_EMOTE <you>: wave
chat: player not found: <name>
chat: system: Global notify: CHAT-SENTINEL-1
...
chat-check: lines by type: CHAT_MSG_SYSTEM 2, CHAT_MSG_SAY 1, CHAT_MSG_EMOTE 1
chat-check: 4 chat line(s), 1 notice(s), 1 player-not-found answer(s), 0 restricted, 0 emote animation(s), 0 text emote(s)
world logout: SMSG_LOGOUT_RESPONSE (result 0, instant false) and SMSG_LOGOUT_COMPLETE
```

On the first run the server's MOTD and achievement strings arrived with the literal `%!s(MISSING)` (the core's
formatting, not this client's); the second run's MOTD was plain. Either way it is server text and is sanitised the
same. The console line `.notify CHAT-SENTINEL-1` arrived as
`SMSG_PRINT_NOTIFICATION` with the text `Global notify: CHAT-SENTINEL-1`: the prefix is the server's trinity string
100, and the command's own text is at the end, unchanged, so the independent proof is the suffix.

The server writes no chat or whisper log: its appenders are Console, Server, GM, DBErrors and CrashRing
(`worldserver.conf` 4701-4705) and `Logger.root=5`; the logs folder holds `Server.log`, `world.out`, `DBErrors.log`
and an empty `GM.log`, and there is no chat or whisper file after the run. The independent proof of chat out is
therefore the server's own echo, the not-found answer and the console notification, not a server log line.

The say echo is the server's own copy (`Player::SendChatMessageToSetInRange`), the emote echo is
`Player::TextEmote`, and the player-not-found answer is `SendChatPlayerNotfoundNotice`; none of them is written by
this client. Self-whisper was not run live (the server allows it and sends type 0x07 and 0x09, code cited above);
the whisper test used a name that is not connected so the not-found answer would prove the packet reached the
server's whisper path.

`forever party-check --invite <bot> --stay 8s` (the bot was a module character logged in with `tb login`):

```
party-check: invited <bot>
party: invite <bot>: ok
party-check: not in a group, so no leave was sent
party-check: no group, 0 member(s), leader 
party-check: command results 1, declines 0, destroys 0, invites 0, kicks 0, leader changes 0, orphan states 0, partial states 0, ready checks 0, states 0, updates 0
```

`SMSG_PARTY_COMMAND_RESULT` with `invite … ok` is the server saying it parsed the invite packet, found the named
player and stored the invitation: that is the independent proof of the outgoing half. The module bot does not answer
invites (the module has no party code), so no `SMSG_PARTY_UPDATE` followed and the client correctly reports it is not
in a group.

**The incoming half could not be proven live in round 5.** The brief's route — `.group join <source> <target>` — is
not available from this core's console: `cs_group.cpp:63` registers `join` with `allowConsole = false`, and the console
prints only the three subcommands registered with `true` (level, repair, revive). The source must already be in a
group (`GetPlayerGroupAndGUIDByName` returns `GetGroup()`, not a pending invite), and a module bot logged in with
`tb login` has no group to start one from. A second real client needs a second account: the world server keeps one
session per account (`World::AddSession`, World.cpp:388-401, deletes the old session), and only one agent account
existed then. It is proven live in the fix round below, with two accounts.

The shared test account was handled as the brief asks: `account onlinelist` was checked before every login (it
answered "No online players"), each session was under 60 s, and no wait was needed. The bot was logged out with
`tb logout` and the console answered "No online players" afterwards.

## Two clients, the fix round (2026-11-18)

Two accounts were used (the fix round's `.agent2`/`.agent3` files, read into environment variables only; the
combat worker's account was never touched). Both characters are level-1 humans at the same starting point:
<X> a warrior (class 1), <Y> a priest (class 5); names below are placeholders.

**Accept.** `<X>: party-check --invite <Y> --wait-join 12s --stay 40s`; `<Y>: party-check --accept-from <X> --stay 25s`
started at the same time, each with its own `--capture-dir`.

```
<Y>  party: invite from <X> (can accept true, roles 0, lfg slots 0)
<Y>  party-check: accepted the invite from <X>
<Y>  party: 2 member(s), leader <X>, loot group-loot
<Y>  party:  <X> connected=true subgroup=0 class=1 roles=0
<Y>  party:  <Y> connected=true subgroup=0 class=5 roles=0
<Y>  party-check: asked for 2 member state(s)
<Y>  party-check: said 19 byte(s) in party chat (language Common)
<Y>  party: member <X> 60/60 hp, 0/1000 power, level 1, online, 4 aura(s)
<Y>  party: member <Y> 52/52 hp, 160/160 power, level 1, online, 2 aura(s)
<Y>  chat: CHAT_MSG_PARTY <you>: forever party-check
<Y>  chat: CHAT_MSG_PARTY_LEADER <X>: forever party-check
<Y>  party-check: left the group
<Y>  party: leave <Y>: ok
<Y>  party: group destroyed
<X>  party-check: invited <Y>
<X>  party: invite <Y>: ok
<X>  party-check: waiting up to 12s for the group to form
<X>  party: 2 member(s), leader <X>, loot group-loot
<X>  party-check: asked for 2 member state(s)
<X>  party-check: said 19 byte(s) in party chat (language Common)
<X>  chat: CHAT_MSG_PARTY <Y>: forever party-check
<X>  party: member <X> 60/60 hp, 0/1000 power, level 1, online, 4 aura(s)
<X>  party: member <Y> 52/52 hp, 160/160 power, level 1, online, 2 aura(s)
<X>  chat: CHAT_MSG_PARTY_LEADER <you>: forever party-check
<X>  party: group destroyed
<X>  party: no group (destroyed)
<X>  party-check: not in a group, so no leave was sent
```

The leader is <X> in both rosters; each side received the other's `SMSG_PARTY_MEMBER_FULL_STATE` (health, power,
level, class column above); each party line arrived with the sender's name. <Y>'s leave destroyed the two-member
group: `Group::RemoveMember` takes the disband path when `GetMembersCount()` is not greater than 2
(Group.cpp:588-591) and `Group::Disband` sends `SMSG_GROUP_DESTROYED` (Group.cpp:753) plus the destroyed
`SMSG_PARTY_UPDATE` (Group.cpp:930-941) — both sides logged `group destroyed` and no roster update.

**Decline.** `<X>: --invite <Y> --stay 12s`; `<Y>: --decline-from <X> --stay 5s`.

```
<Y>  party: invite from <X> (can accept true, roles 0, lfg slots 0)
<Y>  party-check: declined the invite from <X>
<X>  party: invite <Y>: ok
<X>  party: <Y> declined the invite
```

**Uninvite.** `<X>: --invite <Y> --uninvite <Y> --wait-join 12s --stay 20s`; `<Y>: --accept-from <X> --stay 40s`.

```
<X>  party-check: uninvited <Y>
<X>  party: group destroyed
<Y>  party: group destroyed
<Y>  party-check: not in a group, so no leave was sent
```

The uninvite removed <Y>, but this core sends **no `SMSG_GROUP_UNINVITE`** for a two-member group: the kick of the
second member is the disband path (Group.cpp:588-591), and `GroupUninvite` is written only while the group survives
the kick (Group.cpp:610-611, inside the `> 2` branch). A live `SMSG_GROUP_UNINVITE` therefore needs a third member.
The module's bot route was tried: `tb login <bot> <master>` with the live <X> as master answered "is logging in."
and the session count fell back to 0 within a second without any group update reaching <X>, twice. The packet is
covered by `TestHandbuiltGroupUninvite` (one reason byte, `GroupUninvite::Write`) and by the fake server's uninvite
flow, and it is the one fixture that is not from a capture.

**Movement relays of another player.** `<X>: walk-check --dx 6 --pause 5s` while `<Y>` watched with
`chat-check --listen 30s --watch <X>` (the watcher's own name lookup found <X> and the world model followed it):

The samples, with the position written as an offset from the spawn (the repository rule; the raw triples were in
the run output only):

```
watch: <X> at <spawn> (orientation 3.14, 0 move relay(s) so far)
watch: <X> at <spawn> (orientation 0.00, 2 move relay(s) so far)
watch: <X> at <spawn> + 6.00 east (orientation 0.00, 4 move relay(s) so far)
watch: <X> at <spawn> + 6.00 east (orientation 3.14, 6 move relay(s) so far)
watch: <X> at <spawn> (orientation 3.14, 9 move relay(s) so far)
```

The walker's own log has the same two ends (`walk: start forward ... to the spawn + 6.00 east (6.00 yd, facing
0.00)` and back), so the relays (`SMSG_MOVE_UPDATE`, counted as move relays) moved the other player in this client's
world model: 2 relays per turn, 9 in all.

**Fixtures from these runs.** `--capture-dir` wrote every chat and party payload (outside the repository). The
party fixtures cut from them: `party_invite`, `party_update_joined`, `group_decline`, `group_destroyed` (plus the two
live `member_state` packets and `party_command_result`, which are read from the capture dirs first). `group_uninvite`
has no live packet (above) and stays hand-built.

## Two clients, fix round 2 (2026-11-18)

The same two level-1 human characters as the fix round (names below are placeholders): `<A>` a priest (class 5) and
`<B>` a warrior (class 1), both at the starting point. `<A>: party-check --invite <B> --wait-join 20s --stay 45s`;
`<B>: party-check --accept-from <A> --stay 30s`, started together, each with its own `--capture-dir`.

```
<A>  party-check: invited <B>
<A>  party: invite <B>: ok
<A>  party-check: waiting up to 20s for the group to form
<A>  party: 1 member(s), leader <A>                      <- the group the invite creates, <B> has not answered
<A>  party: 2 member(s), leader <A>, loot group-loot
<A>  party-check: asked for 2 member state(s)
<A>  party-check: said 19 byte(s) in party chat (language Common)
<A>  chat: CHAT_MSG_PARTY <B>: forever party-check
<A>  chat: CHAT_MSG_PARTY_LEADER <you>: forever party-check
<A>  party: group destroyed
<A>  party-check: not in a group, so no leave was sent
<B>  party: invite from <A> (can accept true, roles 0, lfg slots 0)
<B>  party-check: accepted the invite from <A>
<B>  party: 2 member(s), leader <A>, loot group-loot
<B>  party-check: asked for 2 member state(s)
<B>  party-check: said 19 byte(s) in party chat (language Common)
<B>  chat: CHAT_MSG_PARTY <you>: forever party-check
<B>  chat: CHAT_MSG_PARTY_LEADER <A>: forever party-check
<B>  party-check: left the group
<B>  party: leave <B>: ok
<B>  party: group destroyed
```

Both runs exited 0; each side received the other's party line, and the rosters agreed on the leader and the two
members (the member states and health lines are in the run output). The one-member roster to `<A>` is the reason the
outgoing-invite check exempts a roster that lists this character alone (fix round 2, item 3). The runs also proved
the new capture layout: each `--capture-dir` became
`<parent>/<UTC start>-<random>/{manifest.tsv,NNNN_SMSG_*.bin}`, and the fixtures below were cut from those run
directories.

## Open questions

* The invite and invite-response client layouts have no captured bytes on this server (the capture's bot group was
  formed without a `CMSG_PARTY_INVITE` from the client). The writers follow the server's reader and the shape of the
  captured member-stats packet; a capture of a player typing `/invite` would settle it.
* The captured bot group reached 6 members with `PartyType` 1 (normal) although `MAX_GROUP_SIZE` is 5: the bot
  module's group is not a core party. This round reports such a roster as raid-sized and does not model it.
* `SMSG_CHAT` SlashCmd is a `uint8` on the wire; `CHAT_MSG_ADDON` (-1) arrives as 0xFF, which the table names but no
  captured packet exercises.

## Merge round (2026-11-18, `Merge main into forever-chat`)

Main brought the combat round (round 4) into this branch; three files conflicted and both sides were kept:
`cmd/forever-agent/forever_login.go` (the six commands' usage lines and dispatch cases), `internal/forever/wire/
opcodes.go` (both opcode tables) and `internal/forever/world/session.go` (both import and field groups, with the chat
and party cases ahead of main's `default: s.handleCombat(p)`; no opcode is in two groups).

After the merge the tree had two capture facilities, two timed-wait loops and two case-insensitive name
comparisons. One of each is left:

* `capture_run.go` is the session's one capture (combat, chat and party; `capturePacket`), and
  `internal/forever/capturedir` is its one reader — both fixture generators use it.
* `session.drain` is the one timed wait; `WaitUntil`, `collect` and `settle` are its three named forms.
* `strings.EqualFold` is the one name comparison; `equalFoldASCII` is gone.
* `safeText` is gone: server text in the world package goes through `chat.Sanitise`.

The link's death ends every chat and party wait too: `TestChatAndPartyChecksNoticeADeadSocket` drops the instance
socket while chat-check listens and while party-check waits for an invite, and both end at once with `ErrLinkLost`.
The shared-state table (with the chat log, the party model and the capture run) and the gates are in
[`SESSION-STATE.md`](SESSION-STATE.md).
