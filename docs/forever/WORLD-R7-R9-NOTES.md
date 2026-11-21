# Forever mail and battleground queue, rounds R7 and R9 notes

Scope: R7, the mail from the player's side (list, take item and money, mark read, delete; send text and money
last) and R9, the battleground queue (battlemaster hello and list, join, status, port accept, leave).
Code: `internal/forever/mail` and `internal/forever/bg` (the wire layers), `internal/forever/world` (`mail.go` and
`bg.go` the session glue, `mail_act.go` and `bg_act.go` the act steps), `internal/forever/wire` (the opcodes of
both rounds), `cmd/forever-agent` (no new flags: the verbs run through `forever act`).

Everything below was checked in ForeverCore at d02645dd21 (`git show d02645dd21:<path>`). Private captures were
read once to check the layouts; the capture checks print counts only, and no capture byte, GUID, name or file name
is in the repository. Fixtures are hand-made from the core's writers.

Status: both rounds are implemented, tested against the fake world, and committed (R7 first, R9 second).
**No live run yet**: the live checks for the lead's queue are at the end, and the notes quote the RESULT line of
the VM suite.

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

Three facts decide the flow:

* **A mailbox is an NPC or a game object the character stands next to.** `CanOpenMailBox` answers the list only
  for one the server's reach check passes (MailHandler.cpp), and the character's own GUID is the cheat path (it
  needs the GM mailbox permission). The `mail` step therefore takes the current NPC with the mailbox flag
  (`UNIT_NPC_FLAG_MAILBOX`, UnitDefines.h:346) within reach, and refuses anything else before any packet.
* **The attach id is the item GUID the list named.** A take names the attachment the list carried for that mail
  (the item's own GUID), and the handler refuses an unknown one (MailHandler.cpp HandleMailTakeItem); the step
  reads the ids from the last list and never invents them.
* **Money takes the whole amount.** The handler refuses a take whose money differs from the mail's
  (HandleMailTakeMoney), so the step sends the whole amount the list named. A read marks the mail anyway (the
  handler sets `MAIL_CHECK_MASK_READ` here); the step does not send mark-read on its own.

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

Three facts decide the flow:

* **The list is the probe.** The hello answers `SMSG_BATTLEFIELD_LIST` (BattlegroundMgr.cpp
  `SendBattlegroundList`); the core always sends an empty battlefield list, and an empty answer — no list id, no
  levels — stops the round with its report instead of queueing (the spec's own rule).
* **The join names the list's packed queue id.** The queue id packs the list id with the fixed high marker
  (`BattlegroundQueueTypeId::GetPacked`, SharedDefines.h): `PackQueueID` builds it, and the step sends roles and
  blacklists as zero.
* **The port echoes the invite's ticket.** The handler points the port at the queue the player really is in
  (BattleGroundHandler.cpp `HandleBattleFieldPortOpcode`), so the accept sends the need-confirmation's own
  ticket with the accept bit set, and the decline sends the same ticket with it clear.

### The server's answers

| Packet | Wire | Body | Core |
|---|---|---|---|
| `SMSG_BATTLEFIELD_LIST` | 0x4D0005 | master GUID, list id i32, min/max u8, count u32, the battlefields, two bits flushed | `BattlefieldList::Write`, BattlegroundPackets.cpp:261-275 |
| `SMSG_BATTLEFIELD_STATUS_QUEUED` | 0x4D0002 | average wait u32, the header, wait u32, spec i32, three bits flushed | `BattlefieldStatusQueued::Write`, BattlegroundPackets.cpp:226-238 |
| `SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION` | 0x4D0000 | timeout u32, map u32, the header, role u8 | `BattlefieldStatusNeedConfirmation::Write`, BattlegroundPackets.cpp:202-210 |
| `SMSG_BATTLEFIELD_STATUS_ACTIVE` | 0x4D0001 | the header, map/shutdown/start u32, arena faction i8, two bits flushed | `BattlefieldStatusActive::Write`, BattlegroundPackets.cpp:212-224 |
| `SMSG_BATTLEFIELD_STATUS_NONE` | 0x4D0003 | the ticket | `BattlefieldStatusNone::Write`, BattlegroundPackets.cpp:195-200 |
| `SMSG_BATTLEFIELD_STATUS_FAILED` | 0x4D0004 | the ticket, the queue id u64, the reason u32, the client id | `BattlefieldStatusFailed::Write`, BattlegroundPackets.cpp:240-248 |

The header is the ticket (`LFGPacketsCommon.cpp:23-44`: GUID, id, kind, time int64, one bit flushed), the queue
count, the range/team/instance bytes, the queue ids, then two bits flushed (BattlegroundPackets.cpp:177-193). The
failing case is `TestStatusHeaderQueuesBeforeBits`: the queue id follows the instance bytes and precedes the two
flushed bits — a parser that reads the bits where the count goes returns no queues and shifts every status after
the header. The queued parser's own order matters the same way: the average wait comes before the header
(BattlegroundPackets.cpp:226-238), and a parser that reads the header first shifts the whole status.

## Live checks for the lead's queue

### R7, the mail

1. **List.** `npc entry=<a mailbox>`; `mail`: the step line names the mail count, one line per mail (copper,
   attachments, flags, subject) and one per attachment (entry, count).
2. **Take money.** A mail with money, `mail-money id=<id>`: the line names the copper taken; `inv` shows the
   coinage rose.
3. **Take item.** A mail with an item, `mail-take id=<id>`: one `mail` line per attachment; `inv` shows the item,
   and a second take shows no attachments.
4. **Refusals.** A take of an unknown mail fails before any packet ("no mail in the last list"); a COD mail's
   take fails with the server's reason (`not enough money`, ...).
5. **Delete.** `mail-delete id=<id>`: the line names the mail; a second `mail` no longer lists it. Deleting a
   COD mail fails with the server's reason.
6. **Send.** `send-mail to=<name> subject="..." body="..." money=<n>`: the line names the recipient and the
   copper; the recipient's next `mail` lists it. A send to an unknown name fails with `recipient not found`.
7. **Relog.** `mail` after a relog matches the server's `mail` table (a taken mail shows no attachments, a
   deleted one is gone).

### R9, the battleground queue

1. **Probe.** `npc entry=<a battlemaster>`; `bg-queue`: when the realm has no support the step reports it and
   sends no join; otherwise the line names the list and the wait.
2. **Queue and leave.** `bg-queue`, then `bg-leave`: the leave line reports the queue left, and a second status
   shows none.
3. **Invite (only when one arrives).** `bg-queue` reporting the ready invite, `bg-accept`: the line names the
   map; `bg-leave` leaves the match.
4. **Refusals.** A non-battlemaster `bg-queue` fails before any packet; a failed queue names the server's reason
   (`range index` for a level-range mismatch, ...).
5. **A battleground the character cannot enter** (too low a level) fails at the queue with the server's reason,
   and sends no further packet.

## Gate lines

All on the clean committed tree (R7 `805913b0`, R9 `b78de815`; `git status` empty before each run).

* `make fmt-check`: no output, `rc=0`.
* `make cross`: the Windows and Darwin cross builds and the Windows vet of `./cmd/... ./internal/forever/...`
  passed, `rc=0`.
* `staticcheck ./...`: `rc=0`, no findings.
* The focused race run: `go test -race -count=1 -timeout 20m ./internal/forever/mail/ ./internal/forever/bg/
  ./internal/forever/wire/ ./internal/forever/npc/` and `go test -race -count=1 -timeout 20m -run
  'TestMail|TestBG|TestAct|TestParseActStep|TestVendor|TestQuest' ./internal/forever/world/`: `ok` for all.
* The full turtle-agent suite on the VM: `RESULT: PASS kind=ta wt=wt-fa-r79 sha=76a984f2 dirty=0 files=2527 runner=yulon-ubuntu2` (`rc=0`, `vm_duration=1143s`).
* The private capture checks are not in CI and were not re-run here (no capture files in this repository).
