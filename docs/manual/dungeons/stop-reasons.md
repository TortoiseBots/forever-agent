---
type: Reference
title: Stop reasons and verdicts
description: Every reason a Forever dungeon run, watch or walk can end with, and the verdict each kind of report carries.
tags: [dungeons, reference, errors, verdicts]
status: stable
generated: { by: omp/deepseek-v4.1-flash, at: 2026-11-25T10:00:00Z }
sources:
  - id: driver-run
    resource: internal/forever/driver/run.go
  - id: driver-states
    resource: internal/forever/driver/states.go
  - id: driver-watch
    resource: internal/forever/driver/watch.go
  - id: driver-play
    resource: internal/forever/driver/play.go
  - id: play-death
    resource: internal/forever/driver/play_death.go
  - id: play-fight
    resource: internal/forever/driver/play_fight.go
  - id: route-walk
    resource: internal/forever/world/route_walk.go
  - id: reportcard
    resource: internal/forever/reportcard/card.go
---

# Where a stop reason comes from

Every run ends in one state, `STOP`, with one stop reason: the first thing that ended it wins, and later problems do
not replace it. The same word goes into the run's summary as `stop_reason`, into the report card (`STOPPED AT ...`,
`PAUSED-STUCK AT ...` or `FAILED (...)`) and into the command's counter line (`run: stop <reason> ...`,
`watch: stop <reason> ...`, `play: stop <reason> ...`).

The reason is built from a fixed vocabulary, sometimes with a suffix that names the member or the module's own
answer: `<word>:<detail>` such as `prepare:offline`, `straggler:dps1` or `start:bad-report`.

# A run's stop (dungeon-run and the module's run)

Setup and party:

| Stop | Meaning | What to do |
|---|---|---|
| `config` | A watch was given no leader or plan, or an unknown `--enter`. | Fix the command line. |
| `legs` | The leg selection is invalid for the plan. | Fix `--from-leg`/`--legs`. |
| `prepare:party` | The module's roster read failed. | Check the serve link and the module. |
| `prepare:roster` | The party is not the expected size (five, or the raid's size). | Put exactly the named members in the party. |
| `prepare:names` | A named member is not in the roster. | Fix the names. |
| `prepare:leader` | The party's leader is not the tank (a run) or not the flagged leader the watch names. | Make the tank lead, or fix `--leader`. |
| `prepare:commander` | The roster's commander is not this character. | The character must command the party. |
| `prepare:offline` | A member is offline. | Log it in. |
| `prepare:level` | A member is below level 10. | Level it up. |
| `prepare:roles` | The roles are wrong: a run needs at least one tank, one healer and three damage dealers. | Fix the party's roles. |
| `prepare:not-bot`, `prepare:not-commanded`, `prepare:observer-commander` | A watch's roster has a member that is no host bot, a member not commanded, or names the observing character as commander. | Fix the module's party. |
| `prepare:gm` / `prepare:gm-visible` | The GM line was refused or could not be sent. | The account needs GM mode; check the rank. |
| `prepare:<ready>` | After 60 s the party was still not ready; `<ready>` is the last readiness reason: `report` (an incomplete state report), `stale`, `dead:<member>`, `resting[:<member>]`, `noengine:<member>`, `stuck:<member>`, `block:<reason>`, `map:<member>`, `inst:<member>`, `othermap:<member>`, `dist:<member>` (distance to the master unknown), `range:<member>` (too far from the master or the leader). | Read the member named and the reason; fix that member's state. |
| `commander:unnamed` | The watch could not learn the module's commander. | Read the module's roster. |

Travel and entry:

| Stop | Meaning | What to do |
|---|---|---|
| `travel` | The whole party did not stand at the plan's portal within 60 s. | Check `portal`, `outside_tele`, and whether the party can follow. |
| `enter:trigger` | Taking the entrance trigger failed. | Check the plan's `entrance_trigger` and the connection. |
| `enter:appear` | The GM's appear of the commander at the tank failed. | Check the GM rights and the tank's name. |
| `enter:<block>` | After 90 s the party was not in, and `<block>` is the module's own block reason, or `timeout`. | Read the block: an engine off, a bot in another instance, a master outside. |
| `enter:party-map` | A watch found the party not all on the plan's map in one instance (`--enter none`). | Place the party correctly, or drop `--enter none`. |

Walking, pulling and fighting:

| Stop | Meaning | What to do |
|---|---|---|
| `walk` | A navmesh path to the next point failed, or the walk did not finish. | Run [plan-check](plan-tools.md) on that leg. |
| `path-blocked` | The way was held by an obstacle for 60 s. | Look at the point; the pack or a door may block it. |
| `combat` | Combat started where the run did not expect it (a pack pulled early). | Check the route for a pack it wakes; see [trash-check](plan-tools.md). |
| `patrol` | A patrol stayed within the pull's aggro for 120 s, so the pull could not go out. | Give the plan the patrol's path and period, or a `stand_pull` with `clear_yd`. |
| `pull-refused:<code>` | The module refused the pull order, with its own code. | Read the code; a sight or a target problem. |
| `lost-mob` | A mob left the fight's threat list and was not found for 60 s: the pack can never count as cleared. | Check the pack's `skip` and its positions. |
| `fight-timeout` | A fight ran 600 s. | Check the pack's size and the party's damage; a genuinely long fight is rare. |
| `attack` / `mark` / `select` | A player run could not send its own attack, a mark, or a target selection. | Check the character's state and the module. |
| `straggler:<role>` | A member fell behind for 90 s in a leg, even after the follow and summon attempts. | Check the member's engine and path. |
| `straggler-behind-pack:<role>` | The commander could not walk back to a straggler behind an uncleared pack within 30 s. | Check the pack and the route. |
| `boss-missing` | The boss was not at the plan's point within the state's wait (`Boss`, 60 s, plus `wait_s` for a patrolling boss). | Check the boss position and `wait_s`; a boss with no spawn row is summoned by an event and must not be a plain boss. |
| `boss-not-ready` | Before a boss, after both rest windows, the party was still short: the healer under 50% mana or the tank under 70% health. The gate fails closed, so a tank or healer missing from the roster, dead, a ghost, or without a readable bar counts as short, and so does a healer whose power is not mana. | Let the party rest and pull again; check the named member's power type. |
| `bosses-alive` | At the end, a boss of the run was still alive (a player run's check of every boss up to `--last-leg`). A boss a fight's view saw dead counts as killed even when it died as an add in another pack's fight. | The route or the boss order is wrong. |

Deaths, wipes and the end:

| Stop | Meaning | What to do |
|---|---|---|
| `dead-wait` | A dead member did not come back within 60 s, or within 30 s after a practice revive. | Check the healer and the member. |
| `wipe-budget` | The wipe count reached `--max-wipes`. | Investigate the wipe; the pack is too big or the party too weak. |
| `wipe-repeat` | The same pack wiped the party twice. | Fix that pack's size or order before running again. |
| `wipe` | A wipe with the practice switch off. | Turn practice on, or fix the wipe. |
| `wipe:recover` | The wipe's recovery did not finish within 300 s. | Check the party's way back and the plan's `portal`. |
| `wipe-unrecovered` | A player run's wipe was not recovered within 10 minutes. | Check the ghost run and the healer. |
| `practice-budget` | The practice revive budget (8) was spent. | Investigate the deaths; the party is not surviving. |
| `clear` | The plan's work is done. Success. | — |
| `leave` | The party could not leave and reset the instance within 120 s. | Check the exit and the reset (`--reset`). |
| `bot-outside:<reason>` | During the reset a member was still not outside. | Check the member's placement. |
| `disband` | A player run's disband failed; the run still ends. | Check the group state. |
| `budget` | The run's minutes (`--max-minutes`, or the plan's) are spent. | Split the plan, or fix what is slow. |
| `stuck` | No progress for the driver's 6-minute stall limit (any progress resets it). | The run is stuck; find the last order and point in the trace. |
| `blind` | Three state reads failed over 30 s; the last error is printed. | Check the serve link and the module. |
| `link-lost` | The connection dropped. Nothing is sent after it. | Reconnect; the character may stay in the world. |
| `interrupt` | The operator interrupted (Ctrl-C). | — |

# A watch's own stops

| Stop | Meaning | What to do |
|---|---|---|
| `start:<code>` | The module refused `.tb run start`. `start:bad-report` is an answer without exactly one run record; `start:foreign-bot` means another client leads that run (use `--attach`); `start:attach-plan` and `start:attach-timeout` are `--attach`'s own refusals. | Read the code; fix the party or the plan. |
| `status:records` | The status answer carried not exactly one run. | Check the module's run table. |
| `done` | The module's run reached `done`. | — |
| `stopped` | The module's run reached `stopped`; its `why` is printed. | Read `why`. |
| `wipes` | The module's wipe count passed `--max-wipes`. | Investigate the wipes. |
| `watchdog` | No new run event arrived for `--watchdog-minutes`; a running run is paused with a reason and reported. | Check the module's run; the bots may be stuck. |
| `paused-stuck` | The module's pause stood for `--pause-minutes`. The report card reads `PAUSED-STUCK AT <leg/step>` and names the pause reason. | Fix the pause's cause; the module never resumes by itself. |
| `practice-budget` | Paused on a wipe and the rescue cap (8) was spent. | Investigate the wipes. |
| `revive:<code>`, `resume:<code>`, `resume:timeout` | The rescue's `.tb revive party` or `.tb run resume` was refused, or the run did not resume in time. | Read the module's code. |
| `enter`, `enter:party-map`, `config`, `budget`, `blind`, `link-lost`, `interrupt` | As above. | As above. |

# A player run's extra stops

[dungeon-play](dungeon-play.md) has the same vocabulary plus the player's own:

| Stop | Meaning | What to do |
|---|---|---|
| `last-leg` | `--last-leg` names no leg of the plan. | Fix the name. |
| `state:<STATE>` | A state returned no next state without a reason. | A driver bug; keep the trace. |
| `out` | The run's record could not be opened. | Fix `--out`. |
| `not-alive` | The character is dead or a ghost at the start. | Resurrect it. |
| `not-near-portal` | The character is over 300 yd from the plan's portal, or the plan has none. | Move it outside, near the portal. |
| `inside-instance` | The character is already on the plan's map; plans have no approach leg. | Leave the instance first. |
| `not-leader` | A group exists and this character is not its leader, or is not its only non-bot member. | Leave the group, or lead it. |
| `bot-not-online` | A bot did not come online after `.bot add` within 60 s. | Check the bot and the module. |
| `form-refused:<code>` | `.bot add` was refused; the code is the module's. | Read the code. |
| `bot-busy` | A bot is in the world already, in another party. | Remove it from that party. |
| `form` | Forming the group failed for another reason. | Check the module. |
| `loot-method` | The loot method could not be set. | Check the module's group commands. |
| `no-module` | The server has no bot module. | Load the module. |
| `tank-hp-unknown` | The tank's max health could not be read. | Check the tank's roster line. |
| `tank-too-weak` | The tank's max health is below the plan's `tank_min_hp` (or its level is above the band). | Autogear, or hire another tank. |
| `time` | The run's own clock (`MaxMinutes`) ran out inside a wait. | Same as `budget`. |
| `enter` | The entrance trigger or the map change failed. | Check the plan's entrance and the connection. |
| `bot-not-entered` | A bot did not get into the instance, even after the summon. | Check the bot's placement and the summon cooldown. |
| `walk`, `walk:<kind>`, `walk:mapchange` | A walk failed; `<kind>` is the navmesh route's answer. The leading player's own approach is not one of these: a route it cannot complete fails the pull (`no-path` with `detail: path`), and only its retreat to the party and its walk back stop the run here. | Plan-check the point. |
| `exact` | The character could not stand on an `exact` waypoint (1.5 yd). | Check the point's ground. |
| `avoid` | The character stood in one of the plan's avoid boxes. | The point is wrong; fix the plan or the box. |
| `straggler` | A bot fell behind a walk for 150 s. | Check the bot. |
| `pull-no-sight` / `pull-failed` | The tank never saw the marked skull / nobody answered the pull. A leading player's own fallback pull that fails is followed by one more tank order (a `pull_retry` event with `after: agent-pull`); when that fails too the run stops `pull-failed`, never `pull-no-sight`. | Check the pack's distance and the geometry. The trace's `agent_pull_failed` names the fallback's reason (`gone`, `already-pulled`, `no-path` with its `detail`, `no-sight-after-approach`, `approach-timeout`, `reach-timeout`, `health-unknown`, `low-health`, `bring-back`); `agent_pull_skipped` with reason `boss-near` means a plan boss stood within 10 yd of the mob, which is the boss's pull, not the agent's. |
| `fight` | The character's own fight tick failed. | Check the character's state. |
| `fight-timeout` | A fight ran over 600 s. | Check the pack. |
| `rest` | Resting failed. | Check the character's state. |
| `step:<kind>` | A plan step could not be done; the trace records its leg, index, kind and the reason. | Read the trace's `step_failed` event. |
| `whisper`, `send` | A line could not be sent (a bad whisper word, or a send error). | Check the module's chat grammar and the link. |
| `recover`, `recover:path` | The ghost run or the raise failed; `recover:path` means the ghost could not get closer to the portal on a walk. | Check the plan's portal and `entrance_trigger`. |

A player run's pre-boss gate also writes its two readings on every one of its events: `boss_rest`, `boss_rest_again`,
`boss_rest_end`, `boss_rest_timeout`, `boss_not_ready` and `boss_pull_min_bars` carry `tank_hp` and `healer_mana` —
the tank's health and the healer's mana in per cent, `-1` when the member or its bar could not be read. Those are the
numbers the gate decided on, so a `boss-not-ready` can be read from the trace alone.

# Verdicts

**Report card** (`dungeon-watch`, `dungeon-run` with `--out`): one word and one line.

| Verdict | When |
|---|---|
| `CLEARED` | The run's `clear` is true. |
| `PAUSED-STUCK AT leg N "name"/step M (pause reason)` | The watch ended on a pause that did not clear. |
| `STOPPED AT leg N "name"/step M (reason)` | The run stopped; `reason` is the stop reason, with the module's `why` appended when it differs. |
| `FAILED (reason)` | The card calls these failures: `link`, `blind`, `config`, `legs`, `enter`, `stuck`, `state`, and any stop whose reason starts with `start:`, `revive:`, `resume:`, `status:`, `prepare:` or `state:`. Also a record with no state and no steps (nothing ran). |

**Player verdict** (`dungeon-play`): `verdict: PASS` or `verdict: FAIL: <reasons>`; the reasons are `not-clear (stop
<reason>)`, `wipes N > 2`, `gm-lines N`, `minutes X > <cap>`, the combat-log problems (`combat log: N rows
dropped`, `truncated at its size bound`, `no rows`, `write failed`), `judge: N of 4 bots judged`, `judge: <bot>
failed`, `judge: <bot> not judged`, `no-play-record`, `judge: <error>` and `link-lost`.

**Route walk** (`forever route-walk`): one verdict per leg, worst first — `FAIL`, `INCOMPLETE`, `UNCONFIRMED`,
`STUCK`, `FELL`, `LIQUID`, `NOPATH`, `DEVIATED`, `PASS` — and the run's verdict is the worst leg's. Exit `12` when
the run's verdict is not `PASS`.

**Plan reports**: `plan-check` exits 11 on a failing point or a lint error; `plan-probe`'s segments are `ok`,
`warn` or `bad` (exit 20 on any bad); `trash-check`'s segments are `clear`, `own`, `patrol` or `danger` (exit 21
with `--fail-on`); `plan-scout` steps are `ok`, `warn`, `fail` or `skip` (exit 27). See [plan-tools](plan-tools.md).
