# Forever dungeon driver, round D2 notes

Scope: the live half's wiring. The driver's run logic is round D1 and is unchanged except for one fix (a walk that
loses the link now ends the run as `link-lost`, below). This round binds the driver's four ports to a real world
session, adds the bot-command records the typed layer did not read yet, adds the live mode of `forever dungeon-run`,
and ships an open-world test plan. There is **no live run in this round**: the servers were busy and the module build
with the order commands runs on one host only today. The section at the end lists what the live half must do first.

Code:

* `internal/forever/world/dungeon_session.go` — the four adapters.
* `internal/forever/world/dungeon_run.go` — the session-side run: settle, GM guard, recorder, `run.Play`.
* `internal/forever/world/session.go` — `Options.DungeonRun`, `Report.Dungeon`, the dispatch branch.
* `internal/forever/command/tbcmd/tbcmd.go`, `instance.go` — the new threat keys and the instance report.
* `cmd/forever-agent/forever_dungeon.go` — dry and live modes.
* `testdata/open_world_northshire.json` — the open-world test plan.

## The ports and their adapters

The driver never imports this package; the session implements the ports. Every read comes from the model the packet
loop already keeps and every send goes through the session's own command transport, on the session goroutine.

| Port | Adapter | What it reads or sends |
|---|---|---|
| `Commander` | `dungeonCommander` | one `command.Session` over `cmdLink` (the A6 transport), one `tbcmd.Client`. `State` = `.tb state party <tank>`, `Threat` = `.tb threat party <tank>`, `Party` = `.tb party <tank>`, `Order` = the typed order, `Act` = one typed setup action (below). There is no method that takes a command line: `(*dungeonCommander).Run` is the one function in the package that reaches the command session, and the module's typed client is built over it, so every line — report, order or action — is built from typed arguments by `tbcmd`. |
| `World` | `dungeonWorld` | `Self` = the mover's position, `objects.World.MapID`, the map change's epoch and its between-maps window. `Units` = the object model's units, positions at the spline clock, health, the values target, the combat model's dead/ghost state and cast; a unit is friendly when it is another player or has NPC flags. `PlayerGUID` = the model's name cache. `EnterAreaTrigger` = the session's own portal packet. |
| `Walker` | `dungeonWalker` | `Path` = the session's navmesh planner on the walking filter (built on first use, dropped on a map change). `Walk`/`Stop` = the session's mover, the one implementation that keeps the movement packets anticheat-clean. A transfer (`objects.ErrMapChange`) or a teleport mid-leg is the driver's `ErrMapChange`; a dead link is `ErrLinkLost`. |
| `Clock` | `dungeonClock` | `Now` is the session's client clock; `Sleep` answers server packets for the interval (`session.collect`), so a poll interval never starves the loop. A dead link ends the wait at once and is left to the next port call (the wait itself sends nothing). |

### The setup actions

Everything the run does outside the orders is an `Action` from the driver's closed vocabulary. `Run.act` is the one
way into it (as `Run.send` is the orders'): it classifies the kind and applies the class's gate in the same step as
the send, and only a parsed success reaches the run. After the gate, both vocabularies end in the same `Run.sendLine`,
the one function every non-read-only line leaves through: it records the order in the run's log, sends through the
port, counts the send for the freshness gates and ends the run on a lost link. Moving actions need a complete state report taken immediately
before the send and not older than the last line sent (the driver is sequential, so that is the report after it), no
stop pending, and — unless the action is the wipe's own recovery — nobody in combat and nobody dead. Revives are the
practice path only and are counted as PRACTICE; the instance reset is asked only once the run has reached its end and
a fresh report shows the whole party outside, because the module refuses it from inside.

| Action | Class | Gate | Line | Reply (typed) |
|---|---|---|---|---|
| `ActionGMOn` | state, in place | stop/link only | `.gm on` (core, tokenless) | the core's answer; a refusal is the collector's named error |
| `ActionGMVisible` | state, in place | stop/link only | `.gm visible on` (core) | as above |
| `ActionTeleOutside` | moving | fresh report + no stop + nobody dead, nobody in combat | `.tele <outside_tele>` (core) | silent; a refusal is the named error, the world confirms the move |
| `ActionTeleCommander` | moving | as above | `.tb tele <who> <map> <x> <y> <z>` | `tele-end count=0 #token` |
| `ActionTeleParty` | moving | as above | `.tb tele party <map> <x> <y> <z>` | `tele-end count=0 #token` |
| `ActionTeleTo` | moving | as above; the wipe recovery is exempt from the combat/dead rule, never from the report rule | `.tb tele <who> to <other>` | `tele-end count=0 #token` |
| `ActionAppear` | moving | as above (the fallback when the module refuses the teleport to the tank) | `.appear <name>` (core) | silent; the world confirms the move |
| `ActionEnter` | moving | as above, and only while the party is not yet in one instance | `.tb enter party` | `enter-end count=0 #token` |
| `ActionRevive` | state, in place | practice switch on, budget left, fresh report, a dead target (party: no target check) | `.tb revive <who>` | `revive-end count=0 #token` |
| `ActionInstanceLeave` | moving | as above | `.tb instance <leader> leave` | `instance-end count=0 #token` |
| `ActionInstanceReset` | state, in place | the run has ended + a fresh report of the whole party outside | `.tb instance <leader> reset` | `instance-end count=0 #token` |
| `ActionInstanceInfo` | read-only | none | `.tb instance <leader> info` | the typed boss report (the run does not read it yet; the supervisor will) |

No setup action starts a fight; the orders own that and their own permit. Party and role setup are not actions:
PREPARE reads the roster (the port's `Party`, read-only) and refuses a wrong party or role instead of changing it, so
the driver never forms or re-roles a party. A module refusal is the `tbcmd` `*CommandError` (its `code`); the run
records the action failed and stops with the state's reason (`enter:error`, `travel:error`, …), and the action log's
`action_error` event carries the code. The GM lines and the two core teleports carry no token — the core does not
echo one — so their parse is the collector's refusal grammar; the move itself is confirmed by the world model
(position, map), not by a reply.

Port errors are translated at this boundary. The driver's `ErrLinkLost` means "nothing may be sent again"; a module
refusal, an incomplete report or a bad reply is returned as it came. A read or an order reads it as *unknown* (it
holds and polls again); an action's refusal is the state's own stop (above).

The GM guard is the A6 one: the run sends the non-mutating probe first and sends no other command until it was
acknowledged. The driver's own PREPARE then sends the GM mode lines.

## New parsed records

The typed layer read strictly before this round; the shapes below were added there, with tests, and a module that
does not send the newer keys keeps working.

* **Threat line, `pos=x,y,z`** — optional. `HasPos` is false when the key is absent, so "not known" is never the
  origin. Negative numbers and decimals are fine (they are inside the grammar's literal set or percent-encoded).
* **Threat line, `flee=yes|no`** — optional (`HasFlee`); a module without it is read exactly as before.
* **`.tb instance <leader> info`** — a token-marked report of records that carry `boss=<entry>:<alive|dead>` lists
  (`;` between bosses). One boss per record is read the same way, and the end line's own `bosses=` list is accepted
  too, so the caller does not depend on which of the two shapes the module writes. Duplicate entries, an unknown
  state word, a missing entry and a reply of another command are refused (`ErrBadValue`/`ErrBadReport`).
* **State, slice-4 keys** — `life`, `relhold`, `inleader`, `block` and the end line's `alive`/`dead`/`wipe`/`whole`
  stay in `Extra`/`End`; a test pins them. The driver already reads `life`/`block` from `Extra`.

## The command

```
forever-agent forever dungeon-run --plan FILE --party TANK,HEALER,DPS,DPS,DPS --character NAME
    [--rest host:port --rpc host:port --ca FILE --account EMAIL] [--enter tele|portal|none]
    [--from-leg N] [--legs a,b] [--reset] [--max-minutes N] [--max-wipes N] [--practice=true]
    [--out DIR] [--navmesh DIR] [--dry]
```

`--dry` is unchanged: the whole machine over the scripted world. The live mode takes the same connection flags as
`world-check`, loads and refuses the plan **before** the login (exit 2, no session), enters the world, runs, prints
the order list and the counters, and logs out. `--enter none` is the open-world mode: the party and the commander are
already placed and the machine starts at the first leg. Exit codes: 0 clear, 2 usage/plan refusal, 7 link lost, 5 a
run that stopped (any other stop reason). The session's deadline is the run's budget plus the stop path's own bound.

`testdata/open_world_northshire.json` is a three-pack map-0 layout for `--enter none`, built from public
starting-area data this repository already uses in its login fixture and its navmesh test; it is a test layout, not
a mapped route, and its notes say so. The leg's end is the entrance so the smoke test starts on the first pack
instead of walking a line through a later one.

## Tests

No-socket adapter tests (`dungeon_session_test.go`): the world adapter's `Self`, `Units` (entry, health, position,
dead, friendly), `PlayerGUID` and `EnterAreaTrigger`; `Path` without a navmesh folder; `Walk`/`Stop` through the
mover and the `ErrLinkLost`/`ErrMapChange` translations; the clock answering a server time-sync request during
`Sleep` (the proof that the wait is the session's packet loop), and returning at once on a dead link.

Action and gate tests: `tbcmd/action_test.go` (every setup command's line, its end marker, its refusal and the
argument checks), `driver/actions_test.go` (every kind classified; `Act` called only inside `act`; each moving
action's gate passing and shut by a stop, a stale report, a fight and a dead member; the revive's practice switch and
budget; the reset's end/outside rule; unknown kinds and destinations refused before the send; the wipe-recovery
report sequence) and `world/dungeon_action_test.go` + `world/dungeon_send_test.go` (the wire scenarios in the table
above, and the source test that the adapter's only call of the command session is its send and that no `.tb` line is
built in the package).

Fake-server tests (`dungeon_fake_test.go`, `dungeon_walk_cgo_test.go`): a scene that answers `.gm`, the GM mode
lines, the party roster, the state report, the threat report and the orders over real sockets, with the character's
own create block and one creature in the pack.

| Scenario | What it pins |
|---|---|
| clean run | PREPARE → POSITION → PULL → FIGHT → REST → clear; one pull; the trace and summary are written; **every `.tb order` line on the wire is exactly one of the run's recorded orders** (the one send function is the only source) |
| late state report | the first state reply arrives past the command deadline; the run does not act on it and the next complete report carries it on to a clear run |
| refused order | the module refuses the pull every time; one retry, then stop `pull-refused:*` |
| link death per port | drop on `.gm on` (a core action), `.tb party`, `.tb state`, `.tb threat`, `.tb order`, `.tb enter` (the entry action) and on the first movement packet (walker): each run ends with exit 7 and stop `link-lost`, and no line follows |
| actions on the wire | a portal plan: `.tb tele Lead …`, `.tb tele party …`, `.tb enter party` and `.tb tele Lead to Tank`, in the driver's order, on the way to a clear; the appear is the fallback after a refused teleport to the tank |
| wipe recovery | the pull's party stays down and the mob stays on the threat list: `.tb revive party`, then one `.tb tele <bot> to Lead` per bot, each with a state report between it and the line before it — never on the wipe's report |
| instance reset | `--reset`: the leave line, a report of the party outside, then the reset; a leave that keeps the party inside stops the run without a reset line |
| refused entry | the module refuses `.tb enter party`: the run stops `enter:error`, nothing follows the refusal |
| shut moving gate | a dead member and a fight shut the travel teleports: no `.tb tele` line on the wire and the state ends on its timeout |

The walker case needs the Detour engine, so it carries `//go:build cgo`; the adapter's translation is also pinned
without sockets.

The command tests cover the dry run, the open-world plan, a refused plan (exit 2 before any network) and the live
mode's argument checks (missing login flags, missing character, bad `--enter`, bad budget, four names).

One driver fix came out of the walker case: `walkTo` and `retreatToward` treated any walk error as `walk`. A walk
that returns the port's `ErrLinkLost` now ends the run as `link-lost`, as the port's own contract says.

## What the live half must do first

The first live attempt is not a full clear; it is four checks, in this order, each one a line of output and a
decision before the next:

1. **A GM commander the bots follow.** The commander inside the instance with the party, GM mode visible, after one
   `.tb follow` — do the bots follow it, or does the client's visibility rule hide it from them? If they follow, the
   run's movement orders are meaningful; if not, the run must be driven from the tank's position instead.
2. **The first pack.** One pull with the party placed by the console (party placed by hand, `--enter none`), 20-25
   yd from the stand, and the state/threat lines logged against the commander's view every 2 s. This is where the
   pull distance, the stand and the pack's real radius meet.
3. **Fight-end detection.** The same pull to its end, logging the threat count against the living bots' combat flag
   and the units in view. A fleeing mob, a mob stuck in the terrain or an evading mob must not read as "fight over"
   while the party is still in combat.
4. **Poll cost.** `.tb perf` during that fight: a state report and a threat report every 2 s is about 2 KB a poll;
   the server log's noise and the module's own load decide whether the fight budget can stay at 2 s.

Only then the ordered Redridge-style open-world pulls with a rest between, and last the first Ragefire pull with the
party placed by the console. Everything before the first pack is cheap to repeat; the pack is the round's real
measurement.

Until the first pack runs, treat this round as "adapter + command + grammar, proven on the fake server only". The
open-world plan is a smoke test, not a route: its coordinates are the public starting-area points the repository
already uses, and the packs are public low-level creatures of that area placed on them.
