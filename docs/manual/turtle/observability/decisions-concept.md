---
type: Concept
title: Decision captures
description: What the TortoiseBots observability daemon records and how pulls carry it home.
tags: [turtle, decisions, dashboard]
status: stable
generated: { by: forever-agent docs, at: 2026-11-05T20:46:00Z }
sources:
  - id: tbdash-client
    resource: internal/tbdash/client.go
  - id: decisions-cmd
    resource: cmd/forever-agent/decisions_cmd.go
---

This page describes the Turtle 1.12 stack's dashboard pull, which refuses a ForeverCore server. The command's Forever mode is its own read (below).

# Decision captures

> **Turtle stack, this page.** The dashboard pull described here reads the TortoiseBots observability daemon. The
> command also has a Forever mode: `decisions pull --leader NAME --out DIR` (or `--run ID`) reads one live run's
> decision rows from the bot module through the session's command channel; the module keeps a capture only while the
> run lives, so a stopped run answers `no-capture`. See
> [the decisions file](../../testing/traces-and-records.md#the-decisions-file).

The TortoiseBots observability daemon — a separate program from the turtle-agent dashboard — records what its commanders decided: one decision capture per commander per window. `internal/tbdash` is the client that talks to that daemon's JSON API; the password never leaves its file except into the login request, and never reaches an error text.

A pull fetches, per capture, `index.json` plus the `seg-NNNN.jsonl` segment files into `<out>/decisions`, with a manifest beside them. The pull stops at the budget (default 1024 MiB) and says so: `decisions: budget stopped the pull at 1024 MiB`.

Two commands pull:

- `turtle-agent decisions pull --dash-url <server> --dash-user TADASH --dash-pass-file <walk-dir>/pass --commander <slot> --out <walk-dir>` fetches one commander's window (`--since`/`--until` narrow it, both RFC 3339).
- `turtle-agent raid-walk --dash-url <server> --dash-pass-file <walk-dir>/pass [--dash-user TADASH] [--decisions on|off] [--decisions-max-mib N]` pulls the same files into the walk folder automatically (`TBDASH_URL`, `TBDASH_USER` and `TBDASH_PASS_FILE` fill the flags). Without `--dash-url` and `--dash-pass-file` it reports `off (no --dash-url/--dash-pass-file)`.

Point replays only at a test daemon: a recording of this data is what `dashboard-replay` plays.
