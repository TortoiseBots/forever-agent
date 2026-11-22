# Forever world life, issue #106: trainers and mail (the auction house is a second PR)

Scope of this PR: the daemon methods for trainers (`trainer_list`, `train`) over the R4 act steps that already
exist (#86), and the mail round from nothing (`mail_*`: parsers, session state, events, methods). Code:
`internal/forever/mail` (the wire layer), `internal/forever/wire` (opcodes), `internal/forever/world`
(`mail.go` the session glue, `serve_mail_methods.go`, `serve_trainer_methods.go`), `internal/events` (three kinds).

Everything below was checked in ForeverCore at 6517af05 (`git show 6517af05:<path>`). Fixtures are hand-built from the
core's writers and readers; no capture byte is in the repository. turtle-agent had trainers (`trainer_list`, `train`)
and no mail at all, so the mail method names and fields are this server's own.

Status: implemented and tested against the fake world. **No live run yet**: the live checks for the lead's queue are
at the end.

## Trainers: what existed and what this adds

R4 (#86) already has the wire layer (`npc/trainer.go`), the handlers (`world/trainer.go`) and the `trainer` and
`learn` act steps (`world/trainer_act.go`). What was missing was the daemon: `trainer_list` and `train` were in
`notPortedMethods`.

| Method | Parameters | Answer |
|---|---|---|
| `trainer_list` | `guid`, `wait_ms` | `since`, `ok`, `event` (`trainer_list`), `trainer`: `trainer_type`, `trainer_id`, `greeting`, `spells[]` (`spell_id`, `state`, `cost`, `level`, `req_skill`, `req_skill_rank`, `req_ability`) |
| `train` | `guid`, `spell_id`, `wait_ms` | `since`, `ok`, `event` (`trainer_trained` with `cost`, `copper_before`, `copper_after`, or `trainer_failed`), `trainer` (the list after the purchase, so `state` shows the spell as known) |

The names and the `guid` / `spell_id` / `wait_ms` parameters are turtle-agent's (`cmd/turtle-agent/npc_methods.go:89-94`).
A row's `state` is `known`, `unavailable` (level or a required ability missing), `learnable` or `unknown_level`
(the model has not read the character's level), which is the client's own reading, not the server's.

Preconditions are RPC errors raised before any packet: a missing or non-positive `spell_id`, a unit that is not in
the world model or out of reach, a creature that is neither a trainer nor a gossip NPC, a spell the cached list of this
trainer does not hold. After a packet went out a refusal is `ok: false` with a `trainer_failed` event: the server's own
(`SMSG_TRAINER_BUY_FAILED`, written by the handler) or the step's client-side one (level, ability, coinage, known
spell: the R4 `learnBlocked` rules, unchanged). A `train` with no cached list for this trainer reads the list first, the
way `buy` reads the vendor's, because the server refuses a purchase under a trainer id the session never opened
(NPCHandler.cpp:134-152). A wait that runs out after the purchase went out is an error that says the outcome is unknown.

## Mail

### The client's packets

The mailbox is opened client-side: the core has no "open" packet, only the range check every handler makes
(`WorldSession::CanOpenMailBox`, MailHandler.cpp:39-63) and the list ask, which starts the interaction
(MailHandler.cpp:622). A mailbox is a game object of type 19 or a creature with `UNIT_NPC_FLAG_MAILBOX` (0x04000000,
UnitDefines.h:346); the game object's interaction distance is 10 here, "not blizzlike" (GameObject.cpp:4428-4431), so
the methods accept a mailbox up to 9 yards away and a creature at the NPC reach.

| Packet | Core opcode | Body | Core |
|---|---|---|---|
| `CMSG_MAIL_GET_LIST` | 0x3E00D8 | mailbox (packed GUID) | MailPackets.cpp:166-169, MailHandler.cpp:599-627 |
| `CMSG_MAIL_MARK_AS_READ` | 0x3E00DC | mailbox, uint64 mail id | MailPackets.cpp:234-238, MailHandler.cpp:378-393 |
| `CMSG_MAIL_TAKE_ITEM` | 0x3E00DA | mailbox, uint64 mail id, uint64 attachment id | MailPackets.cpp:246-251, MailHandler.cpp:466-562 |
| `CMSG_MAIL_TAKE_MONEY` | 0x3E00D9 | mailbox, uint64 mail id, uint64 money | MailPackets.cpp:253-258, MailHandler.cpp:564-596 |
| `CMSG_MAIL_DELETE` | 0x3D00E7 | uint64 mail id, int32 reason (ignored) | MailPackets.cpp:240-244, MailHandler.cpp:396-413 |
| `CMSG_MAIL_RETURN_TO_SENDER` | 0x430081 | uint64 mail id, sender (packed GUID) | MailPackets.cpp:216-220, MailHandler.cpp:415-463 |
| `CMSG_SEND_MAIL` | 0x430026 | mailbox, int32 stationery, int64 money, int64 COD, bit lengths 9/9/11/5, a flush, target, subject, body, then (uint8 position, packed item GUID) per attachment | MailPackets.cpp:196-214, MailHandler.cpp:65-375 |
| `CMSG_QUERY_NEXT_MAIL_TIME` | 0x3E00DB | none | MailPackets.h:199-205, MailHandler.cpp:681-718 |

The wire numbers are the group arithmetic and the `ClassicClientOpcodes.inc` rows, pinned for both supported client
builds in `wire/opcodes_test.go`. `CMSG_MAIL_CREATE_TEXT_ITEM` (copy the body to an item) is not built: nothing asked
for it.

The failing case for the send layout is **`TestSendMailReadsBackThroughTheServersReader`**: the four bit lengths
share two bytes and one flush, and the strings follow in target, subject, body order; the test reads the bytes with a
reader written independently from `SendMail::Read`.

### The server's answers

| Packet | Core opcode | Body | Core |
|---|---|---|---|
| `SMSG_MAIL_LIST_RESULT` | 0x450200 | uint32 count (at most 100), int32 total, then per mail: uint64 id, uint32 sender type, uint64 COD, int32 stationery, uint64 money, int32 flags, float days left, int32 template, uint32 attachment count, the sender (packed GUID for type 0, int32 for 2-9), the attachments, 8-bit subject and 13-bit body lengths, a flush, subject, body | MailPackets.cpp:122-164, :171-180 |
| attachment | | uint8 position, uint64 attachment id, an `ItemInstance`, int32 count, int32 charges, uint32 max durability, int32 durability, 4-bit enchant count, 2-bit gem count, unlocked bit, a flush, the enchants (int32, uint32, int32, uint8), the gems (uint8, `ItemInstance`) | MailPackets.cpp:61-82 |
| `SMSG_MAIL_COMMAND_RESULT` | 0x4500E4 | uint64 mail id, int32 command, int32 error, int32 bag result, uint64 attachment id, int32 quantity (always 32 bytes) | MailPackets.cpp:222-232, Player.cpp:2840-2856 |
| `SMSG_MAIL_QUERY_NEXT_TIME_RESULT` | 0x450201 | float next time, int32 count, per sender: packed GUID, float, int32 sender id, int32 type, int32 stationery | MailPackets.cpp:286-301 |
| `SMSG_NOTIFY_RECEIVED_MAIL` | 0x4500E5 | float delay | MailPackets.cpp:303-308 |

The failing case for the list is **`TestListSecondMailAfterAnAttachedFirst`**: the second mail's id and sender kind
parse after the exact bytes of a first mail with a packed-GUID sender, an enchant and a gem. A command result is
`MAIL_OK` (0) or one of the refusals (`not_enough_money`, `recipient_not_found`, `cannot_send_to_self`,
`not_your_team`, `recipient_cap_reached`, `too_many_attachments`, `attachment_invalid`, ...); an equip error carries
the inventory failure in the bag result, which the answer names.

### The methods

All answers carry `since` (the event-log cursor before the first packet) and `event` (the row that answers: `mail_list`
or `mail_result`). A mail's text is another player's: the log's `mail_list` row carries ids, senders, subjects and the
money and item counts, and the bodies only appear in the method answer.

| Method | Parameters | What it does |
|---|---|---|
| `mail_list` | `guid` (mailbox), `wait_ms` | asks the list: `total`, `mails[]` (`mail_id`, sender, `subject`, `body`, `money`, `cod`, `read`, `flags`, `days_left`, `items[]` with `index`, `attach_id`, `item_entry`, `count`, ...) |
| `mail_read` | `guid`, `mail_id` | marks the mail read, then asks the list again: the core answers a mark with nothing, so the flag in the new list is the proof (`ok` false when the server ignored it); an already read mail sends nothing |
| `mail_take_item` | `guid`, `mail_id`, `index` (optional when the mail holds one item), `force` | takes one attachment, waits for the result (`item`, and for a COD mail the purse before and after). A COD mail pays the sender on the take, so it needs `force: true` |
| `mail_take_money` | `guid`, `mail_id` | takes the money the list showed (the server checks the amount, MailHandler.cpp:572-573), with the purse before and after |
| `mail_delete` | `guid`, `mail_id`, `force` | deletes; a mail that still holds items or money needs `force: true` because the core deletes it with its contents |
| `mail_return` | `guid`, `mail_id` | returns a player's mail; auction and creature mail, a returned mail and a mail the sender made unreturnable are refused before any packet |
| `mail_send` | `guid`, `to`, `subject`, `body`, `money`, `cod`, `attachments[]` (`item_entry`, or `bag` and `slot`) | sends one mail; attachments are the character's own bag items (never an equipped item or a worn bag, none twice); postage is 30 copper per attachment, 30 for a text-only mail (MailHandler.cpp:171); the answer is the server's result and the purse before and after |
| `mail_status` | `wait_ms` | asks when the next mail arrives and who sent the unread ones; names no mailbox |

Every command after `mail_list` addresses a mail that list showed, from that mailbox: a call for another mail, or from
another mailbox, is an RPC error before any packet ("mail_list comes first"). A refusal after a packet went out is
`ok: false` with a `mail_result` event; a result that never came is an error that says the outcome is unknown (a retry
may repeat a send or a take). The core answers some sends with nothing (a receiver below the mail level only gets a
notification, MailHandler.cpp:221-225), so a silent send is the unknown outcome too.

`mail_send` does not pre-check the purse: a game master sends free (MailHandler.cpp:187), and the server's
`not_enough_money` is a result, not a harm. `SMSG_NOTIFY_RECEIVED_MAIL` is a `mail_new` event.

## Live checks (for the lead's queue)

1. At a mailbox with two characters of the same faction: `mail_send` a text mail (with money) from one, `mail_list`
   on the other. Expect `ok`, the purse down by 30 plus the money on the sender, a `mail_new` event and a mail in the
   list. Items and money mail arrive after the delivery delay (MailHandler.cpp:331-334).
2. `mail_take_money`, `mail_take_item` (a plain item, then a COD one with `force`), `mail_read`, `mail_return` and
   `mail_delete` on the received mail; each answer matches the server's own state on the next `mail_list`.
3. A refused send (an unknown recipient) answers `ok: false` with `recipient_not_found`.
4. `trainer_list` and `train` at a class trainer for a spell the character can afford and one it cannot: the
   learned-spell answer with the purse before and after, and the refusal.
