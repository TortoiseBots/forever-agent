# ForeverCore Login & World-Entry Protocol

ForeverCore is a TrinityCore-based server for the WoW Forever client (`doc/ForeverSetup.md:3-5`, `contrib/linux/configure_server.sh:34-61`). It targets client build `70938` (`contrib/linux/configure_server.sh:34,61`, `src/server/shared/Realm/ClientBuildInfo.cpp:36-47`). Client login spans two binaries and three TCP listeners: `bnetserver` (REST HTTP/S and Battle.net RPC) and `worldserver` (Realm and Instance world sockets) (`src/server/bnetserver/Main.cpp:218-253`, `src/server/worldserver/Main.cpp:401-419`).

**Verification status (2026-11-18, round 1).** Sections 1-3 were checked line by line against the server code at commit `6517af05`; the corrections of `PROTOCOL-LOGIN-VERIFY.md` are applied in place and the remaining statements of those sections were re-read in the code. Sections 4.1-4.2 were confirmed by the earlier verification except where corrected below. **Sections 5-9 are not verified against the code** (they come from the first write-up) and are marked as such; treat them as leads, not facts.

---

## 1. Ports and Endpoints

|Listener|Binary|Port|`BindIP`|Config Key|TLS / Encryption|Source|
|---|---|---|---|---|---|---|
|Login REST|`bnetserver`|`8081`|`"0.0.0.0"`|`LoginREST.Port`|HTTPS unless cert CN is `"*.*"`|`src/server/bnetserver/Main.cpp:218-233`|
|BNet RPC|`bnetserver`|`1119`|`"0.0.0.0"`|`BattlenetPort`|Always TLS (`SslContext`)|`src/server/bnetserver/Main.cpp:235-253`|
|World (Realm)|`worldserver`|`8085`|`"0.0.0.0"`|`WorldServerPort`|Raw TCP, then `AES-256-GCM`|`src/server/worldserver/Main.cpp:401-419`|
|World (Instance)|`worldserver`|`8085`|`"0.0.0.0"`|`WorldServerPort`|Raw TCP, then `AES-256-GCM`|`src/server/game/Server/WorldSession.cpp:924-936`|

1. **TLS certificates (`bnetserver`)**: `Battlenet::SslContext::Initialize()` loads `CertificatesFile` (default `"./bnetserver.cert.pem"`) and `PrivateKeyFile` (default `"./bnetserver.key.pem"`) with `PrivateKeyPassword` (default `""`) (`src/server/bnetserver/Server/SslContext.cpp:71-85,155-163`).
2. **REST plain-HTTP fallback**: `SslContext::UsesDevWildcardCertificate()` checks the certificate subject CommonName (`NID_commonName`) (`src/server/bnetserver/Server/SslContext.cpp:118-146`). When CommonName is `"*.*"`, `LoginHttpSession::Start()` disables TLS (`SetSSLHandshake(false)`) and serves plain HTTP (`src/server/bnetserver/REST/LoginHttpSession.cpp:116-124`). Otherwise it runs an async TLS server handshake (`src/server/bnetserver/REST/LoginHttpSession.cpp:126-139`).
3. **Advertised REST hostnames**: `LoginRESTService::Start()` resolves `LoginREST.ExternalAddress` (default `"127.0.0.1"`, set to `"trinity.actual.battle.net"` in `doc/ForeverSetup.md:42`) and `LoginREST.LocalAddress` (default `"127.0.0.1"`) (`src/server/bnetserver/REST/LoginRESTService.cpp:68-91`).
4. **BNet RPC TLS**: `Battlenet::Session` always performs a TLS server handshake (`async_handshake`) on port `1119` (`src/server/bnetserver/Server/Session.h:147`, `src/server/bnetserver/Server/Session.cpp:87-90,145-159`). Client-side certificate pinning is `NOT IN THIS TREE` (`doc/ForeverSetup.md:42` notes that `"trinity.actual.battle.net"` must match the certificate hostname).
5. *Not verified against the code in round 1 (these are configuration values from `configure_server.sh`/`doc/ForeverSetup.md`).* **Required ForeverCore configuration**:
   - `bnetserver.conf`: `Realm.CfgContentSetID = 137` (`Normal` season) and `Realm.SuperDistrictID = 2` (`Normal`) (`doc/ForeverSetup.md:43-44`, `contrib/linux/configure_server.sh:54-56`).
   - `worldserver.conf`: `RealmID = 70`, `Expansion = 0`, `Network.SkipBuildAuthKeyCheck = 1`, `Network.EnterEncryptedModeRegionGroup = 8` (`doc/ForeverSetup.md:34-38`, `contrib/linux/configure_server.sh:43-46`).
   - `auth.realmlist`: `id = 70`, `port = 8085`, `timezone = 70`, `gamebuild = 70938`, `Region = 70`, `Battlegroup = 1` (`contrib/linux/configure_server.sh:61`).

---

## 2. REST Login (`LoginRESTService` / `LoginHttpSession`)

### 2.1 HTTP Session & Cookie Tracking

1. There is **no** per-IP connection cap on the login REST listener: `Network.ConnectionsPerIp` is enforced by `Battlenet::Session` (port 1119) and `WorldSocket` (port 8085) only (`src/server/bnetserver/Server/Session.cpp:129-137`, `src/server/game/Server/WorldSocket.cpp:134-141`); the REST socket chain is ip-ban check, TLS handshake, HTTP, read (`src/server/bnetserver/REST/LoginHttpSession.cpp:80-105`).
2. `LoginHttpSession::ObtainSessionState()` reads `Cookie: JSESSIONID=<uuid>` (`src/server/bnetserver/REST/LoginHttpSession.h:43`, `src/server/bnetserver/REST/LoginHttpSession.cpp:36-59`). If absent or unknown, it creates a `LoginSessionState` with a random UUID and returns `Set-Cookie: JSESSIONID=<uuid>; Path=/bnetserver; Domain=<request Host without its port>; Secure; HttpOnly; SameSite=None` — `Secure` is unconditional, also when the dev wildcard certificate makes the listener plain HTTP (`src/server/bnetserver/REST/LoginHttpSession.cpp:60-72`).
3. Two-step SRP login and 2FA authenticator requests must echo `Cookie: JSESSIONID=<uuid>` so `Srp` and `SrpLogin` persist across requests (`src/server/bnetserver/REST/LoginHttpSession.h:46-52`, `src/server/bnetserver/REST/LoginRESTService.cpp:357,623`).

### 2.2 Registered Endpoints

All endpoints are registered in `LoginRESTService::Start()` (`src/server/bnetserver/REST/LoginRESTService.cpp:98-148`) and serialize protobuf JSON via `JSON::Serialize` / `JSON::Deserialize` (`src/server/shared/JSON/ProtobufJSON.cpp:128-307`, `src/server/proto/Login/Login.proto:1-92`).

|Method|Path|Handler|Request (`Login.proto`)|Response (`Login.proto`)|Source|
|---|---|---|---|---|---|
|`GET`|`/bnetserver/login/`|`HandleGetForm`|None|`FormInputs`|`src/server/bnetserver/REST/LoginRESTService.cpp:98-100,216-225`|
|`POST`|`/bnetserver/login/srp/`|`HandlePostLoginSrpChallenge`|`LoginForm`|`SrpLoginChallenge`|`src/server/bnetserver/REST/LoginRESTService.cpp:102-104,689-774`|
|`POST`|`/bnetserver/login/`|`HandlePostLogin`|`LoginForm`|`LoginResult`|`src/server/bnetserver/REST/LoginRESTService.cpp:106-108,283-475`|
|`POST`|`/bnetserver/login/authenticator/`|`HandleAuthenticatorCode`|`LoginForm`|`LoginResult`|`src/server/bnetserver/REST/LoginRESTService.cpp:110-112,309-311,617-687`|
|`GET`|`/bnetserver/gameAccounts/`|`HandleGetGameAccounts`|`Authorization: Basic`|`GameAccountList`|`src/server/bnetserver/REST/LoginRESTService.cpp:114-116,227-274`|
|`GET`|`/bnetserver/portal/`|`HandleGetPortal`|None|`<host>:<BattlenetPort>`|`src/server/bnetserver/REST/LoginRESTService.cpp:118-120,276-281`|
|`POST`|`/bnetserver/refreshLoginTicket/`|`HandlePostRefreshLoginTicket`|`Authorization: Basic`|`LoginRefreshResult`|`src/server/bnetserver/REST/LoginRESTService.cpp:122-124,776-813`|
|`POST`|`/bnetserver/launcher/remember/`|`HandleLauncherRemember`|`Authorization: Basic`|`{"token":"<64 hex>","expires":<unix>}`|`src/server/bnetserver/REST/LoginRESTService.cpp:130-133,526-556`|
|`POST`|`/bnetserver/launcher/login/`|`HandleLauncherLogin`|`Authorization: Basic`|`{"login_ticket":"TC-...","account_id":N,"email":"...","game_accounts":[{"id":N,"name":"WoW1"}]}`|`src/server/bnetserver/REST/LoginRESTService.cpp:135-138,558-611`|
|`POST`|`/bnetserver/launcher/forget/`|`HandleLauncherForget`|`Authorization: Basic`|`{"ok":true}`|`src/server/bnetserver/REST/LoginRESTService.cpp:140-143,613-624`|

### 2.3 Account Normalization & Three Ways to Obtain `login_ticket`

`NormalizeLoginName(raw, defaultDomain)` appends `'@' + LoginREST.DefaultAccountDomain` (default `"local"`) when `account_name` has no `'@'`, then uppercases all Latin characters via `utf8::ToUpperOnlyLatin(login)` (`src/server/bnetserver/REST/LoginRESTService.cpp:42-55,173,317,710`). The SRP username is `srpUsername = ByteArrayToHexStr(SHA256(login))` (64 uppercase hex chars) (`src/server/bnetserver/REST/LoginRESTService.cpp:349,733`).

#### Way 1: Two-Step BNet SRP6 (`POST /bnetserver/login/srp/` -> `POST /bnetserver/login/`)

1. **`GET /bnetserver/login/`**: Returns `FormInputs` (`type: "LOGIN_FORM"`, inputs `account_name`, `password`, `log_in_submit`, and `srp_url: "http(s)://<host>:<port>/bnetserver/login/srp/"`) (`src/server/bnetserver/REST/LoginRESTService.cpp:154-171,216-225`).
2. **`POST /bnetserver/login/srp/`**: Client sends `LoginForm` with `{"input_id":"account_name","value":"<login>"}` (`src/server/proto/Login/Login.proto:28-39`). Server queries `LOGIN_SEL_BNET_CHECK_PASSWORD_BY_EMAIL`, creates `BnetSRP6v1<SHA256>` (`srp_version == 1`) or `BnetSRP6v2<SHA256>` (`srp_version == 2`), stores `Srp` and `SrpLogin` in the HTTP session, and returns `SrpLoginChallenge` (`src/server/bnetserver/REST/LoginRESTService.cpp:714-770,815-835`):
   - `version` (`1` or `2`), `iterations` (`1` for v1, `15000` for v2), `modulus` (`N` hex: 128B in v1, 256B in v2), `generator` (`"02"` — `BigNumber::AsHexStr()` is OpenSSL `BN_bn2hex`, so it is `"02"`, not `"2"`), `hash_function` (`"SHA-256"`), `username` (`srpUsername`), `salt` (32-byte `s` as uppercase hex in stored byte order — `ByteArrayToHexStr(Srp->s)` with the default `reverse = false`; hex-decode and use the bytes as-is in PBKDF2 (v2) or SHA256 (v1)), and `public_B` (`B` hex) (`src/server/bnetserver/REST/LoginRESTService.cpp:746-770`, `src/common/Cryptography/BigNumber.cpp:210-216`, `src/common/Utilities/Util.h:426`).
   - The `LoginForm` sent to this endpoint declares `platform_id`, `program_id` and `version` as proto2 `required` fields, but the handler reads only `inputs`; an empty `platform_id`/`program_id`/`version` works and is what the real client's SRP request looks like (`src/server/bnetserver/REST/LoginRESTService.cpp:689-718`, `src/server/proto/Login/Login.proto:28-39`).
3. **BNet SRP6 Math (`src/common/Cryptography/Authentication/SRP6.h:121-235`, `src/common/Cryptography/Authentication/SRP6.cpp:29-78,151-220`)**:
   - **Verifier `v`**: Little-endian bytes of `g^x mod N` (`BigNumber(v, true)`) (`src/common/Cryptography/Authentication/SRP6.cpp:36,74-77`).
   - **Multiplier `k`**: `BigNumber(SHA256(N_be || g_be_padded_to_N_bytes), false)` (`src/common/Cryptography/Authentication/SRP6.h:196,219`).
   - **Server public `B`**: `(g^b mod N + v * k) % N` (`src/common/Cryptography/Authentication/SRP6.cpp:66-69`).
   - **Private `x` (v1)**: `BigNumber(SHA256(s || SHA256(srpUsername || ":" || uppercase(password))), true)` (**little-endian** because `BigNumber(Container)` defaults to `littleEndian = true`) (`src/common/Cryptography/Authentication/SRP6.cpp:193-199`, `src/common/Cryptography/BigNumber.h:43`).
   - **Private `x` (v2)**: `PKCS5_PBKDF2_HMAC(srpUsername + ":" + password, s, 15000, EVP_sha512(), 64, xBytes)` (password is case-sensitive in v2!), read as big-endian `BigNumber x(xBytes, false)`; if `xBytes[0] & 0x80`, subtracts `2^512`, then `x = x % (N - 1)` (`src/common/Cryptography/Authentication/SRP6.cpp:206-220`).
   - **Scrambler `u` & secret `S`**: `u = BigNumber(SHA256(A_be_padded || B_be_padded), false)`; `S = (A * (v^u mod N))^b mod N` (`src/common/Cryptography/Authentication/SRP6.h:203-206,226-229`, `src/common/Cryptography/Authentication/SRP6.cpp:172`).
   - **`GetBrokenEvidenceVector(bn)`**: `bytes = (bn.GetNumBits() + 8) >> 3; return bn.ToByteVector(bytes, false);` (big-endian with one leading `0x00` byte when bit length is a multiple of 8) (`src/common/Cryptography/Authentication/SRP6.cpp:182-186`).
   - **Evidence `M1` & `M2`**: `M1 = BigNumber(SHA256(BrokenVec(A) || BrokenVec(B) || BrokenVec(S)), false)`; `M2 = BigNumber(SHA256(BrokenVec(A) || BrokenVec(M1) || BrokenVec(S)), false)` (`src/common/Cryptography/Authentication/SRP6.h:139-148`, `src/common/Cryptography/Authentication/SRP6.cpp:156-160,174-175`).
4. **`POST /bnetserver/login/` (SRP proof)**: Client sends `LoginForm` with `account_name`, `public_A` (hex) and `client_evidence_M1` (hex) plus `Cookie: JSESSIONID=<uuid>`. The server takes the stored `Srp`/`SrpLogin` out of the session (one challenge answers one POST: a second POST with the same cookie falls back to the password path and fails), verifies the account name equals the one the challenge was made for, calls `VerifyClientEvidence(A, M1)` once, and answers `LoginResult { authentication_state: "DONE", login_ticket: "TC-...", server_evidence_M2: "<M2 hex>" }`. The client SHOULD check `server_evidence_M2` against its own `M2` (`src/server/bnetserver/REST/LoginRESTService.cpp:340-365,453-470`).
   - **Every number the server prints is `BigNumber::AsHexStr()` = OpenSSL `BN_bn2hex`, which drops leading zero bytes** (`src/common/Cryptography/BigNumber.cpp:210-216`), and `server_evidence_M2` is one of them (`src/server/bnetserver/REST/LoginRESTService.cpp:364`). A 32-byte `M2` whose first byte is `0x00` therefore arrives as 62 hex characters, not 64 (a value of zero would arrive as `"0"`); a client that compares decoded bytes verbatim rejects one login in 256. Decode it as a number and left-pad to the digest length before the comparison. `modulus`, `generator` and `public_B` are integers too, so the stripping is harmless for them (they are parsed as numbers, not byte strings); `salt` is the exception, it is `ByteArrayToHexStr(s)` of the fixed 32-byte array and always 64 characters.

#### Way 2: Direct Password Form (`POST /bnetserver/login/` without `/srp/`)

If `session->GetSessionState()->Srp` is null, `HandlePostLogin` reads `account_name` and `password` from `LoginForm.inputs` (uppercasing `password` only for `SrpVersion::v1`) and verifies `v == g^CalculateX(srpUsername, password, s) mod N` (`src/server/bnetserver/REST/LoginRESTService.cpp:343-356`, `src/common/Cryptography/Authentication/SRP6.cpp:53-56,71-78`).

#### Way 3: Launcher Token (`POST /bnetserver/launcher/login/`)

`POST /bnetserver/launcher/remember/` with `Authorization: Basic base64("<login_ticket>:")` (the ticket must be a valid `"TC-" + 40 hex` with an unexpired row) inserts a 30-day token and returns `{"token":"<64 hex>","expires":<unix>}`; `POST /bnetserver/launcher/login/` with `Authorization: Basic base64("<token>:")` (64 hex characters) returns `{"login_ticket":"TC-...","account_id":N,"email":"...","game_accounts":[{"id":N,"name":"WoW1"}]}` (or HTTP 401 `{"error":"expired"}` / HTTP 403 `{"error":"banned"}`), and extends the token by another 30 days; `POST /bnetserver/launcher/forget/` deletes it and returns `{"ok":true}` (`src/server/bnetserver/REST/LoginRESTService.cpp:526-624`).

#### Optional 2FA & Final `LoginResult`

If `battlenet_accounts.authenticator_secret` is set, `HandlePostLogin` returns `authentication_state: "AUTHENTICATOR"` and `next_url: ".../bnetserver/login/authenticator/"`, expecting `authenticator_input` (6-digit TOTP) (`src/server/bnetserver/REST/LoginRESTService.cpp:433-451,617-687`). On success, the server stores a 1-hour `"TC-" + ByteArrayToHexStr(GetRandomBytes<20>())` ticket via `LOGIN_UPD_BNET_AUTHENTICATION` and returns `LoginResult { authentication_state: "DONE", login_ticket: "TC-...", server_evidence_M2: "..." }` (`src/server/bnetserver/REST/LoginRESTService.cpp:172,453-470`). **The client must keep `login_ticket`**.

---

## 3. The BNet RPC Connection (Port 1119)

### 3.1 Wire Framing (`Battlenet::Session`)

Every frame in both directions over TLS port `1119` has three parts (`src/server/bnetserver/Server/Session.cpp:73-74,189-267,341-427`):

|Offset|Type|Endianness|Description|Source|
|---|---|---|---|---|
|`0`|`uint16`|**Big-endian**|Length `H` of `bgs.protocol.Header` (`EndianConvertReverse`)|`src/server/bnetserver/Server/Session.cpp:200,368-371`|
|`2`|`uint8[H]`|Protobuf|`bgs.protocol.Header`: `service_id` (`0` = request/push, `0xFE` = response), `method_id` (`& 0x3FFFFFFF`), `token`, `size` (`S <= 0x100000`), `status` (`0` = `ERROR_OK`), `service_hash` (`fixed32`), `ciid`|`src/server/proto/Client/rpc_types.pb.h:707-892`, `src/server/bnetserver/Server/Session.cpp:193-250,387-414`|
|`2+H`|`uint8[S]`|Protobuf|Serialized request or response message of `S = header.size()` bytes|`src/server/bnetserver/Server/Session.cpp:203,389-408`|

### 3.2 Registered Services & Methods (`ServiceDispatcher.cpp:41-64`)

Every `service_hash` below was re-read in round 1 from the `OriginalHash` constant of the generated service class (`src/server/proto/Client/**/*.pb.h`); every `method_id` is the number the generated `ServiceBase` dispatches on (`<service>.pb.cc`, `switch (methodId & 0x3FFFFFFF)`), and a client sends it without the `0x40000000`/`0x80000000` direction flags (`src/server/proto/Client/connection_service.pb.cc:1495`).

|Dir|Service|`service_hash`|`method_id`|Method|Request / Push|Response|Source|
|---|---|---|---|---|---|---|---|
|C->S|`connection.v1.ConnectionService`|`0x65446991`|`1`|`Connect`|`ConnectRequest`|`ConnectResponse`|`src/server/bnetserver/Services/ConnectionService.cpp:32-57`|
|C->S|`connection.v1.ConnectionService`|`0x65446991`|`5`|`KeepAlive`|`NoData`|None|`src/server/bnetserver/Services/ConnectionService.cpp:59-63`|
|C->S|`connection.v1.ConnectionService`|`0x65446991`|`7`|`RequestDisconnect`|`DisconnectRequest`|Closes socket|`src/server/bnetserver/Services/ConnectionService.cpp:65-75`|
|C->S|`authentication.v1.AuthenticationService`|`0x0DECFC01`|`1`|`Logon`|`v1::LogonRequest`|`NoData` (+ push)|`src/server/bnetserver/Services/AuthenticationService.cpp:229-259`|
|C->S|`authentication.v1.AuthenticationService`|`0x0DECFC01`|`7`|`VerifyWebCredentials`|`VerifyWebCredentialsRequest`|`NoData` (+ push)|`src/server/bnetserver/Services/AuthenticationService.cpp:261-272`|
|S->C|`challenge.v1.ChallengeListener`|`0xBBDA171F`|`3`|`OnExternalChallenge`|`ChallengeExternalRequest`|None|`src/server/bnetserver/Services/AuthenticationService.cpp:310-315`|
|S->C|`authentication.v1.AuthenticationListener`|`0x71240E35`|`5`|`OnLogonComplete`|`v1::LogonResult`|None|`src/server/bnetserver/Services/AuthenticationService.cpp:283-306`|
|C->S|`authentication.v2.client.AuthenticationService`|`0xC02F8216`|`1`|`Logon`|`v2::client::LogonRequest`|`NoData` (+ push)|`src/server/bnetserver/Services/AuthenticationService.cpp:330-358`|
|C->S|`authentication.v2.client.AuthenticationService`|`0xC02F8216`|`2`|`VerifyAuthToken`|`VerifyAuthTokenRequest`|`NoData` (+ push)|`src/server/bnetserver/Services/AuthenticationService.cpp:360-371`|
|S->C|`authentication.v2.client.AuthenticationListener`|`0x9DA8116B`|`4`|`OnExternalChallenge`|`ExternalChallengeNotification`|None|`src/server/bnetserver/Services/AuthenticationService.cpp:425-430`|
|S->C|`authentication.v2.client.AuthenticationListener`|`0x9DA8116B`|`1`|`OnLogonComplete`|`LogonCompleteNotification`|None|`src/server/bnetserver/Services/AuthenticationService.cpp:389-421`|
|C->S|`account.v1.AccountService`|`0x62DA0891`|`30,31`|`GetAccountState` / `GetGameAccountState`|`Get*StateRequest`|`Get*StateResponse`|`src/server/bnetserver/Services/AccountService.cpp:30-77`|
|C->S|`account.v2.client.AccountService`|`0x22DC2464`|`101,104,201,203`|`GetAccountInfo` / `GetGameAccountInfo`|`Get*Request`|`Get*Response`|`src/server/bnetserver/Services/AccountService.cpp:86-159`|
|C->S|`game_utilities.v1.GameUtilitiesService`|`0x3FC1274D`|`1,10`|`ProcessClientRequest` / `GetAllValuesForAttribute`|`ClientRequest` / `GetAllValues*`|`ClientResponse` / `GetAllValues*`|`src/server/bnetserver/Services/GameUtilitiesService.cpp:441-482`|
|C->S|`game_utilities.v2.client.GameUtilitiesService`|`0x5DBB51C2`|`1,2`|`ProcessTask` / `GetAllValuesForAttribute`|`ProcessTaskRequest` / `GetAllValues*`|`ProcessTaskResponse` / `GetAllValues*`|`src/server/bnetserver/Services/GameUtilitiesService.cpp:543-579`|

### 3.3 Ordered RPC Sequence from Connect to Realm Join

1. **`ConnectionService::Connect` (`0x65446991`, method `1`)**: Returns `ConnectResponse` with `server_id`, `server_time`, `use_bindless_rpc`, and `ciid` (`src/server/bnetserver/Services/ConnectionService.cpp:32-57`).
2. **`AuthenticationService::Logon` (V1 `0x0DECFC01` method `1`, or V2 `0xC02F8216` method `1`)**:
   - Client sends `program = "WoW"` (V1) or `title_id = 0x576F57` (V2), valid `platform` (`"Wn64"`, `"Mc64"`), valid `locale` (`"enUS"`), `application_version = 70938`, and `cached_web_credentials = "<login_ticket>"` (V1) or `logon_options.auth_token = "<login_ticket>"` (V2) (`src/server/bnetserver/Services/AuthenticationService.cpp:229-259,330-358`, `src/server/shared/Realm/ClientBuildInfo.cpp:25-33`). If the ticket is omitted, the server pushes `OnExternalChallenge` (`web_auth_url = "http(s)://<host>:<port>/bnetserver/login/"`), and the client later calls `VerifyWebCredentials` (V1 method `7`) or `VerifyAuthToken` (V2 method `2`) (`src/server/bnetserver/Services/AuthenticationService.cpp:256-272,308-316,354-371,423-431`).
   - `VerifyEffectiveCredentials()` validates `login_ticket` via `LOGIN_SEL_BNET_AUTHENTICATION`, loads game accounts (`WoW1`), replies `NoData` (`status = 0`), and pushes `OnLogonComplete` (V1 `0x71240E35` method `5` or V2 `0x9DA8116B` method `1`) containing `error_code = 0`, `account_id`, `game_account` (`id`, `region = 2`, `title_id = 0x576F57`), and a 64-byte random `session_key` (`src/server/bnetserver/Services/AuthenticationService.cpp:36-206,283-306,389-421`).
3. **GameUtilities Encoding Rules (`src/server/bnetserver/Services/GameUtilitiesService.cpp:36-56,444-465`)**:
   - `ParseParamName` strips the last `'_'` and everything after it from attributes whose name starts with `"Command_"` (e.g., `"Command_RealmListTicketRequest_v1_b9"` -> `"Command_RealmListTicketRequest_v1"`). **A client must therefore send every command attribute with a trailing `_<suffix>`** (the real 1.60 client uses `_b9`): the bare `"Command_RealmListRequest_v1"` would be reduced to `"Command_RealmListRequest"` and match no command (`src/server/bnetserver/Services/GameUtilitiesService.cpp:36-46`).
   - `CompressJson(json)` prepends a 4-byte LE `uint32` equal to `json.length() + 1` (includes the trailing `\0`), followed by zlib `compress()` (RFC 1950 header, not raw deflate) of `<TypePrefix>:<json>\0` (`src/server/bnetserver/Services/GameUtilitiesService.cpp:48-60`, `src/server/shared/Realm/RealmList.cpp:36-52`).
   - On the response, the `Variant` oneof used is `string_value` for strings, `blob_value` for raw bytes, `int_value` for int64 counts. `ProtobufJSON` encodes protobuf `bytes` fields (such as `info.secret`) as a JSON array of `uint32` integers `[b0, b1, ..., b31]` (`src/server/shared/JSON/ProtobufJSON.cpp:110-121,280-294`).
4. **GameUtilities Calls (`0x3FC1274D` V1 or `0x5DBB51C2` V2)**:
   - **4a. `Command_RealmListTicketRequest_v1`** (`GetRealmListTicket`, `src/server/bnetserver/Services/GameUtilitiesService.cpp:240-302`):
     - Client sends `"Command_RealmListTicketRequest_v1_b9"`, `"Param_Identity"` (uncompressed `"JSONRealmListTicketIdentity:"` + JSON `{ "gameAccountID": <fixed32>, "gameAccountRegion": 2 }`), and `"Param_ClientInfo"` (uncompressed `"JSONRealmListTicketClientInformation:"` + JSON where `info.secret` is a 32-byte `uint32` array `clientSecret`, `info.version.versionBuild = 70938`, and `(info.platformType, info.clientArch, info.type)` form the `VariantId`) (`src/server/bnetserver/Services/GameUtilitiesService.cpp:240-287`, `src/server/proto/RealmList/RealmList.proto:13-42,82-86`).
     - The identity's `gameAccountID` must be one of the `game_account_id` lows from `OnLogonComplete`; the client secret must be exactly 32 bytes. Server saves `clientSecret`, the build variant and the game account in the session, updates `account.last_ip/last_login_build/last_login_locale/last_login_os`, and returns `"Param_RealmListTicket"` = `"AuthRealmListTicket\0"` (20B) as `blob_value` (`src/server/bnetserver/Services/GameUtilitiesService.cpp:277-300`). There is **no** build check in this handler; the build (`session->GetBuild()`) is checked later against `Realm.acceptsBuild` in the realm list and the join.
   - **4b. `Command_SuperDistrictListRequest_v1`** (optional; `src/server/bnetserver/Services/GameUtilitiesService.cpp:375-391`, `src/server/shared/Realm/RealmList.cpp:373-387`): Returns `"Param_SuperDistrictList"` = `CompressJson("JSONSuperDistrictList:{\"superDistricts\":[{\"superDistrictID\":2,\"disallowLogin\":false,\"holdDownUntilTime\":0}]}")`; only rulesets with a realm are listed (`RealmList.cpp:387-397`).
   - **4c. `GetAllValuesForAttribute("Command_RealmListRequest_v1_b9")`** (`src/server/bnetserver/Services/GameUtilitiesService.cpp:132-151`): Returns the sub-region strings of `RealmList::GetSubRegions()`, here `"70-1-70"`; `Realm::GetSubRegionAddress()` returns `"<region>-<site>-<realm>"` (`src/server/shared/Realm/Realm.cpp:190-195`).
   - **4d. `Command_LastCharPlayedRequest_v1`** (optional, `src/server/bnetserver/Services/GameUtilitiesService.cpp:153-238`): Takes the sub-region `"70-1-70"` as its own value, returns `"Param_RealmEntry"`, `"Param_LastPlayedTime"`, `"Param_UtilityInfo"`, and optionally `"Param_CharacterName"` / `"Param_CharacterGUID"`.
   - **4e. `Command_RealmListRequest_v1`** (`GetRealmList`, `src/server/bnetserver/Services/GameUtilitiesService.cpp:304-334`): Takes the sub-region `"70-1-70"` as its own value, returns `"Param_RealmList"` (`CompressJson("JSONRealmListUpdates:" + ...)`, a `RealmListUpdates` with `updates[i].wowRealmAddress`, `updates[i].deleting`, `updates[i].update.{name,cfgRealmsID,cfgContentSetID,...}` plus the server-injected `"superDistrictID":2`; `wowRealmAddress = (70 << 24) | (1 << 16) | 70 = 0x46010046 = 1174470726`) and `"Param_CharacterCountList"` (`CompressJson("JSONRealmCharacterCountList:" + ...)`) (`src/server/shared/Realm/Realm.h:112-132`, `src/server/shared/Realm/RealmList.cpp:316-347,413-444`).
   - **4f. `Command_RealmJoinRequest_v1`** (`JoinRealm`, `src/server/bnetserver/Services/GameUtilitiesService.cpp:336-356`, `src/server/shared/Realm/RealmList.cpp:446-506`):
     - Client sends `"Command_RealmJoinRequest_v1_b9"` and `"Param_RealmAddress"` (`uint_value = 0x46010046`).
     - Server generates 32 random bytes `serverSecret`, forms 64-byte `keyData = clientSecret (32B) || serverSecret (32B)`, and writes `keyData` to `account.session_key_bnet` plus the client's build to `account.client_build` (and last_ip, locale, os, timezone_offset) with `LOGIN_UPD_BNET_GAME_ACCOUNT_LOGIN_INFO` (`src/server/shared/Realm/RealmList.cpp:471-487`, `src/server/database/Database/Implementation/LoginDatabase.cpp:132`); the world server later uses that `client_build` for the opcode numbering and the `build_info` row. Then it returns:
       1. `"Param_RealmJoinTicket"` (`blob_value`, **uncompressed** JSON of `RealmJoinTicket { gameAccount, platform, type, clientArch }`; `platform`/`type`/`clientArch` are the fixed32 FourCC numbers from `Param_ClientInfo`).
       2. `"Param_ServerAddresses"` (`blob_value`, `CompressJson("JSONRealmListServerIPAddresses:" + ...)` with `families[0].family` 1 (v4) or 2 (v6) and `families[0].addresses[0].{ip,port}`).
       3. `"Param_JoinSecret"` (`blob_value`, 32 raw bytes `serverSecret`).

---

## 4. World Socket (Port 8085)

*4.1-4.3 verified against the code; round 2 ran the whole stream against the live server (`WORLD-R1-NOTES.md`).*

### 4.1 Banners, Packet Framing, and `ClassicOpcodes` Translation

1. **53-byte ASCII banners**: Upon TCP connect to port `8085`, the server sends `"WORLD OF WARCRAFT CONNECTION - SERVER TO CLIENT - V2\n"` (`53B`), and the client must reply `"WORLD OF WARCRAFT CONNECTION - CLIENT TO SERVER - V2\n"` (`53B`) within `10s`, after which the server initializes raw deflate (`windowBits = -15`) and sends `SMSG_AUTH_CHALLENGE` (`src/server/game/Server/WorldSocket.cpp:85-86,181-288`).
2. **16-byte `PacketHeader` + 4-byte Opcode** (`src/server/game/Server/WorldSocket.h:68-83`, `src/server/game/Server/WorldSocket.cpp:375-422,547-589`):
   - `uint32 Size` (`4B` LE = `payload_size + 4`, max `0xFFFF` for normal client packets or `0x100000` for `CMSG_HOTFIX_REQUEST`).
   - `uint8 Tag[12]` (`12B` `AES-256-GCM` tag; `0x00 * 12` before encryption starts, `src/common/Cryptography/Authentication/WorldPacketCrypt.cpp:65,80`).
   - `uint32 Opcode` (`4B` LE wire opcode, encrypted as the first 4 bytes of the GCM plaintext/ciphertext) + `Size - 4` payload bytes.
   - **Bit-packing (`ByteBuffer`)**: Bits pack **MSB-first** (`WriteBit` writes bit 7 down to bit 0). Any numeric or byte read/write (`append` / `read`) automatically flushes or resets the bit position (`src/server/shared/Packets/ByteBuffer.h:159-258,495,529`, `src/server/shared/Packets/ByteBuffer.cpp:88-95`).
3. **`ClassicOpcodes` Wire Translation for Build `70938`** (`src/server/game/Server/Protocol/ClassicOpcodes.cpp:102-378`, `src/server/game/Server/WorldSocket.cpp:422,549,576,593`):
   - **Client -> Server (`TranslateClientOpcode`)** (`src/server/game/Server/Protocol/ClassicOpcodes.cpp:251-281,303-306`): the `.inc` table is not only for movement/player — `TranslateClientOpcode70245` looks every opcode up in `ClassicClientOpcodes.inc` first, and group `0x44` has 175 entries of which 33 are not "core + 0x10000" (e.g. wire `0x440182` -> core `0x43018D`). Wire -> core for `clientBuild >= 70790`: (a) the 70790 `ClientNew` list is empty, no change; (b) 70755: in group `0x44`, wire `0x440071`/`0x440072` are unknown and a wire index `>= 0x73` is reduced by `2`; (c) group shift (realm build `>= 70291` or `Classic.OpcodeGroupShift`): group `0x46` -> `0x45`, and in group `0x44` an index `> 0x13B` is reduced by `1`; (d) look the result up in `ClassicClientOpcodes.inc` (keys are 70245 wire numbers); if absent and the group is `0x2B..0x45`, subtract `1` from the group. A client must invert this exactly (best: build the inverse table from the `.inc`), not apply `+0x10000`. Before `CMSG_AUTH_SESSION` is processed the server uses `clientBuild = 0` (steps a/b skipped); after it, `clientBuild = account.client_build` (`src/server/game/Server/WorldSocket.cpp:422,931`).
   - **Server -> Client (`TranslateServerOpcode`)**:
     - Step 1 (`TranslateServerOpcode70245`): Applies `ServerOpcodes` overrides (`SMSG_ACCOUNT_DATA_TIMES -> 0x4601B5`, `SMSG_LOGIN_SET_TIME_SPEED -> 0x4601B8`, `SMSG_SERVER_TIME_OFFSET -> 0x4601BF`, `SMSG_MIRROR_VARS -> 0x460371`), decrements `0x45017E..0x4501C0` by `1`, moves chat group `0x4A` (index `>= 4` gets `+2`) and guild group `0x51` (index `>= 0x1C` gets `-1`), and then adds `+1` to groups `0x45..0x6A` (`src/server/game/Server/Protocol/ClassicOpcodes.cpp:332-340`). `SMSG_AUTH_CHALLENGE` is translated with `clientBuild = 0` (`src/server/game/Server/WorldSocket.cpp:203,549`), so only steps 1-2 apply — which still gives `0x4E0000`.
     - Step 2 (`ExtraGroupShift() == 1` for `realm->Build >= 70291`): Increments group `0x46` indices `>= 0x98` by `+1`, then adds `+1` to groups `0x46..0x6B` (so `0x45 -> 0x47`, `0x49 -> 0x4B`, `0x4C -> 0x4E`, `0x5C -> 0x5E`, `0x5E -> 0x60`).
     - Steps 3 & 4 (`Build70755Changes` + `Build70790Changes` for `_clientBuild >= 70790`): In group `0x47`, `0x470125` added in 70755 and removed in 70790 cancels out, leaving `+1` on all group `0x47` opcodes with Step 2 index `>= 0x00ED`.

### 4.2 `SMSG_AUTH_CHALLENGE`, `CMSG_AUTH_SESSION`, Key Derivation, and Encryption

1. **`SMSG_AUTH_CHALLENGE`** (`Opcodes.h`: `0x4C0000`, wire: `0x4E0000`, plaintext, 65B): `uint32 DosChallenge[8]` (`32B`), `uint8 Challenge[32]` (`32B` `_serverChallenge`), `uint8 DosZeroBits` (`1`) (`src/server/game/Server/WorldSocket.cpp:278-288`, `src/server/game/Server/Packets/AuthenticationPackets.cpp:68-74`).
2. **`CMSG_AUTH_SESSION`** (`Opcodes.h`: `0x440001`, wire: `0x460001`, plaintext) (`src/server/game/Server/Packets/AuthenticationPackets.cpp:76-93`, `src/server/game/Server/WorldSocket.cpp:686-950`):
   - Fields in order: `uint64 DosResponse` (`8B`), `uint32 RegionID` (`4B`), `uint32 BattlegroupID` (`4B`), `uint32 RealmID` (`4B`, must be `70`), `uint8 LocalChallenge[32]` (`32B`), `uint8 Digest[24]` (`24B`), `Bits<1>(UseIPv6)` (`1` bit, auto-reset by next `uint32`), `uint32 realmJoinTicketSize` (`4B`), `uint8 RealmJoinTicket[realmJoinTicketSize]` (raw JSON from `Param_RealmJoinTicket`).
   - **Digest check bypass**: With `AuthCheckSeed` (`{ 0xDE, 0x3A, ..., 0xB0 }`), the full formula is `HMAC_SHA512(key = SHA512(KeyData (64B) || buildAuthKey (16B)), data = LocalChallenge (32B) || _serverChallenge (32B) || AuthCheckSeed (32B))[0..23]` (`src/server/game/Server/WorldSocket.cpp:61-62,780-791`). Because ForeverCore sets `Network.SkipBuildAuthKeyCheck = 1` (`doc/ForeverSetup.md:36`) and has no `70938` row in `auth.build_auth_key`, `HandleAuthSessionCallback` **skips the `Digest` check completely** (`src/server/game/Server/WorldSocket.cpp:756-772`).
3. **40-byte `_sessionKey` & 32-byte `_encryptKey`** (`src/server/game/Server/WorldSocket.cpp:63-68,796-816`, `src/common/Cryptography/SessionKeyGenerator.h:28-52`):
   - `SessionKeySeed` (`32B`): `{ 0xE8, 0x1E, 0x8B, 0x59, 0x27, 0x62, 0x1E, 0xAA, 0x86, 0x15, 0x18, 0xEA, 0xC0, 0xBF, 0x66, 0x8C, 0x6D, 0xBF, 0x83, 0x93, 0xBC, 0xAA, 0x80, 0x52, 0x5B, 0x1E, 0xDC, 0x23, 0xA0, 0x12, 0xB7, 0x50 }`.
   - `seed = HMAC_SHA512(key = SHA512(KeyData (64B)), data = _serverChallenge (32B) || LocalChallenge (32B) || SessionKeySeed (32B))` (`64B`).
   - `_sessionKey` (`40B`) = first 40 bytes of `SHA512(SHA512(seed[0..31]) || 0x00*64 || SHA512(seed[32..63]))`.
   - `EncryptionKeySeed` (`32B`): `{ 0x71, 0xC9, 0xED, 0x5A, 0xA7, 0x0E, 0x4D, 0xFF, 0x4C, 0x36, 0xA6, 0x5A, 0x3E, 0x46, 0x8A, 0x4A, 0x5D, 0xA1, 0x48, 0xC8, 0x30, 0x47, 0x4A, 0xDE, 0xF6, 0x0D, 0x6C, 0xBE, 0x6F, 0xE4, 0x55, 0x73 }`.
   - `_encryptKey` (`32B`) = first 32 bytes of `HMAC_SHA512(key = _sessionKey (40B), data = LocalChallenge (32B) || _serverChallenge (32B) || EncryptionKeySeed (32B))`.
4. **`SMSG_ENTER_ENCRYPTED_MODE` & `CMSG_ENTER_ENCRYPTED_MODE_ACK`** (`src/server/game/Server/WorldSocket.cpp:952-981,1152-1163`, `src/server/game/Server/Packets/AuthenticationPackets.cpp:257-266,367-388`):
   - Sent **unencrypted** after `Network.EnterEncryptedModeDelay` (`1500` ms on realm socket, `0` ms on instance socket): `int32 RegionGroup` (`8`), `uint8 Signature[64]` (`Ed25519::Sign` over `HMAC_SHA512(_encryptKey, {1} || EnableEncryptionSeed)` with `EnableEncryptionContext`), `Bits<1>(Enabled = true)`, `FlushBits()` (`0x80`).
   - Client replies with **unencrypted** `CMSG_ENTER_ENCRYPTED_MODE_ACK` (`Opcodes.h`: `0x440005`, wire: `0x460005`, 0B), after which `HandleEnterEncryptedModeAck()` calls `_authCrypt.Init(_encryptKey)`.
5. **`AES-256-GCM` (`WorldPacketCrypt`) & `SMSG_COMPRESSED_PACKET`** (`src/common/Cryptography/Authentication/WorldPacketCrypt.cpp:22-84`, `src/server/game/Server/WorldSocket.cpp:50-59,558-619`):
   - 12-byte IV is `uint64 Counter` (8B LE) || `uint32 Magic` (4B LE: `0x544E4C43` `"CLNT"` for C->S, `0x52565253` `"SRVR"` for S->C). Because `_clientCounter` and `_serverCounter` increment even on unencrypted packets before `Init()`, the **first encrypted packet in each direction uses `Counter = 2`**.
   - Encrypted S->C packets whose payload (excluding the 4-byte opcode) is larger than `0x400` (`1024`) bytes are sent as `SMSG_COMPRESSED_PACKET` (`Opcodes.h`: `0x4C000E`, wire: `0x4E000E`). Its body is: `uint32 UncompressedSize` (= payload + 4), `uint32 UncompressedAdler` = `adler32` seeded `0x9827D8F1` over the 4 opcode bytes (the inner packet's translated wire opcode, LE) then the payload, `uint32 CompressedAdler` = `adler32` seeded `0x9827D8F1` over the compressed bytes, then the compressed bytes. The compressed data is the inner wire opcode (4B LE) followed by the payload. ONE raw-deflate stream (`deflateInit2` `windowBits -15`, `memLevel 8`, `Z_DEFAULT_STRATEGY`, level = config `Compression`) is created per connection and is NEVER reset: each packet is `deflate(opcode, Z_NO_FLUSH)` + `deflate(payload, Z_SYNC_FLUSH)`, so the compressor state carries across packets (the decompressor must keep one `inflate` stream too) (`src/server/game/Server/WorldSocket.cpp:50-59,558-619`, `src/common/Cryptography/Authentication/WorldPacketCrypt.cpp:22-84`).

---

## 5. After Auth (`InitializeSessionCallback` & Character List)

*Verified against the code and a live server in round 2; corrections in `WORLD-R1-NOTES.md` (the list is regionwide, and the companions are sent unasked).*

### 5.1 Initial Burst After `CMSG_ENTER_ENCRYPTED_MODE_ACK`

Unless queued (`SMSG_WAIT_QUEUE_UPDATE` `0x450002` / wire `0x470002`, `SMSG_WAIT_QUEUE_FINISH` `0x450003` / wire `0x470003`), `WorldSession::InitializeSessionCallback()` immediately sends 6 packets (`src/server/game/Server/WorldSession.cpp:1549-1556`):
1. **`SMSG_AUTH_RESPONSE`** (`0x450001`, wire `0x470001`): `uint32 Result` (`0`), `OptionalInit(SuccessInfo)` (`true`), `OptionalInit(WaitInfo)` (`false`), `FlushBits()`, followed by `AuthSuccessInfo` (`src/server/game/Handlers/AuthHandler.cpp:24-50`, `src/server/game/Server/Packets/AuthenticationPackets.cpp:102-235`).
2. **`SMSG_FEATURE_SYSTEM_STATUS_GLUE_SCREEN`** (`0x450064`, wire `0x470064`, 88B): 6 flag bytes (`0x80, 0, 0, 0, 0, 0`), `CommercePricePollTimeSeconds`, `RedeemForBalanceAmount`, `MaxCharactersOnThisRealm`, `ContentSetID` (`137`), etc. (`src/server/game/Server/Packets/SystemPackets.cpp:247-285`).
3. **`SMSG_CACHE_VERSION`** (`0x49000E`, wire `0x4B000E`, 4B): `uint32 CacheVersion` (`src/server/game/Handlers/AuthHandler.cpp:94-101`).
4. **`SMSG_TUTORIAL_FLAGS`** (`0x450268`, wire `0x47026A`, 32B): `uint32 TutorialData[8]` (`src/server/game/Handlers/AuthHandler.cpp:103-109`).
5. **`SMSG_ACCOUNT_DATA_TIMES`** (`0x4501B6`, wire `0x4701B7`): `ObjectGuid::Empty` (`uint16(0)`), `int64 ServerTime`, `int64 AccountTimes[20]` (`src/server/game/Server/Packets/ClientConfigPackets.cpp:23-31`).
6. **`SMSG_AVAILABLE_HOTFIXES`** (`0x490001`, wire `0x4B0001`): `int32 VirtualRealmAddress`, `uint32 Count`, `(int32 PushID, uint32 UniqueID)` per hotfix (`src/server/game/Server/Packets/HotfixPackets.cpp:79-89`).
7. **`SMSG_BATTLE_NET_CONNECTION_STATUS`** (`0x4502B1`, wire `0x4702B3`, 1B): `Bits<2>(State = 1)`, `Bits<1>(SuppressNotification = false)`, `FlushBits()` (`0x40`) (`src/server/game/Server/Packets/BattlenetPackets.cpp:59-66`).

### 5.2 `CMSG_ENUM_CHARACTERS` and `SMSG_ENUM_CHARACTERS_RESULT`

1. **`CMSG_ENUM_CHARACTERS`** (`0x430014`, wire `0x440014`, 0B) (`src/server/game/Handlers/CharacterHandler.cpp:676-694`).
2. **`SMSG_ENUM_CHARACTERS_RESULT`** (`0x450018`, wire `0x470018`) (`src/server/game/Handlers/CharacterHandler.cpp:412-552`, `src/server/game/Server/Packets/CharacterPackets.cpp:208-467`):
   - **Header**: 10 bits (`Bits<1>(Success)`, `Bits<1>(Realmless)` = `true` when characters exist, `Bits<1>(IsDeletedCharacters)`, `Bits<1>(IgnoreNewPlayerRestrictions)`, `Bits<1>(IsRestrictedNewPlayer)`, `Bits<1>(IsNewcomerChatCompleted)`, `Bits<1>(IsRestrictedTrial)`, `Bits<1>(IsAccountLapsedPlayer)`, `OptionalInit(ClassDisableMask)`, `Bits<1>(ForceCharacterListSort)` — auto-flushed into 2 bytes by the next `uint32`), then `uint32 Characters.size()` (`0` in Realmless mode), `uint32 RegionwideCharacters.size()`, `int32 MaxCharacterLevel`, `uint32 RaceUnlockData.size()`, `uint32 UnlockedConditionalAppearances.size()`, `uint32 RaceLimitDisables.size()`, `uint32 WarbandGroups.size()`, optional `uint32 ClassDisableMask`, and the 6 arrays (`src/server/game/Server/Packets/CharacterPackets.cpp:427-465`).
   - **Per-character entry in `RegionwideCharacters` (`CharacterInfoBasic` + trailer, `src/server/game/Server/Packets/CharacterPackets.cpp:222-286,323-334`)**:
     1. Packed `ObjectGuid Guid` (`uint16` LE mask + non-zero GUID bytes, `src/server/game/Entities/Object/ObjectGuid.cpp:1087-1137`), `uint32 VirtualRealmAddress`, `uint16 ListPosition`, `uint8 RaceID`, `uint8 SexID`, `uint8 ClassID`, `int16 SpecID`, `uint32 Customizations.size()`, `uint8 ExperienceLevel`, `int32 MapID`, `int32 ZoneID`, `float PreloadPos.X/Y/Z`, `uint64 GuildClubMemberID`, packed `ObjectGuid GuildGUID`, `uint32 Flags`, `uint32 Flags2`, `uint32 Flags3`, `uint32 Flags4`, `uint8 CantLoginReason`, `uint32 PetCreatureDisplayID`, `uint32 PetExperienceLevel`, `uint32 PetCreatureFamilyID`.
     2. `19` x `VisualItemInfo` (`19 * 23 = 437B`: `uint32 ItemID`, `uint32 TransmogrifiedItemID`, `uint8 Subclass`, `uint8 InvType`, `uint32 DisplayID`, `uint32 DisplayEnchantID`, `int32 SecondaryItemModifiedAppearanceID`, `uint8 SheatheCategory`).
     3. `int32 SaveVersion` (`>= 49`), `int64 CreateTime`, `int64 LastActiveTime`, `int32 LastLoginVersion`, `CustomTabardInfo` (5 x `int32`), `uint32 ProfessionIds[2]`, `int32 TimerunningSeasonID`, `uint32 OverrideSelectScreenFileDataID`, `uint32 RealmQueue`, `int32 SuperDistrictID` (`2`).
     4. `Customizations.size()` x `ChrCustomizationChoice` (`uint32 OptionID`, `uint32 ChoiceID`).
     5. Bits (`15` bits = 2B): `SizedString::BitsSize<6>(Name)`, `SizedString::BitsSize<6>(surname)`, `Bits<1>(FirstLogin)`, `Bits<1>(RealmInfoFound)` (`true`), `Bits<1>(IsRealmOffline)` (`false`), explicit `FlushBits()`, then `SizedString::Data(Name)` and `SizedString::Data(surname)`.
     6. Regionwide trailer (`23B`): `uint64 Money`, `float AvgEquippedItemLevel`, `float CurrentSeasonMythicPlusOverallScore`, `int32 CurrentSeasonBestPvpRating`, `int8 PvpRatingBracket` (`-1`), `int16 PvpRatingAssociatedSpecID`.
3. **Companion packets sent by `HandleCharEnum()` (`src/server/game/Handlers/CharacterHandler.cpp:556-674`)**:
   - `SMSG_BATTLE_PAY_GET_DISTRIBUTION_LIST_RESPONSE` (`0x45021E`, wire `0x470220`).
   - `SMSG_REGIONWIDE_CHARACTER_RESTRICTIONS_DATA` (`0x450019`, wire `0x470019`): `uint32 count`, per character: 2 zero bits, `FlushBits()`, `ObjectGuid Guid`, `uint32(0)`, `uint32(10)`.
   - **`SMSG_RECENT_ALLY_DATA_RESPONSE`** (`0x450362`, wire `0x470364`, 9B): `uint32(0x08ECBD10), uint8(7), uint32(0)` — **required** before the Classic 1.60 client renders the character list (`src/server/game/Handlers/CharacterHandler.cpp:579-589`).
   - `SMSG_ACCOUNT_ITEM_COLLECTION_DATA` (`0x450360`, wire `0x470362`, 4 zero bytes stub, `src/server/game/Server/Protocol/ClassicOpcodes.cpp:78`).
   - `SMSG_REGIONWIDE_CHARACTER_MAIL_DATA` (`0x45001A`, wire `0x47001A`, sent 3000 ms later by `WorldSession::Update`, `src/server/game/Handlers/CharacterHandler.cpp:642-670`).

---

## 6. Entering the World

*Verified against the code and a live server in round 2 (`WORLD-R1-NOTES.md`).*

1. **`CMSG_PLAYER_LOGIN`** (`0x430016`, wire `0x440016`): Packed `ObjectGuid Guid`, `float FarClip`, `Bits<1>(RPE)` (`src/server/game/Server/Packets/CharacterPackets.cpp:742-747`).
2. **`SMSG_CONNECT_TO`** (`0x4C0008`, wire `0x4E0008`, 45B on the live server and in the capture): `Size<uint32>(Payload)`, `uint32 Serial`, `uint8 Con` (`1` = `CONNECTION_TYPE_INSTANCE`), `uint64 Key` (`_instanceConnectKey.Raw`), `uint32 NativeRealmAddress`, `uint32 Key3`, then per address `uint8 Type` (`1` = IPv4), the 4 address bytes, `uint16 Port`, and a bleep token (35 bits + flushed byte + a nanosecond duration; empty here) (`src/server/game/Server/WorldSession.cpp:912-957`, `src/server/game/Server/Packets/AuthenticationPackets.cpp:282-334`). The `Signature[256]`/`WhereBuffer` fields this section used to list belong to a later build.
3. **Second TCP socket (`CONNECTION_TYPE_INSTANCE` on port `8085`)**: The Realm socket stays open while the client opens a second TCP connection to port `8085`, exchanges the 53-byte V2 banners, and receives a new `SMSG_AUTH_CHALLENGE` (`_serverChallenge_instance`) (`src/server/game/Server/WorldSocket.cpp:181-288`).
4. **`CMSG_AUTH_CONTINUED_SESSION`** (`0x440003`, wire `0x460003`, plaintext, 80B on Instance socket) (`src/server/game/Server/Packets/AuthenticationPackets.cpp:95-100`, `src/server/game/Server/WorldSocket.cpp:983-1064`):
   - Fields, in the order `AuthContinuedSession::Read` reads them: `uint64 DosResponse` (8B), `uint8 LocalChallenge[32]`, `uint8 Digest[24]`, `uint64 Key` (8B), `uint32 NativeRealmAddress`, `uint32 Key3` (80B total; `AuthenticationPackets.cpp:336-344`). The earlier order in this document (Key before the challenge) happens to be 80 bytes too but does not parse.
   - **Mandatory `Digest` check**: `First24Bytes(HMAC_SHA512(key = _sessionKey (40B), data = uint64_LE(Key) || LocalChallenge (32B) || _serverChallenge_instance (32B) || ContinuedSessionSeed (32B)))`, where `ContinuedSessionSeed` is `{ 0x56, 0x5C, 0x61, 0x9C, 0x48, 0x3A, 0x52, 0x1F, 0x61, 0x5D, 0x05, 0x49, 0xB2, 0x9A, 0x39, 0xBF, 0x4B, 0x97, 0xB0, 0x1B, 0xF9, 0x6C, 0xDE, 0xD6, 0x80, 0x1D, 0xAB, 0x26, 0x02, 0xA9, 0x9B, 0x9D }` (`src/server/game/Server/WorldSocket.cpp:61-62,1040-1047`); round 2 corrected the bytes this section used to give.
   - Instance `_encryptKey` is derived with `_sessionKey`, `LocalChallenge`, `_serverChallenge_instance`, and `EncryptionKeySeed`, followed by plaintext `SMSG_ENTER_ENCRYPTED_MODE` (`0x4E0004`, 0 ms delay) and plaintext `CMSG_ENTER_ENCRYPTED_MODE_ACK` (`0x460005`), which starts `AES-256-GCM` at `Counter = 2` and invokes `HandleContinuePlayerLogin()` (`src/server/game/Server/WorldSocket.cpp:1049-1063,1152-1160`).
5. **`SMSG_RESUME_COMMS` & `CMSG_QUEUED_MESSAGES_END`**: `HandleContinuePlayerLogin()` sends `SMSG_RESUME_COMMS` (`0x4C0006`, wire `0x4E0006`, 0B) **on the instance socket** (`ResumeComms(CONNECTION_TYPE_INSTANCE)`) and registers `SPECIAL_RESUME_COMMS_TIME_SYNC_COUNTER = 0xFFFFFFFE`; client replies with `CMSG_QUEUED_MESSAGES_END` (`0x44000A`, wire `0x46000A`: `int32 Timestamp`) (`src/server/game/Handlers/CharacterHandler.cpp:1389-1392`, `src/server/game/Handlers/MovementHandler.cpp:1033-1036`).
6. **World Login Burst (`HandlePlayerLogin`, `src/server/game/Handlers/CharacterHandler.cpp:1439-1595`)**:
   - Sends `SMSG_ACCOUNT_DATA_TIMES`, `SMSG_TUTORIAL_FLAGS`, difficulty packets, **`SMSG_LOGIN_VERIFY_WORLD`** (`0x45002F`, wire `0x47002F`: `int32 MapID`, `float X, Y, Z, O`, `uint32 Reason = 0`), `SMSG_FEATURE_SYSTEM_STATUS` (`0x450063`, wire `0x470063`), `SMSG_SET_TIME_ZONE_INFORMATION` (`0x450123`, wire `0x470125`).
   - `SendInitialPacketsBeforeAddToMap()` sends **`SMSG_TIME_SYNC_REQUEST`** (`0x5E0000`, wire `0x600000`: `uint32 SequenceIndex = 0`), spells/talents/actions/factions/currencies, `SMSG_LOGIN_SET_TIME_SPEED` (`0x4501B9`, wire `0x4701BA`), `SMSG_INITIAL_SETUP` (`0x450014`, wire `0x470014`), and **`SMSG_MOVE_SET_ACTIVE_MOVER`** (`0x5E0003`, wire `0x600003`: packed `ObjectGuid MoverGUID`) (`src/server/game/Entities/Player/Player.cpp:26419-26529`, `src/server/game/Entities/Unit/Unit.cpp:10887-10896`).
   - `Map::AddPlayerToMap()` -> `SendInitSelf()` sends **`SMSG_UPDATE_OBJECT`** (`0x5C0000`, wire `0x5E0000`) with `ThisIsYou = true` and registers `SPECIAL_INIT_ACTIVE_MOVER_TIME_SYNC_COUNTER = 0xFFFFFFFF` (`src/server/game/Maps/Map.cpp:392-429,1896-1909`).
   - `SendInitialPacketsAfterAddToMap()` sends `UpdateVisibilityForPlayer()` (`SMSG_UPDATE_OBJECT` for nearby entities), world states, auras, and `SMSG_PHASE_SHIFT_CHANGE` (`src/server/game/Entities/Player/Player.cpp:26621-26720`).
7. **Minimum Client Post-Login Packets**:
   - **`CMSG_LOADING_SCREEN_NOTIFY`** (`0x430024`, wire `0x440024`): `int32 MapID`, `Bits<1>(Showing = false)` — triggers Discord channel join, MOTD system messages, and achievement `64283` (`src/server/game/Handlers/CharacterHandler.cpp:1412-1437`).
   - **`CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE`** (`0x410064`, wire `0x420064`): `uint32 Ticks` (client ms) — completes `0xFFFFFFFF` time sync and calls `_player->UpdateObjectVisibility(false)` (`src/server/game/Handlers/MovementHandler.cpp:1038-1043`).
   - **`CMSG_TIME_SYNC_RESPONSE`** (`0x41005C`, wire `0x42005C`): `uint32 SequenceIndex`, `uint32 ClientTime` (`src/server/game/Server/Packets/MiscPackets.cpp:181-185`). Every movement packet (`CMSG_MOVE_HEARTBEAT` wire `0x42002E`, `CMSG_MOVE_START_FORWARD` wire `0x420000`, etc.) calls `AdjustClientMovementTime(movementInfo.time)`, which requires a non-zero `_timeSyncClockDelta` computed from time sync responses (`src/server/game/Handlers/MovementHandler.cpp:169-179,558,1000-1043`).

---

## 7. Keep-Alive and Disconnect

*Verified against the code and a live server in round 2 (the two logout packets travel on different sockets).*

1. **BNet RPC (Port 1119)**: `HandshakeTimeout` is `30s` and `LoginTimeout` is `600s` (cancelled once `_authed = true`); there is no idle timeout after authentication (`src/server/bnetserver/Server/Session.cpp:139-140,273-294`). Client `ConnectionService::KeepAlive` (`0x65446991`, method `5`) sends `NoData` and receives no reply (`src/server/bnetserver/Services/ConnectionService.cpp:59-63`). Rate limit is `Network.RequestsPerSecond` (default `10/s`) (`src/server/bnetserver/Server/Session.cpp:141,395-404`).
2. **World Socket Ping & Time Sync (Port 8085)**:
   - `CMSG_PING` (`0x440006`, wire `0x460006`: `uint32 Serial`, `uint32 Latency`) -> `SMSG_PONG` (`0x4C0009`, wire `0x4E0009`: `uint32 Serial`). Sending `CMSG_PING` faster than once per `27s` more than `MaxOverspeedPings` (`2`) times kicks the client (`src/server/game/Server/WorldSocket.cpp:1079-1135`). The server does not kick an open socket if `CMSG_PING` is never sent (`src/server/game/Server/WorldSocket.cpp:264-276`).
   - `WorldSession::Update()` sends `SMSG_TIME_SYNC_REQUEST` (`0x5E0000`, wire `0x600000`) at `5s`, `5s`, then every `10s` (`src/server/game/Server/WorldSession.cpp:1901-1938`).
   - `Player::ResetIdleTimers()` marks the player AFK after `5` minutes (`300000` ms) of no input (`src/server/game/Entities/Player/Player.cpp:33605-33650`).
3. **Logout & Disconnect**:
   - `CMSG_LOGOUT_REQUEST` (`0x3E0072`, wire `0x3F0074`: `Bits<1>(IdleLogout)` or empty 0B) -> `SMSG_LOGOUT_RESPONSE` (`0x45012F`, wire `0x470130` for a 70707 client and `0x470131` for 70938: `int32 LogoutResult`, `Bits<1>(Instant)`, `FlushBits()`) -> `SMSG_LOGOUT_COMPLETE` (`0x450130`, wire `0x470131` for a 70707 client, `0x470132` for 70938: `OptionalInit(SwitchGameMode = false)`, `FlushBits()` = `0x00`). The response is an instance-only opcode and the completion a realm-only one (`Opcodes.cpp:1938-1939`), so the response goes to the instance socket and the completion to the realm socket, which closes the Instance socket after `0s` (instant) or `20s` while keeping the Realm socket open (`src/server/game/Handlers/MiscHandler.cpp:243-292`, `src/server/game/Server/WorldSession.cpp:365-380,683-695`).
   - `CMSG_LOGOUT_CANCEL` (`0x3E0073`, wire `0x3F0075`, 0B) -> `SMSG_LOGOUT_CANCEL_ACK` (`0x450131`, wire `0x470133`, 0B) (`src/server/game/Handlers/MiscHandler.cpp:294-316`).
   - `CMSG_LOG_DISCONNECT` (`0x440007`, wire `0x460007`: `uint32 Reason`) (`src/server/game/Server/WorldSocket.cpp:1137-1142`).

---

## 8. Single Ordered Checklist

*Not verified against the code in round 1.*

|#|Dir|Port|Opcode (`Opcodes.h`) / Service & Method|70938 Wire / Hash|Security|Summary|Source|
|---|---|---|---|---|---|---|---|
|1|C->S|`8081`|`GET /bnetserver/login/`|HTTP(S) `GET`|TLS/HTTP|Request login form metadata|`src/server/bnetserver/REST/LoginRESTService.cpp:216-225`|
|2|S->C|`8081`|`200 OK` (`FormInputs`)|HTTP(S) JSON|TLS/HTTP|`FormInputs` + `Set-Cookie: JSESSIONID`|`src/server/bnetserver/REST/LoginHttpSession.cpp:160-170`|
|3|C->S|`8081`|`POST /bnetserver/login/srp/`|HTTP(S) `POST`|TLS/HTTP|Send `account_name` + `Cookie: JSESSIONID`|`src/server/bnetserver/REST/LoginRESTService.cpp:689-744`|
|4|S->C|`8081`|`200 OK` (`SrpLoginChallenge`)|HTTP(S) JSON|TLS/HTTP|SRP `version`, `modulus`, `salt`, `public_B`|`src/server/bnetserver/REST/LoginRESTService.cpp:746-770`|
|5|C->S|`8081`|`POST /bnetserver/login/`|HTTP(S) `POST`|TLS/HTTP|Send `public_A`+`client_evidence_M1` (or `password`)|`src/server/bnetserver/REST/LoginRESTService.cpp:283-366`|
|6|S->C|`8081`|`200 OK` (`LoginResult`)|HTTP(S) JSON|TLS/HTTP|`DONE`, `login_ticket`, `server_evidence_M2`|`src/server/bnetserver/REST/LoginRESTService.cpp:453-470`|
|7|C->S|`1119`|`ConnectionService::Connect`|`0x65446991`/`1`|TLS|Open BNet RPC session|`src/server/bnetserver/Services/ConnectionService.cpp:32-57`|
|8|S->C|`1119`|`ConnectResponse`|`service_id=0xFE`|TLS|`server_id`, `server_time`, `ciid`|`src/server/bnetserver/Services/ConnectionService.cpp:34-54`|
|9|C->S|`1119`|`AuthenticationService::Logon` (V1/V2)|`0x0DECFC01`/`1` or `0xC02F8216`/`1`|TLS|Send `application_version=70938`+`login_ticket`|`src/server/bnetserver/Services/AuthenticationService.cpp:229-259,330-358`|
|10|S->C|`1119`|`NoData` + `OnLogonComplete`|`0xFE` + `0x71240E35`/`5` or `0x9DA8116B`/`1`|TLS|`account_id`, `game_account` (`WoW1`), 64B `session_key`|`src/server/bnetserver/Services/AuthenticationService.cpp:283-306,389-421`|
|11|C->S|`1119`|`GameUtilities` (`Command_RealmListTicketRequest_v1_b9`)|`0x3FC1274D`/`1` or `0x5DBB51C2`/`1`|TLS|Send `Param_Identity`+`Param_ClientInfo` (32B `secret`)|`src/server/bnetserver/Services/GameUtilitiesService.cpp:240-290`|
|12|S->C|`1119`|`ClientResponse` / `ProcessTaskResponse`|`service_id=0xFE`|TLS|`Param_RealmListTicket="AuthRealmListTicket\0"`|`src/server/bnetserver/Services/GameUtilitiesService.cpp:292-301`|
|13|C->S|`1119`|`GameUtilities::GetAllValuesForAttribute`|`0x3FC1274D`/`10` or `0x5DBB51C2`/`2`|TLS|Query `"Command_RealmListRequest_v1_b9"` for the sub-regions|`src/server/bnetserver/Services/GameUtilitiesService.cpp:132-151`|
|14|S->C|`1119`|`GetAllValuesForAttributeResponse`|`service_id=0xFE`|TLS|Sub-region `"70-1-70"`|`src/server/shared/Realm/RealmList.cpp:389-397`|
|15|C->S|`1119`|`GameUtilities` (`Command_RealmListRequest_v1_b9`)|`0x3FC1274D`/`1` or `0x5DBB51C2`/`1`|TLS|Request realm list for `"70-1-70"`|`src/server/bnetserver/Services/GameUtilitiesService.cpp:304-322`|
|16|S->C|`1119`|`ClientResponse` / `ProcessTaskResponse`|`service_id=0xFE`|TLS|`Param_RealmList` (`0x46010046`)+`Param_CharacterCountList`|`src/server/bnetserver/Services/GameUtilitiesService.cpp:323-333`|
|17|C->S|`1119`|`GameUtilities` (`Command_RealmJoinRequest_v1_b9`)|`0x3FC1274D`/`1` or `0x5DBB51C2`/`1`|TLS|Join `Param_RealmAddress=0x46010046`|`src/server/bnetserver/Services/GameUtilitiesService.cpp:336-355`|
|18|S->C|`1119`|`ClientResponse` / `ProcessTaskResponse`|`service_id=0xFE`|TLS|`Param_RealmJoinTicket`, `Param_ServerAddresses`, 32B `Param_JoinSecret`|`src/server/shared/Realm/RealmList.cpp:446-505`|
|19|S->C|`8085` (Realm)|`ServerConnectionInitialize`|53B ASCII banner|Plaintext|`"WORLD OF WARCRAFT CONNECTION - SERVER TO CLIENT - V2\n"`|`src/server/game/Server/WorldSocket.cpp:86,181-196`|
|20|C->S|`8085` (Realm)|`ClientConnectionInitialize`|53B ASCII banner|Plaintext|`"WORLD OF WARCRAFT CONNECTION - CLIENT TO SERVER - V2\n"`|`src/server/game/Server/WorldSocket.cpp:85,207-237`|
|21|S->C|`8085` (Realm)|`SMSG_AUTH_CHALLENGE` (`0x4C0000`)|`0x4E0000`|Plaintext (`#0`)|32B `DosChallenge`+32B `Challenge`+`uint8(1)`|`src/server/game/Server/WorldSocket.cpp:278-288`|
|22|C->S|`8085` (Realm)|`CMSG_AUTH_SESSION` (`0x440001`)|`0x460001`|Plaintext (`#0`)|`RealmID=70`, `LocalChallenge`, `Digest`, `RealmJoinTicket`|`src/server/game/Server/Packets/AuthenticationPackets.cpp:76-93`|
|23|S->C|`8085` (Realm)|`SMSG_ENTER_ENCRYPTED_MODE` (`0x4C0004`)|`0x4E0004`|Plaintext (`#1`)|`RegionGroup=8`, 64B Ed25519 sig, `Enabled=true`|`src/server/game/Server/WorldSocket.cpp:952-981`|
|24|C->S|`8085` (Realm)|`CMSG_ENTER_ENCRYPTED_MODE_ACK` (`0x440005`)|`0x460005`|Plaintext (`#1`)|Empty; server calls `_authCrypt.Init(_encryptKey)`|`src/server/game/Server/WorldSocket.cpp:1152-1163`|
|25|S->C|`8085` (Realm)|`SMSG_AUTH_RESPONSE` (`0x450001`)|`0x470001`|`AES-256-GCM` (`#2`)|`Result=AUTH_OK (0)`, `SuccessInfo`|`src/server/game/Handlers/AuthHandler.cpp:24-50`|
|26|S->C|`8085` (Realm)|`SMSG_FEATURE_SYSTEM_STATUS_GLUE_SCREEN` (`0x450064`)|`0x470064`|`AES-256-GCM`|Classic 1.60 88B glue feature status|`src/server/game/Server/Packets/SystemPackets.cpp:247-285`|
|27|S->C|`8085` (Realm)|`SMSG_CACHE_VERSION` (`0x49000E`)|`0x4B000E`|`AES-256-GCM`|`uint32 CacheVersion`|`src/server/game/Handlers/AuthHandler.cpp:94-101`|
|28|S->C|`8085` (Realm)|`SMSG_TUTORIAL_FLAGS` (`0x450268`)|`0x47026A`|`AES-256-GCM`|`uint32 TutorialData[8]`|`src/server/game/Handlers/AuthHandler.cpp:103-109`|
|29|S->C|`8085` (Realm)|`SMSG_ACCOUNT_DATA_TIMES` (`0x4501B6`)|`0x4701B7`|`AES-256-GCM`|Global account data timestamps (`ObjectGuid::Empty`)|`src/server/game/Server/WorldSession.cpp:1553`|
|30|S->C|`8085` (Realm)|`SMSG_AVAILABLE_HOTFIXES` (`0x490001`)|`0x4B0001`|`AES-256-GCM`|Available DB2 hotfix IDs|`src/server/game/Handlers/HotfixHandler.cpp:59-73`|
|31|S->C|`8085` (Realm)|`SMSG_BATTLE_NET_CONNECTION_STATUS` (`0x4502B1`)|`0x4702B3`|`AES-256-GCM`|`State=1`, `SuppressNotification=false` (`0x40`)|`src/server/game/Server/WorldSession.cpp:1556`|
|32|C->S|`8085` (Realm)|`CMSG_ENUM_CHARACTERS` (`0x430014`)|`0x440014`|`AES-256-GCM`|Request character list|`src/server/game/Handlers/CharacterHandler.cpp:676-694`|
|33|S->C|`8085` (Realm)|`SMSG_ENUM_CHARACTERS_RESULT` (`0x450018`)|`0x470018`|`AES-256-GCM`|`Success=true`, `Realmless=true`, `RegionwideCharacters`|`src/server/game/Handlers/CharacterHandler.cpp:412-552`|
|34|S->C|`8085` (Realm)|`SMSG_BATTLE_PAY_GET_DISTRIBUTION_LIST_RESPONSE` (`0x45021E`)|`0x470220`|`AES-256-GCM`|Character boost/transfer distribution tokens|`src/server/game/Handlers/CharacterHandler.cpp:556`|
|35|S->C|`8085` (Realm)|`SMSG_REGIONWIDE_CHARACTER_RESTRICTIONS_DATA` (`0x450019`)|`0x470019`|`AES-256-GCM`|Per-character restriction flags|`src/server/game/Handlers/CharacterHandler.cpp:563-577`|
|36|S->C|`8085` (Realm)|`SMSG_RECENT_ALLY_DATA_RESPONSE` (`0x450362`)|`0x470364`|`AES-256-GCM`|`0x08ECBD10, 7, 0` — releases character select list|`src/server/game/Handlers/CharacterHandler.cpp:579-589`|
|37|S->C|`8085` (Realm)|`SMSG_ACCOUNT_ITEM_COLLECTION_DATA` (`0x450360`)|`0x470362`|`AES-256-GCM`|4B zero stub `{0,0,0,0}`|`src/server/game/Handlers/CharacterHandler.cpp:673`|
|38|C->S|`8085` (Realm)|`CMSG_PLAYER_LOGIN` (`0x430016`)|`0x440016`|`AES-256-GCM`|`ObjectGuid Guid`, `float FarClip`, `Bits<1>(RPE)`|`src/server/game/Handlers/CharacterHandler.cpp:1328-1372`|
|39|S->C|`8085` (Realm)|`SMSG_CONNECT_TO` (`0x4C0008`)|`0x4E0008`|`AES-256-GCM`|Directs client to open Instance socket on `8085` with `Key`|`src/server/game/Server/WorldSession.cpp:919-951`|
|40|S<->C|`8085` (Inst)|`ServerConnectionInitialize`/`ClientConnectionInitialize`|53B ASCII banners|Plaintext|V2 connection banners on Instance socket|`src/server/game/Server/WorldSocket.cpp:181-237`|
|41|S->C|`8085` (Inst)|`SMSG_AUTH_CHALLENGE` (`0x4C0000`)|`0x4E0000`|Plaintext (`#0`)|Fresh 32B `_serverChallenge` on Instance socket|`src/server/game/Server/WorldSocket.cpp:278-288`|
|42|C->S|`8085` (Inst)|`CMSG_AUTH_CONTINUED_SESSION` (`0x440003`)|`0x460003`|Plaintext (`#0`)|`DosResponse`, `Key`, `LocalChallenge`, 24B `Digest`|`src/server/game/Server/WorldSocket.cpp:983-1064`|
|43|S->C|`8085` (Inst)|`SMSG_ENTER_ENCRYPTED_MODE` (`0x4C0004`)|`0x4E0004`|Plaintext (`#1`)|`RegionGroup=8`, 64B Ed25519 sig, `Enabled=true`|`src/server/game/Server/WorldSocket.cpp:952-981,1063`|
|44|C->S|`8085` (Inst)|`CMSG_ENTER_ENCRYPTED_MODE_ACK` (`0x440005`)|`0x460005`|Plaintext (`#1`)|Enables `AES-256-GCM` on Instance socket; starts world login|`src/server/game/Server/WorldSocket.cpp:1152-1163`|
|45|S->C|`8085` (Realm)|`SMSG_RESUME_COMMS` (`0x4C0006`)|`0x4E0006`|`AES-256-GCM`|Registers `SPECIAL_RESUME_COMMS_TIME_SYNC_COUNTER` (`0xFFFFFFFE`)|`src/server/game/Handlers/CharacterHandler.cpp:1389-1392`|
|46|C->S|`8085` (Realm)|`CMSG_QUEUED_MESSAGES_END` (`0x44000A`)|`0x46000A`|`AES-256-GCM`|`int32 Timestamp` (completes `0xFFFFFFFE` time sync)|`src/server/game/Handlers/MovementHandler.cpp:1033-1036`|
|47|S->C|`8085` (Inst/Realm)|`SMSG_LOGIN_VERIFY_WORLD` (`0x45002F`)+Login Burst|`0x47002F`,`0x600000`,`0x600003`,`0x5E0000`|`AES-256-GCM`|`SMSG_LOGIN_VERIFY_WORLD`, `SMSG_TIME_SYNC_REQUEST`, `SMSG_MOVE_SET_ACTIVE_MOVER`, `SMSG_UPDATE_OBJECT` (`ThisIsYou`)|`src/server/game/Handlers/CharacterHandler.cpp:1465-1595`|
|48|C->S|`8085` (Realm)|`CMSG_LOADING_SCREEN_NOTIFY` (`0x430024`)|`0x440024`|`AES-256-GCM`|`int32 MapID`, `Bits<1>(Showing=false)`|`src/server/game/Handlers/CharacterHandler.cpp:1412-1437`|
|49|C->S|`8085` (Inst)|`CMSG_MOVE_INIT_ACTIVE_MOVER_COMPLETE` (`0x410064`)|`0x420064`|`AES-256-GCM`|`uint32 Ticks` — completes `0xFFFFFFFF` time sync & visibility|`src/server/game/Handlers/MovementHandler.cpp:1038-1043`|
|50|C->S|`8085` (Inst)|`CMSG_TIME_SYNC_RESPONSE` (`0x41005C`)|`0x42005C`|`AES-256-GCM`|`uint32 SequenceIndex`, `uint32 ClientTime`|`src/server/game/Handlers/MovementHandler.cpp:1028-1031`|
|51|C<->S|`8085` (Realm)|`CMSG_PING` (`0x440006`)/`SMSG_PONG` (`0x4C0009`)|`0x460006`/`0x4E0009`|`AES-256-GCM`|Keep-alive ping/pong (`>=27s` interval)|`src/server/game/Server/WorldSocket.cpp:1079-1135`|
|52|C<->S|`8085` (Inst/Realm)|`CMSG_LOGOUT_REQUEST`->`SMSG_LOGOUT_RESPONSE`->`SMSG_LOGOUT_COMPLETE`|`0x3F0074`->`0x470131`->`0x470132`|`AES-256-GCM`|Logs player out of world and closes Instance socket|`src/server/game/Handlers/MiscHandler.cpp:243-292`, `src/server/game/Server/WorldSession.cpp:683-695`|

---

## 9. Unknown from This Tree

*Not verified against the code in round 1.*

1. **Database rows in `auth.build_info` and `auth.build_auth_key` (`NOT IN THIS TREE`)**:
   - `ClientBuild::LoadBuildInfo()` loads `win64_auth_seed`, `mac64_auth_seed`, and `auth.build_auth_key` from the external `auth` MySQL database (`src/server/shared/Realm/ClientBuildInfo.cpp:74-149`), and `doc/ForeverSetup.md:18-24` states the SQL dump (`TDB_full_forever_2026_10_14.7z`) is distributed outside git. Querying `auth.build_info` and `auth.build_auth_key` in that SQL dump would answer whether a 16-byte `build_auth_key` exists for build `70938` when `Network.SkipBuildAuthKeyCheck = 0`.
2. **Client-side TLS certificate verification rules (`NOT IN THIS TREE`)**:
   - `bnetserver` serves TLS on ports `8081` and `1119` (`src/server/bnetserver/Server/SslContext.cpp:41-94`), and `doc/ForeverSetup.md:42` notes that `LoginREST.ExternalAddress` (`"trinity.actual.battle.net"`) must match the certificate hostname, but what root CA or pinning the `WoW.exe` 70938 client enforces is inside the client binary / launcher patcher.
3. **Client-side Proof-of-Work (`DosResponse`) algorithm (`NOT IN THIS TREE`)**:
   - `WorldSocket::SendAuthChallenge()` sends `DosChallenge` (32B) and `DosZeroBits = 1` (`src/server/game/Server/WorldSocket.cpp:278-288`), and `CMSG_AUTH_SESSION` / `CMSG_AUTH_CONTINUED_SESSION` read `uint64 DosResponse` (`src/server/game/Server/Packets/AuthenticationPackets.cpp:78,97`), but the server never checks `DosResponse` (`src/server/game/Server/WorldSocket.cpp:686-1064`). Reverse-engineering the `WoW.exe` 70938 `SMSG_AUTH_CHALLENGE` handler would answer how the client computes `DosResponse`.
4. **Exact semantics of unmapped bits in Classic 1.60 character & glue packets (`NOT IN THIS TREE`)**:
   - The server skips 3 unknown bits in `CMSG_CHECK_CHARACTER_NAME_AVAILABILITY`, `CMSG_CHAR_CUSTOMIZE`, and `CMSG_CHAR_RACE_OR_FACTION_CHANGE`, 2 unknown bits and an `int32` in `CMSG_CREATE_CHARACTER`, and 5 zero flag bytes in `SMSG_FEATURE_SYSTEM_STATUS_GLUE_SCREEN` (`src/server/game/Server/Packets/CharacterPackets.cpp:476,503,510,588,641`, `src/server/game/Server/Packets/SystemPackets.cpp:249-260`). Disassembling the 70938 client's JamClient readers would identify those fields.
