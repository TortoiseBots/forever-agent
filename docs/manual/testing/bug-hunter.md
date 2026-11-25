---
type: Command
title: Bug hunter
description: Find bot misbehaviour in recorded runs and write the ticket board, with stable ticket ids and a baseline's new/known/gone life.
tags: [testing, bughunt, tickets, watch]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-24T20:35:15Z }
sources:
  - id: bughunt-cmd
    resource: cmd/forever-agent/bughunt_cmd.go
  - id: bughunt-const
    resource: internal/bughunt/const.go
  - id: bughunt-model
    resource: internal/bughunt/model.go
  - id: bughunt-hit
    resource: internal/bughunt/hit.go
  - id: bughunt-tickets
    resource: internal/bughunt/tickets.go
  - id: bughunt-rules
    resource: internal/bughunt/rules_frames.go
  - id: bughunt-events
    resource: internal/bughunt/rules_events.go
  - id: bughunt-loader
    resource: internal/bughunt/load_forever.go
---

`forever-agent bughunt` reads recorded runs offline and names the moments where a bot misbehaved: it was idle in combat, let an ally die at 40% health, stood in fire, stayed stuck, drank while the group fought. Each finding becomes a ticket on a board whose ids stay the same from night to night, so yesterday's baseline turns a growing list into `new`, `known` and `gone`.

Nothing is guessed. A rule whose input a recording does not carry (no combat log, no spell names, no meal report) is a skipped check with its reason, never a hit. This is what makes the board safe to compare across nights.

# Synopsis

```
forever-agent bughunt [--rules LIST] [--names F] [--dispel F] [--tactics F] [--baseline DIR] --out DIR INPUT...
```

# Inputs

Each `INPUT` is one recording:

- a **watch folder** — the folder `forever dungeon-watch` wrote: the run's trace, its `<run_id>.combatlog.jsonl` and its `<run_id>.forever-run.json` summary;
- a **watch trace file** (`.jsonl`) that starts with a trace header;
- a **trials repeat folder** — one with `samples.jsonl` and `events.jsonl`.

Folders are searched recursively. A watch folder or a repeat is one run; a folder holding several runs contributes every run found under it. A `.combatlog.jsonl` is never taken for a trace.

# Flags

| Flag | Default | Meaning |
|---|---|---|
| `--rules` | every rule in the build | comma-separated rule numbers |
| `--names` | empty | live-checked spell names: a JSON object of id to name, or a `spells.json` with a `by_id` map (values may be `{"name": ...}`) |
| `--dispel` | empty | live-checked dispel types: a JSON object of id to `magic`, `curse`, `disease`, `poison` or `never` (or the core's dispel numbers 0-4) |
| `--tactics` | empty | a per-dungeon trash tactics file; rules 16-19 join the registry only when this is passed |
| `--baseline` | empty | a previous run's output directory; its `tickets.json` is the baseline |
| `--out` | required | where the four output files go |

`--names` and `--dispel` are data files read from a live server, never guessed: an id the names file does not name is printed as `spell <id>` and never matches a buff, AoE or heal name. Only harmful ids count as debuffs: an entry is kept when the names file marks its id `"harmful": true` or the dispel entry itself is harmful.

Asking for rules 16-19 without `--tactics` is a usage error that names the missing flag.

# Rules

| # | Rule | Typical input |
|---|---|---|
| 1 | idle in combat > 10 s | frames + combat log |
| 2 | healer not healing a <40% ally for >4 s | combat log + names |
| 3 | an add on a non-tank for >5 s (no aggro) | frames |
| 4 | standing in a damaging AoE > 3 s | combat log + names |
| 5 | stuck >30 s while the group moves | frames |
| 6 | eat or drink while the group is in combat | frames |
| 7 | pet attacks a unit its master is not fighting | frames + pets |
| 8 | buff flip-flop (>3 casts / 60 s / target) | combat log + names |
| 9 | fall or drown death | frames + the run's death log |
| 10 | power at 100% in combat > 30 s while a member is hurt | frames |
| 11 | cast started but nothing lands while mana rises (cast-cancel signal) | combat log |
| 13 | healer under 30% mana out of combat >20 s, no drink, group not moving | frames + meal report |
| 14 | member >40 yd from the group centroid >20 s in combat | frames |
| 15 | missed decurse: a removable debuff >8 s with a free, able dispeller | combat log + names + dispel |
| 16 | trash tactic: a must-interrupt cast completed with an interrupter free | `--tactics` + combat log |
| 17 | trash tactic: crowd control tried on an immune mob | `--tactics` + combat log |
| 18 | trash tactic: a listed dispellable debuff left too long | `--tactics` + combat log |
| 19 | trash tactic: a lower-priority mob killed before a higher one | `--tactics` + combat log |
| 20 | knockback: landed in a pack or off a ledge | combat log (knockback kinds) |

Rule 12 does not exist in this build. Rules 16-19 only join when a tactics file is loaded.

A repeat's raw rows are read as the same combat-log shape on the repeat's own clock. A sparse recording (a walk's 2-14 s frames) can still raise a hit; the hit is marked sparse.

## Why a check was skipped

| Missing input | Rules it silences |
|---|---|
| no combat log | 1, 2, 4, 8, 11, 15, 16-19 |
| combat log holds only rows seen in range (the GM combat-log watch was off or unsupported) | every combat-log rule |
| no combat log with timed rows | every combat-log rule |
| no spell names file | 2, 4, 8 |
| no dispel types file, or no known classes | 15 |
| no meal report | 13 |
| no tactics file | 16-19 |
| no knockback rows | 20 |

# Outputs

`--out DIR` receives four files.

| File | Contents |
|---|---|
| `hits.jsonl` | one hit per line: `rule`, `ds`, `run`, `t`, `clock`, `dur`, `bot`, `cls`, `role`, `ev`, `key`, `deaths`, `sparse`, `cost`, `note` |
| `counts.json` | `runs`, every selected rule's `hits`, `runs` with hits, `skipped` checks and per-dataset counts, plus `skip_reasons` |
| `tickets.json` | the board: one row per (rule, key) |
| `tickets.md` | the board as a Markdown table, worst summed cost first |

A hit's `cost` is its episode length in seconds, plus 60 s for a death of the same member from just before the episode to 15 s after it. A `key` is the hit's own identity (the rule's words, the member, the spell); tickets group by it.

## Tickets

A ticket id is `BH-<rule>-<8 hex of sha1(rule|key)>`, so the same finding keeps its id across nights.

```json
{
  "id": "BH-4-1a2b3c4d",
  "title": "standing in damaging AoE >3 s: Rain of Fire",
  "rule": 4,
  "key": ["4", "Rain of Fire"],
  "count": 6,
  "runs": 2,
  "first_seen": "2026-11-23T21:14:05Z",
  "deaths_near": 1,
  "evidence": ["00:12:31.5 night1/run-01: ..."],
  "labels": ["bughunt", "rule-4", "tank"],
  "status": "new",
  "cost": 41
}
```

Evidence is capped at five lines. For a death near the hit, the member's parsed `.deathrecap` timeline is carried in as extra evidence lines.

| Status | Meaning |
|---|---|
| `new` | the baseline does not hold this ticket |
| `known` | the baseline holds it; keeps its first night |
| `reopened` | the baseline holds it but marked it `gone` |
| `gone` | the baseline holds it and this run did not see it |

A `gone` row only appears when its rule was selected and judged every run in full. A rule that was skipped, only partly judged, or not selected says nothing about its tickets, which keep their previous state.

# What you see

The console prints a run count, one line per rule, the skip reasons, then the worst 15 tickets:

```
runs: 3
rule  1  idle in combat >10 s                                 hits=4    runs=2   skipped=0
skipped rule 15: no dispel types x2
BH-4-1a2b3c4d new        41 s x6     standing in damaging AoE >3 s: Rain of Fire  00:12:31.5 night1/run-01: ...
```

Exit `0` means the hunt ran; a hit total is not an error. Exit `2` is a usage error, a bad input, or an unreadable names/dispel/tactics file.

# Examples

Tonight's runs against last night's board:

```sh
forever-agent bughunt --names spells.json --dispel dispel.json \
    --baseline ./bughunt/2026-11-23 --out ./bughunt/2026-11-24 \
    ./watches/2026-11-24
```

A single rule over one trials repeat:

```sh
forever-agent bughunt --rules 3 --out ./bughunt/one ./runs/2026-11-24/runs/priest-holy-heal-latency/rep1
```

# Common problems

- **`bughunt: no watch trace or trials repeat under ...`** — the input folder has neither a trace header `.jsonl` nor a repeat's `samples.jsonl` + `events.jsonl`.
- **`rule 16 needs --tactics F`** — pass the tactics file, or drop 16-19 from `--rules`.
- **Every rule `skipped=1`** — the run carries no combat log; a trials repeat without `raw-ta.jsonl` or a watch whose log was turned off cannot answer those rules.
- **The board is shorter than last night's** — `gone` rows only appear for rules that ran completely; check `skip_reasons` in `counts.json`.

# Further reading

- [Traces and records](traces-and-records.md): what the watch writes and how its frames are shaped.
- [Dungeon watch](../dungeons/dungeon-watch.md): how a watch run is recorded.
- [Report cards](report-cards.md): the one-line verdict for the same run.
- [Stop reasons](../dungeons/stop-reasons.md): why a watch ended.
