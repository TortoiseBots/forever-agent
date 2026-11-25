---
type: Command
title: raid-suite --forever
description: Walk a suite of Forever raid plans with forever raid-watch, each on its own leased slot, and judge each walk against its history.
tags: [bots-and-raids, raid, suite, slots, forever]
status: stable
generated: { by: forever-agent docs, at: 2026-11-25T10:00:00Z }
sources:
  - id: suite-cmd
    resource: cmd/forever-agent/raid_suite_cmd.go
  - id: compare
    resource: internal/raidwalk/compare.go
  - id: tiers
    resource: internal/raidwalk/tiers.go
  - id: slots
    resource: internal/slots
  - id: raid-watch
    resource: internal/forever/driver/raid_watch.go
---

> **Not yet proven on a live server.** `raid-suite --forever` is exercised against fakes (the slot setup and the watch
> are replaced); no live suite has been run with it yet. See [what has been proven](../getting-started/what-is-forever-agent.md#what-has-been-proven-and-what-has-not).

# Synopsis

```bash
forever-agent raid-suite --forever --suite FILE --history DIR [--slots FILE] [--binary PATH] \
    [--only p1,p2] [--gap 60s] [--out DIR] [--compare-only] [--dry-run] [--json]
```

`raid-suite --forever` walks a list of Forever raid plans one after another, each with
[`forever raid-watch`](raid-watch.md), and judges every walk against the last good walk of its plan in the history —
the same suite command and the same verdict rules as the Turtle form (see the
[raid-suite reference](../turtle/raids/raid-suite-reference.md)). It is the Forever path: the Turtle form walks with
`raid-walk`, places the party with `--sql`/`--from`, and refuses a Forever server.

# What it does

1. Reads the suite file and refuses an entry that names no `leader`, `server` or `slot`, or that carries `sql` or
   `hires`: a watch enters the slot as its own character and starts the module's run on the entry's leader, and it
   places nothing. Slot 3 is refused (owner rule), as everywhere.
2. Resolves the entry's server and slot against the slots file (the server needs its `forever` block: `rest`, `rpc`,
   `ca`) and takes the slot's lease for 30 minutes. A slot held by someone else refuses that raid; nothing is launched.
   The lease is renewed every 5 minutes while the watch runs (a watch's budget is two hours by default). One renewal
   that errors and then passes is retried: only **3** consecutive failures (`raidSuiteLeaseRenewTries`, 15 minutes at
   the default interval, inside the lease's own 30 minutes) count as a lost lease, and then the watch is cancelled —
   a run never keeps a slot it no longer holds. That raid is reported `failed` with the renewal's error, and its
   record's `exit_code` is non-zero (the error is never the watch's own `0`). The lease is released however the watch
   ends, a panic in the watch included.
3. Builds the attempt's slot dir with `slot setup --forever` (the same call the autopilot's attempt makes), then runs
   `forever raid-watch` in this process: the plan file, `--leader`, the slots file's connection flags, the slot's
   account and character, the slot dir and the attempt's folder. No `--until`, `--from`, `--place` or
   `--reuse-instance`: a watch has no resume.
4. Releases the lease when the watch ends, whatever its exit code, then reads the attempt's `summary.json` and judges it
   against the plan's history exactly as the Turtle form does.

The report's `until` for a Forever entry reads `watch`: the module's own run decides where the raid ends.

# The suite file

Format `tb-raid-suite` version 1, walked in file order. A `--forever` entry adds `leader`, `server` and `slot`:

```json
{"format":"tb-raid-suite","version":1,"raids":[
  {"plan":"<plan>","dir":"<plans-dir>","leader":"<leader-bot>","server":"<server>","slot":"2"}]}
```

`leader` is the bot that leads the module's run (`--leader` of the watch). `server` is the server's name in the slots
file, `slot` its slot id there. `--dir` cannot be used with `--forever`: a folder's plans name no leader, server or
slot.

# Flags

| Flag | Default | Meaning |
|---|---|---|
| `--forever` | off | Walk each raid as a `forever raid-watch` on the entry's leased slot. |
| `--suite` | required with `--forever` | The `tb-raid-suite` version 1 file. |
| `--history` | required | The history dir: each walk lives at `<history>/<plan>/<stamp>/`. |
| `--slots` | the standard slots file | The slots file holding the Forever servers (`forever` blocks) and their slots. |
| `--binary` | this executable | The agent binary `slot setup` copies into the slot dir. |
| `--only` | every raid | Plan names to walk, comma separated. |
| `--gap` | `60s` | The pause between two raids, 0 to 10m. |
| `--out` | `./raid-suite-<stamp>` | The report dir, refused if it exists (`suite.json`, `suite.txt`). |
| `--compare-only` | off | Walk nothing: compare each plan's newest walk in the history with the last good one before it. |
| `--dry-run` | off | Print each raid's tier, `until watch` and the folder it would get; send nothing, take no lease. |
| `--json` | off | Print the report as JSON on stdout. |

# Verdicts and exit codes

The verdicts are the Turtle suite's: `ok`, `regression`, `new` (no baseline), `failed` (the watch did not exit 0, or
wrote no summary), `refused` (a raid the slots file or the tier table cannot serve) and `skipped`. Exit 0 when no raid
regressed or failed, 24 when one did, 2 for a usage error.

# What you see

```text
onyxias_lair t2 until watch: new; out history/onyxias_lair/20261125-180000
counts: ok 0, regression 0, new 1, failed 0, refused 0, skipped 0
```

# Common problems

- `no complete 'forever' block` — the entry's server has no `rest`, `rpc` and `ca` in the slots file. Add the block:
  a Forever walk is launched on the server's own connection flags.
- `is held by <holder>` — another run holds the slot. The raid is refused, not failed; wait or pick another slot.
- `slot setup ...` — the slot dir could not be built (the binary, the account's settings file). The lease is still
  released, and the raid is `failed`.
- `no summary` — the watch ended without writing `summary.json`; the raid is `failed` with the watch's own exit code.
