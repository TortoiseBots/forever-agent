---
type: Method Reference
title: Spells and combat
description: Read the spellbook and cooldowns, cast and cancel spells, and fight a unit with melee or spells.
tags: [player-actions, spells, combat, cooldowns]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-25T04:40:00Z }
sources:
  - id: session-methods
    resource: internal/forever/world/serve_session_methods.go
  - id: session-spells
    resource: internal/forever/world/serve_session.go
  - id: spellbook
    resource: internal/forever/world/spellbook.go
  - id: fight
    resource: internal/forever/world/fight.go
  - id: spellstate
    resource: internal/spellstate/spellstate.go
  - id: trade-cast
    resource: internal/forever/world/trade_cast.go
  - id: combat-reasons
    resource: internal/forever/combat/reasons.go
---

This page covers what a `forever serve` session (or a script run with `forever act`) can do with spells and
fighting: list the spellbook with its cooldowns, cast and cancel spells, attack a unit, and read the cast state.
The calls below are daemon methods; see [rpc basics](../daemon/rpc-basics.md) for how to send them and
[status and world reads](../daemon/status-and-world-reads.md) for finding units to cast at.

Spell data comes from a generated table shipped inside the agent, so a spell the table does not hold still appears
in the book but with no name or numbers (`meta: false`). The spellbook itself is what the server said the character
knows, so it is always current.

# The methods

| Method | Parameters | Returns |
|---|---|---|
| `spells` | `spell_id`, `name`, `highest`, `passive` | array of spell rows |
| `cast` | `spell_id` (required), `target`, `trade_slot` | empty object on success, or the fields below for a stored trade-slot cast |
| `cancel_cast` | `spell_id` (0 or omitted: whatever is being cast) | empty object |
| `cast_state` | none | the cast/gcd/cooldown state |
| `attack` | `guid`, `name`, `for_ms` | target, duration, reason |
| `stop_attack` | none | empty object |

# spells

`spells` lists the character's known spells, sorted by name and rank. Filters:

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `spell_id` | number | 0 | Only this spell id is returned (and passive spells are included when it names one). |
| `name` | string | empty | Only names containing this text, case-insensitive. |
| `highest` | bool | false | Only the highest known rank of each name. |
| `passive` | bool | false | Include passive spells. |

A row:

| Field | Meaning |
|---|---|
| `spell_id` | The spell id. |
| `name` | The name from the spell table (empty when the table does not hold it). |
| `rank` | The table's rank number (0 for a spell with one rank). |
| `highest` | True for the highest rank of this name among the known spells. |
| `meta` | False when the generated spell table does not hold this id. |
| `level` | The level the table gives the spell. |
| `passive` | The table's passive flag. |
| `cooldown_ms` | The spell's own cooldown. |
| `category` / `category_cd_ms` | Its cooldown category and that category's cooldown. |
| `gcd_ms` | The global cooldown it starts. |
| `mechanic` | The mechanic id from the table: stun, root, fear and the rest use it (see below). |
| `dispel` | The dispel type id (magic, curse, disease, poison, ...). |
| `class` | The class the table assigns it to, when it assigns one. |

`mechanic` and `cooldown_ms` are how crowd control shows up here: there is no separate "crowd control" call. The
agent does not schedule crowd control itself (planned runs refuse crowd-control settings), so a script that wants a
stun, root or polymorph casts it like any other spell and reads the result.

# cast

`cast` sends one cast and waits for the server's answer: a spell-go is success, a cast failure is an error.

| Parameter | Type | Required | Meaning |
|---|---|---|---|
| `spell_id` | number | yes | The spell to cast. |
| `target` | GUID | no | Who to cast at. Omitted: the currently selected target when there is one, otherwise the character itself. |
| `trade_slot` | number 0-6 | no | Cast at the other player's item in this trade-window slot instead of at a unit. Only slot 6 is castable (see below). |

Notes that matter in practice:

- A target that is not in the world model, or that is dead, is refused before anything is sent. Range and line of
  sight are the server's to check: the call does not walk and does not pre-check distance.
- A refused cast is an error whose message names the server's reason, for example
  `cast: the server refused the spell: not ready` or `cast: the server refused the spell: out of range`. A refusal
  about what the cast lacks carries the missing thing, from the server's own failed-cast argument: the reagent or
  tool item, the spell focus or the totem category, as in
  `totems (needs tool item 5060 in the bags)` for a Pick Lock without Thieves' Tools, instead of a bare
  `spell failure 153`. `spell_fail` combat rows and `last_failure.reason_name` carry the same words.
- A server that never answers fails with `world: no answer to CMSG_CAST_SPELL for spell N`. The spell is then held
  back briefly: the next cast of the same spell waits out a short quarantine so a late answer cannot be mistaken for
  its own.
- `trade_slot` is for enchanting another player's item: a trade window with them must already be open (see
  [trade](trade.md)), and only slot 6 (the non-traded slot) is accepted, because that is the only slot the server
  addresses. `trade_slot` and `target` cannot be combined.
- A successful cast still answers `{}`.
- The server's silent result — `SPELL_FAILED_DONT_REPORT`, whose name is `do not report` — has two meanings, and the
  call tells them apart by the trade data. A trade-slot cast gets it because the core stored the spell instead of
  casting it (`Spell.cpp:3826-3841`): it goes off when both players accept, and the answer is a success with

  | Field | Meaning |
  |---|---|
  | `spell_id` | The spell that was cast. |
  | `trade_slot` | The slot the call named. |
  | `silent` | `true`: the server answered its "no message" result. |
  | `completes` | `trade_accept`: the spell is cast when the trade is accepted. |

  The stored spell is the trade's own record of it: `trade_status` (and `trade`) names it as `my_spell`
  (`TradeData::SetSpell`, `TradeData.cpp:84-101`, sends that update at the moment the spell is stored), and the call
  waits up to a second for it. The update must be newer than the cast: a spell the window still names from an earlier
  cast does not answer for this one, so a second cast that got only the cooldown answer fails as below.
- The same result with nothing stored is no cast at all: the core answers it for a spell on its event cooldown as
  well (`Spell.cpp:6090`). The call then fails with `cast: the server answered its no-message result for spell N and
  <why>: nothing was cast`, where `<why>` is `no spell-go came` for a plain cast and `the trade names no such spell`
  for a trade-slot cast. That answer is not a refusal (`the server refused the spell: ...` stays the wording for a
  real refusal); the fight log's own line for it says `spell N answered the silent result (do not report)`.

# cancel_cast

Sends the cancel for the cast in progress. With no cast in progress the call is refused with
`no cast in progress`. With `spell_id` 0 or omitted the current cast's spell is cancelled; a named spell cancels
that spell's cast, channel or auto-repeat.

# cast_state

`cast_state` answers one object without sending anything:

| Field | Meaning |
|---|---|
| `casting` | A cast is in progress. |
| `spell_id`, `cast_id`, `target` | The current cast. |
| `remaining_ms` | Time left in the cast (0 for an instant or unknown). |
| `in_combat`, `dead`, `ghost` | The character's own flags. |
| `health`, `max_health`, `power`, `max_power`, `power_type` | The character's own values. |
| `gcd` | `{active, remaining_ms, total_ms}`: the global cooldown. |
| `gcd_known` | False when the GCD is not known yet. |
| `last_cast` | `{spell_id, name, ago_ms}` of the last cast that completed. |
| `last_failure` | `{spell_id, name, reason_name, ago_ms}` of the last refused cast. |
| `failures` | How many casts failed since login. |
| `combo_points`, `combo_target` | Rogue/druid combo points and the unit they are on. |
| `cooldowns` | Active cooldowns: `[{spell_id, name, remaining_ms, category}]`. |

Cooldowns are worked out locally from the spell table and then corrected by what the server sends. Four limits are
worth knowing before a script relies on them:

- Talents and spell modifiers that shorten a cooldown are not known, so a shortened cooldown still reads running
  until its nominal end.
- The server rounds cooldowns down to whole seconds, so a spell can be ready up to a second early here.
- Haste is not applied to the global cooldown.
- Cooldowns an item sets (a potion) are not tracked, and Shoot/the wand read ready because their cooldown is the
  ranged attack time, which the agent does not know.

A cast the server refuses as not ready is held back for one second whatever the numbers say, so a script that
retries immediately does not spray refusals.

# attack and stop_attack

`attack` selects the unit and fights it: the agent selects it, walks into melee range when it is too far, and keeps
swinging until the unit dies, the fight is stopped, a map change interrupts it, or the time runs out.

| Parameter | Type | Default | Meaning |
|---|---|---|---|
| `guid` | GUID | 0 | The unit to attack. |
| `name` | string | empty | Or the unit's name (nearest living match). |
| `for_ms` | number | 25000 | How long to keep fighting; 0 means the default, the maximum is 600000 (10 minutes). |

The result is `{target, duration_ms, reason}`. `reason` is `dead` (the unit died), `stopped` (stop_attack or
another call ended the fight) or `map change`. A target that is already dead, not a unit, or one the server marks
unattackable is refused before the first swing.

`stop_attack` stops the current fight immediately and answers without a result object.

# Examples

List the highest rank of every known spell whose name contains "Heal":

```
forever-agent forever rpc ./slots/ACCOUNT spells '{"name": "heal", "highest": true}'
```

Cast a spell at the selected target:

```
forever-agent forever rpc ./slots/ACCOUNT cast '{"spell_id": 133}'
```

Is the fireball cast done, and what is left on cooldown?

```
forever-agent forever rpc ./slots/ACCOUNT cast_state '{}'
```

Kill the nearest wolf, giving up after 40 seconds:

```
forever-agent forever rpc ./slots/ACCOUNT attack '{"name": "Wolf", "for_ms": 40000}'
```

Stop fighting:

```
forever-agent forever rpc ./slots/ACCOUNT stop_attack '{}'
```

# A live fight: forever fight-check

One-shot live check of the combat path, no daemon: it logs in, settles, picks one living creature, fights it for
`--fight-for` and prints what the client understood of the fight.

```bash
forever-agent forever fight-check [--rest host:port] [--rpc host:port] [--ca FILE] --account EMAIL \
    --character NAME [--stay 8s] [--entry N] [--level-min N] [--level-max N] [--fight-for 60s] \
    [--capture-dir DIR] [--provoke]
```

| Flag | Default | Meaning |
|---|---|---|
| `--stay` | `8s` | How long to settle in the world before choosing the target, 0-10m. |
| `--entry` | `0` | Creature template entry to fight; `0` takes the nearest living creature in the level band within 40 yd. |
| `--level-min` | `0` | Lowest target level; `0` means three below the character's own level, 0-60. |
| `--level-max` | `0` | Highest target level; `0` means two above the character's own level, 0-60. |
| `--fight-for` | `60s` | How long to fight before giving up, more than 0 and at most 5m. |
| `--capture-dir DIR` | none | Write every packet the combat, chat and party handlers see into a new run directory. Keep it outside the repository. |
| `--provoke` | off | Debug: provoke refusals a fight log otherwise has no packet for (an out-of-range swing, a cast with no rage). |

The summary is identifier-free: whether the target died, got away or survived, its entry and level and health,
swings, misses, casts and aura events, damage dealt and taken, the character's own health and power, experience
gained, and one row per cast result. Exit `0` when the target died; `2` a usage error; `5` the world server refused
the session; `6` the link was lost; `11` the character died.

# What you see

A successful `cast`, `cancel_cast` and `stop_attack` answer `{}`. `spells` answers an array (possibly empty),
`cast_state` the object above, and `attack` the `{target, duration_ms, reason}` object. Failures are JSON-RPC errors
with the message text quoted above; the error code is the game error for a refused cast and the timeout code when a
wait runs out.

# Common problems

- `cast needs a spell id` — `spell_id` was 0 or missing.
- `cast: the server refused the spell: <reason>` — the server said no. The reason name is the server's own
  (`not ready`, `out of range`, `line of sight`, ...).
- `no target selected; call target first` — `attack` was called with no unit and nothing selected.
- `world: the target is gone` / `world: the server will not let this character attack that unit` — the target died
  or is not attackable.
- `world: no mover (the character's create block never arrived)` — the session has not finished entering the world
  yet; wait for `status` to show the character in the world.
