# Bugs round 2: the automatic build claim and the walk-check settle

Two failures from a run against a server on a newer client build. Both are client bugs: the login picked a build
the server does not fully accept, and a long walk-check settle was cut off by the run's own deadline.

## 1. The automatic build claimed an older build

**What happened.** With no `--build`, the client claimed 70938 and the step log said the realm list showed 70938, so
the login looked accepted; the server only admitted the run with `--build 70959`. The client claimed 70938 because it
had no other build in its automatic list.

**What the realm list's build field means** (`src/server/shared/Realm/RealmList.cpp`, read-only reference):

- `FillRealmEntry` shows `clientBuild` (the claim) when `RealmAcceptsBuild(realm, clientBuild)` is true, and
  `realm.Build` with `RealmFlags::VersionMismatch` when it is false (`:294-311`).
- A realm accepts its own `realmlist.gamebuild` and any build in `Realm.AlsoAcceptBuilds`
  (`RealmAcceptsBuild`, `:255-279`). An also-accepted build is listed with the client's own version and **no**
  mismatch flag.
- The realm list and the realm join read the build of the logon, not the realm list ticket
  (`GameUtilitiesService.cpp:312,344` pass `session->GetBuild()`).
- The world socket looks the claimed build up in `auth.build_info` and its `build_auth_key`
  (`WorldSocket.cpp:739-772`); a build that the realm lists but that has no row there fails at the world socket.

So the build field is only a hint: for an accepted claim it echoes the claim, and it cannot say whether the world
server has build info and keys for that build. A realm on 70959 that also accepts 70938 lists 70938 for a 70938
claim, and the world server then has no 70959-style build row for it. That is the shape the retest had: the client
kept the echoed 70938 and the run only worked with `--build 70959`. The world socket's error code was not part of the
retest output, so the refusal itself is inferred from `WorldSocket.cpp:739-772`, where a build without build info or
a build auth key is refused with `ERROR_BAD_VERSION`.

**Fix** (`internal/forever/wire/opcodes.go`, `internal/forever/bnet/bnet.go`):

- The builds this client knows are 70707, 70938 and 70959 (`wire.Build70959`); 70959 is in 70938's class (both
  `>= 70790`, the same opcode numbering) and a live run at 70959 passed.
- With no `--build`, `Login` claims them newest first. A VersionMismatch names the realm's own build; when that
  build is one this client knows it is tried next, otherwise the remaining known builds are tried newest first.
  The first claim the world server accepts is used. `Result.ClientBuild`/`Result.RealmBuild` therefore carry 70959
  against a realm that accepts it, not the echo of an older claim.
- If the realm accepts none of them, the error names both sides: the realm's build and the client's builds
  (`realm "..." accepts none of the client builds this client knows (70707, 70938 and 70959); the realm is build N`).
- An explicit `--build` is still claimed exactly as given and never replaced.

**Round 2 review: the realm list is not the world server's build list.** The fix above could still stop at a build
the realm lists but the world server refuses (`WorldSocket.cpp:739-763`, `ERROR_BAD_VERSION`), because the realm
list carries no build_info information. `bnet.Options.WorldAccept` now lets `Login` authenticate the world socket
inside the selection loop: a `*worldconn.Refused` with `BadVersion()` set (the typed SMSG_AUTH_RESPONSE refusal from
either handshake stage) closes that attempt's socket and the loop claims the next known build, newest first, bounded
by the known list. Any other world-auth error ends the login. When every build is refused, the error names each
build tried and which side refused it
(`no client build this client knows was accepted (build 70959: the world server refused it; ...), pass --build N ...`).
A pinned `--build` is probed once and its refusal is the login's error.

The command layer's `worldAccept` (`cmd/forever-agent/forever_chat.go`) is passed by every command that opens a
world socket (entry and character screen) and left nil by `login-check`, which stops at the realm list. The
authenticated socket comes back in `bnet.Result.WorldConn` and `world.Options.WorldConn` makes `world.Enter` and the
character screen adopt it, so the accepted build is authenticated once rather than probed and authenticated again.

**Tests** (`internal/forever/bnet`): the fake realm list now models `Realm.AlsoAcceptBuilds` (`fakeRealm.alsoAccept`).
`TestLoginPrefersNewestKnownBuild` serves a realm on 70959 that also accepts 70938: the login claims 70959 in one
logon. On the old code the probe claimed 70938, the fake accepted it and echoed it, and the result carried 70938.
`TestLoginAdvertisedBuild`, `TestLoginFirstOnlineRealmOfSeveral` and `TestLoginAdvertisedBuildUnsupported` are
updated to the new logon order (the realm's build from the mismatch is tried next: 70959 then 70707; three logons
when the realm's build is unknown).

The world-accept tests (`internal/forever/bnet/worldaccept_test.go`) use the fake login service and a fake world
socket (`fake_world_test.go`) that refuses a build with an unencrypted SMSG_AUTH_RESPONSE `ERROR_BAD_VERSION` or
finishes the encrypted handshake. `TestLoginWorldRefusesNewestBuild` (realm accepts 70959 and 70938, world knows
only 70938) claims 70959, is refused, and ends on 70938 with that build's socket handed back.
`TestLoginWorldRefusesEveryBuild` is refused for all three and the error names each one;
`TestLoginPinnedBuildWorldRefusal` pins 70959 and gets the world's refusal without a retry.
`internal/forever/world/.../TestEnterAdoptsTheProbedWorldSocket` sets a dead world address and enters through the
probed socket only.

## 2. `walk-check --stay` died at the run deadline

**What happened.** `walk-check --stay 290s` ended after about two minutes with `worldconn: reading a frame header:
i/o timeout` while the server still sent time syncs. `world-check --stay 5m` held.

**Cause** (`internal/forever/world/session.go`, `internal/forever/worldconn/frames.go`):

- `walk-check`'s `--stay` is `Options.Settle` (the settle before the walk), not `Options.Stay`. `Enter` defaulted the
  whole run to `2 minutes + Stay`, so a 290 s settle had a 2-minute run: the session context expired in the middle of
  it.
- Every socket read was armed with that absolute context deadline (`operationDeadline` returned `ctx.Deadline()`).
  A frame that arrived did not extend the next read: each read got the same fixed time, so the read that was waiting
  when the run deadline arrived returned an i/o timeout (sometimes before the context watcher reported a deadline).
  That is why the message named a frame header and not the run deadline.

**Fix:**

- `Enter` defaults `Deadline` to `defaultRunOverhead + Stay + Settle` (`defaultRunDeadline`); the settle is a stay
  and counts. `defaultRunOverhead` is a variable (like the character-screen bounds) so a test can shrink it and run
  the real path in milliseconds.
- `operationDeadline` is now always `now + defaultIOTimeout` for one operation; the context still ends a blocked
  read or write through its watcher, but the socket deadline is never the connection's absolute end. Each frame
  therefore extends the wait for the next one.

**Tests:**

- `internal/forever/world/.../TestWalkCheckSettleHoldsWithOnlyTimeSyncs`: the fake server runs the walk scenario
  and sends a time sync every 20 ms; the walk-check settles 3 s with the run overhead shrunk to 2 s (the settle
  stands in for the five-minute stay). The run completes the settle, walks, and logs out. With the old deadline
  (overhead + Stay only) the settle dies at 2 s.
- `internal/forever/worldconn/.../TestReadDeadlineExtendsWithEveryFrame`: a recording socket checks that the read
  deadline armed for each frame is about the per-operation `defaultIOTimeout`, not the context's 300 ms end.

## Verification

- `make fmt-check`, `make cross`.
- `vm-test ta` (unit tests under the race detector plus vet), `staticcheck`, the race run.
