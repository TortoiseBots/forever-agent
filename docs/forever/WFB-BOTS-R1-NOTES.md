# WoW Forever Bots: round A1 notes

Scope: the agent talks to the bots of a WoW Forever Bots server module. It sends the module's own chat commands
through the command channel, orders one bot by whisper (the bot answers) or all bots with a party word, and reads
state only through the module's add-on protocol. `forever fb-check` is the live proof: probe, add, roster, detail,
whisper orders, party word, dismiss.

Upstream source was read anonymously: the module's add-on control (`src/modules/PlayerbotV2/Session/
AddonControl.{h,cpp}`), its command handlers (`Commands/cs_playerbot_v2.cpp`, `Bot/BotCommandParser.cpp`) and the
add-on's own network file (`Addon/ForeverBots/ForeverBots_Net.lua`). Nothing was sent to the upstream repositories.

## The protocol, as this client implements it

- Transport: the merged add-on message transport (`internal/forever/addonmsg`). The client registers the prefix
  `PBC`, and sends requests on the **party** kind. The module answers with a self-addressed whisper add-on message
  under the same prefix. Requests are only possible when the account owns a bot and the client is in a group: the
  module ignores a protocol message from an owner-less or solo account. That is why the first step is `.bot add`:
  the server creates one bot, logs it in and groups it itself.
- Envelope: six fields separated by `|` — protocol digit `1`, the sequence number as eight lower-case hex digits,
  the part index (from 1), the part count, a message type in capitals and underscores, and the payload. The payload
  is a list of fields also joined by `|`. Inside any field a backslash is written as two and a pipe as
  backslash-`p`; a splitter must skip the byte after a backslash.
- Requests use sequence numbers below `0x80000000`, the module's unsolicited pushes start at `0x80000000`, and an
  answer echoes the request's number. That is how an answer is matched to its request.
- Chunking: the client writes at most 200 payload bytes per part (the transport's 255-byte text limit includes the
  envelope), the module writes 240. The parts carry the body's bytes in order, so a cut may fall inside an escape
  pair or a multi-byte character; the receiver glues the parts before splitting fields. The client holds at most 64
  parts and 4,096 assembled bytes per body, and drops an incomplete body six seconds after its last part (the
  module's own window is five).
- Requests this round sends: `PING` (one round trip; the answer is `PONG` with the echoed value) and
  `ROSTER_REQ` with the flag word 15 (bit one includes offline bots). For a bot the roster lists, `BOT_DETAIL_REQ`
  carries its numeric id.
- `ROSTER_RESP`: a count, then one record per bot. A record is 19 comma-separated values: id, name, level, class,
  race, role, zone, online, hp percent, mana percent, distance in yards (−1 for another map), intent, last rule,
  group id, spec word, headless, saved, grouped, spec id. A name cannot contain a comma.
- `BOT_DETAIL_RESP`: the id, then `key=value` fields (name, level, class, race, hp, hp_max, mana, mana_max, map,
  pos_x, pos_y, pos_z, zone_id, area_id, indoors, role, spec_id, xp, xp_for_level, rest_xp, last_rule,
  intent_depth). An id-only answer is the module's "I do not know this bot". A request for a bot the account does
  not own is answered with an error push instead.
- `EVENT_PUSH`: severity, bot id, event word, detail. A push is not an answer; an error push that names the bot a
  detail request asked about is that request's refusal.

## The drift rule

Nothing on the wire carries a version, so the client treats the shape as the version:

- a roster record with fewer than 19 values, a count that does not match the records, or a non-numeric number or a
  boolean that is not `0`/`1` is `ErrProtoMismatch`;
- a detail answer whose first field is not the requested id, or whose known numeric/boolean key has a value that
  does not fit, is `ErrProtoMismatch`; **unknown keys are kept** in `Extra`, as are fields that are not
  `key=value`;
- an answer of a type other than the request's, a malformed part, a body past the bounds, or no answer within six
  seconds is also a protocol failure.

Any of those turns the protocol **off for the session** (`pbc: unavailable (<reason>)`). `fb-check` then reads the
roster with `.bot list` (name, level, class, online/offline, saved; no ids, no live fields) and says so; whisper and
party orders keep working because they never used the protocol. A soft failure never fails the run.

## Packages

| Package | Content |
|---|---|
| `internal/forever/pbc` | the envelope, escaping, chunking, per-sequence reassembly (64 parts, 4 KiB, 6 s), typed roster entry, bot detail and event, the drift checks, fuzz targets |
| `internal/forever/command/fbcmd` | the typed command layer: `.bot about`, `.bot language`, `.bot add`, `.bot list`, `.bot dismiss`, whisper orders with ack collection, party words, the reply-line table, the `botctl` adapter |
| `internal/forever/botctl` | the shared controller interface (add, roster, state, order, dismiss) a later round's dungeon and party code uses |
| `internal/forever/world/fb_session.go` | the session side: the command queue without the GM guard, the whisper ack pump, party chat, one protocol request over the add-on inbox, and the `fb-check` driver |

The module's `.bot` commands take optional positional words, so they are sent through the existing command channel
**tokenless**: an appended `#token` would be read as an argument. The probe replaces the GM guard for this kind:
`.bot about` is allowed to every player, and a say echo of the command (or "command does not exist") proves the
module is absent.

## Tests

`internal/forever/pbc`: escape round trips (including a name with `|` and `\`), split/build, frame parse refusals
(zero sequence, bad index/count, 65 parts, a bad type, a payload over 4,096 bytes), chunking and reassembly in
order and out of order, a duplicate part, a conflicting count, a missing chunk that expires, an oversized body, the
roster parser (19 values, appended values kept, a short record, a count mismatch, a non-numeric value), the detail
parser (known keys, unknown keys, an unkeyed field kept, an id-only answer, the wrong id), events, and two fuzz
targets (frame parse and roster parse).

`internal/forever/command/fbcmd`: each command's line and answer, every add refusal, the unknown-answer case, the
list fallback (rows, "no companions yet", a different answer), roster/detail drift, ping, whisper acks (accepted,
refused, no answer, a bad name, a bad word), party words, dismiss (count, unknown name, usage), and the `botctl`
adapter.

`internal/forever/world` (fake server, real sockets): the happy path (say lines, party join, PING/ROSTER/DETAIL, a
roster and detail answered in reverse part order with an unrelated line between parts, whisper acks, dismiss, a
destroyed party), a party word plus the detail that confirms it, an add refusal that falls back to `.bot list`, a
drift-short roster that falls back and says so while the whisper orders still work, an order refusal, a dismiss
refusal, and a link that dies on the first protocol request.

One transport fix came out of this round: `addonmsg.FromLine` bounded a **delivered** text at 255 bytes (the
server's check on a client's outbound text). A server frame is bounded by `SMSG_CHAT`'s 12-bit text field (4,095),
and the module's own 240-byte parts pass 255 once the envelope is added. The receive bound is now 4,095
(`addonmsg.MaxReceiveText`); the send bound is unchanged.

## What round A2 needs

- The group-finder and dungeon commands (`.bot lfg`, `.bot dungeon`), `LFG_REQ`/`LFG_RESP` polling, and
  `lead on|pause|off` with the tank's party lines: all of them are new message types and command words on the same
  transport, the same request/answer path and the same reply table.
- A map-change-driven "inside the instance" confirmation, and a character at the dungeon's level.
- The `botctl.Controller` interface is already the seam for the party/dungeon code; `fbcmd.Controller` implements
  it, and a second module would add its own adapter.

## What was proven live

A box that installs the public package (world bots off, containers stopped beforehand and stopped again
afterwards), a fresh test account and a level-1 character made through the agent's character screen. `fb-check` ran
from the laptop to the box; the realm's advertised build was picked by the agent's normal rule. The transcript
below is identifier-free: the box's address, the account and character names and the bot's generated name are
replaced.

```
fb-check: registered the add-on prefix PBC
system: WoW Forever Bots by ... (GPL-2.0). .bot add <class> [tank|healer|dps], .bot call, .bot dismiss; all orders: BOT_COMMANDS.md
fb-check: module: <the line above>
system: Bot replies: English (your choice). .bot language auto|en|ru changes it.
fb-check: add warrior/dps: <bot> is on the way.
system: <bot> joined you: level 1.
party: 2 member(s), leader <char>
fb-check: <bot> is in the party (2 member(s))
fb-check: pbc: roster of 1 bot(s)
fb-check: roster: <bot> (id 2), level 1 HUMAN WARRIOR, DPS, online, 75hp 0mana, -, idle:always_buff, grouped=1 headless=1 saved=0
fb-check: detail <bot> (id 2): level 1 HUMAN WARRIOR, role DPS, hp 60/80, mana 0/0, map 0 at <position>, zone 12 area 9 indoors=0, spec 72, xp 0/400, last rule idle:always_buff
fb-check: whisper <bot> "follow": following at 5 yards
fb-check: whisper <bot> "stay": staying here
fb-check: dismiss all: Dismissed 1 companion(s).
whisper: <bot>: See you later!
party: group destroyed
fb-check: pbc: ok
fb-check: 2 whisper order(s), 0 refused, 0 unanswered
fb-check: 4 protocol frame(s) received
world logout: SMSG_LOGOUT_RESPONSE (result 0) and SMSG_LOGOUT_COMPLETE
```

The four protocol frames are the PONG, the roster (one part for one bot) and the two parts of the detail answer. The
roster's `headless=1` is the module's own mark for a bot session it drives; `grouped=1` proves the server grouped
the bot before the protocol answered. The roster's 75 % health and the detail's 60/80 are snapshots taken at
different moments, not a contradiction.

Two display notes. The module's usage text contains `|h` and `|r`; the display sanitiser treats them as the client's
formatting tokens (a link closer and a colour reset), so `[tank|healer|dps]` prints as `[tankealer|dps]` and
`auto|en|ru` as `auto|enu`. A real client renders them the same way; the raw line is what the probe matches. The
message trace itself is unaffected.
