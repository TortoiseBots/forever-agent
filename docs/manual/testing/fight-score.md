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
  - id: oracle-turtle-common
    resource: internal/kbspells/oracle/_common.json
  - id: forever-oracle
    resource: internal/kbspells/forever.go
  - id: tracecheck
    resource: internal/tracecheck/tracecheck.go
---

`fight-score` reads a finished fight the way the dashboard's fight debugger does and prints the same two tank checks, plus one oracle line per bot with a warning. It is the quick offline look at a single fight: did the main tank keep Sunder Armor up, did the off-tank taunt at all, and did anyone miss a core spell. It reads files only and never logs in.

The default spell data is the Turtle knowledge base; a Forever (Classic 1.60) run's fight record is scored against the
Forever spell data with `--set spell_oracle=forever`. No fight record names its core, so the selected oracle is used as
asked, and an explicitly selected Forever oracle adds a warning to the reading that the record could not confirm its
origin. Every reading names the oracle that produced it.

# Synopsis

```
forever-agent fight-score [--json] [--mt NAME] [--ot A,B] [--set spell_oracle=SECONDS|forever] <combat-fight.json>...
```

# Input

A fight JSON in the dashboard's shape. The file may be the debugger's envelope `{"fight": {...}, "warnings": [...]}`, a bare fight, or the module's `COMBAT_FIGHT` datagram (the fight is under its `fight` key); a bare fight needs `dur_ms` and `bots`.

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
| `--set spell_oracle=SECONDS\|forever` | `20` | seconds of active time before a bot's core list is judged; the word `forever` selects the Forever spell data instead |

A `spell_oracle` value of `forever` (any case) selects the Forever spell data; any other value is a number and sets the active-seconds threshold, so the two may be given together (`--set spell_oracle=forever --set spell_oracle=30`). `--set` takes any known trace-score threshold id, but only the oracle one changes a fight score; an unknown id is refused (`--set: no threshold for check "x" (have ...)`).

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

Every off-tank is the named `--ot` bots (in the order given), or with no names the tanks other than the main tank. A tank that cast any taunt passes; one that never taunted warns. The taunt list is the selected oracle's: the Turtle list is `Taunt`, `Growl`, `Hand of Reckoning`, `Mocking Blow`, `Challenging Shout` and `Challenging Roar`; the Forever list is the same without `Hand of Reckoning` (a Turtle spell, so it reads as no taunt under the Forever data). With no off-tank the row prints `na: no off-tank in the fight`.

## The oracle line per bot

Every bot is judged against its class spec in the selected data the same way [spell-score](spell-score.md) judges a watch, with one difference: the fight is judged after 20 s of it by default, not a trace's 60 s, and the fight's spell lines may be capped, so a core spell the bot might have cast is reported as `unknown` rather than missing. Only warnings print; the line `N bot(s) judged ok` (plus `M not judged`) closes the reading.

# What you see

```
fight fight-42 Mychar 95 s, 2 bots, spell oracle turtle
mt_sunder  ok   1.5/10 s  Tankbot (warrior) cast Sunder Armor 14x in 95 s; Shield Slam 9x
ot_taunt   warn 0x        Nobrain (druid): no taunt
oracle     warn -         Healbot (paladin heal): spec holy (role heal); missing core: Holy Shock
oracle     ok   -         1 bot(s) judged ok, 0 not judged
```

The header names the oracle that produced the reading (`turtle` or `forever`). With `--json` the object has `fight`, `commander`, `spell_oracle`, `dur_ms`, `warnings`, `mt_sunder`, `ot_taunt` and `oracle`; each row carries `id`, `level`, `num`, `text` and an optional `bot`. `warnings` is the reading's own caveats, `[]` when it has none.

# Score a Forever run's fight

```sh
forever-agent fight-score --set spell_oracle=forever <walk-dir>/combat-fight-1.json
```

`--set spell_oracle=forever` selects the oracle and common lists of the Forever spell data: each bot is judged against its Forever class spec, and `ot_taunt` uses the Forever taunt list (no `Hand of Reckoning`). The input is the module's fight record in the forms it is stored in: the dashboard detail `{fight, warnings}`, the bare fight object, or the `COMBAT_FIGHT` datagram. A numeric `spell_oracle` still sets the active-seconds threshold, so both may be given:

```sh
forever-agent fight-score --set spell_oracle=forever --set spell_oracle=30 <walk-dir>/combat-fight-1.json
```

No fight record names its core, so a reading with an explicit `spell_oracle=forever` carries a warning that the record could not confirm its origin:

```
fight 5-3 Glacienne 52 s, 39 bots, spell oracle forever
warning: no provenance marker in the fight record (no core or build field): scored with the Forever spell data as selected (spell_oracle=forever); the record does not name its core
```

The warning is part of the result: in the JSON it is the `warnings` array (`[]` on a reading that needs none) beside `spell_oracle`, and the text prints one `warning:` line per warning. It does not fail the command (exit stays 0), and no record shape is refused: without the option the Turtle data is read, as before.

# Exit codes

`0` always when the readings printed — warnings never fail a fight — and `2` for a usage error or an unreadable file. An unknown `--set` id or value is a usage error, and so is a `spell_oracle=forever` whose Forever data does not load (`--set spell_oracle=forever: ...`). A `fail` row is information for the debugger; the command is a reading, not a gate.

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
- **`warning: no provenance marker in the fight record ...`** — expected under `--set spell_oracle=forever`: no fight record names its core, so the selected oracle was used and the reading says the record could not confirm it. It is not an error.
- **`mt_sunder na: main tank X is a druid, not a warrior`** — the debugger's Sunder rule is warrior-only by design; a druid tank is still judged by the oracle lines.
- **`mt_sunder na: fight of 8 s is under 10 s`** — too short to judge a per-10-second rate.
- **A bot shows `missing core`** — the fight's spell lines may be capped at 8, in which case the reading says `unknown (list capped at 8)` instead.

# Further reading

- [spell-score](spell-score.md): the same oracle over a watch's combat log.
- [Dashboard](../bots-and-raids/dashboard.md): where the fight JSON comes from.
- [Spells and combat](../player-actions/spells-and-combat.md): the daemon methods behind a fight.
