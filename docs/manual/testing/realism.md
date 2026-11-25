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

## What the taker fills today

`forever bot-snapshot` fills **class, level and worn items for the agent's own character** (the serve's login character, `bot1`), and **class and level for the party bots**. It fills **nothing else**: `spec`, `talents`, `stats`, `enchants` and an item's `source` are empty for every bot today. On a real server that means most checks are `INCONCLUSIVE`: TALENTS, STATS and ENCHANTS for every bot, and GEAR for every party bot; only `bot1`'s gear has item levels and required levels, and even there the `source` part is unread, so the GEAR check as a whole is `INCONCLUSIVE` rather than a `PASS` over a partial reading. The command reads only what the serve already exposes (`status`, `unit`, `equipment`, `item_info`, `bot_view`); the rest needs **a read-out from the bot module**, which does not exist on this side yet: a ticket exists on the module side for it (this page does not invent its commands).

The snapshot format is ready for that read-out. For every bot the module read-out must provide:

| Field | What the module must report |
|---|---|
| `spec` | the bot's spec as the player tables name it (`balance`, `holy`, `beast-mastery`, …) |
| `talents_known`, `talents_complete` | whether the whole learned build was read, not only a marker kit |
| `talents` | each learned talent by its **rank-1 spell id** and the rank learned (not the id of the learned rank) |
| `items_known`, `items` | the whole worn outfit for party bots too, not only the serve's own character |
| `items[].enchant_id` | the applied enchant id, 0 for read-and-unenchanted |
| `items[].source` | one of `quest`, `dungeon`, `raid`, `pvp`, `reputation`, `world`, `crafted`, `vendor` |
| `items[].stats_known`, `items[].stats` | whether the item's primary stats were read, and the names (`Intellect`, `Spirit`, …) |
| `items[].item_level`, `items[].required_level` | from the server's item record |

Until then a snapshot is a class-and-level file plus one character's outfit: useful as a format check, not as a realism verdict.

The realism checks answer one question per bot: does this bot have what a real player of its class, spec and level has? They compare a **bot snapshot** with the aggregate WoW Forever player statistics in `data/aotc` (read 2026-11-24 from the official armory statistics site named in that directory's README: player choices, not game facts, so the server's own data wins on any talent or item fact). Every number below is a **starting value to tune**, not a fact about players; each section says where it came from.

Nothing here scores a fight: the checks read one snapshot file and print a verdict per bot and per spec and level band. Every verdict is `PASS`, `FAIL` or `INCONCLUSIVE`, and a check never fails on missing data (it is `INCONCLUSIVE` instead), and never passes over a missing **decisive** part (see below).

```sh
forever-agent forever bot-snapshot --dir ./serve --out bots.json
forever-agent trials realism --snapshot bots.json
```

# Taking a snapshot

```sh
forever-agent forever bot-snapshot --dir DIR [--bots NAME,...] [--wait MS] [--out FILE]
```

The command reads the serve running in `DIR` (a `forever serve --dir DIR`) through the methods it already has and writes the snapshot as JSON to `--out FILE`, or to stdout without it. `--bots` limits the grouped bots it reads to the names given, and `--wait` (0-2000 ms, the serve's own bound) lets the model catch up before the read. Before writing, the command loads its own output with the snapshot loader: a serve answer that would produce a file `trials realism` refuses (a level outside 1..60, say) fails the command instead of writing a file after a success line.

Bots are named `bot1`, `bot2`, … in the file, never by character name: `bot1` is the character the serve runs, then its grouped bots in name order. A party member the serve proves no class or level for is skipped and named on stderr. **A `botN` id is not an identity**: it is assigned by name order in each snapshot, so `bot1` in two snapshots can be two different characters (this is why the before/after never pairs bots).

| Field | Where it comes from today |
|---|---|
| `id` | assigned by the command (`bot1` …) |
| `class`, `level` | the `unit` row of the character the serve runs; `bot_view`'s unit row for a grouped bot |
| `items` (slot, item id, name, quality, item level, required level) | `equipment` for the character the serve runs, joined with `item_info` for the levels and quality |
| `spec` | not read: no module read-out names a bot's spec |
| `talents` | not read: the module proves learned spells only for the ids it is asked to learn, never the whole build |
| `enchant_id` | not read: `equipment` names no enchant |
| `stats` | not read: `equipment` names no stats, so `stats_known` is false on every item |
| item `source` | not read: nothing says where a worn item comes from |

A grouped bot's worn items are not read either: `equipment` answers only for the character the serve runs, and the `unit` and `bot_view` rows carry no worn items. Each bot's `notes` in the file state those missing read-outs, so the report says what it could not judge instead of guessing.

# The snapshot file

`forever-bot-snapshot/1`, one JSON object. The loader is strict: an unknown key (a `name` key included), an id that is not `botN`, a repeated id or slot, a slot the server does not have, a level outside 1..60, a quality outside the item ladder, an unknown `source` word or a bad `taken` time is refused with the field named. With the player tables at hand it also refuses a **malformed build**: a talent rank above that talent's own maximum rank, or a talent list with more points than the level allows (one point per level from 10). A talent id the tables do not name is not refused; the TALENTS check reports it `INCONCLUSIVE` (a module may use another id, and a bad id must never read as a realism `FAIL`).

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
         "required_level": 58, "quality": 3, "stats_known": true, "stats": ["Intellect"],
         "enchant_id": 0, "source": "dungeon"}
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
| `item_level`, `required_level`, `quality`, `enchant_id`, `source` | left out when not read; `enchant_id` `0` means read and unenchanted, and `source` is one of `quest`, `dungeon`, `raid`, `pvp`, `reputation`, `world`, `crafted`, `vendor` |
| `stats_known`, `stats` | whether the item's primary stats were read, and their names (`Intellect`, `Spirit`, …); `stats` without `stats_known` is refused |
| `talents` | `spell_id` (**the talent's rank-1 spell id**, the id the player tables key it by), `rank` 1-5 and optionally `tab` 0-2 |
| `items_known`, `talents_known` | whether the taker read them at all: false is "not read", never "nothing worn" |
| `talents_complete` | true when the list is the whole build; a partial list is `INCONCLUSIVE`, never a `FAIL` on the points that are there |

A bot that wears a known, empty outfit (`items_known` true and no items) is a bot with nothing on, not a missing read; the checks say "the bot wears nothing".

# Judging a snapshot

```sh
forever-agent trials realism --snapshot FILE [--baseline FILE] [--json]
```

The report prints a per-check table, a per-spec-and-level-band table (class, spec, band, `PASS`/`FAIL`/`INCONCLUSIVE` counts and, when at least five bots were judged, the mean of the numbers the check measured; below five the cell says `n=N` because one bot is not a spec) and, per bot, each check with its number and, for a `FAIL`, the slots or talents that caused it. `--json` prints one object: `reading` (its `bots`, each with `checks` whose `parts` carry the verdict, the value and the reason; then `specs`, the per-check `checks`, and the data `credit`), and `diff` when `--baseline` is given. Every report carries the data credit.

A check is made of parts. A part is **decisive** when the check needs it to conclude: when a decisive part is not judged, the check is `INCONCLUSIVE` with that reason, never a `PASS` over the parts that were read (a known `FAIL` in another part still wins). A part that does not apply to the bot (no slot in the enchant dead zone, say) is skipped and is not counted as missing. The check is `FAIL` when any judged part fails, `PASS` when every judged part passes and no decisive part is missing, `INCONCLUSIVE` otherwise.

With `--baseline`, the older snapshot is compared with the newer **per spec and level band**, on shares of each side's bots (the two bot sets may differ), never bot by bot: a `botN` id is not an identity. Only specs and bands in both readings are compared; a spec or band in one reading only is listed as `only before` / `only after` and never compared against an empty side. Fewer failing bots (as a share) is `better`; a verdict that turned `INCONCLUSIVE` while nothing started passing is **`no longer judged`**, never `better` — losing a read-out is not an improvement.

Exit codes: `0` no `FAIL` in the snapshot given, `1` at least one `FAIL`, `3` when no check could be judged at all (every check `INCONCLUSIVE`, so the snapshot carried too little data — a report that assessed nothing is not a pass), `2` a usage error, a malformed snapshot, or a snapshot or baseline that does not load. The report's header prints its coverage (`coverage: N check(s) judged, M INCONCLUSIVE`).

# The checks

Each check is made of parts; a part is judged only from data the snapshot carries. The per-check text below names each threshold's origin and repeats that it is a starting value to tune.

## Talents by level group (`REALISM-TALENTS`)

Per bot, against the common picks of its own level group (`talent-picks.csv`, the same 50% the player-data talent rule reads):

- **point_share** (decisive) — the share of the points the common picks account for that the bot spends in them. The common picks are the talents at least **50%** of the level group's players of the spec took (a starting line: it is the player-data talent rule's own 50%). The denominator is **capped per tree at what the common picks can hold there**: the lesser of the bot's points in a tree and the summed maximum ranks of that tree's common picks, added over the trees the build uses. A level group's common picks often cannot fill the character's whole point budget — at level 19 a single talent may be the only common pick — so measuring against the bot's full points would fail a build that took every common pick. The file's own figures: with a build made from the data, 16 of 27 specs could not reach an **80%** floor against full points at level 19, 15 at 29 and 5 at 39; the capped denominator turns those into 100%. `PASS` at **80%** or more of the capped denominator. Both 50% and 80% are starting values to tune, not facts about players.
- **split** (level 60 only, secondary) — the bot's points per tree against the nearest of the spec's 20 most common splits in `talent-splits-60.csv`, counted as the points moved between trees (half the sum of the tree differences). `PASS` at **5 points** or fewer from the nearest common split (a starting value). A point in a talent the tables do not name leaves this part `INCONCLUSIVE` (the split would be a guess).

The build must be the whole build: a list with fewer points than the character's level has (one point per level from 10, 51 at 60) is `INCONCLUSIVE` whatever `talents_complete` says, because its missing points would read as off-picks. A list with more points than the level allows, or a rank above the talent's own maximum, is refused by the loader.

INCONCLUSIVE: no talents in the snapshot, a partial list (`talents_complete` false or too few points for the level), a spec the tables do not name, a level with no talent points, no common picks in the file, a talent id that is not a rank-1 id the tables know for the spec, a level group whose common picks account for fewer than **2 points** of the build in its trees (too few to judge), and — below 60, where there is no split part to judge the tree distribution — points in a tree the level group's common picks do not cover (the share would be a guess).

## Gear realism (`REALISM-GEAR`)

The main check, for every bot whose items were read. It reports the average item level, the quality mix and the spread of the item levels, and judges three decisive parts: no `PASS` over a partial reading.

- **required_level** — any item whose required level is above the bot's level is a `FAIL`: no player of that level could wear it. This is a game fact from the item record, not a player choice, and it is the clearest "could not have it" signal. A bot with any worn item whose required level was not read is `INCONCLUSIVE` (the impossible item could be the unread one).
- **item_level** — the share of slots whose item level is above the bot's level plus its band's allowance. `PASS` at **10%** or less (one slot in ten, a starting value to tune). One worn item without an item level leaves the part `INCONCLUSIVE` and says how many slots it could not read (the unread one could be far above the allowance); none read is `INCONCLUSIVE` too.

  | Bot level | Allowance |
  |---|---|
  | 1-19 | 5 |
  | 20-29 | 5 |
  | 30-39 | 5 |
  | 40-49 | 5 |
  | 50-59 | 5 |
  | 60 | 30 |

  These are starting values, derived as the **95th percentile of (item level − required level)** over the game's own item table, counted at each required level: through level 59 an item sits about five item levels above its required level, while at 60 the raid tier reaches item levels 88-92 on required-level-60 items, so the high percentile is 30. A real batch may move them. Raid gear on a pool bot is the `source` part's business, not this one's.
- **source** — any worn item whose `source` is `raid`, `pvp` or `reputation` fails, on a snapshot of pool bots. Every worn item must carry a source for the part to pass: one permitted item beside an unread one is `INCONCLUSIVE`, because the unread one could be the raid piece.

INCONCLUSIVE: no items read (or a bot that wears nothing), any worn item with no required level, any worn item with no item level, and any worn item with no source. A snapshot taken today has no item source, so GEAR is `INCONCLUSIVE` even for the one character whose outfit it reads.

## Stat priority (`REALISM-STATS`)

Level 60 only (`stat-priority-60.csv`). **One judgement**, not two that can disagree:

- **fits** (decisive) — the player-data gear rule itself (`AOTC-GEAR-FITS-SPEC`, see [trials](trials.md)): every worn item must fit the spec, read through the items its own players wear in that slot, and through the item's own stats when the snapshot carries them. This is the same judgement the bench's other rows use.

The spec's stat priority is reported **without a verdict**: the report's evidence says how many stat-reading items carry one of the spec's priority families (a healer's strength piece is named there). It never turns into a second verdict, because a second, stricter rule could fail an outfit the gear rule passes.

INCONCLUSIVE: a level other than 60, no items read (or a bot that wears nothing), no spec, any worn item with no stat reading (`stats_known` false: the stat priority cannot be judged), and an item the tables cannot place.

## Enchants (`REALISM-ENCHANTS`)

Level 60 only (`enchants-60.csv`, the **all-level-60 population**, not the best-geared one: a raider's habit is no rule for the whole pool), per slot the tables name (Head, Neck, Wrist, Hands, Feet, Chest, Legs, Back, Main Hand, Off Hand, Ranged, Rings). The population splits into the share that leaves a slot unenchanted (the `No enchant` row) and each enchant's share. Two named lines leave a **dead zone** in between:

- **present** (decisive) — a slot where at least **70%** of the spec's all-60 characters carry an enchant must carry one. (The enchanted share, not the bare one.)
- **empty** (decisive) — a slot where at most **20%** carry an enchant is one players leave empty, and must carry none.
- Between 20% and 70% enchanted the population is split, so the slot is **not judged**: a best-geared minority's habit is no rule. For example, fury warrior hands are 65.9% bare (34.1% enchanted) in the all-60 population, so an enchanted glove is no fault.

Both lines are starting values to tune. With today's file no slot reaches the 70% line, so `present` is skipped and says so; the `empty` part carries the check. Both parts judge only slots whose enchant was read, and one unread slot in the same group leaves the part `INCONCLUSIVE`: a bare slot it could not read would otherwise pass.
- **common** — report-only: where one enchant holds at least **50%** of the slot (a starting value), the part names it. The tables name the enchant's **item** (its scroll), not the enchant the server stores on a worn item, so the part never compares the two ids and is `INCONCLUSIVE` with that reason until a mapping read-out exists.

INCONCLUSIVE: a level other than 60, no items read (or a bot that wears nothing), no spec, no `enchant_id` read on an applicable slot, and a slot with no table row (shoulders, the waist, the trinkets).

## Spells before/after

```sh
forever-agent forever spell-diff [--json] [--min-active 30] [--fail-unknown] [--oracle DIR] [--spells DIR] <before> <after>
```

The spell check is `forever spell-score`'s: each party member's casts judged against the oracle of its class (see [forever spell-score](spell-score.md)). `spell-diff` runs that same judge on two runs — two watch recordings, or two saved `forever spell-score --json` answers — and prints what changed per class: the class's cast total, every spell whose count moved (or that was added or removed), and every member whose verdict moved. `--json` prints the comparison as one object (`format`, `before`, `after`, `classes`). It exits `0` when both sides read, `2` on a usage or load error: a before/after reports, it does not judge.

# Known limits

- The taker reads what the serve can already read: a bot's spec, talents, stats, enchants and its items' sources are not read today, so most checks report `INCONCLUSIVE` on a taken snapshot until the bot module provides them. Only `bot1`, the serve's own login character, has a worn-item read, and it is not a module bot. See "What the taker fills today" above for the exact fields the module read-out must provide.
- The player data is a snapshot of player choices from 2026-11-24, not game facts; the server's own data wins, and the tables carry no item stats or armour types.
- The gear allowance is derived from the game's item table as a 95th percentile, not from a measured player population; it is a starting value to tune.
- The checks read one file at a time: a before/after is two snapshots taken at two moments, not two runs of one bot pool. Bots are compared per spec and level band, never by id.

# Further reading

- [Trials](trials.md): the behaviour bench, its player-data rules and the class rules.
- [forever spell-score](spell-score.md): the spell check the before/after compares.
- [Report cards](report-cards.md): how a run's card is built.
