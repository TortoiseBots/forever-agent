# Joint watch fixes: what the third watched run found

Scope: the third joint live run (this client watching a run a server module leads) cleared the dungeon, but its first
three attempts stopped before the module's run started. All three were around the party's commander — a bot the
module's roster names, outside the party — and the party's own teleport. Each is fixed with a test that fails on the
code before it. No character, account, host or path of the test environment is named here; the module is "the server
module" throughout.

## 1. `--enter tele` ended `start:stuck`: the commander stayed on the outer map

The party entered, and the module's own state read named the commander on the outer map: the tank stood
`stuck=blocked:bot in an instance` with `dm=othermap` and `ready=no`. The watch moved the *observer* after the party
landed, never the party's commander, and the start's readiness rule then read the block and the watch ended
`start:stuck`.

* `internal/forever/driver/states.go` — `commanderToTank` brings the party's commander (the roster's, never this
  client's character) to the tank's side: the module's own `.tb tele <commander> to <tank>` and nothing else, and then
  a bounded wait (30 s) for a report read after the move to show the commander with the tank and the block its absence
  caused gone. The placement moves no one else — the core's `appear` moves the watching character, not the commander —
  and a roster that names no commander ends the watch (`commander:unnamed`) instead of falling back to it. The ENTER
  state calls it once the party is in.
* `internal/forever/driver/states.go` — `commanderAway` is the evidence: the tank's own `dm=` (the distance to its
  master, the commander), othermap or farther than a master's follow distance (20 yd).
* `internal/forever/driver/run.go` — `Run.commander` keeps the roster's commander, so ENTER still names it after
  PREPARE read the roster.
* Test: `TestWatchCommanderFollowsATeleEntry` (before: `start:stuck`), which also pins the order — the commander's
  teleport follows the party's entry — and the two log lines (where the commander stood, and that it is at the tank's
  side).

## 2. `--enter none` with the commander outside: `prepare:stuck`

With the party already placed, PREPARE reported `not ready: stuck:<tank>` for the same block and ended
`prepare:stuck`: the commander stood on the outer map and nothing moved it.

* `internal/forever/driver/watch.go` and `internal/forever/driver/states.go` — the watch reads the module's roster for
  the commander's name and brings it to the tank's side before PREPARE; PREPARE reads the roster for its own rules and
  retries the move with the GM lines sent, before its readiness rule reads the block.
* Test: `TestWatchCommanderComesToTheTankBeforePrepare` (before: `prepare:stuck`), which checks that the teleport
  precedes PREPARE's first GM line, and that the log says where the commander stood and when it arrived.

## 3. A state read during the party's teleport ended the start

The first state read after the entry showed a member still on the outer map with its engine off (a teleport in
flight). The start's gate refused the party as not on the plan's map and ended the watch at once, before the party had
landed.

* `internal/forever/driver/actions.go` — the start's `party-map` is a hold reason: a watch's start waits for every
  member on the plan's map, up to the entry budget (90 s), and reports the existing error (`start:party-map`) at the
  bound. The engine and readiness checks only ever run on a report that put the whole party on the plan's map, so a
  member in flight is never read as `noengine`.
* Tests: `TestWatchStartWaitsForThePartysTeleport` (before: `start:party-map`) and
  `TestWatchStartTeleportThatNeverLands` (the bound and the unchanged error).

## Tests the change moved

* `TestWatchObserverOutsideTheInstance` keeps this client's character on the outer map: the commander is with the
  party, so no move is asked for, and PREPARE still compares the party's map with the plan's.
* `TestWatchReadyUsesTheLeaderDistance` now refuses the commander's teleport, so its `dm=othermap` stands and PREPARE
  still measures a watched party by its leader's distance (`dl=`).
* `TestWatchStartRechecksThePlacement` counts the commander's placement read before PREPARE's and the start gate's: the
  start still retries, the party still zones out during the retry, and no run starts off the plan's map.

## The placement moves the commander or no one (review fix)

The first cut fell back to the core's `appear` when the module refused `.tb tele`: `appear` acts on this client's own
character, so a refused teleport would have teleported the watching GM to the tank, read as a successful commander
move, and been retried while the module's report kept showing the commander away.

* `internal/forever/driver/states.go` — `moveCommander` sends the module's teleport only; a refusal is the placement's
  answer (logged once) and never a move of anyone else.
* `internal/forever/driver/states.go` — `commanderToTank` stops the watch on `commander:unnamed` when the roster names
  no commander, and `readCommander` reports a roster that arrived without a name instead of dropping it. Nothing is
  sent in the watching character's place.
* Tests: `TestWatchCommanderMoveRefusedKeepsTheExistingError` (before: an `appear` was sent and the observer moved off
  its map) refuses only the teleport, with `appear` accepted, and asserts no `appear` action, the observer's position
  and map unchanged, the logged refusal and the unchanged `prepare:stuck`; `TestWatchCommanderUnnamedFailsTheWatch`
  (before: `done`) blanks the roster's commander and asserts `commander:unnamed` with no action sent but the end's
  instance read.
