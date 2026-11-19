# Forever world socket, round 2 notes

Scope: from the realm join ticket (round 1, `internal/forever/bnet`) to "the server treats the player as fully in
the world", and a clean logout. No gameplay. Code: `internal/forever/wire` (byte layer), `internal/forever/worldconn`
(connection), `internal/forever/world` (session), `cmd/forever-agent` (`forever world-check`), vectors from
`tools/forevervec/world_vectors.cpp`.

Status: complete against the live server. `forever-agent forever world-check` logs in, enters the world with the
character named by the environment, stays, answers time syncs and pings, and logs out with
`SMSG_LOGOUT_COMPLETE`. Output quoted in "What works live" below (the character name, GUID and position are
redacted there; the committed fixture has its own synthetic values).

## What works live

Run on 2026-11-18 from the laptop against the audit server (the join answer named the LAN address, not 127.0.0.1, so
there is no loopback redirection to report; the realm name, addresses, character name, GUID and position are
redacted here, the character and realm names are placeholders):

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
realm join: join ticket 76 bytes, world server <server>:8085, join secret 32 bytes
login: realm: <realm> (id 70, address 0x46010046, world <server>:8085), world <server>:8085
world socket: connected to <server>:8085 (client build 70707, realm build 70707, opcode group shift 1)
world socket: hello banners exchanged (V2)
world auth challenge: 32-byte challenge read, server sent the DoS challenge (ignored: the server never checks it)
world auth session: sent (realm 70, region 70, battlegroup 1, join ticket 76 bytes, 24 zero digest bytes: Network.SkipBuildAuthKeyCheck and no build_auth_key row)
world encrypted mode: region group 8, signature 64 bytes (not verified: no trusted public key here)
world encryption: SMSG_ENTER_ENCRYPTED_MODE acknowledged, AES-256-GCM on (40-byte session key, 32-byte encryption key; lengths only)
world character list: CMSG_ENUM_CHARACTERS sent
world character list: 1 character(s), max level 1, 0 trailing bytes
  character <character>: level 1, class 1, race 1, map 0, at <position>
world login: CMSG_PLAYER_LOGIN sent for <character> (<guid>)
world login: SMSG_CONNECT_TO names <server>:8085 (serial 17)
world socket: hello banners exchanged (V2)
world instance: socket opened, continued session sent (serial 17, digest 24 bytes)
world encrypted mode: region group 8, signature 64 bytes (not verified: no trusted public key here)
world instance: encryption on
world login: SMSG_RESUME_COMMS answered with CMSG_QUEUED_MESSAGES_END (1816 ms)
world: in world on map 0 at <position> (orientation 0.00), reason 0
world: CMSG_LOADING_SCREEN_NOTIFY sent (map 0, showing false)
world: staying 20s, answering time syncs and pings
world: SMSG_MOVE_SET_ACTIVE_MOVER for <guid>, CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE sent
world: stay over (20.001s, 3 time syncs, 1 pings, 1 pongs, in world: true)
world logout: CMSG_LOGOUT_REQUEST sent
world logout: SMSG_LOGOUT_RESPONSE (result 0, instant false) and SMSG_LOGOUT_COMPLETE
server packets: 500 in 71 opcodes
  SMSG_ON_MONSTER_MOVE                                      353
  SMSG_UPDATE_OBJECT                                         44
  SMSG_SET_PROFICIENCY                                        9
  SMSG_AURA_UPDATE                                            8
  SMSG_TIME_SYNC_REQUEST                                      5
  SMSG_ACCOUNT_DATA_TIMES                                     3
  SMSG_MOVE_SET_COLLISION_HEIGHT                              3
  SMSG_AUTH_CHALLENGE                                         2
  SMSG_BATTLENET_RESPONSE                                     2
  SMSG_CANCEL_COMBAT                                          2
  SMSG_ENTER_ENCRYPTED_MODE                                   2
  SMSG_PHASE_SHIFT_CHANGE                                     2
  SMSG_RAID_DIFFICULTY_SET                                    2
  SMSG_SET_TIME_ZONE_INFORMATION                              2
  SMSG_SPELL_GO                                               2
  SMSG_TUTORIAL_FLAGS                                         2
  SMSG_UPDATE_ACTION_BUTTONS                                  2
  SMSG_WEATHER                                                2
  SMSG_ACCOUNT_HEIRLOOM_UPDATE                                1
  SMSG_ACCOUNT_ITEM_COLLECTION_DATA                           1
  SMSG_ACCOUNT_MOUNT_UPDATE                                   1
  SMSG_ACCOUNT_TOY_UPDATE                                     1
  SMSG_ACCOUNT_TRANSMOG_UPDATE                                1
  SMSG_ACCOUNT_WARBAND_SCENE_UPDATE                           1
  SMSG_ACTIVE_GLYPHS                                          1
  SMSG_ALL_ACCOUNT_CRITERIA                                   1
  SMSG_ALL_ACHIEVEMENT_DATA                                   1
  SMSG_AUCTION_FAVORITE_LIST                                  1
  SMSG_AUTH_RESPONSE                                          1
  SMSG_AVAILABLE_HOTFIXES                                     1
  SMSG_BATTLENET_NOTIFICATION                                 1
  SMSG_BATTLE_NET_CONNECTION_STATUS                           1
  SMSG_BATTLE_PAY_GET_DISTRIBUTION_LIST_RESPONSE              1
  SMSG_BATTLE_PET_JOURNAL_LOCK_ACQUIRED                       1
  SMSG_BIND_POINT_UPDATE                                      1
  SMSG_CACHE_VERSION                                          1
  SMSG_CHAT                                                   1
  SMSG_CONNECT_TO                                             1
  SMSG_CONTACT_LIST                                           1
  SMSG_ENUM_CHARACTERS_RESULT                                 1
  SMSG_FEATURE_SYSTEM_STATUS                                  1
  SMSG_FEATURE_SYSTEM_STATUS_GLUE_SCREEN                      1
  SMSG_INITIALIZE_FACTIONS                                    1
  SMSG_INITIAL_SETUP                                          1
  SMSG_INIT_WORLD_STATES                                      1
  SMSG_INSTANCE_INFO                                          1
  SMSG_LOAD_CUF_PROFILES                                      1
  SMSG_LOAD_EQUIPMENT_SET                                     1
  SMSG_LOGIN_SET_TIME_SPEED                                   1
  SMSG_LOGIN_VERIFY_WORLD                                     1
  SMSG_LOGOUT_COMPLETE                                        1
  SMSG_LOGOUT_RESPONSE                                        1
  SMSG_MIRROR_VARS                                            1
  SMSG_MOVE_ROOT                                              1
  SMSG_MOVE_SET_ACTIVE_MOVER                                  1
  SMSG_PONG                                                   1
  SMSG_POWER_UPDATE                                           1
  SMSG_RECENT_ALLY_DATA_RESPONSE                              1
  SMSG_REGIONWIDE_CHARACTER_RESTRICTIONS_DATA                 1
  SMSG_RESUME_COMMS                                           1
  SMSG_SEASON_INFO                                            1
  SMSG_SEND_KNOWN_SPELLS                                      1
  SMSG_SEND_SPELL_CHARGES                                     1
  SMSG_SEND_SPELL_HISTORY                                     1
  SMSG_SEND_UNLEARN_SPELLS                                    1
  SMSG_SETUP_CURRENCY                                         1
  SMSG_SET_DUNGEON_DIFFICULTY                                 1
  SMSG_STAND_STATE_UPDATE                                     1
  SMSG_UPDATE_TALENT_DATA                                     1
  SMSG_VIGNETTE_UPDATE                                        1
  SMSG_WORLD_SERVER_INFO                                      1
world-check: <character> in the world on map 0, 5 time syncs, 1 pings, 1 pongs, logout complete
```

The `rpc connect` line prints lengths only now (`server id label 1 digits, epoch 10 digits, server time 13 digits,
ciid 33 chars, bindless true`); the placeholders in the transcript mark the values of that run, which are not in the
notes.

The run took 42 s: about 6 s login, 20 s stay, and the ~20 s the server waits before completing a logout that is
not instant (`WorldSession.h:1097`). `account onlinelist` on the world console answers "No online players" right
after, so the logout left no session behind. The live Go test (`TestLiveWorld`, skipped without
`FOREVER_LIVE_CHARACTER` and the other `FOREVER_LIVE_*` values) passes too, including its unknown-packet count: all
71 server opcodes of that run translated by name (the packet count varies with nearby bot traffic: 500 in 71 opcodes
in this run, 402 in 72 in the earlier one, 472 in 74 in the first). The run shows the review-1 opcode fix live: the
shared Classic number 0x47017E arrives as `SMSG_POWER_UPDATE` now, where the earlier run named it the never-sent
`SMSG_HEALTH_UPDATE`.

The offline proofs, all in CI:

* `TestEnterAgainstFakeServer` runs the client against a fake server that speaks the captured/hand-computed
  protocol: banners, a digest it really verifies (it has a build key, unlike the live server), key derivation with
  its own HMAC/SHA512, the encrypted mode, compressed auth response and character list, `CMSG_CONNECT_TO`, the
  instance socket with a mandatory continued-session digest, `SMSG_RESUME_COMMS`, the login burst, time sync,
  active mover, ping/pong and logout. Its wire numbers are literals, not this package's translation.
* Vectors from the core's code (`worldconn/testdata/world-vectors.json`) pin the key derivations, both AES-GCM
  directions and a two-packet zlib stream.
* Capture-derived fixtures pin the packet layouts: `world/testdata/capture-packets.json`.

## The live server settings that decide the protocol

Read-only checks on the test server (nothing was changed):

* `auth.realmlist` id 70: address <server>, port 8085, **gamebuild 70707**, region 70, battlegroup 1, flag 0.
* `Realm.AlsoAcceptBuilds = ""` in both `bnetserver.conf:97` and `worldserver.conf:80`. `RealmAcceptsBuild`
  (`src/server/shared/Realm/RealmList.cpp:255-279`) therefore accepts **only** the realm's own build: a client that
  claims 70938 at the Battle.net logon is refused at the realm join, exactly as round 1 saw. The claim must be
  `--build 70707`. (A realm on 70707 can let a 70938 client in only if the config lists it *and* `auth.build_info`
  has that build's row; both are absent here.)
* `auth.build_info` has rows for 70707 and 70938 (and older builds). `auth.build_auth_key` has **no row for 70707
  or 70938** (its newest build is 69933). With `Network.SkipBuildAuthKeyCheck = 1`
  (`worldserver.conf:3487`) the server logs "Missing client build auth key ... digest check skipped"
  (`WorldSocket.cpp:756-772`) and never compares the 24 digest bytes. With the flag off the same missing row would
  answer `ERROR_BAD_VERSION` (`0x1C`) before any comparison.
* `Network.EnterEncryptedModeDelay = 1500` (realm socket only), `Network.EnterEncryptedModeRegionGroup = 8`,
  `Compression = 1`, `Network.AuthTimeout = 60`, `Classic.*` opcode settings at their defaults
  (`OpcodeGroupShift = -1`, `MainGroupInsertIndex = 152`, `ClientMainGroupInsertIndex = 315`, 70755/70790 lists as
  shipped).

The world server's own view of a connection's build is `account.client_build`, which `JoinRealm` sets from the
Battle.net `LogonRequest.application_version` (`RealmList.cpp:471-487`); the group shift comes from the *realm's*
build (`ClassicOpcodes.cpp` `ExtraGroupShift`). Claiming 70707 on a 70707 realm gives: group shift 1, main-group
insert at 0x98, and **no** 70755/70790 steps. The document's wire numbers are the 70938 ones; a 70707 client's
main-group numbers from 0x4700ED on are one lower (see the table below).

## Where PROTOCOL-LOGIN.md was wrong or silent (round 2)

All of these were checked in the code at commit 6517af05 and, where possible, on the live server.

1. **`CMSG_AUTH_CONTINUED_SESSION` field order (doc §6.4) is wrong.** The server reads
   `uint64 DosResponse, uint8 LocalChallenge[32], uint8 Digest[24], uint64 Key, uint32 NativeRealmAddress,
   uint32 Key3` (`AuthenticationPackets.cpp:336-344`), not DosResponse/Key/challenge/digest. Both layouts are 80
   bytes, so an 80-byte packet does not expose the difference by its length; the live server rejected our first
   attempt in principle, the capture's packet is 80 bytes, and the fake server checks the order field by field.
2. **`ContinuedSessionSeed` (doc §6.4) has the wrong value.** The code's constant is
   `56 5C 61 9C 48 3A 52 1F 61 5D 05 49 B2 9A 39 BF 4B 97 B0 1B F9 6C DE D6 80 1D AB 26 02 A9 9B 9D`
   (`WorldSocket.cpp:61-62`), not the `16 AD 0C D4 ...` bytes in the document. Our vector is computed by the core's
   `HMAC_SHA512` with this constant.
3. **`SMSG_CONNECT_TO` (doc §6.2) is not a 297-byte RSA packet in this build.** It is
   `Size<uint32>(Payload), Serial u32, Con u8, Key u64, NativeRealmAddress u32, Key3 u32`, then per address
   `Type u8, address bytes, Port u16, bleep token` (`AuthenticationPackets.cpp:282-334`, `WorldSession.cpp:912-957`).
   The live packet is 45 bytes; the capture's is 45 bytes (`01 000000 11000000 01 <key8> 46000146 00000000 01
   7f000001 951f ...`). The document's Signature/WhereBuffer fields belong to a later build.
4. **`SMSG_RESUME_COMMS` goes to the instance socket, not the realm socket** (doc checklist row 45 says Realm).
   `HandleContinuePlayerLogin` sends `ResumeComms(CONNECTION_TYPE_INSTANCE)` (`CharacterHandler.cpp:1374-1392`), which
   is the socket the login burst follows on.
5. **`SMSG_LOGOUT_RESPONSE` comes on the instance socket, `SMSG_LOGOUT_COMPLETE` on the realm socket.** The logout
   request is handled by the session (either socket), `LogoutPlayer` sends the completion and then closes the
   instance socket (`WorldSession.cpp:766,789`), and
   the opcode table pins the sockets: `SMSG_LOGOUT_RESPONSE` is `CONNECTION_TYPE_INSTANCE` and
   `SMSG_LOGOUT_COMPLETE` is `CONNECTION_TYPE_REALM` (`Opcodes.cpp:1938-1939`; the capture shows con0 for the
   completion while the response was con1). A client that watches one socket misses one of the two; ours reads both.
6. **The instance connection is required, not optional.** `HandlePlayerLoginOpcode` only sends `SMSG_CONNECT_TO`
   (`CharacterHandler.cpp:1371`); the login query and the whole burst run from
   `AddInstanceConnection → HandleContinuePlayerLogin` (`WorldSession.cpp:322-338`), i.e. only after
   `CMSG_AUTH_CONTINUED_SESSION` and the instance's encrypted-mode ack. Without it the character never enters the
   world. The document lists the second socket as one of the steps but never says the client gets nothing otherwise.
7. **`Network.SkipBuildAuthKeyCheck` skips exactly the 24-byte digest.** Key derivation, the realm id check, the
   `build_info` lookup, IP/ban/security checks, the session's `session_key_bnet` load and the encryption keys are
   all still done (`WorldSocket.cpp:739-816`). The digest itself is
   `HMAC_SHA512(SHA512(keyData || buildAuthKey), LocalChallenge || serverChallenge || AuthCheckSeed)[0..23]`
   (`WorldSocket.cpp:775-791`); this client implements it and sends it when it has a build key, and sends 24 zeros
   otherwise (what the live server gets).
8. **The continued-session digest is over the instance challenge and `LE64(Key)` first**:
   `HMAC_SHA512(sessionKey, LE64(Key) || LocalChallenge || instanceServerChallenge || ContinuedSessionSeed)[0..23]`
   (`WorldSocket.cpp:1040-1047`). It is always checked; there is no skip flag for it.
9. **The 64-byte key data is `clientSecret || joinSecret`.** `JoinRealm` stores it in `account.session_key_bnet`
   (`RealmList.cpp:471-487`); the world server hashes the same 64 bytes. The 40-byte session key it derives is then
   written back to the account (`WorldSocket.cpp:847-852`), which is what the instance looks up.
10. **The real client's character list is regionwide.** `HandleCharEnum` moves every character into
    `RegionwideCharacters`, sets `Realmless = true` and clears `Characters` (`CharacterHandler.cpp:494-538`). The
    document's §5.2 describes `Characters` as the main array; the login guid comes from the regionwide entry. Our
    parser reads both arrays and reports the regionwide one.
11. **The character list is not the only data of `SMSG_ENUM_CHARACTERS_RESULT`.** `ClassDisableMask` is present
    (optional bit set) with `ClassPreventingCharCreation`, and 33 race unlocks, no conditional appearances, no race
    limits and no warband groups follow the character entry on the live server. Parsing stops at the wrong byte
    position if any of those is skipped (the capture-derived fixture parses to the last byte, `0 trailing bytes`).
12. **The document's `SMSG_ENTER_ENCRYPTED_MODE` order is right but its delay is per socket.** The realm socket
    waits `Network.EnterEncryptedModeDelay` (1500 ms live) before it is sent; the instance path sends it immediately
    after the continued-session digest (`WorldSocket.cpp:1049-1063`). A client with a short auth timeout should allow
    for the realm delay.
13. **The document does not state that `SMSG_COMPRESSED_PACKET` is how the *first* encrypted packet often arrives.**
    Live, `SMSG_AUTH_RESPONSE` (1179 bytes in the capture, more on the live server) and the character list are
    compressed, so the decompressor has to work before anything else can be read (`WorldSocket.cpp:573-616`).
14. **The document's CMSG_PLAYER_LOGIN far clip is the client's video setting, not a protocol constant.** The two
    logins in the captures sent 777.0 and 2439.5. The server reads the float (`CharacterPackets.cpp:742-747`) and
    does not use it; this client sends 777.0.
15. **`SMSG_TUTORIAL_FLAGS` is sent twice in a session** (glue and world login) and `SMSG_ACCOUNT_DATA_TIMES` three
    times live; a client must not treat repeats as errors.
16. **The loading-screen notification goes on the realm socket.** The capture sends
    `CMSG_LOADING_SCREEN_NOTIFY(map 0, showing=false)` on connection 0 after `SMSG_LOGIN_VERIFY_WORLD` (the instance
    socket carries the burst); `HandleLoadScreenOpcode` runs for the session regardless of the socket
    (`CharacterHandler.cpp:1412-1437`).
17. **The repack capture logs `CMSG_QUEUED_MESSAGES_END` as `CMSG_ENABLE_NAGLE`.** The recorder's build
    (repack 0.1.37.4) lacked the `0x45000A → 0x44000A` table entry that the pinned core has (the `.inc` comment says
    the live 70938 client sends `0x46000A` after every `SMSG_RESUME_COMMS` with a 4-byte rising client time). The
    fixture is the same 4 bytes; our client sends the correct wire number and the live login burst follows it.

## Answers to round 1's open questions

1. **World build and auth key.** The realm's build is 70707 (`auth.realmlist.gamebuild`); the claim must match it
   because `Realm.AlsoAcceptBuilds` is empty. `Network.SkipBuildAuthKeyCheck = 1` and no `auth.build_auth_key` row
   exists for 70707/70938, so the digest is skipped. A `build_info` row is still required
   (`WorldSocket.cpp:739-746`) and exists for both builds.
2. **Opcode numbering for 70707.** Realm build 70707 ≥ 70291 → group shift 1, main-group insert at 0x98; client
   build 70707 < 70755 → no 70755/70790 steps. The document's 70938 numbers differ for main-group indices from
   0x4700ED on (one higher). The live run named all 472 received packets with this table, no unknown wire number.
3. **`DosResponse`.** Never read (`WorldSocket.cpp:686-1064`); the DoS challenge and zero bits are ignored. This
   client sends zero and never solves the challenge, like the core's own headless client (`HeadlessClient.cpp`, auth
   payload starts with `uint64(0)`).
4. **World session key.** Derived and pinned by core-produced vectors: `sessionKey` = 40 bytes of
   `SHA512(SHA512(seed[0:32]) || 0^64 || SHA512(seed[32:64]))`, where `seed = HMAC_SHA512(SHA512(keyData),
   serverChallenge || localChallenge || SessionKeySeed)`; `encryptKey` = first 32 bytes of
   `HMAC_SHA512(sessionKey, localChallenge || serverChallenge || EncryptionKeySeed)`. The instance derives its own
   `encryptKey` from its own challenge with the same 40-byte session key.
5. **`SMSG_ENTER_ENCRYPTED_MODE` signature.** It cannot be verified by this client. The signed input is
   `HMAC_SHA512(encryptKey, 0x01 || EnableEncryptionSeed)` (64 bytes, vector-pinned); the Ed25519 signature uses
   `EnableEncryptionContext` (`AuthenticationPackets.cpp:367-388`). The trusted public key is selected by
   `RegionGroup` (8) from the *client launcher's* key store, which is not in the server tree, the captures or this
   repository. The server's private key is in the core, but deriving the public key from it would only show the
   packet came from this source tree, not that a real client would trust it. What *is* authenticated is everything
   after the ack: AES-256-GCM with a key derived from the join secret and both challenges, so a wrong key or a
   tampered byte fails the tag at the first packet, and this client fails closed.
6. **`Command_*` attribute values.** Round 1 material, not used here.
7. **`ciid` echo.** Not used by the world socket.

## Sized strings and vector appends in the character-list parser (review 3)

Every `SizedString`/`SizedCString` and every vector the parser walks, checked against the writer in
`src/server/game/Server/Packets/CharacterPackets.cpp:217-417` and `PacketOperators.h:322-400`:

| Parser | Server writer | State |
|---|---|---|
| name and surname, `character.go:245-256` | `SizedString::BitsSize<6>` + `Data`, `CharacterPackets.cpp:274-283` | 6-bit length, raw bytes, no NUL: correct |
| warband group name, `character.go:186-191` | `SizedString::BitsSize<9>` + `Data`, `CharacterPackets.cpp:407-410` | length, no NUL, flush handled: correct |
| customizations, `character.go:244` | `Size<uint32>` + 8 bytes per `ChrCustomizationChoice`, `CharacterPackets.cpp:266-268`, `:32-38` | counted skip: correct |
| PersonalTabard, `character.go:231` | five `int32`, `CharacterPackets.cpp:56-65` | 20-byte skip: correct |
| VisualItems, `character.go:226` | 19 fixed entries of 23 bytes, `CharacterPackets.cpp:208-218` | 437-byte skip: correct |
| MailSenderTypes, `character.go:281` | `append(types.data(), size)` = `count * sizeof(uint32)`, `CharacterPackets.cpp:301-302`, `ByteBuffer.h:601-605` | **was `Skip(typeCount)`; now `Skip(4 * typeCount)`** |
| MailSenders, `character.go:284-307` | `SizedCString::BitsSize<6>` + `Data`, `CharacterPackets.cpp:304-311`, `PacketOperators.h:322-400` | **was `ReadCString` per sender; now the six-bit prefix is length+1: `n == 1` is empty (no data), `n > 1` is `n-1` bytes and a checked NUL, `n == 0` rejected** |
| RaceUnlock / ClassUnlock, `character.go:144-158` | `Size<uint32>`, per class 3 bits then `FlushBits`, per race 7 bits then `FlushBits`, `CharacterPackets.cpp:338-378` | flush widths match: correct |
| WarbandGroups and Members, `character.go:160-194` | `Size<uint32>` + `Size<uint32>`, `CharacterPackets.cpp:384-411` | counts and per-member fields match: correct |
| ConnectTo bleep token | `SizedString<5>`, `SizedCString<24>`, `SizedString<6>`, `AuthenticationPackets.cpp:270-279` | not parsed: the parser stops after the first address's port, and the token is the packet's tail |

The bnet RPC strings are protobuf fields, not `ByteBuffer` sized strings, so the two mistakes cannot occur there.
The fixtures for the fixed reader are hand-built from the writer above (`TestReadRestrictionsAndMailsAgainstTheServerWriter`:
two types and no senders, two senders with one empty, both; each ends in a sentinel), and
`TestReadRestrictionsAndMailsRejectsMalformedSenders` covers the zero prefix and a missing NUL.

## Bounds against a flooding server (review 3)

The handshake keeps at most 64 packet names, and each handshake loop fails after 64 ignored packets instead of
reading until its deadline: a server that never sends `SMSG_ENTER_ENCRYPTED_MODE` or `SMSG_AUTH_RESPONSE` ends the
run (`TestHandshakeNamesAreBounded`, `TestHandshakeLoopsCapIgnoredFrames`). The packet count keeps at most 64
distinct unknown wire opcodes and collapses the rest into one "other unknown wire opcodes" row
(`TestUnknownReportKeysAreBounded`).

## How the vectors were made

`internal/forever/worldconn/testdata/world-vectors.json` comes from
`tools/forevervec/world_vectors.cpp`, built by hand (not by CI; the `g++` line is in the file header) against
ForeverCore at 6517af05. It links `AES.cpp` and `WorldPacketCrypt.cpp` and stubs only the core's assertion helpers.
Fixed inputs: `keyData` = bytes 0x00..0x3F (client secret then join secret), realm challenge 0x40..0x5F, local
challenge 0x60..0x7F, instance challenge 0x80..0x9F, a synthetic 16-byte build auth key 0xA0..0xAF, continued key
`0x1122334455667788`.

* `sessionKey`, `encryptKey`: the core's `SHA512`, `HMAC_SHA512` and `SessionKeyGenerator<SHA512>`, called exactly
  as `WorldSocket::HandleAuthSessionCallback` calls them.
* `authDigest`: the core's formula with the synthetic build key.
* `continuedDigest`: the core's formula with `LE64(continuedKey)` and the instance challenge.
* `enableEncryptionInput`: `HMAC_SHA512(encryptKey, 0x01 || EnableEncryptionSeed)`, the bytes the server signs.
* `gcm[]`: eight frames, counters 0..3 in both directions, plaintext = inner wire opcode `0x470001` + 20 bytes.
  Server→client frames are produced by `WorldPacketCrypt::EncryptSend` (the real send path); client→server frames by
  `Trinity::Crypto::AES` with the `"CLNT"` IV, and each one is then fed back through
  `WorldPacketCrypt::DecryptRecv` (and the server ones through an AES decrypting context) — the program exits
  non-zero unless the other side's function accepts it and returns the same plaintext.
* `compression[]`: two packets (1204 and 604 bytes) in **one** `deflateInit2(1, Z_DEFLATED, -15, 8,
  Z_DEFAULT_STRATEGY)` stream with `Z_SYNC_FLUSH` after each, opcode written first, and both adler32 values with
  seed `0x9827D8F1`, exactly like `WorldSocket::CompressPacket`.
* `firstEncryptedFrames`: the counter question. A `WorldPacketCrypt` that is not initialized still advances its
  counter on `EncryptSend`/`DecryptRecv` (`WorldPacketCrypt.cpp:69-103`), and the server installs the key only after
  it sent `SMSG_AUTH_CHALLENGE` and `SMSG_ENTER_ENCRYPTED_MODE` (`WorldSocket.cpp:1156`). So the program pushes two
  plain frames through the uninitialized crypt, calls `Init(encryptKey)`, and encrypts the next frame: that frame
  is at counter 2 for both directions (`server` through `EncryptSend`, `client` through the core's AES with the
  `"CLNT"` IV; the program then has `WorldPacketCrypt::DecryptRecv` accept the client frame back). Before this
  vector the counter was only checked by the fake server, which was written from the same reading.

The Go tests compare every value; the client→server GCM frames must match byte for byte, the server→client ones must
decrypt to the plaintext, and the two compressed bodies must inflate through one `wire.Inflater` (a fresh inflater
is required to fail on the second body, proving the stream is continuous). The seeded adler also has four
zlib-produced literals in `wire/inflate_test.go` (Python's `zlib.adler32(data, 0x9827D8F1)`).

## How the fixtures were made

`internal/forever/world/testdata/capture-packets.json` holds **payloads only** (the 4-byte opcode stripped) from the
server's own packet log of the private capture (PKT 3.1, repack 0.1.37.4, 2026-11-17). Opcodes are the core's
numbers, i.e. before the wire translation. Every capture-derived stable identifier and context value was rewritten
with a synthetic one of the same byte length and the same bit layout (so the parsers still see the real structure):

* the join ticket's `gameAccount` value became `000#0` (4 bytes, the placeholder form);
* the character name and surname (6+4 bytes, bit-packed) became `XXXXXX`/`XXXX`;
* the realm name pair in `SMSG_AUTH_RESPONSE` (two identical names) became fourteen `X`s;
* the character GUID, wherever it appears (bit-packed in `SMSG_ENUM_CHARACTERS_RESULT`, `CMSG_PLAYER_LOGIN` and
  `SMSG_MOVE_SET_ACTIVE_MOVER`): mask `0xE003` and five bytes `11 22 33 44 0A`, i.e. Low `0x2211`, High
  `0x0A44330000000000` (still a player GUID: high type 2, five non-zero bytes);
* the create and last-active times (`SMSG_ENUM_CHARACTERS_RESULT`) became 1700000000 (`0x6553F100`);
* the position (XYZ in `SMSG_ENUM_CHARACTERS_RESULT`, XYZO in `SMSG_LOGIN_VERIFY_WORLD`) became
  1.25, 2.5, 3.75, 0;
* the `SMSG_CONNECT_TO` `Key` became `0x1122334455667788`.

The connect-to address is 127.0.0.1:8085, which is not private. No `.pkt` file is committed and no account name,
password or LAN address is in the fixture. `TestFixturesCarryOnlySyntheticGUIDs` walks every packed GUID the parser
decodes — the character GUID it keeps and the guild GUID and warband-member GUIDs it drops — through the reader's
audit hook, and fails unless each is the synthetic one or empty, so a re-captured fixture cannot reintroduce a real
identifier silently. `TestDroppedGUIDsAreAudited` proves the audit sees the dropped slots: a non-synthetic guild GUID
and a non-synthetic warband-member GUID each fail it.

The fixtures pin: `SMSG_AUTH_CHALLENGE` (65), `CMSG_AUTH_SESSION` (159, including the flushed UseIPv6 bit byte, the
ticket length and the ticket), `SMSG_AUTH_RESPONSE` (1179), `SMSG_ENUM_CHARACTERS_RESULT` (2338, parsed to the last
byte), `SMSG_CONNECT_TO` (45), `SMSG_LOGIN_VERIFY_WORLD` (24, XYZO), `SMSG_TIME_SYNC_REQUEST`, `SMSG_MOVE_SET_ACTIVE_MOVER`,
`SMSG_PONG`, `SMSG_LOGOUT_RESPONSE`, `SMSG_LOGOUT_COMPLETE`, and the client's `CMSG_PLAYER_LOGIN`,
`CMSG_LOADING_SCREEN_NOTIFY`, `CMSG_TIME_SYNC_RESPONSE`, `CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE`,
`CMSG_QUEUED_MESSAGES_END`, `CMSG_LOGOUT_REQUEST`.

## Opcode table (hand-computed, both directions)

`TranslateServerOpcode` for realm build 70707 (shift 1): first the explicit overrides
(`ClassicOpcodes.cpp:55-67`), then `70245 = (group+1)<<16 | index` with −1 for group 0x45 index 0x17E..0x1C0, +2 for
chat 0x4A index ≥ 4 and −1 for guild 0x51 index ≥ 0x1C; then `index ≥ 0x98` in 0x46 gets +1 and every group
0x46..0x6B gets +1; for a 70755+ client, numbers ≥ 0x470125 get +1, and for 70790 the 0x470125 insert is removed
and 0x4700ED added (net +1 from 0x4700ED on).

| core | name | 70707 wire | 70938 wire | computation |
|---|---|---|---|---|
| 0x4C0000 | SMSG_AUTH_CHALLENGE | 0x4E0000 | 0x4E0000 | 0x4C→0x4D, group+1 |
| 0x4C0004 | SMSG_ENTER_ENCRYPTED_MODE | 0x4E0004 | 0x4E0004 | same |
| 0x4C0006 | SMSG_RESUME_COMMS | 0x4E0006 | 0x4E0006 | same |
| 0x4C0008 | SMSG_CONNECT_TO | 0x4E0008 | 0x4E0008 | same |
| 0x4C0009 | SMSG_PONG | 0x4E0009 | 0x4E0009 | same |
| 0x4C000E | SMSG_COMPRESSED_PACKET | 0x4E000E | 0x4E000E | same |
| 0x450001 | SMSG_AUTH_RESPONSE | 0x470001 | 0x470001 | 0x46, index 1 < 0x98, group+1 |
| 0x450018 | SMSG_ENUM_CHARACTERS_RESULT | 0x470018 | 0x470018 | same |
| 0x45002F | SMSG_LOGIN_VERIFY_WORLD | 0x47002F | 0x47002F | same |
| 0x450064 | SMSG_FEATURE_SYSTEM_STATUS_GLUE_SCREEN | 0x470064 | 0x470064 | same |
| 0x4501B6 | SMSG_ACCOUNT_DATA_TIMES | 0x4701B6 | 0x4701B7 | override 0x4601B5, +1 index, group+1; 70938 adds the 70755 insert and nets +1 more |
| 0x450123 | SMSG_SET_TIME_ZONE_INFORMATION | 0x470124 | 0x470125 | 0x460123, +1, group+1; 70938 +1 from 0x4700ED |
| 0x45012F | SMSG_LOGOUT_RESPONSE | 0x470130 | 0x470131 | same rule as SET_TIME_ZONE |
| 0x450130 | SMSG_LOGOUT_COMPLETE | 0x470131 | 0x470132 | same |
| 0x450268 | SMSG_TUTORIAL_FLAGS | 0x470269 | 0x47026A | 0x460268, +1, group+1; 70938 net +1 |
| 0x4502B1 | SMSG_BATTLE_NET_CONNECTION_STATUS | 0x4702B2 | 0x4702B3 | same |
| 0x490001 | SMSG_AVAILABLE_HOTFIXES | 0x4B0001 | 0x4B0001 | 0x4A, group+1 |
| 0x49000E | SMSG_CACHE_VERSION | 0x4B000E | 0x4B000E | same |
| 0x5E0000 | SMSG_TIME_SYNC_REQUEST | 0x600000 | 0x600000 | 0x5F, group+1 |
| 0x5E0003 | SMSG_MOVE_SET_ACTIVE_MOVER | 0x600003 | 0x600003 | same |
| 0x450362 | SMSG_RECENT_ALLY_DATA_RESPONSE | 0x470363 | 0x470364 | 0x460362, +1, group+1; 70938 net +1 |
| 0x45021E | SMSG_BATTLE_PAY_GET_DISTRIBUTION_LIST_RESPONSE | 0x47021F | 0x470220 | same |
| 0x450360 | SMSG_ACCOUNT_ITEM_COLLECTION_DATA | 0x470361 | 0x470362 | same |
| 0x45036F | SMSG_MIRROR_VARS | 0x470372 | 0x470373 | override 0x460371, +1, group+1; 70938 net +1 |

`TranslateClientOpcode` for a 70707 claim: explicit `.inc` entry first, else `classic = core` with group+1 for
groups 0x2B..0x44, then group shift (classic group 0x45 → 0x46) and the main-group insert (+1 index above 0x13B).
For a 70938 claim, group-0x44 messages at/after 0x440071 get +2.

| core | name | 70707 wire | 70938 wire | computation |
|---|---|---|---|---|
| 0x440001 | CMSG_AUTH_SESSION | 0x460001 | 0x460001 | .inc 0x450001, shift |
| 0x440003 | CMSG_AUTH_CONTINUED_SESSION | 0x460003 | 0x460003 | .inc 0x450003, shift |
| 0x440005 | CMSG_ENTER_ENCRYPTED_MODE_ACK | 0x460005 | 0x460005 | .inc 0x450005, shift |
| 0x440006 | CMSG_PING | 0x460006 | 0x460006 | .inc 0x450006, shift |
| 0x440007 | CMSG_LOG_DISCONNECT | 0x460007 | 0x460007 | .inc 0x450007, shift |
| 0x44000A | CMSG_QUEUED_MESSAGES_END | 0x46000A | 0x46000A | .inc 0x45000A, shift |
| 0x430014 | CMSG_ENUM_CHARACTERS | 0x440014 | 0x440014 | .inc 0x440014, index < 0x13B |
| 0x430016 | CMSG_PLAYER_LOGIN | 0x440016 | 0x440016 | .inc 0x440016 |
| 0x430024 | CMSG_LOADING_SCREEN_NOTIFY | 0x440024 | 0x440024 | arithmetic 0x440024 |
| 0x4300AB | CMSG_KEEP_ALIVE | 0x4400AB | 0x4400AD | arithmetic; 70938 +2 |
| 0x430000 | CMSG_CONNECT_TO_FAILED | 0x440000 | 0x440000 | arithmetic |
| 0x410064 | CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE | 0x420064 | 0x420064 | .inc 0x420064 |
| 0x41005C | CMSG_TIME_SYNC_RESPONSE | 0x42005C | 0x42005C | .inc 0x42005C |
| 0x3E0072 | CMSG_LOGOUT_REQUEST | 0x3F0074 | 0x3F0074 | .inc 0x3F0074 (not the arithmetic 0x3F0073) |
| 0x430070 | CMSG_CREATE_CHARACTER | 0x440070 | 0x440070 | arithmetic; below the inserts |
| 0x430071 | CMSG_CHECK_CHARACTER_NAME_AVAILABILITY | 0x440071 | 0x440073 | arithmetic; 70938 +2 |
| 0x43000A | CMSG_START_WAR_GAME | 0x44000A | 0x44000A | .inc 0x44000A, below 0x71 |
| 0x4300EA | CMSG_BATTLE_PAY_GET_PURCHASE_LIST | 0x4400EA | 0x4400EC | .inc 0x4400EA; 70938 +2 |
| 0x43010F | CMSG_GET_UNDELETE_CHARACTER_COOLDOWN_STATUS | 0x44010F | 0x440111 | .inc; 70938 +2 |
| 0x430134 | CMSG_CAN_REDEEM_TOKEN_FOR_BALANCE | 0x440133 | 0x440135 | .inc 0x440133; 70938 +2 |
| 0x430176 | CMSG_SOCIAL_CONTRACT_REQUEST | 0x440177 | 0x440179 | arithmetic, +1 main insert; 70938 +2 |

The Go tests assert these literals in both directions (`TestServerOpcodeTranslation`, `TestClientOpcodeTranslation`)
and the live run exercised 74 server opcodes at build 70707 without a single unknown wire number.

## What the server sends unasked after the auth (realm socket, live)

`SMSG_AUTH_RESPONSE` (compressed on the wire), then `SMSG_SET_TIME_ZONE_INFORMATION`,
`SMSG_FEATURE_SYSTEM_STATUS_GLUE_SCREEN`, `SMSG_MIRROR_VARS` (a 1024-byte payload in the capture),
`SMSG_CACHE_VERSION`, `SMSG_AVAILABLE_HOTFIXES` (large enough to be compressed), `SMSG_ACCOUNT_DATA_TIMES`,
`SMSG_TUTORIAL_FLAGS`, `SMSG_BATTLE_NET_CONNECTION_STATUS` and two `SMSG_BATTLENET_RESPONSE`. The client's
`CMSG_ENUM_CHARACTERS` gets `SMSG_ENUM_CHARACTERS_RESULT` (compressed), and with it (unasked)
`SMSG_BATTLE_PAY_GET_DISTRIBUTION_LIST_RESPONSE`, `SMSG_RECENT_ALLY_DATA_RESPONSE`,
`SMSG_ACCOUNT_ITEM_COLLECTION_DATA` and `SMSG_REGIONWIDE_CHARACTER_RESTRICTIONS_DATA`.

## What the server sends unasked after the login (instance socket)

After the ack: `SMSG_RESUME_COMMS`, then `SMSG_LOGIN_VERIFY_WORLD`, `SMSG_TIME_SYNC_REQUEST`,
`SMSG_BIND_POINT_UPDATE`, talent/spell/action-button/faction/currency packets, `SMSG_LOGIN_SET_TIME_SPEED`,
`SMSG_WORLD_SERVER_INFO`, account mount/toy/heirloom/transmog/warband updates, `SMSG_INITIAL_SETUP`,
`SMSG_MOVE_SET_ACTIVE_MOVER`, `SMSG_UPDATE_OBJECT` with `ThisIsYou`, then the visibility burst (the live 15-second
run above received 402 packets in 72 opcodes: 261 `SMSG_ON_MONSTER_MOVE`, 39 `SMSG_UPDATE_OBJECT`, 9
`SMSG_SET_PROFICIENCY`, 8 `SMSG_AURA_UPDATE`, and so on). `SMSG_TIME_SYNC_REQUEST` came 4 times; the server keeps sending them every
5/5/10 s (`WorldSession.cpp:1901-1938`).

## Review round 1 fixes

1. **Inflate hang (HIGH).** `wire/inflate.go` no longer uses a channel. The deflate reader pulls from a
   `chunkSource` whose `cur` is set on the reading goroutine before `io.ReadFull`, and `Read`/`ReadByte` return
   `io.ErrUnexpectedEOF` when it is used up; the source implements `flate.Reader`, so there is no `bufio` read-ahead
   either. A packet that cannot produce exactly its declared size is an error and poisons the inflater (the stream
   position is unknown, so later packets are refused instead of read from the wrong place). The bytes the decoder
   left in the previous chunk (the tail of a `Z_SYNC_FLUSH` marker) are prepended to the next, so a valid stream
   still decodes packet after packet. `TestInflateTruncatedChunkFailsWithinASecond` is the regression: a truncated
   stream with a valid compressed adler returns an error in well under a second. `TestInflateFailureDoesNotShareState`
   shows a fresh inflater still decodes the same two packets and the poisoned one refuses the next.
2. **Panic on shutdown (HIGH).** With no channel there is nothing to close under a sender; `Inflater.Close` is
   idempotent (`sync.Once`), as are `conn.close` (one `net.Conn.Close`, one `Inflater.Close`), `Realm.Close` and
   `Instance.Close`. `session.shutdown` cancels both pumps, closes both sockets (instance then realm, whatever the
   error path), and waits for both readers before `Enter` returns, bounded by the run's deadline (2 s grace once
   that has passed). `TestConnCloseIsIdempotentAndUnblocksARead`, `TestCloseBothSocketsInEitherOrder`,
   `TestInflateCloseDuringDecompress` (all `-race`) and `TestShutdownWaitsForThePumps` pin it.
3. **Repository hygiene.** The notes no longer print an account; the fixture's identifiers are synthetic (see "How
   the fixtures were made"); `world/live_test.go` has no default character (it skips without
   `FOREVER_LIVE_CHARACTER`); `worldconn/testdata` held only synthetic vectors already.
4. **Instance redirect (MED).** `Realm.DialInstance` compares the address `SMSG_CONNECT_TO` names with the realm
   socket's `RemoteAddr` (both are IPs by then; the port may differ) and refuses a different host with an error that
   names both addresses. `--allow-instance-redirect` / `Options.AllowInstanceRedirect` / `Config.AllowInstanceRedirect`
   permit it for clustered servers. Tests: `TestDialInstanceRefusesAHostOtherThanTheRealm`,
   `TestDialInstanceAllowsARedirectOnlyWithTheOption`.
5. **Deadlines (LOW).** There is no connection-wide deadline. `armRead`/`armWrite` arm the socket for one operation
   from the operation's context (or `defaultIOTimeout` = 60 s when the context has none) and clear it afterwards;
   `DialInstance` takes its deadline from its context. A watch loop can run for hours as long as it passes a long
   context. `TestDeadlinesArePerOperation` shows a timed-out read followed by a successful one.
6. **Reverse opcode table (LOW).** `TranslateServerWire` now indexes by (client-build class, realm build shift), so
   a realm below 70291 resolves the unshifted numbers, and when two core opcodes share a Classic number it prefers
   the one the server can send (`blockedServerCores`, from `Opcodes.h` / `BlockedServerOpcodes`). Cases:
   classic 0x47017E is `SMSG_POWER_UPDATE` (0x45017E), not the never-sent `SMSG_HEALTH_UPDATE`; 0x53001B is
   `SMSG_GUILD_ITEM_LOOTED_NOTIFY` (0x51001C), not `SMSG_GUILD_CHALLENGE_COMPLETED` (0x51001B). At build 70938 the
   power pair moves to 0x47017F because the 70755/70790 inserts apply after the shift.
7. **Account scrubbing (LOW).** `restClient.scrubServerText` sanitises, removes the account case-insensitively in all
   its forms (as given, the part before `@`, and the `#N` discriminator), and only then bounds the text to 120 bytes;
   the request path in every REST error goes through it, and `serverTextError` carries it for transport errors,
   which quote the URL.
8. **Redacting formats (LOW).** `worldconn.Config`, `worldconn.Realm`, `worldconn.Instance` and `world.Options`
   print lengths only under every verb, nested included (`redact_test.go` in both packages).
9. **Frame cap (LOW).** `sendFrame` refuses `Size >= 0x10000` (`wire.MaxClientFrame`, `WorldSocket.h:75`) and says so.
10. **Failed run leaves the character online (LOW).** After `CMSG_PLAYER_LOGIN`, a failing `Enter` sends
    `CMSG_LOGOUT_REQUEST` and `CMSG_LOG_DISCONNECT` on the instance socket (best effort, 2 s) before closing.
    `TestEnterSendsTheAbortLogoutWhenItFailsInWorld` refuses the character after the active-mover exchange and
    checks the two wires arrive. The `BuildLogoutRequest` comment now states what the server's reader says (the
    Classic client sends an empty payload; the capture's one zero byte is accepted).

## Open questions for the gameplay round

1. **Movement on the instance socket.** The capture has `CMSG_MOVE_HEARTBEAT` payloads (78 bytes) with the Classic
   movement-info layout; the world socket's time-sync answers already give the server a clock delta, but the full
   movement flags/fields must be derived from those payloads before any movement is sent.
2. **Which socket gameplay uses.** This core processes packets from both sockets, but the real client sends
   gameplay on the instance socket (capture connection 1). The next round should keep that split.
3. **Object update parsing.** `SMSG_UPDATE_OBJECT` for Classic 1.60 has its own block/update-field layouts; the
   1.12 parsers in `internal/game` cannot be reused. The live burst is the corpus.
4. **Reconnection and errors.** After an inflate or GCM failure the connection is unusable; a bot needs a reconnect
   policy (and `Network.AuthTimeout = 60` bounds the login). Not addressed here.
5. **Channel joins and chat.** The live run received two `SMSG_CHAT` and one `SMSG_BATTLENET_NOTIFICATION`; what
   the client must answer for channels (and whether the Discord bridge expects anything) is open.
6. **Ping pacing.** The server kicks after `MaxOverspeedPings` pings closer than 27 s; this client pings every 30 s
   and sends the first one immediately (allowed: the first ping has no previous one). A gameplay loop must keep
   that rule and also answer `SMSG_TIME_SYNC_REQUEST`.
7. **Logout timing.** A non-resting character takes the server's 20 s before `SMSG_LOGOUT_COMPLETE`
   (`WorldSession.h:1097`); commands that must leave quickly need `CMSG_LOGOUT_CANCEL`/instant logout handling.
8. **`SMSG_COMPRESSED_PACKET` after a bad adler.** The inflate stream is per connection and never reset; one corrupt
   packet poisons it. The client should close and reconnect rather than try to resynchronise.
