---
type: Reference
title: Run an add-on headless
description: Run a real add-on's Lua on a forever serve daemon with addon_start, read what it did with addon_state, tick a manual clock with addon_tick, and stop it with addon_stop.
tags: [bots-and-raids, addon, daemon, untrusted]
status: stable
generated: { by: forever-agent docs, at: 2026-11-25T01:45:00Z }
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
- Every field of a send is checked against the server's own limits before it leaves the sidecar: a
  prefix is 1..16 bytes, an add-on text 255 bytes, a chat line 511 and a whisper target 64
  characters. An oversized field is refused like an over-budget send (the add-on sees a Lua error,
  the call answers `throttled`); the daemon keeps no row with that text. A sidecar that writes a
  protocol line over 64 KiB anyway is killed and the add-on is marked failed, and each kept `sent`
  row holds at most 512 bytes of text, cut with a `...` marker, under a total cap.
- A prefix is also checked for what it is: printable ASCII only, with no space at either end, so
  `ABC 1` is a prefix and ` ABC`, `ABC `, `A<tab>B` or a control byte in one is not. A tab would
  let a consumer that splits `prefix<TAB>text` (the vanilla convention) read one prefix as
  another. A refused prefix raises a Lua error on `SendAddonMessage`, answers `false` on
  `RegisterAddonMessagePrefix`, and never reaches the session.
- The add-on's own prefix list has a share of the server's 64: the smaller of 32 and what the
  session's reserved prefixes leave. `addon_start` takes no more, a prefix registered or sent
  under past the share is refused, and three such refusals mark the add-on `throttled`. The list
  grows only once the session took the registration, so a failed one leaves no entry. A prefix
  only sent under counts against the share exactly as a registered one: the client registers it
  with the send, and past the share the send raises a Lua error the add-on sees (not only an error
  on the row `addon_state` keeps). The prefixes `addon_start` registered up front are counted the
  same way, so `RegisterAddonMessagePrefix` answers `false` for one past the share instead of
  `true` for a prefix the session then refuses. A prefix the
  add-on registers after the load is sent to the session as a request of its own as soon as the
  call answers, so it is fed the frames that arrive under it from then on.
- Its sidecar is a separate process under a 256 MiB address-space limit. A call that does not
  answer within 3 s is killed, and three calls in a row over the send budget or over half of that
  3 s stop the sidecar. The time its calls take is also bounded over a rolling window: 3 s of
  add-on run time in any 10 s of wall clock. A handler that spins just under the per-call limit, or
  alternates a slow call with a fast one, therefore stops as well, instead of holding a core
  forever.
- The prefixes the session held when the add-on started belong to the session (the bot module's own
  protocol prefixes, for one), and the daemon's `TBM` and `PBC` prefixes are always reserved
  whether or not this session had registered them. The add-on cannot register one
  (`RegisterAddonMessagePrefix` answers `false`), cannot send under one (the send raises a Lua
  error and the row is refused), and is not fed the frames delivered under one. It receives only
  the frames of the prefixes it registered itself, and `addon_stop` unregisters only those.
- Its copies of `string.find`, `string.match`, `string.gmatch` and `string.gsub`, and the same
  functions reached as string methods (through the string metatable), refuse a pattern over 512
  bytes and a subject over 1 MiB. Lua 5.1's pattern matcher recurses on the pattern, and a long one
  can crash the interpreter; refusing it keeps the crash inside the add-on's own host.
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
| `prefixes` | Extra prefixes the add-on uses, registered up front beside the ones it registers itself while loading. Each must be 1..16 bytes of printable ASCII with no space at either end, and the list holds at most the add-on's share of the server's 64 (the smaller of 32 and what the session's reserved prefixes leave); an invalid prefix or a list over the share is refused before the sidecar starts, by name. They count as the add-on's own (it may send under them and receives their frames); the session's own prefixes are reserved, not extra, and one named here is refused rather than dropped. |

The answer is the host state described under `addon_state`, including everything the add-on sent
while loading. The add-on's prefixes (the `prefixes` above and the ones it registers) are added to
the list the session already holds; the prefixes the session held before the start are reserved
and stay the session's (see “An add-on is untrusted”). Starting twice is refused (`addon_start:
the add-on host is already running (addon_stop first)`), and so is starting one on a session begun
with `--addon off` (`addon_start: this session was started with the add-on off (--addon off), so
it cannot register a prefix`). A session whose registered prefix list is unknown — a registration
packet failed after the unregister-all, so the server may hold the old list, the new one or none —
is refused too (`addon_start: this session's registered prefix list is unknown ...`): the host
cannot tell the session's prefixes from the add-on's, and reserving only `TBM` and `PBC` would
leave the rest open to the add-on. Send once (`addon_send`) so the session registers again, then
retry.

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
| `running`, `dir`, `name`, `version`, `files`, `clock` | The host and the add-on's `.toc` identity; `files` lists at most 64 names inside 8 KiB, then a `...` marker |
| `prefixes` | The prefixes the add-on asked for (its own, never the session's reserved ones), and `registered` the list the session holds now |
| `sent` | One row per thing the add-on asked the client to do (`addon`, `chat` and `target`, or `register` for a prefix registered after the load), with the transport's refusal on that row's `error` (a field over the server's limit is refused, never truncated). At most 50 rows stay, each holding at most 512 bytes of text (cut with a `...` marker) under a total cap |
| `received` | The add-on frames delivered to the prefixes the add-on registered: prefix, channel, text, sender and the event log's sequence |
| `errors`, `chat` | The add-on's contained handler errors and what it printed to the chat frame, each line capped at 2 KiB and the lists by their encoded size (8 KiB and 12 KiB) |
| `throttled` | Present once the add-on has hit a send, field or CPU budget |
| `inbox` | The session's counters: delivered, filtered for an unregistered prefix, refused as malformed, still queued |
| `dropped`, `stderr`, `error` | Events fallen out of a bounded feed, the sidecar's last stderr lines, and why a host that is not running ended |

The add-on is fed the session's event stream in order: `CHAT_MSG_ADDON` (prefix, text, the channel
word, sender), `CHAT_MSG_SYSTEM`, `CHAT_MSG_WHISPER` and `PLAYER_ENTERING_WORLD`, with the world
pushed before `PARTY_MEMBERS_CHANGED`/`RAID_ROSTER_UPDATE` on a group change. A `CHAT_MSG_ADDON`
under a prefix the add-on did not register (the session's own reserved prefixes included) is not
fed to it.

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

Closes the sidecar and unregisters only the prefixes the add-on added, leaving the session's other
prefixes (the bot module's and every other prefix the session held before the start) registered.
Stopping when nothing runs is refused with `addon_stop: the add-on host is not running`.

# What the add-on can ask the client to do

| Client function | What the daemon does |
|---|---|
| `SendAddonMessage(prefix, text, channel, target)` | `addon_send` on `PARTY`, `RAID`, `INSTANCE_CHAT`, `GUILD`, `OFFICER` or `WHISPER`; a `CHANNEL` message is refused (this client never has the channel's GUID). The prefix must be 1..16 bytes of printable ASCII with no space at either end and is registered the first time it is sent under (within the add-on's share; over it the send is refused), the text is at most 255 and the target at most 64 characters, and a reserved or invalid prefix is refused with a Lua error |
| `SendChatMessage(text, type, language, target)` | The same path as `chat`; the text is at most 511 bytes and the target at most 64 characters |
| `TargetUnit(unit)` / `TargetByName(name)` | The same path as `target`; the unit or name is at most 64 characters |
| `RegisterAddonMessagePrefix(prefix)` | The prefix is registered with the session (1..16 bytes of printable ASCII, no space at either end, at most the add-on's share of the server's 64; over either, or a reserved prefix, the function answers `false`). A registration made while the add-on loads is part of the load's answer; one made after it is a request the daemon performs as soon as the call answers, and `addon_state` keeps it as a `register` row |
| `CreateFrame`, `SetScript`, `RegisterEvent`, `HookScript`, `Show`, `Hide`, `SetText` and the rest of the widget surface | The mock's frame model; `OnUpdate` and event handlers run in creation order for visible frames |
| `GetTime`, `UnitName`, `UnitLevel`, `UnitClass`, `GetNumPartyMembers`, `GetNumRaidMembers` and the other unit reads | The world the daemon pushed: player, target, party1..4 and raid1..40 |

There is no other way out. `addon_click` stays refused: the TortoiseBotsManager button table is that
add-on's own UI, and the host runs the add-on's events and slash commands instead of clicking its
panel. The host's state is read with `addon_state`; `addon_send` remains the one-shot send for callers
that need no add-on code at all.
