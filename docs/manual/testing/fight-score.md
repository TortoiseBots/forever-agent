---
type: Command
title: fight-score
description: Score a finished fight JSON from the dashboard with the fight debugger's two tank checks and the class spell oracle.
tags: [testing, fight-score, tanks, oracle]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:15Z }
sources:
  - id: fightscore-cmd
    resource: cmd/forever-agent/fight_score_cmd.go
  - id: kb-judge
    resource: internal/kbspells/judge.go
  - id: oracle-common
    resource: internal/kbspells/forever/_common.json
  - id: tracecheck
    resource: internal/tracecheck/tracecheck.go
---

`fight-score` reads a finished fight the way the dashboard's fight debugger does and prints the same two tank checks, plus one oracle line per bot with a warning. It is the quick offline look at a single fight: did the main tank keep Sunder Armor up, did the off-tank taunt at all, and did anyone miss a core spell. It reads files only and never logs in.

# Synopsis

```
forever-agent fight-score [--json] [--mt NAME] [--ot A,B] [--set spell_oracle=S] <combat-fight.json>...
```

# Input

A fight JSON in the dashboard's shape. The file may be the debugger's envelope `{"fight": {...}, "warnings": [...]}` or a bare fight; a bare fight needs `dur_ms` and `bots`.

```json
{
  "id": "fight-42",
  "commander": "Mychar",
  "dur_ms": 95000,
  "bots": [
    {"name": "Tankbot", "class": "warrior", "role": "tank",
     "spells": [{"n": "Sunder Armor", "c": 14}, {"n": "Shield Slam", "c": 9}]},
    {"name": "Healbot", "class": "paladin", "role": "heal",
     "spells": [{"n": "Holy Light", "c": 31}]}
  ]
}
```

A file that is not a fight (`not a fight: ...`) is a usage error. `bots[].spells` is the module's own count of spell lines, capped at 8 lines per bot.

# Flags

| Flag | Default | Meaning |
|---|---|---|
| `--json` | false | print the reading as JSON, one object per fight |
| `--mt` | the first tank in the fight | main tank name |
| `--ot` | the other tanks | off-tank names, comma separated |
| `--set spell_oracle=S` | `20` | seconds of active time before a bot's core list is judged |

A fight's duration is the denominator of every rate; the oracle's active time is the fight's duration in seconds.

# What is checked

## `mt_sunder`

The main tank is the `--mt` bot when it is a tank in the fight, otherwise the first tank in the fight's order. The row is `na` when there is no tank, when the main tank is not a warrior, or when the fight is shorter than 10 s. Otherwise the check counts `Sunder Armor` casts, computes Sunders per 10 s, and prints:

| Rate per 10 s | Level |
|---|---|
| at least 1.5 | `ok` |
| at least 0.8 | `warn` |
| below 0.8 | `fail` |

The text also names how many times `Shield Slam` was cast.

## `ot_taunt`

Every off-tank is the named `--ot` bots (in the order given), or with no names the tanks other than the main tank. A tank that cast any taunt passes; one that never taunted warns. The taunt list is the Forever oracle's: `Taunt`, `Mocking Blow`, `Challenging Shout`, `Growl`, `Challenging Roar`. With no off-tank the row prints `na: no off-tank in the fight`.

## The oracle line per bot

Every bot is judged against its class oracle the same way [spell-score](spell-score.md) judges a watch, with one difference: the fight is judged after 20 s of it by default, not a trace's 60 s, and the fight's spell lines may be capped, so a core spell the bot might have cast is reported as `unknown` rather than missing. Only warnings print; the line `N bot(s) judged ok` (plus `M not judged`) closes the reading.

# What you see

```
fight fight-42 Mychar 95 s, 2 bots
mt_sunder  ok   1.5/10 s  Tankbot (warrior) cast Sunder Armor 14x in 95 s; Shield Slam 9x
ot_taunt   warn 0x        Nobrain (druid): no taunt
oracle     warn -         Healbot (paladin heal): spec holy (role heal); missing core: Holy Shock
oracle     ok   -         1 bot(s) judged ok, 0 not judged
```

With `--json` the object has `fight`, `commander`, `dur_ms`, `mt_sunder`, `ot_taunt` and `oracle`; each row carries `id`, `level`, `num`, `text` and an optional `bot`.

# Exit codes

`0` always when the readings printed — warnings never fail a fight — and `2` for a usage error or an unreadable file. A `fail` row is information for the debugger; the command is a reading, not a gate.

# Examples

One fight with a named main tank and two off-tanks:

```sh
forever-agent fight-score --mt Tankbot --ot Bearbot,Ottwo ./fights/fight-42.json
```

Judge the oracle only after 30 s:

```sh
forever-agent fight-score --set spell_oracle=30 --json ./fights/fight-42.json
```

# Common problems

- **`not a fight: no duration and no bots`** — the file is not a fight JSON, or the `fight` envelope is missing its body.
- **`mt_sunder na: main tank X is a druid, not a warrior`** — the debugger's Sunder rule is warrior-only by design; a druid tank is still judged by the oracle lines.
- **`mt_sunder na: fight of 8 s is under 10 s`** — too short to judge a per-10-second rate.
- **A bot shows `missing core`** — the fight's spell lines may be capped at 8, in which case the reading says `unknown (list capped at 8)` instead.

# Further reading

- [spell-score](spell-score.md): the same oracle over a watch's combat log.
- [Dashboard](../bots-and-raids/dashboard.md): where the fight JSON comes from.
- [Spells and combat](../player-actions/spells-and-combat.md): the daemon methods behind a fight.
