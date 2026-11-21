# REST L2 (scenario Forever mode) + L5 (dashboard live view over serve) — notes

Round: NEXT-REST-L2-L5. Packages: `internal/scenario`, `internal/dashboard` only.
Commits: L2 `68068ac8`, L5 `717347d2`, cleanup `a0147d71`, file-server fallback `7102707f` (L2 first).

## Prerequisites on origin/main (checked 2026-11-21)

- L1 present: `internal/forever/serveclient` with typed `Status`, `Units`,
  `Party`, `EventLog`, `Commander` (`CommanderParams`/`CommanderResult`,
  "the old daemon's shape, which serve keeps when it ports the method").
- W3's `event_log` JSON shape: code only (serveclient `Event`:
  "the internal/events Event JSON keys, guids as 0x and 32 hex digits").
  The daemon on main has no `event_log` yet (`serveclient_daemon_test.go`:
  "event_log and act need W3"); L2 decodes the shape, never the daemon.
- C3 MISSING: `commander` is in `notPortedMethods`
  (`internal/forever/world/serve_session_methods.go:1109`), answered
  `-32003 "not ported on this server"`. Per the brief ("if L1 or C3 is
  missing ESCALATE or skip L5 and say so"): L5 is built dashboard-side
  against fakes and the L1 shape (exactly what C3 will serve), with
  `TestForeverControlNotPortedShownAsReply` pinning the not-ported answer
  shown as the reply. The live button path needs C3 merged; the live
  checks below are for the lead's queue after C3.

## L2 — scenario Forever mode (`internal/scenario`)

- File key `"server": "turtle"|"forever"` on sessions (default turtle)
  and on `check_db` steps (overrides the slot for one step). Unknown
  values refused at load.
- Forever start: status state `"online"` is in the world (empty state
  still passes with a character); anything else is ERROR. Turtle probe
  unchanged.
- Forever `drain`/`fetch` call `event_log`, decoding a local
  `foreverEvent` (text guids) into `events.Event` with zero GUIDs.
- Forever `do` steps call serve methods by name: `gm` same text, `bot`
  the bare module line (`command`), `addon_send` same fields, `rpc`
  names the serve method. `not ported` is ERROR naming the method,
  never FAIL.
- `check_db` Forever allowlist: `characters`, `item_instance`,
  `character_inventory` (schema-qualified `characters.*`, core 25fc7ca:
  `item_instance(itemEntry, owner_guid)`, `character_inventory(guid,
  bag, slot, item)`, `characters(name, online, ...)`); item count uses
  `itemEntry`/`owner_guid`. Turtle SQL unchanged.

## L5 — dashboard live view over serve (`internal/dashboard`)

- `Agent.Kind`: `turtle` (default) or `forever`, from the settings file's
  `"server"` field (unknown values skipped, not fatal). Explicit
  `--agents` addrs stay turtle.
- Forever poll (`forever_poll.go`): `status`, `party`,
  `commander {op:status}`, `units {within:100}`, `event_log` tail through
  `serveclient` (fix round: `-32005` retried there, typed errors, token
  rules). No `tbm*`, `dungeon_status`, `tune_list` or `bot_view` call.
  `status` failure is an offline card, not an error; an unreadable token
  file is an offline card with that reason.
- `BuildForeverCard` fills the existing `Card` fields: self from status
  (`online` sets `Pos`), bots (role/hp/pos/order) from the commander
  status members, mobs from units, run state/plan/elapsed from the
  commander run record, stall from the time since the run last made
  progress (fix round; never elapsed). Guids stay text, keyed by
  `serveclient Key` (`runmap.GUIDKey`: high word matters); targets match
  by that key. The `event_log` tail fills `Card.Events` (capped at 100,
  deduplicated by seq).
- Controls (`forever_control.go`): order verbs
  stay/follow/come/attack/pull/release (buttons on the card, fix round)
  and run pause/resume/stop all call `commander {op:cmd|run}`. An
  answered call is 200 with the module's words; a reply the module marks
  not ok is `status:"error"`/`executed:false`, and a JSON-RPC error is a
  502 with its code (fix round; never `status:"ok"`). No `tbm*` call on
  a Forever agent; the turtle `run` handler (fresh `dungeon_status` live
  check, `dungeon_*` verbs) never runs for `Kind == forever`.
- Guides: `dashboard-guide.md` (Forever poll + settings server key),
  `run-controls-guide.md` (commander controls), `scenarios-guide.md`
  (session/check_db server keys, Forever drain/do-step behavior).

## Gates (final commit 7102707f, clean tree)

- `make fmt-check`: exit 0, no output (gofmt clean on all touched packages).
- `make cross`: exit 0 (`GOOS=windows GOARCH=amd64`, `GOOS=darwin GOARCH=arm64` builds plus `go vet ./cmd/... ./internal/forever/...`).
- `staticcheck ./...`: no findings (exit 0; three U1000 dead-code findings on the way were removed).
- `go test -race ./internal/scenario/ ./internal/dashboard/`: both `ok` (scenario 1.2 s, dashboard 21.4 s).
- `vm-test ta ~/wt-fa-rest2`: `RESULT: PASS  kind=ta wt=wt-fa-rest2 sha=7102707f dirty=0 files=2343`, `rc=0  vm_duration=2324s  laptop_wall=2338s`; log `~/TortoiseBots-audit/vm-test-logs/wt-fa-rest2-20261121-045241-157180-7b69.log`.
- Hygiene: `git diff origin/main | grep -nE '100\.|192\.168|forever-test|tortoise-test|TortoiseBots-audit|/home/|@gmail'` empty (0 matches).

## Live checks for the lead's queue (no live run this round)

1. Dashboard over a serve slot: settings with `"server":"forever"`;
   the card shows the Forever party (five members with role/hp/pos),
   mobs from units, run state from the commander run. (Needs C3: until
   then the poll's commander call answers not-ported and the card shows
   self + mobs only.)
2. One button works: `stay` on a bound party answers through
   `commander {op:cmd}` and the reply shows on the page. (Needs C3.)
3. Scenario sample (K3 follow-up): two serve slots, `server:forever`
   file with say + party invite, `check` on `chat` and `party` kinds —
   PASS twice.

## Fix round (FIX-REST-L2-L5, 2026-11-21)

Reviews: `claude-rest-l2-l5.md` (12 findings) and `codex-rest-l2-l5.txt`
(3 findings). All fixed in one round; each fix has a test that would fail on
the old code.

| # | Finding | Fix | Test |
| --- | --- | --- | --- |
| 1 | Stall = total elapsed time (HIGH) | The poller tracks the run's progress key (step, leg, kind, phase, note, deaths/wipes, self position) per agent; the card's stall is the time since that key last changed. No progress known yet = no stall. | `TestForeverStallIsTimeSinceProgress` (healthy 10-minute run none, stuck 6 minutes red, unknown none), `TestForeverProgressMarkTracksChanges` |
| codex | Event log fetched and discarded | The tail (since the last seq shown, limit 100) fills `Card.Events`; deduplicated by seq and bounded to 100 rows. | `TestForeverEventTailOnCard` (shown once, repeated tail not doubled, 150 rows capped to 51..150) |
| codex | Rejected controls returned as `status:"ok"` | A JSON-RPC error is 502 `status:"error"` with code and scrubbed message; a decoded reply with `ok:false` is 200 `status:"error"`, `executed:false`, the module's words. | `TestForeverControlRunActiveShownAsReply`, `TestForeverControlNotPortedIsError` |
| 2 | Start probe ERRORed on `connecting`/`reconnecting` | Waits up to 60 s (never past `max_minutes`), polling status every 1 s; bounded timeout ERRORs naming the last state. | `TestForeverStartWaitsForOnline`, `TestForeverStartWaitIsBounded` |
| 3 | `-32005`/`-32002` flattened or swallowed | `callForever` retries `-32005` every 2 s for up to 60 s; `-32002` always ERRORs. `ignore_error` covers only `-32003` (not a not-ported method) and `-32006`; `-32601`/not-ported always ERROR. | `TestForeverReconnectingIsRetried`, `TestForeverDisconnectedNeverIgnored`, `TestForeverGameRefusalMayBeIgnored`, `TestForeverNotPortedNotIgnored` |
| 4 | `more:true` with a stuck cursor looped | `sessState.advanceForever` returns a typed `*foreverCursorError` when `more` and the cursor did not advance. | `TestForeverMoreWithoutAdvanceIsError` (one page, then the error) |
| 5 | Cursor reset on `next:0` replayed old events | The cursor only moves forward; a `next` below the cursor (including 0) is a typed reset error, never a reset. | `TestForeverCursorResetIsError` (`since 5, next 0`) |
| 6 | `characters.` hardcoded | `db.schema` (default `characters`) names the schema; it is validated as an identifier, used in both SELECT forms, and the allowlist is built per query. A file with `"schema":"mychars"` reads `mychars.*` and refuses `characters.*`. | `TestForeverSchemaFromFile` |
| 7 | No order buttons on the page | `controls.js` renders `orderButtons` (stay, follow, come, attack, pull, release) on a Forever card and posts to `/order` through the same token/nonce gate; Forever run buttons are pause/resume/stop. | `controls_check.js` (verbs, actions, labels, turtle card empty) |
| 8 | `event_log` tail discarded | see the codex row above. | `TestForeverEventTailOnCard` |
| 9 | Target match compared GUID text | Bots and mobs match their target unit by `keyOf` (the serve key), never the text. | `TestForeverTargetsMatchByKey` (other case/width still matches) |
| 10 | Dashboard had its own serve client | `forever_poll.go` and `forever_control.go` use `internal/forever/serveclient` (`New`, typed calls, `-32005` retry, typed errors, `Token`); an unreadable token file is an offline card / 502, never an unauthenticated call. | `TestForeverPollStatusDownIsOffline`, the control tests, and `serveclient`'s own suite |
| 11 | Do-step tests mirrored the code's method choice; duplicate branches | `gm` and `addon_send` have one call path for both slots; a new test drives the real step code against a real JSON-RPC fake daemon over HTTP (`HTTPCaller`), so a wrong method or param shape fails. | `TestForeverDoStepsAgainstFakeDaemon` |
| 12 | No C3 caveat | Restated below: the L5 control path is proven against fakes only until C3 merges. | — |

`serveclient.Client` gained `Token()` (redaction) and `CommanderResult.Status`
(the daemon's decoded status report), both additive.

### Still open: C3

L5's live control path and the Forever card's commander bots are proven
against fakes only: `commander` is `-32003 not ported` on the daemon on main
(C3). Before the PR, the three live checks in the previous section must run
on a C3 daemon: the five-member card, one order button answering through
`commander {op:cmd}`, and the scenario sample.
