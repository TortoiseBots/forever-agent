# Round B2: the bug hunter's core and the frame rules

`internal/bughunt` is the Go home of the bug hunt: it loads a recorded run into one model, cuts episodes the way
the Python checker does, and reports hits with the same fields. This round ships the core, the three loaders and
the rules that need only the frames: 3, 5, 6, 9, 10, 13 and 14. The rules that read the combat log (1, 2, 4, 8,
11, 15) and the tickets wait for B3, which builds on this package.

## The model

One `Run` holds a watch or a trials repeat: frames of members and hostiles, the normalised events, deaths, the
combat-log rows, per-member roles and classes, and the subject set the rules apply to. Two loaders fill it:

- **`LoadWatch`** reads a watch folder: the trace (`<run_id>.jsonl`, header, party and mobs rows, frames), the
  summary (`<run_id>.forever-run.json`, for the instance report's bosses) and the combat log
  (`<run_id>.combatlog.jsonl`). A folder with several runs takes the last run id, the newest. The summary and the
  combat log are optional. Members are keyed by the trace's one guid text (`0x` and 32 upper-case hex digits), the
  same text a combat row carries; a frame row with no guid is not a member. A death is dated at the first frame
  that shows a member not alive after one that showed it alive.
- **`LoadTrials`** is the port of the Python `load_trials`: `events.jsonl`, `samples.jsonl` and `setup.json`, the
  same identity keys (the character name from the event's guid map, else the unit word), the same role/class words
  from the trial directory's name and the setup checks, the same subject set (the bot and `extra1..3`, never the
  player), the same frame fields and the same rage/energy power-type quirk of the samples. `events.jsonl` and
  `samples.jsonl` are required: a missing, unreadable or corrupt one is an error naming the file and the line, never
  an empty run that reads as clean, and `samples.jsonl` must hold a row. Every sample row is schema-checked too: a row
  that lacks the fields the checker reads (a numeric `t`, a recognised `unit`, a `guid`, and the unit kind's health,
  power and position numbers) or carries them with the wrong type is an error naming the file and the first such line,
  and a file with no usable frame is an error; one bad row fails the load. `setup.json` is optional. The samples are
  ordered by time on load (a stable sort) and an exactly repeated row is kept once; the run's clock is the earliest
  sample's wall time.
- **`LoadRoster`** reads a roster record (`dash-bots --watch --record`: one `{"at", "bots"}` object per line, the
  last winning, or one object, or an array of bots). `Run.UseRoster` is decision 4's fallback: it fills a role or
  class the frames do not carry and, for a recording with no party rows at all, makes the roster's members the
  subjects.

`Episodes` is the Python's `episodes()`: a run of consecutive frames where a predicate holds for a member, longer
than the threshold (or at least it), ended by a failing frame, a missing member or a frame gap over `MAX_GAP_S`
(16 s). A one-frame recording is an episode of duration 0, as in Python. `Hit` carries the Python's fields and
JSON keys (`rule ds run t clock dur bot cls role ev key deaths sparse cost note`); `key` is the Python tuple as a
string slice, and it is what B3 hashes into a ticket id. The deaths of that member from one second before the
episode to fifteen seconds after it raise the cost by `DEATH_COST_S` each, and the clock is the wall time when the
run knows its start, else the run-relative time.

All thresholds in `const.go` keep the Python's names and values (`R3_MIN_S`, `R5_STUCK_YD`, `R6_NEAR_COMBAT_YD`,
`R9_FALL_DROP_YD`, `R10_MIN_S`, `R13_MANA_PCT`, `R14_DIST_YD`, ...). The few numbers the Python leaves as literals
inside a function (rule 9's four-second window, the half second after the death) are named here and say so.

## The rules

| Rule | Forever shape |
|---|---|
| 3 add on a non-tank | the mob's threat victim (`.tb threat`), a member that is not the tank, for >5 s while a tank is alive and the group is in combat; a boss the instance report listed is never an add |
| 5 stuck | two keys: **stuck follow** (on `mvm=follow`, 40 yd or more from the leader, own path under 1 yd for >30 s) and the module's own **`stuck=blocked:<reason>`** / **`stuck=away`** |
| 6 eat/drink in combat | the module's `eat`/`drink` flags while the group has been in combat 3 s+ and an in-combat hostile is within 60 yd |
| 9 fall/drown death | a death after a height drop of 12 yd+ in the 4 s before it, with no damage source to name the killer |
| 10 power waste | a healer's mana (or a tank's rage) at 99 %+ for >30 s in combat while a member is under 80 % health |
| 13 healer not drinking | a healer under 30 % mana out of combat for >20 s, no drink or food within 15 s, the group standing still; needs the module's meal report |
| 14 stray member | more than 40 yd from the other members' centroid for >20 s in combat, with 3+ others to judge it |

Rule 5's rework follows the frames: the leader distance is the module's own `dl`, so the centroid and the net-move
test the old walk rule needed are gone. A meal the bot took, a `stay`, its own combat, and a `stay` within ten
seconds before the gap opened all end or hold the episode, as v6 has them. The stuck-follow test is the bot's own
path over the episode under one yard: a bot walking at the group's speed is catching up however far back it is.
Two stuck episodes of one bot less than a minute apart are one pocket (the longer kept), as v6 does.

The module's stuck word is its own key because it is the bot's own report, not an inference from movement:
`("5", "blocked", <reason>)`, `("5", "away")`. The seconds in `away:<seconds>` are deliberately not part of the
key (they tick). `noengine` is not a rule-5 finding: that bot has no engine, which is a setup failure.

Rules 6 and 13 read the module's meal report. A trial sample carries neither the flags nor the action, so none of
its units has the report (`Unit.HasMeal`, `Run.ReportsMeals`): rule 6 has nothing to hit there, and rule 13 is a
skipped check, never a "no drink" fact. An unknown meal anywhere in the drink pad keeps rule 13 silent too.

The Onyxia whelp special cases (fly-in distance, the melee crew) are not ported: they corrected a mob's model
target in the old walk frames, and `vic` is a threat victim, so a whelp still flying in has no victim to be an add
on.

## Tests

- `TestRule3AddOnHealer`: the round's first failing test. Before the rule, a hostile on the healer for 25 s reported
  nothing; now it is one hit on the healer's guid with key `[3 heal 1234]`. Near misses: the tank as victim, a
  five-second fight, a boss entry, a non-member victim, no tank alive.
- One hit and one near miss per rule: 5 (still vs walking, in combat; the module's two words; `noengine`), 6 (a
  close vs a far hostile, group out of combat), 9 (a fall vs flat ground and a corpse above the taken-off height),
  10 (full mana with a hurt member vs nobody hurt, half mana), 13 (still group vs walking, a drink in the pad), 14
  (stray vs 20 yd, two others).
- The v6 pins, rebuilt on Forever frames: `TestT5OwnCombatNoHit` (a bot in combat while the group walks away),
  `TestT5MealStayNoHit` (a meal taken, a stay held while the lead pulled), `TestT5CatchupNoHit` (following at the
  group's speed 100 yd back), `TestT5StuckFollowKind` (still on follow 100 yd back for a minute). Each was checked
  against a deliberately weakened rule (rule 3 disabled, the meal break, the stay-back window, the own-path test,
  the own-combat break each removed in turn): every pin failed, so each guards the behaviour it names.
- `TestLoadWatchRecording` (trace, party rows, registry, summary bosses, combat log, a death, the clock),
  `TestLoadWatchOneFile`, `TestLoadRosterFallback` (class ids as names, the fallback not overwriting a frame's
  word), `TestLoadTrialsRepeat` on `testdata/live1` (the live1 rows as a repeat: name, roles from the trial name
  and the setup checks, the player as a unit but not a subject, five frames, the mob's entry and health percent, the
  cast times, the clock), `TestEpisodesBreakOnGap`.

## What to check when the first real watch is hunted

1. The threat victim (`vic`) is only as good as the module's threat lines: a mob whose threat list is empty has no
   add finding even while it hits someone. That is the better input the plan counted on, and it is also the input's
   limit.
2. Rule 6 needs an in-combat hostile with a position. A watch whose threat reads failed for a stretch has no
   positions for that stretch, so a meal then is silent rather than a hit.
3. Rule 9's death time is the first not-alive frame, 2 s after the fact at the default frame interval; a fall's
   height drop is measured inside the 4 s window, as before.
4. The summary must be the one beside the trace (`<run_id>.forever-run.json`) for the boss list. A recording copied
   without it loses the boss exclusion and may report a boss as an add.

## Review fixes (2026-11-20)

The adversarial review of the round's commit found three defects. Each had a test that failed on the code before
the fix.

1. **[high] Rule 13 called unavailable trials meals "not drinking"** (fix). A trials sample has no eat/drink field,
   so every unit read as "did not eat or drink" and a low-mana healer in a still group became a false hit. Meal
   telemetry is now explicit: `Unit.HasMeal` says a row carried the module's report and `Run.ReportsMeals` says the
   run carries it at all. Rule 13 returns no hits without it, and a pad frame without it keeps the hit silent;
   unknown is not a negative fact. The report counts rule 13 as skipped on such a run.
   Tests: `TestRule13SilentOnTrials` (a loaded trials repeat with a 20 %-mana healer stationary for 30 s: one hit
   before, none now) and `TestRule13UnknownMealInThePad` (the same frames, only the pad frame's report removed).
2. **[high] A missing or unreadable required trials file was a successful empty run** (fix). `LoadTrials` opens
   `events.jsonl` and `samples.jsonl` with `readTrialsJSONL`: an open, read or scanner failure is an error naming
   the file, a line that is not a JSON object is an error naming the line (only a cut-short last line, a killed
   writer's, is dropped), and an empty samples file is an error. `setup.json` stays optional. An empty
   `events.jsonl` is a legitimate repeat without events.
   Test: `TestLoadTrialsRequiredFiles` (missing, unreadable as a directory, corrupt, oversized line, corrupt middle
   line, cut-short-only, empty samples; and the two optional-file cases).
3. **[medium] Out-of-order sample timestamps broke the windows** (fix). `orderSamples` sorts the samples by time with
   a stable sort (rows of one time keep their file order) and keeps an exactly repeated row once, so the frames,
   `framesBetween`'s binary search, episode durations and the run's clock all move forward in time.
   Test: `TestLoadTrialsOrdersSamples` (samples written 20, 0, 10 and one mob row twice; frames 0, 10, 20, one mob,
   the clock from the earliest sample, and a 0..20 episode).
4. **[high] Schema-invalid samples still became a successful empty run** (fix). `readTrialsJSONL` returned every
   JSON object and `LoadTrials` only required one of them, so a `samples.jsonl` holding `{}` loaded as a t=0 frame
   with no unit identity, and absent or non-numeric `t`, health, power or coordinates were silently coerced to zero by
   `rowNumOK`, collapsing times or hiding units. The Python loader reads those fields by key, so its absence is a
   failed load there. Each row now passes `checkTrialSample` before anything is built from it: a numeric `t`, a
   recognised unit (`player`, `bot`, `extra1..3` or `mob:<guid>`), a non-empty `guid`, and numeric `hp`, `max_hp`,
   `x`, `y`, `z` (plus `power` and `max_power` for members) are required, one bad row fails the load, and the error
   names the file and the first bad line. A file with no usable frame is an error as well.
   Test: `TestLoadTrialsSampleSchema` (`{}`, missing or text `t`, an unknown unit, a missing guid, a missing power, a
   text max_hp, a mob missing max_hp or a coordinate; the mob-needs-no-power and cut-short-last-line positives), plus
   400 recorded repeats from the audit tree loading unchanged.
