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
