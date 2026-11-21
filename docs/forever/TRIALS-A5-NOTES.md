# Trials round A5: the other situations, the data check and the class sweep

`internal/trials/live` now runs every situation kind the trial set uses
(`tank_damage caster_mob pack cc_target debuff ooc_buffs ooc_hurt`), the
option-carrying tank trials, `trials forever-data-check` and `trials sweep`.
A4's tank_damage loop is unchanged in shape: the new kinds add their trigger
to it instead of a second loop.

## Layout

| file | mirrors | what |
|---|---|---|
| `situations.go` | `trl/live/situations.py` (`Situation.run`, `Governor`) | the arena fight loop shared by `tank_damage`, `caster_mob`, `pack`, `cc_target` and `debuff`; the pull, the governor, refill, the excluded spans |
| `situations_more.go` | the same file, the ooc and per-kind halves | `prepull_unaura`, `reset_hire_cooldowns`, the tested debuff (strip, apply, refresh), the cc mark/Taunt/engage order and the `cc_gap` span, the threat spell, the rogue energy drain, the caster proof, `ooc_buffs` (meal wait, mana restore) and `ooc_hurt` (the hurt cadence, the floor governor, the stance re-cast) |
| `collect.go` | `collect.py` (`mob_casts`) | `LastRawSeq`, `AuraOnSince` (the fresh trigger proof) and `MobCasts` (the caster proof) over the pump's own rows |
| `client.go` | `agent.py` | `Bot` (one `.bot` line, the module's own command surface), `WalkTo`, `NormGUID` |
| `datacheck.go`, `datacheck_live.go` | `build_data.py`'s live checks (no Python command exists) | the plan (every spell, item and mob entry the trials and kits name), the probe and `forever-data-report.json` |
| `sweep.go` | `trials.py`'s selection/`--class` (no Python sweep doc exists) | the class sweep's order, skips, per-trial verdict and `sweep.md` |
| `cmd/forever-agent/trials_datacheck_cmd.go`, `trials_sweep_cmd.go` | — | the two commands: lease a slot, build the probe/plan, write the report/cards |

## Decisions

- **One fight loop.** `debuff` stamps `fight_start` BEFORE its apply (the
  apply is the trial's latency zero), so the `aura_applied` trigger is a
  t >= 0 row. The strip proves the debuff absent before the pull; a strip
  that cannot be proved ends the repeat INVALID (`prepull`), never a scored
  refresh.
- **`cc_target` without the `mark` RPC.** ForeverCore's serve does not port
  `mark` (`serve_session_methods.go`: `notPortedMethods`), and the module's
  `.bot cc moon <bot>` sets the raid icon itself on the selected target
  (`commands/Action.cpp`, `RunCcOrder`). The runner selects the mob, sends
  `.bot cc moon <bot>` through the serve's `bot` method, and takes the
  module's `TBM:ACTION_ACK|cc moon|...|1|...` as the proof. The icon cannot
  be read back on Forever; `cc_mark_set` says so in its evidence. The marked
  mob is never attacked (only Taunt 355 goes to it); every unmarked summon is
  attacked, and `mobs_ready` grows the `N/M unmarked summons in combat` and
  `marked ... in combat: yes/no` evidence.
- **`ooc_buffs` never strips a meal.** A Drink/Food aura is waited out
  (bounded, 30 s) and the trial's `bot.mana_pct` is re-applied afterwards
  through `.modify mana <value> <max>`; a meal still up, or an aura list
  that never reads, opens no window (`meal still active` / `meal state
  unknown`). An unreadable list is retried inside the same wait.
- **`ooc_hurt` does not raise a victim with a GM hand.** The core's
  `.modify hp` sets current AND maximum health to its one argument
  (`cs_modify.cpp: HandleModifyHPCommand` ignores a second value), so the
  A4 decision stands: a victim above the band is brought down with `.damage`,
  and a victim below the band is left to its healer (logged, the cadence is
  spent). A bot victim uses `.tb hurt <bot> <pct>` as in A4.
- **The floor governor for an extra victim.** An `extra_bots` victim leaves
  the player standing, so ooc_hurt adds a second, floor-only governor for
  the player (the Python's `guard_gov`).
- **`min_rep_gap_s` waits a full gap between repeats.** The Python reads
  the newest `Restore Mana` wall across earlier reps and waits only the
  remaining gap (and does not wait at all when no potion was cast). This
  round has no potion-wall read: the rep boundary always waits the option's
  full value. That is stricter than Python and never shorter; no embedded
  trial sets the option, one reference tank trial does.
- **Data check is plan + probe.** The plan is pure and reads the embedded
  trials, the gear kits (base + Forever overlay), the item-name overlay, the
  spec markers and the merged mob table. The probe is the live half
  (`.tb learn`, `.tb itemscan`, `.npc add temp` + a unit read + `gm_kill`).
  Only the server's own word is a data diff: `.tb learn` answering `unknown`
  for a spell the embedded table knows, an `itemscan` record `exists=no` for
  an overlay item, a mob level outside its table range. A refusal, a missing
  row, `invalid`/`not-learned` or a level problem is `unknown` with the reason,
  never `differs`. Every spawn is ledgered (with the serve dir) before the
  kill.
- **Data check follows the A4 safety rules (review round).** It acquires an
  arena (lock, `.go xyz`, quiet probe) before the first spawn, runs the
  stranger check before every spawn and while a spawn is read (a stranger
  halts the remaining spawns; they are `unknown`), builds its Guard with the
  run's abort (lost lease, interrupt), writes its ledger to
  `<run dir>/ledger.jsonl` (`--dir`, default `<report>.run`; `trials cleanup
  <run dir>` finds the lease row and the summons) and tears down on a fresh
  context on every exit (Ctrl-C included): every ledgered unit not proved dead
  is killed again and read back, what stays is `leftover` in the report. A
  unit is claimed only when it is provably this probe's own: absent from the
  snapshot taken just before the add, the only new unit of the entry, at the
  tester (5 yd). The serve's `gm` answer carries no text, so the add's reply
  cannot name the guid; two new units, or one standing elsewhere, are
  `Unidentified` (nothing is killed, the run is incomplete). The exit code is
  non-zero for `differs`, a leftover or any `unknown` (`--allow-unknown`
  accepts unknowns). The scratch bot is a dedicated character
  (`--scratch` or the server's `forever.scratch_bot`): refused when empty, in
  `bot_pool`, the slot character or already online (never taken over); it is
  logged out again on every exit. `.tb learn` persists on it by design because
  no trial ever hires it (there is no unlearn command).
- **Sweep is one run dir per trial.** "One card each" is implemented
  literally: `--out DIR` holds `DIR/<trial>/` with `RUN.json`, the repeats,
  `trial.json`, `card.md` and `card.json`; `DIR/sweep.md` is the order
  table (tanks first, then each group by id), the per-trial verdict and the
  card path, plus the class-flag skips. A class not named by `--classes` is
  a skip with its reason, never a silent drop. `TrialResult(out, id)` reads
  RUN.json under the runner's Out and trial.json at `TrialJSONPath(out, id)`
  (`Out/<id>/trial.json`), the one helper RunTrial writes through. The loop is
  `RunSweep`: a real FAIL is a FAIL row; PASS needs no INCONCLUSIVE/INVALID
  rule; a stop (lost session, interrupt, a teardown that could not be proven)
  lists every unrun trial as SKIPPED (`not run: ...`); the exit is 0 only when
  every planned trial ran and PASSed. `trialTeardown` returns what it could
  not prove (items not cleared, a refused logout, the role not cleared);
  RunTrial records it in RUN.json `teardown_unproven` and returns a
  `TeardownUnproven` error after scoring.
- **Rows are ok only after a read-back (review round).** `cc_mark_set` stays
  failed on the module's ACK and flips to ok the first time a crowd-control
  aura (the trial's `opportunity.has_aura`, else the Polymorph ranks) is READ
  on the marked mob; `Guids.Marked` is now set so the scorer's `$marked`
  resolves. `hire_cooldowns_reset` is ok only when the hire's unit row carries
  a `cooldowns` list and it reads back empty; today's serve unit row carries
  none, so the two trials that set `reset_hire_cooldowns` score INVALID with
  that reason until the serve exposes the list (blocker, not a silent pass).
  A refused `modify energy`, or a bar that reads back above the target, is a
  failed `energy_drained` row. A refused cooldown line writes
  `cooldown_reset_refused`, not the energy mark.
- **ooc_buffs and ooc_hurt run in the arena (review round).** Both acquire an
  arena, bring the hire in, check for a stranger before the first GM line, in
  the meal wait and every 5 s in the window; ooc_hurt looks again right before
  each hurt line. A stranger ends the repeat INVALID `real_player`.
- **Lows (review round).** `AuraOnSince(seq, spell, target, since)` needs a
  row with a seq past the cursor, exactly the victim as target, and an arrival
  at or after the apply was sent. `trackCCGap` reads the summons once and skips
  on an error. The cc order is `Guard.CCOrder(botName)` (name-validated, the
  guard builds `.bot cc moon <name>`, ledgered, obeys the abort); the raw
  `Client.Bot` is gone. The governor sample's `auras` is nil when the unit row
  carried no list, so `keepStance` and `refreshDebuff` do nothing on unknown.
- **The stance's threat spell is learned with the stance.** `check_stance`
  in the Python bench learns the stance AND its `THREAT_CAST` spell (355
  Taunt for Defensive Stance); `player.go` now does the same. Without it the
  fight's `threat` casts and the cc_target Taunt would be refused on a
  GM-levelled character that never learned its later spells.
- **Where the caster proof comes from.** `mob_caster_casts` is appended by
  the situation after the fight from `Collector.MobCasts(summoned)`: a
  `spell_start` or `spell_go` row whose SOURCE is a summon. Another unit's
  cast never proves it (the A5 test pins both).

## Tests (fake-driven, no server)

| test | what it pins |
|---|---|
| `TestCasterMobProof` | a summon's own `spell_start` and `spell_go` rows prove the caster; another unit's cast does not |
| `TestPackPullsEverySummon` | the pack attacks every summon, not only the pulled one |
| `TestDebuffAppliedAfterFightStart` | `fight_start` precedes the fresh `debuff_applied`; the trigger is fresh |
| `TestCCTargetNeverAttacksTheMarkedMob` | the marked mob is never attacked, both unmarked ones are, `cc_mark_set` ACKs and `mobs_ready` carries `2/2 unmarked ... in combat` and `marked ... in combat: yes` |
| `TestOOCBuffsWaitsOutTheMeal` | a meal is never stripped, the trial mana is re-applied, no window before the meal ends |
| `TestOOCBuffsMealStillActiveInvalid` | a meal outlasting the wait opens no window (`meal still active`) |
| `TestOOCHurtSetsHealthOnSchedule` | the hurt cadence damages into the band, no `.unaura`, the stance is re-cast, the marks are `ooc` |
| `TestOOCHurtBelowBandWaitsForTheHealer` | a below-band victim is not raised (the maximum would shrink) |
| `TestPrepullUnauraProvesTheAuraGone` | an aura the unaura left behind invalidates the repeat before the summon |
| `TestDataCheckReport` | ok/unknown/differs partitioning and every spawn killed |
| `TestBuildDataCheckPlanNamesEveryTrialSpell` | the plan carries trial spells (770, 16462) and the Forever mob entry 2734 |
| `TestSweepOrderAndSkips` | asked classes only, tanks first, no duplicate, druid listed as a class-flag skip |
| `TestTrialResultReadsThePathRunTrialWrites`, `TestRunSweepFailsOnARealFail` | RunTrial's trial.json is where TrialResult reads; a real FAIL is a FAIL row and a non-zero exit (reverting the path gives the review's `INCONCLUSIVE x3`) |
| `TestRunSweepListsUnrunTrialsAsSkipped`, `TestTrialResultPassNeedsNoOpenRule` | a stopped sweep lists every unrun trial; PASS+INCONCLUSIVE is not PASS |
| `TestTrialTeardownReportsWhatItCouldNotProve` | a refused logout is a teardown problem |
| `TestSpawnNeverClaimsAnExistingUnit`, `TestSpawnAmbiguousIsNeverKilled` | a unit that was already there, or an ambiguous spawn, is never claimed or killed |
| `TestLiveDataCheckRunsInTheArenaAndLedgersItsSpawns`, `...StopsOnAStrangerDuringTheWindow`, `...TeardownRunsAfterAnInterrupt`, `...StopsOnALostLease` | arena first, stranger stops the spawns, Ctrl-C still kills and ledgers, lost lease stops |
| `TestCheckScratchRefusesPoolBots` | no pool bot, empty name or slot character as scratch |
| `TestDataCheckReport`, `TestDataCheckHaltStopsTheSpawns` | refusal/no-row/unanswered is unknown, never differs; unknown fails the exit |
| `TestOOCBuffsNeedsAQuietArena`, `TestOOCBuffsStrangerDuringTheWindow`, `TestOOCHurtStrangerBeforeTheNextHurt` | arena and stranger checks in both ooc kinds, hire brought |
| `TestCCMarkNotSetOnAnACKAlone`, `TestCCMarkSetOnceTheAuraIsRead` | cc_mark_set needs an aura read; `Guids.Marked` set |
| `TestHireCooldownsResetNeedsAReadBack`, `TestEnergyDrainRowFailsOnRefusal` | read-back rows, mark kinds |
| `TestAuraOnSinceNeedsAFreshRowForTheVictim`, `TestCCGapUsesOneRead`, `TestCCOrderTypedAndGuarded`, `TestUnknownAuraListsStayUnknown` | the lows |

The live cases the Python tests kept untranslated are these fake tests:
`test_cc_target` (`TestCCTargetNeverAttacksTheMarkedMob`), `test_drink_settle`
(`TestOOCBuffs*`) and `test_ooc_hurt` (`TestOOCHurt*`).

## Live queue (not run in this round)

The brief says no live run. These are for the lead's live queue, in order:

1. `trials forever-data-check --slots F --server S --scratch <dedicated bot> --out forever-data-report.json` (the serve must run with GM units allowed, as A4's live run did; the scratch bot is never a `bot_pool` bot, set `forever.scratch_bot` to make it the default); expect the copied `Sweet Nectar` id (17083) to be a `differs` unless the overlay already maps the name (it does: 1708), and read the mob-level diffs against `arenas.json`'s level windows. Unknown rows make the exit non-zero (`--allow-unknown`). `trials cleanup <report>.run --slots F` replays its ledger.
2. `trials sweep --classes warrior,priest,mage,rogue --slots F --server S --repeats 1 --out DIR`; check `DIR/sweep.md` puts the two tank-role warrior/druid/paladin trials first and the cc_target trial's card shows `cc_mark_set`.
3. `trials run ... dps-mage-cc-respect` once: the module's `.bot cc moon <bot>` ACK and the Polymorph row (the `mark` RPC is not ported; the icon proof is the module's own ACK).
4. `trials run ... priest-holy-low-mana-drinks` once: the ooc meal wait against a real setup meal.
5. `trials run ... priest-holy-pws-def-arms-ooc` once: the hurt cadence at 12 s and the floor governor's `OOC RESCUE FIRED` line (out of combat it should never fire).
6. `trials run ... priest-shadow-form-prepull`, `dps-rogue-low-energy`, `dps-rogue-thistle-tea` and `tank-paladin-defensives` once each: the prepull unaura, the energy drain (`energy_drained` row) and the cooldown reset. The two `reset_hire_cooldowns` trials stay INVALID until the serve's unit row lists the hire's cooldowns (see Decisions).

## Gates

On the final committed code tree, commit `51ed4a41` (review-fix round; the only later change is this gates section):

- `make fmt-check`: exit 0, no output.
- `make cross`: rc=0 (windows/amd64 and darwin/arm64 builds, then the windows vet of `./cmd/... ./internal/forever/...`).
- `staticcheck ./...`: exit 0, no findings.
- `vm-test ta`: `RESULT: PASS  kind=ta sha=e8f3661a dirty=0 files=2453` (rc=0, `go test ./... && go vet ./...`, run on the tree after merging origin/main).
- `go test -race -count=1 -timeout 20m ./internal/trials/...`: rc=0, every package ok, including
  `ok  github.com/pjerra/forever-agent/internal/trials/live  300.296s`.
  (The first race run found a data race in the test fakes, the unit-row maps handed to the collector while a hook flipped them; `sim.unitRows` now returns copies and the cc hooks lock the world.)
