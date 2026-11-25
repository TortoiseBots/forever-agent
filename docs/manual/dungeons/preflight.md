---
type: Command
title: forever preflight
description: Check the serve slot, lease, plan, export, module, SOAP console and bots before a Forever dungeon run starts.
tags: [dungeons, preflight, command, checks]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T21:20:00Z }
sources:
  - id: preflight-cmd
    resource: cmd/forever-agent/forever_preflight.go
  - id: fpreflight
    resource: internal/forever/fpreflight/fpreflight.go
---

# Synopsis

```bash
forever-agent forever preflight --mode dungeon-watch|dungeon-run|dungeon-play --plan FILE --character NAME \
    [--leader NAME | --party A,B,C,D,E | --bots A,B,C,D] [--serve-dir DIR] [--export FILE|DIR] \
    [--soap-host H] [--soap-port N] [--soap-user NAME --soap-password-file FILE | --soap-password-env VAR]
```

`forever preflight` runs the checks a Forever dungeon run makes before it logs in, on their own. The three run
commands ([dungeon-run](dungeon-run.md), [dungeon-play](dungeon-play.md), [dungeon-watch](dungeon-watch.md)) call
the same code and refuse to start on a failure; `--preflight-only` runs it and stops; `--no-preflight` skips it.

It is read-only: nothing here writes a lease, a file or a server setting, and nothing logs in to the world.

# The checks

| Check | What it reads | Fails when |
|---|---|---|
| `serve` | The `slot.json` of `--serve-dir`, its pid file and its port | No `--serve-dir` (warning), no readable `slot.json`, a dir not made by `forever serve`, a pid that names no live serve of that dir, or a live pid whose port accepts no connections. |
| `lease` | The lease the slot manifest names | No lease for that server and slot, an unreadable lease file, another holder, a lease id that is not the one the manifest names (taken over). An expired lease is a warning: the serve is alive, renew it. A serve dir with no slots path is simply not leased. |
| `plan` | The plan file | The plan does not load; its error names the field. |
| `export` | `--export`: the plan's Forever-format migration file or its folder (the folder's `plan_<name>.sql`) | No `--export` (warning), an unreadable or unparsable file, no plan of that name, a different map, or no legs. |
| `module` | The serve link: one cheap read, `.tb state party` | No serve dir to open, an unopenable link (check the serve's token file), an error or an incomplete answer, or a server that does not know the line (the bot module is not loaded). |
| `console` | The SOAP console: a `.pinfo` read | No endpoint configured (warning), an endpoint that does not answer, or a character that does not exist. |
| `bots` | The same `.pinfo` read, one per name | A bot that does not exist, a bot not owned by the player's account (a play run needs its own bots; a run or a watch needs them to exist), or an unreadable answer. Offline bots are a warning: the run logs them in. |

The export check reads the **file**: no console or module command reads back which plan rows the server holds, so a
file that was never applied, or rows left from an older export, are not seen. The report says so beside the OK.

The module probe is `.tb state party` with a 5-second wait; it proves the module answers over the serve link and
that the session has the GM rank for it.

# Flags

| Flag | Default | Meaning |
|---|---|---|
| `--mode` | required | The run to check: `dungeon-watch`, `dungeon-run` or `dungeon-play`. |
| `--plan` | required | The plan file. |
| `--character` | required | The commander (watch, run) or the player character (play). |
| `--leader` | — | `dungeon-watch`: the bot that leads the module's run. |
| `--party` | — | `dungeon-run`: the five party members. |
| `--bots` | — | `dungeon-play`: the four bots. |
| `--serve-dir` | none | The `forever serve --dir` whose slot, lease and module link are checked. Without it those three checks are skipped (serve is a warning, module fails). |
| `--export` | none | The plan's exported migration from `plan-export --forever`, or its folder (which must hold `plan_<name>.sql`). |
| `--soap-host`, `--soap-port` | `FOREVER_SOAP_HOST`, `FOREVER_SOAP_PORT` or the built-in defaults | The SOAP endpoint (loopback only). |
| `--soap-user` | `FOREVER_SOAP_USER` | The admin account the endpoint authenticates; empty skips the SOAP and bots checks. |
| `--soap-password-file`, `--soap-password-env` | `FOREVER_SOAP_PASSWORD_FILE`, or the default variable | Where the SOAP password is read from. The password is never a flag and never printed. |

# Examples

```bash
forever-agent forever preflight --mode dungeon-run --plan dungeons/ragefire_chasm.json \
    --character COMMANDER --party TANK,HEALER,DPS1,DPS2,DPS3 \
    --serve-dir ./serve-slot --export ./export/plan_ragefire_chasm.sql \
    --soap-user ADMIN --soap-password-file ./soap.pass
```

The same check inside a run, without logging in:

```bash
forever-agent forever dungeon-watch --leader LEADER --plan dungeons/ragefire_chasm.json \
    --character OBSERVER --preflight-only --serve-dir ./serve-slot --export ./export
```

# What you see

One line per check, a fix note when there is one, and the totals:

```text
preflight: OK   serve   serve pid 1234 alive, port 34567 listening
preflight: OK   lease   lease held by runner until 21:40:12Z
preflight: OK   plan    plan ragefire_chasm, map 389
preflight: OK   export  plan_ragefire_chasm.sql: map 389, 9 leg(s) (the file only: the rows the server loaded are not read)
preflight: OK   module  .tb state party answered, 3 line(s)
preflight: WARN console no console configured: the server is not read -- fix: pass --soap-user and a password file
preflight: WARN bots    the bots are not checked without a console
preflight: 7 check line(s): 2 warning(s), 0 failure(s)
```

The line format is `preflight: <STATUS> <check> <detail> -- fix: <fix>`.

# Exit codes

| Code | Meaning |
|---|---|
| `0` | No failure (warnings do not fail it). |
| `13` | At least one check failed; a run command that called it starts nothing. |
| `2` | A usage error: a missing flag, an unknown `--mode`, or a missing `--leader`/`--party`/`--bots` for the mode. |

`--preflight-only` with `--dry` is refused: a dry run never opens the slot or the session the checks read. A
`dungeon-play` preflight wants the four bots owned by the account; a watch or a run wants them to exist, because the
module leads or commands them.

# Common problems

- `lease` fails while the serve is alive — the slot belongs to another run, or its lease was taken over; take the
  slot again and re-check.
- `module` fails with "does not know .tb state party" — the bot module is not loaded on that server, so no run
  command can work.
- `export` fails with a map mismatch — the migration was exported from another plan or another map; export it
  again.
- Every SOAP check warns — no `--soap-user` was given; the server-side checks (character, party, bots) did not run.
