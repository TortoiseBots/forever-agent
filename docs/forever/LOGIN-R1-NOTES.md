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
rpc connect: server id <server id>, server time <server time>, ciid <ciid>, bindless true
logon: build 70707, account id 2, 1 game account(s), session key 64 bytes
realm list ticket: 20 bytes
sub-region: 70-1-70
realm list: realm 70 "<realm>", address 0x46010046, build 70707, population 1, flags 0x00
realm join: join ticket 76 bytes, world server 127.0.0.1:8085, join secret 32 bytes
realm: <realm> (id 70, address 0x46010046, world 127.0.0.1:8085)
```

The `rpc connect` line prints lengths only now (`server id label 1 digits, epoch 10 digits, server time 13 digits,
ciid 33 chars, bindless true`); the placeholders in the transcript mark the values of that run, which are not in the
notes.

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

## Fix round (same round 1, after the two reviews)

1. **`server_evidence_M2` loses its leading zero bytes.** The server prints it with `BN_bn2hex`
   (`LoginRESTService.cpp:364`, `BigNumber::AsHexStr` in `BigNumber.cpp:210-216`), so one digest in 256 arrives as 62
   hex characters. `srpClient.verify` now decodes the value with `decodeServerEvidence`, left-pads it to the digest
   length (a zero value prints as `"0"`, an odd length) and rejects anything wider than the digest. The other
   number-as-hex values were checked for the same stripping: `modulus`, `generator` and `public_B` are parsed as
   numbers, so the stripped zeros do not change them; `salt` is `ByteArrayToHexStr` of the fixed 32-byte array and
   always 64 characters. `PROTOCOL-LOGIN.md` §2.3 now states this.
2. **Hostile challenges cannot reach a panic.** The generator must now be the group's `2` (`SRP6.cpp:30,33`); `B`
   must be 1..N-1 and its encoding no longer than the modulus, checked before `SetBytes`; `salt` stays exactly 32
   bytes; hex fields are length-bounded before parsing. `FillBytes` in `pad`/`broken` is only reached with values
   below N, and `randomScalar` returns an error instead of panicking. The new `FuzzNewSRPClient` and
   `TestSRPRejectsHostileChallenges` cover the paths.
3. **Edge vectors.** `tools/forevervec/srp_vectors.cpp` now searches, per variant, for `A`, `B` or `S` with a
   leading zero byte, an evidence value whose bit length is a multiple of 8, and an `M2` with a leading zero byte
   (304 attempts / 2.96 s for v2, 575 / 0.22 s for v1), and still lets the server's own
   `VerifyClientEvidence`/`CalculateServerEvidence` accept each one. The Go vector test consumes `M2` in the
   server's stripped text form.
4. **No secret in `%v`.** `Result`, `Options`, `LogonResult` and `srpClient` implement `String`, `GoString` and
   `Format` and print lengths only (`redact.go`); `live_test.go` prints counts instead of `%+v`. `TestSecretsAreRedacted`
   formats all of them with `%v`, `%+v`, `%#v`, `%s`, `%q` and `%x`.
5. **Server text in errors is bounded.** `sanitizeServerText` keeps printable ASCII, caps at 120 bytes and replaces
   a 32+ character hex/base64 run with `<redacted>`; REST failures carry the status and the server's error code;
   `LoginResult`'s `error_message` and `next_url` go through it too.
6. **One deadline per RPC call.** `rpcSession.call` and `waitLogon` pin one absolute deadline (the earlier of the
   context deadline and the per-call timeout), a context watcher sets a past socket deadline to unblock a read, and
   at most 64 ignored frames are allowed per call. `Options.Deadline` bounds the whole login (2 minutes by default);
   `TestCallSlowDripExpires`, `TestCallCancelledContextUnblocksRead`, `TestCallCapsIgnoredFrames` and
   `TestLoginOverallDeadline` cover it.
7. **Wire-type checks.** Every field is checked against the type its message declares before it is read; a nested
   message that fails to parse is an error instead of a zero value; a `Header` without `service_id` or `token` is an
   error; a game account id above `uint32` is an error in the realm list ticket request.
8. **The fake server is independent.** Its SRP arithmetic (`fakeSHA256`, `fakeBroken`, `fakePad`,
   `fakeEvidenceM1/M2`) no longer calls the client's helpers, `TestFakeEvidenceMatchesVectors` pins it against the
   C++-produced `M1`/`M2`, it writes its own JSON, parses the logon request field by field with `protowire`, and
   checks the service id and the token sequence of every request. `TestLoginAgainstVectorEvidence` goes further: the
   fake is pinned to one vector (challenge salt/B/version, and the vector's A/M1/M2), the client private is fixed to
   the vector's, and the whole `Login` must reproduce that vector — including both `m2-leading-zero` vectors, which
   is the end-to-end regression for the stripped `M2`.

## Fix round 2 (closing round)

1. **The v1 password test proves the folding now.** `TestSRPV1PasswordIsUpperLatin` used to call
   `upperOnlyLatin("vector-password")` itself, so deleting the folding in `srpPasswordKey` left it green. It now feeds
   the raw lowercase and mixed-case passwords to `newSRPClient` (the entry point `Login` uses) with the v1 vector's
   salt, B and private, and requires the vector's `M1` (the vector's `x` was derived from the uppercased password).
   Proof: with `password = upperOnlyLatin(password)` removed from `srpPasswordKey`, the test fails with
   `raw password "vector-password" gives M1 B481B021...66C76F09, want the vector's 428C336C...4AD1B7E6` (plus the
   `VECTOR-password` and `VeCtOr-PaSsWoRd` cases); restored, it passes. `TestSRPVectors` now compares `A`, `S` and
   `M1` as numbers, because the vectors store `BN_bn2hex` text (leading zero bytes dropped, first nibble kept).
2. **Server text is sanitised on every source/sink pair.** The sanitiser is `sanitizeServerText` (printable ASCII,
   `maxServerText` = 120 bytes, 32+ character hex/base64 runs replaced by `<redacted>`); JSON decode errors are
   rebuilt by `decodeError` (message offset and the Go type only, never the input); transport errors whose text can
   quote a certificate name go through `serverTextError`, which sanitises the message and keeps `Unwrap`. A server
   path is only used when it is printable ASCII and at most `maxServerPath` = 512 bytes. `Result.RealmName` and
   `Result.ServerIP` stay raw for the world socket; they are printed through the sanitiser (`Result` formatting, the
   step log, and the new `Result.Summary` the command prints on stdout). The audit table (42 rows):

| # | server-origin field | sink | treatment | pinned by |
|---|---|---|---|---|
| 1 | form `type` | error (`login form type %q`) | sanitise | hostile `form_type` |
| 2 | form `srp_url` (path) | step log (`SRP form at`) | printable+512, then sanitise | hostile `srp_url` |
| 3 | form `srp_url` (path) | REST request/status/transport errors | sanitise path / `serverTextError` | hostile `srp_url` |
| 4 | form `inputs[]`, `platform_id`, `program_id`, `version` | none (decoded, never printed) | not read further | - |
| 5 | challenge `hash_function` | error `%q` | sanitise + bound | hostile `hash_function`, `TestSRPChallengeTextIsSanitised` |
| 6 | challenge `hash_function` | step log | sanitise | code path after the check |
| 7 | challenge `generator` | error `%q` | sanitise + bound | `TestSRPChallengeTextIsSanitised` |
| 8 | challenge `modulus` | log length, errors | numeric only | - |
| 9 | challenge `username` | never printed (SRP input) | not a sink | - |
| 10 | challenge `salt` | error (byte count) | numeric only | hostile SRP cases |
| 11 | challenge `public_B` | error (byte count/range) | numeric only | hostile SRP cases |
| 12 | result `authentication_state` | error `%q` | sanitise | hostile `authentication_state` |
| 13 | result `error_code` | error | sanitise | hostile `error_code` |
| 14 | result `error_message` | error | sanitise | hostile `error_message` |
| 15 | result `next_url` | error | sanitise | hostile `next_url` |
| 16 | result `login_ticket` | step log (length), `Result` formatting | length / redaction | `TestSecretsAreRedacted` |
| 17 | result `server_evidence_M2` | error (one byte `%q`, byte count) | escaped byte / numeric | `TestSRPVerifyStrippedM2` |
| 18 | HTTP reason phrase `resp.Status` | error | sanitise | `TestRESTStatusPhraseIsSanitised` |
| 19 | non-200 response body | error | sanitise, 1 MiB read cap | `TestLoginHidesEchoedRequest`, status test |
| 20 | REST JSON decode error text | error | `decodeError`: offset + Go type | hostile `login_result_json`, 1 MB payloads |
| 21 | x509/TLS message (certificate names) | REST transport error | `serverTextError` (bounded, chain kept) | code path |
| 22 | REST path in a transport error | error prefix | sanitise | hostile `srp_url` |
| 23 | ConnectResponse `ciid` | step log | sanitise | hostile `ciid` |
| 24 | ConnectResponse ids/time/bindless | step log | numeric | - |
| 25 | LogonResult `error_code` | error | numeric | - |
| 26 | LogonResult account ids | step log, errors | numeric | - |
| 27 | LogonResult `battle_tag` | never printed; type formats | redaction | `TestSecretsAreRedacted` |
| 28 | LogonResult `geoip_country` | never printed; type formats | redaction | `TestSecretsAreRedacted` |
| 29 | LogonResult `session_key` | step log (length), type formats | length / redaction | `TestSecretsAreRedacted` |
| 30 | attribute names (`Param_*`, `Command_*`) | compared with constants; errors name the constant | not echoed | rpc tests |
| 31 | attribute values: realm list ticket, character count | presence + length | length | `TestLoginAgainstFakeServer` |
| 32 | attribute values: sub-region | step log, `Result` | sanitise | hostile `sub_region` |
| 33 | attribute value: realm list blob prefix | error `%q` | sanitise | code path |
| 34 | realm list JSON decode text | error | `decodeError` | hostile `login_result_json` (same wrapper) |
| 35 | realm entry `name` | step log, errors, `Result`, `Summary`, live test log | sanitise | hostile `realm_name` |
| 36 | realm entry id/address/build/population/flags | step log, errors | numeric | - |
| 37 | join ticket JSON decode text | error | `decodeError` | code path (same wrapper) |
| 38 | world server `ip` | step log, `Result`, `Summary`, live test log | sanitise | hostile `world_ip` |
| 39 | world server `port` | step log, `Result` | numeric | - |
| 40 | join secret | length only | length | `TestSecretsAreRedacted` |
| 41 | RPC frame/protobuf decode text | error | numeric field/wire/offset | rpc tests |
| 42 | unknown push service/method | rpc log | numeric | code path |

   `TestServerTextIsSanitisedInEverySink` poisons each REST/RPC string field with control bytes, an ANSI escape, a
   1 MB string and a 64-character token-like run, then formats the step lines exactly as the command prints them and
   checks every line: printable ASCII, at most 400 bytes, and no payload. `TestRESTStatusPhraseIsSanitised` covers
   the reason phrase (a Go test server always writes the standard one).
3. **Secret-holding types redact.** `header`, `processID`, `entityID`, `connectRequest`, `connectResponse`,
   `logonRequest`, `logonResult`, `variant`, `attribute`, `clientRequest`, `clientResponse`,
   `getAllValuesForAttributeRequest/Response` are unexported (nothing outside the package used them), and
   `logonRequest`, `variant`, `attribute`, `clientRequest`, `clientResponse`, `loginForm`, `loginFormValue`,
   `loginResultJSON`, `joinAnswer`, `clientInformation`, `realmListTicketClientInformation` and `rpcSession` implement
   `String`/`GoString`/`Format`. `rpcSession` is the container case: it keeps the logon result in an unexported
   field, where `fmt` does not call that type's methods, so the session redacts it itself. `TestSecretsAreRedacted`
   formats every type alone, behind a pointer, as an exported field, as a slice element and through an error wrapped
   with `%w`, with `%v %+v %#v %s %q %x` and `Println`, checking the raw value, its hex in both cases, its base64 and
   its decimal byte list (the form a `jsonBytes` secret would print).
4. **The hostile-challenge test names the check.** `newSRPClient` wraps one sentinel per check
   (`errChallengeIterations`, `errChallengeVersion`, `errChallengeHash`, `errChallengeInteger`, `errChallengeModulus`,
   `errChallengeGroup`, `errChallengeGenerator`, `errChallengeSalt`, `errChallengeNoPublicB`,
   `errChallengePublicB`, `errChallengePublicBLong`); `TestSRPRejectsHostileChallenges` asserts `errors.Is` against
   the expected sentinel per case, so a case cannot pass because another check fired. The login refusal is
   `errLoginRefused`, and `TestLoginWrongPassword` asserts `errors.Is(err, errLoginRefused)` instead of a substring.
5. **stop owns the deadline watcher.** `armDeadline`'s stop function closes a channel and waits for the watcher, and
   the watcher re-checks the stop channel inside the `ctx.Done()` branch, so once stop returns the goroutine cannot
   set a past deadline on the connection (the earlier version could, after the call had returned). `call` and
   `waitLogon` report a context that ended during the call as `context.Canceled` / `context.DeadlineExceeded`
   (`errors.Is`), not as the bare i/o timeout the past socket deadline produces. Pinned by
   `TestArmDeadlineWatcherStopsWithTheCall`, `TestCallCancelledContextUnblocksRead`,
   `TestCallDeadlineSurfacesAsDeadlineExceeded` and `TestCancellationLeavesTheSessionUsable`.
6. **The `bnet_test.go` test bug.** At `bnet_test.go:19` `TestLoginAgainstFakeServer` captured the log callback as
   `steps = append(steps, format)`: only the format string, never its arguments. The later "no step line leaks a
   secret" assertion therefore inspected strings like `"sub-region: %s"` and could never see a secret that was passed
   as an argument, so it proved nothing. (The same test also printed `result.JoinTicket` verbatim in its failure
   message.) The callback is now formatted with `fmt.Sprintf(format, args...)`, the test asserts that the captured
   lines carry their arguments (`sub-region: 70-1-70`), and the join ticket is parsed as JSON and reported by size
   only. Reverting the capture to the bare format string makes the test fail.
7. **An M1 with a leading zero byte is pinned.** `tools/forevervec/srp_vectors.cpp` now searches for
   `clientEvidenceM1` with `M1[0] == 0` as well; the search found it for both variants (search run: 319 attempts for
   v2 in 3.32 s, 1468 for v1 in 0.55 s). `testdata/srp-vectors.json` has 14 vectors, including
   `bnet-srp6-v2-sha256-m1-leading-zero` and `bnet-srp6-v1-sha256-m1-leading-zero`; `TestSRPVectorEdgeCases` requires
   the edge for both versions and the vector test compares M1 as a number in the server's `BN_bn2hex` form.
