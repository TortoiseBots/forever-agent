---
type: Command
title: forever fleet
description: Keep several Forever daemons online at once, one per account, from a single supervisor.
tags: [bots-and-raids, fleet, daemon, slots]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T23:40:00Z }
sources:
  - id: fleet
    resource: cmd/forever-agent/fleet.go
  - id: fleet-forever
    resource: cmd/forever-agent/fleet_forever.go
  - id: main
    resource: cmd/forever-agent/main.go
---

`fleet --forever` starts one `forever serve` daemon per member of a fleet file,
waits until every one is online, prints a table, and then supervises them together:
each child's exit is reported and the rest keep running, and one Ctrl-C logs every
child out. Use it when several accounts must stay in the world at once — one
character per account, each with its own daemon socket — and you want one process
to start, report and stop them.

The daemons do the logging in and serving; this page covers only the supervisor.
See [the session daemon](../daemon/serve.md) for what each `forever serve` is, and
[slots and leases](../getting-started/settings-and-slots.md) for the slot file.

# Synopsis

```
forever-agent fleet --forever --config FILE --slots FILE [--server NAME]
    [--stagger DURATION] [--wait DURATION] [--stop-on-failure]
```

Without `--forever` the same command supervises `login` children from a settings
file instead; that form takes `--settings`, `--reconnect` and a fleet file of
`account`/`character`/`rpc` members, and is described by the command's own usage
line.

# Flags

| Flag | Default | Meaning |
|---|---|---|
| `--config` | required | The fleet file (see below). |
| `--forever` | off | Run `forever serve` children instead of `login` children. |
| `--slots` | the standard slots file | The slots file holding the per-server `forever` block and the slots. `--forever` only. |
| `--server` | the only one | The server whose `forever` block supplies `rest`, `rpc` and `ca`. Required when the file names more than one such server. |
| `--stagger` | `2s` | Pause between starting children. A Forever fleet also paces logins to one per 5 seconds. |
| `--wait` | `2m` | Give up if not every character is online after this long. |
| `--stop-on-failure` | off | Stop every child when one fails. By default a failed child is reported and the others keep running. |
| `--reconnect` | `0` | Login children only; a serve reconnects within its own budget, so a Forever fleet refuses a non-zero value. |

# The fleet file

```json
{
  "members": [
    {
      "account": "ACCOUNT_ONE",
      "character": "Tankone",
      "dir": "./slots/account-one",
      "listen": "127.0.0.1:8765",
      "password_file": "./secrets/password-one"
    },
    {
      "account": "ACCOUNT_TWO",
      "character": "Healerone",
      "dir": "./slots/account-two",
      "listen": "127.0.0.1:8766",
      "password_file": "./secrets/password-two"
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `account` | The login account. It must be a slot account of `--server`. |
| `character` | The character that daemon keeps in the world. |
| `dir` | The slot-style folder the serve writes `slot.json`, its pid file and its token into. |
| `listen` | The daemon's JSON-RPC listen address: loopback, with a fixed port. Its port is also the port the supervisor polls. |
| `password_file` | The private file the child reads the password from. The password never travels on a command line. |

Every field is required. Accounts, characters, folders and listen ports must all be
distinct — one realmd session per account, and two members on one account kick
each other out. The file is read strictly: an unknown field (a password typed into
the JSON, for instance) refuses the whole file.

# What it does

1. Loads the slots file and the fleet file, resolves `--server`'s `forever` block,
   and takes a per-account lock. A second fleet started for the same accounts is
   refused (exit 15) instead of racing the first.
2. Checks every member against the slots file before any login: the account must
   be a slot account of that server, its character must be one the slot allows,
   and no other live lease may hold an account's slot — the member's own
   `slot.json`, if it has one, must match the live lease (server, slot, holder,
   lease id, port, account, character). A member that cannot be cleared stops the
   whole start before a single login.
3. Starts one `forever serve` per member:

   ```
   forever-agent forever serve --rest R --rpc P --ca FILE --account ACCOUNT
       --character NAME --password-file FILE --listen HOST:PORT --dir DIR [--build N]
   ```

   Logins are paced one per 5 seconds, so a big fleet stays under the login
   server's per-address limit.
4. Polls each serve's own `status` method through its folder until every child's
   link state answers `online`. `reconnecting` and `offline` do not count, so a
   fleet that is "up" but not in the world never passes this step.
5. Prints the table and supervises: each child's exit is reported with the code's
   meaning, the others keep running unless `--stop-on-failure` is set, and Ctrl-C
   asks every child to log out (killing one that does not finish within a minute).

# What you see

```
CHARACTER  RPC           STATE
Tankone    127.0.0.1:8765  online
Healerone  127.0.0.1:8766  online
fleet: 2 character(s) in world; Ctrl-C logs them all out
```

Only the state answers are shown in the table. A child's own log lines are printed
with its character's name as a prefix (`[Tankone] ...`) so you can tell them apart.

Exit codes:

| Code | Meaning |
|---|---|
| 0 | Every child logged out cleanly after Ctrl-C. |
| 9 | A child failed or did not reach the world in time. |
| 15 | The fleet file or the slots check refused a member, or another fleet holds the accounts. |
| 2 | Bad flags or a file that does not parse. |

# Examples

```sh
# Start the fleet from a file and a slots file.
forever-agent fleet --forever --config ./fleet.json --slots ./slots.json

# A fleet that stops everything when one member fails, waiting a little longer.
forever-agent fleet --forever --config ./fleet.json --slots ./slots.json \
  --stop-on-failure --wait 5m
```

# Common problems

| Symptom | What to do |
|---|---|
| `account X is held on SERVER/SLOT by HOLDER` | Another live session holds that account's slot. Use its slot's `slot setup` folder for this member, or release the lease first. |
| `the lease in DIR/slot.json (SERVER/SLOT) is no longer held: run slot setup again` | The folder was prepared for a lease that has since been released or taken over; re-run `slot setup`. |
| `DIR/slot.json is for port N, the member listens on M` | The folder belongs to a daemon on another port; give the member its own folder. |
| `account "A" is listed twice (one session per account)` | Two members on one account; give each its own account. |
| `fleet: not every character was in world after 2m0s` | Read the prefixed child lines: usually a refused login or a slot check the start skipped only by `--stop-on-failure` being off. |
| `another fleet ...` (exit 15) | A previous supervisor still holds the accounts; stop it first. |
| `--reconnect is for login children` | Drop the flag; a serve reconnects on its own. |
