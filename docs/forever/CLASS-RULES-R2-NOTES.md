# Forever #115 round 2: the class rules and a valid Innervate trial

Round 1 (#238) scored bots against player choices (`data/aotc`). Round 2 adds the class side: what the
party did with its roles, judged from one repeat's own recording, and an Innervate trial whose rule
can actually fail.

## Rule `HEAL-ASSIGNMENT` (package `internal/trials/classrules`)

Reads one repeat: the party from the trial definition (the player's role or forced role, the bot's
role, each extra's role) and the setup's guids, plus the recording's marks and samples (the fight
window and the excluded spans) and the normalised events' heal rows (caster, target, amount). One
fight per repeat: `fight_start` (the pull, the fight's first hostile action) to `fight_end`, or the
last in-combat sample when the end mark is missing. Only heals inside it and outside the excluded
spans count; a fight under `MinFightSeconds` (30 s) is INCONCLUSIVE.

- **Healers** are the party members whose role is `healer`; a healer is judged only with
  `MinHealerHeals` (10) placed heal events of its own (a placed heal is one on a named party member).
- **Tanks** are the party members whose role is `tank`.
- **The main healer** is the judged healer with the most healing.
- **PASS** needs all three: the main healer puts at least `MainHealerTankShare` (60%) of its healing
  on the tanks; every other judged healer puts at most `OtherHealerTankShare` (40%) on them; the
  other healers healed at least `MinRestTargets` (2) distinct non-tank party members between them.
  The 60/40 pair mirrors to one split between the tank healer and the group healer; 10 placed heals
  is about one every 3 s in the rule's 30 s fight, and two distinct allies is a spread rather than a
  single off-target.
- **INCONCLUSIVE, never FAIL, on missing evidence**: no `events.jsonl`, no fight window, a fight
  under 30 s, no heal rows, no tank role, no healer role, a tank or healer with no guid in the
  setup, fewer than 10 placed heals by every healer, a single judged healer (a split needs a
  second), a placed heal row with no readable amount, or two healers tied on healing.
- Heals on pets or units the setup did not name are left out of the shares and reported in an
  `other` count; they never make the rule INCONCLUSIVE by themselves.
- A healer's heals on itself count in its own total but neither for the tanks nor as cover of the rest.
- The shares are **gross** healing: the combat log's `amount` is `SMSG_SPELL_HEAL_LOG`'s
  `OriginalHeal` and carries no overheal (internal/forever/world/session.go writes the row; the
  packet's `OverHeal` is not recorded), so a heal that mostly overheated still counts in full.
- What it cannot see: overheal, absorb/shield healing (no heal row), mana spent per heal, whether a
  heal was needed, a pet's own healing.

`trials score --class-rules` adds the rows under `trial.json`'s `class_rules`; the card prints them
under `## Class rules`, `card.json` carries `class_rules`, and the count line names the class rules'
`PASS`/`FAIL`/`INCONCLUSIVE` totals. They never change the exit code and a default score is
byte-for-byte the bench's.

Thresholds names: `MainHealerTankShare`, `OtherHealerTankShare`, `MinHealerHeals`, `MinRestTargets`;
each has a boundary test that fails when the constant moves.

Limit: no shipped trial stages two healers (the live runner's damage situations hurt one victim, and
`member_damage` has no runner yet), so on today's trial set the rule reports "one healer only" —
INCONCLUSIVE. It judges any recording that does stage two healers, and the synthetic recordings in
`internal/trials/classrules/heal_test.go` are the rule's own proof until such a trial lands.

## Trial `priest-holy-low-mana-innervate-ally` (edge)

The round-1 trial (`priest-holy-low-mana-innervate`, from #191) is kept as the Python bench defines
it. The round-1 live attempt failed on setup (the Forever overlay has no druid kit, so the extra's
`bot_items` row was SKIPPED) and its rules could not express "the cast landed on a low ally", so the
evidence was thin. The round-2 trial stages the same fight so the rule can fail:

- `bot`: a level 60 Holy priest, healer, `mana_pct` 15 (under the module's 25% Innervate threshold).
- `extra1`: a Balance druid with no `gear` block, so the extra needs no kit.
- `situation`: `tank_damage`, two melee mobs, `refill`, `duration_s` 90, `min_valid_s` 60 — long
  enough for the ally to sit low and the druid to act.
- `opportunity`: `{"unit": "$bot", "power_lt": 35}`: the chance the rules need is the priest's own
  mana under 35% for at least 5 s (`min_opportunity_s`).

Rules:

| Rule | Measure | Verdicts |
|---|---|---|
| `DRU-BAL-INNERVATE-LOWMANA-TARGET` | landed Innervate auras by `$extra1` on `$bot` while `$bot`'s mana reads under 35% (`dst_power_lt`), at least 1; a recording with no aura rows falls back to the cast (`where_fallback`) and says so in the evidence | PASS on such a landing; FAIL when the ally was low and it did not land (a cast elsewhere, a cast on a target above 35%, a cast with no aura, or no cast); INCONCLUSIVE when the ally never sat under 35% for 5 s |
| `DRU-BAL-INNERVATE-NOT-SELF-TARGET` | the druid's Innervate casts on itself over its own casts (`ratio`), 0 | PASS/FAIL when the druid acted and the ally was low 5 s; INCONCLUSIVE on a zero denominator (the druid cast nothing) or from the opportunity floor |
| `PRI-LOWMANA-ACTED` | the priest's own casts, at least 3 | precondition, `expect_today` `?` |

## Engine keys the trial needed

- `where.dst_power_lt`: the target's own bar percentage at the event's time, read from the freshest
  sample at or before it (the mirror of `src_power_lt`). A target with no bar or no sample never
  matches: an unknown bar is not a low one. Takes the `$critical`/`$low`/`$medium` aliases.
- `opportunity.power_lt` / `power_gt`: the opportunity window on the unit's own bar, so
  `min_opportunity_s` can require the ally to have actually been low.
- `check.where_fallback`: a second where filter read instead when the recording carries no aura
  rows at all. An agent whose combat log never writes an aura event cannot answer an aura check, so
  the cast form stands in; the evidence names which form was read. Count checks only (the note is
  part of a count's evidence).

They are tested directly (`internal/trials/rules/power_test.go` and the edge trial's tests),
including the sample that closes each interval, a barless sample, the excluded overlap, a fight the
druid sat out, a self-cast with and without a low ally, an aura recording with no landing, and the
cast fallback in a recording with no aura rows.

## Round-1 limits

- **The wrong hybrid claim was the plain bug.** The round-1 comment, the manual and the bench notes
  said a hybrid is "not read as a pure caster" and named enhancement. Its own three priority rows
  are stamina, intellect and spell power (strength is about equal to spell power at rank 3, outside
  the three rows the set reads), so enhancement reads as a caster — which is the mail its players
  wear and what keeps a shaman in warrior plate contrary. The code is right; the text is fixed and
  `TestGearSpecFamilyReadings` pins what each spec reads as. Widening the reading to catch the tied
  strength row was rejected: it would turn the shipped must-FAIL case (a shaman in a warrior plate
  belt) into a pass, which is the missing-armour-type limit's job to fix (item read-out, #20), not a
  threshold's.
- **The known false failure is pinned** (`TestGearEnhancementHunterOnlyItems`): hunter-worn agility
  items are FINE in Shoulders, Chest and Legs and FAIL in Wrist, Hands, Waist and Feet — 4 of the 8
  armour slots, 4 of the 7 that have such an item at all. `TestGearHeadHunterItemIsFineByItsOwnData`
  pins why Head is not one of the four (the shaman's own tables name those pieces).
- **The largest-family clause cannot matter on this data** (`TestGearLargestFamilyClauseOnShippedTables`):
  every acceptable set and every item reading is the 20% floor's alone. The arithmetic: a
  contribution's share counts once per family it names and a family set holds at most three families,
  so the largest family always holds at least a third of the summed share, above the 0.20 floor. The
  test measures the minimum share and fails if a data refresh or a floor change lets the clause bind;
  the clause stays as a guard for a family set that grows past five entries.

## Files

- `internal/trials/classrules/` — the rule, the record loader, the per-trial rows, tests.
- `internal/trials/rules/rules.go` (+ `power_test.go`) — `dst_power_lt`, `opportunity.power_lt/power_gt`.
- `internal/trials/scenario/scenario.go`, `validate.go` — the closed key sets and their number checks.
- `internal/trials/trialdefs/edge/priest-holy-low-mana-innervate-ally.json` (+ the edge tests).
- `internal/trials/score/score.go` (+ `classrules_test.go`) — `Options{ClassRules}`.
- `internal/trials/report/report.go` — the `## Class rules` section and `card.json`'s `class_rules`.
- `cmd/forever-agent/trials_cmd.go` — `--class-rules` and its count line.
- `internal/trials/playdata/gear_limits_test.go` — the round-1 limits measured on the shipped data.
- `docs/manual/testing/trials.md`, `docs/manual/log.md`, this file — the manual's rules and limits.

## What a live run must show

A repeat of the new trial where the setup rows (`bot_mana` 15%, `extra_bots_present` with the druid,
`mobs_ready`) pass and the combat-log feed carries rows: then `DRU-BAL-INNERVATE-LOWMANA-TARGET`
PASSes only with a landed Innervate aura on the priest at under 35% mana (or, in a recording with no
aura rows at all, the cast, with the evidence saying so), and reports INCONCLUSIVE with "opportunity
0 s < 5 s" when the priest never sat low. The round-1 live run's blocker (an empty combat log:
`gm_combatlog` needs a selected unit) is a runner/server-side item, not a trial rule.
