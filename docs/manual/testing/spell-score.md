---
type: Command
title: forever spell-score
description: "Judge a Forever class fight from its watch combat log: each party member's casts against the oracle of its class, failing only wrong-class abilities."
tags: [testing, spell-score, spells, oracle, forever]
status: stable
generated: { by: forever-agent docs, at: 2026-11-25T14:00:00Z }
sources:
  - id: spellscore-cmd
    resource: cmd/forever-agent/forever_spellscore.go
  - id: spelljudge
    resource: internal/forever/spelljudge/spelljudge.go
  - id: spelljudge-diff
    resource: internal/forever/spelljudge/diff.go
  - id: spell-diff
    resource: cmd/forever-agent/forever_spelldiff.go
  - id: kb-judge
    resource: internal/kbspells/judge.go
  - id: kb-forever
    resource: internal/kbspells/forever.go
  - id: oracle-warrior
    resource: internal/kbspells/forever/warrior.json
  - id: oracle-common
    resource: internal/kbspells/forever/_common.json
---

`forever-agent forever spell-score` reads a Forever watch recording offline and judges each party member's combat against the spell oracle of its class: did the warrior press its core abilities, did the paladin cast something a paladin cannot have, did anyone press a spell no class owns. It never logs in and never changes anything; it only reads the files.

One fault fails the command: a cast of **another class's class ability** (a paladin casting a warrior's Sunder Armor, a mage casting a priest's Renew). Rotation opinions — a missing core spell, a never spell — are warnings, because a verdict that depends on how a bot "should" play would fail good bots. Unknown spell ids fail only when you ask for it.

# Synopsis

```
forever-agent forever spell-score [--json] [--min-active 30] [--fail-unknown] [--oracle DIR] [--spells DIR] <watch dir|trace>...
```

# Flags

| Flag | Default | Meaning |
|---|---|---|
| `--json` | false | print one JSON object per input instead of text lines |
| `--min-active` | `30` | seconds a member must be alive and in combat before its core list is judged |
| `--fail-unknown` | false | fail a member that cast a spell neither the oracle row nor the spell table names |
| `--oracle` | the embedded Forever oracle | a directory holding `_common.json` and one `<class>.json` per class |
| `--spells` | the embedded spell table | a directory holding `spells.tsv` and `names.tsv` (or `names.tsv.gz`) |

Each input is a watch folder or a watch trace file, both read the same way the [bug hunter](bug-hunter.md) reads them.

# What is judged

- **Casts** are counted from the watch's combat log: one `spell_go` row per cast, attributed to the member that produced it.
- **Active time** is the sum of the gaps between consecutive frames where the member is alive and in combat in both, each gap capped at 3 s. A member with less active time than `--min-active` is not judged on its core list; long gaps are not evidence of a fight.
- **Spec** is inferred when the recording carries none: the role picks a tank or healer spec; a dps bot is the spec of its class whose signature spell it cast most, and a bot with no signature (or a tie) is judged against the class-wide lists (the intersection of the class's dps cores).
- **Core spells** with no cast are warnings, except for a spell whose first rank the member is too low a level to have learnt.
- **Never spells** (a spec's "must not press" list, such as a dps warrior pressing Taunt) are warnings.
- **Wrong class** is a failure: a spell the spell table assigns to another class.
- **Unknown ids** are a failure only with `--fail-unknown`: a spell neither the row nor the table names is usually a custom spell or an item.

The oracle's own rules live in `internal/kbspells/forever`: one file per class with `specs`, each holding `core`, `situational`, `never`, `signature` and `taunt` lists, plus `_common.json` with taunts, generic class spells, ignore list and limits. The embedded oracle is cross-checked against the Forever spell table at load; a build without that data answers with an error instead of a silent pass, and `--oracle`/`--spells` let a test point both at a directory.

# What you see

Text output is one line per party member, in the party's order:

```
Mychar (warrior tank): spec protection (role tank); missing core: Shield Slam
Healbot (paladin heal): spec holy (role heal); ok; wrong class: Sunder Armor (warrior)
Castbot (mage dps): spec frost (signature Frostbolt 41); ok
```

A member the recording has no class for prints `no class in the trace` and is not judged.

With `--json`:

```json
{
  "run": "r-20261124T190001-a1b2",
  "findings": [
    {
      "bot": "0x00000000000000A1",
      "class": "paladin",
      "role": "heal",
      "spec": "holy",
      "spec_how": "role heal",
      "level": "fail",
      "text": "spec holy (role heal); ok; wrong class: Sunder Armor (warrior)",
      "wrong_class": ["Sunder Armor (warrior)"],
      "casts": 96
    }
  ]
}
```

The finding object also carries `missing`, `unknown`, `never` and `off_list` lists when they are not empty, `unknown_ids` with `--fail-unknown`, and `spells`: the member's own cast count per spell name, which is what the two-run comparison below reads.

# Comparing two runs

```sh
forever-agent forever spell-diff [--json] [--min-active 30] [--fail-unknown] [--oracle DIR] [--spells DIR] <before> <after>
```

`spell-diff` runs the same judge on both sides and prints what changed per class: the class's cast total, every spell whose count moved (added or removed when one side has none) and every member whose verdict moved:

```
warrior: 60 cast(s) -> 70 cast(s)
  Shield Slam: 10 -> 20 (+10)
  Taunt: removed (3)
  Maintank (protection): pass -> warn
    missing core: Shield Slam
```

| Flag | Default | Meaning |
|---|---|---|
| `--json` | false | print the comparison as one object (`format`, `before`, `after`, `classes`) |
| `--min-active` | `30` | seconds a member must be alive and in combat before its core list is judged |
| `--fail-unknown` | false | judge an unknown cast as a failure on both sides |
| `--oracle` / `--spells` | the embedded data | the oracle directory and the spell table directory |

Each side is a watch folder or a watch trace, or a saved `forever spell-score --json` answer (a `.json` file holding its `findings`). Exit `0` when both sides read, `2` on a usage or load error: a comparison reports, it does not judge.

# Exit codes

| Code | Meaning |
|---|---|
| `0` | every member passed |
| `1` | at least one member failed (wrong class, or an unknown id under `--fail-unknown`) |
| `2` | usage error, an unreadable input, or an oracle/spell-table load error |

# Examples

One watch:

```sh
forever-agent forever spell-score ./watches/2026-11-24/run-1
```

Every watch trace under a folder, as JSON:

```sh
forever-agent forever spell-score --json --min-active 20 ./watches/2026-11-24/*.jsonl
```

A test run against an edited oracle:

```sh
forever-agent forever spell-score --oracle ./testdata/oracle --spells ./testdata/spells ./watches/run-1
```

# Common problems

- **`no Forever spell table in this build; pass --spells`** — the build carries no generated spell data; point `--spells` at a directory with `spells.tsv` and `names.tsv`.
- **Everyone `not judged: 4 s active of 30 s`** — the recording is shorter than the threshold, or the party was out of combat for most of it. Lower `--min-active` or use a longer fight.
- **A member fails on a spell you did not expect** — the spell table assigns that id to another class; check the id and name in the fight's combat log. A custom spell with no class is off the wrong-class check.
- **`not a watch trace` style errors** — pass a watch folder or a trace `.jsonl`, not a `.combatlog.jsonl`.

# Further reading

- [fight-score](fight-score.md): the same oracle over a finished fight JSON from the dashboard.
- [Traces and records](traces-and-records.md): the trace and combat log a watch writes.
- [Bug hunter](bug-hunter.md): behavioural findings from the same recording.
