# What real WoW Forever players use (aotc.gg, read 2026-11-24)

Statistics from aotc.gg, which reads the official WoW Forever armory. They show what real players run on the
Forever talent trees and items. The agent's test bench uses them as a reference for "what a player of this spec
normally does" when it scores bots. They are player choices, not game facts: for a spell, talent or item fact the
server's own data wins when the two disagree.

All numbers cover every region and realm type. "60" means level 60 characters. A spec on aotc.gg is the tree that
holds the most points. No player names or guilds are kept here: every file is an aggregate.

Source: https://www.aotc.gg/forever (data from aotc.gg).

| File | What |
|---|---|
| `talent-picks.csv` | For every spec, level group (`10-19` … `50-59`, `60`, `all`) and talent: the share of characters with enough points who took it (`pick_pct`), the usual rank and the talent's spell id. |
| `talent-tree-points.csv` | Average points per tree for every spec and level group. |
| `talent-splits-60.csv` | The 20 most common point splits (e.g. `12/8/31`) per spec at 60, with share and character count. |
| `stat-priority-60.csv` | The stat priority per spec at 60: order, stat and weight, highest first. Stats marked as about equal share an order number. |
| `gear-60.csv` | The 20 most worn items per slot, and item-set bonus use, per spec at 60. `population` is `best_geared` or `all_60`. `wowhead_id` is the item id. |
| `enchants-60.csv` | The most used enchants per slot, per spec at 60, in the same two populations. |
| `spec-population.csv` | The share of each spec among a class's level 60 characters. |
| `census-classes.csv` | Every class: characters tracked, faction counts, characters at 60. |
| `census-races.csv` | Every race: faction, share of its faction, share of female characters. |
| `census-class-gender.csv` | Share of female characters per class. |
| `census-race-class-pairs.csv` | The 10 most and 10 least played race and class pairs. |
| `census-levels.csv` | Characters per level from 10 to 60. |
| `behaviour-stats.csv` | Lifetime totals: causes of death, how players travel, what they consume. |
| `new-forever-bosses.csv` | Bosses that are new in WoW Forever, their instance, and the share of characters who killed each. |
| `legacy-achievements.csv` | The achievements that award a Legacy Point. |

## Limits

- The site reads the live game. A private server build can differ: check a talent or item against the server
  before code relies on it.
- These are human choices. Bots should follow the common ones, not every outlier: rows with a low `pick_pct` are noise.
- There is no levelling route data. The level groups only tell how talent points are spent.

## Refreshing

The files are produced outside this repository and copied in whole; do not edit them by hand.
