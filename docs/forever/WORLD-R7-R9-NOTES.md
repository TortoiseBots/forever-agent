# Forever mail and battleground queue, rounds R7 and R9 notes

Scope: R7, the mail from the player's side (list, take item and money, delete; send text and money last) and R9,
the battleground queue (battlemaster hello and list, join, status, port accept, leave).
Code: `internal/forever/mail` and `internal/forever/bg` (the wire layers), `internal/forever/world` (`mail.go` and
`bg.go` the session glue, `mail_act.go` and `bg_act.go` the act steps), `internal/forever/wire` (the opcodes of
both rounds), `cmd/forever-agent` (`forever act --mail-to`, `--mail-money-cap` and `--pay-cod`).

Everything below was checked in ForeverCore at d02645dd21 (`git show d02645dd21:<path>`). The capture checks are
opt-in and print counts only; no capture byte, GUID, name or file name is in the repository. Every fixture is
hand-made from the core's writers.

The tables' `Wire` column is the 70707 realm's wire number (what goes on the socket); `internal/forever/wire`
holds the core numbers the server names (e.g. `CMSG_MAIL_GET_LIST` is core 0x3E00D8, wire 0x3F00DB), and
`opcodes_test.go` pins the translation.

Status: both rounds are implemented, tested against the fake world, and committed (R7 first, R9 second).
**No live run yet**: the live checks are at the end, and the gate lines name the VM suite's RESULT line.

## R7: the mail

### The client packets

Each writer follows the server's reader, which is what defines the bytes a real client may send.

| Packet | Wire | Body | Core |
|---|---|---|---|
| `CMSG_MAIL_GET_LIST` | 0x3F00DB | the mailbox's packed GUID | `MailGetList::Read`, MailPackets.cpp:166-169 |
| `CMSG_MAIL_TAKE_ITEM` | 0x3F00DD | mailbox GUID, mail id u64, attach id u64 | `MailTakeItem::Read`, MailPackets.cpp:246-251 |
| `CMSG_MAIL_TAKE_MONEY` | 0x3F00DC | mailbox GUID, mail id u64, money u64 | `MailTakeMoney::Read`, MailPackets.cpp:253-258 |
| `CMSG_MAIL_MARK_AS_READ` | 0x3F00DF | mailbox GUID, mail id u64 | `MailMarkAsRead::Read`, MailPackets.cpp:234-238 |
| `CMSG_MAIL_DELETE` | 0x3E00E9 | mail id u64, reason i32 (always 0) | `MailDelete::Read`, MailPackets.cpp:240-244 |
| `CMSG_SEND_MAIL` | 0x440026 | mailbox GUID, stationery i32, money/cod, three sized strings (target 9, subject 9, body 11) and the attachment count (5) in one bit block, then the bytes and the attachments | `SendMail::Read`, MailPackets.cpp:196-214 |

The client table also names the packets of the round's protocol that no step sends (`CMSG_MAIL_MARK_AS_READ`,
`CMSG_BATTLEFIELD_LIST`, `CMSG_REQUEST_BATTLEFIELD_STATUS`); their opcodes stay in `wire` for translation.

The facts that decide the flow:

* **A mailbox is an NPC or a game object the character stands next to.** `CanOpenMailBox` answers the list only
  for one the server's reach check passes (MailHandler.cpp), and the character's own GUID is the cheat path (it
  needs the GM mailbox permission). The `mail` step therefore takes the current NPC with the mailbox flag
  (`UNIT_NPC_FLAG_MAILBOX`, UnitDefines.h:346) within reach, and refuses anything else before any packet.
* **The attach id is the item GUID the list named.** A take names the attachment the list carried for that mail
  (the item's own GUID), and the handler refuses an unknown one (MailHandler.cpp HandleMailTakeItem); the step
  reads the ids from the last list and never invents them.
* **Money takes the whole amount.** The handler refuses a take whose money differs from the mail's
  (HandleMailTakeMoney), so the step sends the whole amount the list named. Only `CMSG_MAIL_MARK_AS_READ` sets
  `MAIL_CHECK_MASK_READ` (`HandleMailMarkAsRead`, MailHandler.cpp:378-395); `HandleGetMailList` does not, and this
  round sends no mark-read.
* **A take is confirmed, not assumed.** After `MAIL_OK` the step waits for the attachment's item to appear in the
  inventory and for the coinage to read back as the take left it; a COD mail pays its copper to the sender on the
  item take (`MailHandler.cpp:506-545`), so `mail-take` refuses one unless `forever act --pay-cod` is passed.
* **A delete only takes an empty mail.** The server deletes a mail with its items and money and only refuses a COD
  mail (`HandleMailDelete`, MailHandler.cpp:396-413), so `mail-delete` re-reads the list first and refuses a mail
  that still holds items, money or a COD.
* **A send needs the caller's allow-list.** `send-mail` accepts only a name on `--mail-to NAME=GUID`, the guid must
  answer that exact name to a `CMSG_QUERY_PLAYER_NAMES` query, and the money may not pass `--mail-money-cap`. The
  send packet names a name, so this is the only way the step knows the guid it pays.

### The server's answers

| Packet | Wire | Body | Core |
|---|---|---|---|
| `SMSG_MAIL_LIST_RESULT` | 0x470201 | count u32, total i32, then per mail: mail id u64, sender type u32, COD u64, stationery i32, money u64, flags i32, days-left float, template i32, attachment count u32, the sender form the type selects (player GUID or int32), the attachments, then the 8-bit subject and 13-bit body in one bit block before the bytes | `MailListResult::Write`, `MailListEntry`, MailPackets.cpp:131-180 |
| `SMSG_MAIL_COMMAND_RESULT` | 0x4700E5 | mail id u64, command i32, error i32, bag result i32, attach id u64, quantity i32 | `MailCommandResult::Write`, MailPackets.cpp:222-232 |
| `SMSG_NOTIFY_RECEIVED_MAIL` | 0x4700E6 | delay float | `NotifyReceivedMail::Write`, MailPackets.cpp:303-308 |

The failing case is `TestMailListAttachmentCount`: two mails, the first with two attachments; the second mail's id
must parse. A reader that takes the sender form for the wrong type, or that reads the attachment frame or the
string block wrong, shifts every mail after the first.

## R9: the battleground queue

### The client packets

| Packet | Wire | Body | Core |
|---|---|---|---|
| `CMSG_BATTLEMASTER_HELLO` | 0x3E0191 | the battlemaster's packed GUID | `NPC::Hello::Read`, NPCPackets.cpp:94-97; `HandleBattlemasterHelloOpcode`, BattleGroundHandler.cpp |
| `CMSG_BATTLEMASTER_JOIN` | 0x3F00C0 | count u32 (one), roles u8, two blacklist i32, the packed queue id u64 | `BattlemasterJoin::Read`, BattlegroundPackets.cpp:159-168 |
| `CMSG_BATTLEFIELD_PORT` | 0x3F00CA | the ride ticket and the accept bit flushed | `BattlefieldPort::Read`, BattlegroundPackets.cpp:250-254 |
| `CMSG_BATTLEFIELD_LEAVE` | 0x3E0020 | empty | `BattlefieldLeave::Read`, BattlegroundPackets.cpp:270-276 |
| `CMSG_BATTLEFIELD_LIST` | 0x3E002B | the list id i32 | `BattlefieldListRequest::Read`, BattlegroundPackets.cpp:256-259 |
| `CMSG_REQUEST_BATTLEFIELD_STATUS` | 0x440008 | empty | `RequestBattlefieldStatus::Read` |

The facts that decide the flow:

* **The list is the probe.** The hello answers `SMSG_BATTLEFIELD_LIST` (BattlegroundMgr.cpp
  `SendBattlegroundList`); the core always sends an empty battlefield list, and the list id is the supported
  battlemaster type. A list id that is not positive stops the round with its report instead of queueing (the
  spec's own rule); one over 0xFFFF is refused as malformed because the packed queue id cannot hold it.
* **The join names the list's packed queue id.** The queue id packs the list id with the fixed high marker
  (`BattlegroundQueueTypeId::GetPacked`, SharedDefines.h): `PackQueueID` builds it, and the step sends roles and
  blacklists as zero.
* **The port echoes the invite's ticket.** The handler points the port at the queue the player really is in
  (BattleGroundHandler.cpp `HandleBattleFieldPortOpcode`), so the accept sends the need-confirmation's own
  ticket with the accept bit set. A queue is left the same way, with the queued (or invited) ticket and the accept
  bit clear; `CMSG_BATTLEFIELD_LEAVE` only calls `Player::LeaveBattleground`, which returns with no battleground.
  Inside a match that packet leaves (and casts Deserter while the match is running, Player.cpp:25691-25706), so
  `bg-leave` sends it only when the step says `match=yes`.
* **An expired invite is not replayed.** `bg-accept` counts the invite's own `Timeout` (milliseconds) from its
  arrival and refuses one that passed, because the server already removed the queue entry.

### The server's answers

| Packet | Wire | Body | Core |
|---|---|---|---|
| `SMSG_BATTLEFIELD_LIST` | 0x4D0005 | master GUID, list id i32, min/max u8, count u32, the battlefields, two bits flushed | `BattlefieldList::Write`, BattlegroundPackets.cpp:261-275 |
| `SMSG_BATTLEFIELD_STATUS_QUEUED` | 0x4D0002 | the header, average wait u32, wait u32, spec i32, three bits flushed | `BattlefieldStatusQueued::Write`, BattlegroundPackets.cpp:226-238 |
| `SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION` | 0x4D0000 | the header, map u32, timeout u32, role u8 | `BattlefieldStatusNeedConfirmation::Write`, BattlegroundPackets.cpp:202-210 |
| `SMSG_BATTLEFIELD_STATUS_ACTIVE` | 0x4D0001 | the header, map/shutdown/start u32, arena faction i8, two bits flushed | `BattlefieldStatusActive::Write`, BattlegroundPackets.cpp:212-224 |
| `SMSG_BATTLEFIELD_STATUS_NONE` | 0x4D0003 | the ticket | `BattlefieldStatusNone::Write`, BattlegroundPackets.cpp:195-200 |
| `SMSG_BATTLEFIELD_STATUS_FAILED` | 0x4D0004 | the ticket, the queue id u64, the reason u32, the client id | `BattlefieldStatusFailed::Write`, BattlegroundPackets.cpp:240-248 |

The header is the ticket (`LFGPacketsCommon.cpp:23-44`: GUID, id, kind, time int64, one bit flushed), the queue
count, the range/team/instance bytes, the queue ids, then two bits flushed (BattlegroundPackets.cpp:177-193). The
failing case is `TestStatusHeaderQueuesBeforeBits`: the queue id follows the instance bytes and precedes the two
flushed bits — a parser that reads the bits where the count goes returns no queues and shifts every status after
the header. Both statuses write the header first: need-confirmation then writes map, timeout and role, and queued
writes the average wait, the wait and the spec (BattlegroundPackets.cpp:202-210, :226-238); a parser that reads
either field before the header shifts the whole status.

## Live checks

### R7, the mail

1. **List.** `npc entry=<a mailbox>`; `mail`: the step line names the mail count, one line per mail (copper,
   attachments, flags, subject) and one per attachment (entry, count).
2. **Take money.** A mail with money, `mail-money id=<id>`: the line names the copper taken and the wallet's
   before and after; the step fails if the coinage does not read back as the amount the take added.
3. **Take item.** A mail with an item, `mail-take id=<id>`: one `mail` line per attachment; the step fails
   unless the item appears in the inventory; a second take shows no attachments.
4. **Refusals.** A take of an unknown mail fails before any packet ("no mail in the last list"); a COD mail's
   take is refused before any packet unless the run passed `--pay-cod`, and then the wallet reads back lower by
   the COD.
5. **Delete.** `mail-delete id=<id>` re-reads the list: it refuses a mail that still holds an item, money or a
   COD; once the takes are done the delete goes out and a second `mail` no longer lists it.
6. **Send.** `send-mail to=<name> subject="..." body="..." money=<n>` with `--mail-to <name>=<guid>` covering the
   recipient and `--mail-money-cap` covering the copper: the name query must answer that exact name for the guid
   before the send; a name off the allow-list, a guid answering another name, or money over the cap is refused
   before any packet.
7. **Relog.** `mail` after a relog matches the server's `mail` table (a taken mail shows no attachments, a
   deleted one is gone).

### R9, the battleground queue

1. **Probe.** `npc entry=<a battlemaster>`; `bg-queue`: when the list names no supported id the step reports it
   and sends no join; otherwise the line names the list and the wait.
2. **Queue and leave.** `bg-queue`, then `bg-leave`: the step leaves through `CMSG_BATTLEFIELD_PORT` with the
   accept bit clear, the none status is the proof, and a second status shows none.
3. **Invite (only when one arrives).** `bg-queue` reporting the ready invite, `bg-accept`: the line names the
   map and the active status proves the port. An invite past its own timeout is refused before any packet.
4. **Inside a match.** `bg-leave` without `match=yes` refuses before any packet and names the Deserter risk;
   `bg-leave match=yes` sends `CMSG_BATTLEFIELD_LEAVE`, and the line repeats the warning.
5. **Refusals.** A non-battlemaster `bg-queue` fails before any packet; a failed queue names the server's reason
   (`range index` for a level-range mismatch, ...).

## Gate lines

All on the clean committed tree (`git status` empty before each run).

* `make fmt-check`: no output, `rc=0`.
* `make cross`: the Windows and Darwin cross builds and the Windows vet of `./cmd/... ./internal/forever/...`
  passed, `rc=0`.
* `staticcheck ./...`: `rc=0`, no findings.
* The focused race run: `go test -race -count=1 -timeout 20m ./internal/forever/mail/ ./internal/forever/bg/
  ./internal/forever/wire/ ./internal/forever/npc/` and `go test -race -count=1 -timeout 20m -run
  'TestMail|TestBG|TestAct|TestParseActStep|TestVendor|TestQuest' ./internal/forever/world/`: `ok` for all.
* The VM suite's canonical line for this round is in the round's report (`RESULT: PASS kind=ta sha=<sha>
  dirty=0`); the notes keep no host, worktree or runner text.
* The capture checks are opt-in (`FOREVER_CAPTURE_PKT`/`FOREVER_CAPTURE_DIR`) and were not run here (no capture
  files in this repository).
