# PROTOCOL-LOGIN.md verification (2026-11-18)

Checked against ~/ForeverCore source, read only. All file:line refs below are what I opened, not the document's refs.
Paths: WS = src/server/game/Server/WorldSocket.cpp, AP = src/server/game/Server/Packets/AuthenticationPackets.cpp,
CO = src/server/game/Server/Protocol/ClassicOpcodes.cpp, SRP = src/common/Cryptography/Authentication/SRP6.cpp/.h,
REST = src/server/bnetserver/REST/LoginRESTService.cpp.

## Corrections (most expensive first)

1. **4.2 item 5, compression (stream state and checksum inputs missing).** Replace the compression bullet with:
   "Encrypted S->C packets whose payload (excluding the 4-byte opcode) is larger than 0x400 bytes are sent as SMSG_COMPRESSED_PACKET (core 0x4C000E, wire 0x4E000E). Its body is: uint32 UncompressedSize (= payload + 4), uint32 UncompressedAdler = adler32 seeded 0x9827D8F1 over the 4 opcode bytes (the inner packet's translated wire opcode, LE) then the payload, uint32 CompressedAdler = adler32 seeded 0x9827D8F1 over the compressed bytes, then the compressed bytes. The compressed data is the inner wire opcode (4B LE) followed by the payload. ONE raw-deflate stream (deflateInit2 windowBits -15, memLevel 8, Z_DEFAULT_STRATEGY, level = config Compression) is created per connection and is NEVER reset: each packet is deflate(opcode, Z_NO_FLUSH) + deflate(payload, Z_SYNC_FLUSH). The client must keep one raw inflate stream (windowBits -15) for the whole connection and feed every compressed packet into it in order; inflating each packet with a fresh stream fails from the second compressed packet on. The whole SMSG_COMPRESSED_PACKET (outer opcode + body) is then GCM-encrypted like any packet." (WS:51-56, 215, 558-577, 591-619)

2. **2.3 Way 1 step 2, salt encoding.** Document says salt is `ByteArrayToHexStr(Srp->s, true)` (reversed). Code: `ByteArrayToHexStr(session->GetSessionState()->Srp->s)` with reverse = false (REST:765; Util.h:426 default false). Replace with: "`salt` = 32-byte s as uppercase hex in stored byte order (not reversed); hex-decode and use the bytes as-is in PBKDF2 (v2) or SHA256 (v1)."

3. **4.1 item 3, client opcode rule.** The `.inc` table is not only for movement/player: `TranslateClientOpcode70245` looks up `ClassicClientOpcodes.inc` FIRST for every opcode (CO:303-306), and group 0x44 has 175 entries of which 33 are not "core + 0x10000" (e.g. wire 0x440182 -> core 0x43018D). Replace the Client->Server bullet with:
   "Wire -> core (CO:251-281), for clientBuild >= 70790: (a) 70790 ClientNew list is empty: no change; (b) 70755: in group 0x44, wire 0x440071/0x440072 are unknown, wire index >= 0x73 is reduced by 2; (c) group shift (realm build >= 70291 or Classic.OpcodeGroupShift): group 0x46 -> 0x45, and in group 0x44 an index > 0x13B is reduced by 1; (d) look the result up in ClassicClientOpcodes.inc (keys are 70245 wire numbers); if absent and the group is 0x2B..0x45, group - 1. A client must invert this exactly (best: build the inverse table from the .inc), not apply +0x10000. Before CMSG_AUTH_SESSION is processed the server uses clientBuild = 0 (steps a/b skipped); after it, clientBuild = account.client_build (WS:422, 931)."

4. **4.1 item 3, server Step 1 incomplete.** Add to Step 1: "chat group 0x4A: index >= 4 gets +2; guild group 0x51: index >= 0x1C gets -1; these happen before the +1 group move" (CO:332-340). Also note SMSG_AUTH_CHALLENGE is translated with clientBuild = 0 (WS:203, 549): only steps 1-2 apply, which gives the same 0x4E0000.

5. **3.3 4f, DB statement.** Document: `LOGIN_UPD_BNET_SESSION_KEY`. Code: `LOGIN_UPD_BNET_GAME_ACCOUNT_LOGIN_INFO`, which writes `session_key_bnet = keyData` AND `client_build = <build from Param_ClientInfo>`, last_ip, locale, os, timezone_offset (RealmList.cpp:479-487; LoginDatabase.cpp:132). Replace with: "...writes keyData to account.session_key_bnet and the client's build to account.client_build (LOGIN_UPD_BNET_GAME_ACCOUNT_LOGIN_INFO); the world server later uses that client_build to pick the opcode numbering and build_info row."

6. **2.3 step 2, generator.** Document: `"2"`. Code: `g.AsHexStr()` = OpenSSL BN_bn2hex -> `"02"` (REST:750; BigNumber.cpp:210-216). Replace with `generator` ("02", hex).

## Confirmed

Section 4.2
- AUTH_CHALLENGE body: DosChallenge 32B, Challenge 32B, uint8 DosZeroBits = 1; 65 bytes, plaintext. CONFIRMED (AP:68-74; WS:278-288)
- AUTH_SESSION order: uint64 DosResponse, uint32 RegionID, uint32 BattlegroupID, uint32 RealmID, 32B LocalChallenge, 24B Digest, 1 bit UseIPv6, uint32 ticket size, ticket bytes. CONFIRMED (AP:76-93; .h:103-114). The bit takes one whole byte (MSB, so 0x80 or 0x00): the next read resets the bit position (ByteBuffer.h:175-183, 495).
- RealmID must equal config RealmID (70 via configure_server.sh:50). CONFIRMED (WS:844)
- Digest = HMAC_SHA512(key = SHA512(KeyData 64B || buildAuthKey), LocalChallenge || serverChallenge || AuthCheckSeed), first 24 bytes compared. CONFIRMED (WS:775-787)
- AuthCheckSeed, SessionKeySeed, EncryptionKeySeed byte values. CONFIRMED (WS:61-68)
- Bypass: with SkipBuildAuthKeyCheck = 1 and no auth-key row for the variant, the digest is not checked at all; the session key does not depend on it. CONFIRMED (WS:756-772). Note: a build_info row for account.client_build is still required, otherwise ERROR_BAD_VERSION (WS:739-746).
- KeyData = account.session_key_bnet, 64 bytes. CONFIRMED (WS:636,663)
- seed = HMAC_SHA512(key = SHA512(KeyData), serverChallenge || LocalChallenge || SessionKeySeed). CONFIRMED (WS:796-804)
- SessionKeyGenerator: o1 = SHA512(seed[0..31]), o2 = SHA512(seed[32..63]), o0 = SHA512(o1 || 64 zero bytes || o2); 40 bytes < 64 so sessionKey = o0[0..39]. CONFIRMED (SessionKeyGenerator.h; WS:806-807)
- encryptKey = HMAC_SHA512(key = sessionKey 40B, LocalChallenge || serverChallenge || EncryptionKeySeed)[0..31]. CONFIRMED (WS:809-816)
- ENTER_ENCRYPTED_MODE: int32 RegionGroup, 64B signature, 1 bit Enabled, FlushBits -> 0x80; signed message = HMAC_SHA512(key = encryptKey 32B, 0x01 || EnableEncryptionSeed 32B) with Ed25519ctx (ED25519_sign_ctx, context = 16B EnableEncryptionContext). CONFIRMED (AP:367-385; Ed25519.cpp:118-132)
- RegionGroup 8: CONFIRMED as configured (configure_server.sh:55); code default is 0 (WS:979).
- Delay 1500 ms on realm socket, none on the instance (continued-session) socket. CONFIRMED (WS:960-972, 1063-1066)
- ACK handler calls _authCrypt.Init(encryptKey); server encrypts every packet written after that. CONFIRMED (WS:1152-1163; WorldPacketCrypt.cpp EncryptSend)
- Cipher AES-256-GCM, 32-byte key (WorldPacketCrypt builds AES(…, 256); Init takes the span overload, asserting 32 bytes), no AAD, 12B tag in header after Size. CONFIRMED (WorldPacketCrypt.cpp:22-26; AES.cpp; WorldSocket.h:70-81)
- IV = uint64 counter LE || uint32 magic LE (0x544E4C43 C->S, 0x52565253 S->C). CONFIRMED (WorldPacketCrypt.cpp IV struct)
- Counters count every packet in that direction from connect, encrypted or not; so the first encrypted packet uses 2 if exactly AUTH_SESSION + ACK (client) and AUTH_CHALLENGE + ENTER_ENCRYPTED_MODE (server) came before. CONFIRMED (WorldPacketCrypt.cpp DecryptRecv/EncryptSend). Any extra plaintext packet shifts it.
- Ciphertext covers opcode + payload (Size bytes). CONFIRMED (WS:405, 413, 581-586)

Section 4.1
- Banners text and lengths; deflate initialised after the client banner, then AUTH_CHALLENGE. CONFIRMED (WS:85-86, 181-205)
- Header: uint32 Size (= payload + 4), 12B tag, then 4B opcode; Size < 0x10000, CMSG_HOTFIX_REQUEST up to 0x100000; tag zeros before encryption. CONFIRMED (WorldSocket.h:70-81; WS:383-406)
- Server Step 2 and Steps 3-4 (net +1 for group 0x47 index >= 0xED). CONFIRMED by composing CO:236-248, 283-301.
- Five opcodes, builds 70938 and 70888 (both >= 70790, so identical), realm build >= 70291:
  - SMSG_AUTH_CHALLENGE core 0x4C0000: step 1 group 0x4C -> 0x4D; step 2 0x4D -> 0x4E -> wire 0x4E0000. Doc 0x4E0000 CONFIRMED.
  - CMSG_AUTH_SESSION wire 0x460001 (build 0): shift 0x46 -> 0x450001; .inc 0x450001 -> 0x440001 = core. Doc CONFIRMED (.inc:657).
  - CMSG_ENTER_ENCRYPTED_MODE_ACK wire 0x460005: 70790/70755 lists touch only group 0x44; shift -> 0x450005; .inc -> 0x440005 = core. CONFIRMED (.inc:659).
  - CMSG_ENUM_CHARACTERS wire 0x440014: 70755: 0x14 < 0x71, no change; 0x14 <= 0x13B, no change; .inc 0x440014 -> 0x430014 = core. CONFIRMED (.inc:507).
  - CMSG_PLAYER_LOGIN wire 0x440016: same path -> 0x430016 = core. CONFIRMED (.inc:508).

Section 2
- Login normalised to uppercase with "@LOCAL" added (code uppercases first, same result). CONFIRMED (REST:42-55)
- srpUsername = uppercase hex SHA256(login). CONFIRMED (REST:733; Util.cpp:807-825)
- v1: N 128B, iterations 1; v2: N 256B, iterations 15000; g = 2; hash SHA-256 always. CONFIRMED (SRP.cpp:29-33; SRP.h; REST:732)
- k = SHA256(N BE padded || g BE padded) to 128/256 bytes; u = SHA256(A || B) padded the same; read BE. CONFIRMED (SRP.h)
- x v1 = SHA256(s || SHA256(user ":" PASSWORD)) read little-endian. CONFIRMED (SRP.cpp:193-199; BigNumber.h:43)
- x v2 = PBKDF2-HMAC-SHA512(user ":" password, s, 15000, 64B) read BE, minus 2^512 if top bit set, mod (N-1); password case-sensitive. CONFIRMED (SRP.cpp:206-220)
- New accounts are created as v2. CONFIRMED (BattlenetAccountMgr.cpp:48-52)
- B = (g^b + k*v) mod N; S = (A * v^u)^b mod N; M1 = H(A,B,S), M2 = H(A,M1,S) with the "broken" BE vector (bits+8)>>3 bytes. CONFIRMED (SRP.cpp:66-69, 156-186)
- Proof POST reads public_A, client_evidence_M1 as hex; M2 returned as hex. CONFIRMED (REST:357-365)

Section 3
- RPC frame: uint16 BE header length, Header protobuf, body of header.size bytes, max 0x100000; responses use service_id 0xFE. CONFIRMED (Session.cpp:74, 189-210, 366-394)
- keyData = clientSecret (32B from Param_ClientInfo info.secret) || serverSecret (32 random), JoinSecret = serverSecret, join ticket uncompressed JSON. CONFIRMED (RealmList.cpp:471-501; GameUtilitiesService.cpp:264-287, 336-356)

## Unclear
- Whether auth.build_auth_key really has no row for 70938 (DB content; no SQL in tree). Settle: `SELECT * FROM build_auth_key WHERE build=70938`. If a row exists for the client's variant, the digest IS checked even with the flag.

## Not checked
- Sections 1, 5, 6, 7, 8, 9; section 2.1 cookies, 2.2 table, Way 3, 2FA; section 3.2 service hashes/method ids, request-side service_id and method_id masking, 3.3 steps 1-4e; ByteBuffer write side of bits.
- Note: tests/headless/HeadlessClient.cpp in ForeverCore is an existing C++ headless client (sends a zero digest, line 355); worth reading as a reference.
