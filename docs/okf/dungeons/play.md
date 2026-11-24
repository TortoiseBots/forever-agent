---
type: Guide
title: Play a dungeon as the player
description: Run `forever dungeon-play`, where the agent is a real player leading the account's own four bots through a dungeon, with no GM help.
resource: https://github.com/TortoiseBots/forever-agent/blob/main/cmd/forever-agent/forever_play.go
tags: [dungeon, play, player, guide]
sources:
  - id: play-cmd
    resource: https://github.com/TortoiseBots/forever-agent/blob/main/cmd/forever-agent/forever_play.go
    title: The command, the dry mode and the verdict
  - id: play-driver
    resource: https://github.com/TortoiseBots/forever-agent/blob/main/internal/forever/driver/play.go
    title: The player run's machine
status: draft
---

# Play a dungeon as the player

`forever dungeon-play` is the player run (#88): the agent logs in as one ordinary character, forms a group of the account's four bots, walks the dungeon plan with them and fights beside them. It is never a GM. Every line it sends is a `.bot` line of the bot-commands v1 contract, or one of the two whisper words (`release`, `corpse run`); a test proves no `.tb`, `.gm` or other `.` line leaves it. The watch mode (`dungeon-watch`) is the other way round: the bots clear, the agent only reads.

## 1. Start a run

```sh
forever-agent forever dungeon-play --plan dungeons/ragefire_chasm.json \
  --bots TANK,HEALER,DPS,DPS --character NAME --account EMAIL \
  --rest host:port --rpc host:port --ca FILE --out runs/rfc --map --navmesh DIR
```

The character starts alive, outside the dungeon, within 300 yd of the plan's portal. The run's clock starts there.

| Flag | Meaning |
|---|---|
| `--plan FILE` | The dungeon plan. A plan the player route refuses (a raid, a hold, an optional leg) stops the command before any login. |
| `--bots A,B,C,D` | The four bots, in order: tank, healer, two damage dealers. The healer must use mana: the pre-boss rest gate reads its mana pool, and a healer whose power is not mana stops the run `boss-not-ready`. |
| `--character NAME` | The player character. |
| `--max-minutes N` | The time budget. 0 takes the plan's: Ragefire Chasm 90, Wailing Caverns 150, else 120. |
| `--max-wipes N` | The wipe budget, 1-20 or -1 for none. Default 2: the third wipe ends the run. |
| `--disband` | Disband the group before the bots log out. |
| `--out DIR` | Where the trace, summary, combat log, report card and map go. The spell judge reads this folder, so a live verdict needs it. |
| `--map` | Also write the run's map (SVG and HTML replay) into `--out`. |
| `--min-active SEC` | Seconds a bot must be alive and in combat before the judge scores it (default 30). |
| `--dry` | Play the whole machine on a scripted world: no network, every pack and boss of the plan, bots that cast. |

## 2. The verdict

The command ends with one line, `verdict: PASS` or `verdict: FAIL: <reasons>`, and exits 0 on a pass. A run passes only when all of these hold:

- **Clear.** Every boss of the plan is dead (`clear=true`).
- **Wipes.** At most two, whatever `--max-wipes` allowed.
- **No GM line.** `gm_lines` is 0.
- **Time.** The run took no longer than the plan's cap (90 minutes for Ragefire Chasm, 150 for Wailing Caverns), whatever `--max-minutes` allowed.
- **Spell judge.** All four bots were judged by the same judge as `forever spell-score` and none failed. A bot the judge could not score (no class, too little fight) is no evidence and fails the verdict.

A dry run without `--out` has nothing to judge; it says so and the verdict stands on the other rules. A lost link fails the run and exits 7.

## 3. What the run writes

Into `--out`: the trace (`<run>.jsonl`, with the bots as party records by slot name and frames of the party and the hostile units), the summary (`<run>.forever-run.json`), the combat log (`<run>.combatlog.jsonl`), the report card and, with `--map`, the map.

The summary has a `play` record: `minutes`, `max_minutes`, `max_wipes`, `gm_lines`, `lines_by_prefix` (every line the send gate let through, by its first words, for example `.bot add`), and the counts of frames and combat rows. The trace names bots by role (tank, heal, dps1, dps2), never by character.
