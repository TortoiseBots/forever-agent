---
type: Guide
title: Safety and GM guards
description: The rules for testing on a server, the game-master hands and the boundaries that keep them off other players, and the console and capture limits.
tags: [getting-started, safety, gm, guards, testing]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:31:22Z }
sources:
  - id: agents
    resource: AGENTS.md
  - id: serve
    resource: internal/forever/world/serve.go
  - id: serve-methods
    resource: internal/forever/world/serve_session_methods.go
  - id: gm-gaps
    resource: internal/forever/world/serve_gm_gaps.go
  - id: command-session
    resource: internal/forever/world/command_session.go
  - id: rpc-server
    resource: internal/rpc/server.go
  - id: soap
    resource: internal/forever/soap/soap.go
  - id: capture
    resource: internal/forever/world/capture_run.go
  - id: config
    resource: internal/config/config.go
---

forever-agent acts through one ordinary game account. Everything it does reaches the server as the packets a real
client sends or as a chat command the account is allowed to type. The danger is not the agent itself; it is a test
hand acting on the wrong character or a test account acting on a live population.

# The rules of the house

* Use it **only on servers you run or are explicitly allowed to test on**.
* Test on test accounts and test characters. Never test on a real player's account or character, and never put a
  real account name or password into a report, a capture, a fixture or a pull request.
* Your own game-master commands are test tooling, never bot behaviour. Do not send commands that change the
  world for everyone: `.npc set/add/delete/move`, `.gobject add/delete`, `.reload`, `.server`, `.account`, `.ban`,
  config edits, restarts. Do not change server configuration to make a test pass.
* Report only what you observed: event lines, numbers, log lines with times. Mark anything inferred.
* Clean up with `forever-agent teardown <dir>`, which stops the run, unbinds the bots, ends the character's own
  bots, sends `.gm off`, logs out, ends the agent by its pid file and releases the lease. Never `pkill` the agent
  or kill it by name.

# The GM hands are off by default

The hands that can damage, kill or buff a **unit** are disabled unless the daemon was started with
`--allow-gm-units`, and then act only on registered units:

| Method | What it does |
|---|---|
| `gm_kill` | kills one unit (`.die`); a player is refused |
| `gm_set_hp` | sets one unit's health (`.modify hp`; `current_only` sends `.modify currenthp`) |
| `gm_aura` | puts an aura on one unit, or removes it with `remove: true` |
| `gm_revive` | sends `.revive <name>`; its own player rule is below |
| `trial_units` | registers the guids the hands above may act on; also refused without the flag |
| `gm_evade` | `.npc evade` on one creature (test setup) |
| `gm_respawn` | `.respawn` on one dead creature (test setup) |

Without the flag each of these refuses with the typed `-32006` error and sends nothing:

```json
{"code": -32006, "message": "gm_kill: serve was started without --allow-gm-units"}
```

Two methods are not gated by the flag:

* `teleport` sends `.go xyz` for this session's own character only, so it needs neither the flag nor an
  allowlist;
* `gm_combatlog` is a read-only watch (`mode` `unit`, `group` or `off`) and needs only the account's GM
  permission.

The raw `gm` method sends ordinary lines untouched and holds a unit line to the typed hands' boundary before it
is sent:

```json
{"code": -32006, "message": "gm: 0x00000000000000000000000000000007 is not registered for the trial"}
```

# The unit allowlist

Even with the flag on, a hand acts only on a guid the test runner registered through `trial_units`:

```json
{"guids": ["0x00000000000000000000000000000001"]}
```

`trial_units` *replaces* the list and answers `{guids}` in registration order (duplicates are dropped; an empty
list clears the allowlist). It refuses a zero guid and is itself refused without `--allow-gm-units`:

```text
trial_units: serve was started without --allow-gm-units
```

Two extra rules apply to players:

* a player target must be **this session's own character**. Another player is refused even when registered:
  `<guid> is a player other than this session's character`;
* a unit that is not registered is refused: `<guid> is not registered for the trial`.

The allowlist outlives a reconnect: a dropped link does not widen or lose it.

## Which raw lines are guarded

The core's own command table decides. Matching is case-insensitive and a typed token matches a command whose name
starts with it, so `.mod`, `.dam` and `.revi` are the same commands as `.modify`, `.damage` and `.revive`. Only
one command per line is classified.

| Line | Guard |
|---|---|
| `.die`, `.kill` | unit command: the flag and a registered target |
| `.damage`, `.modify hp/mana/rage/energy` (and the core's `power`, `runicpower`, `currenthp`) | unit command |
| `.aura`, `.unaura` (including the `self` form) | unit command |
| `.revive <name>` | its own boundary: the session's own character or a registered player |
| `.revive` with no name | the selected player, if any; the character itself when nothing is selected |
| anything else (`.tb ...`, `.gps`, `.server info`, chat) | not a unit command; passes untouched |

`.revive` is a resurrection, so it cannot damage a guarded unit; it is still restricted so a session cannot raise
a player it has nothing to do with. With the flag on, the character's own player passes without being registered
(a bench whose character died must be able to raise it before anything is registered), a registered player passes
(a trial's own bot), and any other player is refused. Every GM line goes out through the same command channel, so
the core's own GM log carries the audit line.

Selected-target lines check **both** selections in play — the one this session last sent and the one the server
last echoed — so a selection still in flight cannot open the gate for the unit it is replacing. With neither set,
the lines act on the character itself.

# The command permission probe

Chat commands only work for an account that holds the core's command permission (`RBAC_PERM_COMMAND_GM` for the
game-master table). `forever cmd-check` sends one harmless probe, `.gm` with no argument — the core's non-mutating
form, which changes nothing — and sends nothing else unless the reply contains the specific `gm on` entry of the
gm help block. A notification, MOTD, announcement or "command does not exist" is not an acknowledgement; a line
that comes back as the character's own say chat proves the account lacks the permission and the run stops:

```text
world: the account is not a GM: the server said ".gm" as ordinary say chat, so it did not run and this run sends nothing else
```

The guard stays active for the whole run: a say echo at any later point resets it. On the serve channel, `.bot`
owner commands are the one family a normal player account may send without the GM confirmation; every other
command text still needs it.

# SOAP: setup with no client

`forever soap` runs one console command over the core's SOAP endpoint. The endpoint is plain HTTP, so:

* the host must be loopback (`127.0.0.1`, `::1` or `localhost`); a non-loopback host is refused, and a name that
  resolves off-loopback is refused after resolution;
* the account must be a server administrator;
* the password comes from a private file (mode 0600) or an environment variable, never a flag value, stdout,
  stderr or an error message;
* redirects are not followed;
* only commands on the setup allow-list are sent — `.saveall`, `.pinfo`, `.instance setbossstate` — and the text
  must be one line: a line break or NUL is refused, so a second command cannot ride behind an allowed one.

```sh
bin/forever-agent forever soap --user ADMIN --password-file ./soap.pass ".saveall"
```

Flags: `--host` (default `127.0.0.1`), `--port` (default `7878`), `--user`, `--password-file`,
`--password-env` (default `FOREVER_SOAP_PASSWORD`), `--timeout` (default 15 s, at most 2 m). The preflight's
SOAP check and a scenario's `soap` step use the same backend and the same loopback rule.

# Captures are raw bytes

`--capture-dir DIR` writes the run's server packets, so a capture can contain names, coordinates, chat and
addresses:

* a capture is refused inside a git work tree (`capture: <dir> is inside the git work tree <root>; a capture is
  written outside a repository`);
* keep it outside the repository and out of any artefact you share;
* check it before sharing with `forever scrub-check`, which looks for account names, passwords, session keys,
  auth tokens and IPv4 addresses; `--redact DIR` writes a cleaned copy and never touches the original. See
  [Privacy and captures](../developers/privacy-and-captures.md).

# Accounts, fingerprints and anticheat

The old stack's settings file has a `shared_fingerprint` switch that makes every account present one addon
fingerprint, as one real PC would. It is off by default on purpose: the core's anticheat bans by a hash of
fingerprint and IP, so a shared print marked on one account reaches all of them. Leave it off unless you know
your server's anticheat policy for test accounts.

# What the RPC socket exposes

The session's RPC socket is loopback only (`--listen` must be a loopback address; the default is
`127.0.0.1:8765`) and, unless `serve --no-auth` was used, every call needs the bearer token from the slot's
`rpc-<port>.token` file. The token file is mode 0600 and belongs to the agent: drive the agent with
`forever-agent rpc <dir> <method>`, never read the token file yourself. `--no-auth` is for a throwaway local
bench only; it leaves the socket open to every local process.
