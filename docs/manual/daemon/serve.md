---
type: Command
title: forever serve
description: Run the agent as a session daemon so other programs can drive one logged-in character over a local JSON-RPC socket.
tags: [daemon, serve, session, rpc, login]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:07Z }
sources:
  - id: serve-cmd
    resource: cmd/forever-agent/forever_serve.go
  - id: serve-common
    resource: cmd/forever-agent/forever_chat.go
  - id: serve-world
    resource: internal/forever/world/serve.go
  - id: serve-create
    resource: cmd/forever-agent/forever_serve_create.go
  - id: rpc-server
    resource: internal/rpc/server.go
---

# What this is

`forever serve` logs one character into the world and keeps it there. It exposes that character over a
local JSON-RPC socket, so scripts, test runners, the bot module and the other `forever-agent` commands can
read the world and act on it without owning the session. It is the Forever version of the old session
daemon: same socket, same request shape, same error codes.

Use it when you need a character that stays in the world for many calls: a dungeon run, a bot client, a
test bench, a dashboard. For a single scripted session that ends by itself, use `forever world-check`
or `forever act` instead (see [getting started](../getting-started/what-is-forever-agent.md)).

# Synopsis

```
forever-agent forever serve --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME [flags]
```

`--rest`, `--rpc`, `--ca` and `--account` are required, as is `--character`. The password comes from
`FOREVER_PASSWORD` or `--password-file`, and is never printed or written.

# Flags

| Flag | Default | Meaning |
|---|---|---|
| `--rest HOST:PORT` | — (required) | Login REST endpoint of the login server. |
| `--rpc HOST:PORT` | — (required) | Battle.net RPC endpoint of the login server. |
| `--ca FILE` | — (required) | PEM file of the root CA that signed the login server's certificate. |
| `--server-name NAME` | `trinity.actual.battle.net` | Name verified in the login server's certificate. |
| `--build N` | `0` | Client build to claim. `0` picks the newest build the realm accepts and the world server accepts. |
| `--account EMAIL` | — (required) | Account e-mail or name. |
| `--game-account N` | `0` | Game account to join by the join ticket's `#N`; `0` takes the first listed. |
| `--locale NAME` | `enUS` | Client language claimed. One of `enUS koKR frFR deDE zhCN zhTW esES esMX ruRU ptBR itIT`. |
| `--character NAME` | — (required) | Character to enter the world with. |
| `--allow-instance-redirect` | off | Accept an instance redirect naming a host other than the realm socket's (clustered servers). |
| `--world-checks-build-auth-key` | off | The world server verifies the build auth key. Without it the agent assumes the setup default (the check is skipped). |
| `--password-file FILE` | — | File holding the account password (one trailing newline is removed). Without it the password comes from `FOREVER_PASSWORD`. |
| `--listen HOST:PORT` | `127.0.0.1:8765` | Loopback address the RPC socket listens on. Another host is refused; port `0` picks a free port. |
| `--dir DIR` | — | A serve slot directory: `slot.json` and `agent.pid` are written there, and the token file by default, so `forever rpc DIR ...` works. |
| `--token-file FILE` | `rpc-<port>.token` in `--dir`, else the working directory | Where the bearer token file is written. |
| `--no-auth` | off | Serve without a bearer token. Loopback, `Host` and `Origin` checks still apply. |
| `--navmesh DIR` | — | Folder of the server's mmaps files. Enables `path_to`, `walk_path` and `walk_ray`, and is needed by follow. |
| `--parent MAP=PARENT` | — | An instance map whose missing tiles come from a parent map. Repeatable. |
| `--plans-dir DIR` | `dungeons` | Folder `map_snapshot` reads a plan from. |
| `--disconnect-grace DURATION` | `30s` | How long the RPC stays up after the reconnect budget is spent, so `status` can report why. `0` to `10m`; `0` means the default 30 s. |
| `--allow-gm-units` | off | Test-bench mode: allow the GM hands (`gm_kill`, `gm_set_hp`, `gm_aura`, `gm_evade`, `gm_respawn`) on the units `trial_units` registered. |
| `--addon on\|off` | `on` | `off` never registers an add-on prefix, so the session never speaks TBM or a bot module. |
| `--capture-dir DIR` | — | Write every packet the combat, chat and party handlers see into a new run directory under this path, from the first login on. Must be outside a git work tree. |
| `--login-budget ADDR` | — | Loopback address of a fleet's shared login budget. Needs its token in `TURTLE_AGENT_LOGIN_BUDGET_TOKEN` (a fleet sets both). |
| `--create RACE,CLASS[,SEX]` | — | Create the character on login when the account lacks it. Needs `--create-choice` or `--create-no-appearance`. |
| `--create-choice OPTION:CHOICE` | — | Appearance choice for `--create`. Repeatable, sent as given. |
| `--create-no-appearance` | off | With `--create`: a test character with no appearance choices. |

`--create-choice` and `--create-no-appearance` are exclusive. A `--create` with neither is refused: such a
character is a test character's, not a real client's. Creating takes one extra login (the character screen),
so the shared login rate is spent twice.

# What it does

1. Validates its flags, then binds the RPC listener before the login, so a port conflict fails fast.
2. When `--dir` is given, checks the directory before writing anything. An empty directory (or a missing one)
   gets a fresh `slot.json`. A directory made by `slot setup --forever`, or by an earlier `serve`, is kept when
   it names the same directory, port, character and account. A mismatched, unreadable or foreign non-empty
   directory is refused; `serve` never overwrites it.
3. Logs in through the login server, selects a build, enters the world with `--character` and keeps the link
   alive (realm pings, name queries for players coming into view).
4. Serves `POST /rpc` until Ctrl-C, SIGTERM, the `logout` method, a successful `teardown`, or a lost link
   whose reconnect budget ran out.

The RPC listener's details are on [RPC basics](rpc-basics.md); the methods are listed in the
[method index](method-index.md).

# What you see

```
login: realm: REALM (id 1, address 0x00000000, world example.invalid:8085), world example.invalid:8085
rpc: listening on http://127.0.0.1:8765/rpc
rpc: token file /path/to/slot/rpc-8765.token
```

- The `login:` line names the realm, its id and address, and the world endpoint. Live-server ids and names
  are the server's; the line is printed for the operator.
- `rpc: listening on ...` is printed once the listener is bound and the login is through, before the session
  enters the world.
- With `--no-auth` the second line is `rpc: authentication off` instead of a token path.
- At a clean exit: `serve: N server packet(s) in M opcode(s)`, then `serve: logged out`.

While running, state changes are logged with an `rpc:` prefix: `rpc: the world link dropped: <cause>;
state reconnecting`, each failed attempt, `rpc: state online again (...)`, and the teardown lines.

# Exit codes

| Code | Meaning |
|---|---|
| `0` | Clean end: Ctrl-C, SIGTERM, `logout` or `teardown`. The character was logged out first. |
| `2` | Bad flags or arguments, or a problem with `--dir`, the token file, `--navmesh` or `--capture-dir`. Nothing was logged in. |
| `3` | The login server rejected the login. |
| `4` | The realm is missing or offline. |
| `5` | The world server rejected the session, or the reconnect budget was spent (the daemon is offline). |

# Stopping it

- Ctrl-C or SIGTERM: the daemon sends a full protocol logout, then exits `0` (a logout that cannot complete
  is an error exit). A second Ctrl-C ends the process at once.
- `forever rpc DIR logout`: logs the character out and ends the daemon (the call answers
  `{"outcome":"complete"}` first).
- `forever rpc DIR teardown '{"gm":"off"}'`: the step-by-step teardown (GM line, logout) that `teardown`
  and the slot tooling use. `{"dry_run":true}` plans the steps without sending anything.
- `teardown DIR`: the command-line wrapper around the same steps (see
  [settings and slots](../getting-started/settings-and-slots.md)).

# Reconnecting

A dropped world socket is not fatal. The daemon logs in again with a fresh Battle.net login (the first
login's join ticket is single use) and keeps answering reads in between.

| Limit | Value |
|---|---|
| Login attempts per outage | 8 |
| Wait before attempt n | 2 s, 4 s, 8 s, 16 s, 32 s, 60 s, 60 s, 60 s |
| One attempt's login budget | 1 minute (from the world socket to in world) |
| Stable window that resets the budget | 1 minute in the world |

A link that keeps dropping before it has stayed up for a minute spends one shared budget, so a flapping
socket ends offline instead of logging in forever. While reconnecting:

- `status` and the other read-only methods answer from the last session and say `state: "reconnecting"`,
  with the attempt number and limit.
- Every action answers error `-32005` and is not queued for after the re-entry.
- A login the server refuses for good (wrong password, unknown, banned or suspended account, a refused
  version) stops the reconnects at once.

When the budget is spent the daemon is `offline`: `status` carries the last error, actions answer `-32002`,
and the process exits after `--disconnect-grace` with exit code `5`.

# Security

- The listener is loopback only. `--listen` with any other host is refused.
- Every request needs `Authorization: Bearer <token>` unless `--no-auth` is set. The token is 32 random
  bytes as 43 base64url characters, written to the token file with mode `0600`; a world-writable token
  directory is refused.
- Requests carrying an `Origin` or `Sec-Fetch-Site` header are refused as browser requests, and so is a
  `Host` that is not loopback.
- The daemon holds a live session and its sockets. Captures written with `--capture-dir` contain real
  server traffic: keep them out of the repository.

# Examples

A slot directory that `forever rpc` can use:

```sh
export FOREVER_PASSWORD='<password>'
forever-agent forever serve \
  --rest example.invalid:8081 --rpc example.invalid:1119 \
  --ca ./ca.pem --account ACCOUNT --character Mychar \
  --listen 127.0.0.1:8765 --dir ./slot --navmesh ./mmaps
```

```sh
forever-agent forever rpc ./slot status
```

A test bench that may kill allowlisted creatures:

```sh
forever-agent forever serve ... --allow-gm-units --dir ./slot
forever-agent forever rpc ./slot trial_units '{"guids":["0x0000000000000000000000000000ABCD"]}'
forever-agent forever rpc ./slot gm_kill '{"guid":"0x0000000000000000000000000000ABCD"}'
```

A session without the add-on, for a bot module that must not be visible as one:

```sh
forever-agent forever serve ... --addon off
```

# Common problems

| Message | Cause and fix |
|---|---|
| `forever serve: --rest, --rpc, --ca and --account are required` | A required flag is missing. `--character` is checked in the same block. |
| `forever serve: no password (set FOREVER_PASSWORD or pass --password-file)` | Neither source holds a password. An empty password file is refused too. |
| `rpc: RPC listen address must be a loopback address (...)` | `--listen` names a non-loopback host. Use `127.0.0.1`, `::1` or `localhost`. |
| `<dir> is not empty and has no slot.json: pick an empty --dir or one made by slot setup --forever` | `--dir` points at a directory `serve` did not create and will not overwrite. |
| `<dir>/slot.json is for port N, this serve listens on M` | The slot was made for another port. Reuse the same `--listen`, or a fresh directory. |
| `forever serve: --navmesh <dir> is not a folder` | `--navmesh` must be an existing directory holding the mmaps files. |
| `capture: <dir> is inside the git work tree ...` | Captures must be written outside a repository. |
| `rpc: bad reply from the agent: unauthorized: token file no longer matches the agent (restarted?)` | The token file no longer matches the running daemon (a restart replaces it). Re-read the token file, or restart the client. |

# Related

- [RPC basics](rpc-basics.md): the request shape, errors and the `rpc` command.
- [Method index](method-index.md): every method with its page.
- [Status and world reads](status-and-world-reads.md), [Movement](movement.md),
  [Teleports and travel](travel.md), [Events and logs](events-and-logs.md).
- [Settings and slots](../getting-started/settings-and-slots.md), [Safety and GM guards](../getting-started/safety-and-gm-guards.md),
  [Troubleshooting](../getting-started/troubleshooting.md).
