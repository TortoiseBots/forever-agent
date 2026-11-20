# REST-L1 notes: the typed Forever serve client

Scope: `internal/forever/serveclient`, the typed Go client of a `forever
serve` session daemon. L1 of the REST plan. Code: `serveclient.go`,
`serveclient_test.go`. The daemon keeps serving JSON (`forever rpc` is the
CLI client); `trials/live` keeps its own retrying map-based client and is
untouched.

## What it is

* `New(addr, tokenFile)` opens the daemon at host:port (scheme optional)
  with the bearer token from tokenFile; `Open(dir)` opens a serve `--dir`
  (slot.json gives the port and the token file, both asserted the way the
  CLI client asserts them). An empty tokenFile means authentication off.
* Typed `Status`, `Units`, `Unit`, `Party`, `GM`, `Bot`, `EventLog`, `Act`,
  `Commander`, `Logout`, `Teardown` decode the serve JSON exactly: the
  structs match `ServeStatus`, `ServeUnit`, `ServePartyInfo`, `ServeCommand`,
  `ServeEventsPage`, `tbm.Reply` field for field, so serve and client can
  never drift in shape (a field renamed on one side breaks the decode
  test, not a silent zero).
* `GUID` is the serve guid text. `Key()` delegates to
  `runmap.GUIDKey`: one guid, one key on the client and on the run maps.
  Null/`""` decode as empty, a bare number as the old daemon's uint64 low
  half, any other unquoted JSON as an error.
* `-32005` (reconnecting) is retried every 2 s for up to 60 s and then
  surfaces as `ErrReconnecting`; `-32002` (disconnected) surfaces as
  `ErrDisconnected` at once, never retried. Every other error code
  surfaces as `*Error` with its code, so a caller can branch on what the
  retry policy does not own (not-ported `-32003`, unknown-method
  `-32601`); `CodeOf` unwraps it.
* `event_log` keeps the daemon's bounds (limit 0 or over 1000 is 1000,
  wait_ms clamped to 0..2000). `act` refuses an empty or over-600 s
  timeout before anything is sent, matching the daemon's own cap.
  `teardown` decodes the W2 serve shape (`{step, result}`) and the old
  daemon's (`{step, status, detail}`).
* `Commander` sends the old `commander {op:status|...}` shape (op, lead
  bot, bots, on, verb, args, wait_ms), so the method ports without a
  client change when W2/K2 add `teardown` and `event_log` to serve.

## Tests

`serveclient_test.go` drives a scripted JSON-RPC server (queued results
and error codes per method, every call logged). `TestGUIDKeyHighWordMatters`
is the round's failing case (two guids sharing the low 64 bits have
different keys; it fails on any low-half-only hash). `TestStatusStates`,
`TestUnitAurasDecode` (incl. null/empty/numeric guids), `TestOpenSlotDir`
(the dir checks, a foreign dir and a stray token file refused before send),
`TestPageAndActDecode` (event_log, act, party, command, commander, logout,
both teardown shapes), `TestReconnectingRetried` (retry-then-online,
budget spent, disconnected unretried).
