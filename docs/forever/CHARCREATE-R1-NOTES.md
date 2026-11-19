# Forever character screen, round 6: create and delete a character

Scope: the realm socket after the world auth and before any character is logged in. A real client lists the
account's characters there, creates and deletes them and checks a name; this round adds the three exchanges. Code:
`internal/forever/wire/charcreate.go` (core opcodes), `internal/forever/world/charcreate_packets.go` (codecs),
`charcreate_codes.go` (result-code names and refusal errors), `charcreate.go` (the glue session and
`CreateCharacter`/`DeleteCharacter`/`CheckCharacterName`), `cmd/forever-agent/forever_charcreate.go` (`forever
char-create`, `forever char-delete`). Status: proven live on the test server, 2026-11-19.

The server's code at 6517af05 is authoritative below; every layout names its file:line.

## The character screen on the socket

The world socket is the only one at the character screen (the instance socket exists only after `CMSG_PLAYER_LOGIN`
and `SMSG_CONNECT_TO`). `worldconn.Authenticate` already brings the realm socket to `SMSG_AUTH_RESPONSE`; from there
`CreateCharacter`/`DeleteCharacter`/`CheckCharacterName` run a small read loop of their own (`charScreen` in
`charcreate.go`): every packet is counted by nothing and skipped except the answer it waits for, `SMSG_TIME_SYNC_REQUEST`
is answered with `CMSG_TIME_SYNC_RESPONSE`, and the wait is bounded by 30 s and the run by `Options.Deadline`
(default 90 s). The world session (`session.go`) owns a logged-in character and its two sockets, so it is not reused
and the character screen needs no registration in its dispatch list.

A local capture of a real session holds one `CMSG_CREATE_CHARACTER` and one `SMSG_CREATE_CHAR` on connection 0. No
capture deletes a character or sends `CMSG_CHECK_CHARACTER_NAME_AVAILABILITY`, so those two are hand-built from the
server's reader and the tests decode them with a transcription of it.

## Opcodes (core numbers, hand-computed wire numbers)

| core | name | source | wire at 70707/70707 |
|---|---|---|---|
| 0x430070 | CMSG_CREATE_CHARACTER | Opcodes.h:313 | 0x440070 |
| 0x430071 | CMSG_CHECK_CHARACTER_NAME_AVAILABILITY | Opcodes.h:237 | 0x440071 |
| 0x4300CB | CMSG_CHAR_DELETE | Opcodes.h:192 | 0x4400CB |
| 0x4501AC | SMSG_CREATE_CHAR | Opcodes.h:1422 | 0x4701AC |
| 0x4501AD | SMSG_DELETE_CHAR | Opcodes.h:1437 | 0x4701AD |
| 0x45001B | SMSG_CHECK_CHARACTER_NAME_AVAILABILITY_RESULT | Opcodes.h:1357 | 0x47001B |

Client computation (ClassicOpcodes.cpp, realm 70707 ≥ 70291 → group shift 1, main-group insert at 0x13B): 0x44XX →
0x44XX for indexes below 0x13B, then group 0x45 → 0x46. 0x1AC is inside the 0x45 group's `0x17E..0x1C0` removal
(`index--` → 0x1AB), the main insert then adds one (0x1AC), and the group shift gives 0x4701AC; 0x1AD has neither
adjustment and ends at 0x4701AD. The tests pin all six literals in both directions
(`TestCharacterScreenOpcodesHandComputed`).

`SMSG_CREATE_CHAR` and `SMSG_DELETE_CHAR` are `CONNECTION_TYPE_REALM` (Opcodes.cpp:1498, 1513), which is why the
create answer arrives on the realm socket in the capture and here.

## CMSG_CREATE_CHARACTER (CreateCharacter::Read, CharacterPackets.cpp:492-521)

Bit part, in the server's read order, most significant bit first:

1. `SizedString::BitsSize<6>(Name)` — 6 bits, the byte length.
2. `OptionalInit(TemplateSet)` — 1 bit; the client sends 0.
3. `IsTrialBoost`, `UseNPE`, `HardcoreSelfFound` — 1 bit each, all 0.
4. `ReadBits(2)` — two unknown bits, 0.
5. `SizedString::BitsSize<6>(Surname)` — 6 bits.
6. `ResetBitPos()` — the server discards the rest of the current byte. 18 bits are used, so a three-byte bit block
   is written and its last six bits are padding (not "another field").

Byte part: `Race u8`, `Class u8`, `Sex u8`, `Size<uint32>(Customizations)` (four bytes), a skipped `int32` (the real
client sends -1), `TimerunningSeasonID u32`, the name bytes (no NUL), the surname bytes (no NUL), the template set as
`int32` when its bit was set, then per customisation `ChrCustomizationOptionID u32` and `ChrCustomizationChoiceID u32`
(`CharacterPackets.cpp:32-38`).

The real packet, decoded from the capture (its character name and surname were rewritten to `XXXXXX`/`XXXX` in the
fixture): bit block `18 01 00` = name length 6, the six flag bits 0, surname length 4; `01 08 01` = human mage,
female; ten choices, `-1`, season 0, then the two names and ten (option, choice) pairs. The fixture test decodes it
with the server-reader transcription and rebuilds the exact bytes
(`TestCreateCharacterPacketMatchesTheCapture`).

## The default customisation finding

**Zero choices are accepted by the server, but no real client has been seen sending them.** `Player::Create` calls
`WorldSession::ValidateAppearance` (`Player.cpp:439`, `CharacterHandler.cpp:769-832`), which loops over the choices
the packet carries; with none it only checks that `GetCustomiztionOptions(race, gender)` returned a set
(`DB2Stores.cpp:2229-2232`, filled from ChrModel/ChrCustomizationOption/ChrCustomizationChoice at
`DB2Stores.cpp:1185-1260`). Those DB2 tables are the server's data; the client never sends them and does not need
them for the default. An invalid or duplicated choice is refused (`Player::Create` returns false →
`SendCharCreate(CHAR_CREATE_ERROR)`, `CharacterHandler.cpp:1213-1216`); a race/gender with no option set is refused
the same way. The display model does not come from the choices: `InitDisplayIds` uses ChrModel.db2
(`Player.cpp:24797-24806`), so a zero-choice character is a normal character of its race and gender.

Proven: the server accepts the zero-choice create and the zero-choice character at login — `ValidateAppearance`
returns true for an empty list at both (`CharacterHandler.cpp:450` at login, `Player.cpp:439` at create), and live,
creating `<new>` (human, warrior, male) with zero choices answered `CHAR_CREATE_SUCCESS`, appeared in the next list
and entered the world (`world-check`: `own create block: ... health 60/60, rage 100.0/100.0`). Not proven: how a real
game client draws such a character on the character screen; no real client has been seen creating one. The appearance
is therefore explicit: `CreateSpec.Choices` (the `--choice OPTION:CHOICE` flag) sends the pairs as given, and
`CreateSpec.NoAppearance` (the `--no-appearance` flag) says "test character, no choices"; neither is an error, so the
command has no silent default.

The real client sends ten choices (the capture above). This client has no table to derive them for a race, and
inventing ids would be refused.

## SMSG_CREATE_CHAR (CreateChar::Write, CharacterPackets.cpp:523-529)

`uint32 Code` then the packed GUID (two-byte mask, then the non-zero bytes; `ObjectGuid.cpp:1087-1111`). A success
carries the new character's GUID (the capture: code 24 and a 7-byte packed GUID); a refusal carries code only and the
empty packed GUID (mask 0). The parser ignores bytes after the GUID, like the round-2 rule for appends.

## CMSG_CHAR_DELETE / SMSG_DELETE_CHAR (CharacterPackets.cpp:531-542)

The client sends one packed GUID. The server's handler (`CharacterHandler.cpp:1233-1327`) has no confirmation step and
no level check of its own: it refuses only a still-connected player (`CHAR_DELETE_FAILED`; the special case is the
account's own bot, which is kicked first and deleted after it leaves), a guild leader
(`CHAR_DELETE_FAILED_GUILD_LEADER`), an arena captain (`CHAR_DELETE_FAILED_ARENA_CAPTAIN`), an unknown GUID or one of
another account (`CHAR_DELETE_FAILED`), and otherwise calls `DeleteCharacter` → `Player::DeleteFromDB`. Because the
wire has no confirmation, `DeleteSpec.Confirm` must repeat the resolved character's full name or nothing is sent; the
command's `--yes` is that field.

The name is resolved against the account's list the way the server normalises it: `ObjectMgr::normalizePlayerName`
(`ObjectMgr.cpp:157-181`) trims the blanks, keeps the first word, lower-cases every letter and upper-cases the first,
so `AGTONE` and `Agtone` are the same name. The surname is not part of that function (it discards everything after the
first blank), so it compares case-insensitively on its own; a name can carry it after the first blank
(`--name "First Last"`) or in `--surname`. Every match is collected: none or several is an error that lists the
candidates, and an entry without a GUID is skipped. `--guid` is the exact alternative.

`Player::DeleteFromDB` (`Player.cpp:4117-4200+`) does have a level rule: `CharDelete.Method` selects REMOVE (0) or
UNLINK (1), and a character below `CharDelete.MinLevel`/`DeathKnight.MinLevel`/`DemonHunter.MinLevel` is hard-removed
whatever the method. On the test server `CharDelete.Method = 0`, `CharDelete.MinLevel = 0` (and the two class
variants 0), so a level-1 delete is a hard delete: the row disappears (proved by the query below). With UNLINK the row
would stay with `deleteDate` set for `CharDelete.KeepDays`; the answer code is the same.

## CMSG_CHECK_CHARACTER_NAME_AVAILABILITY (CharacterPackets.cpp:469-490)

Present at the pin (`CharacterHandler.cpp:2032-2067`, handler registered `Opcodes.cpp:346`). Layout: `uint32
SequenceIndex`, then 6-bit name length, **three unknown bits**, 6-bit surname length, `ResetBitPos`, then name and
surname bytes (no NUL). The answer is `uint32 SequenceIndex, uint32 Result`: `RESPONSE_SUCCESS` (0) when free,
`CHAR_CREATE_NAME_IN_USE` (27) when taken, or the `CHAR_NAME_*` code `ObjectMgr::CheckPlayerName` returned
(`ObjectMgr.cpp:9172-9193`: empty, too short/long, mixed languages, profanity, reserved, apostrophes, three
consecutive letters, spaces; `MIN_PLAYER_NAME`/`STRICT_PLAYER_NAMES` come from the config).

## Result codes (SharedDefines.h:6804-6897)

Every code the three answers can carry is named in `charcreate_codes.go`, and a refusal is a typed error:

* `CHAR_CREATE_IN_PROGRESS` 23 … `CHAR_CREATE_NEED_ENTITLEMENT` 65 (the create handler's family;
  CharacterHandler.cpp:833-1231). The create answer can also carry a `CHAR_NAME_*` code, because the handler sends
  `CheckPlayerName`'s answer directly (`CharacterHandler.cpp:972-979`), and the create-refusal error names those too.
* `CHAR_DELETE_IN_PROGRESS` 66 … `CHAR_DELETE_FAILED_HOUSE_OWNER` 78 (CharacterHandler.cpp:1233-1327).
* The name check answers `RESPONSE_SUCCESS` 0, `CHAR_CREATE_NAME_IN_USE` 27 or `CHAR_NAME_SUCCESS` 96 …
  `CHAR_NAME_SPACES_DISALLOWED` 113.
* A code from neither family prints as `CHAR_CREATE code N` and is never guessed.

`CreateRefusedError`, `DeleteRefusedError` and `NameRefusedError` carry the number and print the name; the live
`char-create`/`char-delete` commands print `-> CHAR_CREATE_SUCCESS` / `-> CHAR_DELETE_SUCCESS` on stdout and the
named refusal on stderr (exit 5), a lost link with its side and last packet (exit 6).

## Checklist per parser (the round-2 three-mistakes list)

| Parser | Server writer/reader | Checked |
|---|---|---|
| `BuildCreateCharacter` bit part | `CharacterPackets.cpp:498-505`: six-bit lengths, `OptionalInit`, three 1-bit flags, `ReadBits(2)`, `ResetBitPos` | 18 bits + padding; MSB-first; `FlushBits` before the byte part; the name is not NUL-terminated (`SizedString::Data`, `PacketOperators.h:296-310`) |
| `BuildCreateCharacter` byte part | `:506-520`: Race/Class/Sex, `Size<uint32>`, skipped int32, season, name, surname, template set, choices | count is four bytes; -1 skip; name/surname before the template set; 8 bytes per choice; `Array<...,250>` bound |
| `ParseCreateChar` | `:523-529`: `uint32`, packed GUID | packed mask/popcount length; a refusal is the empty GUID; trailing bytes tolerated |
| `BuildCharDelete` / `ParseDeleteChar` | `:531-540` | one packed GUID, no extra field; four-byte code |
| `BuildCheckCharacterNameAvailability` | `:469-482` | three unknown bits between the lengths; `ResetBitPos`; no NUL |
| `ParseCheckCharacterNameAvailabilityResult` | `:484-490` | sequence and result, four bytes each |

Bounds and fuzz: every parser errors on a short payload (never panics or slices), the builders refuse a name or
surname over 63 bytes (six-bit length) and more than 250 choices, and `FuzzParseCreateChar`, `FuzzParseDeleteChar`,
`FuzzParseCheckCharacterNameAvailabilityResult` run in CI (`make fuzz`). The waits are bounded twice: 30 s per answer
and `Options.Deadline` per run, except the delete answer, which gets 60 s (`charDeleteWait`) because the server may
hold a delete for its own 30 s pending-delete bound (`CharacterHandler.cpp:1253`) while the character's bot leaves the
world; a silent peer is a test with a short `Deadline` (`TestCharacterScreenSilentPeer`) and each of the five
link-death points is one (`TestCharacterScreenLinkDeath`). Because a request that is still unanswered when the run's
deadline ends cannot be told from one the server will still carry out, `CreateCharacter` and `DeleteCharacter` check
their remaining time before sending and refuse with a `*DeadlineTooShortError` (nothing sent, and the error names the
least `Deadline`) unless the bound plus a 10 s margin is left: 40 s for a delete (the server's 30 s pending-delete
bound) and 40 s for a create (its 30 s answer bound). A timeout after the packet went out is a `*SentNoAnswerError`
whose `Outcome` says the character may or may not have been created (or deleted) and to list again; the commands print
exactly that line and exit 32. A read timeout is classified as no answer, never as a lost link: the socket's read
deadline and the loop's own timer are armed for the same instant, so either can fire first. A link that dies after the
send is a `*SendLinkLostError` with its own `Outcome` ("sent, then the link was lost: the character may or may not
have been created/deleted: list again"), which still wraps `ErrLinkLost`; the commands print that line and exit 32
too. The wait also ends at once on the two packets that mean the session is over:
`SMSG_CHARACTER_LOGIN_FAILED` (`AbortLogin`, `CharacterHandler.cpp:1400-1409`) and `SMSG_LOGOUT_COMPLETE`
(`WorldSession::LogoutPlayer`, `WorldSession.cpp:766`), named in the error
(`TestCharacterScreenEndsOnSessionPackets`).

## Tests in CI (fake world server)

The fake server (`charcreate_fake_test.go`) reuses round 2's handshake (a digest it really verifies, encryption, the
compressed auth response) and then serves the character screen with hand-computed wire literals; the create packet
is decoded by the transcription of `CreateCharacter::Read`, never by the builder. It keeps a character list and
regenerates `SMSG_ENUM_CHARACTERS_RESULT` from it (`buildEnumCharactersPayload`, the server's writer at
`CharacterPackets.cpp:217-340` and the regionwide trailer `:373-384`), so the second list really gains and loses the
character. Covered: create accepted and in the next list, the choices reaching the server as given, a create with
neither appearance form refused before the network; refusals name taken, name invalid, account limit, realm
limit, race disabled; delete accepted by name and by GUID, refusals failed and guild leader, the unconfirmed delete
sending nothing, the ambiguous first name listing its candidates; name check free, taken
and invalid; a time sync before the list answered; link death at the list, the create answer, the list after a create,
the delete answer and the name answer; a silent peer and the two session-end packets; a failed list after a SUCCESS
answered as a report note, not an error; a delete and a create refused before their send when authentication and the
list spend the run's deadline, a short explicit `Deadline` refused after the list, and a timeout after the send
reported as the named uncertain outcome (`TestDeleteCharacterRefusedWhenTheListSpendsTheDeadline`,
`TestCharacterScreenRefusedByAShortDeadline`, `TestCharacterScreenTimeoutAfterTheSend`); a link that dies after the
create or delete send reported as its own uncertain outcome (`TestCharacterScreenDisconnectAfterTheSend`); argument
and flag checks in the command.

## What is proven live (2026-11-19, the test server)

Create, human warrior, male, zero choices (the run predates the explicit `--no-appearance` flag; its bytes are the
flag's form). The server's `<server>` and account are redacted here:

```
char screen: CMSG_ENUM_CHARACTERS sent
char screen: 3 character(s), max level 2, 0 trailing bytes
  character <existing>: level 2, class 1, race 1, map 0
  ...
char create: CMSG_CREATE_CHARACTER sent (<new>, race 1, class 1, sex 0, 0 customization(s))
char create: SMSG_CREATE_CHAR CHAR_CREATE_SUCCESS
char screen: CMSG_ENUM_CHARACTERS sent
char screen: 4 character(s), max level 2, 0 trailing bytes
  character <new>: level 1, class 1, race 1, map 0
char-create: <new> created (race 1, class 1, sex 0), guid <guid>, 4 character(s) after
```

Entering the world with it once (`forever world-check --stay 8s`): `world character list: 4 character(s)`,
`CMSG_PLAYER_LOGIN sent`, the instance socket, `SMSG_LOGIN_VERIFY_WORLD` on map 0, `own create block: ... health
60/60, rage 100.0/100.0`, 2 time syncs, 1 ping/pong, `SMSG_LOGOUT_COMPLETE`; `account onlinelist` answered "No online
players" afterwards.

Delete (a second character of the same run, for a clean before/after pair):

```
char delete: CMSG_CHAR_DELETE sent for <new> (<guid>)
char delete: SMSG_DELETE_CHAR CHAR_DELETE_SUCCESS
char screen: 3 character(s), max level 2, 0 trailing bytes
char-delete: <new> deleted, 3 character(s) after
```

Database, the same read-only query before and after that delete (the account's three other characters unchanged; the
new row is gone; `CharDelete.Method = 0` makes it a hard delete):

```
before: 3 <existing> 1 1 0 2 NULL | 4 <existing> 1 1 0 1 NULL | 5 <existing> 1 1 0 1 NULL | 9 <new> 1 1 1 1 NULL
after : 3 <existing> 1 1 0 2 NULL | 4 <existing> 1 1 0 1 NULL | 5 <existing> 1 1 0 1 NULL
```

(The first live cycle created and deleted a character before this pair — the same commands, same result codes; the
pair above is the one bracketed by the two queries. Each session was under 3 s except the world entry, 29.5 s.)

## Open questions

1. **Appearance with zero choices.** The model comes from ChrModel.db2 and the live character entered the world with
   the right health/rage, but the model was not inspected visually and no real client has been seen creating a
   zero-choice character. The command insists on `--choice OPTION:CHOICE` or `--no-appearance`; a client that wants
   the real character-select appearance needs `ChrCustomizationOption`/`ChrCustomizationChoice` ids for its race and
   gender (the agent has no copy of the DB2 tables, so the caller supplies them).
2. **Surname.** Classic 1.60 stores one (`characters.surname`, up to 48 characters) and the create packet carries it.
   `--surname` sets it on create and on delete, and the create packet is pinned by the capture's `XXXX`; whether a
   surname the server accepts differs from the create packet's 63-byte six-bit bound was not exercised.
3. **Delete confirmation.** The real client asks before sending `CMSG_CHAR_DELETE`; the wire has no confirmation, so
   `char-delete` resolves the character first and deletes only when `--yes` repeats its full name (the library field
   `DeleteSpec.Confirm`). A level or mail rule exists only in `DeleteFromDB`'s method selection
   (`CharDelete.MinLevel`, 0 on this server), not as a refusal.
4. **The name check is not used by the commands.** They take the create answer, which carries the same
   `CHAR_NAME_*` codes; `CheckCharacterName` is there for callers that want to pre-check a name.
5. **Account limit values.** The refusals are covered with the codes the handler sends; the limit numbers
   (`CONFIG_CHARACTERS_PER_ACCOUNT`, `CONFIG_CHARACTERS_PER_REALM`) were not exercised live (filling them would
   create real characters for the shared account).
