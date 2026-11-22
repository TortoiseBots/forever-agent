# TBM add-on protocol contract v1

Forever shapes of the TBM 2.4.1 line list in
TortoiseBotsForeverManager PROTOCOL.md. Only what v1 pins down is here.

## 1. Forever transport

- Prefix and text are separate packet fields, not `TBM\t<text>`.
- Text holds at most 255 bytes; longer received text is dropped.
- The agent registers prefix `TBM` before the first command.
- The server hook runs after delivery and cannot consume: a PARTY
  request also reaches group members, whose add-ons ignore text
  without `TBM:`.
- PARTY with no group never reaches the hook: expect `TRANSPORT|none`.
- A whisper to self echoes back without `TBM:`: ignored, not a transport.
- Replies arrive as a whisper to self; the chat kind is not contracted.
- Chat fallback stays `.bot <verb>` whispered to self.
- Sends are paced 350 ms apart; one message holds LF-joined lines and a
  line is never split across messages in v1.
- `TBM` clashes with no reserved prefix.

## 2. Versioning (D2)

- Directory = shape version; `v1` = TBM:PROTO level 1.
- MANIFEST line 1 is `tbm-contract v1 rev <N> proto 1 min-addon 1`.
- A new case or appended trailing field bumps rev; a changed meaning or
  order makes `v2/` and raises PROTO.
- The agent speaks PROTO 1 and accepts modules with PROTO >= 1 whose
  min-addon <= 1.

## 3. Transports (D3)

- `chat`: `.bot <command>` as a whisper to self; the hello uses it.
- `addon`: `<command>` with prefix `TBM` on PARTY, only after
  `TBM:TRANSPORT|party`. No `whisper` word in v1.

## 4. Chunking (D4)

- No split lines; bulk travels in streams (`X_BEGIN|n`, rows, `X_END`)
  and the BEGIN count is authoritative.
- The reader splits received add-on text on LF and skips empty parts.

## 5. Strictness (D5)

- Skipped rows count toward BEGIN; unknown `TBM:` types and extra
  trailing fields are ignored; CAPS replaces the whole set, lowercased.
- Stricter: ACK exactly 4 fields, ERR exactly 3; hello needs PROTO,
  VERSION, CAPS and TRANSPORT.

## 6. Roster rows (D6)

- Forever rows: `guid|name|classId|state|grouped|location|ccMark|className`
  (8 fields, `-` is none). 6- and 7-field rows still read: a 7th field
  holding a CC mark id (moon, circle, square, cross, triangle, diamond,
  star, skull) is the mark, else the class name.

## 7. Round 1 message set (D7)

- Hello trailer: TRANSPORT, PROTO, VERSION, CAPS, LATEST; commands
  `roster` and `version`. Status: BOTSTATE stream and single updates.
- Roster: ROSTER_BEGIN/ROSTER/ALTBOTS/ROSTER_END, ROSTER_ERROR;
  CC_ASSIGN_* and CLAIMED_* are count-checked only. Command echo:
  ACTION_ACK, ACTION_ERR. All else is unknown, recorded, not an error.

## 8. Case format

- One record per line; `#` and blank lines skipped. `case <name>`
  (equal to the file stem) comes first; `desc` is free text; `module
  yes` marks canonical module output, re-emitted byte for byte.
- `send <addon|chat> <command>` is followed by one `frame` or
  `encode-error <code>`. Frames: `frame addon <CH> <PREFIX> <text>` and
  `frame chat <CH> <text>`. Receives: `recv addon <CH> <PREFIX> <text>`
  and `recv system <text>`. `expect <path> <value>` runs after Finish.
- In texts and values `\n` is LF, `\\` is a backslash, `{rep N C}`
  repeats C N times. `(empty)` is the empty string; unset Proto is `0`.
- Paths: hello.complete|transport|proto|min_addon|version|caps|
  latest_module|latest_addon|check (caps comma-joined; check `ok` or a
  code), roster.committed|count|`<i>`.guid|name|class|state|grouped|
  location|cc_mark|class_name|error|error_message, altbots
  (comma-joined), botstate.count|`<Name>`, ack.count|`<i>`.intent|
  scope|count|detail, err.count|`<i>`.intent|code|message,
  problems.count|problem.`<i>` (codes), unknown.count, ignored.count.

## 9. Problem and encode codes

`stream-count`, `row-outside-stream`, `end-without-begin`,
`nested-stream`, `unterminated-stream`, `field-count`, `bad-number`,
`bad-scope`, `empty-field`, `bad-row`, `bad-pair`, `text-too-long`,
`hello-incomplete`, `proto-incompatible`, `empty-command`, `unsafe-arg`.
