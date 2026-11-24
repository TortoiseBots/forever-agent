---
type: Guide
title: Ordering bots
description: How the agent talks to a bot module, which commands it sends, and which of your bots it may order.
tags: [bots-and-raids, bots, orders, commander]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T23:40:00Z }
sources:
  - id: bot-commands
    resource: contracts/bot-commands/v1/README.md
  - id: botcmd
    resource: internal/forever/command/botcmd/botcmd.go
  - id: fbcmd
    resource: internal/forever/command/fbcmd/fbcmd.go
  - id: fb-replies
    resource: internal/forever/command/fbcmd/replies.go
  - id: cmdctl
    resource: internal/forever/cmdctl/cmdctl.go
  - id: cmdctl-verbs
    resource: internal/forever/cmdctl/verbs.go
  - id: commander-card
    resource: internal/forever/command/tbcmd/commander.go
  - id: serve-commander
    resource: internal/forever/world/serve_commander.go
  - id: serve-session
    resource: internal/forever/world/serve_session.go
---

The agent orders bots through three different module builds and never mixes them
up. Which one a server runs decides what the agent sends, what it reads back, and
what a refusal means. This page lists the three, the exact lines and replies of
each, and the ownership rules that stop the agent from ordering a bot that is not
the character's own.

# Which module is on the server

All three answer chat commands typed by the session's character, but each has its
own vocabulary and its own reply shape. The agent asks before it acts: the
TortoiseBots contract path matches the `TortoiseBots: ` reply prefix, the Forever
Bots path probes with `.bot about`, and the `.tb` path reads a commander card.

| Module build | The agent's path | You see | The agent sends |
|---|---|---|---|
| TortoiseBots (bot-commands v1) | owner commands over the command channel | `TortoiseBots: <verb> <Bot>: ok ...` system lines | `.bot <verb> <Bot>` |
| The Forever Bots module | `.bot ...` commands, whisper orders, add-on state | The module's own chat text (`<Name> is on the way.`) | `.bot about`, `.bot add`, `.bot list`, whispers |
| TortoiseBots on ForeverCore | GM `.tb ...` command channel | `.tb ...` typed reports | `.tb commander ...`, `.tb order ...`, `.tb cmd ...` |

The `.tb` path needs the character to hold the GM command permission; the other
two are ordinary player commands.

# Owning bots: the bot-commands contract

This is the one bot-side grammar the repository pins as a contract
(`contracts/bot-commands/v1`). It is deliberately small.

A request is one line of say chat on the command channel:

```
.bot <verb> <Bot>
```

The verbs are a closed set: `help`, `who`, `add`, `logout`, `follow`, `stay`,
`attack`, `flee`, `summon`, `stats`. `help` and `who` name no bot; every other
verb names exactly one. No verb takes an argument. A bot's name is the server's
normalised form (first letter upper case, the rest lower) and is compared
case-insensitively. The module's `\\` chain separator and newlines never appear in
a line.

The reply is one or more system chat lines, each starting with `TortoiseBots: `:

```
TortoiseBots: <verb> <Bot>: ok <text>
TortoiseBots: <verb> <Bot>: refused <code>: <text>
TortoiseBots: <verb>: refused <code>: <text>
TortoiseBots: who: <n> bot(s)
TortoiseBots: who: <Bot> level <n> <Class> map <id> <follow|stay|free> <engine|spike>
```

The refusal that names no bot (`usage`, `unknown-verb`, `players-off`) is the one
without a `<Bot>` word. `<text>` is free human text: spaces and `=` may appear and
nothing in it is parsed. A `who` answer is one header line and then one row per
bot; `<n>` is the number of rows that follow.

Refusal codes:

```
usage unknown-verb no-such-player not-a-bot not-yours in-run no-engine players-off too-many
already-online not-found not-leader on-cooldown no-selection not-master queue-full target-dead
in-combat dead teleporting not-in-group already-on-map teleport-refused
```

A code outside that list is still a refusal, and the agent reads it as one. A
reply is never taken for an `ok` because its code looks familiar.

On a server where the module is not loaded, the core answers its own
`There is no such command`. That line is not a reply: it means the module is not
there, and it is how the agent knows to stop.

Two whisper words reach the module and are not `.bot` lines: `release` (for a dead
bot) and `corpse run` (once it is a ghost). The module answers them in its own
chat, which the contract does not shape; the agent only reports whether the
whisper went out.

`not-yours`, `not-a-bot` and `not-found` are the ownership answers: the verb named
a bot the character does not own. The agent never retries those.

# Owning and ordering bots: the Forever Bots module

This build answers player commands whose text is its own, not the contract's. Its
probe is `.bot about`; until that succeeds the agent sends no other `.bot` line in
the session.

| Command | What it does |
|---|---|
| `.bot about` | The probe: names the module. Before it, every other `.bot` line is refused by the agent. |
| `.bot language en\|ru\|auto` | The module's answer language. |
| `.bot add <class> [role]` | Asks for one bot of that class; the module creates it, logs it in and groups it. It answers `<Name> is on the way.` |
| `.bot list` | The chat roster: `<Name>: level <n> <Class>, <online\|offline>[, saved]`. |
| `.bot dismiss <name\|all>` | Logs one bot out and out of the group, or every bot. It answers `Dismissed N companion(s).` |
| `.bot lfg`, `.bot dungeon <name>`, `.bot dungeon cancel\|leave`, `.bot lead <word>` | Its dungeon and lead flow. |

Orders go to one bot as a whisper, and the bot answers the whisper with its own
acknowledgement. The words are the module's; the agent accepts only the
acknowledgement that belongs to the word it sent, so a late `following at ...`
never answers a `stay`.

| Order word | The module's confirmation |
|---|---|
| `follow` | `following at ...` |
| `stay`, `halt` | `staying here` |
| `come`, `summon` | `coming to you` |
| `flee`, `retreat`, `runaway` | `fleeing to you` |
| `stop`, `disengage` | `stopped` |
| `unfollow`, `nofollow` | `stopped following` |
| `fallback`, `fall back`, `back` | `falling back` |
| `rally here`, `rally` | `rally point set` |
| `attack`, `kill`, `engage`, `assist` | `attacking ...` or `not attacking yet` |
| `formation ...`, `spacing ...`, `range ...`, `tight`, `close`, `spread out`, `spaced out` | the word echoed back |
| any other accepted word | `<order> ok` |

An acknowledgement that holds `not attacking yet`, `can't be summoned`,
`not a tank`, `only take orders from` or `refused` is a refusal, and the agent
reports it as one. `only take orders from ...` is this build's ownership rule: the
bot takes orders only from its owner.

A word said in party chat applies to every bot of yours that is in the group. The
module answers little or nothing to a party word, so the agent confirms the effect
by reading the bot again instead of waiting for an acknowledgement.

State is read over the add-on protocol, not over chat: the roster and one bot's
detail. See [the add-on protocol](addon-protocol.md).

# Commanding a party: the `.tb` channel

On ForeverCore the module is driven with GM `.tb` lines. The session's own
character is the commander: it binds a lead bot's party to itself and every order
then addresses `party`, which is the bots bound to that character.

A party is formed and bound in two lines:

```
.tb party <lead> <bot> <bot> ...
.tb master party <lead> <self>
```

Unbinding is `.tb master party <lead> none`; it releases the binding and never
touches the party itself. The module keeps only bindings made from outside a
party, so a character that is already in a party gets `in-party` and must leave
first.

The orders the agent may send:

| Verb | Line | Notes |
|---|---|---|
| `follow [name]` | `.tb order party follow [name]` | One argument at most. |
| `stay`, `come`, `endpull`, `hold`, `free`, `release` | `.tb order party <verb>` | No argument. |
| `attack <target>`, `pull <target>`, `assist <member>` | `.tb order party <verb> <target>` | One argument: a target or a member. |
| `role <bot> <role>` | `.tb role <bot> <role>` | A role mutation of one bot. |
| `run start\|stop\|pause\|resume\|status\|events` | `.tb run <verb> ...` | `start` takes the plan, `stop` the reason words, `events` a sequence number. `status` may name `all`. |
| `move <x> <y> <z> [direct]`, `pullback <tgt>`, `focus <tgt>`, `rest`, `revive party` | `.tb cmd <verb> ...` | The module's own order package (the order grammar). |
| `loot <tgt> [bot] [itemId]`, `open\|use <tgt> [bot]`, `gossip <tgt> [bot] [option]`, `state [bot]`, `lootmethod <method> [threshold]` | `.tb cmd <verb> ...` | The task package. |
| `place`, `rally`, `fallback`, `resume` | `.tb cmd <verb> ...` | The placement package: a held formation and a fall-back point. |

`view`, `scout`, `ritual`, `summon` and `layout` are not ported on this server: the
agent refuses them itself instead of sending a line that would fail.

Before any order that mutates something, the agent reads the commander card
(`.tb commander status <self>`) and reads it again after the line. A target that is
not on the character's own cards is refused as not owned before anything is sent;
a target that changed between the two reads is reported as a line that already
went out. An order while a run is running or paused is refused as `run-active`
unless the call allows the module to pause the run first. An order is sent once
and never retried, because a resend doubles it.

A card carries the binding (commander, lead, group size, raid and player-led
flags), every bound member (bot, class, role, level, health, power, map, zone,
position, raid subgroup, held placement slot), the commander's last eight orders,
and the run record when one of its bots is running.

The module's optional packages are asked about once per session, and the answer is
remembered until the character re-enters the world. The agent calls them `P3` (the
order grammar), `P6` (the task package) and `PL` (the placement package), and its
`not-available` answer names the one it asked about. A package the module lacks
makes its verbs answer `not-available`; no verb is emulated with another.

# Where the agent exposes this

- `forever rpc DIR commander '<json>'` sends one commander call over the session
  daemon: `op` is `bind`, `unbind`, `status`, `cmd` or `run`, with `lead_bot`,
  `bots`, `verb`, `args`, `wait_ms`, `pause_run` and `leave_party` as the op needs
  them. The answer carries `sent` (the exact lines), `bots`, `card` or `cards`,
  `final`, and a run verb's `runs` and `events`.
- `forever rpc DIR bot '{"line":".bot who"}'` sends one raw line. It must start
  with `.bot ` or `.tb`; a `.bot` line is an owner command and needs no GM
  confirmation, every other line confirms the GM guard on first use.

The daemon methods are described in [RPC basics](../daemon/rpc-basics.md).

# Examples

```sh
# Who are my bots, on a server with the bot-commands build?
forever-agent forever rpc ./slot bot '{"line":".bot who"}'

# Bind a five-bot party to this character, then order it to follow.
forever-agent forever rpc ./slot commander \
  '{"op":"bind","lead_bot":"Tankone","bots":["Healertwo","Dpsone","Dpstwo","Dpsthree"]}'
forever-agent forever rpc ./slot commander '{"op":"cmd","lead_bot":"Tankone","verb":"follow"}'

# Read the binding cards, then stop the run on that lead.
forever-agent forever rpc ./slot commander '{"op":"status"}'
forever-agent forever rpc ./slot commander '{"op":"run","verb":"stop","lead_bot":"Tankone","args":["operator"]}'
```

# Common problems

| What you see | What it means |
|---|---|
| `There is no such command` | The module is not loaded on the server, or the build has no such verb. |
| `not-yours`, `not-a-bot`, `only take orders from ...` | The bot belongs to another character or account. The agent will not retry. |
| `not-available: <verb>` | The module's optional package is not installed. |
| `run-active` | A run is running or paused; pass `pause_run` (or stop the run) before ordering. |
| `in-party` | A bind while the character is in a party; leave the party first or pass `leave_party`. |
| `not-owned` | The lead or bot is not on the character's cards. Nothing was sent when `sent` is empty. |
| A `refused` with a code the page does not list | Still a refusal; the code is the module's own. |
