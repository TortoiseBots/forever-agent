---
type: Guide
title: What forever-agent is
description: What the agent does, which server it speaks to, and which parts have been run against a live server.
tags: [getting-started, overview, client, forevercore]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:31:22Z }
sources:
  - id: readme
    resource: README.md
  - id: main
    resource: cmd/forever-agent/main.go
  - id: command-scope
    resource: cmd/forever-agent/command_scope.go
  - id: agents
    resource: AGENTS.md
---

forever-agent is a headless client for **ForeverCore** servers: one process logs one character into the world
with no game window and exposes it over a local JSON-RPC socket, so server-side behaviour can be driven and
tested from scripts. It is an unofficial community project, not affiliated with Blizzard Entertainment, and you
should use it only on servers you run or are explicitly allowed to test.

The repository began as a copy of the Turtle WoW client (1.12) and still carries that stack beside the Forever
one. This manual documents the Forever commands. Where a command exists only for the old 1.12 stack, the page
says so; `forever-agent` with no arguments prints the whole command list and marks those lines with their
Forever replacement.

# What it is for

You use the agent when you want to:

* check that a ForeverCore server's login and world path actually work (`forever login-check`, `forever world-check`);
* keep a character in the world and drive it from scripts (`forever serve` plus `forever rpc`);
* run a bot party through a dungeon or a raid and score the run (the run commands and the watch);
* test a server's bot module over its chat and add-on protocols;
* record, replay and score what happened (traces, combat logs, report cards, captures).

# How it is shaped

One process owns one character. There is no multi-account process; a fleet or a set of slots is several
processes.

* A **one-shot check** logs in, does one thing, prints one line per step and logs out (`forever world-check`,
  `forever chat-check`, `forever fight-check`, ...).
* A **session daemon** (`forever serve`) logs in and stays: it holds the world socket, keeps the character alive
  through a dropped link, and answers JSON-RPC calls on a loopback socket. `forever rpc DIR status` asks it
  what is happening.
* **Slot directories** hold the daemon's manifest (`slot.json`), its pid file and its RPC token, plus the
  `forever-agent` binary it runs. `teardown DIR` cleans one up.

# What has been proven, and what has not

The table below says honestly what has been run against a live server and what has only been exercised against
a scripted (fake) world. The long development notes are under `docs/forever/`.

| Part | State |
|---|---|
| Login through the Battle.net login server (SRP, login ticket, realm join) | works live (`forever login-check`) |
| World socket: world auth, AES-GCM, character list, login burst, time sync, ping, logout | works live (`forever world-check`) |
| Objects, movement, names, the character's own inventory, chat and party, add-on messages, death and corpse run, character create and delete | works live |
| The `act` step runner with the item steps (equip, store, swap, split, destroy, use, `inv`) | works live |
| The single-purpose act steps for vendors, quests, trainers, flight paths, mail, loot and game objects, and the battleground queue | works against the scripted world; a live run is the next step for each |
| `forever dungeon-run --dry` (the run machine over a plan) | works against the scripted world; no live dungeon run yet |
| `forever dungeon-watch` (watch a module-led run) | works against the scripted world; the live mode is wired but has not been run |
| `forever raid-watch` (the same over a raid plan) | works against the scripted world; no live raid run yet |
| `forever dash-bots` (roster dashboard client and watch) | client and watch built; no live run yet |
| The dashboard's 3D view (stage 1: the walkable mesh with markers, live and replay) | built; exercised against a fake serve and a synthetic mesh |
| Trials bench (`trials run` and its situations) | provisions live and drives the situations; see the testing section |
| Navmesh planning (`path_to`, `walk_path`, `forever path-check`) | works offline on a Forever mmaps folder; needs a cgo build |

"Works against the scripted world" means the protocol handling, the state machine and the refusal paths are
tested against a fake server; it does not mean the behaviour has been seen on a real ForeverCore world.

# Which commands you will reach for first

| Want to | Command | Page |
|---|---|---|
| build and smoke-test the binary | `make build`, `forever-agent version` | [Install and build](./install-and-build.md) |
| prove the login and world path | `forever login-check`, `forever world-check` | [Your first login](./first-login.md) |
| keep a character in the world and call it | `forever serve`, `forever rpc` | [The session daemon](../daemon/serve.md), [RPC basics](../daemon/rpc-basics.md) |
| read the world state | `status`, `units`, `inv`, `events` | [Status and world reads](../daemon/status-and-world-reads.md) |
| drive a dungeon or a raid | `forever dungeon-play`, `forever dungeon-watch`, `forever raid-watch` | [Dungeon play](../dungeons/dungeon-play.md), [Dungeon watch](../dungeons/dungeon-watch.md) |
| order bots | `forever rpc DIR bot`, `commander` | [Bot control](../bots-and-raids/bot-control.md) |
| check a run before starting it | `forever preflight` | [Preflight](../dungeons/preflight.md) |

# The old 1.12 stack

These top-level commands serve the old stack alone; each refuses a Forever server before any lease, login, file write
or child process, and `forever-agent` with no arguments marks its line in the command list with its replacement.
The Turtle packages and the old documentation are under `docs/turtle/`.

| Turtle-only command | Forever replacement |
|---|---|
| `login` | `forever serve` |
| `scrub-check` | `forever scrub-check` |
| `capture` (replay) | `forever capture-events` |
| `capture-stats` | `forever capture-stats` |
| `map` | `forever run-map` |
| `path-check` | `forever path-check` |
| `raid-walk`, `raid-suite`, `walk-preflight` | none yet: `forever raid-watch` and `raid-autopilot --forever` are the Forever path |
| `trace-score` | none: it reads a 1.12 run trace |
| `pulltest`, `decisions`, `tune-diff` | none: Turtle test and tuning tools |
| `rotation` | none: the Turtle brain |
| `fight-score`, `dashboard-replay` | none: they read Turtle dashboard recordings |

Every other command works on both stacks, or takes `--forever` where its Forever mode differs; the pages of this
manual say which on each command page.

# What the agent does not do

* It does not render a game window, read game memory or emulate Warden.
* It does not create, move or delete world content for everyone. Its game-master hands are test tooling:
  off by default and limited to units a test runner registered. See
  [Safety and GM guards](./safety-and-gm-guards.md).
* It does not own the bots: the bots run inside the server as a bot module, and the agent talks to that module
  over the server's own chat and add-on channels.
