# Forever mail, round R7 notes

Scope: the mail from the player's side — list, take item and money, delete, and send text and money last — as
`forever act` steps. Code: `internal/forever/mail` (the wire layer: the list parse, the command result, the send
writer), `internal/forever/world/mail_act.go` (the steps), `internal/forever/world/act.go` (the verbs),
`cmd/forever-agent/forever_act.go` (`--mail-to`, `--mail-money-cap`, `--pay-cod`).

Everything below was checked in ForeverCore at d02645dd21 (`git show d02645dd21:<path>`). Every fixture is hand-made
from the core's writers; the capture checks are opt-in and print counts only, and no capture byte, GUID, name or file
name is in the repository.

Status: implemented, tested against the fake world. **No live run yet** (the live checks are at the end).

## The client packets

| Packet | Wire | Body | Core |
|---|---|---|---|
| `CMSG_MAIL_GET_LIST` | 0x3E00D8 | mailbox GUID | `MailGetList::Read`, MailPackets.cpp:186-190 |
| `CMSG_MAIL_TAKE_ITEM` | 0x3E00DA | mailbox GUID, mail id u64, attach id u64 | `MailTakeItem::Read`, MailPackets.cpp:224-229 |
| `CMSG_MAIL_TAKE_MONEY` | 0x3E00D9 | mailbox GUID, mail id u64, money u64 | `MailTakeMoney::Read`, MailPackets.cpp:232-236 |
| `CMSG_MAIL_DELETE` | 0x3D00E7 | mail id u64, reason i32 (always 0) | `MailDelete::Read`, MailPackets.cpp:240-244 |
| `CMSG_SEND_MAIL` | 0x430026 | mailbox GUID, stationery i32, money/cod, three sized strings (target 9, subject 9, body 11) and the attachment count (5) in one bit block, then the bytes and the attachments | `SendMail::Read`, MailPackets.cpp:196-214 |

The facts that decide the flow:

* **A mailbox is an NPC or a game object the character stands next to.** `CanOpenMailBox` answers the list only for
  one the server's reach check passes (MailHandler.cpp), and the character's own GUID is the cheat path (it answers
  only with the GM permission). The step names the creature the `npc` step found and refuses one without
  `UNIT_NPC_FLAG_MAILBOX`.
* **A take names ids from the list, never invented ones.** The attach id is the item's own GUID
  (MailHandler.cpp HandleMailTakeItem); the step reads it from the last list.
* **Money takes the whole amount.** The handler refuses a take whose money differs from the mail's
  (HandleMailTakeMoney), so the step sends the whole amount the list named. Only `CMSG_MAIL_MARK_AS_READ` sets
  `MAIL_CHECK_MASK_READ` (`HandleMailMarkAsRead`, MailHandler.cpp:378-395); this round sends no mark-read.
* **A take is confirmed, not assumed.** After `MAIL_OK` the step waits for the attachment's item to appear in the
  inventory and for the coinage to read back as the take left it; a COD mail pays its copper to the sender on the
  item take (MailHandler.cpp:506-545), so `mail-take` refuses one unless `forever act --pay-cod` is passed.
* **A delete only takes an empty mail.** The server deletes a mail with its items and money and only refuses a COD
  mail (`HandleMailDelete`, MailHandler.cpp:396-413), so `mail-delete` re-reads the list first and refuses a mail
  that still holds items, money or a COD.
* **A send needs the caller's allow-list.** `send-mail` accepts only a name on `--mail-to NAME=GUID`, the guid must
  answer that exact name to a `CMSG_QUERY_PLAYER_NAMES` query, and the money may not pass `--mail-money-cap`. The
  send packet names a name, so this is the only way the step knows the guid it pays. The postage is the core's 30
  copper for a mail without attachments, and the step confirms the exact debit.

## The server's answers

`SMSG_MAIL_LIST_RESULT` (the mail rows, their attachments and the sender forms; `mail.ParseList`),
`SMSG_MAIL_COMMAND_RESULT` (the mail, the command, the result; `mail.ParseCommandResult`) and
`SMSG_NOTIFY_RECEIVED_MAIL`. The list row's failing case is the second mail's id: a reader that reads the sender
form or the attachment frame wrong shifts every mail after the first.

## Tests

`internal/forever/world/mail_act_fake_test.go` runs the steps against the handler-boundary fake: the two-mail list
(second id parses), a non-mailbox and no-NPC refusal before any packet, silence, a refused take with the server's
reason, the exact money take and its read-back, a delete only after the takes, a COD take with and without
`--pay-cod`, the send allow-list, name confirmation and mismatch, the money cap, a long subject, and an unconfirmed
send (no wallet update, wrong debit).

## Live checks (NOT RUN)

1. **List.** `npc entry=<a mailbox>`; `mail`: the step line names the mail count, one line per mail (copper,
   attachments, flags, subject) and one per attachment (entry, count).
2. **Take money.** A mail with money, `mail-money id=<id>`: the line names the copper taken and the wallet's before
   and after; the step fails if the coinage does not read back as the amount the take added.
3. **Take item.** A mail with an item, `mail-take id=<id>`: one `mail` line per attachment; the step fails unless
   the item appears in the inventory; a second take shows no attachments.
4. **Refusals.** A take of an unknown mail fails before any packet ("no mail in the last list"); a COD mail's take
   is refused before any packet unless the run passed `--pay-cod`, and then the wallet reads back lower by the COD.
5. **Delete.** `mail-delete id=<id>` re-reads the list: it refuses a mail that still holds an item, money or a COD;
   once the takes are done the delete goes out and a second `mail` no longer lists it.
6. **Send.** `send-mail to=<name> subject="..." body="..." money=<n>` with `--mail-to <name>=<guid>` covering the
   recipient and `--mail-money-cap` covering the copper: the name query must answer that exact name for the guid
   before the send; a name off the allow-list, a guid answering another name, or money over the cap is refused
   before any packet.
7. **Relog.** `mail` after a relog matches the server's `mail` table (a taken mail shows no attachments, a deleted
   one is gone).

## Gate lines

* `make fmt-check`: no output, `rc=0`.
* The focused race run: `go test -race -count=1 -timeout 20m ./internal/forever/mail/ ./internal/forever/npc/` and
  `go test -race -count=1 -timeout 20m -run 'TestMail|TestSendMail|TestParseMailTo|TestAct' ./internal/forever/world/`.
* The full turtle-agent suite on the build VM (`vm-test ta`): `RESULT: PASS kind=ta sha=<sha> dirty=0`.
