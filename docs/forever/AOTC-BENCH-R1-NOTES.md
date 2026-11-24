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
| `EnchantsFor(class, spec, slot)` | the most used enchants per slot at 60 |
| `Item(id)` | an item's name, slots and the specs that wear it |
| `SpecList()`, `SpecKnown()` | the 27 class/spec pairs the population file names |

`aotc.Credit` is the attribution line every output that quotes the tables carries.

## Rule "gear fits the spec" (`AOTC-GEAR-FITS-SPEC`)

Input: the bot's class, spec, level and equipped items from the run's own record (the setup's proven
`bot_items` read-back, which is the final equipment state, or — when no read-back proved one — the
ledger's `equip` rows replayed in order, each piece replacing the one before it in its slot; the
serve has no equipment read and this repository carries no item stats). Per item:

- **FINE** when the item is among the items the spec's level 60 players wear in that slot.
- **FINE** when the item's own primary stats (when the record carries them) include one of the spec's
  top `PriorityStats` stats.
- **MISMATCH** (the rule FAILs) when its stats match none of the priority, or when the item is
  commonly worn only by specs of one contrary stat family (`GearFamilyShare`, `GearMismatchSpecs`) —
  the stat-family reading that catches a caster in strength plate without item stats.
- **UNPROVEN** otherwise: the rule is INCONCLUSIVE and names the item, so a missing item table is
  never a pass.

Item level is never a reason. A record with no item data, a bot below level 60 (the gear data is
level 60 only) or an unknown spec is INCONCLUSIVE with the reason.

## Rule "talents imply spells" (`AOTC-TALENTS-IMPLY-SPELLS`)

For the bot's level group, the talents at or above `MinTalentShare` (50%) whose talent spell is an
active spell in the Forever spell table (`internal/foreverspells`, not passive) extend the spell
judge's should-have-cast list (`kbspells.BotCasts.Expected`, judged like the spec's core list). The
rule:

- judges only spells whose cooldown fits a combat window (`TalentCooldownFactor`; a 3 minute spell is
  not expected in a 60 s fight) and only a window of at least `TalentMinActiveS` (30 s),
- reads combat time from the bot's own `in_combat` samples (`CombatGapS` joins consecutive
  in-combat samples into one window), never from the repeat's scored time: setup, waiting and travel
  are no fight, so a long repeat with a short fight is INCONCLUSIVE, never a FAIL,
- expects only a talent the record proves learned: a trained talent's spell that is never cast
  FAILs, an absent one is treated as untrained only when the read-back declares the full build
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

- No item stats in this repository: the stat-priority reading runs only when a record carries an
  item's stats; otherwise the family-of-wearers reading or INCONCLUSIVE. A live bot's kit pieces are
  often outside the top 20 of their spec's slot, so expect UNPROVEN (never a pass) until the
  resistance-gear item read-out (#20) carries item stats into the record.
- The gear data is level 60 only; the talent data has a level group per decade.
- An item outside the top 20 of a spec's slot list is not evidence against it (the table is top 20),
  so such an item is UNPROVEN, never a mismatch on its absence alone.
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
