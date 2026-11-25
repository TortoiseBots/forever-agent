---
type: Command
title: forever serve
description: Run the agent as a session daemon so other programs can drive one logged-in character over a local JSON-RPC socket.
tags: [daemon, serve, session, rpc, login]
status: stable
generated: { by: forever-agent docs, at: 2026-11-25T02:45:00Z }
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
  - id: serve-lease
    resource: cmd/forever-agent/serve_lease.go
  - id: serve-lock
    resource: cmd/forever-agent/serve_lock_unix.go
  - id: slots-lock
    resource: internal/slots/lock_unix.go
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
forever-agent forever serve --settings FILE [flags]
```

`--rest`, `--rpc`, `--ca` and `--account` are required, as is `--character`, unless a `--settings` file supplies
them. The password comes from `--password-file`, the settings file's `password_file` or `password`, or
`FOREVER_PASSWORD`, and is never printed or written.

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
| `--password-file FILE` | — | File holding the account password (one trailing newline is removed). Without it the settings file's `password_file` or `password` is used, then `FOREVER_PASSWORD`. |
| `--settings FILE` | — | A Forever slot's settings file: fills every flag the command line left unset (account, character, rest/rpc/ca, listen, dir, navmesh, plans, password and the rest). A relative path inside it is relative to the file's folder. See [the settings file](#a-settings-file). |
| `--listen HOST:PORT` | `127.0.0.1:8765` | Loopback address the RPC socket listens on. Another host is refused; port `0` picks a free port. |
| `--dir DIR` | — | A serve slot directory: `slot.json` and `agent.pid` are written there, and the token file by default, so `forever rpc DIR ...` works. On a dir made by `slot setup --forever`, the serve holds the slot's lease. One serve per dir: a second serve on the same dir is refused (exit `15`), see [One serve per slot dir](#one-serve-per-slot-dir). |
| `--force` | off | Start even when the slot's lease has expired (the serve renews it). It overrides only the clock: never another holder's lease, and never a `slot.json` whose lease id changed. |
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

1. Reads `--settings`, if given, and fills every flag the command line left unset; a broken, loose or
   unknown-key file is exit `2` before any login.
2. Validates its flags, then binds the RPC listener before the login, so a port conflict fails fast.
3. When `--dir` is given, checks the directory before writing anything. An empty directory (or a missing one)
   gets a fresh `slot.json`. A directory made by `slot setup --forever`, or by an earlier `serve`, is kept when
   it names the same directory, port, character and account. A mismatched, unreadable or foreign non-empty
   directory is refused; `serve` never overwrites it. The dir's `serve.lock` is then taken for the whole run, so
   a second serve on the dir is refused (exit `15`) whatever it would listen on — see
   [One serve per slot dir](#one-serve-per-slot-dir).
4. When that `slot.json` names a lease, adopts it before the login: renews it once (a 15-minute TTL, with the
   serve's pid recorded), then renews it every 5 minutes. An expired lease is refused unless `--force`
   (exit `15`).
5. Logs in through the login server, selects a build, enters the world with `--character` and keeps the link
   alive (realm pings, name queries for players coming into view).
6. Serves `POST /rpc` until Ctrl-C, SIGTERM, the `logout` method, a successful `teardown`, a lost link
   whose reconnect budget ran out, or a lease renewal that found the slot taken by another holder (exit `13`).

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

# A settings file

`--settings FILE` starts a slot from one file instead of a long command line. A flag given on the command line
always wins; the file fills the flags the command line left unset. The file must be a regular, non-symlinked
`0600` file, unknown keys are refused, and a relative path inside it is relative to the file's own folder (so
`"dir": "."` is the slot dir). A broken file is exit `2` before any login, and the password is never printed.
The key table, the password precedence and every refusal are on
[Settings, slots and leases](../getting-started/settings-and-slots.md).

```sh
forever-agent forever serve --settings ./slot1/settings.json
```

# The slot lease

`slot setup --forever` records the lease it took in the slot dir's `slot.json`. A serve started with `--dir` on
that dir adopts the lease before the login and renews it every 5 minutes (the lease lasts 15 minutes), so a live
serve never loses its slot. Every renewal stamps the lease with the serve's process (pid, host, pid namespace and
start time), so a serve that died frees its slot **at once**: `lease reap` (and any `lease acquire`) can take the
slot over the moment the process is gone, and the 15-minute expiry is only the fallback for a holder on another
host. The dead serve's `slot.json` is not rewritten, so it still names the old lease id while the lease file is the
truth; a serve started from it refuses and names the way out (`is not this slot's any more`, `was re-acquired`, or
`is gone`), and `slot setup --forever` again records the current lease. Per renewal it prints:

```
serve: lease example/1 renewed until 20:45:00Z
```

- A renewal that fails for a reason that may pass (a lock timeout, an I/O error) is logged as
  `serve: lease example/1 renew failed: ...; retrying in 1m0s` and retried every minute.
- An expired lease refuses the start with exit `15`; `--force` overrides only the clock, never another
  holder's lease and never a `slot.json` whose lease id changed.
- A renewal that finds the lease taken by another holder prints
  `serve: lease example/1 is not ours any more (...): stopping the serve` and stops the serve with exit `13`.
  That holds even when the run was already ending: the command joins its lease keeper before it picks the exit
  code, so a lease lost at the same moment as a logout, a lost link or a Ctrl-C is still exit `13` and not a
  clean `0`.
- The renewal takes the lease ledger's lock (the `leases/` folder beside the slots file). That wait ends with the
  run's cancellation, so another lease command holding the ledger lock cannot keep a shutdown waiting.
- The command waits for its lease keeper at shutdown for at most 5 s. A keeper stuck in a write is named
  (`serve: the lease keeper did not stop within 5s; the dir stays locked until it does`) and the command returns;
  while it is alive the dir's `serve.lock` stays with it, so a new serve cannot start on the dir and adopt the
  lease it could still write. (The process exit releases the lock anyway.)
- The serve does not release a lease it did not take; `teardown DIR` releases a setup slot's lease.
- A bare `forever serve --dir` with no lease in its `slot.json` holds none.

The lease commands and the slot layout are on [Settings, slots and leases](../getting-started/settings-and-slots.md).

# One serve per slot dir

Two serves on one `--dir` would share its `slot.json`, `agent.pid`, token file and lease. The dir's `serve.lock`
is taken with a non-blocking exclusive `flock` for the whole run, so a second serve on the dir is refused
whatever its `--listen` host and port:

```
another forever serve is already running in /path/to/slot (pid 4242): stop it or serve another --dir
```

It exits `15`, before the lease is touched and before anything is written to the server. The kernel drops the
lock when the process ends, however it ends, so a leftover `serve.lock` never blocks a later serve (the file is
not removed; the lock, not the file, is the claim). A crash between the lock and the first `slot.json` leaves only
`serve.lock`, `agent.pid` or a token file in the dir, and a later serve takes such a dir as fresh.

The lock is `flock(2)`, so every Unix build has it, and a build without it (Windows) refuses `--dir` instead of
serving the dir unlocked:

```
one serve per --dir needs a file lock (flock), which this build does not provide: <dir> cannot be served here; serve without --dir, or run forever serve on Linux or macOS
```

That refusal is exit `15` too. A serve without `--dir` writes no manifest, pid file or lease and has no dir to
exclude, so it still runs there.

A kernel flock is only as good as the filesystem: on a network filesystem a stalled server can block the lock for
minutes. The serve survives that (the keeper's lock wait ends with the run, and its shutdown is bounded), but keep
a slot dir on a local disk when a run is timed.

# Exit codes

| Code | Meaning |
|---|---|
| `0` | Clean end: Ctrl-C, SIGTERM, `logout` or `teardown`. The character was logged out first. |
| `2` | Bad flags or arguments, or a problem with `--settings`, `--dir`, the token file, `--navmesh` or `--capture-dir`. Nothing was logged in. |
| `3` | The login server rejected the login. |
| `4` | The realm is missing or offline. |
| `5` | The world server rejected the session, or the reconnect budget was spent (the daemon is offline). |
| `13` | A lease renewal found the slot's lease taken by another holder (or re-acquired under a new id); the serve stopped rather than run on another holder's slot. Read after the lease keeper is joined, so it outranks a clean `0` or a world error when both happen at once. |
| `15` | The slot's lease or the dir refused the start: expired without `--force`, held by another holder, re-acquired, the `slot.json`'s slots file or slot is gone, another serve already holds the dir's `serve.lock`, or this build has no `flock` for a `--dir` serve. |

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
| `forever serve: no password (set FOREVER_PASSWORD, pass --password-file, or give password/password_file in --settings)` | No source holds a password. An empty password file is refused too. |
| `forever serve: settings <path>: mode 0644; run: chmod 600 <path>` | The `--settings` file, or the `password_file` it names, must be private, regular and not a symlink. |
| `forever serve: settings <path> is not valid settings JSON: unknown field "x"` | A misspelt key is refused, not ignored. Compare with the key table in [settings and slots](../getting-started/settings-and-slots.md). |
| `forever serve: lease example/1 expired at ...: renew it (... ) or pass --force` (exit 15) | The slot's lease lapsed. Renew it as printed, or pass `--force` to let this serve renew it; another holder's live lease is still never taken. |
| `serve: lease example/1 is not ours any more (...): stopping the serve` (exit 13) | A renewal found the slot leased to another holder or re-acquired under a new id. The serve stopped instead of running on that slot; adjust the slot's holder or setup again. |
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
