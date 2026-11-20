# Logout round: the instance socket closing with the logout

Scope: the logout's sockets. The symptom this round started from is an earlier run's: in four of nine
add-on runs the client's logout ended with the instance socket closing before `SMSG_LOGOUT_RESPONSE`
(`world abort: logout not possible: connection lost ... EOF`, exit 5), while the add-on traffic itself had
completed. This round reads the core's logout path, runs a 30-run live loop, and pins the sequence in the
fake world server. The client-side fix for the close was made in the other-build round
(63fc3f78); this round verifies it on the live server, adds the exact ordering to the tests, and writes
up the core's dropped response.

## 1. What the core does

The response and the completion travel on different sockets (`src/server/game/Server/Protocol/Opcodes.cpp:1938-1939`):

* `SMSG_LOGOUT_RESPONSE` is a `CONNECTION_TYPE_INSTANCE` opcode;
* `SMSG_LOGOUT_COMPLETE` is a `CONNECTION_TYPE_REALM` opcode.

`WorldSession::HandleLogoutRequestOpcode` (`src/server/game/Handlers/MiscHandler.cpp:243`) queues the response
with `SendPacket` at `:267`. An instant logout then calls `LogoutPlayer(true)` in the same handler (`:274-278`);
a logout that is not instant sets the start time at `:291` and completes after 20 s
(`ShouldLogOut`, `src/server/game/Server/WorldSession.h:1095-1098`, called from
`src/server/game/Server/WorldSession.cpp:599-601`).

`LogoutPlayer` (`src/server/game/Server/WorldSession.cpp:628`) queues `SMSG_LOGOUT_COMPLETE` on the realm socket
at `:766`, then closes the instance socket at `:787-791`.

The close is a send-side shutdown without a flush: `CloseSocket` in `src/common/network/Socket.h:228-240`
does `shutdown(shutdown_send)`; `SendPacket` (`src/server/game/Server/WorldSocket.cpp:517`) only puts the packet
in `_bufferQueue`, and `WorldSocket::Update` (`:226`) moves it to the write queue on its next run. Nothing drains
`_bufferQueue` when the socket is closed. So the queued `SMSG_LOGOUT_RESPONSE` is written only if the socket's
update happened to run between the `SendPacket` and the `CloseSocket`; otherwise the close drops it and the client
sees EOF. The completion is queued on the realm socket, which stays open, so it is written normally.

Other ways the core can close the instance socket:

* the 20 s timer path above (same `LogoutPlayer`, same order: completion first, then the close);
* the session destructor (`WorldSession.cpp:170-177`) calls `LogoutPlayer` when a player is still loaded, then
  closes both sockets; a client whose realm socket is already gone gets neither message;
* the session update's socket cleanup (`:604-615`): once one tracked socket is not open, `expireTime` (60 s,
  `:146`) starts counting down and then every socket is closed; this is the path after the client disappears;
* `KickPlayer` (`:800-816`) closes both sockets without a logout message;
* the idle realm close (`:369-373`) closes the realm socket first; the instance follows through the cleanup path;
* the instance socket's own auth failure (`:326-333`) closes it before any player login;
* any read or write error on the socket (`src/common/network/Socket.h:300-310`) closes it.

## 2. What the client does now

`session.logout` (`internal/forever/world/session.go:1361`):

* sets `logoutSent` **before** writing `CMSG_LOGOUT_REQUEST` (`:1365`), so a close that races the send is already
  the session ending, not a lost link;
* waits for `SMSG_LOGOUT_COMPLETE` on the realm socket, ignoring the instance side (`nextExcept`, `:2034`). The
  wait is 30 s (`logoutWait`, `:1352`), which covers the core's 20 s timer;
* gives the response 500 ms after the completion; the response may never arrive, and that is not an error;
* a non-zero response result is reported as a refusal; a realm socket lost before the completion, or a silent
  realm, is `ErrLinkLost`; a completed logout clears the teardown loss (`clearTeardownLoss`);
* `setLinkDead` (`:599`) suppresses an instance close only once `logoutSent` is set; a close before that is still
  the loss it looks like.

Every way the client can close a socket first was checked: `Enter` closes both sockets only after `enter`
returns (`shutdown`, `:722`); a read is bounded by the operation's context or the 60 s default
(`internal/forever/worldconn/frames.go:57-68`), not by the connection's life; the stay pings every 30 s (`:1318`)
and the link's silence bound is 30 s (`:584`); the logout's 20 s window starts after the request, so no
keepalive gap expires it. A write error after the request is suppressed for the instance side by the same
`logoutSent` rule, and the run then waits for the completion; a realm write error is still a loss.

## 3. The live loop

Thirty login -> short activity -> logout runs on the test server, with the current client: ten with no chat
traffic, ten with a self-whisper just before the logout, and five party pairs (ten clients: one invites with a
party add-on message, one accepts). Each run's outcome was read from its own log and timestamped line by line.

| mode | runs | clean | response seen | response dropped |
|---|---:|---:|---:|---:|
| quiet | 10 | 10 | 5 | 5 |
| whisper | 10 | 10 | 2 | 8 |
| party | 10 | 10 | 8 | 2 |
| **total** | **30** | **30** | **15** | **15** |

No run ended with `world abort`, a lost link, or a non-zero exit. The response drop is common (15 of 30), which is
the core's close-order race above, and the client now treats it as clean because the completion arrived.

Timings, request to the logout's end:

* instant logout with the response read: 4-8 ms;
* instant logout with the response dropped: the run ends 500 ms after the completion, the client's grace for a
  response that will not come; the completion itself is immediate;
* not-instant logout (the clients that logged out while not resting): 19.11-19.30 s, the core's 20 s timer.

The instance socket is closed by the core at the same moment it queues the completion, so the close and the
completion are simultaneous on the two sockets; the client's ordering guarantee is the completion on the realm,
never the close.

## 4. The test

The fake world server's logout is played step by step, by the test. `playLogout`
(`internal/forever/world/fake_world_test.go`) has the fake announce CMSG_LOGOUT_REQUEST on `logoutAt` and then
perform exactly the steps the test sends on `logoutStep`, one at a time: `completion` (SMSG_LOGOUT_COMPLETE on the
realm socket, its write returned when the frame is flushed), `response` (the response on the instance socket),
`close-instance`, `close-realm`, `end`. The two sockets are separate TCP connections, so nothing in the client can
order them; a sleep in the fake makes one order likely and lets the test pass either way, which pins nothing. With
the steps, each scenario's order is exact and each asserts its own outcome:

| test | steps | asserted outcome |
|---|---|---|
| `TestLogoutInstanceCloseIsNotLinkLoss` | completion, close-instance | clean: completion seen, no link loss, the absent-response line |
| `TestLogoutInstanceCloseBeforeCompletionIsNotLinkLoss` | close-instance, completion | clean: the instance EOF first, the completion after it, still no link loss |
| `TestLogoutInstanceCloseWithoutCompletionIsALinkLoss` | close-instance, close-realm | `ErrLinkLost` on the realm, no completion, never clean |

Each test also asserts the steps the fake performed (`f.logoutSteps()`), so the order itself is pinned, not only the
outcome. The first two are the live order when the queued response is dropped: the close and the completion are
simultaneous on two sockets, and the client may read either first. The third is the same sequence with the session
not ending; it fails if the client ever reports a clean logout that no completion proved.

Stability, fifty runs of the three tests in one process:

```
$ go test ./internal/forever/world/ -run 'TestLogoutInstanceClose' -count=50
ok  	github.com/pjerra/forever-agent/internal/forever/world	66.268s
```

The existing tests still pin the rest of the rule: a realm loss before or after the response is a loss
(`TestLogoutRealmLossBeforeCompletionIsAnError`, `TestLogoutRealmLossAfterTheResponseIsStillALoss`), a silent
realm is a loss (`TestLogoutSilentRealmIsALoss`), and a refusal is its own error (`TestLogoutRefusedByTheServer`).

## 5. Core issue (not changed here)

**Response dropped by the instance close.** Sequence, all in the pinned core:

1. `MiscHandler.cpp:267` — `SendPacket(logoutResponse.Write())` queues `SMSG_LOGOUT_RESPONSE` on the instance
   socket's `_bufferQueue`.
2. `MiscHandler.cpp:278` — the instant case calls `LogoutPlayer(true)` before the socket's `Update` moves the
   queue.
3. `WorldSession.cpp:766` — `SMSG_LOGOUT_COMPLETE` is queued on the realm socket.
4. `WorldSession.cpp:789` — the instance socket is closed with `CloseSocket`.
5. `Socket.h:228-240` — `CloseSocket` shuts the send side down; it does not drain `_bufferQueue`, so the queued
   response is discarded.

A client that requires the response (as this client did before the earlier fix) fails in 15 of 30 runs. The core
fix would be to flush the instance socket before closing it (or close it with `DelayedCloseSocket` after moving
`_bufferQueue` to the write queue), or to send the response on a socket it does not close. The client-side
handling of the same sequence is the code above; no core file was changed in this round.

For a client, the sequence is legal: the completion proves the logout, and the response is optional. Nothing in
this round weakens a real error: a realm socket that closes before the completion, a silent realm, and a refusal
are all still errors.
