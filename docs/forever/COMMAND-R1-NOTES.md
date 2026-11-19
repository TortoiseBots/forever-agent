# Forever command channel, round A6 notes

Scope: send a chat-command line on purpose (a line that starts with `.`), collect the system lines that belong to
it, and parse the state lines a server module with `.tb` commands prints into a typed model. No new wire packet:
the request is `CMSG_CHAT_MESSAGE_SAY`, the answer is `SMSG_CHAT` with chat type `CHAT_MSG_SYSTEM` (and, for one
kind of answer, `SMSG_PRINT_NOTIFICATION`), both already implemented in round 5.

Code: `internal/forever/command` (the collector and the refusal classification), `internal/forever/command/tbcmd`
(the typed `.tb` layer and the state/threat/party grammars), `internal/forever/world/command_session.go` (the
session side: the transport over the packet loop, the GM guard, `forever cmd-check`), `cmd/forever-agent/
forever_command.go` (the command line).

## How the core recognises and answers a command

* The chat handler parses a line as a command **before** any chat validation, for every chat kind including a
  whisper: `ChatHandler(this).ParseCommands(msg.c_str())` at `Handlers/ChatHandler.cpp:233`. A line that starts
  with `.` or `!` is a command (`Chat.cpp:181-201`); a single `.`/`!`, `..`, or a `.` followed by the command
  delimiter is not.
* Replies are system chat to the sender: `ChatHandler::SendSysMessage` (`Chat.cpp:111-125`) sends one
  `SMSG_CHAT` of type `CHAT_MSG_SYSTEM`, `LANG_UNIVERSAL`, per line, splitting its text on `\n`. It builds an
  escaped copy of the message (every `|` doubled) but tokenises the **original**, so a single `|` arrives and `||`
  stays two bars. The packet's text length is a 12-bit field (`Packets/ChatPackets.cpp:196-238`), so one reply
  line is at most 4,095 bytes.
* A few answers are not system chat. `.gm on`/`.gm off` answer with `SendNotification` →
  `SMSG_PRINT_NOTIFICATION` (`scripts/Commands/cs_gm.cpp:226-240`); the world session turns that into the same
  system line in the chat log, so the collector sees it.
* Permission: a command row declares an RBAC permission; a session without it does not see the row at all
  (`ChatCommands/ChatCommand.cpp:472-479`, `IsInvokerVisible`). `_ParseCommands` (`Chat.cpp:166-179`) then behaves
  in one of two ways:
  * the session holds `RBAC_PERM_COMMANDS_NOTIFY_COMMAND_NOT_FOUND_ERROR` (33): it gets the trinity string 6,
    `Command '<text>' does not exist`, and nothing runs;
  * it does not hold 33: `ParseCommands` returns false and the chat handler carries on with the line as ordinary
    chat — **the command text is said as say chat**. This is the reason for the GM guard below: an account that
    lacks the permission cannot be told from an ordinary player by the reply alone, and sending commands from it
    would put `.tb ...` lines into local say chat.
* The `.tb` commands of the module need `RBAC_PERM_COMMAND_GM` = 371 (`Accounts/RBAC.h:245`), the same permission
  as `.gm on`/`.gm off`.

## Reply collection

`command.Run(ctx, conn, line, opts)` sends one line through the session's existing `SendCommand` (the only path
that allows a leading `.`; `ValidateCommandText` owns the rule) and collects the reply through `conn.Pump`, which
is the session's packet loop. The pump sets `session.cmdFeed`, and every chat line the session handles reaches the
collector through `feedCommand` **before** the chat log's display-sized copy is made: the log truncates text to
512 bytes, and a state line is longer than that (a typical one is about 530), so the collector must see the parsed
line, bounded at 4,095 bytes instead. Nothing is taken out of the log: the log gets its own copy, and unrelated
system lines still reach it and its output.

Two live-found details shaped this: the server fills `SMSG_CHAT.SenderName` only for a whisper, so this
character's own say line has **no sender name** — the say-echo check compares the GUID (`SelfGUID`, the active
mover's until the create block names it) as well as the name; and a core refusal can be one line *inside* a longer
usage block (`Possible subcommands:` precedes the entries), so a tokenless run records the refusal but keeps
collecting until its quiet window, or the block's tail would be handed to the next command.

End conditions:

| case | ends when |
|---|---|
| a token command (`#t...` as the last word of the line) | a line whose last word is that token and whose kind is an error (`error ...`) or ends in `-end`; with `Options.EndKind` set, only that end kind (or an error) |
| a tokenless command (a core command, or an older module command) | no matching system line for `Options.Quiet` (default 1.5 s), or `Options.MaxLines` lines (default 64), whichever first |
| every command | `Options.Deadline` (default 15 s) |

Bounds: at most `MaxCollectedLines` 256 lines, `MaxCollectedBytes` 64 KiB, and each collected line is at most
`MaxLineBytes` 4,095 bytes (the chat text field). A reply past a bound ends the run with a named error rather than
growing memory. Lines are matched to the command by the token; a system line without the token is **not** part of
the reply. For a tokenless command that distinction is impossible (the stated reason for the one-at-a-time queue):
every system line after the send is a candidate, and the run is documented as a probe, not as a way to run two
commands at once. `command.Session` serialises `Run` calls with a mutex; the session goroutine also runs them
serially by construction.

An **untagged** refusal is the one thing a token cannot match. For a tokenless run it is the run's own (the whole
help block is kept until the quiet window). For a tokened run the collector takes it only when it arrives before
any tokened line of that run **and** the session has established that no earlier command's tail can still be
arriving: after a run that did not end with its own marker (a tokenless run's line cap or deadline, or an
incomplete reply), `Session` waits until `Quiet` after that run's last line before it sends a tokened line. A
refusal after the first tokened line is unrelated traffic and neither ends nor joins the run.

Colour codes: the core's `SendSysMessage` builds an escaped *copy* (every `|` doubled) but then tokenises the
**original** (`Chat.cpp:111-125`), so a single `|` arrives and `||` means two bars. `Visible` therefore does not
collapse `||`; it removes `|cAARRGGBB`, `|r`, `|n` and unfolds `|H...|h` to its label, so a wrapped player link does
not break the field split. The raw text is kept in `Reply.Raw`; parsing uses the visible text. A raw `|` that is not
a code stays as it is.

## Refusals

Recognised from the client's side, returned as named errors (the offending line is in `Reply.Raw` and in
`Reply.RefusalText`):

| error | matched text / evidence | note |
|---|---|---|
| `ErrUnknownCommand` | system line `Command '<x>' does not exist` (trinity string 6) — matched on the prefix `Command '` and the suffix ` does not exist` | also what a missing permission looks like when the account holds permission 33: the core hides it (`IsInvokerVisible`) |
| `ErrSyntax` | `Incorrect syntax.` (string 10); `Subcommand '<x>' does not exist.` (string 193); `Command '<x>' is ambiguous:` (194) / `Subcommand ... is ambiguous:` (7); `Value '<x>' is not valid for type <t>.` (1502); a help line starting `Syntax: `; `Possible subcommands:` (8) or `Commands available to you:` (9) | a command that ran and refused its arguments sends its usage/help, which is what an invocation failure produces (`ChatCommand.cpp:307-316`); a tokenless run keeps the whole block and ends at the quiet window |
| `ErrNoPermission` | any command text comes back as this character's own `CHAT_MSG_SAY` (this run's line or an earlier one's late echo), or no line arrives before the deadline | the say echo is the core's fall-through path above; the client treats it as "the account is not a GM" and sends nothing else in the session |
| `ErrNoReply` | nothing at all within the deadline | a module command that does not answer is not a refusal; the tb layer reports the incomplete reply |
| `ErrIncomplete` | lines arrived, the end marker did not | the tb layer requires the end line before it acts |

## The GM guard

`forever cmd-check` sends one probe before anything the user asked for: `.gm` with no argument, the only
non-mutating form of the core's gm command (`src/server/scripts/Commands/cs_gm.cpp:46-60`: the `gm` table has the
subcommands `chat`, `fly`, `ingame`, `list`, `visible`, `on`, `off`, and no bare invoker). With no argument the
core prints the command's help block; the `on` and `off` entries are leaves whose permission is
`RBAC_PERM_COMMAND_GM` 371 (`cs_gm.cpp:53-54`, `Accounts/RBAC.h:245`), so the core lists them
(`ChatCommand.cpp:472-479`, `IsInvokerVisible`) only when the account holds 371 — the same permission the
module's `.tb` commands need. The probe needs nothing else and changes nothing; there is no GM mode to restore and
no way to switch off a character that had GM mode on.

It is sent with a 4-second deadline and a 1.5-second quiet window, and the guard confirms only when:

* a collected line is exactly the probe's acknowledgement (`|- gm on` or `|- gm off`, the help block's entry), and
* a say echo of the probe appears nowhere in the run — the check keeps running after the first collected line, so a
  mute/flood/unknown-language notification, a late MOTD or an announcement that arrives first cannot stand in for
  the answer.

A notification, an announcement, "Command 'gm' does not exist" (an account that holds 33 but not 371) or no line at
all leaves the guard unconfirmed, and the run sends nothing else. A confirmed run also sends nothing afterwards to
put anything back.

The guard is enforced in one place: `cmdLink.SendCommand` refuses every line that is a command text while the
session's guard is unconfirmed (only the probe link may send the probe). `runOne`, `tbcmd.Client` and any future
caller go through that link, so they cannot send a `.` line without it. The guard also watches every chat line:
a self say whose text is a command text is a late echo even between runs; it resets the guard to unconfirmed and
sets a stop error, so the next send is refused and the run ends. A new session starts unconfirmed.

## The reply grammar, version 2, in my own words

The module's author replaced the version-1 escapes with a new grammar after the first round's interface list; a
later addendum settled the eleven questions that version left open. The typed layer speaks version 2 only; version
1 is neither accepted nor produced. Nothing in the client keys on a module name: a tokened reply is recognised by
its token and its grammar, and a free-text reply by the core's refusal texts.

* A line is words separated by exactly one space. The first word is the record kind (`state`, `order`, `party`,
  `threat`, `error`, ...); every other word is `key=value`; the last word of every line of a tokened reply is
  `#<token>`. The token grammar is 4..16 characters of `A-Z a-z 0-9` (the client also accepts `_ . : -`).
* Only an `error` is a single-line reply: `error code=<word> cmd=<word> [msg=<text>] #token`, with no other key
  and no markers. Every other tokened reply has both markers. The markers are named after the command
  (`state-begin`/`state-end`, `party-begin`/`party-end`, ...); the data lines between them carry their own record
  kind.
* `<command>-end` carries `count=` (the lines between the markers, continuation lines included) and `records=` (the
  lines that are not continuations); both are required and checked against the lines. Other totals (`resting=`,
  `bots=`, ...) count records.
* A key is `[a-z][a-z0-9_]{0,23}` and appears at most once in a line, and at most once in a record. A record that
  would be longer than the cap is continued on a `<kind>+` line whose first key repeats the record's identity
  (`bot=`, `member=`, `mob=`), immediately after its base line; any other key may appear once across the base and
  its continuations.
* Values are percent-encoded: every byte outside `A-Z a-z 0-9 . _ -` is `%XX` with upper-case hex. A `%` is always
  an escape: a bad or lower-case sequence, a control byte, or an unencoded space, `=`, `|`, `#` or `+` is refused.
  An empty value is the empty string.
* Inside a value, `;` separates list items (strategies, rest reasons, threat pairs), `:` separates the parts of one
  item (a target label `party:Tbtank`, `ord=41:ok`, `stuck=blocked:<reason>`, `top=Name:412`) and `,` separates
  coordinates. Those three never come out of the encoding of data, so the client splits the encoded value first and
  decodes each part after.
* One line is at most 3,000 bytes (the chat field itself holds 4,095).
* Numbers are decimal, booleans `yes`/`no`. A number that is not known is an absent key, never `0` or `-1`; the
  word `none` has no special meaning and is never written for an empty value (the empty value is `key=`).
  `state-end` carries `count=`, `records=`, `resting=`, `target=`, `ready=yes|no`, and `why=` (a non-empty list)
  exactly when `ready=no`.
* A position is one key `pos=x,y,z` (each decimal with at most two fraction digits) next to `map=`; a corpse is
  `corpse=x,y,z` with `corpsemap=`. There are no separate `x=`/`y=`/`z=` keys.

The typed model requires the keys the interface documents as always present, accepts the empty value where the
grammar defines it, refuses an unknown enumeration word where the set is fixed, keeps a genuinely unknown key in
the record's `Extra` map after checking its encoding, and checks ranges (level 1..255, hp and
power counters at least 0, hp <= hpmax, percentages 0..100, map/instance as uint32 so a negative cannot wrap). An
unknown number is simply absent (the struct carries a presence bit). Two records with the same identity are refused,
as is a record that is split and then repeats a key, and a continuation whose identity is not its first key.

## What was proven live (2026-11-19)

Server: the test server. Two test characters were used; names below are placeholders. These are the round-1
transcripts; their guard probe was the old mutating `.gm on`, which round 2 replaced with the non-mutating `.gm`
(the round-2 check is at the end of this section).

**A character without the permission is refused.** `cmd-check --line ".server info"`:

```
chat: CHAT_MSG_SAY <you>: .gm on
cmd: .gm on  (the GM guard's probe)
cmd-check: GM confirmed false, 1 command(s): 0 answered, 1 refused, 0 not acted on
cmd-check: .gm on -> refused (command: the server did not run it (the line came back as ordinary say chat: the
           account lacks the command permission))
forever cmd-check: world: the account is not a GM: the server said ".gm on" as ordinary say chat, so it did not
           run and this run sends nothing else (command: the server did not run it (...): .gm on)
```

So what a player without the permission gets is **its own line back as say chat** — the core's fall-through in
`_ParseCommands` (`Chat.cpp:166-179`) — and no system reply at all. The client named it `ErrNoPermission` and did
not send `.server info`: the requested command never went out. (The run's exit code was 5, not 0.)

**The GM character's rights.** It already held the command permission: its inherited role chain links 371
(`Command: gm`). The run changes no account or permission, and no account data is quoted here.

**Core commands as the confirmed GM.** `cmd-check --line ".gm" --line ".gm on extra" --line ".account onlinelist"
--line ".gps" --line ".nosuchcmd"`:

```
cmd-r: GM mode is ON                                       (the probe; the guard confirmed)
cmd: .gm
cmd-r: ### USAGE: .gm ...
cmd-r: Possible subcommands:
cmd-r: |- gm chat
cmd-r: |- gm ingame
cmd-r: |- gm off
cmd-r: |- gm on
cmd-r: |- gm visible
cmd: refused: command: the server rejected the command's syntax or arguments: Possible subcommands:
cmd: .gm on extra
cmd-r: Syntax: .gm on
cmd-r:
cmd-r: Enables in-game GM mode. This makes you exempt from most game mechanics.
cmd-r: Among other things, it makes creatures unable to attack you, and makes you untargetable by most spells. It
       also makes creatures in all phases, as well as invisible creatures, visible to you.
cmd: refused: command: the server rejected the command's syntax or arguments: Syntax: .gm on
cmd: .account onlinelist
cmd-r: ### USAGE: .account
cmd-r: Prints information about the current account.
cmd-r: Possible subcommands:
cmd-r: |- account 2fa ...
cmd-r: |- account addon
cmd-r: |- account email
cmd-r: |- account lock ...
cmd-r: |- account password
cmd: refused: ...: Possible subcommands:
cmd: .gps
cmd-r: Command 'gps' does not exist
cmd: refused: command: the server says the command does not exist: Command 'gps' does not exist
cmd: .nosuchcmd
cmd-r: Command 'nosuchcmd' does not exist
cmd: refused: command: the server says the command does not exist: Command 'nosuchcmd' does not exist
cmd-r: GM mode is OFF                                      (the guard's undo)
```

`.gps` and `.server info` (tried in an earlier run: `Command 'server info' does not exist`) are **not** in this
role chain: permission 505 (`Command: gps`) and 725 (`Command: server info`) belong to 196 (`Administrator
Commands`), which a higher role links. The expectation was that they would be visible here; the character gets the
core's "does not exist" answer instead. That is not the client's guess: the same run shows the usage block of
`.gm`, which *is* visible. Multi-line core replies were proven with `.gm`, `.gm on extra` and
`.account onlinelist` (3 to 6 lines each), the unknown command with `.nosuchcmd`, and the wrong arguments with
`.gm on extra`.

**The module's own commands (free text, no tokens).**
`cmd-check --quiet 2s --line ".tb status" --line ".tb login <bot>" --line ".tb where <bot>" --line ".tb logout <bot>"`:

```
cmd-r: <module>: enabled, 0 bot session(s).
cmd-r: <module>: <bot> is logging in.
cmd-r: <module>: <bot> level 1 map 0 at <position>, health 60/60, out of combat, victim none (health 0).
cmd-r: <module>: brain: spike
cmd-r: <module>: 16 creature(s) within 60 yd, nearest alive <creature> at 11.9 yd (may attack: yes,
       friendly: no).
cmd-r: <module>: sees it: yes, bot unit flags 0x8 player flags 0x0, target unit flags 0x0, target faction 32,
       uninteractible: no.
cmd-r: <module>: <bot> is logging out.
```

(Each line begins with the module's fixed prefix and a colon; the prefix is the module's own name, replaced here
because the module's port is private work. The position was the character's spawn area; the creature is a starter
zone mob.)

The four commands' lines arrived in the runs they belonged to (1, 1, 4 and 1 lines, each ended by the quiet
window), and the server's own log records the probe and the explicit `.gm on` as commands by that character (the
GM command appender). This is the channel end to end with a **real module command**: `.tb status` answered by the
module, `.tb where` with a four-line answer. The typed `.tb` layer is **not** proven live in this round: the live
module has no `.tb state`, no tokens and no typed orders, so `--tb-state` against it fails with the core's
`Command 'tb state ...' does not exist` (covered by the fake server instead).

### Round 2: the guard's new probe, checked live (2026-11-19)

The probe changed from `.gm on` to the non-mutating `.gm`, so both characters were run again. Identifiers are
placeholders.

**A character without the permission.** `cmd-check --line ".server info"`:

```
chat: CHAT_MSG_SAY <you>: .gm
cmd: .gm  (the GM guard's probe)
cmd-check: GM confirmed false, 1 command(s): 0 answered, 1 refused, 0 not acted on
cmd-check: .gm -> refused (command: the server did not run it (the line came back as ordinary say chat: the
           account lacks the command permission))
forever cmd-check: world: the account is not a GM: the server said ".gm" as ordinary say chat, so it did not
           run and this run sends nothing else
```

Exactly one say line (the probe, as the guard's stated allowance); `.server info` was never sent. Exit code 5.

**The GM character.** `cmd-check --line ".gm on extra" --line ".nosuchcmd" --line ".tb status"`:

```
cmd: .gm  (the GM guard's probe)
cmd-r: ### USAGE: .gm ...
cmd-r: Possible subcommands:
cmd-r: |- gm chat
cmd-r: |- gm ingame
cmd-r: |- gm off
cmd-r: |- gm on
cmd-r: |- gm visible
cmd-check: GM confirmed (the probe listed the gm command); the caller's commands will be sent
cmd: .gm on extra
cmd-r: Syntax: .gm on
...
cmd: refused: ...: Syntax: .gm on
cmd: .nosuchcmd
cmd-r: Command 'nosuchcmd' does not exist
cmd: refused: ...: Command 'nosuchcmd' does not exist
cmd: .tb status
cmd-r: <module>: enabled, 0 bot session(s).
cmd: 1 line(s), ended by the quiet window
cmd-check: GM confirmed true, 4 command(s): 1 answered, 3 refused, 0 not acted on
```

No `CHAT_MSG_SAY` line at all: the probe was not spoken, and no `.gm on`/`.gm off` went out. Exit code 0.

## What is proven only against the fake server

The token reply (matched, split over several messages, with interleaved unrelated system lines), the quiet-window
end, the hard deadline and the incomplete report, the say-echo refusal, the unknown-command and syntax refusals,
the link death mid-reply, the 4,095/4,096-byte reply-line boundary, the typed state table, and every typed call
(order/role/master/party/threat, WaitRested) including the module's `error code= ...` line. The fake implements these from
the core's writers and readers (ChatPackets.cpp, Chat.cpp), not from this client's parsers.

## Interface problems (v2)

The version-1 list (a space escape that collides with real underscores, `+` and `:` as both structure and escapes,
`none` for empty, no begin marker, no line cap, `error=` as a kind) is what produced version 2; it is closed. The
client now accepts version 2 only. All eleven questions below were answered by the module's author; each item states
the answer and how the client follows it.

1. **Which replies are single-line? Settled: only `error`.** Every other tokened reply has both markers; the client
   requires them for state, order, role, master, the party roster, forming and threat.
2. **What do `count` and the other numbers count? Settled: `count=` is lines, `records=` is records, and both are
   required on every end line.** The client checks both against the lines and refuses a missing or wrong one; the
   other totals (`resting=`, `bots=`, `asked=`, `size=`) count records.
3. **The forming answer's markers. Settled: markers are named after the command.** The forming answer is
   `party-begin ... party-join ... party-end count=... records=...`, and the client asks for `party-end`.
4. **Continuation identity. Settled: required, equal, and first.** A `<kind>+` line repeats the record's identity as
   its first key and follows its base line immediately; the client refuses a missing, late or different identity and
   any repeated key across the split.
5. **`why=` on a state-end. Settled: present and non-empty exactly when `ready=no`.** The client refuses `why=` when
   ready and an absent or empty one when not ready.
6. **`none`. Settled: it has no special meaning anywhere.** No value is the empty string, and an unknown number is an
   absent key. The client no longer accepts `none` for any enumeration and no longer reads it as -1.
7. **A lower-case escape. Settled: an error, not normalised.** The client refuses `%2f` and any `%` that is not two
   upper-case hex digits.
8. **Unknown keys. Settled: allowed, validly encoded, kept.** Every record type has an `Extra` map that holds the
   keys the model does not type, decoded, after checking their encoding.
9. **An empty list versus an empty item. Settled: the empty value is the empty list; an empty item is an error.**
   The client reads `rest_why=` as an empty list and refuses `a;;b` or a trailing `;`.
10. **Coordinates. Settled: one key, next to its map.** A position is `pos=x,y,z` with `map=`; a corpse is
    `corpse=x,y,z` with `corpsemap=`; there are no separate `x=`/`y=`/`z=` keys. The client reads exactly those and
    refuses a coordinate with more than two fraction digits.
11. **`error`'s keys. Settled: exactly `code=`, `cmd=` and the optional `msg=`.** No `name=`, and no other line; the
    client refuses both.

## Tests

* `internal/forever/command` — the collector unit tests: token matching and a reply split over messages, the
  quiet window, the deadline and the incomplete report, the say echo (by GUID, as the server sends it, and by
  name) including one that arrives after a collected notification and a late echo of an earlier command, every
  refusal text, a refusal line inside a usage block, an untagged refusal before and after the first tokened line,
  the session's quiet wait between a tokenless run and a tokened one, the line/byte bounds, the serialising queue,
  the token grammar, duplicate keys, `Visible` (a single bar, `||`, codes and links), reply-shaped say/whisper/party
  lines that are neither collected nor an end marker, and fuzz targets over `ParseReplyLine` and `Visible`.
* `internal/forever/command/tbcmd` — every state key, the version-2 decode (bad percent, lower-case hex, a raw
  bar, a duplicate key, a free word, two spaces, the 3,000-byte cap, a missing begin, a token mismatch, a count
  or `records=` mismatch or absence), continuation lines (merge, duplicate across the split, identity not first,
  wrong identity, no base), unknown keys kept, required keys, presence bits (`HasSeq`, `HasLevel`, `HasManaPct`,
  `HasTargetEntry`, ...), ranges (negative map, uint32 overflow, level 0, hp > hpmax, percentage over 100, a bad
  GUID), the rest reasons, five bots, the longest plausible line, a literal `none` and separate `x=`/`y=`/`z=`
  refused, the `ready`/`why` rule in both directions (including an empty `why=` when not ready), the error line's
  exact keys and single-line rule, every typed call's line and answer, `WaitRested` and its timeout, and a fuzz
  target over the line decoder and the state record.
* `internal/forever/world` — the collector against the fake world server: a token reply with a middle system line
  that reaches the chat log but not the reply, a quiet end, a deadline, the core refusals, the say echo, a link
  death mid-reply, the 4,095-byte line (kept) and the 4,096-byte line (the chat parser refuses it and the run
  ends), the typed state table, a character without the permission that only sends the probe, a notification
  before the probe's echo, an unrelated system line with no acknowledgement, an echo after a confirmed probe, a
  character that had GM mode on (no restore is sent), and the pump's context.

## Merge hygiene

The round adds no opcode and no line to `session.go`'s dispatch switch: chat and party already dispatch there, and
the command channel rides on `SMSG_CHAT`. The session gained one branch in `enter` (the `CommandCheck` mode), one
field (`cmdFeed`), the options and report fields, and two call sites in the existing chat handler (`feedCommand`
before the log copy, and in `addNotice`); the chat log itself is unchanged. A branch adding handlers at the end of
the dispatch switch does not conflict with this one.

