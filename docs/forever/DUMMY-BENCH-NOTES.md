# Dummy bench round (issue #18): spell and damage bench on a training dummy

`internal/trials/live` runs a new situation kind `dummy` on a live
`forever serve`: one quiet arena, one spawned target (a training dummy or a
passive high-health creature the teardown kills), the class bot's module
rotation for a fixed time. The combat log rows the agent already parses
carry the casts (spell_start/spell_go/spell_fail), the damage (`amount`),
and the spell data (cast time, GCD, cooldown) carries the readiness clock.
`internal/trials/dummybench` scores them against a small per-class table of
expected numbers; the report card shows per spell: damage, casts,
mean/max reaction.

The trial's `situation.rotation` list is the declared fight order: the
table loader checks every spell in it against the table (a rotation spell
the table lacks fails at load, so a typo cannot turn every repeat INVALID).
It does not drive the bot: the bot's own module rotation does, and the
table names the spells the fight may cast.

## Layout

| file | mirrors | what |
|---|---|---|
| `internal/trials/live/dummy.go`, `dummy2.go`, `dummy3.go` | `situations.go` (tank legs) | `dummyRun`: arena, quiet probe, hire, one `npc add temp` spawn claimed like the tank summon, the engage order (the scored window opens here), the fixed-time fight loop, engagement/read-back rows |
| `internal/trials/live/dummy_score.go` | `run.go` scoring seam | per-repeat `verdicts.json` plus per-trial `trial.json`: one row per trial rule (`{metric: dummy, spell, stat}`), majority of valid repeats, tie FAIL |
| `internal/trials/live/dummy_test.go`, `dummy_score_test.go` | `a4_review_test.go` (harness shape) | scripted dummy repeats: spawn + fight, stranger, teardown, engagement, lost spawn reply; the scoring path over repeat dirs on disk |
| `internal/trials/dummybench/*` | `events.go` + `score.go` shapes | `Tally` (window, cast pairing, damage attribution, readiness), `React` (able-to-act clock), `Score` (table rule), `TallyRep` (whole-repeat INVALID), `LoadExpects`/`ParseExpects` (typed refusals) |
| `internal/trials/dummybench/testdata/dummy_mage_fire.json` | `data/forever/*.json` (documented format) | the round's one table: fire mage spells + expects (all null = to be measured) |
| `internal/trials/dummybench/testdata/dummy-mage-fire.json` | `trialdefs/*` shape (kept beside the table, not embedded: the shipped `dps/` set pins its file list) | the round's one trial: dummy kind, three dummy rules |

## Decisions (binding)

- A setup row is ok only after a server read-back; unknown stays unknown;
  INVALID or SKIPPED never become PASS (the A4 bench rule, unchanged).
- **One INVALID voids the repeat.** A stranger (a cast spell the table does
  not name) or any guard reason makes the WHOLE repeat INVALID: every
  verdict row of that repeat is INVALID (the per-repeat card too), so no
  spell in it scores PASS or FAIL, and the trial rule for a spell in it is
  INVALID.
- **The expected table decides.** A mean damage below `min_damage` is FAIL,
  a mean reaction above `max_reaction_s` is FAIL, both met is PASS. A spell
  with no table row or a "to be measured" (null) bound is UNKNOWN. The
  trial's own `pass_if` is carried for the card (the trial schema wants it)
  and never overrides the table.
- **The window.** The fight window starts at the engage order, before the
  first cast (the dummy is proven up and the bot has the target), and ends
  at `fight_end`; damage up to `DamageGraceS` (2 s, `dummybench`) after a
  cast's go still belongs to that cast (the last projectile's travel). A
  cast whose go is inside the window counts with all its damage rows; a
  spell with damage but no in-window cast invents no cast. The window is
  read in the events' own time base (the normaliser re-bases on the
  fight_start wall): `lo = 0`, `hi = fight_end wall - fight_start wall`.
- **Reaction is able-to-act to cast start.** Readiness is the latest of the
  engage order, the previous cast's start plus the longer of its GCD and
  its cast time (an instant's GCD is thus excluded from the number, a
  long cast's GCD is spent inside it), and the cast's own cooldown end
  (its last start plus the spell's cooldown, and its category's last start
  plus the category cooldown; from `internal/spells` meta). It is never the
  previous cast's go alone. The engage order proves in-range/in-LoS
  readiness once (the attack order succeeded on the proven dummy); the log
  carries no per-cast position. A negative gap is clamped to 0. A spell id
  the spell table does not know has no bounded cooldown: its casts claim no
  reaction.
- **Only the bot's damage at the dummy counts.** Damage rows with
  `src_unit $bot` and `dst_unit` the dummy; heals, self-target rows and a
  pet's rows never add to a spell's damage.
- **A known reaction bound with no measured reaction is UNKNOWN**, never
  PASS on damage alone (in `Score` and in the trial rule).
- **Zero casts.** A table spell with no casts is UNKNOWN (expected but
  uncast). A spell with damage rows but no cast and no table row (a proc,
  a DoT tick) is ignored, not a stranger: it is not a rotation spell.
- **The table loader refuses what it cannot score**: NaN, Infinity,
  negatives, strings, bools and a duplicate spell are a `*dummybench.TableError`.
  Names are trimmed and folded for matching; the row shows the trimmed name.
- **`situation.expects_file` stays inside the data root**: an absolute
  path, a volume name or a `..` escape is refused. The default table is
  `dummy_<class>_<spec>.json` in the bench's data root: the trial file's
  own directory on disk (no working-directory assumption), or `dummyTableDir`
  when a caller sets one.
- **Every spawn is ledgered on every error path.** A `.npc add temp` whose
  reply is lost (a disconnect, a refused reply) may have run: the spawn is
  counted unidentified and every fresh unit of the entry is ledgered at
  once, so teardown kills it. An unreadable pre-spawn scan claims nothing
  (freshness is unknown) and the spawn is counted instead.
- **Engagement.** The fight is engaged by the bot's own row at the dummy (a
  cast, swing, spell damage or periodic tick from the bot to the dummy), or
  by the dummy's `in_combat`; a passive dummy that never turns InCombat is
  engaged by the bot's first cast. A cast of the dummy at the bot proves
  nothing.
- **A cancelled cast's start is dropped**: a `spell_fail` after a
  `spell_start` and before the go means the next go is an instant (its own
  time), never paired with the failed cast's start.
- The dummy never fights back, so `mobs_ready` proves alive + entry only
  (no combat requirement, unlike the tank's row where combat is the hostile
  proof).
- A dead or vanished dummy ends the repeat `dummy_down` (INVALID, never
  scored): a bench that scores a dead dummy's leftovers would claim damage
  the rotation did not deal. The teardown kills the ledgered dummy through
  `gm_kill` and the heal refill, like every tank repeat.
- The arena lock is held by the `LiveSituation` until `Finish`, and a real
  player in reach ends the repeat INVALID `real_player` (checked at most
  every 5 s), same as the tank kind.
- The card's `metric k/n` column carries casts/casts for dummy rows (k/n
  summed over valid repeats) and `value` the rule's stat; UNKNOWN rows show
  the missing bound as the reason, INVALID rows the stranger.

## Forever data proven live

None yet: no live run. The table's bounds are all null ("to be measured")
and the trial's `expect_today` is `?` on every rule, so any run scores
UNKNOWN until the server proves the numbers. The live check to run:

1. `forever-agent trials run --slots F --server <srv> --repeats 3 --out <dir> internal/trials/dummybench/testdata/dummy-mage-fire.json` on the test server (GM account, level 3 RBAC for `.npc add temp`; `findTrial` reads a trial file from disk, so the non-embedded path runs, and its table is the sibling `dummy_mage_fire.json`).
2. `forever-agent trials report <dir>/dummy-mage-fire` (or `trials score <dir>`): expect three `duration` repeats, `mobs_ready` ok, and UNKNOWN rows naming `min_damage`/`max_reaction_s`.
3. Fill the table's nulls from the measured means, re-run, and confirm the card flips to PASS/FAIL on the table's numbers (not on `pass_if`).

Gates (a clean committed tree, sha c6aed74e): `make fmt-check` clean, `make cross` (windows/amd64, darwin/arm64 and the windows vet) clean, `staticcheck ./...` clean, and

    RESULT: PASS kind=ta sha=c6aed74e dirty=0

## What the scripted tests prove (hand-made combat rows, never captures)

`internal/trials/dummybench` (through `ScoreRep`, the card's path):

- `TestDamageSumsPerSpell`: 30 + 50 over two Fireball goes scores mean 40 over 2 casts; the table floor passes it.
- `TestCastStartNeverCountsTwice`: one go with a start is one cast.
- `TestTableBoundsDecide`: a mean under the table floor is FAIL and a mean over the reaction ceiling is FAIL, whatever `pass_if` says.
- `TestReactionExcludesGCDAndCooldown`: Fireball start 1 (1.5 s GCD spent in the cast), Fire Blast at 3 is 0.5 s of reaction (not 1 s from the go), and the second Fire Blast at its 8 s cooldown end is 0 s (not 8 s); the first cast is measured from the engage order.
- `TestHealsAndForeignRowsDoNotCount`: self-heals, heals at the dummy and the pet's damage never add to the spell.
- `TestWindowCountsTheLastCastsDamage`: a cast at 0.2 s is in the window, its damage 1.5 s past fight_end counts (grace), a tick past the grace does not.
- `TestDamageOnlySpellIsIgnored`: an uncast proc (Ignite) is not a tally and not a stranger.
- `TestStrangerVoidsEveryRow`: Blizzard beside Fireball makes BOTH rows INVALID.
- `TestCancelledCastStartIsDropped`: a `cast_fail` drops the start; the go is measured from the engage order.
- `TestUnboundedReactionIsUnknown`: a spell the spell table does not know claims no reaction; a known ceiling scores UNKNOWN.
- `TestScoreNeedsAReaction`: `Score` refuses PASS for a known ceiling with no reaction.
- `TestMissingNumberIsUnknownNeverFail`, `TestUncastTableSpellIsUnknown`: a null bound or an uncast table spell is UNKNOWN.
- `TestTableLoaderRefusesHostileValues`: NaN, Infinity, -1, "5", true and a duplicate spell are a `*TableError`; zero, an integer, a float and null load.
- `TestWindowUsesTheMarksWalls`: with live marks walls the events' `t=2` cast is inside the window (the marks' own `t` would have dropped it).
- `TestFailedSetupIsInvalid` / `TestUnreadableSetupIsAnError`: a failed setup is INVALID, an unreadable one an error.

`internal/trials/live` (the trial path over repeat dirs on disk):

- `TestDummyTrialTableBoundsDecide`: the trial row is FAIL on the table's floor and ceiling although `pass_if` is `>= 0`.
- `TestDummyTrialStrangerVoidsEveryRule`: every trial rule and every per-repeat card row of a stranger repeat is INVALID and names the stranger.
- `TestDummyRuleNoReactionWithCeilingIsUnknown`: the rule path scores UNKNOWN with no measured reaction and a known ceiling.
- `TestDummyRuleCastsStatIsUnknown` / `TestDummyRuleInvalidRepeatIsInvalid`: the table has no casts column; an INVALID repeat voids every rule.
- `TestDummyExpectsRefusesPathEscape` / `TestDummyExpectsDefaultBesideTheTrial`: `..`, absolute and Windows paths are refused; the default table is the trial's sibling, wherever the binary runs.
- `TestDummyShippedTrialLoads` / `TestDummyRotationMustBeInTheTable`: the shipped trial and table load, every rule's spell is in the table with the normaliser's spelling, and a declared rotation spell the table lacks fails at load.
- `TestDummySummonsAndFights` / `TestDummyTeardownKillsLedgered`: the repeat spawns the dummy entry, stamps `fight_start`, ends `duration`, and the teardown kills the 1 ledgered dummy.
- `TestDummyEngagedOnlyByBotAtDummy`: the dummy's own cast never engages; the bot's cast at the dummy does.
- `TestDummyFightStartsBeforeTheFirstCast`: a passive dummy (never InCombat) is engaged by the bot's row, and `fight_start` precedes the engaging cast.
- `TestDummyLostSpawnReplyIsLedgered`: a lost `.npc add temp` reply still ledgers the spawned unit and counts it unidentified.
- `TestDummyStrangerEndsRealPlayer` / `TestDummyStrangerArrivingMidFightEndsRealPlayer`: a stranger makes the arena not quiet (no summon next to them); one arriving mid-fight ends INVALID `real_player`.
