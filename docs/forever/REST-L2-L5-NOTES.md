# REST L2 (scenario Forever mode) + L5 (dashboard live view over serve) — notes

Round: NEXT-REST-L2-L5. Packages: `internal/scenario`, `internal/dashboard` only.
Commits: L2 `68068ac8`, L5 `<L5-SHA>` (this round, two commits; L2 first).

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
  `commander {op:status}`, `units {within:100}`, `event_log` tail. No
  `tbm*`, `dungeon_status`, `tune_list` or `bot_view` call. `status`
  failure is an offline card, not an error.
- `BuildForeverCard` fills the existing `Card` fields: self from status
  (`online` sets `Pos`), bots (role/hp/pos/order) from the commander
  status members, mobs from units, run state/plan/elapsed from the
  commander run record, stall from elapsed. Guids stay text, keyed by
  `serveclient Key` (`runmap.GUIDKey`: high word matters).
- Controls (`forever_control.go`): order verbs
  stay/follow/come/attack/pull/release and run pause/resume/stop all
  call `commander {op:cmd|run}`. `run-active`/`not-available` come back
  shown as the reply (200 with the module's words). No `tbm*` call on a
  Forever agent; the turtle `run` handler (fresh `dungeon_status` live
  check, `dungeon_*` verbs) never runs for `Kind == forever`.
- Guides: `dashboard-guide.md` (Forever poll + settings server key),
  `run-controls-guide.md` (commander controls), `scenarios-guide.md`
  (session/check_db server keys, Forever drain/do-step behavior).

## Gates (final commit, clean tree)

```
<make fmt-check line>
<make cross line>
<staticcheck line>
<vm-test line>
<go test -race lines>
```

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
