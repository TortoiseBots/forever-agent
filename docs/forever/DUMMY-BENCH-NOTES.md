# Dummy bench round (issue #18): spell and damage bench on a training dummy

`internal/trials/live` runs a new situation kind `dummy` on a live
`forever serve`: one quiet arena, one spawned target (a training dummy or a
passive high-health creature the teardown kills), the class bot's rotation
for a fixed time. The combat log rows the agent already parses carry the
casts (spell_start/spell_go), the damage (`amount`) and the reaction clock
(able to act to cast start). `internal/trials/dummybench` scores them
against a small per-class table of expected numbers; the report card shows
per spell: damage, casts, mean/max reaction.

## Layout

| file | mirrors | what |
|---|---|---|
| `internal/trials/live/dummy.go`, `dummy2.go`, `dummy3.go` | `situations.go` (tank legs) | `dummyRun`: arena, quiet probe, hire, one `npc add temp` spawn claimed like the tank summon, the bot's attack order, the fixed-time fight loop, engagement/read-back rows |
| `internal/trials/live/dummy_score.go` | `run.go` scoring seam | per-repeat `verdicts.json` plus per-trial `trial.json`: one row per trial rule (`{metric: dummy, spell, stat}`), majority of valid repeats, tie FAIL |
| `internal/trials/live/dummy_test.go` | `a4_review_test.go` (harness shape) | scripted dummy repeats: spawn + fight, stranger, teardown |
| `internal/trials/dummybench/*` | `events.go` + `score.go` shapes | `Tally` (go = one cast, damage sums per spell), `React` (start minus previous go), `Score` (PASS/FAIL/UNKNOWN/INVALID), `TallyRep`/`ScoreRep` (verdicts.json) |
| `internal/trials/dummybench/testdata/dummy_mage_fire.json` | `data/forever/*.json` (documented format) | the round's one table: fire mage rotation + expects (all null = to be measured) |
| `internal/trials/trialdefs/dps/dummy-mage-fire.json` | `trialdefs/*` | the round's one trial: dummy kind, rotation, three dummy rules |

## Decisions

- A setup row is ok only after a server read-back; unknown stays unknown;
  INVALID or SKIPPED never become PASS (the A4 bench rule, unchanged).
- One cast is one `spell_go`; a cast-time spell's `spell_start` moves the
  go's time earlier, never counts twice. Damage sums per spell, never per
  row: unattributed damage (a tick, a proc) never invents a cast.
- Reaction is the cast's start minus the previous cast's go (the moment the
  bot was able to act again: cooldown/GCD spent, target in range on a quiet
  dummy). The first cast has no reaction: nothing before it proves when the
  bot was ready.
- A spell the table does not name (a stranger: a proc, a wrong rotation,
  another bot's cast) is INVALID, never a silent PASS. A number the table
  does not know ("to be measured", JSON null) is UNKNOWN, never FAIL. A
  table spell with no casts is UNKNOWN (expected but uncast).
- The trial's own rules name spells, not `verdicts.json` rows: one rule per
  spell per stat (`{metric: dummy, spell, stat}` with stat damage/casts/
  reaction). `pass_if` compares the rule's stat; the card shows the per-spell
  damage, casts, mean/max reaction from the repeat tallies.
- The dummy never fights back, so `mobs_ready` proves alive + entry only
  (no combat requirement, unlike the tank's row where combat is the hostile
  proof). Engagement is in-combat OR the collector's first cast row.
- A dead or vanished dummy ends the repeat `dummy_down` (INVALID, never
  scored): a bench that scores a dead dummy's leftovers would claim damage
  the rotation did not deal. The teardown kills the ledgered dummy through
  `gm_kill` and the heal refill, like every tank repeat.
- The arena lock is held by the `LiveSituation` until `Finish`, and a real
  player in reach ends the repeat INVALID `real_player` (checked at most
  every 5 s), same as the tank kind.
- The card's `metric k/n` column carries casts/casts for dummy rows (k/n
  summed over valid repeats) and `value` the rule's stat; UNKNOWN rows show
  the missing bound as the reason.

## Forever data proven live

None yet: no live run. The table's bounds are all null ("to be measured")
and the trial's `expect_today` is `?` on every rule, so any run scores
UNKNOWN until the server proves the numbers. The live check to run:

1. `forever-agent trials run --slots F --server <srv> --repeats 3 --out <dir> dps/dummy-mage-fire` on the test server (GM account, level 3 RBAC for `.npc add temp`).
2. `forever-agent trials report <dir>/dummy-mage-fire` (or `trials score <dir>`): expect three `duration` repeats, `mobs_ready` ok, and UNKNOWN rows naming `min_damage`/`max_reaction_s`.
3. Fill the table's nulls from the measured means, re-run, and confirm the card flips to PASS/FAIL on real numbers.

Gates: `make fmt-check`, `make cross`, `staticcheck ./...`, `vm-test ta ~/wt-fa-dummy` RESULT: PASS on a clean committed tree.

## What the scripted tests prove (hand-made combat rows, never captures)

- `TestDamageSumsPerSpell`: 30 + 50 over two Fireball goes scores mean 40 over 2 casts.
- `TestCastStartNeverCountsTwice`: one go with a start is one cast.
- `TestReactionIsStartMinusPreviousGo`: starts at 4 and 9 over goes at 2 and 5 score reaction 2/2; a missing `max_reaction_s` is UNKNOWN with the measured 4 s kept.
- `TestMissingNumberIsUnknownNeverFail`: null bounds score UNKNOWN, never FAIL.
- `TestBelowFloorIsFail`: a known floor missed is FAIL.
- `TestStrangerIsInvalid`: an unnamed spell (Blizzard) is INVALID with the evidence naming the stranger.
- `TestUncastTableSpellIsUnknown`: an expected but uncast spell is UNKNOWN (`casts`).
- `TestFailedSetupIsInvalid` / `TestUnreadableSetupIsAnError`: a failed setup is INVALID, an unreadable one an error.
- `TestDummySummonsAndFights` / `TestDummyTeardownKillsLedgered`: the repeat spawns the dummy entry, stamps `fight_start`, ends `duration`, and the teardown kills the 1 ledgered dummy.
- `TestDummyStrangerEndsRealPlayer` / `TestDummyStrangerArrivingMidFightEndsRealPlayer`: a stranger makes the arena not quiet (no summon next to them); one arriving mid-fight ends INVALID `real_player`.
