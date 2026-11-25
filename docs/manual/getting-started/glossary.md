---
type: Reference
title: Glossary
description: The words this manual and the agent's output use, in plain terms.
tags: [getting-started, glossary, terms]
status: stable
generated: { by: forever-agent docs, at: 2026-11-24T20:31:22Z }
sources:
  - id: glossary
    resource: GLOSSARY.md
  - id: readme
    resource: README.md
  - id: pbc
    resource: internal/forever/pbc/pbc.go
  - id: tbm
    resource: internal/forever/tbmproto/proto.go
  - id: slots
    resource: internal/slots/lease.go
---

Terms are grouped by subject; within a group they are alphabetical.

# The agent

**Capture** — a directory of the raw server packets one run saw, written by `--capture-dir`. For debugging the
protocol; it is not the run's record and must not be committed.

**Command channel** — the path every chat command takes: a say line that starts with `.` or `!`, sent as the
character, with the account's own permission as the limit. The agent's GM hands and the bot module's commands
both go through it.

**ForeverCore** — the server line the Forever client speaks to: a TrinityCore-lineage core with its own login,
world and add-on behaviour. The 1.12 Turtle stack in the same repository is a different server.

**GM guard** — the permission probe and the boundaries that keep game-master commands on test units. See
[Safety and GM guards](./safety-and-gm-guards.md).

**Preflight** — the checks a run command makes before it logs in: the serve slot and its lease, the plan and its
export, the bot module's answer, the console, the named bots. `--preflight-only` stops after them.

**Record** — what a run writes on disk: a trace (events, frames), a combat log, and a `<run_id>.forever-run.json`
summary. The report card is built from the record, offline.

**Report card** — the one-page verdict of a run: per-leg time, pulls and re-pulls, deaths, wipes, pauses, flags
and a one-line result. `forever run-report RECORD` rebuilds it from a saved record.

**Serve, session daemon** — `forever serve`: one character kept in the world, answering JSON-RPC on a loopback
socket, with a bounded reconnect budget for a dropped link.

**Slot** — one leased place in a server's slot table: an account, a character and an RPC port. A **slot
directory** is the working directory of one slot's session (`slot.json`, pid file, token, binary, plans).

**Lease** — the record that a named holder owns a slot until an expiry. A stale lease (expired, its local
holder process gone) can be taken over. See [Settings, slots and leases](./settings-and-slots.md).

**Trial unit** — a guid a test runner registered with `trial_units`. The GM hands may act only on registered
units, plus the session's own character where the rules allow.

**Trials** — the behaviour test bench (`forever-agent trials`): provision a group, drive a situation, record what
happened and score it.

**Watch** — a command that follows a run without ordering it (`forever dungeon-watch`, `forever raid-watch`,
`forever dash-bots --watch`) and ends with a record, budgets and a summary.

# Dungeons and movement

**mmaps, navmesh** — the server's pathfinding data, repacked for Forever. A folder of `MMMM.mmap` and
`MMMM_XX_YY.mmtile` files. Building with cgo gives the agent the Detour engine to plan on it.

**Plan** — a JSON file describing a dungeon or raid run: entrance, legs, waypoints, pulls, bosses. Plans live
under `dungeons/`; the tools that check and export them are described in the dungeons section.

**Clear** — every boss of the dungeon killed by the group. A clear is either bots-only (an observer run) or
with a player in the group (a real person or the agent in a player run).

**Observer run** — the bots play on their own; the agent only watches and scores.

**Player run** — the agent plays a character in the group as a player would.

# Bots

**Bot module** — the playerbot module running inside the server. The agent does not own bots; it talks to the
module. Two bot systems are named in this project: the **TortoiseBots module** (the port of TortoiseBots to
ForeverCore) and **ForeverCore playerbots** (the system ships with the core). A **merged bot module** is planned.

**Contract** — a versioned agreement, owned by forever-agent, on how the agent and a bot system talk (messages,
replies, status rows), with golden examples either side can test against.

**Owner** — the Battle.net account a bot's character belongs to; any of its game accounts counts. Avoid
"master" (the master is who a bot follows right now) and "account" (say which one).

**Owned bot** — a character of the owner's own, logged in as a bot by its owner while the owner is in the world;
the owner may give it every order.

**Led bot** — a bot in a player's group that the player does not own; it takes the group leader's group orders
(follow, stay, attack, flee) while the group lasts, and nothing more.

**PBC** — the add-on message prefix the bot module uses for its protocol frames (roster, detail, events). The
agent reads only frames that arrive as a whisper from the character's own guid, as the module sends them.

**TBM** — the original TortoiseBots manager's add-on prefix, still spoken by the agent's TBM transport.

**Spec** — the talent build a bot plays: the one its talents point to, or the one its owner orders; below level
10 the class's levelling spec. Not a role, and not a kit (the kit is the set of actions a spec uses).

**Combat items** — consumables a bot uses in a fight: health and mana potions, healthstones, bandages, poisons,
sharpening stones and oils, soulstones.

# Output you will see

**`cmd-r:`** — a line the server answered to a chat command, printed by `cmd-check`.

**`step N verb ok|fail reason`** — one line of an `act` script: the step's number, its verb, and whether it
worked.

**`-32006`** — the RPC error code for a call a boundary refused (a GM hand with the flag off, or a target outside
the allowlist). The call changed nothing.

**refused method** — a daemon method name the server answers `-32003 <name>: <reason>` and does nothing;
`forever rpc DIR list` prints those rows `name (refused: <reason>)`. No method is "not ported" any more: every old
name is implemented or refused with its reason.
