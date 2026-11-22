---
type: Guide
title: Repeat a live test with scenarios
description: Write a scenario file, check it, run it against live agents, and re-apply its normaliser.
resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/scenario_cmd.go
tags: [scenarios, guide]
sources:
  - id: scenario-cmd
    resource: https://github.com/pjerra/turtle-agent/blob/dev/cmd/turtle-agent/scenario_cmd.go
  - id: scenario-file
    resource: https://github.com/pjerra/turtle-agent/blob/dev/internal/scenario/file.go
generated:
  by: muse-spark-1.3/omp
  at: 2026-11-05T21:52:00+01:00
status: draft
---

# Repeat a live test with scenarios

A scenario is a repeatable live test: steps drive 1–8 running agents over JSON-RPC, built-in checks print PASS/FAIL, and `raw.txt` plus `norm.txt` diff cleanly before and after a deploy. The examples are `scenarios/lft-addon-path.json` and `scenarios/itemleg3-g1-items.json`.
Scenarios drive the same RPC the agent pages document ([the agent and its RPC](../agent/index.md)) and the combat they exercise is scored as in [trace checks](trace-checks-concept.md).

## 1. Write the file

Strict JSON, no comments. The top level holds `scenario` (lower-case name), top-level `server` (`turtle` or `forever`, default `turtle`: the fallback for sessions and `check_db` steps, where a step that names no server takes `forever` when every session is a Forever one; a file that mixes servers must name it on the step), `sessions` (each `{rpc, token_file, server}` with `server` `turtle` or `forever`, default the file value; RPC must be loopback), `steps`, and these optional keys: `description` (printed as a `#` header line), `db` (`{host, container, schema}` for the read-only `check_db` counts; `schema` is the ForeverCore characters database, default `characters`), `vars` (overridden by `--var`), `addon_prefix` (default `prefix` for addon-kind matchers), `settle_s` (default 1; 0–60), `timeout_s` (default 10; 1–3600), `on_fail` (`continue` or `stop`, default `continue`), `max_minutes` (default 30; 1–360), `record` (matchers saved to the transcript), `normalise` (at most 50 `match`/`replace` rules rewriting each norm line), and `finally` (at most 50 action steps: `section`, `mark`, `gm`, `bot`, `addon_send`, `rpc`, `sleep`). Each step has `do` plus its fields, and every step takes `note`; checks (`expect`, `expect_not`, `check_db`, `check_rpc`) also take `name` and `on_fail`:

| `do` | Does |
| --- | --- |
| `section` | prints `=== STEP n: <title>` and moves the match window |
| `mark` | moves the match window |
| `gm` | a `.`-command (`text`) |
| `soap` | one console-capable command on the server's SOAP endpoint (`text`), with no session; needs the `--soap-*` flags, expands variables, and sends only the SOAP setup allow-list (`.saveall`, `.pinfo`, `.instance setbossstate`) |
| `bot` | the bot module's command line (`command`); the runner sends it with the session server's own prefix (`.tb` on a Forever session, `.bot` on a turtle one) whether the file writes the bare line or either mark, and the raw transcript prints the line that went out |
| `addon_send` | an addon message (`text`, `channel`, `prefix`, `to`) |
| `rpc` | any agent method (`method`, `params`, `save`, `record_result`, `ignore_error`) |
| `sleep` | waits `seconds` (1–3600) |
| `wait` / `expect` / `expect_not` | match event lines (`session`, `kind`, `prefix`, `channel`, `source`, `match`, `capture`, `timeout_s`); `expect_not` needs `within_s` (0–600) instead of `timeout_s` (1–3600) |
| `check_db` | row counts or item counts over ssh (`table`, `where`, `rows`, `op`; or `item`, `count`); optional `within_s` (0–600), optional `server` (`turtle` or `forever`: the characters-database tables `characters`, `item_instance`, `character_inventory` at core 25fc7ca, item count over `itemEntry`/`owner_guid`). On a Forever slot the table is schema-qualified with the file's `db.schema` (default `characters`), never a hardcoded schema: `{"db":{"host":"h","container":"c","schema":"mychars"}}` reads `mychars.characters` and refuses `characters.characters` |
| `check_rpc` | `equals`, `match` or `len` over a result path; optional `within_s` (0–600) and `timeout_s` (1–600). A `gm` method gets the world-mutating guard of the `gm` step, and a Forever session retries `-32005` |

`gm`, `bot`, `addon_send` and `rpc` steps also take `settle_s` (0–60): the wait after the action before the log is drained, defaulting to the top-level `settle_s`.

On a `forever` session the start wait accepts `online` as in the world and waits up to a minute (never past `max_minutes`) for a slot that is still connecting, instead of ERRORing at once. The drain reads `event_log` (rows decoded to events with zero GUIDs) and retries `-32005` (reconnecting) every 2 s for up to 60 s; `-32002` (disconnected) always ERRORs. The cursor only moves forward: `more` with an unchanged cursor is a typed ERROR, and a cursor that goes backwards (including `next:0`) is a reset ERROR, so old events are never replayed into a PASS. `do` steps call serve methods by name: `gm` and `addon_send` take the same fields, `bot` takes the module line in `command` and adds the session's own prefix (a file's `.bot`/`.tb` mark is replaced), `rpc` names the serve method (`not ported` answers are ERRORs naming the method, never FAILs). A `rpc` step's `ignore_error` covers only the step's own refusals: `-32003` (a game refusal, not a not-ported method) and `-32006` (forbidden); `-32002`, a still-reconnecting `-32005`, and `-32601` are always ERRORs.

Matching is ordered per session: each `expect` sees only later lines, while `expect_not` scans everything since the window. A failed check is FAIL (`on_fail` `continue` or `stop`); anything unevaluatable — an RPC or DB error, an unset var, a denied GM command, the deadline — is ERROR and aborts. World-changing GM commands (AGENTS.md) are refused at load and at run time. Variables are `${name}`: file `vars` (overridden by `--var`), `${S.character}` built-ins, `capture` groups and `save` paths; `$$` is a literal `$`.

## 2. Check the file

```sh
turtle-agent scenario check scenarios/lft-addon-path.json
turtle-agent scenario check --var plan=zulfarrak scenarios/itemleg3-g1-items.json
```

`check` loads the file and prints each step (`N <do> <short form>`). `--var NAME=VALUE` overrides a file var; the name must exist. Exit 2 on any load error.

## 3. Run it against live agents

```sh
turtle-agent scenario run --out <walk-dir>/lft-1 scenarios/lft-addon-path.json
turtle-agent scenario run --var plan=zulfarrak --session C=127.0.0.1:8771@<walk-dir>/rpc-8771.token scenarios/itemleg3-g1-items.json
# two forever serve slots: the address and token come from each slot dir
forever-agent scenario run --slot A=<slot-a-dir> --slot B=<slot-b-dir> scenarios/forever-chat-party.json
```

A `--slot` may be written `NAME=DIR` for any session the file declares `"server": "forever"`, or as a bare `DIR` when the file has one session. The address is the slot `slot.json`'s `127.0.0.1:<port>` and the token file is the dir's `rpc-<port>.token`; a `--no-auth` daemon sends no header. A slot for a turtle session is refused.

Every `run` flag and its default:

| Flag | Default | What it does |
| --- | --- | --- |
| `--out` | `scenario-out/<scenario>-<UTC yyyymmddThhmmss>` | output directory; a non-empty one needs `--force` |
| `--force` | off | write into a non-empty `--out` |
| `--quiet` | off | print only PASS/FAIL/ERROR/SKIP lines and the summary |
| `--var` | none, repeatable | override a file var (the name must exist) |
| `--session` | none, repeatable | override a session: `NAME=ADDR[@TOKENFILE]` (the name must exist; the address is re-checked for loopback) |
| `--slot` | none, repeatable | a forever session's serve slot dir: `NAME=DIR` (or a bare `DIR` for a one-session file); the address and token come from that dir |
| `--soap-host` / `--soap-port` / `--soap-user` | `127.0.0.1` / `7878` / none | the console backend for `soap` steps; `--soap-user` enables it |
| `--soap-password-file` / `--soap-password-env` | none / `FOREVER_SOAP_PASSWORD` | where the console password comes from; never printed |

The run writes `raw.txt` (seq, times, expanded text), `norm.txt` (templates, no timings, DB counts kept), `transcript.jsonl` (one object per line) and `result.json`. Exit 0 when every check passed, 14 when a check failed, 15 on abort or error, 2 on usage. `scenario` uses plain flag parsing: put flags before the file name. On/off flags take `=` (`--force=true`); a space-separated value is read as the file instead.

## 4. Re-apply changed normaliser rules

```sh
turtle-agent scenario norm scenarios/lft-addon-path.json <walk-dir>/lft-1
```

`norm` re-applies the file's (possibly edited) `normalise` rules to the stored `transcript.jsonl` and rewrites `norm.txt`. The run wrote `norm.txt` with the old rules; after you fix a rule, `norm` updates the artifact without re-running the live test. DB checks run `SELECT COUNT(*)` over ssh in a read-only transaction, from an allowlist of tables, and only the count comes back.
