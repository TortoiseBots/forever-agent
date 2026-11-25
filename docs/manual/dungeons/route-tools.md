---
type: Guide
title: Route tools
description: Score a plan's route against the navmesh, walk it on a live server, edit and promote routes, rebuild a leg's waypoints, record patrol loops and record a GM's walk as world data.
tags: [dungeons, routes, navmesh, tools]
status: stable
generated: { by: forever-agent docs, at: 2026-11-25T00:30:00Z }
sources:
  - id: route-oracle-cmd
    resource: cmd/forever-agent/route_oracle_cmd.go
  - id: route-walk-cmd
    resource: cmd/forever-agent/forever_route_walk.go
  - id: route-walk-core
    resource: internal/forever/world/route_walk.go
  - id: route-edits-cmd
    resource: cmd/forever-agent/route_edits_cmd.go
  - id: routeedit-store
    resource: internal/routeedit/store.go
  - id: route-regen
    resource: tools/dungeongen/route_regen.go
  - id: route-aggro
    resource: tools/dungeongen/route_aggro_cmd.go
  - id: patrol-record
    resource: cmd/forever-agent/patrol_record_cmd.go
  - id: gm-walk
    resource: cmd/forever-agent/forever_gmwalk.go
  - id: gm-walk-core
    resource: internal/forever/world/gm_walk.go
  - id: gm-convert
    resource: cmd/forever-agent/forever_gmconvert.go
  - id: gmwalk-convert
    resource: internal/forever/gmwalk/io.go
---

# What these tools answer

A plan's waypoints can be wrong in three different ways: the route is longer or clumsier than the mesh needs, the
character cannot actually stand where the plan says, or a pack wakes before the plan pulls it. The tools here
answer those questions in that order: `route-oracle` scores the stored route offline, `route-walk` proves it on a
live server with no fighting, and `patrol-record` supplies the patrol data the other checks use. The edit side
(`route-edits` and `dungeongen route-regen`) writes a corrected route back.

# route-oracle

Compares each plan's stored route with what the navmesh would plan for its objective chain, and writes a table and
a JSON report per plan.

```bash
forever-agent route-oracle --plans DIR --mmaps DIR --out DIR [--mode walk|bosses] \
    [--objectives plan|bosses] [--plan NAME]... [--forever [--parent MAP=PARENT]...]
```

| Flag | Default | Meaning |
|---|---|---|
| `--plans` | `dungeons` | The folder with the plan files. |
| `--mmaps` | required | The mesh folder (the 16-format files with `--forever`). |
| `--out` | required | Folder for `<plan>.txt` and `<plan>.json`. |
| `--mode` | `walk` | `walk`: plan the plan's own stored sequence hop by hop and score the stored hops on the mesh. `bosses`: plan entrance → each leg's objective live, with no hand points. |
| `--objectives` | `plan` | `bosses` mode only: `plan` takes each leg's own objective (its boss, else its `to`); `bosses` takes only the chain entrance → boss 1 → boss 2 … and folds legs without a boss into the next boss. |
| `--plan` | five plans | A plan name; repeatable. The default is `wailing_caverns`, `ragefire_chasm`, `uldaman`, `zulfarrak`, `deadmines`. |
| `--forever` | off | Plan on a ForeverCore 16-format mesh instead of the 1.12 one. |
| `--parent` | none | `MAP=PARENT`, repeatable; an instance map whose missing tile comes from the parent map. Needs `--forever`. |

Every leg is planned twice: plainly, and on a scratch planner with the plan's `avoid` boxes; the report's numbers
are the avoid-aware path, the one a run plans with. File names follow the mode: `<plan>.txt`/`.json` in `walk`,
`<plan>-bosses.*` in `bosses` mode, `<plan>-bosses-only.*` with `--objectives bosses`.

Output per plan is the report table, its one-line summary and the mesh tile files it read, with their sha256 and
size, so two reports can be proven to rest on the same mesh. Exit code `12` when a plan could not be reported; `0`
otherwise. A plan whose route fails is a report, not a failure.

# route-walk

Walks a whole plan in an instance with the agent's real session, checking every planned point against the server's
own confirmed position, and stops before any fighting happens. This is the "mapping walk" the plan statuses talk
about.

```bash
forever-agent forever route-walk --plan FILE [--legs a-b|NAME,...] [--gm] --navmesh DIR \
    [--parent MAP=PARENT[,..]] [--enter tele|none] [--leave tele|none] [--confirm gps|client] \
    [--out DIR] [--json] [--max-deviation YD] [--stuck-after DURATION] [--stuck-step YD] \
    [--fall-drop YD] [--sample DURATION] [--liquid-above YD] [--liquid-below YD] \
    [--settle DURATION] [--timeout DURATION] --rest host:port --rpc host:port --ca FILE --account EMAIL --character NAME
```

| Flag | Default | Meaning |
|---|---|---|
| `--plan` | required | The plan file. `entrance_tele`, `outside_tele`, `avoid` and the legs are used. |
| `--legs` | every leg | 1-based numbers and ranges (`1-3`) or plan leg names, comma separated. |
| `--gm` | off | Turns GM mode on for the walk and off at the end; also lets the entry teleport skip the instance's entrance checks. |
| `--navmesh` | required | The ForeverCore mmaps folder (`MMMM.mmap`, `MMMM_XX_YY.mmtile`). |
| `--parent` | none | `MAP=PARENT` pairs, comma separated. |
| `--enter` | `tele` | `tele`: the plan's `entrance_tele` row; `none`: the character is already placed. |
| `--leave` | `tele` | After the last leg: the plan's `outside_tele` row, or `none`. |
| `--confirm` | `gps` | `gps` reads the server's own position with `.gps`; `client` takes the last movement packet and shows a server correction as a deviation (no stall test). |
| `--max-deviation` | 2 yd | Distance above which a leg is DEVIATED. |
| `--stuck-after` / `--stuck-step` | 6 s / 0.5 yd | Stillness that makes a leg STUCK. |
| `--fall-drop` | 2 yd | How far the confirmed Z may end below the planned point before the leg is FELL. |
| `--sample` | 3 s | Longest uninterrupted walk before the position is confirmed again. |
| `--liquid-above` / `--liquid-below` | 2 / 0.5 yd | Band round a planned point in which a magma or slime floor fails the leg. |
| `--settle` | 4 s | Time to settle in the world before the first command. |
| `--timeout` | 30 m | The whole run's budget. |
| `--out` | none | Writes `route-walk.json` there. |

Leg verdicts, worst first: `FAIL`, `INCOMPLETE` (the deadline or a cancel ended the leg), `UNCONFIRMED` (the
server did not confirm the position), `STUCK`, `FELL`, `LIQUID`, `NOPATH`, `DEVIATED`, `PASS`. A leg is `PASS` when
every planned point was confirmed within the deviation threshold, no walk crossed a fall, damaging liquid or a
stall, and nothing went wrong. The run's verdict is the worst leg's, so `PASS` means every leg passed. The walk
ends at the first `UNCONFIRMED` leg: without a confirmed position there is no honest origin for the next leg.

What you see is one line per leg and a final line:

```text
route-walk: leg 0 entrance           PASS        110.2 of  110.2 yd, 8 point(s), max deviation 0.41 yd, max snap 0.00 yd
route-walk: 1 leg(s), verdict PASS, 110.2 yd planned, 110.4 yd confirmed, 2m1s
```

Exit codes: `0` PASS; `12` the walk ran but a leg did not pass; `5` the world rejected the session or the character;
the login errors of every Forever command (3, 4, 6) and `2` for a plan or a flag this build refuses before login.

# route-edits

The dashboard's route editor keeps every edit as a numbered version (`v0007`) with its plan check, its operations
and its warning that the check ran against one server's mesh. `route-edits` lists those versions, exports a checked
one into a plan folder, and promotes one into the repository for a PR.

```bash
forever-agent route-edits list [--edit-dir DIR] [--json] [<plan>]
forever-agent route-edits export [--edit-dir DIR] [--config-dir DIR] --to DIR <plan> [vN|latest]
forever-agent route-edits promote [--edit-dir DIR] [--message-file FILE] --worktree PATH <plan> vN
```

- `--edit-dir` defaults to `~/forever-agent-route-edits`; the versions are `vNNNN.json` with `vNNNN.meta.json`
  beside them, numbers from `v0001` to `v9999`.
- A version's status is `checked` (the plan check found no issue at all — the only kind a run may use),
  `unchecked` (the edit made no new issue but the plan has issues of its own), `refused` (the check failed or could
  not run; a record only) or `accepted` (a run walked it and passed).
- `list` prints one line per version: plan, version, status, time, who, operations, the check's numbers and the
  base's sha256.
- `export` copies the newest checked version (or the one named, `vN`, `N` or `latest`) into a fresh folder as
  `<plan>.json`, for a stopped slot's plan folder, and prints a `route_edit=<tag>` line to pass as the run's
  `trace_meta`. It refuses a folder a running agent reads, and warns when the target slot's `mmaps_dir` is not the
  mesh the version was checked on.
- `promote` writes the version over `dungeons/<plan>.json` of the given worktree and prints the commit message
  (`--message-file` also writes it to a file).

# dungeongen route-regen

Rewrites one leg's route from the navmesh: it keeps the leg's anchors (its `to`, its `gm_cross` vias, every pull
group's stand or last approach point, the boss and its approach), moves an anchor that fails the placement rules to
the cheapest passing point within 25 yd, orders the pulls by path length (flagging one-way pairs), plans each
anchor pair round the packs not pulled yet, the patrol lanes and water, string-pulls the path with clearance, pushes
each kept corner 3 yd inward, re-checks the points and spaces them 8-45 yd apart, and writes them as authored
approach or via points. The pull set does not change: only the order and the route do.

```bash
go run ./tools/dungeongen route-regen --mmaps DIR --leg N [--leg N...] [--dry-run] [--dir dungeons] \
    [--spawns DIR] [--out FILE] [--force] [--forever [--parent MAP=PARENT...]] <plan>
```

`--dry-run` prints the diff and writes nothing. Exit `0` when done (even with a note for the lead), `2` when the
command cannot run.

# dungeongen route-aggro

Lists the route segments of a plan that walk within aggro of a pack a later row pulls — the mistake that makes a
run pull two packs at once.

```bash
go run ./tools/dungeongen route-aggro [--dir dungeons] [--spawns DIR] [--forever] [--ignore-factions 35] <plan>
```

Exit `0` no segment enters a later pack's aggro, `1` findings, `2` the command cannot run. `--ignore-factions` is a
comma-separated list of factions that are not hostile (35, friendly to all, by default).

# dungeongen: drafts, snaps and the table

The same tool generates the draft plans and keeps the generated tables honest:

```bash
go run ./tools/dungeongen -spec tools/dungeongen/plans.json -out-dir dungeons -table docs/PLANS.md
go run ./tools/dungeongen -spec tools/dungeongen/plans.json -snap-update ~/tb-ci
go run ./tools/dungeongen -spec tools/dungeongen/plans.json -forever -snap-update FOREVER_MMAPS
go run ./tools/dungeongen route-regen --forever --mmaps FOREVER_MMAPS --spawns EXPORT --leg N <plan>
```

- The first rebuilds every draft and `docs/PLANS.md`; a test fails when the file is out of date.
- The second snaps spawn positions that have no floor within 5 yd sideways and 3 yd up or down to the nearest
  walkable floor and writes `tools/dungeongen/snaps.json`; a point with no such floor keeps its spawn position and
  carries `needs_walk`. With `--forever` it writes `snaps-forever.json` from the ForeverCore mesh and leaves
  `snaps.json` alone.
- A ForeverCore world export is read with `--forever` from the folder `tools/dungeongen/export-forever.sh` writes.

# patrol-record

Records patrol loops of creatures around an already logged-in instance slot and writes them in the `tb-patrols`
format, which `trash-check --patrols`, `plan-scout` and the raid walks read.

```bash
forever-agent patrol-record <slot-dir> --map M --area x,y,r [--z Z] --minutes N [--sample-ms 1000] \
    [--out FILE] [--spawns DIR] [--mmaps DIR] [--entries e1,e2] [--forever]
```

The area radius is 10-200 yd, the recording 1-60 minutes, the sample 500-5000 ms. The command is a bystander: it
leases nothing, logs in nothing and sends only its allow-list of GM lines (`.gm on`, `.gm visible off`, one
`.go xyz` into the area, `.gm visible on` at the end); it never attacks, casts, targets or moves the character. Exit
`22` means the recording stopped for safety (combat, death, lost world) — the file is still written.

# forever gm-walk and forever gm-convert

Records a game master walking a route in game, then turns that recording into the world data the server module reads.
`gm-walk` logs in as a real client, follows the GM named by `--follow`, samples the GM's own position, and treats the
GM's whisper (or a nearby say) as the marks that make points, danger zones and road splits; the agent only follows and
prints, it never moves the GM. `gm-convert` takes no login at all: it validates a recording and rewrites it.

```bash
forever-agent forever gm-walk --follow NAME [--marks-from gm|any] [--sample 2s] [--tolerance 1.0] \
    [--follow-yd 6] [--out FILE] [--module-out FILE] [--force] [--dry FILE] [--settle 4s] \
    [--timeout 60m] --rest host:port --rpc host:port --ca FILE --account EMAIL --character NAME

forever-agent forever gm-convert --in FILE --out FILE [--module-out FILE] [--force]
```

| Flag | Default | Meaning |
|---|---|---|
| `--follow` | required (or `--dry`) | The GM character to follow; its whisper or nearby say carries the marks. |
| `--marks-from` | `gm` | Whose lines may mark: `gm` (only `--follow`) or `any` (any sender's whisper or nearby say). |
| `--sample` | `2s` | Position sample interval, at most `1m`. |
| `--tolerance` | `1.0` | Road-thinning tolerance in yards, 0-100. |
| `--follow-yd` | `6` | How close the agent stays to the GM, above 0 up to 100 yards. |
| `--out` | `gm-walk-<map>-<utc-timestamp>.json` | The world-data file; never overwritten unless `--force`. File names never carry a character name. |
| `--module-out` | none | The module-shape file beside it: `handcrafted_road` rows plus zone and point proposals; never overwritten unless `--force`. |
| `--dry` | none | Replay a hand-made recording instead of the network (test data, never a capture). |
| `--settle` | `4s` | How long to settle in the world before the follow starts, 0-2m. |
| `--timeout` | `60m` | The whole run's budget (login, follow, logout), 1m-6h. |

`gm-convert --in` reads a recording and writes two files: `--out` is the same shape, validated and normalised, and
`--module-out` is the converter's `handcrafted_road` rows with the zone and point proposals (a one-point road has no
hop to render and is reported as dropped). Both are refused when the file exists, unless `--force`.

Exit codes: `0` done; `2` a usage or recording error (`--in`/`--out` missing, an unreadable file, a flag out of its
range); `5` a file could not be written (or, for `gm-walk`, the world server refused the session).
