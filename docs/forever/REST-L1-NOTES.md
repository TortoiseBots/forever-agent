# REST-L1 notes: the typed Forever serve client

Scope: `internal/forever/serveclient`, the typed Go client of a `forever
serve` session daemon. L1 of the REST plan. Code: `serveclient.go`,
`serveclient_test.go`, the real-daemon tests in
`internal/forever/world/serveclient_daemon_test.go`, and the shared slot dir
reader `internal/slotdir`. The daemon keeps serving JSON (`forever rpc` is
the CLI client); `trials/live` keeps its own retrying map-based client and is
untouched.

## What it is

* `New(addr, tokenFile)` opens the daemon at host:port (scheme optional) with
  the bearer token from tokenFile. Only loopback addresses are accepted
  (127.0.0.0/8, `::1`, `localhost`, no lookup of other names): anything else is
  `ErrNotLoopback`, so the token never leaves the machine. No redirect is
  followed. An empty tokenFile means authentication off.
* `Open(dir)` opens a serve `--dir` through `internal/slotdir`, the code the
  `rpc` and `teardown` commands use too (not a copy): server, slot, holder
  and port present, slot.json's dir the one given, settings/pid/binary/token
  paths derived inside the dir, no symlinks. The token is read only from
  `<dir>/rpc-<port>.token`; a `token_file` pointing elsewhere is refused. A
  dir without that file is a `forever serve --no-auth` daemon (it records the
  path but never writes the file): the client sends no header, and a daemon
  that wants a token answers `ErrUnauthorized` (HTTP 401).
* Methods the daemon on main serves today, decoded exactly: `Status`,
  `Units`, `Unit`, `Party`, `GM`, `Bot`, `Events` (the combat log, the
  daemon's `CombatRow` and `ServeEventsPage`), `Logout`.
* **Needs W2/W3** (the spec names them, the daemon on main does not serve
  them): `EventLog` (`event_log`) and `Act` (`act`) are not registered and
  answer -32601; `Teardown` (`teardown`) and `Commander` (`commander`) are
  registered as not-ported and answer -32003. Their shapes are the spec's and
  are tested against a scripted fake only (`TestSpecShapesDecode`).
  `TestServeClientMethodPin` pins the daemon's table
  (`world.ServeRPCMethods`) and these answers: the day W2/W3 lands it fails
  and these notes flip.
* `GUID` is the serve guid text. `Key()` delegates to `runmap.GUIDKey`.
  Null/`""` decode as empty, a bare number as the old daemon's uint64 low half.
* Errors: every daemon error is `*Error` with its code (`CodeOf`).
  `-32005` (reconnecting) is retried every 2 s for up to 60 s and then
  surfaces as an `*Error` that is `errors.Is(ErrReconnecting)` and keeps
  -32005; `-32002` surfaces at once as an `*Error` that is
  `errors.Is(ErrDisconnected)` and keeps -32002. 401 is `ErrUnauthorized`.
* Replies are checked: `jsonrpc` "2.0", id equal to the request's, exactly one
  of result and error (an error with a null id is the daemon's own
  parse/invalid-request answer and keeps its code). A request over the
  daemon's 64 KB body cap is `ErrRequestTooLarge`, refused before it is sent.
  One `http.Client` per `Client`; every reply is drained so the connection is
  reused.

## Tests

* Against the REAL daemon (`internal/forever/world`, the fake world server
  behind the real `rpc.Server`: `rpc.Typed` with `DisallowUnknownFields`, the
  64 KB cap, the registered table), over a loopback port through `Open`:
  `TestServeClientAgainstTheRealDaemon` (every served method, each reply
  also strictly decoded into the client type), `TestServeClientNoAuthDaemon`,
  `TestServeClientTypesMatchTheDaemon` (JSON keys and leaf kinds equal both
  ways, plus a populated round trip), `TestServeClientBodyCapMatchesTheDaemon`
  (at the cap passes, one byte over is refused client-side and is the
  daemon's Parse error), `TestServeClientMethodPin`.
* `serveclient_test.go`: transport and policy against small fakes or a bare
  `rpc.Server`: loopback-only `New`, redirect not followed, envelope checks,
  401, request cap, connection reuse, retry/budget/disconnect with codes,
  `Open` against a bare `rpc.Server` with no override after Open (the dir
  checks: missing holder/server/slot, another dir, token_file, settings,
  binary and pid outside, a symlinked token, no slot.json) and `--no-auth`
  dirs.
* `internal/slotdir/slotdir_test.go`: the shared reader and checker.
