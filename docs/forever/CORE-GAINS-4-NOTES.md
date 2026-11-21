# Forever core gains 4 notes: knockback landings and SOAP setup

Scope: item 12 (knockback of others and the mover's own packet, with its ack) and item 8 (a SOAP console helper for
setup without a client) of `FOREVER-AGENT-CORE-GAINS.md`. Core read at ForeverCore `6517af05aa`, the rev the
`objects` package pins; no capture bytes, and no live run this round (the brief asks for none).

## Item 12: knockback

### The packets

`SMSG_MOVE_KNOCK_BACK` (`0x5E0033`, Opcodes.h:1915) goes to the mover only. `MoveKnockBack::Write`
(MovementPackets.cpp:964-972) is:

| field | type |
|---|---|
| mover GUID | packed GUID |
| sequence index | `uint32` |
| direction | two floats: the core's `Position(vcos, vsin)` (TaggedPosition<Position::XY>, Position.h:251) |
| horizontal speed | `float` |
| vertical speed | `float` (Unit::SendMoveKnockBack fills `-speedZ`, Unit.cpp:13193) |

`SMSG_MOVE_UPDATE_KNOCK_BACK` (`0x5E0010`, Opcodes.h:2005) carries only one `MovementInfo`
(MoveUpdateKnockBack::Write, MovementPackets.cpp:974-979): no speeds, no sequence. The server broadcasts it after the
mover's ack (HandleMoveKnockBackAck, MovementHandler.cpp:760-776), and this core's `SendMessageToSet` ignores its
`self` argument (Object.cpp:1110-1115), so the mover receives its own echo too.

`CMSG_MOVE_KNOCK_BACK_ACK` (`0x410030`, Opcodes.h:644) is `MovementAck` — a `MovementInfo`, the `int32` ack index —
then `OptionalInit(Speeds)` (MoveKnockBackAck::Read, MovementPackets.cpp:981-987). The handler validates the
movement block and ignores the index and the speeds; this client sends the mover's current block and the sequence
index as the ack index, with the optional-speeds bit clear.

### What the session does

`internal/forever/objects/knockback.go` parses both packets (`ParseKnockBack`, `ParseMoveUpdateKnockBack`) and builds
the ack (`BuildKnockBackAck`). Every float must be finite, the mover must not be empty, and the payload must be
consumed exactly; a short packet, a trailing byte or an empty mover is an error.

`internal/forever/world/knockback.go` keeps the takeoffs whose landing has not arrived, keyed by GUID and bounded at
64 (the oldest past the bound is dropped and counted). For the mover's own packet it records the takeoff at the
client's position and writes one `knockback` row, then sends exactly one ack; the server's echo only refreshes the
pending takeoff and writes no second row and no second ack. For another mover, the first
`SMSG_MOVE_UPDATE_KNOCK_BACK` is the takeoff (the only evidence an observer gets) and writes the row with the
position it carries; its speeds are unknown and left zero. A teleport cancels a pending takeoff, and a map change
drops them all.

The landing is the next `SMSG_MOVE_UPDATE` for that unit: while the falling flag (`MOVEMENTFLAG_FALLING` or
`..._FALLING_FAR`) is set the unit is airborne and the takeoff stays pending; the first update without it is the
landing and writes one `knockback_land` row with the position, the air time and the drop. A takeoff older than 15 s
is dropped as missed when a later update arrives (never paired with a fight that came later); a second knockback for
the same unit replaces the first and counts it missed.

### Where it shows

`CombatRow` gains `pos`, `self`, `horz_speed`, `vert_speed`, `dir_x`, `dir_y`, `air_ms` and `drop` (`CombatPoint` is
the position). They reach the session's ring, the serve `events` page (and its mirror in `serveclient`), the
event log (`pos`, `self`, `speed_xy`, `speed_z`, `air_ms`, `drop`) and the watch's
`<run_id>.combatlog.jsonl`. The watch's drain also writes a `knockback` and a `knockback_land` trace event from the
drained rows, at the row's own wall time, so the landing is readable beside the frames (Recorder.CombatEvents).

`bughunt` gains rule 20, "knockback: landed in a pack or off a ledge", in the event-rule group. It pairs each landing
with the takeoff row before it for the same GUID and reports:

* **off a ledge** when the landing z is at least `R20_LEDGE_DROP_YD` (8 yd) below the takeoff;
* **into a pack** when the nearest frame within `R20_FRAME_PAD_S` (3 s) places at least two in-combat hostiles within
  `R20_PACK_YD` (10 yd) of the landing and at least two of those were not that near the takeoff in the takeoff's
  frame (a knockback inside the same fight is no finding).

A landing without a takeoff row, without a position, or without a frame near it is an UNKNOWN window, never a pass;
a recording with no timed landing at all is a skipped check ("no timed knockback landing"), because the row kinds
did not exist before this round. The key is `(20, member, shape)`, so one member's repeated ledge falls are one
ticket.

### Bounds and hostile input

The parser refuses empty movers, non-finite floats, short payloads and trailing bytes. The pending table is bounded
at 64. A landing never creates an object; a knockback for a GUID the model does not hold is still recorded (the row
names the GUID).

## Item 8: SOAP setup without a client

### The core's service

`SOAP.Enabled` is off by default, `SOAP.IP` is `127.0.0.1` and `SOAP.Port` is 7878 (worldserver.conf.dist:3560-3579).
The endpoint is gSOAP's `ns1__executeCommand` (TCSoap.cpp:86-138, set up in CreateSoapThread at :28-70): HTTP Basic auth, the account must exist and have
`SEC_ADMINISTRATOR` (401/403 otherwise), the command is one console line, and the answer is the command's printed
text in `ns1:executeCommandResponse/result` (a failing command is a SOAP fault). Only console-capable commands work
there; anything that needs a selected unit stays on a GM client. TLS is not part of this endpoint, so the helper
speaks plain HTTP.

### The helper

`internal/forever/soap`:

* **Config** (`host`, `port`, `user`, `password_file` or `password_env`, `timeout_ms`, `allow_non_loopback`) with the
  core's defaults. `Validate` refuses a scheme, a path or userinfo in the host, a port outside 1-65535, a timeout
  over 2 minutes, a missing user and — unless `AllowNonLoopback` — any host that is not `127.0.0.1`, `::1` or
  `localhost`. The transport checks the connected peer as well, so a name that resolves off the machine is refused.
* **Password** from a file (read like every other secret here: regular file, mode 0600, no symlink, at most 4 KiB)
  or from an environment variable (default `FOREVER_SOAP_PASSWORD`). Errors name the path or the variable, never the
  value; the value lives in the Basic header only, redirects are refused so it cannot be forwarded, and a transport
  error has the secret redacted if a peer echoed it.
* **Run** builds the gSOAP document, posts it to `http://host:port/` with
  `SOAPAction: "urn:TC#executeCommand"`, and parses `result` or the fault text. HTTP 401 and 403 are typed errors
  naming the cause. The command deny list is the AGENTS.md one the scenario runner uses (`.reload`, `.server`,
  `.account`, `.ban`, `.npc add/delete/move/set`, `.gobject add/delete`): this helper is for per-character and
  per-instance setup, never a world change.

`forever-agent forever soap [--host H] [--port N] [--user NAME] [--password-file FILE | --password-env VAR]
[--allow-non-loopback] [--timeout DURATION] COMMAND` runs one command and prints its result. The password is never a
flag value (so it is not in the process list) and nothing prints it; a refused or failed command exits 1 with one
line, a bad flag or config exits 2.

### The setup paths

* **Scenarios**: a new step `{"do":"soap","text":".instance setbossstate 0 3"}` runs a console command with no
  session (variables expand; the GM deny list applies at load and at run). `scenario run` builds the backend from
  `--soap-host/--soap-port/--soap-user/--soap-password-file/--soap-password-env/--soap-allow-non-loopback`; without
  a backend the step is an ERROR naming the flags, so a setup scenario cannot pass with its setup missing. The
  result lines are recorded in the transcript.
* **Trials bench**: a slots file's per-server `forever` block may carry
  `"soap": {"host": ..., "user": ..., "password_file"/"password_env": ...}` and `"soap_setup": [".saveall", ...]`.
  `trials run`'s live setup runs those commands before provisioning and records a `server_console` setup row: a
  failing command (or a configured command with no backend) makes the setup invalid with the command and the error
  as evidence. `--dry` does not use the backend.

## Tests

* `objects/knockback_test.go`: hand-made `SMSG_MOVE_KNOCK_BACK` / `SMSG_MOVE_UPDATE_KNOCK_BACK` round trips and the
  ack layout, every prefix length and a trailing byte refused, an empty mover and NaN/Inf flight fields refused.
* `world/knockback_test.go`: one ack with the sequence index and the mover's block, no ack for the echo or for a
  packet naming another mover, a relayed takeoff and a landing paired across an airborne update with the air time
  and drop, no second landing, a teleport and a 15 s-old takeoff dropped as missed.
* `driver/knockback_test.go`: the knockback rows' trace events, at the rows' own times and with their fields.
* `bughunt/rules_knockback_test.go`: a landing in a new pack, the same pack (no finding), the ledge shape, no
  takeoff / no position / no frame as UNKNOWN windows, and the skip without a timed landing. The registry test pins
  rule 20's name and group.
* `soap/soap_test.go`: a fake core-shaped endpoint answers (Basic auth, SOAPAction, escaped command, exact text), a
  SOAP fault, 401 and 403 and a closed endpoint are errors with no password in them, the loopback rules (including
  the explicit flag and bad hosts), the private password file and the environment fallback, and the deny list.
* `cmd/forever-agent/forever_soap_test.go`: the CLI end to end, the non-loopback refusal, the deny list, and the
  password absent from stdout and stderr.
* `scenario/console_test.go`: a `soap` step with variable expansion and recorded result lines, an ERROR without a
  backend, and a world-changing step refused at parse time.
* `trials/live/console_test.go`: the `server_console` row for success, failure and no backend, and the slots block's
  JSON shape.

## Gates

On a clean committed tree at `687448f3` (the only later change is this section):

```
make fmt-check            -> rc=0
make cross                -> windows/amd64 and darwin/arm64 build, windows vet: rc=0
staticcheck ./...         -> rc=0 (no finding)
go vet ./...              -> rc=0
the build VM's go test -count=1 -timeout 20m ./... && go vet ./...:
RESULT: PASS  kind=ta sha=687448f3 dirty=0
rc=0  vm_duration=768s
```

The first VM run at `8071e768` failed one test that pinned the old rule list
(`cmd/forever-agent` `TestSelectedRulesAreInRuleOrder`, which did not know rule 20); the list was updated and the
suite passed at `687448f3`.

## Live checks for the lead's queue

1. **Self knockback ack.** With a test server and a logged-in character, provoke a knockback (a mob's Knockback or
   an Onyxia wing buffet). Expected: the world server's log shows no mover/tampering refusal for the ack, the next
   movement packet is accepted, and the session report has `KnockBacks == KnockBackAcks` with one row per packet. If
   the server log shows the ack arriving twice, the echo was answered: the failure this round's test pins.
2. **Landing pairing.** In a fleet (or any observer with another agent's character nearby) record a
   `dungeon-watch`; after a knockback the combat log must show `kind=knockback` with the takeoff (speeds only for
   the observer's own) and, after the unit grounds, one `kind=knockback_land` with `air_ms` and a drop. The trace
   must carry the same two events. `forever-agent bughunt --rules 20` over the watch must skip a recording with no
   landing and flag a landing within 10 yd of two hostiles absent from the takeoff.
3. **SOAP.** With `SOAP.Enabled = 1`, `SOAP.IP = 127.0.0.1` and an administrator account, run
   `forever-agent forever soap --user NAME --password-file FILE .pinfo`: the account's own info comes back; a wrong
   password is HTTP 401 and the process prints nothing of the password; `--host` with a LAN address without
   `--allow-non-loopback` is refused before a connection. Then a scenario with
   `{"do":"soap","text":".instance setbossstate 0 3"}` and the same flags must print the core's state line and the
   step's result lines must be in the transcript.
4. **Bench setup row.** A slots file with a `soap` block and `"soap_setup": [".saveall"]` must add a
   `server_console` row with `ok: true` to `setup.json`, and a wrong password must make that row `ok: false` with
   the HTTP status in its evidence and the repeat invalid (never a silent run without its setup).

## Known limits

* The client does not simulate its own ballistic flight. The ack carries the block the mover holds at the moment of
  the packet (the server only validates it), and the mover's own landing is paired only if a later position update
  for it arrives; the server does not echo the mover's own ordinary movement updates back to it (MovementHandler.cpp:580-582
  skips the sender), so a self landing is usually unknown. The observed landings are other movers'.
* A relayed knockback has no direction or speeds: the core's update packet does not carry them.
* Server-side bots get a parabolic spline from `Unit::KnockbackFrom` (Unit.cpp:13171-13175), not
  `SMSG_MOVE_KNOCK_BACK`; those knockbacks are visible as `SMSG_ON_MONSTER_MOVE` (already parsed) and are not ruled
  on by rule 20. A fleet of real clients (this repository's setup) is where these packets appear.
* The landing rule uses the falling flag; a knockback that never sets it pairs the first position update, which is
  the best the packet states.
* SOAP has no TLS in this core; the helper refuses non-loopback hosts by default and never follows a redirect, but a
  deployment that enables the endpoint on an interface must protect it itself.
