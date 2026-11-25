# AOTC bench rules (round 1): player data, gear and talents

Round 1 of #115 scores bots against what real WoW Forever players of their class and spec do. The
reference is `data/aotc`, the aggregate player statistics added in #215 (aotc.gg, read 2026-11-24).
They are player choices, not game facts: for a spell, talent or item fact the server's own data wins.

## The data layer (`data/aotc`, package `aotc`)

`aotc.go` embeds the 15 CSV files, so a build needs no path. `aotc.Load(fs.FS)` parses every file with
its exact header, refuses a malformed row as `file.csv: line N: what` (a bad number, a wrong field
count, a percentage outside 0..100, an unknown level group or population), and cross-checks the files:
every class/spec any table names has a `spec-population.csv` row, a stat priority and level 60 gear
rows. `aotc.Default()` is the embedded copy (a malformed embedded file panics at first use; the
package test parses the shipped set).

Typed accessors:

| Accessor | What |
|---|---|
| `CommonTalents(class, spec, levels, minShare)` | the talent picks at a level group with at least `minShare` percent, highest share first |
| `StatPriorityFor(class, spec)` | the stat priority at 60, highest first |
| `GearFor(class, spec, slot)` | the 20 most worn items per slot at 60, `best_geared` population only (item-set rows excluded) |
| `GearRows(class, spec)` | every level 60 gear row the spec has, in every slot and population (the gear rule's own-data reading) |
| `EnchantsFor(class, spec, slot)` | the most used enchants per slot at 60 |
| `Item(id)` | an item's name, slots and the specs that wear it |
| `SpecList()`, `SpecKnown()` | the 27 class/spec pairs the population file names |

`aotc.Credit` is the attribution line every output that quotes the tables carries.

## Rule "gear fits the spec" (`AOTC-GEAR-FITS-SPEC`)

Input: the bot's class, spec, level and equipped items from the run's own record (the setup's proven
`bot_items` read-back, which is the final equipment state, or — when no read-back proved one — the
ledger's `equip` rows replayed in order, each piece replacing the one before it in its slot; the
serve has no equipment read and this repository carries no item stats). Per item:

- **FINE** when any row of the spec's own level 60 gear tables names the item: any population
  (`best_geared` or `all_60`), any share and any slot. The spec's own players' data is the rule's
  strongest evidence, so a hybrid's shared pieces (enhancement mail, protection plate) are fine
  without reading a stat family.
- **FINE** when the item is outside that data and one of its families is among the families the
  spec's own players wear in its slot. That acceptable set is the data's, summed by wear share (not
  a union): every row of the spec's tables in the slot adds its share to each family the item's
  wearers name, and a family is acceptable when its summed share is at least `familyShareFloor`
  (0.20) of the slot's summed family share, or when it is the single largest family. All
  populations and shares count, cached per repeat. An item's families are its own primary stats
  when the record carries them; otherwise they are the share-weighted families of the specs that
  commonly wear it (`GearMismatchSpecs` specs at or above `GearFamilyShare`, each spec's set from
  its top `PriorityStats` stats, weighted by its wear share). The item reading is weighted too,
  because a hybrid wearer else adds its caster family to every plate piece it shares.
- **MISMATCH** (the rule FAILs) when the item's family is known and none of its families is one the
  spec's own players wear in that slot — the reading that catches a caster in strength plate, a
  warrior in a caster's shoulders, a hunter in strength plate and a rogue in cloth.
- **UNPROVEN** otherwise: an item whose family is unknown (no item stats and no common wearers, or
  no family above the share floor), a slot whose own items name no family, or an item with no table
  entry. The rule is INCONCLUSIVE and names the item, so a missing item table or item stats is
  never a pass.

Stamina, crit and hit have no stat family: every priority lists stamina for survivability, so it
cannot make an item contrary (a feral's Agility is second, an enhancement shaman's Intellect
second). A spec's family set is every family among its top `PriorityStats` stats, not only the
first one, so a mixed spec is never reduced to a pure caster or a pure physical spec. The stat
priority remains in the evidence line as the tie-break; the verdict's families come from the data.

Known limits (the tables carry no item stats or armour type, only wearer choices):

- a feral druid in plate is FINE: the feral's own data names agility and strength, so a strength
  plate piece shares a family with the feral's own leather. An outfit in the wrong armour class but
  the right stat family passes.
- an enhancement shaman in hunter-only agility mail FAILs in Wrist, Hands, Waist and Feet, where
  the shaman's own slot data names a caster family above the floor; the same mail is FINE in the
  other four armour slots (Shoulders, Chest, Legs and the ones without such an item; measured on
  the shipped tables, 2026-11-25), and the agility mail its own players wear is FINE by the
  own-data rule. The largest-family clause adds no family on the shipped data at all: every
  acceptable set and every item reading is the 20% floor's alone (the outfit and data-sweep tests
  say so).

Both need the item read-out of #20 (item stats), not a threshold change.

Item level is never a reason. A record with no item data, a bot below level 60 (the gear data is
level 60 only) or an unknown spec is INCONCLUSIVE with the reason.

## Rule "talents imply spells" (`AOTC-TALENTS-IMPLY-SPELLS`)

For the bot's level group, the talents at or above `MinTalentShare` (50%) whose talent spell is an
active spell in the Forever spell table (`internal/foreverspells`, not passive) extend the spell
judge's should-have-cast list (`kbspells.BotCasts.Expected`, judged like the spec's core list). The
rule:

- judges only spells whose cooldown fits a combat window divided by `TalentCooldownFactor` (a 180 s
  spell needs a 360 s window, so it is not expected in a 90 s fight) and only a window of at least
  `TalentMinActiveS` (30 s),
- reads combat time from the bot's own `in_combat` samples (`CombatGapS` joins consecutive
  in-combat samples into one window), never from the repeat's scored time: setup, waiting and travel
  are no fight, so a long repeat with a short fight is INCONCLUSIVE, never a FAIL,
- expects only a talent the record proves learned: a trained talent's spell that is never cast
  FAILs, unless the spec's spell lists call that spell situational — then the rule reports it as
  held and never FAILs it (the bot may rightly hold Inner Focus, Nature's Swiftness or Cold Blood).
  An absent pick is treated as untrained only when the read-back declares the full build
  (`complete`); with the runner's partial marker-kit read-back an absent pick is unproven, so it can
  turn a PASS into INCONCLUSIVE but never into a FAIL,
- reports the expectations and judges nothing (INCONCLUSIVE) when the record carries no talent data
  or no proven combat participation.

## Wiring

Both rules are off by default: `forever-agent trials score <run> --player-data` adds them under
`trial.json`'s `player_data` and the report card prints them under `## Player data (aotc.gg)` with
the credit line; `card.json` carries `player_data` and `player_data_credit`. A default score is
byte-for-byte the bench's. `trials report` re-renders the section from the stored trial.json.

Thresholds are named constants in `internal/trials/playdata/playdata.go`.

## Limits

- No item stats in this repository: an item's families come from its own stats when a record
  carries them, otherwise from the specs that commonly wear it; with neither the item is UNPROVEN,
  never a pass. A live bot's kit pieces are often outside its spec's tables, so expect UNPROVEN
  until the resistance-gear item read-out (#20) carries item stats into the record.
- The gear data is level 60 only; the talent data has a level group per decade.
- An item absent from all of a spec's gear tables (both populations) is judged only by its family:
  with no item stats and no common wearers it is UNPROVEN, never a mismatch on its absence alone.
- The rules read the trials record (setup.json's `bot_items` read-back, the ledger's `equip` and
  `bot_talents` rows, the samples' bot combat flags). A watch record carries neither items nor
  talents, so a watch is not judged by them today; the resistance-gear read-out (#20) is the piece
  that would feed a watch.
- The `bot_talents` read-back lists the spec's marker kit (`specs.json`), not a full talent build: a
  talent outside that kit is unproven, so the rule reports it and returns INCONCLUSIVE instead of a
  PASS on absent picks. A read-back that declares `complete` holds the full learned set, and absence
  then means untrained. No runner writes such a row yet.
- Active time is the bot's own `in_combat` samples. A record without bot samples, or whose bot was
  never in combat, is INCONCLUSIVE: the rule does not substitute the scored time or the configured
  duration.
- The site reads the live server: a private build can differ. Player choices are the reference; the
  server's own data wins.
