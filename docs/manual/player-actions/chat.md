---
type: Method Reference
title: Chat and channels
description: Send say, yell, party, raid, guild and whisper lines, join and leave custom channels, and read the chat log.
tags: [player-actions, chat, channels, messages]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:00Z }
sources:
  - id: serve-session
    resource: internal/forever/world/serve_session.go
  - id: channel-session
    resource: internal/forever/world/channel_session.go
  - id: session-methods
    resource: internal/forever/world/serve_session_methods.go
  - id: chat
    resource: internal/forever/chat/chat.go
---

The session can speak on every channel a normal client can, join and leave custom channels, and read back
everything it heard. The `chat` method sends one line; `chat_log` reads the log of lines the session received (and
the ones it sent, once the server echoes them).

There is no chat step in the act language: a script that talks uses the `chat` method, or the `act` method for
positional work and `chat` separately.

# Sending

| Method | Parameters | Returns |
|---|---|---|
| `chat` | `type` (required), `text` (required), `to` | empty object |

`type` names the channel:

| `type` | `to` | Meaning |
|---|---|---|
| `say` | - | Say, to everyone nearby. |
| `yell` | - | Yell. |
| `party` | - | The party. |
| `raid` | - | The raid. |
| `raid_warning` | - | The raid warning (raid leader/assistant only; the server enforces that). |
| `guild` | - | The guild. |
| `officer` | - | Guild officers. |
| `emote` | - | An emote text. |
| `whisper` | a player name | A whisper to one player. |
| `channel` | the channel name | A custom channel. |
| `instance` / `battleground` | - | The instance chat line. |

The leader-only variants (`raid_leader`, `battleground_leader`, `instance_leader`) are refused by name: the server
marks a leader's line itself, so send the plain type.

`text` is checked before anything is sent, with the server's own rules: it may not be empty, may not exceed 511
bytes, must be valid UTF-8, may not contain control characters or newlines, may not start with `/`, `.` or `!` (the
server would run it as a command), and any `|` markup must be a well-formed link (a stray `|` or a malformed
hyperlink is refused, because the server may kick on one).

A whisper takes the target's name and normalises it the way the server does (no spaces, at most 12 bytes,
printable). A channel line needs a channel this session has joined.

# Custom channels

| Method | Parameters | Returns |
|---|---|---|
| `join_channel` | `name`, `password` | `{channel, joined, already, flags}` |
| `leave_channel` | `name` | `{channel, left}` |
| `channel_list` | `name` | `{channel, flags, total, members}` |

A channel name is at most 31 characters, may not start with a digit, and may not contain `|` or control bytes.

`join_channel` waits for the server's answer. A refusal (wrong password, banned, bad name) is an error naming the
server's notice. Joining a channel this session is already on answers `joined: true, already: true`.

`leave_channel` sends the name the server gave at the join, so case does not matter; a channel the session is not
on is refused without sending anything.

`channel_list` answers the members: `[{name, self, owner, moderator, muted}]`. The list is cut at 500 rows
(`total` is the server's count). A member whose name the server has not sent yet has no `name`.

# Reading the log

| Method | Parameters | Returns |
|---|---|---|
| `chat_log` | `since`, `limit`, `wait_ms` | `{events, next, oldest, dropped, more}` |

`chat_log` reads the chat ring: `since` is the cursor to start from (0 for the beginning of what the ring still
holds), `limit` is at most 5000 (default 500), and `wait_ms` (0-25000) long-polls until at least one line arrives.
Each event is `{seq, kind: "chat", type, source, channel, text}`: `type` is the core's chat type name
(`CHAT_MSG_SAY`, `CHAT_MSG_WHISPER`, ...), `source` the sender's name, `channel` the channel a channel line came
from. `next` is the cursor for the next call, `more` says another page follows, and `dropped` means the ring lost
lines between `since` and what it still holds.

For everything else the session saw (combat, loot, party changes), see
[status and world reads](../daemon/status-and-world-reads.md) and [events and logs](../daemon/events-and-logs.md);
for the add-on message protocol see [the add-on protocol](../bots-and-raids/addon-protocol.md).

# Examples

Say something nearby:

```
forever-agent forever rpc ./slots/ACCOUNT chat '{"type": "say", "text": "pulling in 10"}'
```

Whisper a player:

```
forever-agent forever rpc ./slots/ACCOUNT chat '{"type": "whisper", "to": "Otherchar", "text": "on my way"}'
```

Join a channel, speak in it, list it, leave it:

```
forever-agent forever rpc ./slots/ACCOUNT join_channel '{"name": "worlddefense"}'
forever-agent forever rpc ./slots/ACCOUNT chat '{"type": "channel", "to": "worlddefense", "text": "kobolds at the mine"}'
forever-agent forever rpc ./slots/ACCOUNT channel_list '{"name": "worlddefense"}'
forever-agent forever rpc ./slots/ACCOUNT leave_channel '{"name": "worlddefense"}'
```

Follow the whisper log for 20 seconds:

```
forever-agent forever rpc ./slots/ACCOUNT chat_log '{"since": 0, "wait_ms": 20000}'
```

# What you see

`chat` answers `{}` once the line is on the wire. `chat_log` answers a page object; `join_channel` and
`leave_channel` the small objects above. A refused chat line (bad text, unknown type, unknown channel) is a
JSON-RPC error raised before any packet.

# Common problems

- `chat: unknown type "shout" (say, yell, party, emote, whisper, guild, officer, raid, raid_warning, battleground,
  channel)`.
- `chat: a whisper needs a target name: ...` — `to` was missing.
- `chat: the message starts with /, . or !, which the server runs as a command instead of saying it`.
- `chat: the message's '|' markup is malformed ...` — escape or remove the bar.
- `addon_send: a channel add-on message names its channel, channel:NAME` — only for `addon_send`.
- `leave_channel: the server says this character is not on [NAME]`.
- `join_channel: the server refused: wrong password [NAME]` and the other notice names.
