# Roster dashboard client and watch, round DASH-1 notes

Scope: a typed client for the server's roster dashboard (login, the logged-in bots, the heartbeat) and
`forever-agent forever dash-bots`, which prints the roster once or watches it and reports four changes: a bot that
died, a bot whose state says moving but whose position does not change, a bot in combat with no group mate in it, and
a bot far from its group. The command also leaves the client and the rules in a package the dungeon watch can take
later. **There is no live run in this round**; the last section lists the live steps.

Code:

* `internal/tbdash/client.go` — the shared HTTP session: `PasswordSource` (a private file or an environment
  variable), `NewWithSecret`, `SetTimeout`. `New(base, user, passFile)` and every method the raid tools use are
  unchanged in behaviour.
* `internal/roster/client.go` — the typed models (`Bot`, `Roster`, `Heartbeat`, `Text`), `DecodeRoster`,
  `Client` (login and `Roster`), `RosterSource`.
* `internal/roster/watch.go` — `Config` and its defaults, `Tracker`, `Event`, `Monitor`, `Summary`.
* `cmd/forever-agent/forever_dash_bots.go` — the command and its output.
* `cmd/forever-agent/forever_login.go` — the `forever` dispatch and usage line.

## What was reused

| Piece | From | How this round uses it |
|---|---|---|
| `POST /api/v1/auth/login`, the cookie jar | `tbdash.New` | The roster client is built with the same `tbdash.Client`, so the session cookie, the redirect policy (401 and 302 both come back) and the one re-login on 401 are the same code. |
| `GetJSON` and `HTTPError` | `tbdash` | `Roster` is one `GetJSON(ctx, "/api/v1/bots", nil)`; a non-200 is the same `*HTTPError`, which carries no response body. |
| Password file rules | `tbdash` / `privfile` | A file source keeps `privfile.Read`: regular file, owner-only mode, no symlink, one trailing newline dropped. |
| Password scrubbing and zeroing | `tbdash` | `Login` still wraps errors with `scrub` and zeroes the credential buffer after the request. |
| `safeName` | `cmd/forever-agent` | Every dashboard string the command prints (names, zone, instance, class, target, group, state ratios) goes through it: bytes outside `0x20-0x7E` become `?` and the value is cut at 64 bytes, so a name cannot carry a terminal escape. |

The one change to the old client is additive: the password no longer has to be a file. `New` now calls
`NewWithSecret(base, user, PasswordFile(path))`; `SetTimeout` bounds the requests a caller wants bounded (the roster
client sets it, the raid tools do not).

## The API as the client decodes it

`GET /api/v1/bots` may answer with a bare array of bots or with an object holding `bots` and, optionally,
`heartbeat`; both are accepted. The object form is the one the heartbeat needs:

```json
{"bots":[ ... ], "heartbeat":{"bots":4,"humans":2,"states":{"idle":0.5,"moving":0.25,"combat":0.25,"dead":0}}}
```

Every field named in the round's API is modelled. `role` and `strategy` decode to `""` when absent or null; `XP` is
not modelled at all. Unknown fields are ignored at every level, so a later field costs nothing. The decoder is
strict about types and about two closed sets:

* a `level`, `hp`, `level`-like field that arrives as a string is an error; a bot without a name or guid is an
  error; `kind` must be `pool` or `hired`; `state` must be `idle`, `moving`, `combat` or `dead`.
* identifier-like fields (`guid`, `class`, `power_type`, `map`'s companions `zone` and `instance`, `target`,
  `group`, `kind`, `state`, `role`, `strategy`) accept a string, a number or null. This is the only type tolerance:
  the port may spell a class or an instance as a name or an id, and a number keeps its decimal text. An object,
  array or bool in one of those fields is still an error.
* a `bots` key that is missing or null, an empty body and a non-JSON body are errors; `[]` and `{"bots":[]}` are a
  valid empty roster.

The heartbeat is assumed to travel in the bots answer. If the port moves it to its own route, only `Client.Roster`
and the `Roster` type change; the watch rules do not read it.

## The command

```
forever-agent forever dash-bots --url URL --user NAME [--pass-file FILE] [--watch [--interval 2s]]
    [--stuck-after 20s] [--lone-combat-after 5s] [--group-distance 60] [--timeout 15s]
```

* The password is read from `--pass-file` (mode 0600, no symlink) or from `FOREVER_DASH_PASSWORD`; there is no flag
  for the value. Without either, the command prints the usage and exits 2. The password reaches no error text and
  no line of output.
* `--user` is the GM game account. It is not a secret and is not printed.
* One bounded request: `--timeout` applies to the login and to every read (1s to 2m, default 15s). The command's
  context (Ctrl-C, SIGTERM) also ends a request and a wait.
* Without `--watch`: login, one read, the roster, exit 0. The first line is the heartbeat when the dashboard sends
  one (`roster: N bot(s), M human(s); states ...`), then one line per bot, sorted by name:
  `name  class level  kind  state  hp A/B  powertype A/B  map Z zone instance  (x, y, z)  target  group`.
* With `--watch`: the first successful read is the baseline and prints the roster; later reads print only events:
  `HH:MM:SS  death  NAME died  (map ...)`, `stuck`, `lone-combat`, `far-from-group`. A failed read is printed to
  stderr and the next interval retries; five consecutive failures end the watch with exit 1. Ctrl-C exits 0.
* Exit codes: 0 printed or watched, 1 login/read failed or the watch gave up, 2 usage or a refused value.

## Detection rules and defaults

The tracker keeps one small record per bot guid. A rule fires once per condition and re-arms only after the
condition clears, so a watch does not repeat a line every interval.

| Rule | Event | Fires when | Default | Re-arm |
|---|---|---|---|---|
| Death | `death` | a bot's state becomes `dead`; the baseline sample never counts (a bot already dead is in the printed roster) | — | the bot's state is not `dead` again |
| Stuck | `stuck` | state is `moving` and the position stays within 0.5 yd of the previous sample for at least the threshold | 20 s | the position moves more than 0.5 yd or the state stops being `moving` |
| Lone fight | `lone-combat` | state is `combat` and its `group` has at least two bots in the roster but this is the only one in combat, for at least the grace | 5 s | another bot of the group is in combat, or this bot is not |
| Far from group | `far-from-group` | the distance to the centre of its group mates (same map and same instance) is over the threshold | 60 yd | the distance falls to 80% of the threshold |

Details that the thresholds imply:

* The stuck clock starts at the first sample that shows `moving`; with a 2 s interval a bot that stops right after a
  sample is reported after the threshold plus at most one interval.
* `MoveEpsilon` and the re-arm share are not flags; they are `DefaultMoveEpsilon` (0.5 yd) and 0.8. The four flags
  above are the operator's thresholds; the command rejects zero, negative and absurd values.
* A `group` with one bot in the roster is **not** judged for a lone fight: the roster lists bots only, a human may
  be in that group, so there is no evidence either way. A bot with no group is skipped for the same reason.
* The distance uses the centre of the group mates on the same map **and** the same instance; a mate elsewhere is no
  reference point. This keeps instance entry and exit from firing the flag. The distance is 3D.
* Events carry the guid as well as the name, so a report keyed by guid survives a rename.

## For the dungeon watch

The branch that adds the dungeon watch is not merged and was not touched. It gets a small port and two functions:

```go
// internal/roster
type RosterSource interface {
    Roster(ctx context.Context) (*Roster, error)
}
func NewMonitor(src RosterSource, cfg Config) *Monitor
func (m *Monitor) Poll(ctx context.Context, at time.Time) ([]Event, error)
```

The watch builds `roster.New(url, user, src, timeout)` with the same secret rules as `dash-bots` (a file or
`FOREVER_DASH_PASSWORD`; the watch's `--dash-url` names the dashboard), wraps it in a `Monitor` with the thresholds
it wants, and calls `Poll` on its own tick. `Poll` returns only the events since the previous tick and keeps the
per-bot state; the watch feeds each event to `Summary.Add` (or `Summary.EventsOf`) and the report card prints:

* **deaths per bot**: `Summary.Deaths[guid]`, counted from `death` events; a bot dead at the baseline is not a death
  the watch saw, so the card can say "no deaths observed" while the roster shows a dead bot.
* **stuck bots**: `Summary.Stuck[guid]` and the event's `For` and position; a bot that was stuck twice counts twice.
* **distance**: `Summary.Farthest[guid]`, the largest distance from the group's centre seen for that bot; a
  `far-from-group` event is the line to show with it.
* **lone fights**: `Summary.LoneCombat[guid]`, the count of fights started alone.

The watch owns the login and the loop; `Monitor` owns no clock and no I/O beyond `RosterSource.Roster`. That keeps
the watch's fake source (a script of rosters) independent of the HTTP client.

## Tests

`internal/roster/client_test.go` (an `httptest` fake):

* login with the cookie, the typed answer, unknown fields, numeric identifiers and absent `role`/`strategy`;
* the bare-array answer and no heartbeat;
* one re-login on a 401 and the retry (one login, two reads);
* the contract breaks: a string `level`, a numeric name, a bad `guid`, a missing name/guid/kind, an unknown kind or
  state, a missing/null `bots` list, an empty and a non-JSON body;
* the timeout: a server that never answers is cut at the client timeout, and the error names it;
* the environment source (the login sends its value; empty names the variable, never a value);
* a 0644 password file is refused before any request and the error does not carry the password;
* a 500 is a `*tbdash.HTTPError`.

`internal/roster/watch_test.go`:

* a death is reported on the transition only, and again after a resurrection; a dead baseline is not reported;
* stuck fires at the threshold, once, and again after a move; a 0.2 yd jitter counts as standing still; an idle bot
  never fires;
* a lone fight waits out its grace, fires once, re-arms when a mate joins;
* a group with no bot mate is skipped;
* far-from-group fires once, stays quiet between the threshold and the re-arm line, and fires again after going
  back; the centre is used; another instance is no reference;
* a bot that left is forgotten;
* the monitor polls a scripted source and the summary counts a death;
* a nil monitor and a failing source are errors.

`cmd/forever-agent/forever_dash_bots_test.go`:

* missing flags and a missing password are usage, not a request;
* a live-shaped fake: the roster and heartbeat print, a terminal escape in a name is neutralised, the password
  reaches no output;
* a watch over a fake that goes from idle to dead prints the death and exits 0 on cancel;
* a 0644 file exits 1 with the mode in the error and no password.

## Live steps (not run this round)

1. Pick a server whose dashboard is up and a GM game account that may read it. Start the agent in the world with a
   handful of bots logged in.
2. Put the password in a file with mode 0600 (or export `FOREVER_DASH_PASSWORD`) and run
   `forever-agent forever dash-bots --url URL --user NAME --pass-file FILE`. Check that every bot in the world is
   listed once, that hp/power/map/position match the world, and that the heartbeat's bot and human counts add up.
3. Run it with `--watch --interval 2s`. Kill one bot (in game) and look for one `death` line. Hold a bot that is in
   `moving` state still for 25 s: one `stuck` line at about 20 s, none after. Start a fight with one bot of a group
   and keep the others out: one `lone-combat` line after about 5 s. Send one bot more than 60 yd from its group:
   one `far-from-group` line with the distance; bring it back under 48 yd and send it away again for a second line.
4. Expire the session (restart the dashboard or clear its cookie) during a watch. The next read must re-login once
   and continue; a second 401 in the same read is an error line, not a loop.
5. Stop the dashboard during a watch: the read errors are printed; five in a row end the watch with exit 1, and
   Ctrl-C ends it with 0.
6. Record counts and times only. No bot names, accounts, hosts or addresses go into a report.

## Limits of this round

* No live run: the shapes above are the port's stated API and the tests' fake; the first live run may find a field
  spelled differently. A type change is a named error, not a silent zero; a renamed field costs one struct tag.
* The heartbeat in the bots answer is an assumption; see above.
* `lone-combat` and `far-from-group` can only judge bots the roster lists. A group with one bot is skipped rather
  than flagged.
* A bot that is dead at the baseline is not a death event; the roster's state is the only record of it.
