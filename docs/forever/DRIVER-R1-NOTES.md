# Forever dungeon driver, round D1 notes

Scope: the driver package that plays one dungeon plan for a five-bot party through a server module with bot
commands, the run loop as a state machine, the safety rules, the plan view, the run trace and summary, and a fake
harness that runs the whole machine without a network. This round has **no session adapter and no live server**: a
later round wires the same interfaces to the world session and the command channel. The fake and the `--dry` mode are
the only users of the driver today.

The command channel (round A6) is already on main; its typed model is `internal/forever/command/tbcmd`.

Code: `internal/forever/driver/{ports,plan,packs,orders,run,states,record,fake}.go`,
`cmd/forever-agent/forever_dungeon.go`.
The fake is a normal file, not a test file, because the `--dry` command runs the machine against it; the tests use the
same fake. The machine core (the ports' calls, the polls, the safety rules) is in `run.go`, the one send function and
the two order gates in `orders.go`; the states' work is in
`states.go`.

## The three ports

* `Commander` — the server module over the command channel: one state report, one threat report, one order, one party
  roster, and two free-form line senders (a token-matched setup line, and a tokenless core line). The port takes the
  typed reports of `tbcmd`, so the driver never parses a line itself. The fake answers from the same scripted state.
* `World` — what the commander's own client sees: our position/map/instance-epoch and whether a map transfer is in
  flight, the units in view, a party member's GUID by name, and the area-trigger packet. The real adapter is the
  session's object store; the fake is a scripted set of units.
* `Walker` — the navmesh path and the walk: `Path(from,to)` returns the plan's route type, `Walk` walks a polyline,
  `Stop` cancels. Walks and polls never overlap: the driver is one goroutine, and it cuts a walk into segments of at
  most 25 yards with a poll between segments (the session goroutine owns the packet loop, so a port call is either a
  send or a loop run, never both).

`Clock` is a port too: `Now` and `Sleep`. The fake advances virtual time and runs the scenario's script; the wall clock
is `time.Now`/`time.Timer`. Every wait in the state machine goes through it, so a scenario is exact and fast.

## The states, in my own words

The loop is one goroutine and one current state. Entering a state appends its name to the run's state list (that list
is what the scenario tests assert) and runs the state's work; a state may block, but only through the clock and the
ports.

| State | What happens | Leaves when | Gives up at |
|---|---|---|---|
| PREPARE | GM lines for the commander, the party roster (five, leader is the tank, we command it, levels), one state report: all alive and rested | every check passes | 60 s -> STOP `prepare:<what>` |
| TRAVEL | the commander is teleported to the outside point, the party is teleported to the portal, the party is told to follow | all five on the outer map within 15 yd of the portal | 60 s -> STOP |
| ENTER | the party enters first; when the tank's report says the instance map, the commander is teleported to the tank (fallback: appear), or walks through the portal's trigger (`--enter portal`) | our map and every bot's map/instance agree | 90 s -> STOP `enter:<block>` |
| LEG_WALK | path to the leg's via points and `to` on the walking filter, in segments of at most 25 yd; a hazard check on the units in view between segments | at `to` within 2 yd and the party is ready | a failed segment -> STOP `walk`; a blocked path -> STOP `path-blocked` |
| PICK | the next group of the leg in plan order (a patrol group first when its patrol is in view); after every group, the boss | a group with a live member, or the group judged clear | — |
| POSITION | walk to the group's last approach point (else a look point computed from the group centre), then wait for the patrol gate | ready and no patrol member within 25 yd of the stand or the pack | patrol 120 s -> pull the patrol as its own fight if it is a plan group, else STOP |
| PULL | one pull order at the live member nearest the tank, only through the pull gate (a fresh complete report in the same step, readiness, rate, budget, no stop) | the order is queued, then the tank is in combat or the threat report lists the mob | 12 s -> one retry with another member; a second failure -> STOP `pull-refused:<code>` |
| FIGHT | state and threat every 2 s; the trace's frames; runner and add handling; a mob that left the threat list out of sight is lost, not dead | the threat report is empty, every living bot is out of combat in two polls 2 s apart, and no mob is lost | 60 s of a lost mob -> `lost-mob` event, the group stays not-clear and the run stops `lost-mob`; 180 s trash / 300 s boss -> `long-fight` event; 600 s -> STOP |
| REST | polls until the party is ready; no free/hold order | ready | 180 s -> STOP `rest` |
| DEAD_WAIT | waits for the dead bots to come back after the fight | every bot alive | 60 s -> one revive per dead bot, event `practice_revive` |
| WIPE | the commander walks back to the leg's start and waits for the mobs to reset | the end line says whole | 300 s -> revive the party and teleport each bot to us; a second wipe on the same group or the third in the run -> STOP |
| BOSS | the boss's `first` entries in order, then the boss itself; same pull and fight rules | a dead unit of the boss entry | as FIGHT |
| DONE | one rest, then the ready check | the party is ready | 180 s -> STOP `rest` |
| LEAVE (`--reset`) | leave the instance, come out, reset it | the replies are ok and the bots are on the outer map | 120 s -> report, no retry |
| STOP | see below | — | — |

## The safety rules, in my own words

* Nothing is ordered unless the party is **ready**: the state report is at most 6 s old, its end line's counts agree,
  it has five bot lines with every party member in them, every bot is alive, not resting, its engine is on, it is not
  stuck and not blocked, it is on our map and instance, its distance to its master is known and short, and its last
  order did not fail. An error, a short report, a missing line, a missing distance or a stale report is *unknown*,
  and unknown never permits an order: the driver holds and polls again.
* One gate starts every fight. `pullAllowed` runs immediately before each pull or attack order, in the same step as
  the send: it polls a complete report taken after the last order, checks readiness, the rate window, the budget and
  the stop, and returns the view the target is picked from. `sendPull` and `sendAttack` refuse without its permit.
* Every order goes through `send`, and `send` is the package's only call of the commander's `Order` port. It
  classifies the verb: `pull`, `attack` and `assist` are fights (the permit above); `come`, `follow` and any future
  movement verb are moves; `stay`, `hold`, `free` and `endpull` are passive and always allowed. A verb that is not in
  the table is refused, never sent (`TestEveryOrderVerbIsClassified`, `TestOnlySendSendsOrders`).
* Movement orders have their own gate (`moveAllowed`). A come or a follow may go out only with no stop pending, the
  commander's position known and no transfer in flight, a complete report newer than the last order, the bot alive on
  the commander's map, and the line the bot would walk — the walker's planned route when it returns a complete one,
  else the straight line — clear of every plan pack not marked dead: the group's radius (the plan's, or 25 yd) plus a
  10 yd margin, counting both the pack's plan positions and the live members in view. Otherwise nothing is sent.
* A straggler behind such a pack gets no come: the commander walks back toward it, one 20 yd step per poll and only
  on ground outside every live pack's radius, and only when no walk is in progress. After 30 s (`StragglerBack`)
  the run stops as `straggler-behind-pack:<role>`. A refused movement order is retried on the next poll; the
  straggler stages (come, follow, stop) advance only when an order actually went out.
* Pull rate: at most one pull or attack in 15 s and three in a minute. One attack per mob per fight.
* Stragglers: a bot more than 30 yd from its master for 20 s gets a come order; still 30 s after that, a follow
  order; at 90 s the run stops. A bot on another map or blocked waits 60 s and then stops the run.
* Stuck: no progress (a walked segment, a finished pull, a revived bot) for 6 minutes stops the run.
* Blind: three failed polls in a row hold the run; 30 s blind stops it.
* Budget: 120 minutes, three wipes, eight practice revives, three pull retries per group.
* STOP outside a fight: one more complete report, then the stay order and the summary. Inside a fight: no new order;
  the stop gets its own polls and sleeps, detached from the caller's cancel, and the stay goes out only after a
  complete report positively shows no living bot in combat. The whole stop phase is bounded twice: by the stop-fight
  cap on the run's clock (600 s) and by the same span of real time as a backstop, so a server that is connected but
  silent cannot hold it. Each State and Order call on the stop path has its own 10 s bound (`FinishCallTimeout`). If
  a bound, the link or the polling fails first, nothing is sent and the stop reason ends in `:fight-unresolved`: the
  bots keep their combat order. A lost link ends the stop at once. The passive hold is never sent in a fight: it
  would let the bots die.
* The walker stops on the first failed poll and walks no further segment until a good report is back; every stop path
  of a walk calls `Walker.Stop`. Stragglers are handled in POSITION, PULL and REST as well as on the walk.
* Link lost: no port may be called after the error; the summary is rewritten with `stop=link-lost` and the command
  exits 7. The bots keep following a master that is gone, so the operator ends them from the console.
* When in doubt, do less: hold (send nothing new) rather than pull.

The driver itself never sends a `free` or a `hold`; the only orders it sends are pull, attack, come, follow and stay
(plus the free-form setup/core lines of the entry phase), and all of them go through `send`. Rest is observed, not
ordered.

No plan file in `dungeons/` loads today: every one sets at least one field this round refuses, and the loader test
records the exact fields per file (Ragefire Chasm: `kill_first`, `cc_priority`, `cc_always`, `tank_min_hp`,
`legs[6].boss.tactics` and `legs[7].boss.tactics`). The `--dry` command therefore runs plans without those fields; the state-machine smoke is
a built nine-leg, four-boss route, and the trace and summary are written when `--out` is given.

## What the fake can and cannot show

Can: the exact state sequence, every order's target/verb/argument and its time, the exact pull rate, the effect of a
death (a dps, then a priest's resurrection; or no resurrection at all), a wipe and a repeated wipe, a fleeing mob
that brings a second pack, a patrol's gate, a bot left outside the instance with a block reason, a straggler, a
straggler behind an uncleared pack (the walk back, and the run that ends behind it), a server that blocks the stop's
calls, a state call that times out, a report with four lines, a report without a distance key, a successful report
that is stale, a refused pull and a refused stay, a report failing during the rate wait, a boss pull, a stop mid-fight that
resolves or does not, an interrupt, Force, a lost mob that holds the fight, a link loss from any port, and a plan the
driver refuses.

Cannot: what the server really does. The fake does not prove that a pull order makes a bot fight, that the threat
list is complete, that a bot's report arrives within 6 s on a busy server, that a commander the bots cannot see is
followed the same way, or that the walker's paths exist on the real map. Those belong to the live rounds. The fake's walker moves in a
straight line at 7 yd/s and does not use the navmesh; the dry run over the real plan therefore proves the machine's
sequence and the plan's shape, not the walking.

## Needs from the module

Everything the driver assumes about commands the module does not have yet, so the module's brief can carry it:

1. The typed state report as the A6 model reads it, five bot lines for `party <tank>`, an end line whose `resting=`
   equals the lines, and a `ready=`/`why=` pair consistent with it (A6 interface request 2). The driver recomputes
   readiness itself from the twelve keys today, because the module's `ready` is the one authoritative gate it would
   rather use.
2. `life=alive|awaiting-res` and `block=<reason>` on each bot line, open-world bots included (A6 interface request 5).
   Round D1 reads both out of the record's extra keys when they are present and falls back to `st` when they are not.
3. An end-line `wipe=yes` and `whole=yes`, so the wipe states have the module's own verdict as well as the driver's
   count of dead bots (D1 can work without them; it counts).
4. Threat lines with a position and a flee flag (A6 interface request 1) would make the runner and the "which pack"
   questions exact. D1 works without them: it compares unit positions across polls and knows the plan's fleers.
5. `order` replies with per-bot `status=queued|refused` and a refusal code (the model has it); the driver's stop
   reason carries the code of a refused pull.
6. Token-matched setup lines `tele`, `enter`, `instance`, `revive` usable by the commander from chat, with tokens
   (A6 interface requests 6 and 7). D1 formats them and the fake answers; the module must accept these exact forms:
   `.tb tele <who> <map> <x> <y> <z>`, `.tb tele <who> to <other>`, `.tb tele party <map> <x> <y> <z>`,
   `.tb enter party`, `.tb instance <who> info|leave|reset`, `.tb revive <who>`.
7. `.tb instance info` with a `bosses=` list (A6 interface request 4), for the boss state in a later round. D1 uses
   the world's dead units instead.
8. One order that waits for its result (A6 interface request 3) is explicitly not in M1; the driver polls.

## Decisions taken while writing it

* **Hazard and "the next group".** "The next group" is read as the target group and the first uncleared group after
  it in plan order (the first uncleared group while walking a leg). The real plan needs that reading: on `trogg_cave`
  the look point of the pack before the lone Trogg stands 17.9 yards from that Trogg, and on `molten_cave` the third
  group's pack 14.9 yards from the second's stand. The pack right after the target is pulled first (the plan would
  have pulled it one step later), and the target follows once it is clear. A pack *further* ahead near the way still
  holds the walk for 60 s and then stops it (`path-blocked`); a unit that belongs to no plan group is not a pack
  hazard and does not stop a walk (a boss stands exactly where the party must walk).
* **Patrol identity.** Without spawn ids a patrol and a pack share an entry, so a unit is a patrol member only when
  its entry is in the patrol's list, it stands within 10 yards of the patrol's polyline, and no plan group claims it.
  A patrolling spawn that stands inside a pack's radius is missed; the plan's interface request for spawn data would
  settle it.
* **Field refusals.** Every plan field that would change an order is either read by the driver or refused by its
  plan path. That includes steps, after_boss, optional and leftover entries, holds, ambushes, needs_walk, rest and
  companions_loot, a via's direct/exact/gm_cross, a group's stand_pull/clear_yd/max_bodies/team/equiv, a boss's
  tactics/wait_s/phase_entries/stand_pull/summon_open_step/skip_unreachable/team/companions_loot/loot_item, and the
  plan's kill_first, cc_priority, cc_always, no_cc, summoned_kill_first, pull_ceiling, tank_min_hp, watchdog and team.
  Leg.Skip is read: a skipped entry is not part of a group's membership and is not waited for as a boss's first entry.
  A plan's name/status/notes are descriptions, its level band and hire composition belong to party preparation, and
  entrance_tele, skip_export and a patrol's source/period_estimate belong to the remote mode and plan-export; these
  are not run inputs. Decoding is strict: an unknown JSON key is an error. No plan file in `dungeons/` loads today;
  the loader test records, per file, the fields that refuse it.
* **Practice revives are counted per bot.** One party revive that raises five bots costs five of the eight; a wipe
  that needs it is late in a run anyway.
* **A stop inside a fight** watches with the same 2 s polls and never sends anything; the plan's `stay` goes out when
  the party is out of combat, or at once when the operator interrupts a second time.
* **Readiness and `dm`.** `tbcmd.Distance` now carries `Has`, set when the key was present: an absent distance is
  not known and never ready (it is not zero yards). The adapter round must always send `dm`; the driver treats
  `othermap`, an absent key and a number over the limit as failures.

## Open questions

* The exact wording of the setup lines is a guess from the module's prose; the fake treats them as opaque strings.
  The module's brief should fix the grammar before the adapter round.
* Does a group-less GM enter the party's instance with a teleport-to-character? The adapter round must find out
  live. D1 only models the answer.
* The runner threshold (20 % health, 8 yd away from the tank between polls) is a first guess; whether a real flee
  is visible in two 2 s polls is unknown.
* The wipe reset (`whole=yes`) is a slice-4 key; until it exists the driver also accepts the threat list going empty
  and the bot lines leaving combat for 10 s.
* The interface requests above are still the module's; the driver works without them and lists what it recomputes.
* `--enter portal` walks the commander into the portal's area trigger; this is a client packet the session will send,
  never the instance's own trigger by teleport (the teleport path stays the default).
