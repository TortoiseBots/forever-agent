---
type: Reference
title: Settings, slots and leases
description: The files that tell the agent which server and account to use, how slot directories are laid out, and how leases keep one worker per slot.
tags: [getting-started, settings, slots, leases, configuration]
status: stable
generated: { by: forever-agent docs, at: 2026-11-25T02:45:00Z }
sources:
  - id: config
    resource: internal/config/config.go
  - id: slots-config
    resource: internal/slots/config.go
  - id: slots-lease
    resource: internal/slots/lease.go
  - id: live-slot
    resource: internal/trials/live/slot.go
  - id: slotdir
    resource: internal/slotdir/slotdir.go
  - id: lease-cmd
    resource: cmd/forever-agent/lease_cmd.go
  - id: slot-setup
    resource: cmd/forever-agent/slot_setup_forever.go
  - id: slot-setup-common
    resource: cmd/forever-agent/slot_setup_common.go
  - id: serve
    resource: cmd/forever-agent/forever_serve.go
  - id: serve-lease
    resource: cmd/forever-agent/serve_lease.go
  - id: serve-lock
    resource: cmd/forever-agent/serve_lock_unix.go
  - id: slots-lock
    resource: internal/slots/lock_unix.go
---

There are three configuration paths in the repository. The **settings file** (`settings.json`) belongs to the
old 1.12 stack: `forever-agent login` and the Turtle-only tools read it, a Forever command does not. The **slots
file** plus **slot directories** belong to the Forever path: the slots file names servers and their slots, and a
slot's per-server `forever` block carries the connection flags the Forever tools use. A Forever serve may also be
started from its own **serve settings file**, named with `--settings FILE`: it holds one slot's account,
endpoints and serve flags and is a different file from the old `settings.json`.

If you only use Forever commands, you need a slots file and slot directories (and, if you like, a serve settings
file), not an old settings file.

# The slots file

Default path: `~/.config/forever-agent/slots.json`. The file must be mode `0600` and its directory mode `0700`;
the agent refuses to read a wider one with a message that names the fix:

```text
slots <path> has mode 0644: it decides which account a client gets; run: chmod 600 <path>
```

`version` must be `1`. Unknown top-level fields are refused by the decoder.

```json
{
  "version": 1,
  "servers": [{
    "name": "example", "realmd": "example.invalid:1119",
    "slots": [{
      "id": "1", "account": "ACCOUNT", "character": "Mychar",
      "port": 8765, "settings_from": "~/.config/forever-agent/settings.json"
    }]
  }],
  "forever": {
    "example": {
      "rest": "example.invalid:1119", "rpc": "example.invalid:1119",
      "ca": "/path/to/ca.pem", "serve_dir_root": "/path/to/serves"
    }
  }
}
```

## Server fields

| Field | Meaning |
|---|---|
| `name` | server name: lower-case letters, digits and `-`, at most 32 characters; unique in the file |
| `realmd` | `host:port` of the login server (required) |
| `realm` | optional realm name |
| `world_addr` | optional `host:port` of the world server, for tools that need it before a login |
| `mmaps_dir` | optional navmesh folder |
| `state_dir` | optional folder for fingerprints and the instance ledger |
| `slots` | one or more slots (required) |

## Slot fields

| Field | Meaning |
|---|---|
| `id` | 1–8 letters or digits; unique per server |
| `account` | the game account the slot leases |
| `character` | the slot's character: 2–12 letters |
| `other_characters` | other characters the slot allows |
| `port` | the RPC port: 1024–65535, unique across all servers in the file (one port per slot per machine) |
| `settings_from` | the settings file that holds this account's password, for the old stack and for `slot setup` |
| `reserved` | a reason string; a reserved slot is never leased and needs none of the fields above |

## The `forever` block

The `forever` object is keyed by server name. A server named there is a **Forever server**: the Turtle-only
commands (for example `login`, `raid-walk`, `map`, `capture`) refuse it before any lease, login, file write or
child process and say to pass `--forever`. Each block may carry:

| Field | Meaning |
|---|---|
| `rest` | login server REST address |
| `rpc` | login server RPC address |
| `ca` | path to the root CA PEM |
| `build` | client build to claim; `"0"` or absent = automatic |
| `serve_dir_root` | where this server's serve slot directories live (`<root>/<slot>`); the trials runner and `--forever` tools need it |
| `player` | the slot character the serve logged in (empty: read from serve status) |
| `bot_pool` | persistent hire pool by class, as bot names |
| `arena_state_dir` | where the trials runner keeps its arena locks |
| `scratch_bot` | the character the spell-data check learns spells on |
| `soap` | `{host, port, user, password_file|password_env}` for the server console |
| `soap_setup` | console commands the trials runner sends before provisioning (allow-list only) |

`slot setup --forever` requires `rest`, `rpc` and `ca` in the block and refuses a server without them.

# Slot directories

A slot directory is the working directory of one session. `slot setup --forever` builds it; `forever serve
--dir DIR` runs from it. It holds:

| File | Written by | What it is |
|---|---|---|
| `forever-agent` | `slot setup` | the copied binary the slot runs |
| `slot.json` | `slot setup`, or `serve --dir` when the dir was empty | the manifest the RPC client and `teardown` read; mode 0600, no secrets |
| `serve.lock` | `forever serve` | the one-serve-per-dir claim: a kernel `flock` on this file, held for the whole run and never removed (the lock, not the file, is the claim) |
| `agent.pid` | `forever serve` | the daemon's pid; removed at exit; pairs with `pid_start` |
| `rpc-<port>.token` | `forever serve` | the bearer token for the RPC socket; removed at exit; absent with `--no-auth` |
| `dungeons/` | `slot setup` | the plans folder; empty unless `--dungeons-from` named a folder to copy `*.json` plans from |

`slot.json` fields (all checkable path fields are assertions: they must be the paths derived inside the dir,
never symlinks, never outside):

| Field | Meaning |
|---|---|
| `server`, `slot`, `holder`, `lease_id` | the slot identity and the lease it was made under |
| `account`, `character`, `port` | what the daemon logs in and where it listens |
| `dir`, `binary`, `settings`, `pid_file`, `token_file` | the canonical directory and the paths derived inside it |
| `slots_path` | the slots file that named the server |
| `created` | UTC timestamp of the manifest write |
| `no_auth` | set by `serve --no-auth`: no token file exists, so clients send no header |
| `pid_start` | the daemon's process start time (Linux), so a reused pid is told apart from the daemon |

A `slot.json` that does not belong to the directory, or a directory that is not empty and has no `slot.json`,
is refused: `forever serve` never overwrites a foreign directory (see [Troubleshooting](./troubleshooting.md)).

# Leases

A lease is how two workers avoid taking the same slot. Every lease lives in a ledger under the slots file's
directory (`leases/<server>/`), so several machines sharing a slots file see each other's leases. Holder names
are 1–64 letters, digits, `.`, `_` or `-`.

## Acquire

```sh
bin/forever-agent lease acquire example --slot 1 --holder work --ttl 180
```

* `--slot ID` or `--any` (take the first free slot): exactly one is required;
* `--holder NAME` (required);
* `--ttl MIN`: 1–720, default 180;
* `--slots FILE`: default `~/.config/forever-agent/slots.json`.

```text
leased example/1 to work until 20:45:00Z: account ACCOUNT, character Mychar, port 8765, lease 3f9c...
```

A stale lease (expired, its local holder process gone) is taken over and reported first:
`took over a stale lease of example/1 from old-holder (expired 19:00:00Z, no agent running)`.

Refusals exit 15 and change nothing:

| Message | Cause |
|---|---|
| `example/1 is reserved (owner rule: never slot 1)` | the slot carries `reserved` |
| `example/1 is already leased by work (already yours: renew, do not re-acquire)` | re-acquiring your own live lease |
| `example/1 is held by other since 19:00:00Z until 22:00:00Z (agent pid 4242 running): pick another slot or wait` | another holder has it; the tail is `(no agent running; it can be taken over after the expiry)` when nothing runs in its dir |
| `example/1: port 8765 has a listener on 127.0.0.1 (an agent outside the leases?): stop it or pick another slot` | something already listens on the slot's port |

## Renew, release, list, reap

```sh
bin/forever-agent lease renew example 1 --holder work --lease-id 3f9c... --ttl 180
bin/forever-agent lease release example 1 --holder work --lease-id 3f9c...
bin/forever-agent lease release example 1 --holder work --force
bin/forever-agent lease list example
bin/forever-agent lease list --json
bin/forever-agent lease reap --dry-run
```

* `renew` needs `--lease-id` (the holder alone is not enough) and prints
  `renewed example/1 for work until 20:45:00Z`;
* `release` needs `--lease-id` unless `--force` is passed, prints `released example/1 (was work)` (+ ` (forced)`),
  and refuses while the lease's agent runs: `run forever-agent teardown <dir> first`;
* `list` prints one row per slot (server, id, character, port, state: `free`, `reserved: ...`, `stale: ...`, or
  `held by ... since ..., expires ...`); `--json` omits the account name;
* `reap` removes leases of holders that died on this host (pid gone or reused); `--dry-run` prints
  `would reap ...`. Another host's lease is left to its expiry.

## A serve holds its lease

`slot setup --forever` records the lease it was made under in the slot dir's `slot.json` (`server`, `slot`,
`holder`, `lease_id`, `slots_path`). `forever serve --dir` now holds that lease for the whole run, on the old
agent's heartbeat rule:

* it renews the lease **at start** and then **every 5 minutes**; the lease is **15 minutes** long, so a live
  serve never loses it. Every renewal stamps the lease with the serve's process, so a dead serve frees its slot
  **at once**: `lease reap` (and any `lease acquire`) can take it the moment the process is gone, and the expiry
  is only the fallback for a holder on another host. The dead serve's `slot.json` is not rewritten and still
  names its lease id, which is intended: the manifest is a setup record, the lease file is the truth, and the
  next serve refuses with the way out (`is not this slot's any more`, `was re-acquired` or `is gone`) until
  `slot setup --forever` records the current lease;
* a renewal that fails for a reason that may pass (a lock timeout, an I/O error) is logged
  `serve: lease <server>/<slot> renew failed: ...; retrying in <duration>` and retried every minute — never
  silently dropped;
* an **expired lease refuses the start** (exit `15`) with a line naming the slot and its expiry and the two ways
  out: `renew it (forever-agent lease renew ...) or pass --force`. `--force` overrides only the clock: it never
  takes a live lease from another holder, and a `slot.json` whose lease id changed (the slot was re-acquired or
  released) is refused with or without it;
* a renewal that finds the lease **taken by another holder** prints
  `serve: lease <server>/<slot> is not ours any more (...): stopping the serve` and the serve stops cleanly with
  exit `13` — it never keeps running on another holder's slot. The code is read after the serve has joined its
  lease keeper, so a lease lost as the run also ends (a logout, a Ctrl-C) is still `13`;
* the lease ledger's own lock can never hold a shutdown: the renewal's wait for it ends with the run, and the
  wait for the keeper is bounded at 5 s (`serve: the lease keeper did not stop within 5s; the dir stays locked
  until it does`), with the dir's `serve.lock` staying with a keeper that is still alive;
* a bare `forever serve --dir` (no `slot setup`, no lease in its `slot.json`) holds no lease, as before;
  `teardown` remains the command that ends the game presence and releases the lease.

One serve per slot dir: the dir's `slot.json`, token file, pid file and lease are one serve's, so a second
`forever serve` on the same `--dir` is refused with exit `15` whatever its listen address, by the `serve.lock`
flock in the dir (see [forever serve](../daemon/serve.md#one-serve-per-slot-dir)).

`trials run` takes its own lease on the slot (holder `trials` by default) before it opens the serve dir, so a
`trials run` against a slot a leased serve holds is refused `held by <holder>` (exit `15`). Run the trials
against a bare `forever serve --dir`, or a slot no serve leases, until the two designs are reconciled.

# Setting up a slot

A Forever slot is set up in two steps: lease it, then build its directory.

```sh
bin/forever-agent lease acquire example --slot 1 --holder work
bin/forever-agent slot setup --forever example 1 --holder work --binary bin/forever-agent
```

`slot setup --forever`:

* takes the lease's identity (`slot setup` refuses a slot you do not hold: `is not leased by work: run
  forever-agent lease acquire ... first`);
* claims `--dir` (default `~/ta-<holder>`): new or empty, created mode `0700`, never overwritten, never written
  through a symlink; copies `--binary` (default: the running binary) into it and creates `dungeons/` (plans
  copied from `--dungeons-from` when given);
* writes no `settings.json` (its path is recorded in `slot.json`, but no file is written there) and prints the
  start line:

```text
slot example/1 ready in /path/to/ta-work
account ACCOUNT, character Mychar, port 8765
serve: /path/to/ta-work/forever-agent forever serve --rest example.invalid:1119 --rpc example.invalid:1119 --ca /path/to/ca.pem --account ACCOUNT --character Mychar --password-file <password-file> --listen 127.0.0.1:8765 --dir /path/to/ta-work
```

`--mmaps-dir` is refused with `--forever`: a serve takes its navmesh folder from its own `--navmesh` flag, not
from the slot.

# The Forever settings file (`forever serve --settings`)

A Forever slot can be started from one settings file instead of a long command line:

```text
forever-agent forever serve --settings /srv/forever/slot1/settings.json
```

A flag given on the command line always wins; the file fills every flag the command line left unset; a flag
neither names keeps its default. `--create`, `--create-choice`, `--create-no-appearance` and `--login-budget`
have no key and stay on the command line (a one-off character or a fleet child's wiring).

```json
{
  "account": "slot1@example.invalid",
  "character": "Slotchar",
  "rest": "login.example.invalid:1119",
  "rpc": "login.example.invalid:1118",
  "ca": "config/ca.pem",
  "server_name": "example.invalid",
  "build": 70959,
  "game_account": 1,
  "locale": "enUS",
  "listen": "127.0.0.1:28771",
  "dir": ".",
  "navmesh": "~/forever/maps/mmaps",
  "plans_dir": "dungeons",
  "addon": "on",
  "disconnect_grace": "30s",
  "password_file": "password"
}
```

| Key | Serve flag | Meaning |
|---|---|---|
| `account`, `character` | `--account`, `--character` | The slot's account and character. |
| `rest`, `rpc`, `ca` | `--rest`, `--rpc`, `--ca` | The Forever login endpoints and the root CA file. |
| `server_name` | `--server-name` | Name verified in the login certificate. |
| `build` | `--build` | Client build; `0` or absent is the automatic pick. |
| `game_account` | `--game-account` | Join ticket `#N`; absent is the first listed. |
| `locale` | `--locale` | One of the client languages. |
| `allow_instance_redirect`, `world_checks_build_auth_key` | the same flags | Booleans. |
| `listen` | `--listen` | RPC listen address. |
| `dir` | `--dir` | The slot dir: `slot.json`, pid and token files. `.` is the settings file's folder. |
| `token_file` | `--token-file` | Default: `rpc-<port>.token` in `dir`. |
| `no_auth` | `--no-auth` | Boolean. |
| `navmesh` | `--navmesh` | The mmaps folder. |
| `plans_dir` | `--plans-dir` | Folder `map_snapshot` reads a plan from. |
| `parents` | `--parent` | `{"36": 43}` is `--parent 36=43`; the file sets the whole map, and a `--parent` flag on the command line sets the whole map too. |
| `addon` | `--addon` | `"on"` or `"off"`. |
| `capture_dir` | `--capture-dir` | Packet capture parent (keep it outside the repository). |
| `disconnect_grace` | `--disconnect-grace` | Duration string, `0` to `10m`. |
| `allow_gm_units` | `--allow-gm-units` | Boolean, test bench only. |
| `password_file` | `--password-file` | A file holding the password (one trailing newline is dropped). |
| `password` | — | The password inline; prefer `password_file`. |

Rules:

- **Unknown keys are refused** and the error names the key, so a misspelt key cannot silently leave a default in
  place. Malformed JSON, a trailing value after the object and an out-of-range value are refused too, each as a
  usage error (exit `2`) before any login.
- The file must be a **regular, non-symlinked file only its owner can read** (`chmod 600`), at most 256 KiB: it
  may name the account's password and the slot's account. The read checks the opened file against the one it
  inspected, so a file swapped in between is refused rather than used.
- A `password_file` the settings file names is held to the same rule — regular, not a symlink, `chmod 600`, at
  most 4096 bytes — because it is the slot's bearer secret; a loose mode or a symlink is a usage error naming the
  path and the problem, never the content. The `--password-file` **flag** keeps its historical, looser read (any
  file you can read): to get the check, name the file in the settings file instead of on the command line.
- **A relative path is relative to the settings file's folder**, not the shell's working folder; a leading `~/`
  is your home folder. That is what makes `"dir": "."` and `"ca": "config/ca.pem"` sensible inside a slot dir.
- **Password precedence:** an explicit `--password-file` (an empty one means the environment), then
  `password_file`, then `password`, then `FOREVER_PASSWORD`. `password` and `password_file` together are refused.
  The password is never printed, on any path.
- A settings-file problem is reported as `forever serve: settings <path>: <problem>` (or with
  `(from --settings <path> and the command line)` when the merged flags are still incomplete).

# Tearing a slot down

```sh
bin/forever-agent teardown --gm off ./slot
```

`teardown` reads the dir's `slot.json`, stops a running follow loop over RPC if there is one, sends the GM line
(`--gm off` by default; `on` and `keep` are the other values), logs the character out, ends the agent by its pid
file, removes secrets and releases the lease. `--keep-lease` leaves the lease in place, and `--release-anyway`
releases it even when a game-side step failed (by default a game-side failure keeps it); `--slots FILE` names the
slot table instead of the one recorded in `slot.json`. `--dry-run` plans the steps and sends nothing; `--json`
prints the steps. It accepts both a `slot setup --forever` dir (the lease is released) and a bare `forever serve
--dir` (no lease exists, so that step is skipped). Exit 14 means a step failed; a lease refusal is recorded as
skipped.

# The settings file (old stack only)

The settings file is the old 1.12 configuration: `forever-agent login` and the Turtle-only tools read it. It
defaults to `~/.config/forever-agent/settings.json`, must be mode `0600` (`settings <path> has mode 0644; it
holds passwords, run: chmod 600 <path>`), and refuses unknown keys. A minimal file names the old realmd and the
account's password; `rpc` must be loopback and defaults to `127.0.0.1:8765`:

```json
{ "realmd": "example.invalid:3724", "accounts": { "ACCOUNT": "<password>" }, "rpc": "127.0.0.1:8765" }
```

Other keys it accepts: `realm`, `world_addr`, `rpc_auth` (`"on"` default, or `"off"`), `rpc_token` (16+ visible
ASCII characters), `bots`, `test_spell`, `spot`, `mmaps_dir`, `tbm_addon_dir`, `dungeons_dir`, `rotations_dir`,
`traces_dir`, `tb_dashboard_url`, `addons`, `run_meta`, `shared_fingerprint`, `server_name`, `state_dir`. Folders
accept a leading `~/`. A Forever command does not read this file; keep Forever connection details in the slots
file.
