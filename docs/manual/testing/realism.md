---
type: Guide
title: Realism checks
description: "Take a bot snapshot with forever bot-snapshot, judge it against the aggregate player data with trials realism, and read the before/after of a realism batch."
tags: [testing, trials, realism, talents, gear, enchants, player-data]
status: stable
generated: { by: forever-agent docs, at: 2026-11-25T14:00:00Z }
sources:
  - id: realism
    resource: internal/trials/realism/snapshot.go
  - id: realism-judge
    resource: internal/trials/realism/judge.go
  - id: realism-talents
    resource: internal/trials/realism/talents.go
  - id: realism-gear
    resource: internal/trials/realism/gear.go
  - id: realism-stats
    resource: internal/trials/realism/stats.go
  - id: realism-enchants
    resource: internal/trials/realism/enchants.go
  - id: realism-report
    resource: internal/trials/realism/report.go
  - id: bot-snapshot
    resource: cmd/forever-agent/forever_botsnapshot.go
  - id: trials-realism
    resource: cmd/forever-agent/trials_realism_cmd.go
  - id: spell-diff
    resource: cmd/forever-agent/forever_spelldiff.go
  - id: spelljudge-diff
    resource: internal/forever/spelljudge/diff.go
  - id: playdata-gear
    resource: internal/trials/playdata/gear.go
  - id: aotc
    resource: data/aotc/aotc.go
---

The realism checks answer one question per bot: does this bot have what a real player of its class, spec and level has? They compare a **bot snapshot** — a file listing each bot's talents, worn items and enchants — with the aggregate WoW Forever player statistics in `data/aotc` (read 2026-11-24 from the official armory statistics site named in that directory's README: player choices, not game facts, so the server's own data wins on any talent or item fact).

Nothing here scores a fight: the checks read one snapshot file and print a verdict per bot and per spec. Every verdict is `PASS`, `FAIL` or `INCONCLUSIVE`, and none of them fails on missing data — a field the snapshot does not carry leaves the part that needs it `INCONCLUSIVE` with the reason stated.

```sh
forever-agent forever bot-snapshot --dir ./serve --out bots.json
forever-agent trials realism --snapshot bots.json
```

# Taking a snapshot

```sh
forever-agent forever bot-snapshot --dir DIR [--bots NAME,...] [--wait MS] [--out FILE]
```

The command reads the serve running in `DIR` (a `forever serve --dir DIR`) through the methods it already has and writes the snapshot as JSON to `--out FILE`, or to stdout without it. `--bots` limits the grouped bots it reads to the names given, and `--wait` (0-2000 ms, the serve's own bound) lets the model catch up before the read.

Bots are named `bot1`, `bot2`, … in the file, never by character name: `bot1` is the character the serve runs, then its grouped bots in name order. A party member the serve proves no class or level for is skipped and named on stderr.

| Field | Where it comes from |
|---|---|
| `id` | assigned by the command (`bot1` …) |
| `class`, `level` | the `unit` row of the character the serve runs; `bot_view`'s unit row for a grouped bot |
| `items` (slot, item id, name, quality, item level, required level) | `equipment` for the character the serve runs (the bot it owns), joined with `item_info` for the levels and quality |
| `spec` | not read: no module read-out names a bot's spec, so every bot's spec is empty and the talent, stat and enchant checks answer `INCONCLUSIVE` for it |
| `talents` | not read: the module proves learned spells only for the ids it is asked to learn, never the whole build |
| `enchant_id` | not read: `equipment` names no enchant |
| item `source` | not read: nothing says where a worn item comes from |

A grouped bot's worn items are not read either: `equipment` answers only for the character the serve runs, and the `unit` and `bot_view` rows carry no worn items. Each bot's `notes` in the file state those missing read-outs, so the report says what it could not judge instead of guessing.

# The snapshot file

`forever-bot-snapshot/1`, one JSON object. The loader is strict: an unknown key (a `name` key included), an id that is not `botN`, a repeated id or slot, a slot the server does not have, a level outside 1..60, a quality outside the item ladder, an unknown `source` word or a bad `taken` time is refused with the field named.

```json
{
  "format": "forever-bot-snapshot/1",
  "taken": "2026-11-25T12:00:00Z",
  "source": "forever bot-snapshot",
  "bots": [
    {
      "id": "bot1",
      "class": "druid",
      "spec": "",
      "level": 60,
      "items_known": true,
      "items": [
        {"slot": "head", "item_id": 22267, "name": "Spellweaver's Turban", "item_level": 62,
         "required_level": 58, "quality": 3, "stats": ["Intellect"], "enchant_id": 0, "source": "dungeon"}
      ],
      "talents_known": false,
      "talents_complete": false,
      "talents": [],
      "notes": ["spec: no read-out names this character's spec"]
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `slot` | one of the core's 19 equipment slots: `head`, `neck`, `shoulders`, `body`, `chest`, `waist`, `legs`, `feet`, `wrists`, `hands`, `finger1`, `finger2`, `trinket1`, `trinket2`, `back`, `main_hand`, `off_hand`, `ranged`, `tabard` |
| `item_level`, `quality`, `enchant_id`, `source` | left out when not read; `enchant_id` `0` means read and unenchanted, and `source` is one of `quest`, `dungeon`, `raid`, `pvp`, `reputation`, `world`, `crafted`, `vendor` |
| `stats` | the item's primary stats as names (`Intellect`, `Spirit`, …), when known |
| `talents` | `spell_id` (the talent's own spell, what a learn read-back proves), `rank` 1-5 and optionally `tab` 0-2 |
| `items_known`, `talents_known` | whether the taker read them at all: false is "not read", never "nothing worn" |
| `talents_complete` | true when the list is the whole build; a partial list is `INCONCLUSIVE`, never a `FAIL` on the points that are there |

A bot that wears a known, empty outfit (`items_known` true and no items) is a bot with nothing on, not a missing read.

# Judging a snapshot

```sh
forever-agent trials realism --snapshot FILE [--baseline FILE] [--json]
```

The report prints a per-check table, a per-spec table (each bot's class and spec, `PASS`/`FAIL`/`INCONCLUSIVE` counts and the mean of the numbers the check measured) and, per bot, each check with its number and, for a `FAIL`, the slots or talents that caused it. `--json` prints one object: `reading` (its `bots`, each with `checks` whose `parts` carry the verdict, the value and the reason; then `specs`, the per-check `checks`, and the data `credit`), and `diff` when `--baseline` is given. Every report carries the data credit.

With `--baseline`, the older snapshot's reading is compared with the newer one per check, per spec and per bot: fewer `FAIL`s (or more `PASS`es with the fails equal) is `better`, the other way is `worse`, and a bot whose verdict moved is listed with both words (bots are paired by their `botN` id, so a bot that is in only one of the two snapshots is not listed).

Exit codes: `0` no `FAIL` in the snapshot given, `1` at least one `FAIL`, `3` when no check could be judged at all (every check `INCONCLUSIVE`, so the snapshot carried too little data — a report that assessed nothing is not a pass), `2` a usage error, a malformed snapshot, or a snapshot or baseline that does not load. The report's header prints its coverage (`coverage: N check(s) judged, M INCONCLUSIVE`).

# The checks

Each check is made of parts; a part is judged only from data the snapshot carries. The check is `FAIL` when any judged part fails, `PASS` when at least one part was judged and none failed (the reason then names the parts that were not judged), and `INCONCLUSIVE` when no part could be judged.

## Talents by level group (`REALISM-TALENTS`)

Per bot, against the common picks of its own level group (`talent-picks.csv`, the same 50% the player-data talent rule reads):

- **point_share** — the share of the bot's talent points that sit in the talents at least **50%** of its level group's players of the spec took. `PASS` at **80%** or more: four points in five follow the common build, and a spec's off-picks (a hybrid's second tree, a PvP talent) are a fifth of a build at most.
- **split** (level 60 only) — the bot's points per tree against the nearest of the spec's 20 most common splits in `talent-splits-60.csv`, counted as the points moved between trees (half the sum of the tree differences). `PASS` at **5 points** or fewer from the nearest common split.

The build must be the whole build: a list with fewer points than the character's level has (one point per level from 10, 51 at 60) is `INCONCLUSIVE` whatever `talents_complete` says, because its missing points would read as off-picks.

INCONCLUSIVE: no talents in the snapshot, a partial list (`talents_complete` false or too few points for the level), a spec the tables do not name, a level with no talent points, and no common picks in the file. Points in talents the tables do not name leave only the `split` part `INCONCLUSIVE` (it would be a guess); the point share still judges.

## Gear realism (`REALISM-GEAR`)

The main check, for every bot whose items were read. It reports the average item level, the quality mix and the spread of the item levels, and judges two parts:

- **item_level** — the share of slots whose item level is above the bot's level plus its band's allowance. `PASS` at **10%** or less (one slot in ten), so a single noisy reading is not a fault while a bot dressed for a later band shows several.

  | Bot level | Allowance |
  |---|---|
  | 1-19 | 6 |
  | 20-29 | 8 |
  | 30-39 | 10 |
  | 40-49 | 12 |
  | 50-59 | 15 |
  | 60 | 30 |

  The allowances follow the item level of the best gear each band's own content drops; level 60's 30 covers the epic high end of the era, which is what a player of that level could have. Raid gear on a pool bot is the `source` part's business.
- **source** — any worn item whose `source` is `raid`, `pvp` or `reputation` fails, on a snapshot of pool bots. Every worn item must carry a source for the part to pass: one permitted item beside an unread one is `INCONCLUSIVE`, because the unread one could be the raid piece.

INCONCLUSIVE: no items read, no item carries an item level (the item read-out is missing), and any worn item carries no source.

## Stat priority (`REALISM-STATS`)

Level 60 only (`stat-priority-60.csv`). Two parts:

- **fits** — the player-data gear rule itself (`AOTC-GEAR-FITS-SPEC`, see [trials](trials.md)): every worn item must fit the spec, read through the items its own players wear in that slot, and through the item's own stats when the snapshot carries them.
- **priority** — every item whose own stats name a stat family must name one of the families among the spec's top priority stats: a healer's gear must not be strength gear. Stamina, crit and hit name no family, so an item that carries only those is not judged.

INCONCLUSIVE: a level other than 60, no items read, no spec, no item with stats (the `priority` part), an item the tables cannot place (the `fits` part).

## Enchants (`REALISM-ENCHANTS`)

Level 60 only (`enchants-60.csv`, best geared population), per slot the tables name (Head, Neck, Wrist, Hands, Feet, Chest, Legs, Back, Main Hand, Off Hand, Ranged, Rings). The population splits into the share that leaves a slot unenchanted (the `No enchant` row) and each enchant's share:

- **present** — a slot the players mostly enchant (fewer than **50%** leave it bare) carries some enchant.
- **empty** — a slot the players mostly leave empty (at least **50%** leave it bare) carries none.

Both parts judge only slots whose enchant was read, and one unread slot in the same group leaves the part `INCONCLUSIVE`: a bare slot it could not read would otherwise pass.
- **common** — where one enchant holds at least **50%** of the slot, that enchant is the one carried. The tables name the enchant's **item** (its scroll), not the enchant the server stores on a worn item, so a snapshot's enchant id can be compared only when the taker reads that scroll id; an id that does not match is `INCONCLUSIVE` with that reason rather than a `FAIL`.

INCONCLUSIVE: a level other than 60, no items read, no spec, no `enchant_id` read (or one applicable slot without one), and a slot with no table row (shoulders, the waist, the trinkets).

## Spells before/after

```sh
forever-agent forever spell-diff [--json] [--min-active 30] [--fail-unknown] [--oracle DIR] [--spells DIR] <before> <after>
```

The spell check is `forever spell-score`'s: each party member's casts judged against the oracle of its class (see [forever spell-score](spell-score.md)). `spell-diff` runs that same judge on two runs — two watch recordings, or two saved `forever spell-score --json` answers — and prints what changed per class: the class's cast total, every spell whose count moved (or that was added or removed), and every member whose verdict moved. `--json` prints the comparison as one object (`format`, `before`, `after`, `classes`). It exits `0` when both sides read, `2` on a usage or load error: a before/after reports, it does not judge.

# Known limits

- The snapshot command reads what the serve can already read: a bot's spec, its talents, its enchants and its items' sources are not read today, so most checks report `INCONCLUSIVE` on a taken snapshot until a module read-out exists for them (a bot's whole build, its spec, an item's enchant and an item's source).
- The player data is a snapshot of player choices from 2026-11-24, not game facts; the server's own data wins, and the tables carry no item stats or armour types.
- The checks read one file at a time: a before/after is two snapshots taken at two moments, not two runs of one bot pool.

# Further reading

- [Trials](trials.md): the behaviour bench, its player-data rules and the class rules.
- [forever spell-score](spell-score.md): the spell check the before/after compares.
- [Report cards](report-cards.md): how a run's card is built.
