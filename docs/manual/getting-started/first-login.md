---
type: Guide
title: Your first login
description: Prove that the agent can reach a ForeverCore server with login-check and world-check, create or delete a character, then start a session.
tags: [getting-started, login, world-check, serve]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-25T00:30:00Z }
sources:
  - id: forever-login
    resource: cmd/forever-agent/forever_login.go
  - id: forever-world
    resource: cmd/forever-agent/forever_world.go
  - id: forever-chat
    resource: cmd/forever-agent/forever_chat.go
  - id: bnet
    resource: internal/forever/bnet/bnet.go
  - id: locale
    resource: internal/forever/bnet/locale.go
  - id: main
    resource: cmd/forever-agent/main.go
  - id: char-screen
    resource: cmd/forever-agent/forever_charcreate.go
  - id: char-screen-core
    resource: internal/forever/world/charcreate.go
---

The first thing to do with a new server is prove two things in order: that the account can log in through the
Battle.net login server, and that the character can enter the world. `forever login-check` proves the first and
stops; `forever world-check` does both and enters the world for a few seconds. Neither command changes anything
on the server.

# Before you start

You need:

* the login server's REST address and its RPC (Battle.net) address, as `host:port`;
* a PEM file holding the root certificate authority that signed the login server's certificate;
* an account name (an e-mail or a name) and its password;
* for `world-check`, a character on that account.

The password is read from the environment, never from a flag and never printed:

```sh
export FOREVER_PASSWORD='<password>'
```

Prefer a shell that does not keep history for that line, or a wrapper that exports it from a private file. The
commands print no password, ticket, key or proof — only their lengths — so their output is safe to paste into a
report.

# Connection flags

Every Forever command that logs in takes the same connection flags, in any position.

| Flag | Default | Meaning |
|---|---|---|
| `--rest host:port` | — (required) | login server REST base |
| `--rpc host:port` | — (required) | login server RPC address |
| `--ca FILE` | — (required) | PEM of the root CA that signed the login server's certificate |
| `--server-name NAME` | `trinity.actual.battle.net` | name to verify in the login server's certificate |
| `--build N` | `0` | client build to claim; `0` means automatic selection (see below) |
| `--account EMAIL` | — (required) | account name |
| `--game-account N` | `0` | game account to join by the join ticket's `#N`; `0` = the first listed |
| `--locale NAME` | `enUS` | client language to claim: `enUS`, `koKR`, `frFR`, `deDE`, `zhCN`, `zhTW`, `esES`, `esMX`, `ruRU`, `ptBR`, `itIT` |

A world command adds `--character NAME` (required), `--allow-instance-redirect` (accept an instance socket on a
host other than the realm socket's, for clustered servers) and `--world-checks-build-auth-key` (see the build
rule below).

# Step 1: the login path

```sh
bin/forever-agent forever login-check \
    --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem \
    --account ACCOUNT
```

The command prints one line per completed step and stops at the realm join answer. The lines look like these
(numbers are lengths, counts or the server's own values, never a secret):

```text
login form: LOGIN_FORM, SRP form at <path>
srp challenge: version <n>, <variant>, <n> iterations, modulus <n> bytes, salt 32 bytes
srp proof: client evidence M1 <n> bytes accepted, server evidence M2 <n> bytes verified
login ticket: <n> bytes
rpc connect: server id label <n> digits, epoch <n> digits, server time <n> digits, ciid 33 chars, bindless <bool>
logon: build <n>, account id <n>, <n> game account(s), session key <n> bytes
realm list ticket: <n> bytes
realm list: realm <id> "<name>", address 0x<hex>, build <n>, population <n>, flags 0x<hex>
client build: claimed <n>, the realm list shows <n>
realm join: join ticket <n> bytes, world server <host>:<port>, join secret <n> bytes
login: realm: <name> (id <n>, address 0x<hex>, world <host>:<port>), world <host>:<port>
realm: <name> (id <n>, address 0x<hex>, world <host>:<port>)
```

The last two lines say the same thing in two shapes: the realm the join answer is for and where its world server
sits. A login failure is a named step (for example `login form`, `srp`, `logon` or `realm list`) followed by the
server's own reason; nothing after that was done.

## The build rule

A realm accepts only the builds it knows. With `--build 0` the command claims the newest build this client knows
that the realm accepts; a build the realm refuses is skipped for the next known one, and a realm that accepts
none of them is refused by name. `--build N` claims exactly N and the server's answer stands.

A world command has a second gate: the realm list accepting a build does not mean the world server authenticates
it. `world-check` therefore also requires the world authentication to accept the build, skipping builds the
world refuses with `ERROR_BAD_VERSION`, newest first.

`ERROR_BAD_VERSION` has two causes on a ForeverCore server, and the client's setup expects
`Network.SkipBuildAuthKeyCheck = 1`. With that setting the world does not check the build auth key, so the
refusal is about the build and the next one is tried. If your server sets `Network.SkipBuildAuthKeyCheck = 0`,
pass `--world-checks-build-auth-key`: the client then knows the key is checked, stops at the first
`ERROR_BAD_VERSION` and names both causes instead of calling the build unsupported. Getting this wrong turns a
working login into a long list of refused builds.

# Step 2: the world

```sh
bin/forever-agent forever world-check \
    --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem \
    --account ACCOUNT --character Mychar --stay 20s
```

`world-check` logs in like `login-check`, then authenticates the world socket, lists the characters, sends the
login for `Mychar`, follows the login burst (loading screen, time syncs, the active-mover exchange), keeps the
link alive for `--stay` (default `20s`, at most `10m`), logs out, and prints a packet count per opcode plus one
final line:

```text
world-check: Mychar in the world on map 0, 3 time syncs, 1 pings, 1 pongs, logout complete
```

The logout state is `complete`, `complete (instant)` or `not completed`. A world refusal (bad character name,
character not on this account, a character already in the world) is the command's error and exits 5.

# Step 3: a session you can call

Once both checks pass, start the session daemon on a slot directory and ask it for its status:

```sh
bin/forever-agent forever serve \
    --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem \
    --account ACCOUNT --character Mychar \
    --password-file ./password.txt --listen 127.0.0.1:8765 --dir ./slot

bin/forever-agent forever rpc ./slot status
```

`serve` binds the RPC socket first, logs in, then prints the RPC address and its token file before it enters the
world, and prints `serve: logged out` when it stops.
The `--dir` becomes a slot directory: `slot.json`, `agent.pid` and `rpc-8765.token`. `--password-file` is the one
place a password comes from a file (one line, at most 4096 bytes, one trailing newline allowed; the daemon does
not check the file's permissions, so keep it `0600` yourself); every other command uses `FOREVER_PASSWORD`.
See [The session daemon](../daemon/serve.md) and [RPC basics](../daemon/rpc-basics.md).

# The character screen: forever char-create and forever char-delete

Both commands log in like `forever world-check` (the same connection flags and `FOREVER_PASSWORD`), authenticate the
world socket, and work on the character screen without entering the world. Both list the account's characters before
and after, so a rerun shows what is actually there.

```bash
bin/forever-agent forever char-create --name NAME [--surname NAME] --race R --class C [--sex 0|1] \
    (--choice OPTION:CHOICE ... | --no-appearance) --rest example.invalid:1119 --rpc example.invalid:1119 \
    --ca ./ca.pem --account ACCOUNT

bin/forever-agent forever char-delete (--name NAME [--surname NAME] | --guid 0xHIGH_LOW) \
    --yes NAME --rest example.invalid:1119 --rpc example.invalid:1119 --ca ./ca.pem --account ACCOUNT
```

| Flag | Default | Meaning |
|---|---|---|
| `--name` | required | The character name (for `char-delete`, its full name may carry the surname after the first blank). |
| `--surname` | none | The character's surname, when it is not part of `--name`. |
| `--race`, `--class` | required, 1-255 | Race and class ids (`ChrRaces.dbc`, `ChrClasses.dbc`). |
| `--sex` | `0` | `0` male, `1` female. |
| `--choice OPTION:CHOICE` | none | One appearance choice, repeatable and sent as given. |
| `--no-appearance` | off | For test characters: create with no appearance choices. `--choice` and this are exclusive. |
| `--guid 0xHIGH_LOW` | none | `char-delete`: delete exactly this GUID instead of resolving `--name`. |
| `--yes NAME` | required | `char-delete`: the resolved character's full name; without the match nothing is sent. |

The result code is the core's own name (`char-create: Mychar -> CHAR_CREATE_SUCCESS` and the like).
`char-delete` first resolves the name from the account's list and deletes by GUID; `--yes` must repeat the resolved
full name, so a typo can never delete a character. A create or delete that was sent and never answered exits `32`:
the character may or may not exist, so list the account's characters before retrying (the server may hold a delete
for its own 30-second pending-delete bound).

# Exit codes you will meet here

| Code | Meaning |
|---|---|
| 0 | success |
| 2 | usage: a missing or malformed flag, or the settings/slots file refused |
| 3 | the login server rejected the login (account, password, authenticator, version) |
| 4 | the realm is missing or offline |
| 5 | the world server rejected the session or the character |

The full list, including the RPC and slot codes, is in [Troubleshooting](./troubleshooting.md).
