---
type: Command
title: forever dungeon-play
description: Play one dungeon plan as a real character leading four bots, and read the run's record and its pass or fail verdict.
tags: [dungeons, play, player, command]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-25T10:00:00Z }
sources:
  - id: play-cmd
    resource: cmd/forever-agent/forever_play.go
  - id: play-core
    resource: internal/forever/driver/play.go
  - id: play-death
    resource: internal/forever/driver/play_death.go
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
```

`dungeon-play` is the player mode: the agent's own character is party leader and fights as one of the group, and
the owner's four bots follow it (tank, healer and two damage dealers, in that order). Every line it sends is a bot
module line or one of the two whisper words; it never travels by GM, and the run's record proves that (a line
starting with `.` that is not a `.bot` line fails the milestone).

The run is also judged: a spell judge reads the run's recording and checks that each bot cast what its class and
role should, and the milestone's rule turns the run and the judge's findings into a single PASS or FAIL.

# What it does

| State | Work |
|---|---|
| `PREPARE` | Reads its own character: alive, on the outer map and within 300 yd of the plan's portal, not already inside the instance. If a group is already formed, this character must lead it and be its only member that is not one of the named bots. |
| `FORM` | Adds the four bots in order and waits for each to come online, then sets the loot method (group loot, uncommon threshold) and reads the tank's max health. A module refusal stops the run by its code. |
| `TRAVEL` | Walks to the plan's portal and waits until every bot stands within 30 yd (a straggler gets a follow order at 20 s; the run stops at 60 s). |
| `ENTER` | Takes the entrance trigger, waits for the group on the plan's map, and summons any bot still outside (a summon on cooldown is waited out once). |
| `LEG_WALK` | Walks each leg in segments of at most 15 yd, staying out of the plan's avoid boxes, holding for a live patrol on the way (20 yd, 120 s, with a stillness test so a static hostile is not waited for forever). |
| `PULL` / `FIGHT` | Marks the skull, orders the attack, attacks itself 1.5 s after the tank has the skull, re-orders the tank if the skull breaks to a non-tank, and ends the fight when no living unit on a party member remains. |
| `REST` / `LOOT` / `STEPS` | Rests between packs, loots the kills, and runs the plan's steps between the packs and the leg's boss. |
| `DEAD_WAIT` / `WIPE` | A dead character waits for a resurrect offer or a healer's raise; a wipe is recovered (release, ghost run to the portal, the entrance's raise) within 10 minutes. |
| `BOSS` | Gathers the party within the pull distance, rests to the bars, marks the boss, kills its `first` entries and then the boss. |
| `DONE` / `LEAVE` | Checks that every boss up to `--last-leg` is dead, disbands if asked, logs the bots out. |

The plan's crowd-control lists (`cc_priority`, `cc_always`, `no_cc`) and boss `tactics` are **not** run by player
runs; the run says so once in its notes instead of dropping them silently.

A plan boss killed as an add in another fight is recorded killed when a fight's view shows it dead, so the run neither
pulls it again nor fails `bosses-alive` on it.

# The leading player's own pull

The pull is the tank's first: the agent puts the skull on the pack's kill-order entry and the cross on the next unit,
selects the skull and orders the tank onto it. The tank's answer is the module's own: when it whispers that it cannot
see the marked skull, the party steps 5 yd along the navmesh path toward it (never nearer than 5 yd), waits up to 15 s
for the tank beside the new stand, and orders again with the mark re-sent. The refusal's own bounds are 4 steps
(`PlayRefuseTries`) and 90 s from the first refusal (`PlayRefuseTime`).

When those retries are spent — or the skull already stands inside the character's own 5 yd reach, where another step
would add no sight — the leading player pulls the mob itself:

1. `agent_pull` records the fallback: `group`, `why` (`sight` for the spent retries, `reach` for the in-reach
   shortcut), `refusals` (the tank's whispers so far), the distance to the mob (`yd`) and to the party's stand
   (`stand_yd`), and the stand itself (`x`, `y`, `z`).
2. The agent walks along the navmesh into the position its own pull opens from and attacks it: within the range of
   the attack it attacks with (the character's own auto attack, whose client chases into melee reach: `PlayAgentReachYd`
   less `PlayAgentPullMarginYd`, so the walk's arrival tolerance still lands in range) and in line of sight of the mob.
   The approach is bounded four ways: by `PlayLimits.PullReach` (the time), `PlayAgentApproachPathYd` (the path it may
   walk), `PlayAgentApproachPastYd` (how far past the party's stand it may end) and `PlayAgentApproachPackYd` (it never
   walks through another pack's radius). It never walks in under 40 per cent of its own health or with that health
   unknown (the row is re-read every 3 s while it is missing); under the floor it keeps to the party and never trades
   blows. An approach that cannot be made fails the fallback with `no-path` (the walker's path bound; its `detail` says
   which bound: `path`, `path-length`, `past-stand` or `pack-near`), `no-sight-after-approach` (in range, the line never
   cleared) or `approach-timeout` (the time ran out before the mob was in range and in sight).
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
| `PlayAgentApproachPackYd` | 10 yd | The radius around another pack's live unit the approach never walks through (the core's assistance radius). |
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

A failed fallback records `agent_pull_failed` with its reason:

| Reason | Meaning |
|---|---|
| `gone` | The mob left the view or turned friendly before or during the approach. |
| `already-pulled` | This group (or this unknown unit) already had its one agent pull. |
| `no-path` | No walk to the mob inside the approach's bounds; `detail` says which: `path` (no navmesh route), `path-length` (past 40 yd), `past-stand` (past 30 yd from the stand) or `pack-near` (another pack's radius is in the way). |
| `no-sight-after-approach` | The approach reached the pull's range but the line to the mob never cleared. |
| `approach-timeout` | `PlayLimits.PullReach` ran out before the agent stood in range and in sight. |
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
| `--bots` | required | The four bots: tank, healer and two damage dealers, in that order, no repeats. The healer must use mana: the pre-boss rest gate reads its mana pool, and a healer whose power is not mana stops the run `boss-not-ready`. |
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
`<run_id>.combatlog.jsonl` (the combat log). The summary's `play` block holds the run's minutes, its budgets,
`gm_lines`, the lines by prefix, the frame count and the combat-log counters (`combat_rows`, `combat_dropped`,
`combat_truncated`, `combat_error`).

The verdict is PASS only when all of these hold:

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

- `not-alive` — the character was dead or a ghost when the run started.
- `not-near-portal` / `inside-instance` — the character is over 300 yd from the plan's portal, or already inside
  the instance, where there is no approach leg to start from. Move it outside first.
- `not-leader` — a group exists and this character does not lead it with exactly the four named bots.
- `form-refused:<code>` — the module refused `.bot add`; the run stops by the code. `bot-busy` means the bot is
  already in the world in another party.
- `tank-too-weak` / `tank-hp-unknown` — the tank's max health is below the plan's `tank_min_hp` (autogear again or
  hire another tank), or its health could not be read at all.
- `straggler` — a bot fell behind a walk for 150 s even after the follow and summon attempts.
- `wipe-unrecovered` — a wipe was not recovered within 10 minutes; the party did not come back to a standing state.
- `judge: <bot> failed` — the spell judge saw casts its class and role should not make (or too few of the right
  ones); the judge's lines name what it saw.
