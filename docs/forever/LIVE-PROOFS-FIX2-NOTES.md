# Live proofs, fix round 2 (ticket #87): the stand the core dropped, and a purchase's own update

Two failures survived PR #127 in the live proofs of 2026-11-22 (a release ForeverCore server; the proofs' notes are
kept with the run): the plan-scout's live pass stopped at a `.go xyz` that came right after a walk (exit 28,
reproduced twice), and `sell-junk` after a `buy` refused "the inventory is stale" for the rest of the session. Both
are client-side; the server was unchanged.

## 1. The stand that was dropped in silence (`internal/forever/world/gm_silent.go`, `cmd/forever-agent/plan_scout_forever.go`)

Evidence, from the serve's own log of the second live run: the last `SMSG_MOVE_TELEPORT` answered was the stand at
the point the walk had just reached; the next line, the next hop's origin (a `.go xyz` to the hop's own first row),
produced **no** teleport packet at all, and the status stayed at that walk's stop for the scout's whole 10 s. Once
the pass had stopped, the same line sent by hand landed at once ("0 line(s), ended by confirmed").

Why the line was lost: the core's `Player::TeleportTo` starts with `if (GetTeleportState() !=
TeleportState::NotTeleporting) return false;` (`Player.cpp`), so a teleport that arrives while the previous near
teleport still waits for its `CMSG_MOVE_TELEPORT_ACK` is dropped **with no message**, and `.go xyz` still looks like
it worked. And the serve's own confirm was a *position* test (`teleportArrived`): a stand to the spot the
character already stood on (the walk's stop, 0.25 yd away) matched before the server had answered, so the gm call
returned before the client had even read the teleport packet — the session applies packets in its waits, and the next
wait was the next stand's. The one socket then carried, in this order: the first stand's `.go xyz` chat, the next
stand's `.go xyz` chat, and only then `CMSG_MOVE_TELEPORT_ACK`. The core ran the second line while its teleport state
was still `WaitingForTeleportAck`, and dropped it.

Fixes:

* `teleportArrived` now needs the server's own move of this character (its `SMSG_MOVE_TELEPORT`, counted in
  `selfTeleports`, or a new world) **and** the landing within `teleportArriveYd` of the named point on the named map.
  A `.go xyz` the core answered with no teleport is "not confirmed", never a success, and because the check runs after
  the packet handler, the ack is on the wire before the call returns — no later line can overtake it.
* `foreverStandAt` sends a stand the serve did not confirm once more after `scoutTeleportSettle` (the core's rule is
  that the line is lost, not that the spot is unreachable), at most `scoutTeleportAttempts` times, and its own status
  read stays the second check. A refusal (any other error) is still the stand's answer at once.
* PR #127's mover rule is untouched: a teleport in place (same x/y, height only) continues the leg, a sideways
  relocation ends it (`objects.Mover.settleTeleport`). What the live walk met as a "re-grounding in place" was this
  same stand line arriving late, mid-leg: its destination was exactly the plan's `hop.From` (x/y unchanged, the z
  0.62 yd under the walk's own) and its own ack. With the confirm fixed that teleport is answered inside the stand,
  before `path_to`/`walk_path` start, so a walk no longer meets it at all.

## 2. A purchase's own ActivePlayerData update (`internal/forever/objects/values.go`)

Evidence, the client's own log:

```
world: item push: entry 2385 count 1 at slot 255/39, 1 in the inventory
world: an own ActivePlayerData update named inventory slots next to fields it could not walk; classic mask bits by group: before-inv-slots[0 18 70 101 134 146]
world: the inventory is stale: a server update named inventory slots next to fields this client does not decode; slot positions may be wrong until the next login or map change
```

A buy's own self block names the bought `InvSlots` entry and `Coinage` next to **Transmog** (classic bit 18, a
`DynamicUpdateField` of u32 appearance ids), **LocalFlags** (101, u32) and **TransportServerTime** (146, int32); the
bits 0, 70 and 134 in the list are those fields' group bits, which carry no value. The decoder gave up on the whole
block because it could not step over a field it does not walk, so the slot deltas were lost and the tracker marked the
inventory stale until the next own create — every `sell-junk` after a buy then refused for the session.

Fixes in `readActivePlayerUpdate`:

* a Classic **group bit** is recognized as such (`activePlayerGroups70291` / `activePlayerGroups70425`,
  `UpdateFields.cpp:5870-5879`) and never counts as an undecodable field;
* the three fields above are stepped over where the server writes them: Transmog's change mask in the second bit run
  (`DynamicUpdateFieldBase::WriteUpdateMask`, `UpdateFieldImpl.h:28-51`) and its u32 values, then LocalFlags and
  TransportServerTime among the scalars;
* the **group 134 presence bits** (four, written whenever any group 134 field changed — TransportServerTime is one)
  and their flush are read before `InvSlots` (`UpdateFields.cpp:7153-7163`); the 70425 group 166 bit keeps its own
  path;
* any other set field still leaves the block undecoded and the inventory stale (a half-read update stays worse than
  an undecoded one), and the log line names the bits by region as before.

## Tests

* `TestServeGMGoXYZToWhereTheCharacterStandsNeedsTheServersMove`: the character already stands on the point, the fake
  answers the second line silently with no teleport — "not confirmed" (the old position-only check passed it).
* `TestServeGMTeleportIsConfirmedByTheServersMove`, `TestServeGMTeleportThatNeverArrivesFails`,
  `TestServeGMTeleportToTheWrongPlaceIsNotConfirmed`, `TestServeGMTeleportToTheWrongMapIsNotConfirmed`,
  `TestServeGMTeleToAPlaceNeedsTheServersOwnMove`, `TestActGMTeleportNeedsNoChatReply`: unchanged, and they now also
  prove that a teleport the server did not send is never taken for one.
* `TestScoutStandResendsALineTheCoreDropped` (two lines, the second lands), `TestScoutStandStopsWhenTheLineIsTwiceUnconfirmed`
  (bounded), `TestScoutStandDoesNotRetryAnotherGMError` (a refusal stops at once).
* `TestActivePlayerUpdateNextToAPurchasesOwnFields`: the live mask and value order (Transmog, Coinage, LocalFlags,
  TransportServerTime, the group 134 bits, the slot) decode to the slot and the money; the old decoder reported the
  slots lost.
* `TestActivePlayerUpdateOtherFieldIsReportedNotSilent`, `TestActivePlayerUpdateCoinageWithSlots`,
  `TestActivePlayerUpdateInvSlots`, `TestActivePlayerUpdateSlotBitsMoveWithTheRealmBuild`: unchanged.

Live re-proof to run next (the same server, plan and vendor as the proofs): the scout's pass through all 80 steps,
and `vendor` → `buy` → `sell-junk` in one session with no relog.
