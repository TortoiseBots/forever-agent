---
type: Reference
title: Settings, slots and leases
description: The files that tell the agent which server and account to use, how slot directories are laid out, and how leases keep one worker per slot.
tags: [getting-started, settings, slots, leases, configuration]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:31:22Z }
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
---

There are two configuration paths in the repository. The **settings file** (`settings.json`) belongs to the
old 1.12 stack: `forever-agent login` and the Turtle-only tools read it, a Forever command does not. The **slots
file** plus **slot directories** belong to the Forever path: the slots file names servers and their slots, and a
slot's per-server `forever` block carries the connection flags the Forever tools use.

If you only use Forever commands, you need a slots file and slot directories, not a settings file.

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

# Tearing a slot down

```sh
bin/forever-agent teardown --gm off ./slot
```

`teardown` reads the dir's `slot.json`, stops a running follow loop over RPC if there is one, sends the GM line
(`--gm off` by default; `on` and `keep` are the other values), logs the character out, ends the agent by its pid
file, removes secrets and releases the lease. `--dry-run` plans the steps and sends nothing; `--json` prints the
steps. It accepts both a `slot setup --forever` dir (the lease is released) and a bare `forever serve --dir` (no
lease exists, so that step is skipped). Exit 14 means a step failed; a lease refusal is recorded as skipped.

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
