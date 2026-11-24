# Forever add-on messages, round A0 notes

Scope: the transport a Classic client uses for add-on messages: the prefix registration packets, an add-on message
on the group kinds and the whisper (a channel message can be received and classified, but sending on a channel is
not supported yet), the receive side (how a delivered add-on message is marked, what is kept, the queue bounds),
and `forever addon-check`. The chunked protocol that a module may lay on top of these messages is **not** part of
this round; the client only guarantees arrival order per (prefix, sender).

Server facts below were read in the core at 6517af05. The client code is `internal/forever/addonmsg` (wire layouts,
limits, the classifier, the bounded inbox) and `internal/forever/world/addon_session.go` (the session side:
registration, sending, the receive feed, `addon-check`).

## What already existed

Round 5 (`CHAT-R1-NOTES.md`) parsed `SMSG_CHAT` in every form the core sends and exposed the `Prefix` field, but it
treated an add-on message as one more chat line: nothing registered a prefix, nothing sent one, and nothing
separated an add-on message from ordinary chat for a caller. This round adds exactly that separation and the client
packets.

## Client packets

All four are the core's numbers (Opcodes.h:194-195, 231, 236); the wire numbers for the build this client claims
come from the same `OpcodeTable` the other chat packets use.

| packet | core number | reader | layout |
|---|---|---|---|
| `CMSG_CHAT_REGISTER_ADDON_PREFIXES` | 0x2C0005 | `ChatPackets.cpp:306-313` | `uint32` count, then **per prefix**: a 5-bit length, a flush, and that prefix's bytes (no terminator) |
| `CMSG_CHAT_UNREGISTER_ALL_ADDON_PREFIXES` | 0x2C0006 | empty packet | no payload |
| `CMSG_CHAT_ADDON_MESSAGE` | 0x2C002A | `ChatPackets.cpp:76-91` | 5-bit prefix length, 8-bit text length, 1 `IsLogged` bit, `int32` chat type, flush, prefix, text |
| `CMSG_CHAT_ADDON_MESSAGE_TARGETED` | 0x2C002B | `ChatPackets.cpp:93-110` | the same params, then packed `ChannelGUID`, packed `PlayerGUID`, `uint32` player virtual realm, three 9/9/8-bit `SizedCString` lengths (`PlayerName`, an empty Classic second name, `ChannelName`), flush, then the strings with their NUL terminators |

Two details are the server's own: the text is read with `Strings::DontValidateUtf8` (`ChatPackets.cpp:83`), so an
add-on text is **bytes**, not necessarily UTF-8, while the prefix is read through the default UTF-8-validating
reader; and the targeted packet's second 9-bit name is the Classic extra field that `CMSG_CHAT_MESSAGE_WHISPER` also
carries, written empty (a zero length prefix, as the captured client does).

## The limits, and where they are enforced

| limit | server evidence | client side |
|---|---|---|
| prefix non-empty, at most 16 bytes | `ChatHandler.cpp:546` | `addonmsg.ValidatePrefix` refuses before any packet is built |
| prefix fits the 5-bit length field (at most 31) | `ChatPackets.cpp:79` | implied by the 16-byte rule |
| text at most 255 bytes | `ChatHandler.cpp:615` | `addonmsg.ValidateText` refuses before sending |
| text may be empty | no emptiness check on the add-on path | allowed; a channel delivery of an empty text is dropped by the channel itself (`Channel.cpp:848`) |
| at most 64 prefixes in one registration packet | `ChatPackets.h:282, 289`; a larger count resizes past the array and throws (`PacketUtilities.h:226-231`) | `addonmsg.MaxPrefixes`, checked before the packet is built |
| the server filters incoming add-on messages by the receiver's registered list | `WorldSession.cpp:1287-1293`; group `Group.cpp:970`, guild `Guild.cpp:2825`, channel `Channel.cpp:1100`, whisper `Player.cpp:23693` | the client keeps its own list and delivers only prefixes on it |
| the filter turns off once a session has more than 64 registered prefixes | `WorldSession.cpp:1309` (`_filterAddonMessages`) | irrelevant to delivery: the client's own list still decides what callers see |
| add-on flood: 100 per second by default, then a mute | `Player.cpp:23118-23121`, defaults in `World.cpp:829-831` | not paced in this round; a caller that sends in a loop must pace itself |

The server also drops an add-on message when the player may not speak (`ChatHandler.cpp:560`), and it throttles
before the length check.

## Which chat kinds the handler accepts

`HandleChatAddonMessage` (`ChatHandler.cpp:542-700`) is reached by both client packets; the targeted one only
accepts `CHAT_MSG_WHISPER` and `CHAT_MSG_CHANNEL` (`ChatHandler.cpp:526-538`). The message switch accepts:

| kind | delivery | evidence |
|---|---|---|
| `CHAT_MSG_GUILD`, `CHAT_MSG_OFFICER` | guild members with the matching listen rank right and not ignoring the sender | `ChatHandler.cpp:624-631`, `Guild.cpp:2814-2829` |
| `CHAT_MSG_WHISPER` | the one receiver, looked up by GUID when the packet names one, else by normalised name | `ChatHandler.cpp:632-660`, `Player.cpp:23688-23700` |
| `CHAT_MSG_PARTY`, `CHAT_MSG_RAID`, `CHAT_MSG_INSTANCE_CHAT` | the group (a raid to a party-only group is delivered as `PARTY`; instance chat uses the instance group) | `ChatHandler.cpp:662-686`, `Group.cpp:959-974` |
| `CHAT_MSG_CHANNEL` | the channel identified by the packet's GUID (see below; this client neither sends nor joins one) | `ChatHandler.cpp:687-694`, `ChannelMgr.cpp:178-185`, `Channel.cpp:846-891` |

Every other kind is logged and dropped (`ChatHandler.cpp:696-699`). The core also acts on a few add-on prefixes of
its own, outside this transport; this client neither uses nor claims any of them.

The sender never gets its own group message back (`Group.cpp:966` skips the sender) and never gets an inform copy of
its own add-on whisper (`Player.cpp:23694-23700` sends the receiver's packet only). A whisper to itself is the one
way to see one's own message, when the prefix is registered.

## Channel sending: not supported yet

The client packet's `ChannelGUID` is a plain `ObjectGuid` (`ChatPackets.h:118`), and the handler turns it into an
engaged optional for the channel case (`ChatHandler.cpp:687-694`), so the server only ever takes the GUID path:
`ChannelMgr::GetChannelForPlayerByGuid` compares it with the GUID of every channel the sender joined
(`ChannelMgr.cpp:178-185`). The name path, `GetChannelForPlayerByNamePart` (`ChannelMgr.cpp:149-169`), is not
reached, and a packet with an empty GUID matches no joined channel, so the message is dropped whatever name it
carries. The GUID is not unknown to a real client in principle — the join answer carries it
(`ChannelNotifyJoined.ChannelGUID`, `ChannelPackets.h:83`, filled in `Channel.cpp:231`) — but this client has no
channel join at all and so never learns one. Sending on a channel is therefore refused with
`addonmsg.ErrChannelUnsupported` ("sending on a channel is not supported yet") in the builder, in the command's
channel word and in its help text. A delivered channel message is still classified and reported.

## How an add-on message arrives

The core keeps the real chat kind and marks the line in two fields (`Chat::Initialize`, `ChatPackets.cpp:143-172`):

* `SMSG_CHAT.SlashCmd` is the chat kind the message was sent on (`PARTY`, `RAID`, `GUILD`, `OFFICER`, `WHISPER`,
  `CHANNEL`), **not** `CHAT_MSG_ADDON` (-1 / 0xFF): this core sends no packet with that type for a player message.
* `Language` is `LANG_ADDON` (183) or `LANG_ADDON_LOGGED` (184) (`SharedDefines.h:1266-1267`).
* `Prefix` carries the prefix (5-bit length field, `ChatPackets.cpp:212`), `ChatText` the text (12-bit field).

So the client's classifier is: an add-on message is a `SMSG_CHAT` whose language is 183/184 **and** whose prefix is
non-empty. Language alone is not enough: a scripted creature line can carry `LANG_ADDON` with type `CHAT_MSG_ADDON`
and no prefix (`CreatureTextMgr`), and it must stay ordinary chat. Prefix alone is not enough: every other field is
ordinary chat. A party line with `LANG_ADDON` and an empty prefix, and a say line with a prefix but `LANG_COMMON`, both
stay chat; the fake server proves both.

The packet carries no sender name for a player: `Chat::SetSender` fills `SenderGUID`, the guild GUID, the account
GUID and the chat flags, and leaves `SenderName` empty for a player (`ChatPackets.cpp:174-187`). `WhisperAddon`
does not set the names either. The sender is therefore the packet's `SenderGUID`; the client resolves a display name
from its own name cache and party roster, and prints a role word when it knows neither.

## What is delivered to callers

`addonmsg.Message` holds the prefix, the text, the channel (the client's own channel name for the chat kind), the
chat type byte, the sender GUID, any sender name the packet carried, the logged flag, and the channel name when the
kind is a channel.

The text is handed over as **the exact bytes the server sent**: the server does not require UTF-8 on this path and
a protocol needs its own field separators (tab, the unit separator) intact, so the transport repairs nothing and
filters nothing. Display is a separate concern and every printed or logged string goes through the chat display
sanitiser, which yields valid UTF-8 only, replaces the C0/C1 controls, the line and paragraph separators and the
bidi controls, shows a tab as the two characters `\t`, and caps the length. Sender names and prefixes take the same
path with their own bounds; no log line takes the raw text. The prefix is compared to the registry byte for byte.

The inbox is a single FIFO: arrival order is therefore preserved globally and so per (prefix, sender). It holds at
most `addonmsg.MaxQueue` (256) messages; when it is full the oldest is dropped and `Dropped` counts the loss.
Messages whose prefix is not on the client's own list are not delivered at all: they are counted in `Unregistered`
and the caller never sees them (the server filters them too, unless its softcap is off; the client filter is the
one that matters here). Nothing else about an add-on message is reassembled, buffered or timed in this round.

## Registration failures

Registration is two packets: unregister-all, then the full list (`WorldSession.cpp:1302-1313`). The local list is
cleared as soon as the unregister-all packet has left, so between the two packets nothing is registered; if the
second send then fails, the state becomes **unknown** rather than "cleared", because a write error does not prove
the server refused the packet: it may hold the old list, the new one, or none. In the unknown state no prefix is
reported as registered (`AddonSummary.RegistrationUnknown` marks the report, and `addon-check` prints a line saying
so), an arrived add-on line is counted in `Unregistered` and never delivered, and `SendAddon` refuses with
`addonmsg.ErrRegistrationUnknown` instead of sending a message that cannot be answered reliably. A later
unregister+register whose two sends both succeed restores the known state; the session ends otherwise. A failure of
the unregister-all packet itself changes nothing: the local list and its state stay as they were, since neither
list was touched.

## `forever addon-check`

Registers `--prefix` (the full-list form, after the unregister-all form, so a repeated run replaces the list),
sends one `--send` on `--channel`, then listens `--listen` and prints every message that arrives for the registered
prefix:

```
forever-agent forever addon-check ... --prefix PREFIX [--send TEXT] [--channel party|raid|instance|guild|officer|whisper:NAME] [--invite NAME | --accept-from NAME] [--listen DURATION] [--stay DURATION]
```

`--channel` defaults to `party`. `channel:NAME` is refused with "sending on a channel is not supported yet". The
closing line counts delivered messages by chat kind, the prefixes the server sent but the client had not
registered, the messages the queue had to drop, and the messages the report did not keep.

`--invite NAME` and `--accept-from NAME` exist for the live two-client proof: they run party-check's invite or
accept flow (without its party line, member states or stay) so the two sessions are in one group for the whole
add-on exchange, and the group is left before the logout. The prefix is registered **before** the party step, so
both sessions are registered by the time the group exists and the first party message cannot race the peer's
registration.

## Tests

`internal/forever/addonmsg` (hand-built from the core's readers): the registration bit block, the plain and the
targeted message layouts including the packed empty GUIDs and the empty Classic second name, the exact prefix/text
limits, the classifier on six language/prefix combinations, the exact-bytes rule for a delivered text (controls, a
lone 0x9B, invalid UTF-8, the separators and the bidi controls all survive), the channel refusal, the inbox's
arrival order and its drop-oldest bound, the four opcodes' wire numbers, and a fuzz seed run over `Parse`.

`internal/forever/chat`: the display sanitiser's rules, including the tab escape, the line and paragraph separators,
the bidi controls and a lone C1 byte.

`internal/forever/world/fake_addon_test.go` against the fake world server:

* registration: the unregister-all packet then the full list, parsed by the core's reader; a refused prefix or text
  never reaches the client packet layer (the fake sees no registration or no message);
* delivery: the fake's own receive filter refuses an unregistered prefix (as the core does) and a forced one is
  refused by the client; a message whose text looks like a command reaches the add-on caller, not the command feed;
  a say line carrying a registered prefix and a party line in `LANG_ADDON` with no prefix both stay ordinary chat;
  a control-byte text and a hostile one (a lone 0x9B, U+2028, invalid UTF-8, a bidi override) reach the report byte
  for byte while every printed line is a display form; add-on lines are not stored in the chat log;
* each of the six sending kinds end to end: the fake parses the client's opcode, chat type, target, prefix and text,
  and a peer message on the same kind reaches the inbox under the right channel; `channel:General` is refused before
  login and the fake sees no message (the fake drops any channel message by the server's GUID rule);
* limits: a 17-byte prefix and a 256-byte text are refused before sending, 255 bytes is accepted;
* flood: four bursts of 400 give 1600 = delivered + dropped, the report keeps its cap and counts the rest in
  arrival order, and the receive feed prints at most its line budget with a suppressed-line total at the end;
* link death: a socket closed during the listen ends the run with a named error, and the same happens on the leave
  the run sends at the end.

`internal/forever/world/addon_registration_test.go` (a transport without sockets that fails a chosen send): a
refused registration send leaves no prefix exposed as registered, counts an arrived line as unregistered instead of
delivering it, refuses `SendAddon` with `ErrRegistrationUnknown`, and a later full unregister+register restores the
list, the send and the delivery; a refused unregister-all changes nothing.

## What was proven live (2026-11-19)

Two sessions from two different accounts, both characters level 1 in the same starting area, the realm's own build.
The prefix below is the test's own placeholder. Sessions were well under two minutes; no server restart, config or
database write was made; the only console command was `account onlinelist`, which showed no online players before
each run.

**Party, prefix registered.** One session accepted the other's invite (the `--accept-from` half), the other invited
(`--invite`) and sent one party message; both were grouped (2 members) before the send:

```
addon-check: registered prefix CHK0 (unregister-all, then register)
addon-check: waiting up to 30s for an invite from <peer>
addon-check: accepted the invite from <peer>
addon-check: grouped, 2 member(s)
addon-check: listening 35s
addon: party CHK0 from <peer>: PARTY-ADDON-ONE
addon-check: 1 message(s), 0 with an unregistered prefix, 0 dropped (queue cap 256), 0 refused
```

The sender's own run saw `0 message(s)` and `left the group`: the server does not send a group add-on message back
to its sender (`Group.cpp:966`), and it delivered nothing after the group ended.

**Party, prefix not registered.** The receiver registered `CHK0`; the sender sent on `OTHR`. The receiver saw
`0 message(s)`, 0 unregistered, 0 dropped: the core's `IsAddonRegistered` filter dropped the line before it was
ever sent (`Group.cpp:970`).

**Whisper, prefix registered.** Two sessions online, no group needed. The sender sent
`--channel whisper:<peer> --send WHISPER-ADDON`; the receiver printed

```
addon: whisper CHK0 from player: WHISPER-ADDON
```

The sender's own run saw nothing: `Player::WhisperAddon` sends only the receiver's copy (`Player.cpp:23688-23700`),
never the `WHISPER_INFORM` copy a normal whisper has. The sender name is empty in the packet, so the receiver named
the sender by its object kind ("player"); the party roster or the world name cache fills the name when available.

**Whisper, prefix not registered.** The receiver registered `CHK0`, the sender used `OTHR`: the receiver saw
`0 message(s)`. No error, no not-found answer.

**Whisper to oneself.** A registered prefix and the character's own name: the server delivered exactly one copy to
the sender (`addon: whisper CHK0 from you: SELF-ADDON`) and nothing else. With the prefix not registered it is
dropped (`Player.cpp:23693`).

**Whisper to an offline name.** Nothing came back at all: no chat line, no `SMSG_CHAT_PLAYER_NOTFOUND`, and the
logout was clean. The handler looks the name up and breaks when no connected player has it
(`ChatHandler.cpp:640-660`); unlike an ordinary whisper it does not answer `SMSG_CHAT_PLAYER_NOTFOUND`
(`ChatHandler.cpp:293-320`).

**A note on the logout.** In four of the nine whisper runs (including one sender of a pair) the client's logout
ended with the instance socket closing before `SMSG_LOGOUT_RESPONSE`
(`world abort: logout not possible: connection lost ... EOF`) and exit code 5, after the add-on messages had
already arrived; the other five whisper runs logged out cleanly. The
add-on exchange itself was complete in every run. This is the session's existing logout wait treating any socket
close before the response as fatal; it is not specific to add-on messages and was not changed in this round.

## The add-on host (T9)

`internal/forever/addonhost` runs a real add-on headless, and the serving daemon exposes it as
`addon_start`/`addon_state`/`addon_tick`/`addon_stop`. `addon_send` stays what it was: the one-shot send.

**The sidecar.** The add-on's own `.toc` and Lua files run in a `lua5.1` child process on a small client mock
(`internal/forever/addonhost/lua/mock.lua`): frames and scripts, the event registry and dispatch, the chat frame,
`SendAddonMessage`/`RegisterAddonMessagePrefix`, `SendChatMessage`, `TargetUnit`/`TargetByName`, and the unit
functions over a world the daemon pushes (player, target, party1..4, raid1..40). The folder is read by the daemon,
not the child: the `.toc` and every source it names are resolved and must stay beneath the folder's own resolved
path (a symlinked `.toc` or Lua file pointing outside is refused, and so is an absolute or `..` line), then their
text goes to the child in the `load` request. The child therefore never opens a file of the add-on's own, and the
add-on's whole code is bounded (8 MiB of `.toc` and sources). The sources travel as JSON text, so each must be UTF-8:
one that is not is refused by its name instead of being run with replacement characters. Each of its files gets its own environment: only the
mocked API and the safe half of the Lua library reach it, so `io`, `os`, `loadfile`, `loadstring`, `require`,
`package` and `debug` stay in the host, and the add-on has no way to a file, the protocol or the network beyond the
mock. Requests and replies are JSON lines on stdin and file descriptor 3; nothing the add-on prints can pass for a
frame.

**Resource bounds.** The sidecar is another process, and the daemon treats it as untrusted: it is started under a
kernel address-space limit (`RLIMIT_AS`, 256 MiB, set by `prlimit(1)` or `sh`'s `ulimit -v` before `exec`; a host
with neither is refused rather than run unbounded), so an add-on that allocates without end gets Lua's
"not enough memory" and cannot take the machine's memory. A call that does not answer within `CallTimeout` (3 s)
kills the child, and that call's error says so. A handler that raises is recorded in the host's error list and the
dispatch goes on.

**One goroutine, one job.** The sidecar's calls run on the host's own goroutine, not the session loop: each turn the
loop prepares at most one job (the world pushes, the events to fire, the tick) and hands it over without waiting;
the job's answer is applied on the loop, where the `Out`s are performed on the session. A looping `OnUpdate` or
event handler therefore holds the add-on, never the session: packets keep being collected and RPCs keep answering
while it runs, and when the budget kills the sidecar only the add-on host ends (`addon_state` reports it, and the
daemon stays online). Only `addon_tick` waits for its own result, because a manual clock answers from that tick.

**Registration.** The prefixes the add-on registers with `RegisterAddonMessagePrefix` (plus any `prefixes` argument
of `addon_start`) are unioned with the list the session already holds and sent through `addonmsg`'s unregister-all +
registration pair, so the bot module's `PBC` prefix stays registered. A re-entry's fresh session has an empty list,
so the host's next feed registers again; `addon_stop` removes only the add-on's prefixes and leaves the rest.

**Frames in.** The host is driven from the session's own event log, in order: a delivered add-on message is
`CHAT_MSG_ADDON` with the client's four arguments (prefix, text, the channel word — `PARTY`, `RAID`,
`INSTANCE_CHAT`, `GUILD`, `OFFICER`, `WHISPER` — and the sender), a system notice is `CHAT_MSG_SYSTEM`, a whisper
to the character is `CHAT_MSG_WHISPER`, and an entry into the world (login, re-entry, teleport) is
`PLAYER_ENTERING_WORLD`. Where the party or raid changed (`roster`, `destroyed`, `leader`, `kicked`) the daemon
pushes the world first, so `PARTY_MEMBERS_CHANGED`/`RAID_ROSTER_UPDATE` arrive at their place in the stream. The
loop reads the log and translates its rows (so the received rows and the cursor are the loop's), and the resulting
calls are what the host's goroutine runs. A real clock ticks `OnUpdate` every 100 ms the same way;
`clock: "manual"` leaves the clock to `addon_tick`, for a test or a caller that wants a fixed sequence.

**Frames out.** `SendAddonMessage` comes back as an `Out` the daemon performs with `session.SendAddon`, so a frame
leaves through the same `addonmsg` layout and limits as `addon_send`, on `PARTY`, `RAID`, `INSTANCE_CHAT`, `GUILD`,
`OFFICER` or `WHISPER` (the target is the client's fourth argument). A `CHANNEL` message is refused: the core looks
a channel up by the GUID in the packet, which this client never has (see "Channel sending"). `SendChatMessage` and
`TargetUnit`/`TargetByName` are performed through the same session paths as `chat` and `target`.

**The 255-byte limit.** `addonmsg.Build` refuses a text over the server's 255 bytes and the refusal is kept on that
sent row's `error` (the real client has no chunking of its own; a protocol that needs long bodies chunks them
itself, as the bot module's TBM protocol does with its 200-byte parts). A frame is never truncated: nothing half a
message reaches the server.

**What the daemon answers.** `addon_start {dir, clock?, prefixes?}` loads the add-on and returns its name, `.toc`
version and files, its prefixes, and everything it sent while loading. `addon_state` queues what arrived and returns
the same plus the sent rows (with their errors), the received frames (prefix, channel, text, sender, seq), the
add-on's contained errors and chat lines as its last state read saw them (a call's answer lands a turn later, never
by holding the session), the session's registered list, and the session's own receive counters (delivered, filtered
for an unregistered prefix, refused as malformed, still queued), so a frame the add-on never saw is visible as
filtered or bad rather than silently missing. `addon_tick {ms}` advances a manual clock and returns the clock and
what the tick sent. `addon_stop` ends the child and unregisters the add-on's prefixes. `addon_click` and the TBM
button methods remain refused: they need the TortoiseBotsManager button table, which is the add-on's own UI, not a
client feature.

**Tests.** `internal/forever/addonhost/host_test.go` runs a synthetic add-on on the real interpreter: the `.toc` and
a registered prefix, the login frame, `CHAT_MSG_ADDON` arguments, a raising handler followed by a delivered frame,
a slash command's send, the clock and the visible-frame rule for `OnUpdate`, the sandbox, a stuck add-on killed by
the call timeout, an allocation bomb stopped by the sidecar's address-space limit (read back from the kernel),
`Start`'s one-`.toc` rule, a symlinked `.toc`, a symlinked Lua file, an absolute line and a `..` line all refused
as outside the folder, and a source's bytes kept exact (multi-byte text included; a non-UTF-8 source refused).
`internal/forever/world/serve_addon_host_test.go` runs the same kind of add-on against the fake world server: the
registration packets the fake parses, the party frame it receives, a delivered
guild frame reaching `CHAT_MSG_ADDON` with the add-on's guild echo going back out, a 300-byte frame refused with the
255-byte error and no packet on the wire, a handler error that leaves the daemon online and serving, a looping
handler that leaves `status` answering and `units` served while it spins (the call is killed and only the add-on
host ends), the start/stop refusals, a manual clock, and an `--addon off` session that cannot start a host.
