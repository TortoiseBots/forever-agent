---
type: Command
title: Scenarios
description: "Write, check and run scenario files: scripted live tests that drive one or more agent sessions and check what came back."
tags: [testing, scenarios, live, checks]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:15Z }
sources:
  - id: scenario-cmd
    resource: cmd/forever-agent/scenario_cmd.go
  - id: scenario-file
    resource: internal/scenario/file.go
  - id: scenario-runner
    resource: internal/scenario/runner.go
  - id: scenario-checks
    resource: internal/scenario/checks.go
  - id: scenario-steps
    resource: internal/scenario/steps.go
  - id: scenario-sample
    resource: scenarios/forever-walk-path.json
---

A scenario is a JSON file of steps that drive one or more running agents — send a GM command, a bot command, an add-on message or an RPC call — and checks what came back: a transcript line (`expect`), the absence of one (`expect_not`), an RPC value (`check_rpc`) or a database count (`check_db`). A run prints `PASS`/`FAIL`/`ERROR`/`SKIP` per check and keeps a raw and a normalised transcript, so two runs of the same scenario can be compared line by line.

The files under `scenarios/` in the repository are ready to run; `scenarios/forever-walk-path.json` is the smallest Forever sample.

# Synopsis

```
forever-agent scenario check [--var NAME=VALUE]... <file>
forever-agent scenario run  [--out DIR] [--force] [--quiet] [--var NAME=VALUE]...
                            [--session NAME=ADDR[@TOKENFILE]]... [--slot NAME=DIR]...
                            [--soap-host H] [--soap-port N] [--soap-user NAME]
                            [--soap-password-file FILE | --soap-password-env VAR] <file>
forever-agent scenario norm <file> <dir>
```

# The file

```json
{
  "scenario": "forever-walk-path",
  "description": "path_to answers a path and walk_to lands on the point",
  "sessions": {
    "A": {"rpc": "127.0.0.1:8765", "server": "forever"}
  },
  "settle_s": 1,
  "timeout_s": 10,
  "on_fail": "continue",
  "steps": [
    {"do": "check_rpc", "session": "A", "method": "path_to",
     "params": {"x": -8893, "y": -118, "z": 3.4}, "path": "status", "equals": "complete", "within_s": 5},
    {"do": "rpc", "session": "A", "method": "walk_to", "params": {"x": -8893, "y": -118, "z": 3.4}}
  ]
}
```

Unknown keys at any level are refused, as are bad enums, so a typo fails at load, not halfway through a run.

## Top-level fields

| Field | Default | Meaning |
|---|---|---|
| `scenario` | required | the name: lower-case letters, digits and `-`/`_` |
| `description` | empty | one line |
| `server` | `turtle` | default for sessions and `check_db` steps: `turtle` or `forever` |
| `sessions` | required | 1-8 sessions, each `{rpc, token_file, server}`; `rpc` must be loopback |
| `db` | none | `{host, container, schema}` for read-only `check_db` counts (Forever counts name the core's characters schema) |
| `vars` | empty | placeholders expanded as `${NAME}`; an empty value must be set with `--var` |
| `addon_prefix` | empty | default add-on prefix for `addon_send` |
| `settle_s` | `1` | settle time after each action step (0-60) |
| `timeout_s` | `10` | default wait of a check (1-3600) |
| `on_fail` | `continue` | `continue` or `stop` |
| `max_minutes` | `30` | whole-run cap |
| `record` | empty | matchers that copy transcript lines, with optional captures |
| `normalise` | empty | regexp rewrite rules applied to the normalised transcript |
| `steps` | required | 1-500 steps |
| `finally` | empty | up to 50 steps that always run; checks are not allowed here |

A session's `token_file` is read for the RPC bearer token; with no `token_file` the run uses the default `~/.config/forever-agent/rpc-<port>.token` layout.

## Step kinds

| `do` | Fields | What it does |
|---|---|---|
| `section` | `title` | a heading in the transcript |
| `mark` | — | a clock mark in the transcript |
| `gm` | `session`, `text`, `settle_s` | send text to the agent's GM console |
| `bot` | `session`, `command`, `settle_s` | send a `.tb` module command |
| `soap` | `text`, `settle_s` | one console command over SOAP (needs the `--soap-*` flags) |
| `addon_send` | `session`, `text`, `channel`, `prefix`, `to`, `settle_s` | send an add-on message |
| `rpc` | `session`, `method`, `params`, `timeout_s`, `ignore_error`, `save`, `record_result`, `settle_s` | call one daemon method; `save` maps a var to a result path |
| `sleep` | `seconds` | wait |
| `wait` | `session`, `kind`, `prefix`, `channel`, `source`, `match`, `timeout_s`, `capture` | wait for a matching transcript line (no check result) |
| `expect` | as `wait`, plus `name`, `on_fail` | pass when the line arrives, fail on timeout |
| `expect_not` | as `expect`, plus `within_s` | pass when no matching line appears in the window |
| `check_db` | `table`, `where`, `op`, `rows`, `count`, `item`, `server`, `within_s` | read-only database count |
| `check_rpc` | `session`, `method`, `params`, `path`, `equals`, `match`, `len`, `absent`, `within_s` | poll one RPC call until the value matches or the window ends |

A step's `on_fail` overrides the file's: `continue` or `stop`. Check steps may carry a `name` used in the transcript. A `check_rpc` has no numeric tolerance: `equals` compares the value as it comes back.

# check

```
forever-agent scenario check scenarios/forever-walk-path.json
```

Loads and validates the file, then prints every step as `<n> <do> <short description>`. It touches no server. Exit `0` valid, `2` a load error (also where a bare `--var`-less check names the vars a run needs).

# run

`scenario run` loads the file, applies `--session`/`--slot` overrides, connects every session, and runs the steps. Sessions are drained of events after every step, so checks read only what happened since the previous step.

| Flag | Default | Meaning |
|---|---|---|
| `--out` | `scenario-out/<scenario>-<UTC yyyymmddThhmmss>` | output folder |
| `--force` | false | write into a non-empty `--out` |
| `--quiet` | false | print only the PASS/FAIL/ERROR/SKIP lines and the summary |
| `--var` | none | override a file var; repeatable |
| `--session` | none | `NAME=ADDR[@TOKENFILE]`; repeatable |
| `--slot` | none | `NAME=DIR` (or a bare `DIR` for a one-session file): take the address and token from a serve slot's own layout |
| `--soap-host` / `--soap-port` | the console backend's defaults | SOAP host and port; the host must be loopback |
| `--soap-user` | empty | admin account; enables `soap` steps |
| `--soap-password-file` / `--soap-password-env` | the default env var | where the console password lives; never a flag value |

`--slot` works only for a session the file declares as `"server": "forever"`. The address and token come from the slot dir, never from the file's placeholders; a slot that says `no_auth` may have no token, and a missing token is refused rather than guessed.

## What you see

```
1 check_rpc path_to {"x":-8893,...}
  PASS path_to status complete (seq 12, 0.8 s)
2 rpc walk_to {"x":-8893,...}
  PASS status x equals -8893
# summary: 2 passed, 0 failed, 0 errors, 0 skipped; exit 0
```

Anything a connection or the agent itself breaks is an `ERROR`, not a `FAIL`; a `FAIL` is a check that ran and did not hold. With `on_fail: stop` (or a step's `on_fail: stop`), the remaining main steps print `SKIP` and the `finally` steps still run.

## Output files

| File | Contents |
|---|---|
| `raw.txt` | the transcript with everything the run saw |
| `norm.txt` | the same lines after the built-in and file `normalise` rules |
| `transcript.jsonl` | one JSON record per transcript line |
| `result.json` | scenario, file, start and end, seconds, exit, passed/failed/errors/skipped counts and every check |

Every file is written through a temp file and rename, so a failed run never leaves a half-written artifact.

# norm

```
forever-agent scenario norm scenarios/forever-walk-path.json ./scenario-out/forever-walk-path-20261124T190000
```

Re-applies the file's (possibly changed) normalise rules to a stored `transcript.jsonl` and rewrites `norm.txt`. This is how an old transcript is compared after a rule changed.

# Exit codes

| Code | Command | Meaning |
|---|---|---|
| `0` | all | every check passed |
| `2` | all | usage or load error |
| `14` | `run` | a check failed |
| `15` | `run`, `norm` | the run errored (a connection, an unworkable artifact), which wins over 14 |

# Common problems

- **`scenario: vars MAP has no value; set them with --var NAME=VALUE`** — the file declares a placeholder with an empty value; pass it.
- **`--session NAME: the scenario has no session NAME`** — override names must match the file.
- **`--slot DIR: slot.json says no_auth but .../rpc-<port>.token exists: refusing to guess`** — the slot's files disagree; fix the slot rather than the scenario.
- **`scenario: ... exists and is not empty (pass --force to write into it)`** — pick a new `--out`.
- **A check fails on a line you can see in `raw.txt`** — the `expect` window starts after the previous step, and matching is on the normalised text; check `norm.txt` for what the matcher actually saw.

# Further reading

- [Report cards](report-cards.md): the card a trials run writes.
- [Trials](trials.md): the behaviour bench, which uses its own situation scripts.
- [The daemon](../daemon/rpc-basics.md): the RPC methods a `check_rpc` can call.
- [Add-on protocol](../bots-and-raids/addon-protocol.md): what `addon_send` speaks.
