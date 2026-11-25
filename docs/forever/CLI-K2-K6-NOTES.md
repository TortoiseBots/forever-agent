# CLI rounds K2-K6: what ran, what was skipped

Round CLI-K2-K6 of `FOREVER-AGENT-REST-SPEC.md` (sections 2, 3 K2-K6, 6). Base: `origin/main` 9f72f496.
At that base round K4 ran; K2, K3, K5 and K6 were skipped because a dependency they name was not on
`origin/main` (never stubbed). A later round (sections 7-9) landed K2, K3 and K5 on `origin/main` 15c7b9ff.

## 1. History: the first dependency check (superseded by sections 7-9)

`git log origin/main --oneline -1` = `9f72f496 Forever: a typed client for the session daemon (#47)`.

| Round | Needs | On `origin/main`? | Evidence |
|---|---|---|---|
| K2 | W2 (serve teardown + capture), L1 | **W2 no**, L1 yes | `internal/forever/world/serve_session_methods.go` `notPortedMethods` still lists `"teardown"`; `internal/forever/world/serve.go`'s `ServeConfig` has no `CaptureDir`. The W2 commits exist only on another branch (not merged). |
| K3 | W3 (`event_log`, `act`), L2 | **W3 no, L2 no** | No `event_log` method or `internal/forever/world/eventlog.go` on `origin/main`; `internal/scenario` has no `"server"` file key or Forever mode. |
| K4 | L1, L3 | **yes** | `internal/forever/serveclient/` (#47) and `internal/worldspawn/foreverdb.go` (#45). |
| K5 | L4, L5 | **L5 no** (L4 yes) | `internal/db2/` and `internal/minimap/forever.go` are on `origin/main` (#46); `internal/dashboard/` has no `forever_card.go`/`forever_poll.go`. |
| K6 | module decision-capture API | **blocked** | The brief: skip unless the spawn message says the module landed its `/api/v1/decisions` API; it did not. |

K2, K3, K5 and K6 were therefore skipped at that base with no code change. The one runnable round, K4, is below.

A later round on `origin/main` 15c7b9ff (W2-W4 and L1 merged) landed K2, K3 and K5; sections 7-9 are its
notes. K6 is still blocked on the module's decision-capture API.

## 2. K4: plan-scout on Forever

Files: `cmd/forever-agent/plan_scout_cmd.go`, new `cmd/forever-agent/plan_scout_forever.go` (new test beside it),
`cmd/forever-agent/plan_scout_allow.go`, `cmd/forever-agent/plan_check_cmd.go` (the one fix the draft's acceptance
needed), `internal/forever/serveclient/serveclient.go`.

### Offline draft

`plan-scout <dungeon> --forever --spawns DIR --mmaps DIR [--parent MAP=PARENT]`:

- opens the v16 planner (`navmesh.NewForeverPlanner(mmaps, parents)`); `--parent` is repeatable `MAP=PARENT`, the
  server's parent-map rule (`MMapManager.cpp:353-357`, already modelled by the planner);
- loads the world with `worldspawn.LoadForeverDB(spawns, map)` (levels in the core's order, 60 + note when unknown);
- keeps `planscout.Offline`, the lints, the fix file, `--apply-fixes`, `--sql` and the exit codes unchanged;
- probes the mesh with `Plan` instead of the 1.12-only `ServerPath` (a v16 tile set refuses `ServerPath` by design);
- the report passes `plan-check --forever` (tested).

### plan-check --forever (the fix the acceptance needed)

`plan-check --forever` passed a `*navmesh.Planner` to `dungeon.CheckPlanWith`, which type-asserts the optional
`ServerQuerier` and asks `ServerPath`; a v16 planner refuses that query (`navmesh: ServerPath replays the 1.12 core's
own query`), so **every** `plan-check --forever` run with a plan exited 2 before this round. The check now passes a
`foreverNav` wrapper that offers `Plan`, `Raycast`, `Nearest` and `SetAvoid` only: the v16 planner plans each hop and
the 1.12 run-leader server walk is skipped, as the path lints already did (they drop a `ServerPath` error). Bug fix.

### Live pass (`--live --slot DIR`)

For the Forever pass, `--live` is a bool and `--slot` is a serve dir, so `foreverLiveArgs` splits them out before the
flag package reads the turtle pass's `--live <server>` / `--slot N`. The pass reads the slot's own `slot.json`
(`slotdir.Read`/`Check`), opens the slot with `serveclient.Open(dir)` and, in order:

1. waits for `status` state `online` (60 s), then requires the session's `status.character` to equal the character
   `slot.json` names (case-insensitive), so a mistaken `--slot` cannot enable GM mode, unbind or teleport another
   character; a manifest that names no character is refused;
2. sends `.gm on` and `.instance unbind all` (`.instance unbind all` resets the session player's own instance locks:
   `ForeverCore cs_instance.cpp:93-111`) and checks each answer: the core's success line (`LANG_GM_ON` "GM mode is ON",
   `LANG_COMMAND_INST_UNBIND_UNBOUND` "Unbound N instance(s).") and its error lines (`LANG_INVALID_TARGET_COORD`, the
   trinity syntax/unknown-command refusals); a refusal stops the phase with the server's own words;
3. per stop group (`planscout.LiveStops(planscout.LiveSpots(...))`): `.go xyz X Y Z MAP`, a status wait for that map
   and within 10 yd, then `units {within:40}`; every unit is converted to `world.Unit` and each spot of the group gets
   its own copy of the slice; the report notes `live stops: N of M read`. The serve confirms the line by the server's
   own teleport of this character, not by the position (a stand to the spot the character already stands on matched
   before the server answered, the next line then overtook the ack and the core dropped it in silence); a line the
   serve did not confirm is sent once more after `scoutTeleportSettle`, at most `scoutTeleportAttempts` times, and
   the pass stops on the last answer (see [LIVE-PROOFS-FIX2-NOTES.md](LIVE-PROOFS-FIX2-NOTES.md));
4. per primary hop: `.go xyz` to the hop's own origin row (`hop.From`) first, because the server plans from the
   session's position; then `path_to` (recorded as `live_path`), and for the risky hops (`climb_anchor`, `water`,
   `arrival`, `drop`, a live path that is not ok: the turtle walk list) a `walk_path` whose context deadline is the
   walk budget's end, so the budget is spent once however many walks are attempted; under 2 s of budget left no walk
   starts and the hop is `walk budget`; a walk RPC error is recorded as the hop's skipped row with the error (never a
   measured stop) and aborts the pass (28); a refused route (status partial/none, no error) still records where the
   session stopped;
5. returns `planscout.LiveData`; exit codes stay 0 done, 28 aborted part-way, 18 phase refused.

`--max-minutes` is the pass's overall ceiling: the pass runs under `context.WithDeadline`, so every online wait, units
read, path query and walk inherits it and the pass aborts (28) at the ceiling with the reason in the report note.

Every GM line goes through the allow-list first. `scoutCanonicalGM` parses it into tokens with a strict grammar and
returns the canonical text that is sent: single ASCII spaces (the line must equal `strings.Join(strings.Fields(line),
" ")`), numbers matching `^-?(0|[1-9][0-9]*)(\.[0-9]+)?$` and finite within ±10 000 000 (no exponent, hex, `+`, `_`,
`.5`, `inf`/`nan`, no leading zero), map ids with the same rule. The map must be the plan's or the portal's, and map 0
is allowed only when the plan itself is on map 0. `.instance unbind` takes only the plan's map or `all`. Everything
else is refused: `.npc delete`, `.npc add`, `.die`, another map's `.go xyz`, a `.bot` form with an extra field
(`TestScoutForeverAllowlist`).

### Levels from the live units read

`LoadForeverDB` has already replaced an empty `level_max` with 60 when the draft is built, so the unknown set is read
back from `creatures.tsv` (`foreverUnknownLevels`). After the live pass, `scoutForeverFillLevels` gives an unknown
spawn the level of the one live unit it can stand for: type `creature`, the spawn's low-24-bit guid **and** the spawn's
entry. The old entry fallback is gone because the core rolls `level_min..level_max` per spawn, so one unit's level
says nothing about its siblings; a player or pet whose guid collides is not trusted. The offline report is rebuilt
(`offline()`) before `planscout.MergeLive`, so aggro and joiner numbers use the live level; the report notes
`live levels: N spawn(s) ...`. `planscout` itself is unchanged.

### serveclient additions

`Path` (serve's `ServePath`), `PathTo` and `WalkPath`; `walk_path` waits up to 5 minutes for the walk to finish. The
daemon's unit JSON carries no hostility, so a unit's hostiles come from the export spawn `liveAggroCheck` joins it to
(the `serveclient` type stays field-for-field equal to `ServeUnit`, pinned by `TestServeClientTypesMatchTheDaemon`).

## 3. Tests

- `TestScoutForeverLevelsFromLiveUnits`: fixture export map 700, spawn 101 has no level offline (60 assumed) and
  lives at `0x...65`; the fake serve names a player on the same guid (level 60), a creature of another entry (60) and
  the spawn's own creature (30). The stand's `live_aggro` reach is 5 yd (level 30); the old by-guid fill made it 20.
- `TestScoutForeverLevelFillTrustsOnlyTheSpawnsOwnCreature`: spawn 100 (same entry, no live unit) keeps the assumed 60
  (no entry fallback), spawn 101 takes 30 from its own creature.
- `TestScoutForeverPathsStartAtHopOrigins`: every path query starts at the positioned row before its target, never at
  the last stop; every sent GM line is its canonical allow-listed form.
- `TestScoutForeverWalkBudgetIsNotExceeded`: a walk that never answers is cut at the budget's end and only one walk is
  started; every later risky hop is `walk budget`.
- `TestScoutForeverWalkFailureIsAnErrorRow`: a walk RPC error aborts (28) and its `live_walk` row is a skip carrying
  the error, never a measured shortfall.
- `TestScoutForeverGMRefusalStopsThePhase`: a refused `.gm on` is phase-refused (18) with nothing else sent; a refused
  `.go xyz` aborts (28) with the core's line.
- `TestScoutForeverMaxMinutesAborts`: a units read that never answers is cut at the `--max-minutes` ceiling and the
  report records it.
- `TestScoutForeverUnitsAreCopiedPerStep`, `TestScoutForeverSlotIdentityRefused`, `TestScoutForeverSlotManifest`.
- `TestScoutForeverAllowlist`: the canonical forms allowed; map 0, leading zeros, exponents, hex, `inf`/`nan`, `+`,
  `_`, `.5`, extra fields, tabs and doubled/spaced padding refused.
- `TestScoutForeverDraftPassesPlanCheck`: `plan-scout --forever` on a flat v16 fixture exits 0 and the same plan passes
  `plan-check --forever` (exit 0). `TestForeverParents`: `--parent` parsing and refusals.

Each fix was shown failing before: reverting it in turn made its test fail (`path 1 asks from 40, want 10`,
`2 walks started, want 1`, `live reach 20.0, want 5`, `filled 0 spawns, want 1`, `the abort does not name the
ceiling`, the refusal tests, the allow-list grammar test).

## 4. Live checks (not run here)

On a running `forever serve` slot for a real ForeverCore test dungeon:

1. `plan-scout <dungeon> --forever --spawns <export> --mmaps <v16 mmaps> --out <dir>`: exit 0, report's counts, and
   `plan-check --forever <dungeon> --mmaps <v16 mmaps> --spawns <spawns TSV>` exit 0 (this command was broken on main).
2. `plan-scout <dungeon> --forever ... --live --slot <slot dir>`: record the `status` state at start, the two phase
   lines, the stops' `.go xyz` landings (status x/y), the units read per stop, how many spawns `live levels` filled,
   `path_to`/`walk_path` counts and the walk budget left. Then `teardown <slot dir>` when W2 is merged.
3. Confirm the allow-list never sent a line outside the list (the slot's log) and no `.tb`/toolkit line went out.

## 5. Gates (on the final code commit)

Hygiene check on the diff: no hosts, accounts or local paths found.

The gates ran on the round's final committed tree (clean, no uncommitted changes). One earlier tree failed the
`TestServeClientTypesMatchTheDaemon` check because the round briefly added `Hostile/Faction/NPCFlags` to
`serveclient.Unit`; the type stays field-for-field with the daemon's `ServeUnit`, and those fields are gone.

- `make fmt-check`: clean (exit 0).
- `make cross`: clean (exit 0).
- `staticcheck ./...`: clean (exit 0).
- `go test -race ./cmd/forever-agent ./internal/forever/serveclient` (on the VM, same tree):
  `ok  	github.com/pjerra/forever-agent/cmd/forever-agent	648.867s` and
  `ok  	github.com/pjerra/forever-agent/internal/forever/serveclient	1.278s`, `VM-TEST-END rc=0 seconds=1937`.
- The full VM gate passed on that tree (`dirty=0`).

## 6. Fix round: the K4 review findings

Two reviews of the K4 branch (the cold review and the adversarial review) found the live pass measuring from the wrong origins,
converting walk failures into pass data, ignoring its runtime ceilings, not checking GM replies, a loose allow-list and
an unfiltered level fill. One go fixed all of them; each fix has a test that failed before it (the failures are quoted
in section 3).

| Finding | Fix |
|---|---|
| `path_to`/`walk_path` ran from the last stop | `.go xyz` to `hop.From` (allow-listed, landing checked on the plan's map) before each hop's path query |
| the walk budget could be exceeded by a full walk per hop | each walk runs under `context.WithDeadline(walkEnd)`; under 2 s left no walk starts; the remaining budget is spent once |
| `--max-minutes` parsed but ignored | the whole pass runs under `context.WithDeadline(max-minutes)` and aborts (28) naming the ceiling |
| a failed walk recorded as a measured stop | the walk's error is the hop's skipped row and aborts (28); a completed walk records the status stop |
| GM replies ignored | `foreverGM` checks the core's success line and its error lines; a refusal stops the phase/hop with the server's words |
| map 0 allowed through the portal sentinel, loose matching | map 0 only when the plan is on map 0; strict tokens and canonical re-render before sending |
| the level fill trusted any unit and fell back to the entry | only `creature` with the spawn's low-24-bit guid and entry; no entry fallback |
| every step shared one units slice | each spot gets its own copy |
| the report mixed partial levels after an abort | the report notes `live stops: N of M read` |
| the slot's identity was never checked (the review) | `slot.json`'s character must equal the session's `status.character` before any GM line |
| tests mirrored the code | fakes track the session position, fail, refuse and block; the canonical sent lines are asserted |

### Gates (fix round)

The gates ran on the fix round's committed tree (clean, no trailers). Hygiene check on the diff: no hosts,
accounts or local paths found.

- `make fmt-check`: clean (exit 0).
- `make cross`: clean (exit 0).
- `staticcheck ./...`: clean (exit 0).
- The full VM gate passed on that committed tree (`dirty=0`).

## 7. K2: teardown, capture, patrol-record and route-oracle on a serve slot

Second round on `origin/main` 15c7b9ff, which carries W2-W4 (#63) and L1 (#47), so the four items the
earlier round skipped are built on the merged daemon. K6 stays blocked (no module decision-capture API);
K4 was already merged (#59) and is not touched.

### `teardown DIR` on a serve slot

`cmd/forever-agent/teardown_cmd.go`. A bare `forever serve --dir` writes its own `slot.json` (server
`forever`, slot `1`, holder `forever`) and no lease: there is no lease id and no slots file behind it,
where a `slot setup --forever` dir has both. `teardownSlotHoldsALease` asks that question; when a slot
holds none, the lease step is `skip` ("a serve slot holds no lease") and `--dry-run` plans the same skip.
Before the fix the lease step loaded the default slots file, failed when it did not exist, and the run
exited 14 even though every game-side step had succeeded. A setup slot keeps releasing its real lease.

### `forever serve --capture-dir` and `forever capture-stats DIR`

`--capture-dir` arrived with W2 (`world.ServeConfig.CaptureDir`, a repository parent refused before the
login); the K2 work is the CLI side (`cmd/forever-agent/forever_capture_stats.go`, the `forever` group's
`capture-stats`): `capturedir.Stats(dir)` sums the runs' `manifest.tsv` files per core opcode and prints
`opcode packets bytes` plus `TOTAL`. It accepts one run directory or the parent of several, returns usage
for a folder with no capture, and never opens a payload. The `forever` group's usage line now lists
`--capture-dir`.

### `patrol-record DIR --forever`

`cmd/forever-agent/patrol_record_cmd.go` keeps its parse, its stance allow-list (`patrolRecordAllowedLine`,
unchanged) and its output path; `--forever` selects `patrol_record_forever.go`, which talks to
`serveclient` through a small interface (`Status`, `Unit`, `Units`, `Party`, `GM`) so the tests inject a
fake slot. The order is the 1.12 command's: party check, live-world check (`status.state` empty or
`online`, named character, not dead or ghost, a guid), the three stance lines, a status wait for the
teleported map, the sample loop, `.gm visible on`. Safety rules are the same, with two Forever details:

- a unit targeting the agent stops the run only when its target is the agent's full 128-bit guid
  (`bughunt.CanonGUID` on both sides); a guid that merely shares the low 64 bits does not. Comparing a
  truncated uint64 is exactly what misses the real target.
- `Sample.GUID` is the serve `GUID.Key()` the analysis groups by; each sample also records the unit's
  32-hex text, and after `patrols.Analyze` the file's `static`/`wanderers` guids, each patrol's `guids`
  and an unmatched patrol's `id` are rewritten to that text (the "side column"), so a Forever recording
  names real guids, not the 64-bit analysis key.
- `--mmaps` is a v16 folder (`patrolRecordForeverFloors` uses `navmesh.NewForeverPlanner`); `--z` still
  skips it. `--spawns` reads the same `creatures.tsv`; spawn matching is by entry and position, unchanged.
- The file's `source` is `record <slot server> <date>` (the slot manifest's own server name).
- The sample loop keeps NPC creatures only. A Forever serve names an object type (`objects.TypeName`), so its NPCs
  arrive as `unit`; the 1.12 agent's `creature` is accepted too. Filters on the 1.12 word alone recorded nothing on a
  serve slot (0 patrols, 0 static, 0 wanderers with ~30 units in range).

### `route-oracle --forever --mmaps DIR [--parent MAP=PARENT]`

`cmd/forever-agent/route_oracle_cmd.go` opens `navmesh.NewForeverPlanner(mmaps, parents)` instead of the
v6 planner; `--parent` is the K4 `foreverParents` parser and is refused without `--forever`. The modes,
plan checks, reports and exit codes are unchanged. One bug fix in `internal/routeoracle` fell out of the
acceptance: `tiles` hardcoded `%03d.mmap`, so a Forever report failed on the map file the server never
writes (`700.mmap`); it now asks the planner's format (`Format().MmapName`, `0700.mmap` for v16). The
MaNGOS (three-digit) case is unchanged and pinned by the existing report test.

### Tests

- `TestTeardownServeSlot` (failing case): a dir with serve's own `slot.json`, a token file and a fake
  `/rpc` that answers teardown steps → exit 0, the lease line says a serve slot holds none, the token and
  pid files are gone, and no slots file is created. The old code failed the lease step (exit 14).
- `TestCaptureStatsPrintsNoBytes`: two packets of a named opcode and one unknown, the payload files deleted
  before the run (one payload present but never printed) → counts and `TOTAL` right, exit 0, no payload
  text; a dir with no capture is usage. A reader of payloads fails on the deleted files.
- `TestPatrolRecordForeverStopsWhenTargeted` (failing case): run 1 has a creature targeting a guid that
  only shares the agent's low 64 bits → exit 0 and the file's `static` row carries the full 32-hex guid;
  run 2 has a creature targeting the agent's full guid → exit 22, `targeted by Ambusher`, the file written
  with `stopped`, and the four stance lines exactly in allow-list order. A low-64 compare fails run 1; a
  text compare rewritten from the key fails run 2.
- `TestPatrolRecordForeverRefusesADeadSlot`: a `reconnecting` slot → exit 15 and no file.
- `TestRouteOracleForeverPlanner` (failing case): a v16 fixture and a plan → `--forever` exits 0 and writes
  the report; the same folder under the v6 planner exits 12; `--parent` without `--forever` is usage.
- `TestOracleNamesTheForeverMapFile` (routeoracle): the report names and hashes `0700.mmap`.
- Mutation checks: skipping the serve-slot lease test fails `TestTeardownServeSlot` (exit 14); a low-64
  target compare fails the decoy run; `%03d.mmap` fails `TestRouteOracleForeverPlanner`; reading payloads
  fails `TestCaptureStatsPrintsNoBytes`.

### Live checks (not run here)

1. Two serve slots: `forever serve --dir A --capture-dir <outside-dir>` and `--dir B`; after the start,
   `teardown A` (a bare serve dir) → every step ok, the lease step skipped, the process gone.
2. 30 s capture on A, then `forever capture-stats <outside-dir>`: nonzero counts, `TOTAL` matches the
   manifests, and the command's output holds no payload byte. Delete one payload file and repeat.
3. `patrol-record A --forever --map <m> --area x,y,r --minutes 5 --spawns <export> --mmaps <v16>`: file
   written, guids 32-hex, the command's stance lines only in the slot log.
4. `route-oracle --forever --mmaps <v16> --out <dir> --plan <name>` on a Forever plan: exit 0 and
   `0700.mmap` (or the map's own) in the tile list.

## 8. K3: scenarios on Forever

`cmd/forever-agent/scenario_cmd.go` adds `--slot NAME=DIR` (repeatable; a bare `DIR` when the file has one
session). The session must be declared `"server": "forever"`; `slotdir.Read`/`Check` validate the dir and
the session's address and token come from it (`127.0.0.1:<port>`, `<dir>/rpc-<port>.token`), never from the
file's placeholder. A `no_auth` slot sends no header (`scenario.HTTPCaller.NoAuth`, additive). The
`--session` override still applies first; a slot for a turtle session is refused. The file's `server` key
still drives the run itself (L2: `online` start wait, `event_log` drain, serve methods by name).

`scenarios/forever-chat-party.json`: two sessions, A says `hello from ${A.character}` and invites B, B's
event log is checked for the `chat` line and the `party` invite, B accepts, both `check_rpc party in_group`, and A leaves the group. `--prefer`/`--var` untouched.

Tests: `TestScenarioRunForeverSample` (failing case) runs the shipped sample through `scenario run --slot
A=DIR --slot B=DIR` against two fake serves; the fakes answer `status`, `event_log`, `chat`, `invite`,
`accept_invite`, `party` and `leave_group` and move the chat/party rows between their logs. Exit 0,
`result.json` passes every check, and exactly one `accept_invite` ran. Before the change the CLI did not
know `--slot` (usage), and ignoring it leaves the sample's placeholder addresses unreachable (ERROR).

Live (X1): two serve slots, the sample PASS twice.

## 9. K5: minimap-extract on the 1.60 client data

`cmd/forever-agent/minimap_cmd.go` adds `--forever` and `--db2 DIR`. `--forever` runs
`minimap.RunForever` (L4: a `Map.db2`-driven map list, a CASC extract as `--client`, tiles or the navmesh
render, `--prefer` limited to `minimap,navmesh`); `--db2` is required with it and refused without it. When
`--prefer` is empty, the CLI passes no preference, so the Forever default is `minimap,navmesh` (the v6
default `worldmap,minimap,navmesh` is unchanged for the MPQ path). `reportTotal`'s table and exit codes are
shared.

`dashboard_cmd.go` needed no flag: L5 reads `server` per agent from the settings file, and a Forever agent
is polled through `serveclient`.

Tests: `TestMinimapForeverFlagsRequireDB2` (failing case): `--forever` without `--db2` is usage and names
`--db2`; `--db2` without `--forever` is usage and names `--forever`.

Live (X2): an outside CASC extract plus the server's `dbc/<locale>`: `--maps all-instances --list`, then a
real extract of one dungeon; a WMO dungeon falls back to the navmesh render.

## 10. Gates (this round)

The gates ran on the clean tree of the last code commit (the three code rounds and the notes; no AI or
co-author trailers). The commit after it is documentation only.
Hygiene: a grep of the branch diff for private hosts, IPs, accounts, e-mails or local paths finds nothing.

- `make fmt-check`: exit 0, no output.
- `make cross`: exit 0 (windows/amd64 and darwin/arm64 builds, then the windows vet of `./cmd/...` and
  `./internal/forever/...`).
- `staticcheck ./...`: exit 0, no findings.
- `go test -race -count=1 ./cmd/forever-agent ./internal/routeoracle ./internal/scenario`: rc 0;
  `ok github.com/pjerra/forever-agent/cmd/forever-agent 539.122s`,
  `ok github.com/pjerra/forever-agent/internal/routeoracle 1.286s`,
  `ok github.com/pjerra/forever-agent/internal/scenario 1.533s`.
  A first attempt failed `TestSecondInterruptKills` with `child never printed "ready": ""` (its 5 s
  child-start deadline under load, unrelated to this round); the test passes alone and in the clean rerun
  whose result is quoted above.
- The full VM gate passed on that committed tree (`dirty=0`).

## 11. Hardening on a serve slot

A hardening pass over the K2, K3 and K5 paths changed how a running serve dir is recognised and how hostile input
is bounded; all of it is on the same commands.

### Teardown of a real serve daemon

`forever serve --dir` runs the binary from wherever it lives, so `agent.pid` is no longer checked against a copy
inside the dir. The dir's agent is the pid in `agent.pid` **and** a process whose `/proc/<pid>/cmdline` is
`forever-agent forever serve ... --dir <dir>` **and** whose start time (field 22 of `/proc/<pid>/stat`) equals the
`pid_start` the daemon records in `slot.json`. A slot setup dir still runs the copied `<dir>/forever-agent`, which
the same probe accepts as before. A pid that is running but fails these checks is a reused pid: the run reports it,
signals nothing and keeps `settings.json`, the token file and `agent.pid`. `--no-auth` dirs are called with no
`Authorization` header and no token file read; a `slot.json` that says `no_auth` while a token file exists is
refused as ambiguous (the same rule `serveclient.Open` applies). A `slots_path` taken from `slot.json` must be an
absolute regular file; a missing, relative or symlinked one is the lease step's failure, and nothing is created
beside it. A serve started as `--dir D --token-file /elsewhere/tok` writes a manifest whose `token_file` is
outside D; `slotdir.Check` refuses such a dir to `teardown` and `scenario --slot` (a safe failure with a clear
message), so the token path is always the dir's own `rpc-<port>.token`.

### capture-stats

The manifests are read as regular files (a symlink, FIFO or device is refused) with a byte cap, a line cap and the
writer's per-run entry bound (8192 packets, each at most the frame reader's `wire.MaxUncompressed`: the world
socket's header carries a uint32 `Size`, so a 16-bit byte bound rejected every live capture that holds a hotfix
list). A parse error names the line number and the reason,
never the line's text (a hostile manifest may hold captured chat), and neither the per-opcode nor the printed
TOTAL can overflow.

### Scenario

The HTTP client re-checks every redirect hop against the loopback rule, so a 307 cannot carry the JSON-RPC body or
a no-header `no_auth` call to another host. `--slot` takes its token rules from `serveclient.Open` (no_auth and no
token file; a token file with no_auth, or no token file without no_auth, is refused), refuses a name given twice
and a dir given to two sessions, and `check_rpc` enforces the world-mutating `gm` guard (literal text at load, the
expanded text at run time) and retries `-32005` on a Forever session. A `check_db` step (which cannot name a
session) infers the Forever slot when every session is a Forever one and refuses a mixed-server file that does not
name `server` on the step.

### patrol-record --forever

The final `.gm visible on` runs on a fresh bounded context, so Ctrl-C still restores visibility; a refusal line in
a GM reply is an error; the teleport wait needs the status on the map near the area centre, not only the map; and
the units read asks for more than serve's default 100 units, so a hostile unit is not cut off the list.

### minimap-extract --forever

`Map.db2`, the minimap folders and the tile files are read only when they resolve inside `--db2` / `--client`; a
symlink that leaves them is treated as absent. `--prefer worldmap` with `--forever --client` is a usage error (a
listfile folder holds no world-map textures).

`--forever --casc INSTALL [--product P]` replaces `--client` with the install itself (both is a usage error, neither
is one too), so the textures are read by FileDataID instead of by listfile name. The terrain minimap is the
`Map.db2` row's `WdtFileDataID` WDT, whose `MAID` chunk lists 64 x 64 tiles of eight FileDataIDs: entry 7 is the
tile's minimap texture, 0 is none; entry index is row*64+col as the `MAIN` ADT list is. The world map is the map's
`UiMapAssignment` row (lowest `OrderIndex`, then the largest world region), its `UiMapXMapArt` picture in phase 0,
and that `UiMapArtTile` art's `LayerIndex` 0 tiles (256 px, stitched by row and column). The picture's `<id>.json`
places it by the assignment's world region and UI rect: the region covers the UiMin..UiMax rectangle of the
picture, X to the right and Y down, and `px_per_yard` is its west-east scale. `--prefer worldmap` works on this
path only, and the three UiMap tables are required in `--db2` (`Map.db2`'s own field and layout checks apply to
each: field count and layout hash as in `UiMapArtTileMeta`, `UiMapAssignmentMeta` and `UiMapXMapArtMeta`).

## 12. #104 round 4: dungeon interiors from the WMO

An indoor map has no terrain tiles: its WDT is WMO-only, and the client draws the WMO groups' own block textures.
`minimap-extract --forever --casc INSTALL --db2 DIR --maps 389 --wmo-probe` reports one map and writes nothing:
the WDT's root WMO FileDataID (the MODF row whose flags say its Id is a FileDataID, else the MWID entry that Id
indexes), the root's MOHD WMO id, every MOGI group's box, the `WMOMinimapTexture` rows of that id and the block size
those boxes imply. The rows are keyed by the MOHD id, not by the root file's FileDataID; the client's BlockY pallet
stores 0x100+BlockY (all 33 values of build 70959), so the byte is the low 8 bits.

That block size is the round's open value, and it is not written anywhere in the client data: a group of E yards
covered by n blocks spans E/n <= block < E/(n-1). The groups of the Ragefire Chasm and the Wailing Caverns interiors
give (124.34, 148.85] and (127.08, 128.76] yd, so the value is 128 yd (`minimap.WMOBlockYards`, pinned by
`TestWMOBlockYardsFromClient` when `TURTLE_CASC_INSTALL` and `TURTLE_DBC_DIR` are set). The textures agree: a group's
whole minimap image is padded to powers of two at 2 px a yard and cut into 256-px tiles, so a full block's texture is
256x256 px and an edge tile is 256x128 or 64x256 (Ragefire Chasm's and the Wailing Caverns' blocks match). The
composition places each tile at the group box's minimum plus (BlockX, BlockY) * blockYards, at the tile's own pixel
extent (a trimmed tile reaches less far), and the picture is at 2 px a yard; groups are split into floors by their
boxes' heights with mapview's floor gap, the lowest floor is
`<id>.png` and the higher ones `-2f`, `-3f`, all with `"from": "wmo"`. `WMOMinimapTexture.db2` is read when
`--db2` holds it (the other tables stay); `--forever --casc` requires `--wmo-block-yards`, and a run without it is a
usage error naming the flag and the probe, so the code never draws at a guessed size.

Tests: `TestWMOBlocksPlaced` (block 1,0 below and 0,1 right of block 0,0, with another WMO's row present),
`TestWMOProbeListsGroups` (root FileDataID, boxes, rows of this WMO only, the implied size), `TestWMONeedsBlockYards`
(the missing size is an error naming `--wmo-probe`, also through `runForever`), `TestCascWMOInterior` (a full
`--forever --casc` run writes `60.png` with `"from": "wmo"`), `TestWMOFloorsByHeight` (two floors, the second
suffixed `-2f`), and the db2 reader's `TestWMOMinimapTextureFields`, `TestWMOMinimapTextureLayoutRefusesOther` and
`TestWMOMinimapTextureNeedsParent`. Mutations: BlockX/BlockY swapped and the WMOID filter dropped are both caught
by `TestWMOBlocksPlaced`.

