---
type: Command
title: forever dungeon-play
description: Play one dungeon plan as a real character leading four bots, and read the run's record and its pass or fail verdict.
tags: [dungeons, play, player, command]
status: stable
generated: { by: forever-agent docs, at: 2026-11-25T19:45:00Z }
sources:
  - id: play-cmd
    resource: cmd/forever-agent/forever_play.go
  - id: play-core
    resource: internal/forever/driver/play.go
  - id: play-death
    resource: internal/forever/driver/play_death.go
  - id: play-regroup
    resource: internal/forever/driver/play_regroup.go
  - id: play-fight
    resource: internal/forever/driver/play_fight.go
  - id: play-record
    resource: internal/forever/driver/record.go
---

> **Not yet proven on a live server.** The run machine, its walker and its fight logic are exercised against the scripted (fake) world; no live dungeon run of `forever dungeon-play` has been made yet, so its verdict covers the client side only. See [what has been proven](../getting-started/what-is-forever-agent.md#what-has-been-proven-and-what-has-not).

# Synopsis

```bash
forever-agent forever dungeon-play --plan FILE --bots TANK,HEALER,DPS,DPS \
    [--rest host:port --rpc host:port --ca FILE --account EMAIL] --character NAME \
    [--max-minutes N] [--max-wipes N] [--last-leg NAME] [--disband] [--out DIR] [--map] \
    [--min-active SEC] [--navmesh DIR] [--dry] [--no-preflight | --preflight-only] \
    [--serve-dir DIR] [--export FILE] [--soap-user NAME --soap-password-file FILE]

forever-agent forever dungeon-play --plan FILE --tanks NAMES --healers NAMES --dps NAMES \
    [--layout FILE] [--rest host:port --rpc host:port --ca FILE --account EMAIL] --character NAME \
    [--max-minutes N] [--max-wipes N] [--last-leg NAME] [--disband] [--out DIR] [--map] \
    [--min-active SEC] [--navmesh DIR] [--dry] [--no-preflight | --preflight-only] \
    [--serve-dir DIR] [--export FILE] [--soap-user NAME --soap-password-file FILE]
```

`dungeon-play` is the player mode: the agent's own character is party leader and fights as one of the group, and
the owner's four bots follow it (tank, healer and two damage dealers, in that order). Every line it sends is a bot
module line or one of the two whisper words; it never travels by GM, and the run's record proves that (a line
starting with `.` that is not a `.bot` line fails the milestone).

With `--tanks`, `--healers` and `--dps` (and optionally `--layout`) the same run leads a **raid** instead of a
five-member party, from a raid plan; see [a player-led raid](#a-player-led-raid).

The run is also judged: a spell judge reads the run's recording and checks that each bot cast what its class and
role should, and the milestone's rule turns the run and the judge's findings into a single PASS or FAIL.

# What it does

| State | Work |
|---|---|
| `PREPARE` | Reads its own character: alive, on the outer map and within 300 yd of the plan's portal, not already inside the instance. If a group is already formed, this character must lead it and be its only member that is not one of the named bots. |
| `FORM` | Adds the four bots in order and waits for each to come online, then sets the loot method (group loot, uncommon threshold) and reads the tank's max health. A module refusal stops the run by its code. A raid group is converted after its first bot is online and laid out after its last add ([below](#a-player-led-raid)). |
| `TRAVEL` | Walks to the plan's portal and waits until every bot stands within 30 yd (a straggler gets a follow order at 20 s; the run stops at 60 s). |
| `ENTER` | Takes the entrance trigger, waits for the group on the plan's map, and summons any bot still outside (a summon on cooldown is waited out once). |
| `LEG_WALK` | Walks each leg in segments of at most 15 yd, staying out of the plan's avoid boxes, holding for a live patrol on the way (20 yd, 120 s, with a stillness test so a static hostile is not waited for forever). Before it leaves the stand a fight ended on, and between every segment, it checks the party ([below](#the-party-gate)). |
| `PULL` / `FIGHT` | Marks the skull, orders the attack, attacks itself 1.5 s after the tank has the skull, re-orders the tank if the skull breaks to a non-tank, and ends the fight when no living unit on a party member remains. |
| `REST` / `LOOT` / `STEPS` | Rests between packs, loots the kills, and runs the plan's steps between the packs and the leg's boss. |
| `DEAD_WAIT` / `WIPE` | A dead character waits for a resurrect offer or a healer's raise; a wipe is recovered (release, ghost run to the portal, the entrance's raise) within 10 minutes. |
| `BOSS` | Gathers the party within the pull distance, rests to the bars, marks the boss, kills its `first` entries and then the boss. A player-led raid stops here instead (`raid-boss-reached`). |
| `DONE` / `LEAVE` | Checks that every boss up to `--last-leg` is dead, disbands if asked, logs the bots out. |

The plan's crowd-control lists (`cc_priority`, `cc_always`, `no_cc`) and boss `tactics` are **not** run by player
runs; the run says so once in its notes instead of dropping them silently.

A plan boss killed as an add in another fight is recorded killed when a fight's view shows it dead, so the run neither
pulls it again nor fails `bosses-alive` on it.

## The party gate

The leader does not walk on without its party. Before it leaves the stand a fight ended on, between every 15 yd
segment of a walk, **and at a pull's gather** (the stand it orders a pull from), it checks the party:

| What it sees | What it does |
|---|---|
| A living bot further than **60 yd** (`PlayRegroupYd`) | Stands and waits **20 s** (`PlayRegroupWait`); then sends that bot one `.bot follow`; waits **20 s** more (`PlayRegroupFollowWait`); then walks **15 yd** (`PlayRegroupBackYd`) toward the party, up to **2** times (`PlayRegroupTries`) and **60 s** in all (`PlayRegroupBackTime`). Every step is a `regroup_hold`, `regroup_follow` or `regroup_back` event in the trace. |
| The walk back | The walk's own rules: a navmesh route cut into 15 yd segments, out of the plan's avoid boxes (`regroup_refused` ends the gate, because that ground kills), clear of every uncleared pack's radius and of every hostile within the patrol's 20 yd that the plan does not name (a point nearer to it than the leader already stands, less `PlayRegroupClearYd` 0.5 yd, refuses the step: `regroup_blocked`, and the ladder below carries the party). The step runs under the **earlier** of the gate's 60 s and the run's own remaining time (`--max-minutes`), so a blocked movement is cancelled at that bound and never waits the run out. |
| The party is fighting | The leader joins the fight (`FIGHT`) at once instead of standing beside it: the hostiles on the party become the fight's set (the one on the tank, else the nearest, its skull) and the normal fight runs (`fight_join`, which names the plan `group` that owns the skull's unit, `-1` for none). The gate's waits and walk backs are not taken while the group fights. A combat flag with no hostile on the party is no fight to join: nothing is entered with an empty set, and the ladder carries the gate. |
| The party fights another pack than the one being pulled | Only the pack that was fought is marked pulled: the joined fight's pack is the plan group that owns the skull's unit (none when no group owns it), so the pull in hand is not marked fought and the run pulls it after the fight. The fight writes its own pull record for that group (`pull_start` with `joined: true`, and the summary's pull row carries `joined`); a fight no plan group owns writes none. The record of the pull it replaces is closed (`pull_end` with `aborted: joined`). |
| The party came back | Walks on (`regroup_join`); the next split starts a fresh gate. |
| Still behind after those attempts | The walk's own straggler ladder is the last arm: a follow at 30 s, a summon at 90 s, and the `straggler` stop at 150 s, which names every bot still behind and its distance. `regroup_timeout` records a gate whose walk back ran out of time, `regroup_blocked` the step a pack or a patrol refused or that the navmesh has no complete route for (the ladder carries it, the run does not stop on a walk error), and `regroup_giveup` the one whose attempts were spent. |
| The character dead | The gate's waits stop the run `not-alive`: no wait or walk can put the leader back. This applies everywhere `waitNear` waits, the pull's and the boss's gathers included. |
| A member dead, ghost or missing | Read on **2** consecutive polls (`PlayRegroupDownPolls`, at different instants; the gate waits one poll to read it again), so a roster row that drops out for one poll is no member down and costs no regroup. Then the run's own regroup (`WIPE`) before any travel: out of combat the healer raises the member (waited `BotRez`, 120 s), and one still down gets the release and corpse-run whispers; the party is then waited for. The death is counted in the summary and written as a `death` event, wherever it happened: the walks and the waits count deaths too, not only a fight. After the regroup's rest the run goes back to what the death interrupted: the portal wait (`TRAVEL`) while it is still outside the instance, the leg walk inside it (which re-enters the pull or the boss it was gathering for). The walk's own place never moves: a leg resumes at the same via point. |
| A member down while the pull already has the party | The fight is never overridden: the damage dealers keep their orders, the pack is fought, the pull's record is closed (`pull_end`), and only then does the regroup run (`regroup` after it). A member down after the pull's orders with nothing on the party yet is the regroup's instead, and the pull's record is closed first (`pull_end` with `aborted: regroup`, no row in the summary); the pull re-enters with its own record. The abort also takes the pull's orders back before the recovery: every bot gets one `.bot follow` and the skull and cross marks are cleared (`raid_mark_clear`), so a tank still on its attack order cannot drag the pack into the recovery. A run that stops inside a pull closes the record the same way (`aborted: stop`). |
| The same place costing regroups | `PlayRegroupLimit` (**2**) regroups of one place, then the run stops `regroup-limit:<key>`, naming it: `group-<id>` for a pack, `boss-<entry>` for a leg's boss, `stranger-<entry>` for a patrol candidate, and outside a pull `walk-<leg>-<via>` (the leg's index and the via point the walk is at, the leg's end counted as the last), `step-<leg>-<step>` for a leg's step, `travel` outside the instance. Every `regroup_down` event carries the same `key`. A stand that keeps killing the party is ground the plan does not mark, and re-entering that pull forever is not a run that can finish. |

A pull's row in the record is opened once, when the party is gathered and the marks go out: a gather a regroup
interrupts leaves no `pull_start` behind it, and every `pull_start` has exactly one `pull_end` (a pull a regroup or a
stop took the run out of before a fight ends with `aborted` and has no row in the summary). After the gate walked the
leader back inside the gather, it returns to the pull's stand before it marks and orders.

An early exit after the `pull_start` closes the record where it happens, so the next pull does not close it later
with the wrong end time: a pack the bots finish during the refusals is a pull that happened (`pull_end` with no
`aborted`, and its row in the summary's `pulls`), while a boss gone from the view and a patrol candidate given up or
left evading end with `aborted: boss-gone`, `aborted: stranger`, `aborted: stranger-gone` or `aborted: evade`. A
record still open when the next pull starts (a path that could not close it) is closed as `aborted: superseded`.

The order and its time are in the trace as an `order` event (`{"bot":"dps2","verb":"follow"}`), and every frame
carries the leader's own position (`self`) beside the bots', so a party that splits can be read from the record
afterwards. A bot's `cb` in a frame is the run's own fight **or** a living hostile in view with that bot as its
target — a party left behind and fighting reads as in combat — so a bug-hunt's "idle in combat" rule may see a
regroup's or a wait's idling bots as fighting; read those hits beside the `regroup_*` events.


# A player-led raid

`--tanks`, `--healers` and `--dps` name a raid group (comma lists) and make the same player run lead a raid instead of
the four bots; `--layout FILE` puts the members in their subgroups before the run starts. They exclude `--bots`, and a
plan loaded this way must be a raid plan (kind `raid` with its `raid` block): `--bots` keeps refusing a raid plan, so
the five-member path is unchanged.

| Flag | Default | Meaning |
|---|---|---|
| `--tanks` | required for a raid | The bots that tank, comma separated. The **first** one is the pull tank: every order that names the party's tank goes to it, and the off-tanks fight with the damage dealers. |
| `--healers` | required for a raid | The bots that heal. Any living one may raise a dead member; the first one is the rest gate's healer (it must use mana). |
| `--dps` | required for a raid | The damage dealers, at least 2. Every one of them (and every off-tank) is ordered onto the skull. |
| `--layout` | none | A layout file: one bot per line, its name and the subgroup 1-8 it is moved to with a raid move ([raid-suite](../turtle/raids/raid-suite-reference.md) reads the same format). A move into a subgroup that already holds five trades places with a member it holds (the first in roster order the layout does not place there); a failed or unsatisfiable layout stops the run `raid-move`. |

A group is refused before anything is sent when it has no tank, no healer, fewer than 2 damage dealers, the same name
twice, or a size outside **5** to **39** bots (`PlayRaidMinBots`, `PlayRaidMaxBots` = the core's raid of 40 less this
character); a plan whose `raid.size` is known caps it there (`size - 1`). It is refused with a usage error. The plan's
own `raid` role split (`tanks`, `healers`, `dps`) is a placeholder for the raid it was written for, so a split that
differs from it is not refused: it is recorded once as a note (a `note` event in the trace and a line in the summary's
`notes`). A layout is refused before anything is sent when it names a bot the group does not hold, names one member for
two subgroups, or names more than five members into one subgroup.

`FORM` for a raid converts the group first and adds after:

1. The first bot (the first tank) is added and comes online.
2. The group is converted to a raid (`raid_convert` in the trace). The core refuses a sixth member of a party, so this
   comes before the second add; a failed convert stops the run `raid-convert`, and a group port with no raid support
   stops it `raid-group-unsupported`.
3. The rest of the raid is added, in role order: the tanks, then the healers, then the damage dealers.
4. The `--layout` is applied (a `raid_move` event per move, with the member's slot and its subgroup). A move into a
   subgroup with room is sent first, so a member leaving a full subgroup frees its slot; a move whose target subgroup
   already holds five is a swap with a member of it that the layout does not place there (`CMSG_SWAP_SUB_GROUPS`, the
   event carries `swap_with`). Only the members the layout names count against a subgroup's five: a member the layout
   does not name may be swapped into the mover's old subgroup (the first such member of the target, in roster order),
   so a layout of one line into a full subgroup is carried out. At most one move or swap is sent per named member. A
   layout that cannot be satisfied — more than five members named into one subgroup, a member that is not in the
   party, one member named for two subgroups — is refused by `dungeon-play` before anything is sent, and by the run
   (which sends nothing and stops it `raid-move`, with the reason in a `raid_layout_refused` event) by the same rule.

Everything else is the player run it already was, over the raid's roster: the party gate reads every member, the
straggler ladder and regroups follow one bot at a time, rest reads the whole group's bars, and the trace names each
member by its role and number instead of a character (`tank1`, `tank2`, `heal1`, `dps12`).

The run stops **at a boss**: the boss fight belongs to the module's raid walk, not to the player's group, so reaching a
leg's living boss records `raid_boss_reached` and stops the run `raid-boss-reached`. A boss that aggroes on the party by
itself is fought through the joined fight like any hostile on it, and the run stops `raid-boss-reached` as soon as that
fight ends (its `raid_boss_reached` event carries `after_fight: true`); it never walks on to the boss's own pull. A
run whose last leg has no boss (Onyxia's tunnel) ends at `DONE` as a clear. A player-led raid's verdict is PASS when the run stopped at a boss
(`raid-boss-reached`) or cleared, made at most **2** wipes, and left no GM line. The spell judge still prints its
findings, but a raid's verdict does not read them (its size is not the judge's four slots).

The dry run needs no network and no server:

```bash
forever-agent forever dungeon-play --plan dungeons/onyxias_lair.json \
    --tanks TANK1,TANK2,TANK3,TANK4 --healers HEAL1,HEAL2 --dps DPS1,DPS2,DPS3 \
    --character PLAYER --dry --out ./runs/raid-dry
```

# The leading player's own pull

The pull is the tank's first: the agent puts the skull on the pack's kill-order entry and the cross on the next unit,
selects the skull and orders the tank onto it. The tank's answer is the module's own: it refuses a mark its own
line-of-sight test cannot see **from the bot's position**, which the character's navmesh ray is not (live 2026-11-24:
the ray clear at 30.5 yd, the refusal right after). So the run asks the server's own sight before it orders, through
the module's read-only `.tb los` query — the one line of the player run's command channel that is not a `.bot` verb:

- The **skull** goes on the first member of the kill order the TANK sees. Two queries are never sent closer than
  250 ms (`PlayLosQueryEvery`), under the module's five a second; a rate refusal is waited out once and that one query
  retried.
- The answer is the module's own words: a unit it found is echoed by its **name** (spaces and apostrophes
  percent-encoded), even when the query named it by guid; a ground spot comes back with **two decimals**, so the echo
  is matched within a hundredth of a yard per axis (`sightcmd.SpotToleranceYd`); a refusal carries `los=0` and no
  distance. A unit the module cannot find (it left the view) is a NO the pick **skips**: the next member is asked.
  A malformed line, another query's answer and an access refusal are unknown and keep the older flow.
- A tank that **sees none** is walked into sight of the first: the navmesh path from the tank toward the unit is
  sampled every 2 yd (`PlaySrvSightStepYd`), never nearer than 5 yd to the unit, never more than 30 yd past the pull's
  stand (`PlaySightPastStandYd` — the plan's for a pack, where the party gathered for a boss and a stranger), and cut
  where the path first enters **another pack's clearance** (`PlayAgentApproachPackYd`, the fallback approach's own
  rule: the pull's own units, the party's pets and critters are not counted; the blocker is recorded, see below). Each
  sample is asked as a spot on the ground with the eye at 2 yd above it (`PlayLosEyeYd`); the agent walks to the first
  sample **ahead of its own stand** (the sample nearest it, never one behind it toward the tank) that the server
  clears, and then steps forward along the path (`PlayAgentLeadYd` a step, the path's last sample ahead when less is
  left) until the tank itself sees the unit — neither step ever goes back toward the tank, which is what run 4 did for
  every try. After every step the search **waits for the tank to close up** (`PlayTankWait`, 15 s at most) before it
  reads the sight again, because a live follow reaches its spot a poll or two behind the leader; the step that reaches
  the path's end is waited for and looked at like every other, and a search with no step left does not wait twice on
  the same ground.
- **One sight budget a pull**: at most 24 queries (`PlayTankSightSpots`) and 60 s (`PlayTankSightTime`) from the
  first look cover the skull pick, the stand walk and the search after **every** tank refusal together, and the
  refusal path's own 90 s (`PlayRefuseTime` from the first refusal) caps what is left; the waits for the tank spend
  that same time. Spent, the answer is **unknown** and the pull keeps the older flow below (the module is the judge),
  never "the server sees from no point". The lead steps are bounded by 8 tries (`PlayTankSightTries`).
- The search is recorded as `pull_tank_sight` (`group`, `target`, `seen`, `yd`) for the pick, `pull_sight_stand`
  (`group`, `target`, `seen`, `known`, `tries`, `x`, `y`, `z`) for the walk, and `pull_sight_cut` (`group`, `target`,
  `blocker` with its `guid`, `entry` and `yd` from the unit) when another pack's clearance ended it.

When it whispers that it cannot see the marked skull anyway, the same search runs again from the refusal (never the
older step, which was measured from the tank and so never moved it: run 4's tank stood at one point for the whole 60 s),
spending the same one budget. The refusal's own bounds stay 4 steps (`PlayRefuseTries`) and 90 s from the first
refusal (`PlayRefuseTime`); a skull the server sees from no point of the path takes the fallback pull at once. An
answer the module cannot give — a rate refusal, an error, or a module without the command — keeps the older flow
exactly: the navmesh close-in before the order, and after a refusal a 5 yd step along the path from the tank toward the
skull with a 15 s wait for the tank beside it. A module that answers once that it has no sight command is asked no more
for the rest of the run. The same holds for an account the module does not answer sight for: a query the core says
aloud as this character's own chat is read as that answer, not as a command falling through, so it trips no say-echo
guard and the next order still goes out. Either way the one `pull_sight_unsupported` event carries the `reason`
(`no-command` or `said-aloud`), and every later pull reads the unknown answer at once.

When those retries are spent — or the skull already stands inside the character's own 5 yd reach, where another step
would add no sight — the leading player pulls the mob itself:

1. `agent_pull` records the fallback: `group`, `why` (`sight` for the spent retries, `reach` for the in-reach
   shortcut), `refusals` (the tank's whispers so far), the distance to the mob (`yd`) and to the party's stand
   (`stand_yd`), and the stand itself (`x`, `y`, `z`).
2. The agent walks along the navmesh into the position its own pull opens from and attacks it: within the range of
   the attack it attacks with (the character's own auto attack, whose client chases into melee reach: `PlayAgentReachYd`
   less `PlayAgentPullMarginYd`, so the walk's arrival tolerance still lands in range) and in line of sight of the mob.
   The approach is bounded four ways: by `PlayLimits.PullReach` (the time), `PlayAgentApproachPathYd` (the path it may
   walk), `PlayAgentApproachPastYd` (how far past the party's stand it may end) and `PlayAgentApproachPackYd` (the
   clearance it keeps from every live hostile it is not pulling, 22 yd: proximity aggro at equal level is about 20 yd,
   larger for a lower-level agent). That clearance is measured against another pack of the plan, a patrol no plan group
   claims and a stranger alike, but not against the pull's own units, a unit already fighting the party, or one within
   `PlayAssistYd` (10 yd) of the skull, which is the pull's own social add and comes with it. The party's own pets,
   totems and guardians (units a party member owns, or that a party member has charmed) and critters are not counted;
   an enemy hunter's pet or a hostile totem is, and so is a party pet an enemy has charmed (the charm is the
   controlling ownership while it is on). The same ownership decides the stranger and gone answers: a unit that turns
   friendly or hostile because a charm came on or off is read again on the next plan, so it can appear as `gone`, as
   a new blocker in the clearance, or as a pull's own unit without moving. Where the agent already stands inside a
   hostile's clearance, only a point of
   the path nearer to that hostile than the agent stood at the pull's first plan (less `PlayAgentApproachStartEpsYd`,
   0.5 yd) blocks, so a path that walks away is clear and repeated re-plans cannot creep nearer a little at a time. A hostile the view does not carry (a pack out of view) is unknown and is not counted: the check fails open there. When the path cannot keep
   the clearance, the approach walks the longest prefix of it that ends short of the first hostile, inside the other
   bounds, and plans again from there; a prefix that does not move the agent is no path. It never walks through a pack.
   The path it walks is the path it checked for those bounds, not a second query.
   It never walks in under 40 per cent of its own health or with that health unknown (the row is re-read every 3 s
   while it is missing); under the floor it keeps to the party and never trades blows. An approach that cannot be made
   fails the fallback with `no-path` (the walker's path bound; its `detail` says which bound: `path`, `path-length`,
   `past-stand` or `pack-near`), `no-sight-after-approach` (in range, the line never cleared) or `approach-timeout`
   (the time ran out before the mob was in range and in sight). A navmesh route the approach cannot complete is
   `no-path` with `detail: path`; only the retreat to the party keeps the run's own `walk`/`walk:<kind>`
   stops on a route or walk error (the party gate's walk back hands a missing route to the ladder).
3. `agent_pull_engaged` records the mob that turned on the agent (`target`, `entry`, `yd`).
4. The agent walks back to where the party waits, the mob following. The tank is ordered onto it once the mob stands
   within 10 yd of the tank (a body beside the tank is inside the module's own line of sight), and an accepted order is
   given 3 s to show the mob on the tank or dead: the module's sight refusal arrives as a whisper, not as the order's
   code, so the reply alone is no proof of the take. A refusal, or the window's end, re-orders while the walk back
   lasts. A mob on the healer or a damage dealer is the party's fight, but it is not the tank's take: the hand-off is
   not done until the tank has it.
5. `agent_pull_back` records the end: `target` and `dead: true` when the mob died on the way, or `target`, `tank_yd`
   and `yd` (the agent's distance to the stand) when the tank took it. The normal fight follows.

The fallback is bounded and it never becomes a second tank:

| Limit | Default | Meaning |
|---|---|---|
| `PlayLimits.PullReach` | 20 s | The approach's time bound: the walk into the pull's range and the wait for the mob to turn on the agent. |
| `PlayLimits.PullBack` | 20 s | The walk back to the party and the tank's take. |
| `PlayLimits.TankTake` | 3 s | How long an accepted tank order may take to show the mob on the tank or dead. |
| `PlayAgentReachYd` | 5 yd | The agent's own melee reach: the range its own pull opens from; a skull already inside it skips the remaining step-closer retries. |
| `PlayAgentPullMarginYd` | 2 yd | The approach stops this far inside the pull's range, so the walk's arrival tolerance lands in range. |
| `PlayAgentApproachPathYd` | 40 yd | The longest navmesh path the approach may walk to the mob. |
| `PlayAgentApproachPastYd` | 30 yd | How far past the party's pull stand the approach may end. |
| `PlayAgentApproachStartEpsYd` | 0.5 yd | The slack of the clearance's start-inside rule: from a start already inside a hostile's clearance, a path point blocks only when it is nearer to that hostile than the distance the pull's first approach planned from, by more than this. The distance is kept per hostile for the whole pull, so repeated re-plans cannot creep nearer. |
| `PlayAgentApproachPackYd` | 22 yd | The clearance the approach keeps from every live hostile it is not pulling: another pack, an unclaimed patrol or a stranger (proximity aggro is about 20 yd; the plan carries no aggro margin of its own). A pack out of view is unknown and is not counted. |
| `PlayTankEngageYd` | 10 yd | How near the tank the brought-back mob must stand before the tank is ordered again. |
| `PlayAgentPullHP` | 40 % | The character's health floor for pulling; unknown health fails closed. |
| `PlayAssistYd` | 10 yd | The core's assistance radius: a plan boss inside it of the mob makes the pull the boss's. |

The bounds on what may be pulled:

- **One agent pull per group.** The same group's fallback is never taken twice; the second attempt records
  `agent_pull_failed` with reason `already-pulled`.
- **Never a boss pull.** The leg's boss keeps its own pull flow. A plan boss within 10 yd of the mob would join on the
  agent's own hit, which is the boss's pull and not a leading player's: the agent records `agent_pull_skipped` with
  `reason: boss-near` (`group`, `target`, `entry`, `boss`, `boss_yd`) and does not pull, and the pull ends as it would
  have without a fallback.
- **A moving patrol is checked only on a planned path.** The clearance check walks the fallback's own navmesh path and
  counts the hostiles the path comes near; when no complete route to the mob exists (`no-path` with `detail: path`),
  the agent pulls it from where it stands and that check does not run, so a patrol crossing that spot is not seen. It
  is a known limit of a fallback taken only after the tank refused the order, not a promise the plan's own stand
  replaces.

A failed fallback records `agent_pull_failed` with its reason:

| Reason | Meaning |
|---|---|
| `gone` | The mob left the view or turned friendly before or during the approach. A charm changing hands counts as turning: a party pet charmed by an enemy is a pack, and an enemy pet charmed by the party is the party's. |
| `already-pulled` | This group (or this unknown unit) already had its one agent pull. |
| `no-path` | No walk to the mob inside the approach's bounds; `detail` says which: `path` (no complete navmesh route), `path-length` (past 40 yd), `past-stand` (past 30 yd from the stand) or `pack-near` (another pack, an unclaimed patrol or a stranger is inside the 22 yd clearance, and the longest clear prefix was no path either). A `pack-near` event also carries `blocker` (`guid`, `entry`, `yd`: the hostile that stood in the way and how far it stood from the mob). |
| `no-sight-after-approach` | The approach reached the pull's range but the line to the mob never cleared. |
| `approach-timeout` | `PlayLimits.PullReach` ran out before the agent stood in range and in sight. |
| `reach-timeout` | The agent stood in its pull position and the wait for the mob to turn on it ran out (`PlayLimits.PullReach`); the mob evaded in place, held a non-party target, or the single swing did not land. |
| `health-unknown` | The character's health row could not be read: it never walks in. |
| `low-health` | The character stood under its 40 per cent floor. |
| `bring-back` | The mob never came to the tank inside the 20 s walk back. |

A pack's failed agent pull does not stop the run at once: the tank gets one more normal order (a `pull_retry` event
with `after: agent-pull`) for the pull's own window, because the fallback's approach may have moved the pack into the
tank's sight; only when that order fails too does the run stop `pull-failed`. A hostile the plan does not know (a
patrol candidate) is given up with `patrol_unpullable` (reason `agent-pull`) and the leg walks on. A pack the bots
finish during the refusals is no failed pull at all: nothing is left to pull, the pack counts as fought, and the leg
walks on.

# Flags

| Flag | Default | Meaning |
|---|---|---|
| `--plan` | required | The plan file. Its name picks the time budget: Ragefire Chasm 90 minutes, Wailing Caverns 150, anything else 120. |
| `--bots` | required without the raid flags | The four bots: tank, healer and two damage dealers, in that order, no repeats. The healer must use mana: the pre-boss rest gate reads its mana pool, and a healer whose power is not mana stops the run `boss-not-ready`. |
| `--tanks` | required for a raid | A raid's tanks, comma separated; the first is the pull tank (see [a player-led raid](#a-player-led-raid)). Excludes `--bots`. |
| `--healers` | required for a raid | A raid's healers, comma separated; any living one may raise. Excludes `--bots`. |
| `--dps` | required for a raid | A raid's damage dealers, at least 2. Excludes `--bots`. |
| `--layout` | none | A raid layout file (one bot and its subgroup 1-8 per line), applied before the run starts. Excludes `--bots`. |
| `--character` | required | The player's character: the agent's own, the party leader. |
| `--max-minutes` | 0 | The run's budget; `0` takes the plan's, 0-1440 otherwise. |
| `--max-wipes` | 2 | The run's wipe budget: the (n+1)th wipe ends it; `-1` is no budget, 1-20 otherwise. |
| `--last-leg` | every leg | End the run after that leg's boss. The rest of the route is still walked; a pack of a later leg met on the way is fought there. |
| `--disband` | off | Disband the group before the bots are logged out. |
| `--out` | none | Folder for the run's trace, summary and combat log; the spell judge reads it, so a live verdict needs it. |
| `--map` | off | After the run, write its map (SVG and HTML replay) into `--out`. Needs `--out`. |
| `--min-active` | 30 | Seconds a bot must be alive and in combat to be judged. |
| `--navmesh` | none | mmaps folder for the live walker (the dry walker walks straight lines). |
| `--dry` | off | Run the whole machine against the scripted world; no network. With `--out` the dry run is judged too. |
| `--no-preflight`, `--preflight-only`, `--serve-dir`, `--export`, `--soap-*` | | As on [dungeon-run](dungeon-run.md); the preflight mode is `dungeon-play` and checks the four bots are owned by the account. |

# Examples

The dry run first (it forms the group, walks and fights against the scripted world and leaves a trace):

```bash
forever-agent forever dungeon-play --plan dungeons/ragefire_chasm.json \
    --bots TANK,HEALER,DPS1,DPS2 --character PLAYER --dry --out ./runs/play-dry
```

The live run, written and judged:

```bash
forever-agent forever dungeon-play --plan dungeons/ragefire_chasm.json \
    --bots TANK,HEALER,DPS1,DPS2 --character PLAYER \
    --rest example.invalid:8080 --rpc example.invalid:8081 --ca ca.pem --account ACCOUNT \
    --navmesh ~/mmaps-forever --out ./runs/play-rfc --last-leg tunnel --map
```

# What you see

The run's counters, the lines it sent by their first words, its states, the judge's lines and the verdict:

```text
play: stop clear, 9 pulls, 0 wipes, 0 deaths, 47.3 min, gm_lines=0, clear=true
lines: .bot add=4, .bot attack=9, .bot follow=1, whisper release=1
states: PREPARE FORM TRAVEL ENTER LEG_WALK PULL FIGHT ...
summary: runs/play-rfc/r-20261124T190000-ef56.forever-run.json
trace: runs/play-rfc/r-20261124T190000-ef56.jsonl
judge: tank (warrior tank) warn: spec protection (role tank); missing core: Shield Slam
verdict: PASS
```

With `--out` the run writes `<run_id>.jsonl` (the trace), `<run_id>.forever-run.json` (the summary) and
`<run_id>.combatlog.jsonl` (the combat log). A player-led run has no decision capture to read, so `dungeon-play` does
not pull the module's rows; [dungeon-watch](dungeon-watch.md) does, while its run lives. The summary's `play` block
holds the run's minutes, its budgets,
`gm_lines`, the lines by prefix, the frame count and the combat-log counters (`combat_rows`, `combat_dropped`,
`combat_truncated`, `combat_error`). Its `notes` array holds the run's notes — the plan fields it does not act on and a
raid group's split when it differs from the plan's — the same lines the trace writes as `note` events.

The verdict is PASS only when all of these hold (a player-led raid's verdict is its own rule, three bullets, written
in [a player-led raid](#a-player-led-raid)):

- the run cleared (its stop reason is `clear`);
- it made at most **2** wipes, whatever `--max-wipes` allowed;
- `gm_lines` is 0;
- its minutes are within the plan's cap;
- the combat log has no dropped row, no truncation, no write error and at least one row;
- the spell judge judged all four bots and none failed or came out `na` (no class, or too little fighting to
  judge).

Every reason a FAIL needs is printed after `verdict: FAIL:` separated by `;`.

# Exit codes

| Code | Meaning |
|---|---|
| `0` | The verdict is PASS. |
| `2` | A usage error or a plan this build refuses. |
| `5` | The verdict is FAIL. |
| `7` | The link to the server was lost (the verdict still prints). |
| `13` | The preflight failed; nothing was started. |
| `3`, `4`, `6` | Login errors. |

# Common problems

- `not-alive` — the character was dead or a ghost when the run started, or it fell during one of the gate's waits.
- `not-near-portal` / `inside-instance` — the character is over 300 yd from the plan's portal, or already inside
  the instance, where there is no approach leg to start from. Move it outside first.
- `not-leader` — a group exists and this character does not lead it with exactly the four named bots.
- `form-refused:<code>` — the module refused `.bot add`; the run stops by the code. `bot-busy` means the bot is
  already in the world in another party.
- `tank-too-weak` / `tank-hp-unknown` — the tank's max health is below the plan's `tank_min_hp` (autogear again or
  hire another tank), or its health could not be read at all.
- `straggler:<slot>@<n>yd` — a bot fell behind a walk for 150 s even after the party gate's follow, the walk's own
  follow and a summon. The reason names every bot still behind and how far (`straggler:tank@97yd,dps2@103yd`); the
  trace's `regroup_hold`, `regroup_back` and `order` events say what was tried.
- `wipe-unrecovered` — a wipe was not recovered within 10 minutes; the party did not come back to a standing state.
- `raid-boss-reached` — a player-led raid reached a leg's living boss and stopped there (this is a PASS with the
  raid's verdict, and `clear` stays false). A boss that aggroed by itself stops the run the same way when its joined
  fight ends. The boss is the module's raid walk's.
- `raid-convert` / `raid-move` — the group would not become a raid, or the layout could not be applied (a failed move,
  six members into one subgroup, a name the party does not hold); nothing is walked.
- `raid-group-unsupported` — the session's group layer cannot convert or move a raid member (a build without the raid
  calls); the run stops before the sixth add.
- `judge: <bot> failed` — the spell judge saw casts its class and role should not make (or too few of the right
  ones); the judge's lines name what it saw.
