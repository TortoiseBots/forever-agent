---
type: Guide
title: Battlegrounds
description: Queue a character for Warsong Gulch or Arathi Basin with the scripted act steps, or watch a whole match and read its report card.
tags: [bots-and-raids, battleground, queue, report-card]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T23:40:00Z }
sources:
  - id: bg-act
    resource: internal/forever/world/bg_act.go
  - id: bg-bg
    resource: internal/forever/bg/bg.go
  - id: bg-watch
    resource: internal/forever/bg/watch.go
  - id: watch-cmd
    resource: cmd/forever-agent/forever_bgwatch.go
  - id: card-text
    resource: cmd/forever-agent/forever_bgwatch_dry.go
---

Two things in the agent involve a battleground. The three scripted **queue
steps** let any character queue, accept and leave through the core's own
battlemaster packets — you combine them with walks and other steps in `forever
act`. **`forever bg-watch`** goes further: it queues its character, accepts, plays
the match as a passive observer, records the whole match and prints a report card
about the bots' behaviour.

What is proven: the queue steps and the watch work against the scripted world; a
live battleground run is the next step, and nothing here has been run against a
real match yet.

# Queue steps

The steps are `bg-queue`, `bg-accept` and `bg-leave`. They act on the NPC the
previous `npc` step selected, so a step file usually looks like this:

```sh
forever-agent forever act --account ACCOUNT --character Mychar \
  --rest 127.0.0.1:8081 --rpc 127.0.0.1:8082 --ca ./ca.pem \
  --step 'npc entry=12345' --step 'bg-queue' --step 'bg-accept' --step 'bg-leave'
```

| Step | What it sends | What it needs |
|---|---|---|
| `bg-queue` | The battlemaster hello, reads the list, then the join; records the queue it joined | The current NPC must be a battlemaster within reach. A battlemaster that lists no supported battleground stops the step instead of queueing. |
| `bg-accept` | The port accept for that queue's invite ticket | A `bg-queue` that got an invite; an invite whose accept window has passed is refused before any packet. |
| `bg-leave` | For a queue or an invite: the port with the accept bit clear, answered by the status `NONE` for that slot | A selected queue. Inside a match it needs `match=yes`, because leaving a started match casts the Deserter debuff. |

Every status is bound to the queue the server named. A status, invite or `NONE`
for another queue the character is in never selects, accepts or leaves this one,
so a second queue in the same session cannot make a step act on the wrong one.
`bg-leave` with `match=yes` sends the core's match leave and reports the map it
left.

`bg-queue` succeeds when the server confirms the join: a queued status (with the
estimated wait) or an invite (with its map). A refused join fails the step with
the server's own reason.

The act steps in general are described in [Scripted actions](../player-actions/act-steps.md).

# bg-watch

```
forever-agent forever bg-watch --map wsg|ab [--role observer] [--character NAME]
    [--rest host:port] [--rpc host:port] [--ca FILE] [--account EMAIL]
    [--max-minutes N] [--poll SEC] [--quiet-min X] [--idle-s N] [--escort-share X]
    [--touch-s N] [--out DIR] [--run-id NAME] [--dry]
```

It is the human in the queue: it queues its character through the typed queue id,
accepts the invite after a random 2-8 s (capped below the invite's remaining
window), stays at its start spot and never touches a flag, and polls the module's
`.tb bg status` for the bots' side totals and per-bot rows. The queue wait and the
match share the one time budget.

| Flag | Default | Meaning |
|---|---|---|
| `--map` | `wsg` | `wsg` (Warsong Gulch) or `ab` (Arathi Basin). Also accepts `warsong`, `arathi`, or the map id. |
| `--role` | `observer` | The only role. `observer` stays at the start spot; there is no flag-carrier mode. |
| `--team-of` | `0` | Reserved, and must be 0. |
| `--max-minutes` | `45` | The match budget, queue included (1-1440). |
| `--poll` | `5` | The status poll interval in seconds (1-30). |
| `--quiet-min` | `10` | Card: flag a match with no capture (WSG) or assault (AB) in this many minutes. |
| `--idle-s` | `60` | Card: flag a bot idle this many seconds while the match is in progress. |
| `--escort-share` | `0.5` | Card: flag a flag carrier escorted by under this fraction of the sampled time. |
| `--touch-s` | `10` | Card: flag a dropped own flag untouched this many seconds. |
| `--out` | empty | Folder for the record and the card files; empty prints the card only. |
| `--run-id` | `bg-<UTC time>` | The name the files carry (one file name, no folder separators). |
| `--dry` | off | Run the watch machine against the scripted world (no network). |

`--character` is required unless `--dry`. The movement the watcher does is only
the queue, the accept and the match leave: everything else it sends is a `.tb bg
status` poll — no orders, like the dungeon watch.

# What you see

```
forever bg-watch: map wsg role observer stop match-end winner alliance events 64
card: CLEARED (alliance won in 14:22)
record: ./out/bg-20261014T093012Z.forever-bg.json
card files: ./out/bg-20261014T093012Z.bg-card.txt ./out/bg-20261014T093012Z.bg-card.json
```

With `--out` it writes three files:

| File | Contents |
|---|---|
| `<run>.forever-bg.json` | The record, schema `forever-bg` version 1: match clock and scores, flag events (WSG), node events (AB), deaths per side, the module's bot rows, the watcher's own position samples, the result and length, and the counted facts the card reads. No character name, account, host or path. |
| `<run>.bg-card.txt` | The card as text: verdict, match numbers, thresholds, every flag with its evidence lines, and the notes on rules that could not run. |
| `<run>.bg-card.json` | The same card as JSON, schema `forever-bg-card` version 1. |

The record carries:

- `scores`: one sample per score change, with the match clock, captures per side
  (WSG) or resources and occupied bases (AB).
- `flags`, `nodes`, `deaths`: the typed event lists, each with the match second and
  what it was about (a flag, a node, a role word or a side).
- `alliance`, `horde`: the module's humans, bots and deaths per side.
- `bots`: one row per module bot: role, objective, node, alive, and the longest
  time it stood still while the match was in progress.
- `moved_yd`, `escort_share`, `escort_selected_s`, `touched_dropped`,
  `lost_no_defend`, `deserter`, `premature`: the counted facts behind the rules.

# The report card

The verdict is one word plus a sentence. A match that reached its end is
`CLEARED`, or the first failing rule's name (`QUIET`, `IDLE`, `UNESCORTED`,
`UNTOUCHED`, `UNDEFENDED`, `DESERTER`, `PREMATURE`). A match that never reached a
result is `STOPPED (<reason>)`, or `FAILED (<reason>)` for a lost or blind link.

| Rule | Fires when |
|---|---|
| `quiet` | A WSG match with no capture, or an AB match with no assault, after `--quiet-min` minutes of match. |
| `idle` | A bot's longest still span reaches `--idle-s` seconds while the match is in progress. |
| `unescorted` | The flag carrier's escort share is under `--escort-share`, when a carrier was seen at all. |
| `untouched` | A dropped own flag lay untouched past `--touch-s`. |
| `undefended` | An AB node was lost with no defender near it. |
| `deserter` | Any bot carried the Deserter aura at a poll. |
| `premature` | The match ended by the core's premature-finish timer. |

The untouched-flag and undefended-node rules need the side the module's status
poll names. When no poll carries it, the card says so in a note instead of passing
those rules silently.

# Stop reasons and exit codes

| Stop | Meaning | Exit |
|---|---|---|
| `match-end` | The match reached its result. The watcher then leaves the instance. | 0 |
| `queue-timeout` | No invite before the budget ran out. | 5 |
| `queue-refused: <reason>` | The server refused the join (a deserter debuff, out of the bracket). | 5 |
| `invite-expired` | The invite's accept window closed before the accept, or the entry was never confirmed. | 5 |
| `time-budget` | The match budget ended. | 5 |
| `status-refused` | The `.tb bg status` poll was answered without the module's battleground block. | 5 |
| `link-lost` | The world socket dropped. | 6 |
| `refused: ...` | A bad setting (the reserved `--team-of`, an unknown map). | 5 |

# Examples

```sh
# Watch one Warsong Gulch match with the default rules and files.
forever-agent forever bg-watch --map wsg --character Mychar --out ./out \
  --rest 127.0.0.1:8081 --rpc 127.0.0.1:8082 --ca ./ca.pem --account ACCOUNT

# Arathi Basin with a tighter idle rule and a named run.
forever-agent forever bg-watch --map ab --idle-s 30 --run-id ab-trial-1 --out ./out \
  --character Mychar --rest 127.0.0.1:8081 --rpc 127.0.0.1:8082 --ca ./ca.pem --account ACCOUNT

# The whole watch against the scripted world, no login.
forever-agent forever bg-watch --map wsg --dry --out ./out
```

# Common problems

| Symptom | What to do |
|---|---|
| `bg: --role "carrier" is not observer` | There is no flag-carrier role yet; watch as `observer`. |
| `status-refused` | The server has no battleground module, or it is off; the match itself was still observed. |
| The card has a note about an unknown team | The module's status answer carries no side; the two rules that need it stayed off. |
| `queue-refused: ... deserter ...` | The character has the Deserter debuff; wait it out. |
| No record files | `--out` was empty; the card was printed only. |
