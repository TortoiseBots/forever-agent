---
type: Reference
title: Run an add-on headless
description: Run a real add-on's Lua on a forever serve daemon with addon_start, read what it did with addon_state, tick a manual clock with addon_tick, and stop it with addon_stop.
tags: [bots-and-raids, addon, daemon, untrusted]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-25T01:45:00Z }
sources:
  - id: addon-host
    resource: internal/forever/addonhost/host.go
  - id: addon-host-lua
    resource: internal/forever/addonhost/lua/host.lua
  - id: addon-host-mock
    resource: internal/forever/addonhost/lua/mock.lua
  - id: addon-serve
    resource: internal/forever/world/serve_addon_host.go
  - id: addonmsg
    resource: internal/forever/addonmsg/addonmsg.go
---

A `forever serve` daemon can run a real add-on's Lua headless instead of only sending single
add-on messages: `addon_start` loads the add-on's own `.toc` and files into a `lua5.1` sidecar
process, registers the prefixes it asks for, and feeds it the session's events. What the add-on
asks the client to do (send an add-on message, say a chat line, target a unit) comes back to the
daemon and is performed on the same session, through the same paths as `addon_send`, `chat` and
`target`.

The page that explains the message layer itself is [the add-on protocol](addon-protocol.md); this
page is the host that runs an add-on's code.

# An add-on is untrusted

A loaded add-on is treated as untrusted code, because it is code you point the daemon at, not
code the daemon wrote. These are the rules it runs under:

- It may use the client API the mock provides and the safe half of the Lua library. It may not
  open files, run a child process, reach the network, read the RPC protocol, load further code
  (`loadstring`, `loadfile`, `dofile`, `require`, `package`, `debug` are not in its environment), or
  start a process beyond the ones it can reach through the client API it is given.
- Its chunks must be text. A source whose first byte is the escape byte `\27` is precompiled Lua
  bytecode and is refused; so is a source holding a NUL byte. Both are refused by the daemon's
  reader and again in the sidecar before its `loadstring`.
- Its files are read once, by the daemon, before the sidecar starts: each path is resolved and
  must stay beneath the add-on's folder, only regular files are read (a FIFO cannot block the
  start), one file is capped at 2 MiB and the folder at 8 MiB, and the read is bounded. A symlink
  swapped in after the check is refused, never read.
- Its sends are budgeted: 20 per call, 10 per wall-clock second and 100 queued. Past the budget
  the client refuses the send (the add-on sees a Lua error), that call answers an error, and
  `addon_state` marks the add-on `throttled`; the host stays up.
- Its sidecar is a separate process under a 256 MiB address-space limit. A call that does not
  answer within 3 s is killed, and three calls in a row over the send budget or over half of that
  3 s stop the sidecar: an add-on that spins most of every tick, or floods on every call, does not
  keep the daemon's CPU or the character's name.
- Its environment does not expose the host's shared string table (`getmetatable("")` is nil and
  `string.dump` is not reachable), so it cannot replace the functions the host frames its replies
  with.

A handler that raises is contained: the error is recorded in the host's error list and the
dispatch goes on. A broken add-on ends the add-on host, never the daemon, and the session keeps
answering `status`.

# addon_start

```json
{"method": "addon_start", "params": {"dir": "./MyAddon", "clock": "real", "prefixes": ["TINY"]}}
```

| Param | Meaning |
|---|---|
| `dir` | The add-on's folder. Its one `.toc` is found in it (a folder with no `.toc`, or with more than one, is refused by name). |
| `clock` | `real` (default) ticks `OnUpdate` every 100 ms from the daemon's own loop; `manual` moves only when you call `addon_tick`. Any other value is refused with `addon_start: clock "<value>": use real or manual`. |
| `prefixes` | Extra add-on prefixes to keep registered beside the ones the add-on registers itself. |

The answer is the host state described under `addon_state`, including everything the add-on sent
while loading. The add-on's prefixes are unioned with the list the session already holds, so the
bot module's own prefix stays registered. Starting twice is refused (`addon_start: the add-on host
is already running (addon_stop first)`), and so is starting one on a session begun with
`--addon off` (`addon_start: this session was started with the add-on off (--addon off), so it
cannot register a prefix`).

`lua5.1` must be on `PATH` (the error says `lua5.1 not found (apt install lua5.1)`), and on Linux
the sidecar must be startable under an address-space limit with `prlimit(1)` or `sh(1)`; a host
without either is refused rather than run unbounded.

# addon_state

```json
{"method": "addon_state", "params": {}}
```

Queues what arrived for the add-on since the last feed and answers the host's last state read:

| Field | What it holds |
|---|---|
| `running`, `dir`, `name`, `version`, `files`, `clock` | The host and the add-on's `.toc` identity |
| `prefixes` | The prefixes the add-on asked for, and `registered` the list the session holds now |
| `sent` | One row per thing the add-on asked the client to do, with the transport's refusal on that row's `error` (a frame over the server's 255-byte text limit is refused, never truncated) |
| `received` | The add-on frames delivered to its prefixes: prefix, channel, text, sender and the event log's sequence |
| `errors`, `chat` | The add-on's contained handler errors and what it printed to the chat frame |
| `throttled` | Present once the add-on has hit a send or per-call time budget |
| `inbox` | The session's counters: delivered, filtered for an unregistered prefix, refused as malformed, still queued |
| `dropped`, `stderr`, `error` | Events fallen out of a bounded feed, the sidecar's last stderr lines, and why a host that is not running ended |

The add-on is fed the session's event stream in order: `CHAT_MSG_ADDON` (prefix, text, the channel
word, sender), `CHAT_MSG_SYSTEM`, `CHAT_MSG_WHISPER` and `PLAYER_ENTERING_WORLD`, with the world
pushed before `PARTY_MEMBERS_CHANGED`/`RAID_ROSTER_UPDATE` on a group change.

# addon_tick

```json
{"method": "addon_tick", "params": {"ms": 250}}
```

Advances a `clock: "manual"` host by `ms` milliseconds (100 when omitted or not positive) and runs
`OnUpdate` once on every visible frame. The answer carries the add-on's new `clock` and the `sent`
rows the tick produced. A real-clock host is ticked by the daemon itself; `addon_tick` on one still
advances the clock and runs the frames.

`addon_tick`, `addon_start` and `addon_stop` do their sidecar waits on their own RPC goroutine, with
one overall 10 s bound per method; only their short session reads and writes are queued. A load or
a tick that hangs in the sidecar therefore leaves `status` answering.

# addon_stop

```json
{"method": "addon_stop", "params": {}}
```

Closes the sidecar and unregisters only the add-on's prefixes, leaving the session's other prefixes
(the bot module's, for one) registered. Stopping when nothing runs is refused with `addon_stop: the
add-on host is not running`.

# What the add-on can ask the client to do

| Client function | What the daemon does |
|---|---|
| `SendAddonMessage(prefix, text, channel, target)` | `addon_send` on `PARTY`, `RAID`, `INSTANCE_CHAT`, `GUILD`, `OFFICER` or `WHISPER`; a `CHANNEL` message is refused (this client never has the channel's GUID) |
| `SendChatMessage(text, type, language, target)` | The same path as `chat` |
| `TargetUnit(unit)` / `TargetByName(name)` | The same path as `target` |
| `RegisterAddonMessagePrefix(prefix)` | The prefix is registered with the session (1..16 bytes; over it the function answers `false`) |
| `CreateFrame`, `SetScript`, `RegisterEvent`, `HookScript`, `Show`, `Hide`, `SetText` and the rest of the widget surface | The mock's frame model; `OnUpdate` and event handlers run in creation order for visible frames |
| `GetTime`, `UnitName`, `UnitLevel`, `UnitClass`, `GetNumPartyMembers`, `GetNumRaidMembers` and the other unit reads | The world the daemon pushed: player, target, party1..4 and raid1..40 |

There is no other way out. `addon_click` stays refused: the TortoiseBotsManager button table is that
add-on's own UI, and the host runs the add-on's events and slash commands instead of clicking its
panel. The host's state is read with `addon_state`; `addon_send` remains the one-shot send for callers
that need no add-on code at all.
