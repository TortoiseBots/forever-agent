---
type: Guide
title: Privacy and captures
description: What must never be committed or shared from a run, what the packet captures contain, and how to check and scrub one.
tags: [developers, privacy, captures, fixtures]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:38:33Z }
sources:
  - id: capture
    resource: internal/capture/capture.go
  - id: capturedir
    resource: internal/forever/world/capture_run.go
  - id: scrub
    resource: cmd/forever-agent/forever_scrub.go
  - id: scan
    resource: .github/workflows/privacy-scan.yml
  - id: allow
    resource: .github/privacy-allow.txt
  - id: bnetredact
    resource: internal/forever/bnet/redact.go
  - id: connredact
    resource: internal/forever/worldconn/redact.go
  - id: tbdash
    resource: internal/tbdash/tbdash.go
  - id: privfile
    resource: internal/privfile/privfile.go
  - id: config
    resource: internal/config/config.go
  - id: agents
    resource: AGENTS.md
---

This repository is public. The agent logs in to real servers with real accounts, so almost
everything a live run produces is personal data: account e-mails, character names, host names and
addresses, session keys, auth tokens, and the payloads that carry them. This page says what must
never be committed or shared, how to check a capture, and which code puts a lid on a secret before
it reaches a log or a file.

The rule in one line, from `AGENTS.md`: never put account names or passwords in reports, captures,
fixtures or pull requests. Examples in documentation use placeholders (`example.invalid`,
`ACCOUNT`, `Mychar`, `./plans`).

# What must never be committed

| Category | Examples of the problem |
|---|---|
| Credentials | Account passwords, session keys, join tickets, bearer tokens, build auth keys, dashboard passwords. |
| Identities | Account e-mails or names, real character names, Battle.net account ids, guild names from a live server. |
| Locations | Real server host names and addresses, the private checkout and data paths a run used, runner and machine names. |
| Run artifacts | A live capture directory, a fixture cut from one without scrubbing, a trace or report card that quotes a name. |
| Contract files | A golden case that names a real bot or account; contract names are invented. |

Test data is not exempt. A committed capture or fixture is read by every checkout and every CI
run, so the scan below treats it exactly like source.

# What a capture holds

There are two capture writers, and they bound different things:

- The 1.12 writer, `internal/capture`, records decrypted world packets in its own format (magic
  `TWCAP1`). It never stores realmd traffic, `CMSG_AUTH_SESSION` or Warden bodies, whatever they
  contain, and it redacts registered secrets in every form a server can send them (raw, reversed,
  upper or lower case, and the hex of each).
- The Forever writer, `internal/forever/world` with `--capture-dir`, writes one new run directory
  per world link: a clock-and-random name, a `manifest.tsv` with one line per packet
  (`<file>\t0x<core opcode>\t<length>`), and one file per packet holding the payload alone.
  Nothing an earlier run wrote is overwritten; every file is created exclusively.

The Forever capture is the one to be careful with: it holds the payloads as they arrived, and a
transfer or a chat line can name a server, an account or a player. Bounds limit the size, not the
sensitivity:

- At most 8,192 packet files and 32 MiB of payload per run.
- A serving daemon shares one budget across all its reconnects: the same packet and byte bounds
  plus at most 64 run directories.
- A capture directory inside a git work tree is refused before the login, and a symlinked path
  component is refused, so a capture cannot be committed by accident or redirected through a link.
- The capture is a debug aid. When a bound is reached the run says so once and keeps running.

# Checking a Forever capture: `forever scrub-check`

```sh
forever-agent forever scrub-check \
  --slots ./slots.json \
  --account ACCOUNT \
  --password-file ./password.txt \
  --session-key-file ./session.key \
  --auth-token-file ./token \
  ./capture
```

The needles are every slot account of a server whose slots entry has a `forever` block, each
`--account` (raw and upper case), the password (from `--password-file`, else the
`FOREVER_PASSWORD` environment variable), every `--session-key-file` key (raw and decoded from hex)
and every `--auth-token-file` token. Every file of every run is read in every form the capture
package lists, and every file is also read for an IPv4 address, which is a finding with no needle
of its own.

Exit codes:

| Code | Meaning |
|---|---|
| 0 | Clean. |
| 7 | A finding: the output names the run, the file and the class, never the secret. |
| 2 | Usage, settings or secret-file error; no needle given; a symlink; a file past the writer's bounds; a run over 33 MiB. |

To share a capture, write a cleaned copy instead:

```sh
forever-agent forever scrub-check --slots ./slots.json --redact ./capture-clean ./capture
```

`--redact` takes exactly one capture directory and a destination that must not exist yet. Every
needle in every form and every address is masked by one `*` per byte, so the manifests keep their
lengths and the copy still reads. The copy is read back before the command answers; exit 0 says it
is clean, and the original is never touched.

# Where the code keeps secrets out

The code does not rely on reviewers to catch a secret in a log line:

- The Forever login and world-socket types (`internal/forever/bnet`,
  `internal/forever/worldconn`) format themselves with lengths only, so a stray `%v`, `%+v` or
  `%#v` prints `join-ticket=<redacted 512 bytes>`, not the ticket. Server-supplied text is bounded
  and long hex or base64 runs are replaced with `<redacted>` before it goes into an error.
- The settings package (`internal/config`) holds passwords and states that no value read there may
  reach a log line, an error, an event or a capture.
- `internal/tbdash` takes the dashboard password from a private file or an environment variable,
  never a flag, never prints it in an error, and zeroes the buffers once the login request is sent.
- `internal/privfile` reads owner-only files and refuses a symlink, a loose mode, or a file
  swapped between the check and the open.
- The 1.12 capture writer drops the packets that carry an account name and key-derived digests, and
  redacts the secrets it was given.

A new feature that handles a secret follows the same shape: a type that will not print it, a
private file or environment source, and a redaction before anything is written.

# Fixtures and fuzz seeds

The committed captures under test data seed the fuzz corpora (`internal/testfixture`). To turn a
live capture into a fixture:

1. Scrub it first (`--redact`), and cut the fixture from the clean copy.
2. Replace identifiers the scrubber cannot know are real: `wire.Buffer` has a GUID hook that
   receives every decoded GUID (and its offset), which is what a fixture generator uses to rewrite
   exactly those bytes and not a coincidental lookalike.
3. Read the result once more. A fixture is a permanent public file.
4. Keep the fixture small: it is meant to exercise one layout, not to archive a run.

# The repository scan

`.github/workflows/privacy-scan.yml` runs nightly on `main` and refuses the tree when it finds
private-looking values: private checkout and data paths, runner and machine names, a personal mail
domain, and addresses from the shared-address range. It reports `file:line:text` up to 200
characters and fails the job.

Two mechanics matter if you edit it or write documentation about it:

- The workflow excludes itself from the scan, and its patterns are written so the file does not
  match itself. Do not copy the pattern text into a manual page: the page would then fail the
  scan.
- `.github/privacy-allow.txt` holds fixed strings that are known to be fake (documentation ranges
  and test fixtures). A hit that matches only through an allow-list line is not reported. Add a
  line only for a value that is provably fake and say why in the review.

# Before you commit

- No capture directory, no trace, no report card, no fixture cut from a live run without scrubbing.
- No real account, character, guild, host or address in a page, a golden case or a test.
- `forever scrub-check` clean (or a clean `--redact` copy) for any Forever capture you keep.
- The nightly scan's categories in mind for anything new: paths, runner names, mail domains,
  shared addresses.

# Related pages

- [Traces and records](../testing/traces-and-records.md) — what a run writes and where.
- [Contracts](contracts.md) — why contract names are invented.
- [Contributing](contributing.md) — the pull-request checks, the scan included.
