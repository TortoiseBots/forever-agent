---
type: Reference
title: The add-on protocol
description: How the agent exchanges add-on messages with a server's bot module and how to check that exchange.
tags: [bots-and-raids, addon, protocol, bot-module]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T23:40:00Z }
sources:
  - id: addonmsg
    resource: internal/forever/addonmsg/addonmsg.go
  - id: tbm-contract
    resource: contract/tbm/v1/CONTRACT.md
  - id: tbm-proto
    resource: internal/forever/tbmproto/proto.go
  - id: tbm-client
    resource: internal/forever/tbmclient/client.go
  - id: pbc
    resource: internal/forever/pbc/pbc.go
  - id: tbm-check
    resource: cmd/forever-agent/forever_tbm.go
  - id: tbm-adapter
    resource: internal/forever/world/tbm_adapter.go
  - id: addon-check
    resource: cmd/forever-agent/forever_addon.go
---

An add-on message is a chat message the core delivers to clients that registered
a prefix, instead of showing it as speech. Bot modules use it for machine-readable
reports: bulky rosters, per-bot state, and acknowledgements that would be noise in
chat. The agent speaks two such protocols — the TBM protocol (pinned by a contract
in this repository) and the older `PBC` protocol of the Forever Bots module — and
has a command, `forever tbm-check`, that runs the TBM exchange against a live
server so you can see whether a module answers.

# The transport

- A client registers a prefix before any message arrives: up to 64 prefixes per
  registration, each at most 16 bytes. The agent registers `TBM` (and `PBC` for
  the older module) before its first command.
- The server checks an outgoing text against 255 bytes. A received add-on line can
  be longer (the chat packet's own 12-bit length, 4095 bytes), which is why a
  chunked protocol fits inside it.
- Kinds the agent sends and receives: party, raid, instance, guild, officer and
  whisper. Sending on a chat *channel* is not supported: the core looks a channel
  up by GUID and the agent never joins one, so a channel message can be received
  and classified but not sent.
- A delivered message carries the prefix, the sender and the text. The core
  delivers in order per prefix and sender, and does not consume: a party message
  also reaches the other group members, whose add-ons ignore text they do not know.
- A whisper to self comes back to the sender without the `TBM:` marker; that echo
  is ignored, never read as a reply. A party message with no group never reaches
  the add-on transport at all.
- The agent keeps a bounded inbox (256 messages), so a flooding peer cannot make it
  hold more.

# The TBM protocol, version 1

The contract lives in `contract/tbm/v1`: `CONTRACT.md` and the case files that both
the module and the agent are tested against. The directory is the shape version;
`v1` is protocol level 1. A module states which levels it speaks, and the agent
speaks level 1 and accepts a module at level 1 or newer whose minimum add-on level
is 1.

## Transports

| Name | What goes on the wire |
|---|---|
| `addon-self` | An add-on whisper to the character itself. Used for the hello while the transport is unknown. |
| `addon` | An add-on message on party chat, only after the module answered `TRANSPORT|party`. |
| `chat` | `.bot <command>` as a whisper to self, for a module pinned with `--transport chat`. |

## Hello

The agent's first message is the hello. A complete hello carries `PROTO`,
`VERSION`, `CAPS` and `TRANSPORT`; `LATEST` is the module's own version pair. A
hello missing one of the four required parts is a problem (`hello-incomplete`), and
a protocol level the agent cannot speak is `proto-incompatible`.

The answer decides the transport (party switches the client to `addon`, `none`
keeps `addon-self`), and lists what the module can do. Caps replace the whole set
each time they arrive and are lower-cased.

## Commands

A command ends at the message that answers it: `roster` and `list` at their
`TRANSPORT` line, `alts` at `ALTSUM_END`, every other command at the
`ACTION_ACK`/`ACTION_ERR` whose intent is the command's second word (for
`action`) or its first word, or the intent `-`. Anything else is a timeout.

The message set of level 1:

- `VERBS_BEGIN|<n>`, then `VERBS|a,b,...` lines, then `VERBS_END`. The count is in
  tokens; a mismatch is `stream-count` and leaves the set unset. An older module
  omits the stream, which is allowed.
- `ROSTER_BEGIN`/`ROSTER`/`ALTBOTS`/`ROSTER_END` and `ROSTER_ERROR`.
- `BOTSTATE` as a stream and as single updates.
- `ACTION_ACK` (exactly 4 fields) and `ACTION_ERR` (exactly 3 fields) as the
  command echo.

A roster row has eight fields — `guid|name|classId|state|grouped|location|ccMark|
className` — and `-` is none. Six- and seven-field rows still read: a seventh field
that holds a crowd-control mark id (moon, circle, square, cross, triangle, diamond,
star, skull) is the mark, otherwise it is the class name.

## Chunking and strictness

- A line is never split across messages; no message splits a line. Bulk data
  travels in streams (`X_BEGIN|n`, rows, `X_END`) and the begin count is
  authoritative.
- The reader splits received text on newlines and skips empty parts.
- A skipped row still counts toward the begin count. Unknown `TBM:` types and
  extra trailing fields are ignored, not errors. Anything stricter fails that
  message only.

Message senders are paced 350 ms apart, which is the module's own send delay.

## For module authors

The case files under `contract/tbm/v1/cases/` are one record per line: a `case`
name, a `desc`, `module yes` for canonical module output that must be re-emitted
byte for byte, a `send` and its `frame` (or `encode-error`), optional `recv` lines,
and `expect` paths for the finished state. The full grammar, the problem codes
(`stream-count`, `row-outside-stream`, `end-without-begin`, `nested-stream`,
`unterminated-stream`, `field-count`, `bad-number`, `bad-scope`, `empty-field`,
`bad-row`, `bad-pair`, `text-too-long`, `hello-incomplete`, `proto-incompatible`,
`empty-command`, `unsafe-arg`) and the case format are in `CONTRACT.md`.

# The PBC protocol

The older Forever Bots module answers state queries with a different protocol, on
the `PBC` prefix. One message is a line of six fields joined by `|`: the protocol
digit `1`, an eight-digit lower-case hex sequence number, the part index, the part
count, a message type in capitals, and the payload. The payload is itself a list of
`|`-joined fields, where a backslash is written twice and a pipe as `\p`.

- A body over 200 bytes is cut into parts; the parts carry the body's bytes in
  order, so an escape pair may straddle two parts. The module caps a client's part
  count at 64.
- The server answers a request on the request's own sequence number, and sends
  unsolicited notifications on numbers from `0x80000000` upward, so a caller can
  tell an answer from a push.
- The types this client uses are `PING`, `ROSTER_REQ`, `BOT_DETAIL_REQ` and
  `LFG_REQ` with their responses, plus `EVENT_PUSH`.
- The protocol is used to *read*: roster and one bot's detail. Orders go out as
  whispers or a party word (see [Ordering bots](bot-control.md)).

# Checking the exchange: `forever tbm-check`

The protocol itself is pinned by the contract's case files, which the reader and
encoder are tested against. This repository does not yet record a live run of the
exchange: `tbm-check` is the command that makes one, and its first run is what
proves a module's hello and command answers end to end.

```
forever-agent forever tbm-check [--rest host:port] [--rpc host:port] [--ca FILE]
    [--server-name NAME] [--build N] [--allow-instance-redirect]
    [--world-checks-build-auth-key] --account EMAIL --character NAME
    [--bots A,B] [--keep-bots] [--stay DURATION] [--cmd COMMAND]...
    [--transport auto|addon|addon-self|chat] [--timeout DURATION]
    [--tbm-raw FILE] [--capture-dir DIR]
```

It logs in, forms a party from `--bots` (the account's own bots, at most four), runs
the TBM hello, then each `--cmd` in order, and removes the bots it added on exit.
A module answers `no-bots` to every command while the character leads no party, so
with no party and no `--bots` the check stops at once with `no party: pass --bots`.

| Flag | Default | Meaning |
|---|---|---|
| `--bots` | none | Owned bot names to add to the party, comma separated, at most four. |
| `--keep-bots` | off | Leave those bots in the party and logged in on exit. |
| `--stay` | `4s` | How long to settle in the world before the hello. |
| `--cmd` | none | A TBM command to run after the hello (`action follow`); repeatable, run in order. |
| `--transport` | `auto` | `auto` follows the hello's answer; the others pin it. |
| `--timeout` | `5s` | How long the hello and each command may take. |
| `--tbm-raw` | none | Append every accepted message to this file as case `recv` records, for a live capture. Keep it outside the repository. |
| `--capture-dir` | none | Capture every packet the combat, chat and party handlers see into a new run directory. |

Output is identifier-free: counts, codes and flags only, and bots are printed by
slot.

```
tbm hello: complete=true transport="party" version="2.4.1" proto=1 min-addon=1 verbs=roster,version caps=[...] latest="2.4.1"/"1" check=ok
tbm "action follow": roster=0 committed=false altbots=0 botstate=3 acks=1 errs=0 problems=0 unknown=0 ignored=2
  ack[0]: intent="action" scope="party" count=5
```

A problem line (`problem[0]: stream-count`) is the protocol telling you a message
did not hold together; the command still ran, so read the problem before you
trust the counts.

For the raw transport alone, `forever addon-check` registers one prefix, optionally
sends one message on a chosen kind, and prints every message that arrives for the
prefix during `--listen` (10 s by default). Its `--prefix`, `--send`, `--channel`,
`--invite` and `--accept-from` flags are described by its own usage line; it sends
no protocol of its own.

# Common problems

| Symptom | Likely cause |
|---|---|
| The hello never answers | The server has no module, the prefix was never registered (a session started with `--addon off`), or the character leads no party. |
| `tbm hello: ... check=hello-incomplete` | The module's hello lacks `PROTO`, `VERSION`, `CAPS` or `TRANSPORT`. |
| `proto-incompatible` | The module speaks a protocol level below 1. |
| A `PBC` frame "not a frame" | The text is not the envelope; another add-on on the same prefix is the usual cause. |
| No messages at all on `--listen` | Party messages need a group, and a whisper to self echoes back without the prefix. |
