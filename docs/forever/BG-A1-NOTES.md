# Forever battlegrounds agent watch notes

Scope: the agent side of the battlegrounds card: the BG packet layer,
`forever bg-watch`, the `<run>.forever-bg.json` record and the report card.
The module side does not exist yet; the `.tb bg status` poll this watch sends
is specified here as the row shape this client parses (below).

Everything was checked in ForeverCore at d02645dd21. No packet capture was
read: the tests use hand-made fixtures laid out from the core's own writers
and readers, and no live match was played or recorded.

## New packets

Client sends (Opcodes.h, all in the wire table with translation rows):
`CMSG_BATTLEMASTER_JOIN` 0x3E00BD, `CMSG_BATTLEFIELD_PORT` 0x3E00C7,
`CMSG_BATTLEFIELD_LEAVE` 0x3D001F, `CMSG_BATTLEFIELD_LIST` 0x3D002A,
`CMSG_REQUEST_BATTLEFIELD_STATUS` 0x430008, `CMSG_PVP_LOG_DATA` 0x3D0028,
`CMSG_AREA_SPIRIT_HEALER_QUEUE` 0x3E0047 (+ the query 0x3E0046),
`CMSG_GAME_OBJ_USE` 0x3E0089 (already existed, reused for flag/banner clicks).

Server reads (all parsed; modelled ones listed, the rest parsed+counted):
`SMSG_BATTLEFIELD_STATUS_QUEUED` 0x4B0002,
`SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION` 0x4B0000,
`SMSG_BATTLEFIELD_STATUS_ACTIVE` 0x4B0001,
`SMSG_BATTLEFIELD_STATUS_NONE` 0x4B0003,
`SMSG_BATTLEFIELD_STATUS_FAILED` 0x4B0004,
`SMSG_INIT_WORLD_STATES` 0x4501F0, `SMSG_UPDATE_WORLD_STATE` 0x4501F2,
`SMSG_PVP_MATCH_INITIALIZE` 0x4B0030, `SMSG_PVP_MATCH_COMPLETE` 0x4B002F,
`SMSG_PVP_MATCH_STATISTICS` 0x4B0010,
`SMSG_BATTLEGROUND_PLAYER_POSITIONS` 0x4B0006,
`SMSG_BATTLEGROUND_PLAYER_JOINED` 0x4B0009,
`SMSG_BATTLEGROUND_PLAYER_LEFT` 0x4B000A,
`SMSG_AREA_SPIRIT_HEALER_TIME` 0x4501EA.
Parsed but not modelled (counted, never merged): `SMSG_BATTLEFIELD_LIST`,
`SMSG_BATTLEGROUND_PLAYER_POSITIONS`, `SMSG_UPDATE_CAPTURE_POINT` (the
contested tail form is refused: the watch would rather drop a contested icon
than misread it), `SMSG_CAPTURE_POINT_REMOVED`, `SMSG_PVP_MATCH_SET_STATE`
(1 byte). AB banners use the world-state path (SetNodeStatus writes 1767+),
not capture points.

## Wire details worth stating

* `CMSG_BATTLEMASTER_JOIN` (BattlemasterJoin::Read,
  BattlegroundPackets.cpp:159-168): count, roles, two blacklist map ids,
  then the packed `BattlegroundQueueTypeId`s. The core's join path is
  NPC-free: `HandleBattlemasterJoinOpcode` reads `QueueIDs[0]` straight into
  `BattlegroundQueueTypeId::FromPacked` (BattleGroundHandler.cpp:69-78). The
  watch queues exactly that way (list id 2 WSG / 3 AB, type 0, unrated, team
  size 0). The ticket (RideTicket, LFGPacketsCommon.h:31-47): requester GUID,
  slot id, ride type (1 = battlegrounds), unix join time, cross-faction bit.
  The accept points the ticket at the invited slot (the core re-points it the
  same way, BattleGroundHandler.cpp:373-389).
* The status header's packed queue-id count is server data: a count past 8
  is refused before the list is built, and so is a count longer than the
  bytes left. A hostile packet cannot make the parser allocate or spin.
* `SMSG_BATTLEFIELD_STATUS_ACTIVE` field order is map, shutdown, start,
  faction — not the header's order (BattlegroundPackets.cpp:212-224).
* `UpdateWorldState` value is int32 with a hidden bit (WorldStatePackets.cpp:41-52).
  The session's world-state map is capped at 4096 ids: a known id keeps
  taking updates, an unknown one past the cap is dropped and logged.
* `TaggedPosition<XY>` is two floats (Position.{h,cpp}: X/Y only).
* `SMSG_PVP_MATCH_COMPLETE` (PVPMatchComplete::Write,
  BattlegroundPackets.cpp:420-432) is winner, duration, the log-data bit, two
  solo-shuffle bits, then the log data. `Battleground::EndBattleground`
  always attaches the log data (:801-805), so the head is decoded and the
  tail is read with the same reader as `SMSG_PVP_MATCH_STATISTICS`. A tail
  this parser cannot read (a layout detail the hand-built fixtures cannot
  prove) does not drop the end: the 12-byte head's winner and duration are
  kept, the tail is logged as a parse error, and the record ends the match.
  `Winner` is the `PvPTeamId` value: 0 Horde, 1 Alliance, 2 Neutral
  (SharedDefines.h:6936-6939). `Duration` is `WorldPackets::Duration<Seconds>`
  already in seconds (BattlegroundPackets.h:506-532), so it is used as sent.
  A Neutral winner is the premature-finish timer's answer (neither side met
  the minimum) or a draw; the record marks it premature and names no winner.
* `SMSG_PVP_MATCH_INITIALIZE` duration is seconds too. The complete state it
  reports on entry (WAIT_LEAVE) names the length but no winner, so it never
  finalises the record: the record ends only once the winner's packet
  arrives. If no result packet follows within 15 s (the character entered a
  match that had already ended; the result packet went to the players
  inside), the initialize state is the fallback end: the record keeps its
  length, names no winner, and does not infer one from the last score
  sample.
* The invite's accept window is server data: `Timeout` is
  `INVITE_ACCEPT_WAIT_TIME` (90 s, Battleground.h:136) on the first invite
  and the time left on a status answer (BattleGroundHandler.cpp:626). The
  session stamps the arrival (`Expires`), so a late poll sees the window
  that is left. The accept delay (a human 2-8 s) is capped below that
  window, an invite whose window has closed is not accepted at all
  (`invite-expired`), and the join is recorded only after
  `SMSG_BATTLEFIELD_STATUS_ACTIVE` confirms the entry.
* WSG flag ids: 1545/1546 (0 base, 1 carried, -1 ground), 1581/1582
  captures, 2338/2339 carrier flags. `CaptureFlag` writes the flag state to
  0, raises the capture counter and broadcasts (:1030-1092); a carrier
  logout also resets the flag state to 0 (:815-821). So a capture has one
  source only: the 1581/1582 counter. A 1 -> 0 step emits nothing; a
  ground -> base step is a return.
* AB node ids (NodeData, battleground_arathi_basin.cpp:707-715): stables
  1767, blacksmith 1782, farm 1772, lumber 1792, mine 1787. The state ids
  are `StateWorldStateOffset` over `NodeStatus` (:718): +0 Alliance
  occupied, +1 Horde occupied, +2 Alliance contested, +3 Horde contested.
  The event table is the core's: neutral -> contested is a claim/assault
  (:1006-1047); contested -> the same side's occupied state is the contested
  timer's occupation (:800-818); occupied -> enemy contested is an assault
  (:1043-1047); contested by the enemy -> the original owner's occupied state
  is a defend (:1019-1030); contested A -> contested H is a re-assault
  (:1019-1025). The first state seen for a node is the baseline, not a
  transition: a character entering an active match sees contested and
  occupied nodes it never watched change, and no event is derived from that
  first snapshot. An undefended loss counts at the enemy's occupation (the
  node's last occupying side was the watcher's, no bot of the team was
  assigned to it), not at its assault: a node assaulted and defended back
  was never lost. Win score 2000 (MaxTeamScore, :614, checked at :282-287).
* Deserter 26013 (Player::LeaveBattleground, Player.cpp:25694-25716);
  Waiting to Resurrect 2584; premature-finish default 300 s
  (worldserver.conf.dist:3086, 3124-3130).

## The `.tb bg status` rows

One `TBM:BG` record answers the poll:

| field | meaning |
|---|---|
| `team` | the watcher's side, `alliance` or `horde` |
| `map`, `status` | `wsg`/`ab`, and `in-progress` for the match itself |
| `alliance_humans`, `alliance_bots`, `horde_humans`, `horde_bots` | inside now |
| `alliance_score`, `horde_score` | the module's own scoreboard |
| `alliance_deaths`, `horde_deaths` | deaths per side |
| `deserter`, `fallback` | a bot carried 26013; a fallback was called |
| `fc`, `fc_x`, `fc_y` | the flag carrier and where |
| `fc_escorts` | the watcher's team within 20 yd of the carrier, carrier excluded |

Then one `TBM:BG_BOT` row per bot: `role`, `objective`, `node` (the AB node
it is assigned to), `alive`, `move_age`. No row carries a character name.

ForeverCore's module answers with plain `bg ...` rows instead, behind its
`TortoiseBots: ` system-message prefix and with the token on the end row only:

```
bg: enabled=1 ...
bg maps: ...
bg instance=3 map=wsg status=in-progress score=1-0 alliance=1h+4b horde=0h+5b
bg bot=<id> role=fc objective=flag node=- map=wsg instance=3 team=alliance alive=1 move_age=2s
bg queued ...
bg-end verb=status lines=N #token
```

The poll reads both grammars. In the real rows the side counts are `1h+5b`
forms, `move_age` is a duration (`2s`, `1m30s`, `-`), the watcher's instance
is the first instance row holding a human, unknown keys and the maps/queued
rows are skipped so a later module round cannot break the poll, and
`enabled=0` is a refused poll. When humans appear in two instances the
module's answer cannot say which match is the watcher's, so the parser
refuses it (`ErrBadReport`) rather than report the wrong match. The
collector's `Options.BareBody` is what takes the untagged body rows: a line
carrying another token is still not this command's reply.

The watch reads the escort share from `fc_escorts` (the module sees the
battlefield; the passive observer does not), the per-side deaths from the
`*_deaths` fields, and an AB node's defenders from the bot rows whose
`node` is that node. It flags an unsupervised dropped own flag only when
`team` says which flag is its own. A reply without a `TBM:BG` block is a
refused poll: the watch stops as `status-refused` (exit 5), not as a lost
link.

## The watch machine

`internal/forever/bg` (watch.go): config (map, role, out, run id, budgets,
thresholds), the `Ports` seam (Send, Status, WorldStates, SelfPos, Died,
PollStatus, Refused, Active, Sleep; the dry fake implements the same seam), the
record (`Record`), the card (`Card`, `Thresholds`, `Build`), and the
`Watcher`: queue, wait for the invite while polling the status, accept after
a random 2-8 s on the world thread capped below the invite's remaining
accept window, wait for `ACTIVE`, observe to the match end, send
`CMSG_BATTLEFIELD_LEAVE`, build the card. Every stop builds the record's
stop reason and the card (no path leaves an empty verdict).

The watch sends nothing to the module during the match except `.tb bg status`
polls (no orders). The role is a passive observer; `--role fc` is refused
because a flag carrier needs a movement and click implementation the watch
does not have. `--team-of` is reserved and refused at argument parse.
`--run-id` must be one file name (no folder separators), because it names
the record files.

During the queue wait the watch polls `CMSG_REQUEST_BATTLEFIELD_STATUS` and
sleeps a poll interval until the invite arrives, the deadline passes, the
server refuses the join (a `FAILED` status) or the link fails. An invite
whose accept window has closed stops as `invite-expired`, and so does an
accept the server never confirms with `ACTIVE` (a few polls after the send).
Only a seen match end sends `CMSG_BATTLEFIELD_LEAVE`. A time-budget stop
does not leave mid-match, because that is the Deserter path in every case:
the run's logout still ends the way a client logout from a match does, so a
budget stop can leave the tester with Deserter, and the record's
`time-budget` stop reason says the match was not seen to its end.

The record is `<run>.forever-bg.json` (schema `forever-bg` version 1): the
score timeline, flag events (pick/drop/return/capture from world states, no
chat text and no names), node events (assault/defend/occupied), the
watcher's own deaths, the watcher's team, per-side humans/bots/deaths,
per-bot rows (role, objective, node, longest still span), the watcher's own
positions, the match result and length. No field carries a character name,
account, host or path.

The card (schema `forever-bg-card`) flags: quiet (no capture/assault in
10 min), idle (a bot with no 5 yd move in 60 s over any in-progress poll),
escort share under 50% (sampled only on polls with an FC; -1 with none
seen), untouched own flag (10 s, once per drop), a node lost with 0
defenders near, deserter, premature end. The verdict word is the first flag's
id in upper case (`QUIET`, `IDLE`, ...), or CLEARED/STOPPED/FAILED. The card's
`notes` say which rules could not run: the untouched own-flag and
undefended-node rules need the team the `.tb bg status` poll names, and
without it they stay off.

Exit codes: 0 = a match end was seen; 6 = the link was lost; 5 = every other
stop (queue timeout, an expired or unconfirmed invite, refused join, refused
status poll, time budget, unwritable files). The dry command cannot return 6.

`cmd/forever-agent` (`forever bg-watch ... [--dry]`, plus
`forever_bgwatch_dry.go`): usage, the end line, the record and card files
(`<run>.forever-bg.json`, `<run>.bg-card.txt/.json`).

Session side (`internal/forever/world/bg_session.go`, one dispatch line in
`session.go`, death attribution in `combatlog.go`): the decoded BG state
(world-state map, invite, match end, join refusal), the packet dispatch
(`handleBG`, `isBGPacket`), the `.tb bg status` poll through the typed client
(`tbcmd.BGStatus` in `command/tbcmd/bgstatus.go`; a reply with no BG block is
`ErrBadReport`, which the adapter turns into `bg.ErrStatusRefused`, so the
watcher never reads a refused answer as an empty match), and `bgWatch`
(settle, GM probe, the machine over `bgPorts`). `Died()` counts only the
watcher's own GUID: a creature or another player dying in view is not a
watcher death. BG-system chat is not read at all: it carries player names and
the world states are the authority on the flags.

## Known limits

* No packet capture was ever read, and no live match was played: the
  `SMSG_PVP_MATCH_COMPLETE` log tail is decoded from the core's writer only,
  which is why a tail mismatch now falls back to the head instead of
  dropping the end. The tail parser is still unproven until a capture of a
  finished match is added as a fixture.
* The `.tb bg status` grammar (`team`, `fc_escorts`, `node`, `in-progress`,
  deaths) is the client's guess at a module that does not exist yet;
  the untouched, escort and undefended rules stay off (and the card says so)
  when the module's answer does not carry the rows.
* A `time-budget` stop logs out mid-match and can leave the tester with
  Deserter; the watch does not send `CMSG_BATTLEFIELD_LEAVE` there because
  leaving is the very path that applies Deserter. The card's stop reason
  says the match was not seen to its end.
* The dry command scripts every port: the live queue/invite/port timing,
  the GM probe against a non-GM tester, death attribution and the opcode
  wire numbers on both client builds still need live checks before a
  server rollout.

## Dry runs

None recorded from a live match (no live run). The dry command exercises the
whole machine:

* `forever bg-watch --dry --map wsg --out DIR`: queue, invite, one pick,
  one drop, one return, one capture, alliance win, leave; card CLEARED.
* `forever bg-watch --dry --map ab --out DIR`: five claims, five timer
  occupations, one Horde assault on an occupied node, the defend back,
  resources; card CLEARED.
* Refusals: `--map xx`, `--role xx`, `--role fc`, `--poll 0/31`, the
  threshold ranges, `--team-of 1` and a `--run-id` with a separator all exit
  before any send.

## ForeverCore 0.1.39

The owner-only AttackPower block of a unit create is 23 writes, 92 bytes, with nothing after `TaxiNodesID`
(`UpdateFields.cpp:1190-1263`); before 0.1.39 it was 80 bytes plus three owner-only int32 after `TaxiNodesID` — the
same 92 in all — so the decode skips 92 once and is aligned on both cores. Client build 71037 is named and checked
to share the 70790 numbering, but it is not claimed on its own until one live login has checked its packet layouts.

## Files

* `internal/forever/bg/bg.go`: queue-type packing, ticket/header layouts,
  send builders, parse functions, WSG/AB ids, aura/config constants.
* `internal/forever/bg/handbuilt_test.go`: every layout from the core's
  writer, sentinel after each optional/counted block, trailing-byte and
  bound checks.
* `internal/forever/bg/watch.go`, `watch_test.go`, `fixes_test.go`: events,
  record, card, dry scenarios (WSG full/quiet/untouched/idle+escort/
  deserter+premature, AB claims/transitions), the review-round regressions.
* `internal/forever/wire/opcodes.go`, `opcodes_test.go`: BG opcodes both
  directions (plain group arithmetic below the insert indexes, no per-build
  difference).
* `internal/forever/world/bg_session.go`, `bg_session_test.go`,
  `bg_fix_test.go`, `session.go`, `combatlog.go`: dispatch, match end, the
  status poll, deaths, the runner.
* `internal/forever/command/tbcmd/bgstatus.go`, `bgstatus_test.go`: the
  typed `.tb bg status` client and its fixtures.
* `cmd/forever-agent/forever_bgwatch.go`, `forever_bgwatch_dry.go`,
  `forever_bgwatch_test.go`, `forever_login.go`: command, dry script,
  tests, dispatch.
* `README.md`: the bg-watch line under "What you can do with it".
