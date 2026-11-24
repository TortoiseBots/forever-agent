---
type: Troubleshooting
title: Troubleshooting
description: The errors the agent prints most often, what causes them, and the fix.
tags: [getting-started, troubleshooting, errors, exit-codes]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:31:22Z }
sources:
  - id: main
    resource: cmd/forever-agent/main.go
  - id: forever-chat
    resource: cmd/forever-agent/forever_chat.go
  - id: forever-rpc
    resource: cmd/forever-agent/forever_rpc.go
  - id: slot-client
    resource: cmd/forever-agent/slot_client.go
  - id: serve
    resource: cmd/forever-agent/forever_serve.go
  - id: bnet
    resource: internal/forever/bnet/bnet.go
  - id: rpc-server
    resource: internal/rpc/server.go
  - id: config
    resource: internal/config/config.go
  - id: slots-config
    resource: internal/slots/config.go
  - id: netutil
    resource: internal/netutil/netutil.go
  - id: login-hold
    resource: cmd/forever-agent/login_hold.go
---

Work down this page by where the failure happened. Every message below is printed by the code on `main`; names,
paths and numbers are placeholders.

# Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | a failure with no more specific code: an RPC call cancelled (Ctrl-C) or past `--timeout` (no result printed), or a `forever soap` error |
| 2 | usage: a bad flag, a missing argument, a settings/slots file refused |
| 3 | the login server rejected the login |
| 4 | the realm is missing or offline |
| 5 | the world server rejected the session or the character |
| 6 | the server closed the connection |
| 7 | `scrub-check` found a secret in a capture, or a watch lost its link |
| 8 | the logout was refused (in combat) until the retries ran out |
| 9 | a fleet child failed or the fleet never reached the world |
| 10 | a logout ended without a confirmed answer |
| 11 | a fight-check character died |
| 12 | `loot-check`/`object-use` found several candidates and no `--at` |
| 13 | a preflight found a failure, or a `forever serve` stopped because its slot lease went to another holder |
| 14 | a teardown step failed |
| 15 | a slot or lease refused the operation |
| 16 | the daemon answered a typed JSON-RPC error |
| 32 | a character create/delete was sent but not answered; list the characters before retrying |

# Before the login

## `--rest, --rpc, --ca and --account are required`

One of the four connection flags is missing. All four are always required, even when a slots file would supply
the same values elsewhere: the Forever commands do not read the slots file unless they say so.

## `FOREVER_PASSWORD is not set` / `no password (FOREVER_PASSWORD is not set)`

The login commands read the password only from the environment. Export it in the shell that runs the command:

```sh
export FOREVER_PASSWORD='<password>'
```

`forever serve` is the one command that also takes `--password-file FILE` (one line, at most 4096 bytes, one
trailing newline allowed; the flag's permissions are not checked, so keep it `0600`). `password file <path> is
empty` and `password file: more than 4096 bytes` are its two content refusals. A `--settings` file can name a
`password_file` (strictly checked: `0600`, regular, not a symlink, at most 4096 bytes) or a `password` inline,
and `FOREVER_PASSWORD` is the last resort.

## `settings <path> has mode 0644; it holds passwords, run: chmod 600 <path>`

The old-stack settings file must be readable by you alone. The same rule applies to the slots file
(`slots <path> has mode 0644: it decides which account a client gets; run: chmod 600 <path>`), whose directory
must also be `0700`. Check with `ls -l`, fix with `chmod 600 <file>` and `chmod 700 <dir>`.

## `settings <path> is not valid settings JSON: unknown field "x"`

The settings loader refuses keys it does not know, so a typo in a key name fails loudly instead of being
ignored. Compare the key with the list in [Settings, slots and leases](./settings-and-slots.md).

## `forever serve: settings <path>: mode 0644; run: chmod 600 <path>` (exit 2)

The `--settings` file of a Forever serve must be a private regular file: `0644` or more, a symlink, a missing
file or one larger than 256 KiB is refused before any login. The `password_file` such a file names is held to
the same rule (and at most 4096 bytes); the `--password-file` flag is still read loosely. Malformed JSON, an
unknown key and an out-of-range value are usage errors too, each naming the field and never a value.

## `rpc must be a loopback host:port (127.0.0.1, ::1 or localhost)` / `RPC listen address must be a loopback ...`

The RPC socket is loopback only, by design. Use `127.0.0.1:8765`, `[::1]:8765` or `localhost:8765`. If you need to
reach it from another machine, forward the port over an SSH tunnel instead of binding a public address.

# The login

## `bnet: realm "<name>" accepts none of the client builds this client knows (<builds>); the realm is build <n>; pass --build N to claim a build anyway`

The realm does not accept any build this client knows. Either the server's build is newer than this client's
list, or the realm's build filter excludes them. Pass the exact build the server expects with `--build N`.

## `client build: claimed <n>, the realm list shows <m>` and then a world refusal

The realm list accepting a build does not mean the world server authenticates it. `world-check` tries the next
known build on `ERROR_BAD_VERSION`, newest first, and ends with:

```text
bnet: realm "<name>": no client build this client knows was accepted (build <a>: the world server refused it; build <b>: the realm does not accept it; ...)
```

Check the server's setup: this client expects `Network.SkipBuildAuthKeyCheck = 1`. If the server sets it to `0`,
add `--world-checks-build-auth-key`, which makes the client stop at the first `ERROR_BAD_VERSION` and name both
causes (a missing build row, or the build auth key check) instead of trying builds that cannot pass.

## `the account is not ...` / wrong password / banned

The login server's own text is passed through. A wrong password, an unknown account, a banned or suspended
account, a refused version and an authenticator step are all final: the session daemon stops reconnecting at once
instead of spending its budget (repeat failures count towards the login service's failed-login auto-ban).

## Logins are slow at the top of every UTC hour

On the old 1.12 stack, `forever-agent login` (and the tools that start it) waits out the core's login throttle
when a login starts between `hh:59:57` and `hh:03:05`, and prints `login: waiting <n> s for the realmd
hour-start window`. The wait ends at `hh:03:05` plus 0–20 s of jitter; `TA_LOGIN_HOUR_HOLD=0` turns it off. The
Forever commands (`forever login-check`, `forever world-check`, `forever serve`) do not take that hold.

# The world

## `world-check: ...` with exit 5

The login worked and the world did not. The common causes are a character name that does not exist on the
account, a character already in the world, or a world server that does not know the account's session. A
`forever world-check` on another character, or `forever char-create`, lists the account's characters.

# The session daemon

## `forever serve: <dir> is not empty and has no slot.json: pick an empty --dir or one made by slot setup --forever (serve never overwrites a foreign dir)`

`--dir` points at a directory with files in it that the daemon did not create. Point it at a new or empty
directory, or at one made by `slot setup --forever`.

## `forever serve: <dir>/slot.json is for port <a>, this serve listens on <b>: serve will not overwrite it`

The directory's manifest describes a different port (usually because another daemon already holds that port).
Start the daemon with the port from the manifest (`--listen 127.0.0.1:<a>`), or use a different directory.

## `forever serve: <dir>/slot.json is for another account or character: serve will not overwrite it`

The manifest is bound to the account and character it was set up for. Serve that pair, or make a new slot.

## `rpc: RPC listen address ...` or a bind error at startup

The listener is bound before the login, so a busy port fails immediately and nothing is logged in. Check with
`ss -ltn` which process holds the port; a leftover daemon is stopped with `teardown <dir>`, never `kill`.

## The daemon went `reconnecting` and calls answer `-32005`

The world socket dropped and the daemon is logging in again (up to 8 attempts, waits of 2 s doubling to 60 s; one
attempt bounded by 60 s, and a session that stayed up a minute starts a fresh budget). Calls answer `-32005` and
nothing is queued for after the re-entry: retry the call yourself once `status` says `online`. A `logout` or Ctrl-C
ends the daemon cleanly.

## The daemon is `offline`

The reconnect budget is spent. `status` carries the last error. The daemon stays up for `--disconnect-grace`
(default `30s`) so you can read it, then exits with a world error. In this state actions answer `-32002`.

# `forever rpc`

## `forever rpc: cannot reach the agent at 127.0.0.1:<port>: ...` (exit 15)

The daemon is not running, listens on another port, or the directory is not its slot dir. Use the dir the daemon
was started with and read the daemon's own output.

## `forever rpc: <dir>/slot.json missing or unreadable: rpc works on a dir made by slot setup` (exit 2)

`<dir>` is not a slot directory. Use the `--dir` of `forever serve` or the directory `slot setup --forever`
printed.

## `forever rpc: the token file is not an agent token` (exit 15)

The token file is not one the agent wrote (wrong length or characters), or belongs to another daemon. Restart the
daemon so it writes a fresh token.

## `error: -32006 ...` (exit 16)

The daemon refused the call on a boundary: a GM hand with `--allow-gm-units` off, or a target outside the
registered trial units. The message names the reason; see [Safety and GM guards](./safety-and-gm-guards.md).

## `forever rpc: timed out after <d>: the call was aborted and the connection is closed` (exit 1)

The call did not answer within `--timeout` (default 30 s) and was aborted. **No result was printed.**
An irreversible call may still have run on the server, so check the world state (`status`, `inv`, the unit) before
retrying it.

## `forever rpc: cancelled: the call was aborted and the connection is closed` (exit 1)

Same as above, caused by Ctrl-C or SIGTERM.

# Slots and leases

## `lease: --holder must be 1-64 letters, digits, . _ -`

The holder name is a label, not a path or an e-mail. Use something like `work`, `walk-2` or `ci-run`.

## `example/1 is held by other since 19:00:00Z until 22:00:00Z (agent pid 4242 running): pick another slot or wait`

Someone else holds the slot. Wait for the lease to expire or for its holder to release it. If the holder died on
this machine, `lease reap` removes the dead lease at once instead of waiting.

## `lease: --lease-id ...` errors

`renew` and `release` need the `--lease-id` printed by `acquire`; the holder name alone is not enough. The lead
may pass `--force` to release another holder's lease. A lease acquired again after a takeover has a new id, so
the old one answers `lease id mismatch: it was re-acquired`.

## `forever serve: lease example/1 expired at ...: renew it (...) or pass --force` (exit 15)

The slot's lease lapsed between `slot setup` and the serve. Renew it as printed, or pass `--force` to start
anyway and let the serve renew it: `--force` overrides only the clock, never another holder's live lease and
never a lease id that changed.

## `serve: lease example/1 is not ours any more (...): stopping the serve` (exit 13)

A renewal found the slot leased to another holder, or the slot re-acquired under a new id. The serve stopped
instead of running on that slot. Check `lease list example`, then `teardown` the dir and set the slot up again
with a holder that matches the lease.

## `slot setup: example/1 is not leased by work: run forever-agent lease acquire example --slot 1 --holder work first`

`slot setup` never takes a lease for you. Acquire first, then set up with the same holder name.

## `slot setup: example/1 allows Mychar, Altchar`

`--character` names a character the slot does not list. Use the slot's `character` (or add the name to its
`other_characters` in the slots file).

## `slot setup: --mmaps-dir is not used with --forever: a serve takes its navmesh folder from its own --navmesh flag, not from the slot`

Drop the flag from `slot setup` and pass `--navmesh DIR` to `forever serve` instead.

## `slots <path>: port 8765 is used by a/1 and b/2 (ports are per laptop: one per slot over all servers)`

Two slots in the file share a port. Give every slot a port of its own; the file is the registry.

## `teardown: slot.json dir "..." is not this dir "..."` (exit 2)

`slot.json` is being read from a copy or through a different path than the one it was written for. Run teardown
with the exact directory `slot setup` printed.

# Captures and privacy

## `capture: <dir> is inside the git work tree <root>; a capture is written outside a repository`

Captures contain raw server bytes and must live outside the repository.

## `scrub-check` exits 7

A finding was printed (`RUN/FILE contains an account name`, `... a session key`, `... an IP address`). Do not
share that capture; `--redact DIR` writes a masked copy and the original is never touched.

# When nothing here fits

Read the run's own files before guessing: the daemon's stdout, the run's trace and `<run_id>.forever-run.json`
summary, and the unit's `status`. The development notes under `docs/forever/` say what was proven and where a
feature's first live check must start.
