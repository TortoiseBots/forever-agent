# Round B3: the event rules, the tickets and the command

The bug hunter can now judge the rules that need the fight itself, and it writes the tickets the board carries.
`internal/bughunt` gains rules 1, 2, 4, 8, 11 and 15 over a run's combat log (C) beside its frames (F), the
live-checked spell names and dispel types those rules read, a ticket file with stable ids and a baseline, and
`forever-agent bughunt`. With B2's frame rules that is every rule the plan kept; 7 waits for pet data and 12 for
raids, as the plan says.

## What was built

**The combat rows on the run's clock.** A watch's rows carry a wall time; a trials repeat's `raw-ta.jsonl` carries
the same shape. Both loaders now stamp every row with `T` (seconds from the run's t=0) and `TKnown`, derived from
the row's `time` and the run's own start, and canonicalise both guids to the trace's one text (`0x` and 32 upper-case
hex digits). A row whose time cannot be read, or a run that does not know its start, stays in the log but no rule
places it in time: unknown at a rule is never t=0.

**One identity for a row and a frame.** A watch keys its members by guid and a trials repeat by character name. A
new `Run.ByGUID`/`Run.GUIDOf` pair (built in `finalise` from the frames' `g`) joins them, and every rule reads a
row's source and target through `Run.Member`. So rule 4 on a repeat says the hit belongs to `Cmage`, not to
`0x…0A03`, and rule 8 groups a repeat's casts per member the same way.

**The rules** (`rules_events.go`, thresholds in `const.go` with the Python names and values):

| Rule | Forever shape | Key |
|---|---|---|
| 1 idle in combat | alive, group in combat 3 s+, still, not casting, no `spell_start`/`spell_go`/`melee` row from it within 1 s, a healer with nobody under 95 %, and (when the frame carries one) an idle action word | `(1, role, act)` |
| 2 healer not healing | healer, mana over 10 %, an alive ally under 40 % within 40 yd, not casting, no `heal` row or heal-spell start/completion within 1.5 s | `(2, class)` |
| 4 standing in an AoE | `periodic`/`spell_damage` rows of one named area spell on one member, ticks more than 3.5 s apart ending it, over 3 s | `(4, spell)` |
| 8 buff flip-flop | more than 3 `spell_go` of one named buff on one target in 60 s, each genuine when a `spell_start` of the spell came within 0.5 s before it or its `aura_applied` within 0.2 s after | `(8, spell, short-aura or long-aura)` |
| 11 cast-cancel signal | a mana-casting member in combat with no damage, heal, swing or periodic row from it, episodes over 10 s, at least 2 started casts that never completed within 2.5 s, mana risen; `spell_fail` reasons in the evidence | `(11, role, class, cast-cancel-signal)` |
| 15 missed decurse | a dispel-file debuff on a member over 8 s while an alive, not-casting member of a class that can remove that type stood within 30 yd after the grace | `(15, type, name)` |

The Python's `CHARM_AURAS` exclusion for rules 1 and 2 was a walk input and is not ported: the B3 row names F's
in-combat and still flags and C's silence, and a charmed bot is a false positive the record cannot tell apart until a
rule for it is decided.

**The lexicons** (`lexicon.go`). `--names` and `--dispel` are required inputs for the rules that read a name or a
type; both loaders accept a plain JSON object, the trials `spells.json` shape (`by_id`, values `{"name": ...}`) and,
for dispel, the core's numeric `spell_template.dispel` column (0 never, 1 magic, 2 curse, 3 disease, 4 poison).
`magic`, `curse`, `disease`, `poison` and `never` are the type words; `never` is the Python's must-stay policy,
which the data file now carries instead of a Go table. A name rule matches only a name the file gives: an unnamed id
is `spell <id>` and never matches the buff, AoE or heal patterns. The patterns are the Python's own, with `(?i)`
restored (the Python compiled them with `re.I`) and the non-capturing groups rewritten for Go's RE2.

**Skipped checks are counted.** A `Rule` now has a `Skipped` reason. `Hunt` counts a run a rule cannot judge as one
skipped check and never calls the rule: no combat log skips 1, 2, 4, 8 and 11; no names skips 2, 4 and 8; no dispel
types or no known classes skips 15; no meal report skips 13 (B2's rule). `counts.json` carries every rule's hits,
runs with hits, skipped runs, per-dataset hits and a `skip_reasons` map.

**The tickets** (`tickets.go`). One ticket per `(rule, key)`. The id is `BH-<rule>-<8 hex of sha1(rule|key)>` where
the key is the hit's own key joined with `|` (the key already begins with the rule, so the hash input repeats it:
`15|15|magic|Hex`); the same finding keeps its id across nights and datasets. A ticket's row is `{id, title, rule,
key, count, runs, first_seen, deaths_near, evidence, labels, status, cost}`. `cost` is the summed hit cost the
markdown ranks by; the rest are the plan's fields. `first_seen` is the earliest hit's wall time as RFC3339 UTC (empty
when the run does not know its start) and `labels` are `[bughunt, rule-N, role]` with the role `mixed` when a key's
hits disagree. Evidence is at most five `clock ds/run: evidence` lines, earliest first.

With `--baseline DIR` every baseline ticket is `known` (its earlier `first_seen` and its higher `deaths_near` kept),
a new key is `new`, and a baseline ticket the run does not see is `gone` (count and cost 0, no evidence, its row and
labels kept). `tickets.md` prints the header line and one table row per ticket, worst summed cost first, tie by id.
A pipe or newline in a cell is escaped so the table cannot split.

**The command.** `forever-agent bughunt [--rules LIST] [--names F] [--dispel F] [--baseline DIR] --out DIR INPUT...`
discovers a watch folder (a `.jsonl` that starts with a trace header), a watch trace file, or a trials repeat
(`samples.jsonl` and `events.jsonl`), recursively; each root names the dataset the counts group by. It loads the
runs, runs the selected rules, writes `hits.jsonl`, `counts.json`, `tickets.json` and `tickets.md`, and prints the
counts and the 15 worst tickets. A rule number this build does not have (7, 12) is a usage error, as are a missing
names, dispel or baseline file and a root with no recording in it. `main.go` registers the command and the usage
lists it; README documents it.

## Decisions taken here

- **The hash repeats the rule.** The key begins with the rule (B2's own shape) and the plan's `sha1(rule|key)` names
  both parts, so the input is `rule|key[0]|key[1]…`. Repeating it keeps the function literal to the plan and the id
  stable; a change of rule number changes both, which is what a rule rework should do.
- **`cost` is in `tickets.json`.** The plan's row list does not name it, but `tickets.md` ranks "worst summed cost
  first" and the board needs the number that ranks it; the row keeps every listed field unchanged.
- **Rule 15 reads the frames' auras, not the log's.** The plan's Forever input is "F auras per bot + dispel file +
  classes"; for a watch that is the client's own model, and for a repeat the samples' `auras`. The log's
  `aura_applied`/`aura_removed` rows still serve rule 8's genuineness and its aura life.
- **A trials repeat's raw rows are read when present.** The plan calls a repeat's input T and says it keeps the old
  files, one of which is the recorder's `raw-ta.jsonl`; its rows are the same shape as C, so the repeat gets the
  combat rules too instead of a permanent skip. Without the file the log rules are skipped as any run without C is.
- **Rule 4's source text** is the row's own source name (the module's name for the caster), else the member key: a
  name in a runtime record, never a name the repository holds.

## Tests

- `TestRule15MissedDecurse` (the round's first test): a 14 s magic debuff with a paladin six yards away is one hit
  with key `[15 magic Hex]`. `TestRule15NearMisses` covers a 6 s debuff, a dispeller at 40 yd, one casting, a
  must-stay type and no dispel file; `TestRule15UnknownClass` makes the class-less run a skipped check.
- `TestRule1IdleInCombat` (a 12 s silent stretch, a cast in the middle, a moving member),
  `TestRule1HealerWithNobodyHurt`, `TestRule2HealerNotHealing` (a heal row and a named shield break it, an ally out
  of range and low mana do not), `TestRule4StandingInAoE` (five ticks vs two, a direct spell),
  `TestRule8BuffFlipFlop` (four casts vs three, an unreal completion, the aura life in the note),
  `TestRule11CastCancel` (four starts and a failure reason vs a landed spell, a flat mana bar, one start).
- `TestUnknownSpellNeverMatches`: four casts and five ticks of an id with no name produce no hit and the name is
  `spell 999`.
- `TestEventRulesSkipWithoutInput`: the skip reasons and their counting.
- `TestLoadNames`/`TestLoadDispel` (plain, `by_id`, objects, numbers, every error's file and line),
  `TestStampCombatRows` (the run clock, an unreadable time, a run with no start, guid canonicalisation) and
  `TestLoadTrialsRawRows` (a repeat's raw rows land on its clock; without the file the run has no C).
- `TestTicketIDStable` (four golden ids), `TestBaselineNewKnownGone` (history kept, gone row kept, counts, labels),
  `TestTicketsMarkdownGolden` (`testdata/tickets.golden.md`), `TestWriteTickets`, `TestWriteHitsAndCounts`.
- The command: `TestBughuntCommandWritesTickets` builds a watch recording, hunts rule 15, reads the four files and
  runs the same input with no dispel file to see the counted skip; `TestBughuntCommandRefusesBadInput` covers a
  missing names file and rule 7.
- `TestParityWithPythonChecker` (skipped unless `BUGHUNT_PY_CHECKER` names the Python checker) builds one repeat
  whose events and raw rows describe the same AoE and buff flip-flop and requires rules 4 and 8 to produce the same
  keys in both engines (and that rule 15 stays silent there: a repeat's extras carry no class). It was run against
  the audit tree's checker; both keys matched and rule 15 reported nothing.

## What to check on the first real watch

1. **The signs' names.** The names file must be the one the live check wrote; until it has a spell's id, rules 2, 4
   and 8 treat it as unknown (`spell <id>`), which is a silent check rather than a false one. Rule 2 also needs the
   healer's heal spells named or a `heal` row.
2. **Rule 1 on a watch's frames.** The action word a frame carries today is the module's own `.tb state` (empty on a
   watch), so rule 1 judges by movement, the cast bar and the log; a bot with an instant rotation the log names is
   not idle, but a charmed bot still is (the note above).
3. **Rule 11's window.** A `spell_start` and a `spell_go` further apart than 2.5 s (a long cast completing after an
   interrupt-free cast) would read as unlanded; check the first healer repeat's casts against the log's own rows.
4. **Rule 15's auras are the client's model.** The commander must stand near the party (the plan's own limit); a bot
   outside its view carries no aura in the frame, so a debuff there is invisible rather than clear.
5. **Ticket keys across datasets.** The same role and spell in two watches are one ticket by design; a dataset whose
   roles differ (a watch's `heal`, a repeat's `healer`) is still one key only if the key's own words agree, and they
   are not normalised on purpose: the key is the finding, not a display text.
