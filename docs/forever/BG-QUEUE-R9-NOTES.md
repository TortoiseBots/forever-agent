# Forever battleground queue, round R9 notes

Scope: the battleground queue from the player's side — the battlemaster's list probe, the join, the statuses, the
port accept and the leave — as `forever act` steps. Code: `internal/forever/bg` (the list parse, the hello builder,
the status/port/leave packets the watch already had), `internal/forever/world/bg_queue.go` (the per-slot queue
state), `internal/forever/world/bg_act.go` (the steps), `internal/forever/world/act.go` (the verbs).

Everything below was checked in ForeverCore at d02645dd21 (`git show d02645dd21:<path>`). Every fixture is hand-made
from the core's writers; no capture byte, GUID, name or file name is in the repository.

Status: implemented, tested against the fake world. **No live run yet** (the live checks are at the end).

## The client packets

| Packet | Wire | Body | Core |
|---|---|---|---|
| `CMSG_BATTLEMASTER_HELLO` | 0x3D018F | the battlemaster's packed GUID | `NPC::Hello::Read`, NPCPackets.cpp:94-97 |
| `CMSG_BATTLEMASTER_JOIN` | 0x3E00BD | the count (one), the roles byte, two blacklist map ids, the one packed queue id | `BattlemasterJoin::Read`, BattlegroundPackets.cpp:159-168 |
| `CMSG_BATTLEFIELD_PORT` | 0x3E00C7 | the ride ticket, the accepted-invite bit | `BattlefieldPort::Read`, BattlegroundPackets.cpp:250-254 |
| `CMSG_BATTLEFIELD_LEAVE` | 0x3D001F | empty | `BattlefieldLeave::Read`, BattlegroundPackets.cpp:270-276 |
| `CMSG_BATTLEFIELD_LIST` | 0x3D002A | the list id i32 | `BattlefieldListRequest::Read`, BattlegroundPackets.cpp:256-259 |
| `CMSG_REQUEST_BATTLEFIELD_STATUS` | 0x430008 | empty | `RequestBattlefieldStatus::Read` |

The facts that decide the flow:

* **The list is the probe.** The hello answers `SMSG_BATTLEFIELD_LIST` (BattlegroundMgr.cpp
  `SendBattlegroundList`); the list id is the supported battlemaster type. A list id that is not positive stops the
  round with its report instead of queueing (the spec's own rule); one over 0xFFFF is refused as malformed because
  the packed queue id cannot hold it.
* **The join names the list's packed queue id.** The queue id packs the list id with the fixed high marker
  (`BattlegroundQueueTypeId::GetPacked`, SharedDefines.h): `bg.PackQueueType` builds it, and the step sends roles
  and blacklists as zero.
* **The port echoes the invite's ticket.** The handler points the port at the queue the player really is in
  (BattleGroundHandler.cpp `HandleBattleFieldPortOpcode`), so the accept sends the need-confirmation's own ticket
  with the accept bit set. A queue is left the same way, with the queued (or invited) ticket and the accept bit
  clear; `CMSG_BATTLEFIELD_LEAVE` only calls `Player::LeaveBattleground`, which returns with no battleground.
  Inside a match that packet leaves (and casts Deserter while the match is running, Player.cpp:25691-25706), so
  `bg-leave` sends it only when the step says `match=yes`.
* **An expired invite is not replayed.** `bg-accept` counts the invite's own `Timeout` (milliseconds) from its
  arrival and refuses one that passed, because the server already removed the queue entry.
* **Every status names its queue.** A queued, invite or active status's header carries the packed queue id; a
  failed status carries it beside the ticket; a NONE names only the ticket slot. The session keeps one status per
  slot, and `bg-accept` and `bg-leave` act only on the queue `bg-queue` joined, so a status for another queue the
  character sits in never selects, accepts or leaves this one.

## The server's answers

| Packet | Wire | Body | Core |
|---|---|---|---|
| `SMSG_BATTLEFIELD_LIST` | 0x4B0005 | master GUID, list id i32, min/max u8, count u32, the battlefields, two bits flushed | `BattlefieldList::Write`, BattlegroundPackets.cpp:261-275 |
| `SMSG_BATTLEFIELD_STATUS_QUEUED` | 0x4B0002 | the header, average wait u32, wait u32, spec i32, three bits flushed | `BattlefieldStatusQueued::Write`, BattlegroundPackets.cpp:226-238 |
| `SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION` | 0x4B0000 | the header, map u32, timeout u32, role u8 | `BattlefieldStatusNeedConfirmation::Write`, BattlegroundPackets.cpp:202-210 |
| `SMSG_BATTLEFIELD_STATUS_ACTIVE` | 0x4B0001 | the header, map/shutdown/start u32, arena faction i8, two bits flushed | `BattlefieldStatusActive::Write`, BattlegroundPackets.cpp:212-224 |
| `SMSG_BATTLEFIELD_STATUS_NONE` | 0x4B0003 | the ticket | `BattlefieldStatusNone::Write`, BattlegroundPackets.cpp:195-200 |
| `SMSG_BATTLEFIELD_STATUS_FAILED` | 0x4B0004 | the ticket, the queue id u64, the reason u32, the client id | `BattlefieldStatusFailed::Write`, BattlegroundPackets.cpp:240-248 |

The status header is the ticket (LFGPacketsCommon.cpp:23-44), the queue-id count and ids, the range/team/instance
bytes, then two bits flushed (BattlegroundPackets.cpp:177-193). Both statuses write the header first:
need-confirmation then writes map, timeout and role, and queued writes the average wait, the wait and the spec; a
parser that reads either field before the header shifts the whole status. The failing case is
`TestStatusHeaderQueuesBeforeBits`: the queue id follows the instance bytes and precedes the two flushed bits.

## Tests

`internal/forever/world/bg_fake_test.go` runs the steps against the handler-boundary fake: the empty list stops the
round, a non-battlemaster refuses before any packet, the join names the list's queue and the queued status is the
proof, silence and a refusal (with the server's reason), the invite, the port accept confirmed by the active
status, the leave of a queue and of an invite through the port, `match=yes` for a started match (with the Deserter
warning), an expired invite refused before any packet, an unpackable list id, and the two correlation cases (another
queue's status is not the join's answer; a NONE for another slot does not drop the other queue).

## Live checks (NOT RUN)

1. **Probe.** `npc entry=<a battlemaster>`; `bg-queue`: when the list names no supported id the step reports it
   and sends no join; otherwise the line names the list and the wait.
2. **Queue and leave.** `bg-queue`, then `bg-leave`: the step leaves through `CMSG_BATTLEFIELD_PORT` with the
   accept bit clear, the none status is the proof, and a second status shows none.
3. **Invite (only when one arrives).** `bg-queue` reporting the ready invite, `bg-accept`: the line names the
   map and the active status proves the port. An invite past its own timeout is refused before any packet.
4. **Inside a match.** `bg-leave` without `match=yes` refuses before any packet and names the Deserter risk;
   `bg-leave match=yes` sends `CMSG_BATTLEFIELD_LEAVE`, and the line repeats the warning.
5. **Refusals.** A non-battlemaster `bg-queue` fails before any packet; a failed queue names the server's reason
   (`range index` for a level-range mismatch, ...).

## Gate lines

* `make fmt-check`: no output, `rc=0`.
* The focused race run: `go test -race -count=1 -timeout 20m ./internal/forever/bg/ ./internal/forever/npc/` and
  `go test -race -count=1 -timeout 20m -run 'TestBG|TestAct' ./internal/forever/world/`.
* The full turtle-agent suite on the build VM (`vm-test ta`): `RESULT: PASS kind=ta sha=<sha> dirty=0`.
