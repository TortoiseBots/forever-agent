---
type: Reference
title: Contracts
description: The versioned agreements between forever-agent and a bot module, where they live, and how each side proves it speaks them.
tags: [developers, contracts, bot-module, protocol]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T20:38:33Z }
sources:
  - id: adr1
    resource: docs/adr/0001-agent-talks-to-bots-through-contracts.md
  - id: tbmgo
    resource: contract/tbm/tbm.go
  - id: tbmdoc
    resource: contract/tbm/v1/CONTRACT.md
  - id: tbmmanifest
    resource: contract/tbm/v1/MANIFEST
  - id: botcmdreadme
    resource: contracts/bot-commands/v1/README.md
  - id: botcmdgolden
    resource: contracts/bot-commands/v1/golden/cases.txt
  - id: tbmproto
    resource: internal/forever/tbmproto/proto.go
  - id: botcmd
    resource: internal/forever/command/botcmd/botcmd.go
  - id: makefile
    resource: Makefile
  - id: ci
    resource: .github/workflows/contract-check.yml
---

A contract is a versioned agreement, owned by forever-agent, on how the agent and a bot system
talk: the messages, the replies, the status rows, with golden examples either side can test
against. The agent never depends on one module's internals, so that it works with the
TortoiseBots module, ForeverCore playerbots and whatever the two become
(`docs/adr/0001-agent-talks-to-bots-through-contracts.md`).

This page is for someone changing a contract or adding one. It lists what exists, how each
contract is checked, and the versioning rules that keep a module and the agent from drifting.

# The two directories

Both spellings exist, and they hold different things:

| Directory | What it is |
|---|---|
| `contract/` | The TBM add-on protocol contract: a Go package (`contract/tbm`) that embeds `v1/`, plus its rules and cases. |
| `contracts/` | The player-command contract: prose rules and golden cases for the `.bot` lines and replies. |

Both are plain files in the repository, so a module author can read them, and both are exercised
by tests in this repository. Neither is produced by a build step; a generated contract would hide
the change from review.

# TBM add-on protocol v1

`contract/tbm/v1/` holds:

| Path | What it is |
|---|---|
| `MANIFEST` | The contract's header line (`tbm-contract v1 rev 2 proto 1 min-addon 1`) and a SHA-256 for every other file. |
| `CONTRACT.md` | The rules: transport, versioning, chunking, strictness, roster rows, the first message set, the case format, the problem codes. |
| `cases/*.case` | 28 cases: what a client sends, the frames it must emit, what it receives, and the fields the reference reader must end up with. |

The Go package that embeds it is small:

```go
// contract/tbm/tbm.go
package tbmcontract

import "embed"

//go:embed v1
var V1 embed.FS

const Dir = "v1"
```

`internal/forever/tbmproto` is the reference reader and encoder. Its tests load every case and
check two things the contract insists on: a client's encoding matches the case, and a case marked
`module yes` is re-emitted byte for byte as the module sends it. `TestManifest` rebuilds the
manifest from the files and fails when it drifts, so adding a case without updating `MANIFEST` is
a test failure, not a silent change.

The contract is also replayed through the real add-on:

```sh
make tbm-contract-forever
```

That target runs `tools/tbmcontract/forever/run.sh`, which fetches TortoiseBotsForeverManager at
the pinned commit, feeds it the same cases in Lua 5.1 (the add-on's own runtime), and fails on a
divergence. The commit is in `fetch.sh`; the add-on version the cases were verified against
(`2.4.1-forever.3`) is checked by `run.sh`. It skips with a message when Lua 5.1 or the add-on is not
available, unless `TBM_CONTRACT_STRICT=1` is set. The 1.12 add-on has its own replay,
`make tbm-contract`, through `tools/tbmcontract/run.sh`.

CI runs the Go side nightly and on any pull request that touches `contract/**` or
`tools/tbmcontract/forever/**` (`.github/workflows/contract-check.yml`).

# Player commands v1 (`contracts/bot-commands/v1/`)

This contract covers the player side of the bot module: the `.bot` lines a player sends on the
command channel and the replies the module sends back as system chat. It has two files:

| Path | What it is |
|---|---|
| `README.md` | The request grammar, the reply shapes, the closed verb set, the refusal-code list, the "no module" line, and the golden-case format. |
| `golden/cases.txt` | 23 blocks. Each is one request line, the reply lines, and the parse a v1 client must make of them (`verb=… bot=… ok=… code=… text=…`, `who n=…` with rows, or `no-module`). |

`internal/forever/command/botcmd` is the client side. `Line` builds a request; `ParseReply` reads
one reply; `Classify` answers "no module" for the core's own unknown-command line. Its tests read
`golden/cases.txt` directly from the repository, so the building and the parsing are checked
against the same file the module team reads.

Two rules from the contract worth knowing before you touch it:

- A refusal never names an account or another character of the owner. The parser must not require
  more than the contract promises.
- A code outside the listed set is still a refusal with that code. A reply is never read as `ok`
  because its code looks familiar.

# Versioning

Both contracts follow the same rule:

- The directory name is the shape version (`v1`, later `v2`).
- A new case, or an appended trailing field, bumps the revision. For TBM that is the `rev` on line
  one of `MANIFEST`. The player-command contract has no `rev`: adding a case for behaviour that
  already exists is allowed, but any change to a request, reply or `who` row shape is version 2.
- A changed meaning or a changed field order is a new version directory, never an edit in place.
  For TBM it also raises `PROTO`, and the agent accepts modules whose `PROTO` is at or above its
  own with a compatible minimum add-on version.
- A `v1` file is never edited to mean something else. Add a case instead, or add `v2/`.

Changing a contract is therefore a three-part change: the files, the client code, and the pinned
replay (`tools/tbmcontract/forever/fetch.sh` and `run.sh`).

# Checklist for a contract change

1. Decide whether it is additive (same version, new case or trailing field) or a shape change
   (new version directory).
2. Add or edit the cases; keep a `module yes` case byte-for-byte what the module actually sends.
3. Update `MANIFEST` for TBM. Run the `tbmproto` tests: `TestManifest` prints the expected text
   when it drifts.
4. Run the Go contract tests, then the replay (`make tbm-contract-forever`) if the change touches
   what the add-on does.
5. Update the client code that reads the contract, and the tests beside it.
6. If the change is visible to a user of the agent, update the manual page that describes it.

# Related pages

- [Wire and packets](wire-and-packets.md) — the byte-level layer under these contracts.
- [Adding a daemon method](adding-a-daemon-method.md) — how a method that reads a contract is exposed.
- [Bot control](../bots-and-raids/bot-control.md) and [add-on protocol](../bots-and-raids/addon-protocol.md)
  — the user-facing side of the bot commands and the add-on transport.
