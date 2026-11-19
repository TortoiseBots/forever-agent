# Session state shared between goroutines

This is the one table of what `internal/forever/world`'s session shares with another goroutine, and what it does
not. It covers the login and world rounds, the combat round (round 4), the chat and party round (round 5) and the
command channel (round A6); the round notes (`COMBAT-R1-NOTES.md`, `CHAT-R1-NOTES.md`, `COMMAND-R1-NOTES.md`)
point here instead of keeping a copy, because the copy went stale the moment a round added a field.

The rule: **the session goroutine owns the session.** `Enter` runs the handshake, the socket readers start after it,
and every handler (`handle`, `handleChat`, `handleParty`, `handleCombat`, the fight, the chat and party commands, the
capture) runs on that one goroutine. Only the two socket readers are separate goroutines. So the only state that
needs protection is what a reader touches, plus the report fields a reader fills when it marks the link dead.

`objects` and `combat` have no goroutine of their own. `chat.Log` and `party.State` have none either: their mutexes
guard their own accessors, and the only caller is the session goroutine.

| state | written by | read by | protection |
|---|---|---|---|
| `session.deadChs[2]` | `setLinkDead` closes one | `nextExcept` selects on both | closed under `deadMu`; created in `Enter` before `startPump` |
| `session.deadClosed`, `deadErrs` | `setLinkDead` | `setLinkDead`, `deadErrorExcept`, `deadDetail` | `deadMu` |
| `session.deadAny`, `deadSide`, `deadErr` | `setLinkDead` | `setLinkDead` (`deadSide`/`deadErr` are not read elsewhere) | `deadMu` |
| `session.deadStale` | `markStale` (session goroutine) | `deadDetail`, `deadErrorExcept` | `deadMu` |
| `session.logoutSent`, `session.logoutComplete` | `logout`/`handle` (session goroutine) | `setLinkDead` (reader) | `deadMu` |
| `session.lastPacketName`, `lastPacketAt` | `notePacket` (session goroutine, only writer) | `setLinkDead` (reader), `lastPacket`, `deadErrorExcept` | `deadMu` |
| `report.LinkLost`, `LinkLostSide`, `LinkLostDetail`, `LastPacket`, `LastPacketAgo` | whichever goroutine calls `setLinkDead` | `abortLogin` (after a `deadMu` acquire), `cmd/forever-agent` after `Enter` | written under `deadMu`; the readers are waited for in `shutdown` |
| `session.events` | both readers send | session goroutine receives | buffered channel (happens-before) |
| `session.pumpCtx`, `pumpCancel`, `pumpDone` | `Enter` | readers read the context and close their done; the session stops the context and waits | context and channels; no field is written after `startPump` |
| `session.chat` (`chat.Log`) | `handleChat` / `addNotice` / `summariseChat` (session goroutine; `initChat` creates it on that goroutine) | `chatLineText`, `summariseChat`, the report copy | single owner; the log's own mutex covers its accessors |
| `session.party` (`party.State`) | `handleParty`, `handle` (`SetSelf`), the party-check steps (session goroutine; `initChat` creates it on that goroutine) | `printParty`, `chatName`, the party-check steps, `summariseParty` | single owner; the model's own mutex covers its accessors |
| `session.captureRun`, `session.captureErr` | `capturePacket` (session goroutine) | `capturePacket`, `shutdown` (`close`) | single owner: `shutdown` runs on the session's goroutine after the command returns, before the sockets close |
| `session.cmdFeed` | `cmdLink.Pump` sets it before its loop and clears it after (session goroutine) | `handleChat` / `addNotice` (`feedCommand`) on the same goroutine | single owner: set, called and cleared on the session goroutine while one command runs; `command.Session` serialises the runs |
| `session.fight`, `known`, `castSerial`, `parseErrorLogged` | handlers and the fight (session goroutine) | same | single owner |
| `session.fightSender` | the tests, before `Enter` enters the world | `transport` (session goroutine) | set before the run starts |
| `session.death` (`deathWatch`) | `handleDeath` and the death flow (session goroutine) | the same | single owner; `observeSelf` runs on that goroutine |
| `report.Captured`, `report.Chat`, `report.Party`, the rest of `Report` | session goroutine | session goroutine; `cmd` after `Enter` returns | single owner |
| `worldconn.conn.recvSeq`, `crypt`, `inflater` | the socket's receiving reader (the handshake that sets them runs on the session goroutine before `startPump`) | same | single owner |
| `worldconn.conn.handshakeNames` | the handshake only, frozen by `handshakeDone` before `startPump` | `HandshakeNames` (session goroutine) | written before any reader exists |
| `worldconn.conn.sendSeq` and the seal counter | every sender | every sender | `sendMu` (counter and write as one operation) |
| `worldconn.conn.encrypted` | the handshake, before the pump starts | both directions | written once, read-only after |
| `worldconn.conn.nc` | both directions, `Close`, the deadline watchers | all of them | `net.Conn` is safe for concurrent use |
| `worldconn.conn.closeOnce`, `closeErr` | `Close` callers | `Close` callers | `sync.Once` |
| `objects.World`, `objects.Mover`, `fightLog` | session goroutine only | session goroutine; `cmd` after `Enter` returns | single owner (`World`'s own `RWMutex` covers its read accessors) |
| `fakeServer.deliberateClose` (test harness) | `dropLink` | both socket readers | `fakeServer.mu` via `deliberate()` |

## The race the combat round's CI found

`lastPacketName` and `lastPacketAt` were written by `handle` (session goroutine) without `deadMu`, while `setLinkDead`
(a socket reader) read them; `deadErrorExcept` and `nextExcept` also read them unlocked. `deadMu` now guards both,
with the invariant stated on the field block; `handle` writes through `notePacket`, `nextExcept` reads through
`lastPacket`, and `deadErrorExcept` takes the name inside the same critical section as `deadStale`. The harness's
`deliberateClose` moved under the fake server's own `mu`.

The chat and party round added no shared field: the chat log, the party model and the capture run are all touched
only by the session goroutine that handles their packets, and the link's liveness (which they wait on) is the one
mechanism above.

## Gates

The pre-merge gate of this repository includes the race run. Before a merge:

| gate | command |
|---|---|
| the whole suite under the race detector (the CI `test` job) | `go test -race -count=1 -timeout 20m ./...` |
| the forever packages, ten times, under the race detector | `go test -race -count=10 ./internal/forever/...` |
| format, vet, no-cgo build | `make fmt-check vet nocgo` |
| static analysis | `staticcheck ./...` (2026.2.2) |

A single `-race` run is not enough for this code: the failing test starts its goroutines 12 times, and `-count=5` on
the focused link tests reproduced all three pairs on the laptop.
