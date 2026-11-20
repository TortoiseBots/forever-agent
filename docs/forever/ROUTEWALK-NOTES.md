# Forever route-walk notes

Scope: walk a whole dungeon plan on the server's own navmesh, live, and check every planned point against a
confirmed position. Three existing pieces are reused, not rebuilt: `forever path-check`'s v16 planner
(`internal/navmesh`, [NAVMESH-R1-NOTES.md](NAVMESH-R1-NOTES.md)), `forever walk-check`'s real movement
(`objects.Mover`, [OBJECTS-R1-NOTES.md](OBJECTS-R1-NOTES.md)), and the GM command channel
([COMMAND-R1-NOTES.md](COMMAND-R1-NOTES.md)). New code: `cmd/forever-agent/forever_route_walk.go` (the command),
`internal/forever/world/route_walk.go` (the session side), tests in `fake_route_walk_test.go` and
`forever_route_walk_test.go`.

## The command

```
forever-agent forever route-walk --plan FILE [--legs a-b|NAME,...] [--gm] --navmesh DIR [--parent MAP=PARENT[,..]]
    [--enter tele|none] [--leave tele|none] [--confirm gps|client] [--out DIR] [--json]
    [--max-deviation YD] [--stuck-after DURATION] [--stuck-step YD] [--fall-drop YD] [--sample DURATION]
    [--liquid-above YD] [--liquid-below YD] [--settle DURATION] [--timeout DURATION]
    --rest HOST:PORT --rpc HOST:PORT --ca FILE --account EMAIL --character NAME
```

* `--plan` is a version-1 plan (`dungeons/*.json`); the command loads it with `dungeon.Load`, so a plan this build
  refuses costs no session.
* `--legs` takes 1-based numbers and ranges and plan leg names (`1`, `2-4`, `tunnel,boss`); the selection keeps the
  plan's order. Empty walks every leg.
* `--navmesh` is a Forever mmaps folder (v16); `--parent MAP=PARENT` is the server's instance rule
  (`MMapManager.cpp:349-357`).
* `--gm` sends `.gm on` after the GM guard and `.gm off` before the logout. It is also what lets the entry teleport
  skip the dungeon's access rules (see below).
* `--enter tele` (default) sends `.tele <plan.entrance_tele>` when the client is not already on the plan's map;
  `--enter none` expects the character to be placed and refuses to move it.
* `--leave tele` (default) sends `.tele <plan.outside_tele>` after the last leg when the character is still inside;
  `--leave none` stays where the walk ended. The leave runs **before** `.gm off`: clearing the GM flag among a
  dungeon's packs aggros them at once, and the server then refuses the logout (result 1, in combat) — seen live on
  a run without the leave.
* `--out DIR` writes `route-walk.json` there; `--json` prints the same report instead of the table.
* Exit codes: 0 when every leg passed, `12` when a leg did not (the report still runs every leg), `5` for a session
  error (link lost, no create block, a refused command, the run's deadline or a cancel), `2` for a usage error. A
  run that ends on an error prints its report with `INCOMPLETE`, never `PASS`.

## What a leg is

A leg's route is the plan's own walk: each `via` waypoint in order, then the leg's `to`. `pull_groups`, `steps` and
the boss look are not walked: route-walk tests the corridor the plan says the party walks, not the fights.

* A plain waypoint is planned with `navmesh.Planner.PathTo` (the Forever walk filter: ground, steep, water, road;
  magma and slime excluded) from the position the previous hop actually ended at.
* A `direct` waypoint is the plan's straight step over a gap the mesh does not link; route-walk walks the straight
  line, as the plan's driver does, and says so in the log.
* A `gm_cross` waypoint is a point a GM account moves the party to. route-walk does not teleport mid-plan: the leg
  stops there with `NOPATH` and the reason.
* The plan's `avoid` boxes are given to the planner (`dungeon.NavBoxes`), as the plan check does, so a route does
  not plan through the ground the plan calls fatal (the lava basin and the shelf).

Between legs the next plan starts from the **confirmed position the last leg ended at**, never from the plan's own
point. The report shows both (`leg.from`, and the previous leg's last waypoint).

Every check is against the **plan's own point**, not the point the planner snapped it to. A hop's last stop is
compared with the hop's planned `via`/`to`; the distance between that plan point and the mesh point the walk
actually aimed at is the *snap* (`snap_yd` on the waypoint, `max_snap_yd` on the leg, and a `max snap` column in
the table). A snap beyond `--max-deviation` is `DEVIATED` even when the server confirms the mesh point exactly:
the walk cannot claim a waypoint it never reached. The waypoints between two plan points have no plan reference
and are compared with the walked path point, as before.

## The walk

Each planned polyline point is walked with the client's real movement: `CMSG_MOVE_SET_FACING`,
`CMSG_MOVE_START_FORWARD`, a `CMSG_MOVE_HEARTBEAT` every 500 ms, `CMSG_MOVE_STOP` at the target
(`objects.Mover.MoveTo`; the packet rules are [OBJECTS-R1-NOTES.md](OBJECTS-R1-NOTES.md)). A hop longer than
`--sample` (default 3 s of running, about 21 yd at 7 yd/s) is split at that interval: the character stops, the
position is confirmed, and the walk goes on. That bounds how long a stall can go unseen to about `--stuck-after`.

The waypoint table is one row per such stop: the planned point, the confirmed position, their 3-D distance (to the
plan point on a hop's last stop, to the path point between them), the Z difference, the time, a liquid name if the
mesh has one there, the snap distance when the stop ends a hop, and a note naming the snap.

## Confirmed positions: two sources

`--confirm gps` (default) is the server's own answer:

* `CMSG_SET_SELECTION` selects this character (the core reports the selected unit; `MiscHandler.cpp:400-402`,
  `Player::SetSelectedUnit` is a field write).
* `.gps` (`cs_misc.cpp:185-307`) answers with the map line, the coordinate line
  (`X: <f> Y: <f> Z: <f> Orientation: <f>`) and the grid line, read through the command collector
  (`internal/forever/command`) with a 500 ms quiet window and a 4 s deadline. The block is read strictly: exactly
  one map line, before exactly one coordinates line, with the grid line (`grid[...]`) after them. The grid line's
  own `(Map: 1 VMap: 1)` is the *tile's* map and is not a second block.
* The core's `.gps` is gated by `RBAC_PERM_COMMAND_GPS` (505). An account without it gets the hidden-command answer,
  `Command 'gps' does not exist` (`ChatCommand.cpp:472-478`, `Chat.cpp:166-179`), and route-walk stops with a clear
  error instead of pretending the client's belief is the server's.
* **Every request is marked.** The command is tokenless, so a reply to an earlier `.gps` could otherwise answer the
  next one. Before each read the session whispers a fresh marker (`route-gps <token>`) to its own character and
  pumps until the server echoes it: the core handles one session's chat messages in order and writes every answer to
  the same stream, so when the echo is seen, nothing older is left, and the `.gps` sent after it is the first
  request whose answer the stream carries. No echo, or a block that is not the marked request's, is not a
  confirmation. A reply that ends at the collector's deadline or line cap, a block missing a line, or a reply
  holding two blocks is not a confirmation either.
* **A stop needs two agreeing post-request reads.** One read is never enough, however close its position is to the
  stop: a block the server wrote for an earlier command can still arrive after the new request. The confirmation is
  the second read, accepted only when it agrees with the first within 0.5 yd (a real position — the stop, a
  deviation or a correction — reproduces; a late block does not) on the same map. Two reads that disagree are
  `UNCONFIRMED`: nothing is recorded as the server's answer. That doubles the reads of a leg (and so roughly doubles
  the quiet windows): about a second per waypoint at the 500 ms default.

The marker is a whisper to the character itself, which is a real client's own line and stays private to the
session's stream (the same transport the core's solo trade add-on uses); it changes no world state and prints no
nearby chat.

`--confirm client` records the position of the last movement packet the client sent (the mover's own belief). It is
what a server whose account cannot run `.gps` leaves. In that mode:

* `server_confirmed` is false on every waypoint and the report says `"confirmed": "client"`;
* a **server correction** is still seen: a teleport or a create block that moves the character replaces the mover's
  position (`ErrTeleported`; `applyUpdate`), the leg records `DEVIATED` with the server's position, and the fall
  check compares against that;
* the **stall test is not run**: the client's belief advances by construction, so a server that silently ignores the
  movement packets (a root, a stun, a transfer) is invisible in this mode. The table's STUCK verdict can only come
  from `--confirm gps`.

## Verdicts and thresholds

| flag | default | meaning |
|---|---|---|
| `--max-deviation` | 2.0 yd | a waypoint farther than this from the plan point (or a snap farther than this) makes the leg DEVIATED |
| `--stuck-after` | 6 s | the confirmed position moved less than `--stuck-step` for this long: STUCK at that position |
| `--stuck-step` | 0.5 yd | progress that counts as movement |
| `--fall-drop` | 2.0 yd | the confirmed Z this far below the planned point: FELL |
| `--sample` | 3 s | longest uninterrupted run before the position is confirmed |
| `--liquid-above` / `--liquid-below` | 2.0 / 0.5 yd | the band round a planned point in which a magma/slime navmesh floor fails the leg (LIQUID) |

Verdicts, most serious last: `PASS`, `DEVIATED`, `UNCONFIRMED` (the server did not confirm the stop: no complete
block, two reads that disagree, or an answer that cannot be tied to the stop), `NOPATH` (no complete route; or a
`gm_cross` hop), `STUCK`, `FELL`, `LIQUID`, `INCOMPLETE` (the deadline or a cancel ended the leg or the run before
its work was done), `FAIL` (a planning or session error). A leg keeps its first failure as `status` and lists every
point that failed in `issues`; a leg marked DEVIATED keeps walking (the deviation is a fact, not a wall), an
UNCONFIRMED, STUCK, FELL or LIQUID leg stops there, and an INCOMPLETE leg or run is never `PASS` whatever it had
confirmed so far.

The liquid check reads the mesh before the character walks: at every planned point, `Planner.Floors` is asked for the
polygons in `[z - liquid-below, z + liquid-above]`, and a polygon whose flags carry `NavMagma|NavSlime` (Forever's
one 0x08 bit for both, `MMapDefines.h:58-80`) fails the leg at that point. This catches both a bed flagged as
damaging liquid and a liquid surface above a ground the generator called walkable. The check is run again at every
confirmed position, so a teleport into lava is a `LIQUID` finding too.

## GM mode: what it changes, and what it does not

Read from the core; the movement packets themselves are handled the same way with GM mode on or off
(`WorldSession::HandleMovementOpcode`, `MovementHandler.cpp:487-660`, has no GM branch; `ValidateMovementInfo`,
`:49-150`, treats GMs specially only for flying, `:122-132`).

* **Aggro.** `Player::SetGameMaster(true)` sets `PLAYER_FLAGS_GM`, restores the faction to friendly and stops combat
  (`Player.cpp:2093-2112`). `Unit::IsAttackable`/`Object::IsAttackable` then answers false for the character
  (`Object.cpp:2396-2412`, "can't attack GMs"), and creature aggro needs an attackable target, so no creature
  starts a fight. This is why the walk can run through a dungeon's packs.
* **Fall damage.** `Player::HandleFall` returns without damage for a GM (`Player.cpp:29161`: the condition includes
  `!IsGameMaster()`), so a fall during the walk is a route finding, not a death.
* **The dungeon gate.** `Player::Satisfy` skips every access requirement for a GM (`Player.cpp:21380-21386`), so
  `.tele` into an instance works below the plan's level band. Instances of a GM are also always valid
  (`Player::CheckInstanceValidity`, `Player.cpp:25373-25377`) and a GM does not create an instance lock
  (`Player::ConfirmPendingBind`, `Player.cpp:21343-21346`).
* **Visibility.** GM mode sets the server-side visibility to `SERVERSIDE_VISIBILITY_GM` at the session's security
  (`Player.cpp:2111`), which is what keeps other players from seeing the character; `.gm visible` is not sent.
* **Not changed:** the server still stores every movement packet's position, still relays it to other players
  (never to the mover itself: `MessageDistDeliverer::SendPacket`, `GridNotifiers.h:197-207`), and still applies its
  under-map check (`MovementHandler.cpp:640-666`). A GM walked below the map is put at the graveyard like anyone.

## Tests

`go test ./internal/forever/world -run TestRouteWalk` runs the whole command against the fake world server
(`fake_route_walk_test.go`): a real login, encryption, the character's create block, a navmesh fixture and a
scripted server that answers the GM guard and stores every movement packet's position:

| test | server behaviour | expected |
|---|---|---|
| `TestRouteWalkAgainstFakeServer` | stores every move, answers `.gps` with it | both legs PASS, every waypoint within 0.05 yd, leg 2 starts where leg 1 ended |
| `TestRouteWalkStuck` | stores two moves, then keeps the position | STUCK at the frozen position, reason names the stall |
| `TestRouteWalkRejectsMoves` | stores no move at all (a root or a transfer) | STUCK, every confirmed position is the start |
| `TestRouteWalkFalls` | answers `.gps` 2.5 yd lower | FELL, `dz` about -2.5 |
| `TestRouteWalkDeviates` | answers `.gps` 4 yd east | DEVIATED, max deviation about 4 |
| `TestRouteWalkLiquid` | fixture mesh with a magma polygon over the corridor | LIQUID before any movement packet, reason names magma/slime |
| `TestRouteWalkSelectsItself` | checks the packed GUID of every `CMSG_SET_SELECTION` | one select per map, always this character |
| `TestRouteWalkGPShidden` | answers `.gps` with the hidden-command refusal | the run stops at once and names `--confirm client` |
| `TestRouteWalkClientConfirmation` | store-and-answer server, `--confirm client` | PASS with no `.gps` sent, every waypoint `server_confirmed: false` |
| `TestParseRouteGPS` | the core's line shapes | coordinates parse, the grid line does not |

The CLI tests (`cmd/forever-agent/forever_route_walk_test.go`) cover the leg grammar, the table, the JSON file and
every validation that happens before a login.

## The live runs

### Client-confirmed (round 1)

One full run of `dungeons/ragefire_chasm.json` (map 389, 9 legs) on the round's test server, a GM character, GM
mode on, `--enter tele` (entered by `.tele` to the plan's `entrance_tele`), `--confirm client`, 2m55s:

| leg | name | status | points | planned | walked | max dev |
|---|---|---|---|---|---|---|
| 1 | tunnel | PASS | 3 | 7.5 | 7.7 | 0.43 |
| 2 | tunnel_end | PASS | 29 | 105.9 | 105.9 | 0.02 |
| 3 | trogg_cave | PASS | 16 | 61.2 | 61.2 | 0.00 |
| 4 | oggleflint | PASS | 60 | 230.8 | 230.8 | 0.00 |
| 5 | trogg_west | PASS | 29 | 98.7 | 98.4 | 0.30 |
| 6 | molten_cave | PASS | 65 | 240.3 | 240.1 | 0.31 |
| 7 | taragaman | PASS | 34 | 107.8 | 107.8 | 0.38 |
| 8 | jergosh | PASS | 43 | 149.8 | 149.8 | 0.23 |
| 9 | bazzalan | PASS | 37 | 130.9 | 130.9 | 0.37 |

Verdict PASS: 1133.0 yd planned, 1132.5 yd walked, every leg planned complete on the first try, no NOPATH, STUCK,
FELL or LIQUID. The largest deviation is 0.43 yd on the entry leg (the first stop is within the mover's 0.5-yd stop
tolerance); the rest is under 0.4 yd, which is the densified route's own stop spacing. No `SMSG_MOVE_TELEPORT`
arrived during the walk (the only one is the entry's transfer), so the server never corrected a position.

The same command on `dungeons/wailing_caverns.json` (map 43, 13 legs), entered and left by `.tele`, GM mode on,
`--confirm client`, 8m47s:

| leg | name | status | points | planned | walked | max dev |
|---|---|---|---|---|---|---|
| 1 | anacondra_a | PASS | 18 | 57.9 | 58.0 | 0.34 |
| 2 | anacondra_b | PASS | 63 | 221.5 | 221.4 | 0.28 |
| 3 | cobrahn_a | PASS | 65 | 227.3 | 227.3 | 0.30 |
| 4 | kresh | PASS | 50 | 177.0 | 177.0 | 0.36 |
| 5 | cobrahn_b | PASS | 30 | 102.4 | 102.3 | 0.49 |
| 6 | cobrahn_c | PASS | 115 | 402.2 | 402.1 | 0.22 |
| 7 | pythas_a | PASS | 158 | 558.9 | 558.8 | 0.48 |
| 8 | pythas_b | PASS | 105 | 380.2 | 380.1 | 0.40 |
| 9 | pythas_c | PASS | 95 | 341.2 | 341.2 | 0.49 |
| 10 | skum | PASS | 70 | 265.5 | 265.2 | 0.27 |
| 11 | west_column | PASS | 88 | 328.3 | 328.3 | 0.16 |
| 12 | serpentis | PASS | 98 | 356.9 | 356.9 | 0.27 |
| 13 | verdan | PASS | 45 | 163.7 | 163.7 | 0.06 |

Verdict PASS: 3583.0 yd planned, 3582.3 yd walked, every leg planned complete, no NOPATH, STUCK, FELL or LIQUID.
The plan's one `direct` waypoint (the serpentis leg's drop across a gap the mesh does not link) was walked as its
straight line: 9.31 yd 3-D from the foot's mesh point, 5.4 yd of it the drop.

Both runs left the instance with `.tele` to the plan's `outside_tele` and logged out with result 0 (instant). A WC
run of the code before the leave step existed was refused the logout with result 1: three creatures aggroed in the
second between `.gm off` and the logout request. That is why `--leave tele` is the default.

Server-side spot check of the no-correction source: while the Wailing Caverns run was walking, one console `saveall`
wrote the character's row on the instance map; the run's nearest confirmed waypoint at that time (by the waypoint's
own timestamp) was the same position within 0.5 yd (one heartbeat's travel at the run speed).

### Server-confirmed (round 2)

Round 2 made the server the source of every confirmed position. The round's GM test account was missing the core's
GPS permission, so its auth database gained one row: `rbac_account_permissions` with the account, permission 505
(`RBAC_PERM_COMMAND_GPS`), `granted = 1` and `realmId = -1` (all realms; the loader reads
`realmId = ? OR realmId = -1`, `LoginDatabase.cpp:105`). Permission 505 has no linked permissions
(`rbac_linked_permissions` has no row for it), so the grant adds `.gps` and nothing else. Nothing was restarted or
reloaded: a session loads its permission set at login (`WorldSession::LoadPermissions`, `WorldSession.cpp:1348-1360`,
called at `:1605`), and a probe that logs in as that account and sends only `.gps` answered with the eight-line
position block before the walks.

`--confirm gps` on `dungeons/ragefire_chasm.json`, same entry, same GM mode, 5m50s (the `.gps` round trip adds about
0.55 s per stop; the client-confirmed run above took 2m55s). The two tables below are those runs recomputed from their
saved `route-walk.json` under the plan-point measure (round 1's numbers were measured against the snapped endpoint);
no new live run was needed, and the recomputation changes no verdict.

| leg | name | status | points | planned | walked | max dev | max snap |
|---|---|---|---|---|---|---|---|
| 1 | tunnel | PASS | 3 | 7.5 | 7.7 | 0.43 | 0.20 |
| 2 | tunnel_end | PASS | 29 | 105.9 | 105.9 | 0.62 | 0.62 |
| 3 | trogg_cave | PASS | 16 | 61.2 | 61.2 | 0.03 | 0.03 |
| 4 | oggleflint | PASS | 60 | 230.8 | 230.8 | 0.21 | 0.21 |
| 5 | trogg_west | PASS | 29 | 98.7 | 98.4 | 0.30 | 0.03 |
| 6 | molten_cave | PASS | 65 | 240.3 | 240.1 | 0.31 | 0.28 |
| 7 | taragaman | PASS | 34 | 107.8 | 107.8 | 0.38 | 0.20 |
| 8 | jergosh | PASS | 43 | 149.8 | 149.8 | 0.23 | 0.11 |
| 9 | bazzalan | PASS | 37 | 130.9 | 130.9 | 0.37 | 0.05 |

Verdict PASS: 1133.0 yd planned, 1132.5 yd confirmed, all 316 waypoints `server_confirmed`, every leg planned
complete, no DEVIATED, UNCONFIRMED, NOPATH, STUCK, FELL or LIQUID. The largest plan-point deviation is 0.62 yd and
the largest snap 0.62 yd, both far inside the 2-yd tolerance. No `SMSG_MOVE_TELEPORT` arrived during the walk, so the
server never corrected a position.

`--confirm gps` on `dungeons/wailing_caverns.json`, 17m54s, recomputed the same way:

| leg | name | status | points | planned | walked | max dev | max snap |
|---|---|---|---|---|---|---|---|
| 1 | anacondra_a | PASS | 18 | 57.9 | 58.0 | 0.34 | 0.19 |
| 2 | anacondra_b | PASS | 63 | 221.5 | 221.4 | 0.28 | 0.20 |
| 3 | cobrahn_a | PASS | 65 | 227.3 | 227.3 | 0.30 | 0.09 |
| 4 | kresh | PASS | 50 | 177.0 | 177.0 | 0.36 | 0.14 |
| 5 | cobrahn_b | PASS | 30 | 102.4 | 102.3 | 0.74 | 0.73 |
| 6 | cobrahn_c | PASS | 115 | 402.2 | 402.1 | 0.27 | 0.27 |
| 7 | pythas_a | PASS | 158 | 558.9 | 558.8 | 0.48 | 0.21 |
| 8 | pythas_b | PASS | 105 | 380.2 | 380.1 | 0.40 | 0.16 |
| 9 | pythas_c | PASS | 95 | 341.2 | 341.2 | 0.49 | 0.08 |
| 10 | skum | PASS | 70 | 265.5 | 265.2 | 0.34 | 0.21 |
| 11 | west_column | PASS | 88 | 328.3 | 328.3 | 0.16 | 0.05 |
| 12 | serpentis | PASS | 98 | 356.9 | 356.9 | 0.27 | 0.13 |
| 13 | verdan | PASS | 45 | 163.7 | 163.7 | 0.38 | 0.38 |

Verdict PASS: 3583.0 yd planned, 3582.3 yd confirmed, all 1000 waypoints `server_confirmed`, every leg planned
complete, no DEVIATED, UNCONFIRMED, NOPATH, STUCK, FELL or LIQUID. The largest plan-point deviation is 0.74 yd and
the largest snap 0.73 yd, both inside the 2-yd tolerance. The serpentis leg's `direct` hop was walked as its straight
line again (9.31 yd 3-D). The liquid check ran against every confirmed position and found no magma or slime polygon
at any of them.

The recomputation changes a leg's deviation only where a hop's plan point sits off the mesh point the walk reached:
the new number is the distance to the plan point, which includes that snap. Every value stays under the 2-yd
threshold; no leg's verdict, distance or point count changes. A live rerun is unnecessary for the measure; it would
only re-measure the same walk.

The round-1 tables above keep their numbers as measured then (against the snapped endpoint). The positions
themselves are the same in both rounds — the server stores every movement packet it receives, and `.gps` returns that
stored position — so the difference between the tables is the measure, not the server's answers. The equality of the
two sources is also why the mode is not independent evidence of the route; what it adds is that a correction (a
teleport), a silent refusal (a stall), a delayed answer or a server-side liquid would have shown, and none did.
Every run left the instance with `.tele` to the plan's `outside_tele` before `.gm off`, and logged out with result 0.

The round-2 walks themselves ran with the earlier, single `.gps` read. The fix rounds' stricter read — the marker
barrier (a self-whisper echoed before each request) and the two agreeing post-request reads — is exercised by the
fake server's late-block, ambiguous and missing-reply cases, not live: the answer values are unchanged, but a
stop now costs two marked reads (about a second more at the default quiet window), so a live rerun would take roughly
another second per waypoint.

## Open points

* **`--confirm gps` needs an account permission.** The command is gated by `RBAC_PERM_COMMAND_GPS` (505), which the
  default RBAC data links into the administrator command role (`rbac_linked_permissions` 196 → 505), not the
  gamemaster one; an account below it gets the hidden-command answer and route-walk stops with it. Round 2's test
  server now grants the round's GM account one row for it; a fresh account does not. This is server data, not a
  client change.
* **The marker needs a whisper the server echoes.** Each `.gps` request is marked by a whisper to the character
  itself, and a stop is not confirmed without its echo. The core refuses a whisper to an account that has them
  turned off (`.whispers off`) with the not-found notice, so such an account gets `UNCONFIRMED` with the missing
  echo named instead of a confirmation. A character's whispers are on by default.
* **A silent movement refusal is only visible with `.gps`.** A root, a stun or a transfer leaves the client's belief
  advancing; only `--confirm gps` can call that STUCK. Both round-2 walks ran in that mode and the stall test never
  fired, so no leg was STUCK.
* **Pull positions are not walked.** The plan's `pull_groups` are the fight's look spots; path-check already plans
  them offline. If a later round wants them walked, they are the same hops.
* **`gm_cross` is not walked.** The plan's designed GM move is a teleport, which would hide exactly what route-walk
  measures.
* **Z is the plan's Z.** The walker interpolates Z between mesh points; it does not sample the server's terrain, so
  a mesh that is wrong about a floor shows up as a server correction (a teleport) or not at all.
