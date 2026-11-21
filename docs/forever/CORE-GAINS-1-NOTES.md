# Core gains 1: pushed threat, encounter packets, the advanced log and six log kinds

ForeverCore rev `d02645dd21`; agent branch `forever-core-gains` from `origin/main`. The round covers items 1-4 of
`FOREVER-AGENT-CORE-GAINS.md`: decision A (advanced combat log), B (pushed threat), C (encounters) and D (six
combat-log kinds). No private capture bytes, names or values are in the tree; every fixture was built by hand from
the core's writer, cited per parser. The client still sends only packets a real 1.60 client sends; the only new send
this round is the advanced-log flag, beside the login burst, the attack/cast traffic and the chat channel.

## A. The advanced combat log

`CMSG_SET_ADVANCED_COMBAT_LOGGING` (core `0x3D0193`; `MiscHandler.cpp:1237-1240`) is one bit, most significant first
(`ClientConfigPackets.cpp:77-80`), so the packet is `80` for enable. The session sends it once, right after it answers
`SMSG_MOVE_SET_ACTIVE_MOVER` with `CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE` (the moment `Report.InWorld` turns true). A
reconnect builds a fresh session, so the flag goes again exactly once per world entry. The session records the send in
`Report.AdvancedCombatLog`; the fake world server validates the byte and counts it, and `TestEnterAgainstFakeServer`
asserts exactly one.

With the flag set the server sends such a player the full log packet (`Object.cpp:1126-1148`), whose
`SpellCastLogData` (`CombatLogPacketsCommon.cpp:150-172`) carries health, attack power, spell power, armor and the
current power bar. The parsers already read that block; this round **exposes** it instead of adding polling:

- `world.CombatRow` gains `sample: {health, power, power_type}` (the first `SpellLogPowerData` entry is the unit's own
  bar, `CombatLogPacketsCommon.cpp:35-45`), written only when the packet carried a log block.
- The rows that now pass the block on: `melee` (the attacker-state update's full form; the landed-log copy is
  parsed for its layout but the server sends it only to the swing's attacker and victim, so it is not this
  observer's row), `spell_damage`, `heal`, `periodic`, `absorb` and `environmental` (the last two describe the
  victim, per their send sites). Nothing polls `.tb state` for this.

A handful of health/mana points therefore sit on every combat event, which is what the bug hunter's healer-mana and
overkill evidence, and a trials repeat's 1 Hz `samples.jsonl`, were missing between ticks.

## B. Pushed threat

`internal/forever/combat/threat.go` parses the four packets (`Combat/ThreatManager.cpp:859-893`,
`CombatPackets.cpp:46-79,138-143`):

| opcode | core | layout (writer) |
|---|---|---|
| `SMSG_THREAT_UPDATE` | `0x450186` | mob GUID, uint32 count, count × (victim GUID, int64 threat) |
| `SMSG_HIGHEST_THREAT_UPDATE` | `0x450185` | mob GUID, highest GUID, uint32 count, the list |
| `SMSG_THREAT_REMOVE` | `0x450187` | mob GUID, victim GUID |
| `SMSG_THREAT_CLEAR` | `0x450188` | mob GUID |

Threat values are the server's own `threat * 100` (`ThreatManager.cpp:874-880`); the world adapter divides by 100
when it shapes a `.tb threat` report, so every existing caller compares the same units as before. The counts are
bounded twice: at 512 entries and by the bytes left (an entry needs at least the packed GUID's two mask bytes plus
eight threat bytes). A short packet, a forged count or an empty-GUID entry is an error; the parser never allocates
from the count before both checks pass. `SMSG_THREAT_REMOVE`'s writer names its fields the reverse of their meaning
(`UnitGUID` is the mob, `AboutGUID` the victim), so the parser names them by role.

`internal/forever/objects/threat.go` keeps one list per mob: replace on update/highest, drop one victim on remove
(an emptied list leaves the table), drop the mob on clear. `ThreatSnapshot(now)` serves only lists whose last packet
arrived within `objects.ThreatFreshFor` (5 s) and copies them (a caller may hold a snapshot while the next packet
replaces the list); `ThreatSnapshotAll` is the "did any packet flow" view. The table is bounded at 1024 mobs, evicting
the oldest list when a hostile server names more; `ResetForMap` clears it with the rest of the instance state.

The driver and the watch read it first:

- `driver.World` gains `ThreatTable(now)` (the pushed lists shaped like the module's report, with the mob's entry,
  name, health percent and position from the world model) and `Encounters`.
- `Run.threat` returns the pushed report whenever it is fresh. `.tb threat` becomes the fallback: called when no list
  is fresh (a mob outside the client's visibility pushes nothing to this client), and otherwise at most once per 10 s
  (`threatFallbackEvery`) instead of once per 2 s tick. The 10 s read also picks up a mob that pushed nothing. The
  watch's frame takes the same path, so its threat half stops issuing a command on the ticks where pushes answered.
- `.tb state` keeps its 2 s cadence: it carries the party's own state (health, position, combat flag, order status),
  which no threat packet has, and `stateFreshFor` (6 s) gates pulls on it. The item's poll that is replaced is the
  threat read; the notes below give its measured count.

**Polls saved, unit-test number.** `TestThreatTableSavesPolls` plays the same scripted five-minute fight twice (one
group, the pack dying at 300 s), once with the pushed table answering and once without. The fight loop ticks every
2 s, so the old path makes 150 `.tb threat` reads; with pushes it makes 30 (the 10 s fallback), saving **120
reads**. The test logs `150 ticks at 2 s; .tb threat reads 150 without the pushed table, 30 with it (saved 120)`.
The pushed ticks still answered every threat question: the scenario ends `clear` in both runs.

## C. Encounters

`internal/forever/combat/encounter.go` parses `SMSG_ENCOUNTER_START` (`0x450228`: int32 encounter, difficulty,
group size, uint16 zero), `SMSG_ENCOUNTER_END` (`0x450229`: the three, int32 duration ms, one success bit) and
`SMSG_BOSS_KILL` (`0x4B002B`: uint32 encounter id), all from `InstancePackets.cpp:198-233`; the send sites are
`InstanceScript.cpp:413-465,924-962`, and the classic start/end pair is only sent while a boss state moves
IN_PROGRESS → FAIL/DONE/NOT_STARTED.

`objects.World` keeps a bounded ring (64) of `EncounterEvent` — kind, encounter id, difficulty, group size, duration,
success, arrival time — drained by the driver. `SMSG_BOSS_KILL` right after an end for the same encounter (within
10 s, the pair `InstanceScript.cpp:463-466` sends) is not a second event.

- Run: `refresh` drains the ring every state poll (the fight loop's 2 s tick, the pull wait, the entry checks) and
  writes trace events `boss_pull`, `boss_kill` and `boss_wipe` with `encounter`, `difficulty`, `group_size`,
  `duration_ms` and `success`. The trace event list in `internal/trace/trace.go` names them.
- Watch: `watchLoop` drains them into `RunWatch.Encounters` (`EncounterRecord`: `at_s` on the module run's clock,
  kind, encounter, difficulty, group size, duration, success) and logs one line per event. The trace gets the same
  `boss_pull`/`boss_kill`/`boss_wipe` events.
- Fallback: `watchBosses` skips `.tb instance` while encounter packets flowed within the last 5 minutes, and the
  forced read at the end of the watch still runs. An instance whose script has no `DungeonEncounter` rows (the
  classic Ragefire Chasm bosses today) never sends one, so it keeps the old once-a-minute read;
  `TestWatchBossReadWithoutEncounters` pins that.

## D. The six combat-log kinds

`internal/forever/combat/logkinds.go` parses, and the session writes into the same ring the fight already fills:

| row kind | opcode | source / target | fields |
|---|---|---|---|
| `interrupt` | `SMSG_SPELL_INTERRUPT_LOG` `0x67000D` | interrupter / interrupted unit | `spell_id` is the interrupting spell, `interrupted_spell_id` the stopped cast |
| `dispel` | `SMSG_SPELL_DISPELL_LOG` `0x670007` | dispeller / target | `spell_id` is the dispel, `dispelled[]` the auras (spell, harmful) |
| `absorb` | `SMSG_SPELL_ABSORB_LOG` `0x67000C` | shield caster / victim | `spell_id` is the shield, `absorbed_spell_id` the incoming spell, `amount` absorbed, sample (victim) |
| `miss` | `SMSG_SPELL_MISS_LOG` `0x670034` | caster / each victim | one row per entry, `reason` = miss reason (dodge, parry, evade, immune, …) |
| `resist` | `SMSG_PROC_RESIST` `0x450205` | caster / target | `reason: proc` |
| `immune` | `SMSG_SPELL_OR_DAMAGE_IMMUNE` `0x67001D` | caster / victim | `IsPeriodic` is parsed, not written to the row |
| `instakill` | `SMSG_SPELL_INSTAKILL_LOG` `0x670021` | caster / target | `spell_id` |
| `environmental` | `SMSG_ENVIRONMENTAL_DAMAGE_LOG` `0x67000E` | empty / victim | `amount`, `reason` = exhausted/drowning/fall/lava/slime/fire/fall-to-void, sample (victim) |

The attacker-state hit info (D's "misses, immunes, resists" second half) is the melee row's `reason`:
`AttackRound.RoundReason()` names `HITINFO_MISS` or the victim state (dodge, parry, interrupt, block, evade, immune,
deflect), so a swing that did not land stops looking like a zero-damage hit.

The `SMSG_SPELL_INTERRUPT_LOG` writer swaps its two spell fields: `InterruptedSpellID` carries the caster's spell
(`m_spellInfo->Id`, `Spell.cpp:5317`) and `SpellID` the victim's stopped cast (`SpellEffects.cpp:3136` passes
`curSpellInfo->Id`). The parser names them semantically and the row keeps `spell_id` = the interrupting spell, so the
Kick/Counterspell trials match a response by spell.

Consumers:

- **Bug hunter rule 15 (missed decurse).** `Run.dispelRemovals` indexes the dispel rows; an interval whose victim and
  spell appear ends at that row's own time (and an out-of-view interval the row resolves is no longer "unknown"). A
  run from before this round has no rows and is judged from the frames exactly as before.
- **Kick/Counterspell trials.** `dps-rogue-kick-caster` and `dps-mage-counterspell-caster` now use response
  `kind: interrupt`; the row names the interrupting spell, so a Kick that the server did not land on a live cast is
  no answer. The pool, the debounces and the thresholds are unchanged.
- **Trials normaliser.** `KindMap` maps the new kinds (`environmental` → `damage`, the rest by name) and carries
  `reason`, `interrupted_spell_id`, `absorbed_spell_id` and `dispelled` into `events.jsonl` when present.
- **Bug hunter rows.** `bughunt.CombatRow` reads the four new fields, so the rules can use them without a schema
  change to a recording.

## Bounds and hostile input

Every count this round reads is checked twice (its own cap and the bytes left) before allocation: threat lists 512,
dispels 64, misses 256; the world's threat table is capped at 1024 mobs and its encounter ring at 64 events. Every
parser requires exact consumption, so a truncated payload is an error at every prefix length and a trailing byte is
an error too (`TestGainsTruncation` walks every byte of every new packet). `FuzzGainsPackets` mutates them all; it
ran 60 s on the VM with no panic (gate line below).

## Tests

- `combat/gains_test.go`: round-trip fixtures for all 15 new opcodes (built from the writers), `BuildSetAdvanced...`
  bit, hostile counts, the prefix/trailing-byte sweep, and the fuzz target.
- `objects/gains_test.go`: the threat edits, the 5 s window, the table bound, encounter events, kill-credit dedupe,
  the ring bound and the map reset.
- `world/combatlog_test.go`: the row fields and the sample; `fake_world_test.go` asserts the advanced flag is sent
  once and `TestEnterAgainstFakeServer` carries it in the sent list.
- `driver/gains_test.go`: the 5-minute fight's poll count, the fallback at 2 s, `boss_pull`/`boss_kill` on the trace,
  `boss_wipe` for a failed end, the watch's encounter records with the `.tb instance` read suppressed, and the old
  read cadence when an instance sends nothing.
- `bughunt/rules_events_test.go`: `TestRule15DispelEventEndsTheDebuff` (a dispel at 6 s turns a 30 s episode into no
  finding; a 16 s dispel ends the hit at 16).
- `trials/tests/classes` and `trials/tests/pooled`: the interrupt response; `scenario.kindEnum` accepts the new kinds.
- `wire/opcodes_test.go`: every new opcode translates to a wire number and back, and the client flag does too.

## Gates

PENDING

## Live checks for the lead's live queue

No live run was made this round. The first live session should check, on a test server:

1. **Threat table versus `.tb threat`.** One fight on a test server with `forever dungeon-watch` (or a run):
   compare the frame's mob victim/top with a manual `.tb threat <tank>` at the same moment. Expected: the same mobs
   and victim, with threat in points (the packet's x100 divided once). Then check the suppression: the log lines
   should show the 10 s `.tb threat` reads, not one per frame, and no frame should carry a stale victim.
2. **Encounter start/end on a scripted boss.** In an instance with `DungeonEncounter` data (ZG or Onyxia), pull a
   boss: one `watch: boss pulled encounter=…` line at the pull, one `boss killed … duration=…s` at the kill, and
   `encounters` in `<run_id>.forever-run.json`. A wipe or reset should say `boss wipe` with `success: false`. On
   the classic Ragefire Chasm bosses (no script data) the `.tb instance` cadence must still appear and the summary's
   `bosses` map must still fill.
3. **One interrupt.** Kick or Counterspell a casting mob in a fight: the combat log must gain a row
   `kind=interrupt`, `spell_id` = Kick/Counterspell, `interrupted_spell_id` = the mob's cast, source = the bot,
   target = the mob. In a trials repeat, `DPS-ROG-KICK`/`DPS-MAG-COUNTERSPELL` should count the interrupt as the
   answer (the event stream shows `kind: interrupt`), and a Kick into a finished cast should not count.
4. **Advanced log block.** In the same fight, a `melee`/`spell_damage`/`heal` row should carry
   `sample.health`/`sample.power`; the healer's mana curve should move between the 1 Hz sample rows. If every row
   lacks it, the flag packet did not reach the server (check the log line `CMSG_SET_ADVANCED_COMBAT_LOGGING sent`).

## Known limits

- The state read stays at 2 s (`stateFreshFor` gates on it); only the threat read is replaced by pushes. A future
  round could move the party's own health/mana to the log events and then slow the state read, but the gates also
  need the party's order/position state, which no combat packet carries.
- The pushed table is what this client can see: a mob outside the observer's visibility sends nothing to it, so the
  fallback read still matters for an observer standing away from the party.
- `.tb instance` remains the only boss evidence for an instance whose script has no `DungeonEncounter` rows; the
  core-side RFC boss states are a separate small PR (item 7/2 of the research note).
- The environmental/resist rows keep the single `amount` field; resisted and absorbed parts of an environmental hit
  are parsed but not written to the row (the shield absorb's own row names that part).
