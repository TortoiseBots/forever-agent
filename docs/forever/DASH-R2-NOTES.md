# Roster dashboard, round DASH-2 notes (review fixes)

Scope: four review findings on the round-1 client and command. No live run in this round; the live steps in
[DASH-R1-NOTES.md](DASH-R1-NOTES.md) still apply, with the URL step below. The round-1 file stays as the round-1
record; the banner there points here for what changed.

## 1 (HIGH) A GM password over plain HTTP

The client no longer sends a password to a remote plain-http dashboard. `tbdash.NewWithOptions` (and therefore
`New`, `NewWithSecret` and `roster.New`) refuses `http://` unless the host is this machine:

* `localhost` (any case, a trailing dot allowed);
* a loopback IP literal: `::1`, or an IPv4 address in the loopback block (127.0.0.1 is the usual one);
* any other name whose **every** resolved address is loopback. The lookup has a 2 s bound; a name that cannot be
  resolved is refused, because loopback cannot be proven. `https://` is always accepted.

The boundary is explicit, never silent: `Options{AllowInsecureHTTP: true}` allows a remote plain-http URL, and the
commands that can set it print one warning line before connecting. The flag is `--dash-insecure-http`, off by
default, on the commands that take a dashboard URL:

| Command | Flag | Where it goes |
|---|---|---|
| `forever dash-bots` | `--dash-insecure-http` | `roster.Options.AllowInsecureHTTP` |
| `decisions pull` | `--dash-insecure-http` | `tbdash.Options.AllowInsecureHTTP` |
| `raid-walk` | `--dash-insecure-http` | `walkDeps.openDash(..., insecure)`; warning in `openDashboard` |
| `raid-suite` | `--dash-insecure-http` | forwarded to each `raid-walk` as the same flag |

The warning text: `warning: plain HTTP dashboard: the GM password and the session cookie travel in clear text`.
It is printed when the flag is set and the effective URL is `http://`, including loopback (a loopback URL did not
need the flag, but the flag still says what it does). A name that resolves to a public address, a TEST-NET literal
and an unresolvable name are all refused without the flag.

Tests (`internal/tbdash/review_test.go`): two non-loopback literals refused; 127.0.0.1, a second address in the same
loopback block, ::1 and localhost allowed; https allowed; a replaced resolver proves the resolved-address rule both
ways (loopback name allowed, public name refused, unresolvable name refused); the explicit option allowed and still
refusing a bad scheme. Command tests: `dash-bots` refuses a remote plain-http URL without the flag, and with the flag
prints the warning and reaches the login (exit 1 on an unreachable address).

## 2 (HIGH) Missing numeric fields became zeros

`DecodeRoster` now decodes each record into a wire struct whose watch-critical fields are presence-tracked
(pointers): a missing key and an explicit null are both distinguishable from a zero. The required fields are
`hp`, `max_hp`, `map`, `instance`, `x`, `y`, `z` and `state` (plus the identity fields `name`, `guid` and `kind`).
The optional fields stay optional: `level`, `power`, `max_power`, `o`, `class`, `zone`, `target`, `group`, `role`
and `strategy`; `XP` is still not modelled.

A record is also refused for an invalid value:

* `hp` or `max_hp` negative;
* `hp > max_hp`;
* `max_hp == 0` while the state is not `dead` (a corpse may have 0);
* a negative `map`;
* a non-finite `x`, `y` or `z` (a JSON number out of float64 range fails at decode);
* a value of the wrong JSON type (the type rules of round 1 still apply).

A refused record is **skipped, not fatal**: the answer's other records are kept, and the refusal is reported as a
`roster.Rejection` (index in the answer, name when readable, reason). `Roster.Rejected` carries them. The command
prints one line per new rejection on stderr (`bot record N NAME skipped: REASON`) and de-duplicates a standing
rejection in a watch. The watch rules only see `Roster.Bots`, so a record that loses a field disappears from the
tracker instead of being read as a death, a stuck bot or a distance; a bot that returns later starts a new baseline.
The answer as a whole is still an error only when it cannot be a roster (empty, non-JSON, neither an array nor an
object, no `bots` list).

Tests (`internal/roster/decode_test.go`): one sub-test per required field omitted (hp, max_hp, map, instance, x, y,
z, state), each expecting one rejection naming the field and no bot; the range cases and the dead-bot exception; a
non-finite position; one bad record beside two good ones; and a tracker test where a bot whose record turns bad next
to a real death produces only the real death. The round-1 client tests were updated to send the now-required fields.

## 3 (MEDIUM) Concurrent 401s logged in repeatedly

`tbdash.Client` has a session generation (`atomic.Uint64`), bumped after each successful login. `GetJSON` and
`DecisionFile` read the generation before their request; after a 401/302 they call `refreshSession(ctx, seen)`, which
takes the login lock, returns without a request when the generation has already moved (another caller refreshed),
and otherwise logs in and bumps it. Every caller still retries its request once; a retry that fails again returns
the `*HTTPError`, never a loop. A direct `Login` also bumps the generation, so it counts as a refresh for the readers
waiting on it.

Test (`internal/tbdash/review_test.go`, `TestConcurrent401sLogInOnce`): eight readers start together, the fake holds
their first reads until all eight arrived, rotates the cookie on every login and rejects any cookie that is not the
current one. The test asserts exactly one login and no failed read. On the round-1 code the same test counted eight
logins and five 401s.

## 4 (MEDIUM) Unbounded response size

`maxResponseBytes` is 8 MiB (`internal/tbdash/client.go`). `get` refuses early when `Content-Length` is over it, and
otherwise reads through `io.LimitReader(body, maxResponseBytes+1)` and fails when the extra byte is reached. The cap
applies to every JSON read, including the retry after a 401; the decision-file streaming path keeps its own caller
limit. Errors name the byte cap.

Tests: a 200 with an over-limit body and a declared Content-Length; a 401 whose retry sends an over-limit chunked
body (no Content-Length), asserting two reads and the limit error; the normal small answers in the existing tests.

## Compatibility

The old dashboard commands now follow the same boundary. A deployment whose dashboard is plain http on a remote
host must pass `--dash-insecure-http` to `decisions pull`, `raid-walk` or `raid-suite` (each prints the warning), or
put https in front of it. Round 1's `dash-bots` on loopback needs no flag.

## Tests run

`go test ./internal/tbdash ./internal/roster ./cmd/forever-agent` green, then the gates named in the round's brief
(`make fmt-check`, `make cross`, `vm-test ta`, `staticcheck ./...`, `go test -race ./...`).

## Live steps (unchanged, plus the URL rule)

The round-1 live steps apply. Before the first run: use an https dashboard URL, or loopback, or add
`--dash-insecure-http` to the command and accept the warning. Then check that a bot record with a deliberately
missing field is printed as `skipped` and does not appear as a death or a stuck line, and that a session expiry in
the middle of a watch logs in once and continues.
