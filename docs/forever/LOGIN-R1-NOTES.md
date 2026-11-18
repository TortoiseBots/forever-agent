# Forever login, round 1 notes

Scope: REST login (SRP + login ticket) and the Battle.net RPC login up to and including the realm join answer
(join ticket, server addresses, 32-byte join secret). The world socket is not part of this round.

Status: complete against the live login server. `forever-agent forever login-check` reaches the join answer; the
output is in the final report. Code: `internal/forever/bnet`, command: `cmd/forever-agent/forever_login.go`.

## What works

* `GET /bnetserver/login/` gives the form and its `srp_url`; the SRP request and proof run over HTTPS with the
  server's certificate checked against a root CA file (no `InsecureSkipVerify`).
* SRP6 v2 (PBKDF2-HMAC-SHA512, 15000 iterations) and v1 (SHA-256, password uppercased) are implemented exactly as
  `BnetSRP6v1Base/BnetSRP6v2Base` verify them; the server accepts our `M1` and its `M2` matches ours.
* The login ticket is used as `cached_web_credentials` in `authentication.v1.LogonRequest`; the `OnLogonComplete`
  push (64-byte session key, account ids) is read.
* `Command_RealmListTicketRequest_v1_b9` (identity + client information with the 32-byte secret),
  `GetAllValuesForAttribute` for the sub-regions, `Command_RealmListRequest_v1_b9` (compressed JSON) and
  `Command_RealmJoinRequest_v1_b9` (realm address) all work, including the zlib/4-byte-length blob format.
* The join answer is parsed into `bnet.Result`: join ticket JSON (76 bytes on the live server), the world server
  address from `Param_ServerAddresses`, and the 32-byte join secret. The 64-byte bnet session key, the 32-byte client
  secret and the login ticket are kept in the same struct for the world-socket round; the world session key is not
  derived here.

Live run (2026-11-18, login server on the build VM, tunnel):

```
login form: LOGIN_FORM, SRP form at /bnetserver/login/srp/
srp challenge: version 2, SHA-256, 15000 iterations, modulus 256 bytes, salt 32 bytes
srp proof: client evidence M1 32 bytes accepted, server evidence M2 32 bytes verified
login ticket: 43 bytes
rpc connect: server id 1/1794972337, server time 1794975225932, ciid "000000016AFD1AB1-000000076AFD25F9", bindless true
logon: build 70707, account id 2, 1 game account(s), session key 64 bytes
realm list ticket: 20 bytes
sub-region: 70-1-70
realm list: realm 70 "Trinity", address 0x46010046, build 70707, population 1, flags 0x00
realm join: join ticket 76 bytes, world server 127.0.0.1:8085, join secret 32 bytes
realm: Trinity (id 70, address 0x46010046, world 127.0.0.1:8085)
```

## Where `PROTOCOL-LOGIN.md` was wrong or silent

The corrections of `PROTOCOL-LOGIN-VERIFY.md` are applied to `PROTOCOL-LOGIN.md` in place (compression stream and
checksum inputs, salt byte order, client opcode rule, server opcode step 1, session-key DB statement, generator
`"02"`). Beyond those, this round found:

1. **Command attribute names need a trailing suffix.** `ParseParamName` removes everything after the last `_` of any
   name starting with `Command_` (`src/server/bnetserver/Services/GameUtilitiesService.cpp:36-46`), so the bare
   `Command_RealmListRequest_v1` becomes `Command_RealmListRequest` and matches nothing. A client must send e.g.
   `Command_RealmListRequest_v1_b9`; the document wrote `Command_RealmListRequest_v1*`, which hides this. All
   `Command_*` attributes here carry `_b9`.
2. **There is no per-IP connection limit on the REST login port.** `Network.ConnectionsPerIp` is enforced by
   `Battlenet::Session` and `WorldSocket` only (`src/server/bnetserver/Server/Session.cpp:129-137`,
   `src/server/game/Server/WorldSocket.cpp:134-141`). The document claimed `LoginHttpSession::CheckAccess()`.
3. **`Set-Cookie` is always `Secure`**, also on a plain-HTTP listener (`src/server/bnetserver/REST/LoginHttpSession.cpp:60-72`),
   and it carries a `Domain` attribute taken from the request `Host`. A client that dials an IP but sends the
   certificate name as `Host` must keep the cookie for that name (this client does).
4. **Launcher endpoints** return `{"token","expires"}`, `{"login_ticket","account_id","email","game_accounts"}` and
   `{"ok":true}`; there is no `RL-` prefix and no `remember_token`/`forgotten` field
   (`src/server/bnetserver/REST/LoginRESTService.cpp:526-624`).
5. **No build check in the realm list ticket request.** The handler only needs a 32-byte client secret and a known
   game account id; the build is checked later against the realm (`RealmAcceptsBuild`).
6. **`CompressJson` is zlib (RFC 1950), not raw deflate**, with a 4-byte little-endian length that counts the trailing
   NUL (`src/server/bnetserver/Services/GameUtilitiesService.cpp:48-60`). `Param_RealmJoinTicket` is the exception:
   plain JSON, no prefix and no compression.
7. **The login form's `srp_url` advertises the server's configured port**, not the port the client reached it on
   (the form shows `:8081` through a tunnel to `:18081`), so a client must keep its own REST address and only take
   the path.
8. **The evidence padding applies to `M1` too.** `CalculateServerEvidence` hashes `M1` through
   `GetBrokenEvidenceVector`, which is big-endian `(bits+8)>>3` bytes; a 32-byte `M1` whose top byte is below `0x80`
   therefore gets a leading zero byte. The document says `BrokenVec(M1)` correctly, but it is the trap that first
   broke this client's `M2`; the vectors in `testdata` pin it.
9. **A realm only admits the client builds it accepts, and the login does not tell you the realm's build up front.**
   `JoinRealm` checks `RealmAcceptsBuild(realm, session->GetBuild())`
   (`src/server/shared/Realm/RealmList.cpp:255-279,446-456`). The realm's build is `realmlist.gamebuild` (70707 on
   the live server), and the build comes from `LogonRequest.application_version`. Claiming 70938 while the realm is
   on 70707 lists the realm with `flags: 1` (version mismatch) and the join answers
   `ERROR_USER_SERVER_NOT_PERMITTED_ON_REALM` (`0x800000E1`). `--build` (and `Options.ClientBuild`) exist for this;
   the default stays 70938, the build the protocol document targets.
10. **An offline realm is still listed.** With `realmlist.flag = 2` the realm appears with `populationState: 0`
    (Offline) and the join is refused the same way; once the world server writes the row, the same login succeeds
    (observed: flag 2/served at first, then flag 0 and the join went through). A client should treat
    `populationState == 0` as "not joinable yet", not as "protocol error".

## Why the protobuf messages are hand-written

`protoc` can be installed without sudo, but there is nothing to generate this round's code from: the core ships the
RPC protocol only as generated C++ (`src/server/proto/Client/*.pb.cc/.pb.h`). The `.proto` sources under
`src/server/proto` are `Login/Login.proto` and `RealmList/RealmList.proto` — both are protobuf-JSON payloads here
(the REST form and the realm list, parsed with `encoding/json`), so generated Go for them would be dead weight. The
RPC messages (`bgs.protocol.Header`, `EntityId`, `Variant`, `Attribute`, `ConnectionService`, `AuthenticationService`,
the `GameUtilities` requests/responses) are therefore transcribed by hand into `internal/forever/bnet/proto.go` with a
small `protowire`-based encoder; each one has byte fixtures in `rpc_test.go`, hand-checked field by field against the
generated C++ description, and the live server accepts and answers them. If a later round needs many more RPC
messages, extracting the `.proto` files from the embedded descriptors and running `protoc-gen-go` becomes worthwhile.

## How the SRP vectors were made

`internal/forever/bnet/testdata/srp-vectors.json` comes from the server's own implementation, not from the Go code:

* `tools/forevervec/srp_vectors.cpp` is a small C++ program, built by hand (not by CI; the exact `g++` line is in its
  header) against the ForeverCore checkout at commit `6517af05`. It links `SRP6.cpp`, `BigNumber.cpp` and
  `CryptoRandom.cpp` and stubs only `Trinity::Assert`/`GetDebugInfo`, which live in `Errors.cpp` and pull in `fmt`.
* Fixed inputs: login `VECTOR@LOCAL`, password `vector-password`, a fixed 32-byte salt, a fixed client private `a`.
  For version 1 the vector uses the uppercased password, because client and server uppercase it
  (`LoginRESTService.cpp:346-349`).
* The verifier is derived with the core's `CalculateX`/`CalculateVerifier` formulas and then checked with the
  server class's own `CheckCredentials`. `BnetSRP6v2<SHA256>`/`BnetSRP6v1<SHA256>` produce `B` (their `b` is random,
  so `B` differs between generations of the file); the program computes `A`, `S`, `M1`, `M2` as a client does, calls
  the server's `VerifyClientEvidence(A, M1)` and requires the returned session key to equal its `S`, then calls
  `CalculateServerEvidence` and requires that `M2` equals its own. A vector only reaches the file when the server's
  code accepted it.
* The Go test `TestSRPVectors` recomputes the verifier from the same password (that pins the password derivation),
  then `A`, `S`, `M1`, `M2`, and checks `server_evidence_M2`. A wrong password test makes sure the evidence changes.

## Tests

* `TestSRPVectors`, `TestSRPWrongPassword`, `TestSRPV1PasswordIsUpperLatin`, `TestSRPRejectsWrongGroup`.
* Byte fixtures for `Header` (request and response), `ConnectRequest`, `ConnectResponse`, `LogonRequest`,
  `LogonResult`, `Variant` (string/blob/bool/uint), `Attribute`, `ClientRequest`, `ClientResponse`,
  `GetAllValuesForAttributeRequest/Response`, plus frame round-trips on `net.Pipe` and the frame size/empty-header
  rejections.
* `TestLoginAgainstFakeServer`: `httptest` TLS REST endpoint with its own CA (the client verifies it, nothing is
  skipped) plus a TLS listener speaking the frame format; the fake server verifies `M1` with its own SRP math and the
  vector's verifier, so the whole sequence runs in `go test`. `TestLoginWrongPassword` covers the refusal.
* `TestLiveLogin` runs against a real server only when `FOREVER_LIVE_*` is set and skips otherwise.

## Open questions for the world-socket round

1. **World build and auth key.** With `Network.SkipBuildAuthKeyCheck = 1` the `CMSG_AUTH_SESSION` digest is skipped,
   but a `build_info` row for `account.client_build` is still required (`WorldSocket.cpp:739-746`). The join ticket
   carries the variant (`platform`, `type`, `clientArch`) that this client claims; whether `Win/x64/WoW` has a
   `build_auth_key` row for 70707/70938 in the live DB was not checked.
2. **Opcode numbering for 70707.** `ClassicOpcodes` treats every build `>= 70790` alike; 70707 is below that. The
   world round must confirm the exact table for the build the realm accepts (the `.inc` client table and the
   `Build70755/70790` steps) instead of assuming the 70938 numbers.
3. **`DosResponse`.** The server never checks it (`WorldSocket.cpp:686-1064`); this client will send zeros like the
   core's own headless client (line 355) until a real client capture says otherwise.
4. **World session key.** `keyData` (64 bytes = our 32-byte client secret + the 32-byte join secret) plus the two
   challenges feed `SessionKeyGenerator`; not derived in this round by design.
5. **`SMSG_ENTER_ENCRYPTED_MODE` signature.** The 64-byte Ed25519 signature over `HMAC(encryptKey, 0x01 ||
   EnableEncryptionSeed)` cannot be verified without the server's public key; the world round has to decide whether to
   verify it and where the key would come from.
6. **The value of a `Command_*` attribute.** For `RealmListTicketRequest` and `RealmJoinRequest` the server reads only
   the attribute name (and `Param_RealmAddress`); this client sends the strings `"AuthRealmListTicket"` and
   `"AuthRealmJoinTicket"`, which the server ignores. Only a real client capture can confirm what the game puts
   there. `Command_RealmListRequest_v1` carries the sub-region, which is read.
7. **`ciid` in request headers.** Responses always carry the `ciid` the server assigned in `ConnectResponse`; this
   client does not echo it in later request headers, and the server does not read it. A capture would settle what the
   real client sends.
