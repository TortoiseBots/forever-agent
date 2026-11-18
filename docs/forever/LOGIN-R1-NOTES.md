# Forever login, round 1 notes

Scope: REST login (SRP + login ticket) and the Battle.net RPC login up to and including the realm join answer
(join ticket, server addresses, 32-byte join secret). The world socket is not part of this round.

## What works

TBD (filled in as the client is built).

## Where `PROTOCOL-LOGIN.md` was wrong or silent

The corrections of `PROTOCOL-LOGIN-VERIFY.md` are applied to `PROTOCOL-LOGIN.md` in place. Beyond those, this round
found:

1. **Command attribute names need a trailing suffix.** `ParseParamName` removes everything after the last `_` of any
   name starting with `Command_` (`src/server/bnetserver/Services/GameUtilitiesService.cpp:36-46`), so the bare
   `Command_RealmListRequest_v1` becomes `Command_RealmListRequest` and matches nothing. A client must send e.g.
   `Command_RealmListRequest_v1_b9`; the document wrote `Command_RealmListRequest_v1*`, which hides this.
2. **There is no per-IP connection limit on the REST login port.** `Network.ConnectionsPerIp` is enforced by
   `Battlenet::Session` and `WorldSocket` only (`src/server/bnetserver/Server/Session.cpp:129-137`,
   `src/server/game/Server/WorldSocket.cpp:134-141`). The document claimed `LoginHttpSession::CheckAccess()`.
3. **`Set-Cookie` is always `Secure`**, also on a plain-HTTP listener (`src/server/bnetserver/REST/LoginHttpSession.cpp:60-72`),
   and it carries a `Domain` attribute taken from the request `Host`.
4. **Launching endpoints** return `{"token","expires"}`, `{"login_ticket","account_id","email","game_accounts"}` and
   `{"ok":true}`; there is no `RL-` prefix and no `remember_token`/`forgotten` field
   (`src/server/bnetserver/REST/LoginRESTService.cpp:526-624`).
5. **No build check in the realm list ticket request.** The handler only needs a 32-byte client secret and a known
   game account id; the build is checked later against the realm (`RealmAcceptsBuild`).
6. **`CompressJson` is zlib (RFC 1950), not raw deflate**, with a 4-byte little-endian length that counts the trailing
   NUL (`src/server/bnetserver/Services/GameUtilitiesService.cpp:48-60`).
7. **The login form's `srp_url` advertises the server's configured port**, not the port the client reached it on
   (form shows `:8081` through a tunnel to `:18081`), so a client must keep its own REST address and only take the path.

## How the SRP vectors were made

TBD.

## Open questions for the world-socket round

TBD.
