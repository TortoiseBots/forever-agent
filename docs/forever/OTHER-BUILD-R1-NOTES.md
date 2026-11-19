# Other-build round: client behaviour when the realm's build differs from the client's default

This round makes the client work against a realm that advertises a build other than the one the client claims by
default: a character may log in already grouped, and an area with many units exercises more update-block kinds than
a quiet one. The changes are client-side only; every check ran against the fake world and login servers in
`internal/forever/...`.

## 1. The Windows build failed

`cmd/forever-agent/raid_autopilot_cmd.go` used `syscall.Flock`, `LOCK_EX`, `LOCK_NB`, `syscall.Kill`,
`Setsid` and `Setpgid`, and `raid_walk_cmd.go` used `Setpgid`; none of that exists in the Windows `syscall`
package, so `GOOS=windows go build ./cmd/forever-agent` failed on every Unix-only call in those two files.

The Unix-only pieces now sit behind build tags in two small files:

- `cmd/forever-agent/autopilot_procctl_unix.go` (build tag `unix`): `lockWalkDir` (flock `EX|NB`),
  `detachSupervisor` (`Setsid`), `setProcessGroup` (`Setpgid`), `pidAlive` (`kill(pid, 0)` with the group form) and
  `signalProcessGroup` (`kill(-pid, sig)`). Behaviour on Linux is unchanged: same flags, same calls.
- `cmd/forever-agent/autopilot_procctl_other.go` (build tag `!unix`, which includes Windows): every one of those
  returns one error, `not supported on Windows: the raid walk commands need Unix process groups and flock (the
  forever commands work)`. So `raid-autopilot` and `raid-walk` refuse at run time with a clear message instead of
  being broken binaries, and the two code paths that used to set `SysProcAttr` inline report that error through
  their existing failure paths (`autoLock`, `autoDetach`, `launch`, `startSlotAgent`).

Nothing under `forever ...` used any of it: the login, world, walk, fight, chat, party and command channels build
and work on Windows and macOS (`GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...` and `GOOS=darwin
GOARCH=arm64 CGO_ENABLED=0 go build ./...` both succeed; the navmesh engine is cgo and is not built there).

The test files that drive Unix process and signal machinery (`raid_autopilot_cmd_test.go`, the teardown fixture in
`teardown_cmd_test.go`, `login_safety_test.go`, and `lease_reap_cmd_test.go` through that fixture) carry the `unix`
build tag. The one otherwise-portable CLI test that used the teardown fixture moved to `cli_args_unix_test.go`, and
the login helpers the portable CLI tests share moved to `login_helpers_test.go`, so the Windows vet of the package
still type-checks everything that has no Unix call in it.

Check: `make cross` compiles the whole tree for `windows/amd64` and `darwin/arm64`, and then vets the `cmd/...` and
`internal/forever/...` packages for `windows/amd64` as well (compile and vet only, no binaries kept). That vet is
what catches a test that uses a Unix-only syscall; a plain cross build does not compile test files. The whole target
takes about 12 s with a warm build cache here (2 cores), under a minute. `.github/workflows/test.yml` runs it as its
own step. The committed `autopilot_procctl_unix_test.go` pins the lock's exclusivity, `Setpgid` on the walk and
`Setsid` on the supervisor.

## 2. The default client build was refused

The flags defaulted `--build` to 70938 (a build the client has tables for), and a realm that accepts a different
build answers the realm list with `flags: 1` (VersionMismatch) and would refuse the join: the error came from
`internal/forever/bnet/bnet.go`, `realm "..." does not accept client build 70938 (the realm is build N)`.

Where the checks come from:

- The realm list shows the realm's own build and sets `VersionMismatch` when it does not accept the claim
  (`src/server/shared/Realm/RealmList.cpp:280-315`, `RealmAcceptsBuild` at `:257`).
- Both the realm list and the join read the build of the **logon**, not of the realm list ticket
  (`GameUtilitiesService.cpp:312` and `:344` pass `session->GetBuild()`; the ticket's client info carries only the
  variant and the secret, `:263-291`). So a retry must be the whole login again, not a re-request.
- A realm can accept more than one client build; the builds it accepts are its own and any extra ones it is
  configured with. With no extra build configured, the only build a realm admits is its own.

New rule: `--build` not given means `Options.ClientBuild = 0`, and `bnet.Login` claims the build the realm
advertises. It logs in once claiming 70938 (the build the client's tables are written at, `wire.Build70938`); if the
realm list answers a version mismatch it logs in again claiming the advertised build, and the world socket uses
both builds from the result (`Result.ClientBuild`, `Result.RealmBuild` — the sites in `session.go` and
`worldconn.go` previously assumed `RealmBuild == ClientBuild`). An explicit `--build` always wins and is claimed
exactly as given, so the mismatch error is still available.

Every command reaches the login through one path (`foreverCommon` in `cmd/forever-agent/forever_chat.go`):
`connectionFlags` registers `--build` with its 0 default, `login` is the only `bnet.Login` call, and `withLogin`
fills both builds from the result. login-check, world-check, walk-check, fight-check, chat-check, party-check,
cmd-check, addon-check, map-check, char-create, char-delete and death-check all use it (char screen the connection
half only); dungeon-run is dry-only this round and has no live login yet, so its placeholder `--build` stays 0.

Before claiming an advertised build the client checks it has tables for it: `wire.SupportedClientBuilds()` is
70707 and 70938 — 70707 is the base numbering the tables above are written at (below the 70755 and 70790
insert/remove lists) and 70938 is the `>= 70790` class. A build in the same class shares the opcode numbering
(70888 is in 70938's class per `PROTOCOL-LOGIN-VERIFY.md`), but the client has no wire tables for it, so it is not
claimed. Any other advertised build is refused with `realm "..." is build N, which this client has no opcode table
for (supported builds: 70707 and 70938); pass --build N to claim a build anyway`, which names the supported builds.

An automatic login takes the realm the realm list names first among the online ones, whatever that realm's build
is: an offline realm (population state 0) is only the fallback when every realm is offline, and the build of the
realm that was chosen is the one the second logon claims. A list with several realms on different builds therefore
joins the first online realm, not the realm whose build happens to match the probe.

Tests (fake login server, `internal/forever/bnet`): the fake now derives the realm list's flags and shown build
from the logon build, like `FillRealmEntry`, and records every logon build. `TestLoginAdvertisedBuild` runs against
a realm that accepts 70707: the first logon claims 70938, the second 70707, and the result carries 70707 twice.
`TestLoginAdvertisedBuildUnsupported` gets a realm on an unsupported build and asserts the refusal names both
supported builds and that no second logon is made. `TestLoginBuildMismatch` now pins `--build 70938` explicitly and
still gets the mismatch error. `TestLoginFirstOnlineRealmOfSeveral` puts two online realms in the list on different
builds and asserts the first one is joined, with its build.

## 3. The party roster arrived before this character's own GUID

A character that logs in already grouped gets `SMSG_PARTY_UPDATE` before anything names its own GUID (the
`ThisIsYou` create block or `SMSG_MOVE_SET_ACTIVE_MOVER`), so `party.State.validateRoster` refused a roster the
server sent in a legal order and the run said `party: invalid roster: this character's own GUID is not known yet`
and `party: no group (...)`.

`party.State` now holds one such roster (`held`) instead of judging it: the roster is not counted as invalid, not
actionable, and is applied by `SetSelf` when the GUID arrives; a newer roster replaces the held one, and a group
destroyed update drops it (so a stale roster can never be applied after the group it named is gone). A roster that
arrives after the GUID is known is validated exactly as before, so a roster that does not list this character is
still refused. `HeldRosters` counts the held rosters for the report. `State.DropHeld` discards it at a map change
(`world.applyNewWorld`): a held roster read before `SMSG_NEW_WORLD` belongs to the map the character left, and the
server sends the party update again after the new world (MAPCHANGE-R1-NOTES.md), so applying it would act on a
stale group.

Tests: `TestRosterValidation/self unknown holds the roster` applies two rosters before `SetSelf`, asserts neither is
counted invalid and that `SetSelf` applies the newer one (leader, members, `MyIndex`), and that no invalid roster is
counted for the legal order. `TestRosterDestroyedThenSetSelf` holds a roster, applies the group-destroyed update,
then calls `SetSelf`: the party is not resurrected (`InParty` false, no members, no held roster left).

## 4. "N object(s) partial": which blocks the decoder does not finish

"Partial" means a block that named the object was not decoded to its declared end. An area with many units, and so
with many vendor and player fragments, exercises more of these blocks than an empty one. Two causes:

- **A creature marked as a vendor carries the vendor fragment** (wire id 18, `VendorData`:
  `src/server/game/Entities/Object/Updates/UpdateFields.cpp:9254-9277` — one int32 in a create, a two-bit mask
  flushed then the int32 in a values update). It was not walked, so every such create counted as partial. It is now
  decoded in both forms (`readVendorCreate`, `readVendorUpdate` in `internal/forever/objects/update.go`, kept in
  `Info.HasVendor`/`Info.VendorFlags`). A body that ends inside the fragment is a parse failure (`short read`), not
  a complete block with zero flags: both readers check the buffer after every read, and so does the whole walk for
  the flags, the id list and the existence bytes.
- **The object type's own data after the walked blocks**: a player's (also the receiver's active player)
  `PlayerData`/`ActivePlayerData`, an item's or container's `ItemData`, an area trigger's own data. Those tables
  are defined at our pinned commit, but they are hundreds of mask-driven fields with no consumer in this client
  yet; they stay undecoded, the block is still consumed to its declared size (so the next block is never
  misread), and the run now says so.

So that a report no longer gives only a number, the model now counts partial blocks by a closed key (object type,
create or values update, and where the walk stopped: an undecodable block with its reason, a named fragment, or
"the type's own data after the walked blocks") and both the run log and `walk-check`'s summary print them. The counts
below are illustrative, not a measurement:

```text
world: 8 object(s) partial: a block naming them was not decoded to its declared end
  partial: player create: the type's own data after the walked blocks, not decoded this round    6
  partial: player values update: the type's own data after the walked blocks, not decoded this round    1
  partial: item create: the type's own data after the walked blocks, not decoded this round    1
```

If a report shows an **unknown fragment** or an **undecodable block**, that *is* a layout difference: record it
with the fragment id or the reason and the object type, and get that build's source. Nothing decoded so far needs
it.

Tests: `TestVendorFragment` pins both vendor forms (create, update with and without a value) and that a fragment id
this round does not walk is named and stops the walk safely. `TestVendorTruncated` cuts the create and the update
body short and asserts the block is reported partial with `short read`, never complete.

## 5. EOF on the instance socket during logout, and the walk's final facing

Server facts (`src/server/game/Server/WorldSession.cpp`):

- `SMSG_LOGOUT_RESPONSE` is an instance-socket opcode and `SMSG_LOGOUT_COMPLETE` a realm-socket one
  (`Protocol/Opcodes.cpp:1938-1939`).
- `HandleLogoutRequestOpcode` queues the response on the instance socket, then (an instant logout) calls
  `LogoutPlayer` (`Handlers/MiscHandler.cpp:243-283`).
- `LogoutPlayer` sends `SMSG_LOGOUT_COMPLETE` on the realm socket (`WorldSession.cpp:766`) and then closes the
  instance socket (`:789`, unconditionally). A logout that is not instant completes after the 20 s timer
  (`ShouldLogOut`, `:599-601`).

So the server does close the instance socket as part of every logout, right after the completion is queued, and the
response it queued on that socket first can be dropped with the close — which is the symptom when the client sees
EOF on the instance socket and no response line.

The client treated that close as link death: `setLinkDead` marked the link lost unless the *response* had been
handled, and the logout loop made both sockets fatal until then, so `logout()` returned `ErrLinkLost` and `Enter`
aborted with `logout not possible`. Now:

- The logout-requested state is set **before** `CMSG_LOGOUT_REQUEST` is written (`logoutSent`), so a close that
  races the send is already the session ending, not a lost link; `logout()` waits with the instance side ignored.
- What it waits for is `SMSG_LOGOUT_COMPLETE` on the realm socket, bounded at 30 s (`logoutWait`), which covers
  the 20 s non-instant completion. The realm socket closing before the completion, or a silent realm, is
  `ErrLinkLost`; a realm that stops sending during the wait ends it at the silence bound (30 s) with
  `ErrLinkLost`, never with an empty event (`TestLogoutSilentRealmIsALoss`). The logout *response* alone is not the
  session's end, so a realm socket lost after the response is still a loss
  (`TestLogoutRealmLossAfterTheResponseIsStillALoss`).
- A refusal is an error of its own: `SMSG_LOGOUT_RESPONSE` with a non-zero result ends the run with
  `the server refused the logout: result N` (`TestLogoutRefusedByTheServer`); the server does not send the
  completion then, and the client does not wait for one.
- Only the completion marks `logoutComplete`, and only then is a close of either socket teardown rather than a loss.
  A close that races the already-queued completion is forgotten when the logout returns cleanly
  (`clearTeardownLoss`), so the run never reports a completed logout as a lost link.
- The response is given 500 ms after the completion and may never come: the run ends clean and says
  `SMSG_LOGOUT_COMPLETE (no SMSG_LOGOUT_RESPONSE: the server closes the instance socket with the completion)`.
- An EOF with **no** logout requested (during the stay, a fight, a walk, a map change) is unchanged: `ErrLinkLost`.

Tests against the fake (`internal/forever/world`, whose fake now sends no response in this mode and closes the
instance socket with the completion, like the server): `TestLogoutInstanceCloseIsNotLinkLoss` (no error, completion
seen, no link-loss report, the run says the response was absent), `TestLogoutRealmLossBeforeCompletionIsAnError`
and `TestLogoutRealmLossAfterTheResponseIsStillALoss` (realm dropped during the logout → `ErrLinkLost`, no
completion), `TestLogoutSilentRealmIsALoss` (instance closed, realm silent → `ErrLinkLost` naming the realm, no
zero packet handled) and `TestLogoutRefusedByTheServer` (a non-zero result ends the run with the refusal error).
The existing link tests still pin the instance close with no logout requested as `ErrLinkLost`.

The close is the server's own socket close as part of the logout, not link death. If a build ever keeps the
instance socket open through the logout, the waits above still work (the instance side is merely never used again),
so nothing else would change.

**Orientation after walk-check.** A walk that goes out and back ends facing the way back (the last leg's
direction), and the server saves a character's orientation with it. That final facing packet was not intended to be
the last word, so `walk-check` now sends one more `CMSG_MOVE_SET_FACING` at the spawn with the orientation the
create block gave, and its log line says both (`orientation restored to X (the way back faced Y)`). Test:
`TestWalkCheckAgainstFakeServer` asserts the restore packet (a third `SET_FACING` after both legs) and that
`Report.BackAt`'s orientation equals `Report.WalkFrom`'s.

## 6. fight-check's level window

The geometric target filter was fixed at level 1-3 (the round-4 brief's window, written for a starting-area
character), so a character above it found nothing and the message said `no living creature of level 1-3 within
40 yd`. The band is now relative to the character's own level (`fightLevelWindow`,
`internal/forever/world/fightcheck.go`): the character's level −3 to +2, clamped to the client's 1-60. A level-1
character therefore keeps the old 1-3; a level-30 one looks for 27-32. `--level-min`/`--level-max` win over either
end and are validated (0 = "from the character's level", at most 60, min not above max), and `--entry` still
overrides the band entirely. A single explicit bound that lands past the automatic one on the other side (for
example `--level-min 40` on a level-1 character, whose automatic maximum is 3) does not leave an empty band
silently: the automatic end moves to the explicit bound, so the band is 40-40. The run prints the band before it
searches and the empty-search message repeats it:
`fight-check: no living creature of level 27-32 within 40 yd (the walk looked to 200 yd)`.

Test: `TestFightLevelWindow` pins the band for levels 1, 3, 30, 60 and an unknown level (1-3), both explicit
bounds, and one explicit bound past the automatic other end (widened, not empty), and the candidate-filter test
pins that the band parameter includes and excludes the right unit.

## What to report next time

Run `forever world-check`, `walk-check` and `fight-check` with no `--build` and report: the two new login lines
(`realm list: ... build N` and `client build: claimed N, the realm list shows N`), whether the logout ends clean or
names `no SMSG_LOGOUT_COMPLETE`, the `partial:` lines with their counts, `walk-check`'s
`orientation restored to ...` line, and the level band line of `fight-check`. If a partial line names a fragment
(or an undecodable block), that session's packets are the ones to keep.
