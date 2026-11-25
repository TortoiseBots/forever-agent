---
type: Guide
title: Plan tools
description: Check, lint, probe, stage, export, scout and compare a dungeon plan offline, before it costs a run.
tags: [dungeons, tools, checks, export]
status: stable
generated: { by: forever-agent docs, at: 2026-11-25T00:30:00Z }
sources:
  - id: plan-check
    resource: cmd/forever-agent/plan_check_cmd.go
  - id: plan-probe-cmd
    resource: cmd/forever-agent/plan_probe_cmd.go
  - id: trash-check-cmd
    resource: cmd/forever-agent/trash_check_cmd.go
  - id: stage-check-cmd
    resource: cmd/forever-agent/stage_check_cmd.go
  - id: plan-scout-cmd
    resource: cmd/forever-agent/plan_scout_cmd.go
  - id: plan-export-cmd
    resource: cmd/forever-agent/plan_export_cmd.go
  - id: plan-diff-cmd
    resource: cmd/forever-agent/plan_diff_cmd.go
  - id: plan-list-cmd
    resource: cmd/forever-agent/plan_list_cmd.go
---

# What these tools are for

A plan is checked offline, on the same navmesh the server walks on, before a run spends a session: every command
here reads the plan file (or the rows exported from it), the mmaps folder and, for some checks, the map's world
export, and writes a report. None of them logs in, sends a packet or changes the plan; only `plan-scout
--apply-fixes` writes a corrected copy, never in place.

The mmaps folder is the server's own `data/mmaps` copy: `<map>.mmap` plus `<map>*.mmtile` for the 1.12-format mesh,
or `MMMM.mmap` plus `MMMM_XX_YY.mmtile` for a ForeverCore mesh (`--forever`). See
[navmesh-and-maps](navmesh-and-maps.md) for what those files are.

# plan-list

One row per plan file of a folder: name, map, kind, the first word of `status`, and the whole `forever_status`.

```bash
forever-agent plan-list [--dir dungeons] [--json]
```

Output is a tab-separated table headed `NAME MAP KIND STATUS FOREVER`, sorted by name. With `--json` it prints an
array of `{name, map, kind, status, forever_status}`. Files whose name is not a plan name are skipped; a plan that
does not load stops the command with the loader's error (exit 2).

# plan-check

Walks every point of a plan on the navmesh, in the order a driver goes: from the previous leg's end through each
`via` point to the leg's `to`, then every pull group, the steps' stand points and the boss. It reports the points the
mesh cannot take, the packs the party cannot see from where it pulls, the objects and items nothing provides, and the
lints below.

```bash
forever-agent plan-check [--dir dungeons] --mmaps DIR [--from-leg N [--from-pull-group N]] \
    [--spawns DIR] [--floor-aware] [--approach-hops] [--forever [--spawns DIR|FILE]] [--strict] [--json] <plan>...
```

| Flag | Default | Meaning |
|---|---|---|
| `--dir` | `dungeons` | The folder with the plan files. |
| `--mmaps` | required | The mesh folder. With `--forever` its 16-format files. |
| `--from-leg` | `0` | Check from this leg on; the legs before it only place the party. |
| `--from-pull-group` | all | With `--from-leg`: start at this 0-based pull group of that leg. |
| `--spawns` | `<dir>/../tools/dungeongen/testdata/mapN` | The map's world export, for the aggro, patrol, joiner and object lints. |
| `--floor-aware` | off | Build the rows for the lints with the floor-aware cutter (`plan-export --floor-aware`). |
| `--approach-hops` | off | Build them with the approach hops (`plan-export --approach-hops`). |
| `--forever` | off | Read `--mmaps` as the 16-format files and `--spawns` as a ForeverCore world export (a map folder or a root of `map<N>` folders) or a TSV spawn list. |
| `--strict` | off | Any lint finding fails the check, not only an error-severity one. |
| `--json` | off | Print the report as JSON. |

Exit codes: `0` every point is fine and no lint error; `11` a point fails or a lint error exists; `2` a usage error,
a plan the loader refuses, or a mesh that cannot answer (no cgo build, or no files for the map).

What you see, per plan (the counts below are an abbreviation of one run):

```text
<plan>: ok, <N> points on the map <M> navmesh
<plan>: <k> of <N> points fail (<r> root, <s> re-anchored after an earlier failure)
leg 5 (tunnel) to 12.0 -300.5 -44.0: unreachable (...)
<plan>: 3 hops exempted by the plan (check_skip)
<plan>: lints: water 1, short hop 2, aggro 4 -- ...
```

Each failing line names the leg, its name, the part (`via 2`, `direct via 1`, `to`, `pull group 9 of 12
(from_pull_group 8)`, `boss look`, or a step path such as `steps[0]`), the point and the reason. Reasons are
`unreachable`, `no line of sight`, `too far`, `off the mesh`, `detour` (a warning only), `outside the zone` (an area
trigger's stand outside it), `needs a mapping walk` (the point is marked `needs_walk`), `in an avoid box`, `cut by an
avoid box`, `server unreachable` (the server's own query refuses the hop), `hop inside the arrival radius` (the
point is within 3 yd of the one before, so the run never sends the move) and `key` (a keyed object without its
item, or an item nothing provides). Warnings are `needless direct`, `estimated patrol period` and `pack spread` (a
pull group whose members stand more than 20 yd apart, `PackSpreadYd`, on spawns the world data does not link as one
pack: a `creature_groups` group that aggros together, or a `creature_linking` edge with an aggro flag, is what links a
real pack — a formation-only group and a plain link are not one — and no stand is in the sight of both, so the tank may
refuse the far member. Split the group, or keep it on purpose and read the warning as the price of one planned pull). `pack spread`
reads the world export like the aggro lints, so a map without one is not judged. None of the three fails the plan. Hops the plan
exempts with `check_skip` are listed with the plan's reason.

The lints are counted by name: `water`, `off-floor`, `edge`, `ledge`, `short hop`, `aggro`, `boss_aggro`, `patrol`,
`reach`, `hop_len`, `arrival`, `climb_anchor`, `joiners`, `objects`. `joiners` is the pull-size estimate: bodies
outside the target pack (three or more is an error) and a body count over the row's ceiling (8 for a raid, 6 for a
party, or the plan's `pull_ceiling`). `objects` needs a world export with game objects: a spawn list that holds none
says so instead of approving a plan whose doors were never checked.

`--sql FILE` lints a deployed world migration instead: `plan-check --mmaps DIR --sql FILE [--plan NAME]` walks the
rows the server actually runs and prints only the lints.

# plan-probe

Replays every hop of a plan's exported run-leader rows on the navmesh with both queries (the server's own path
query and the offline planner), looks up each row's floor, and samples the walk's surface: where a row sits under or
over the floor, where the server refuses a hop, where the walk crosses water, lava or a cliff.

```bash
forever-agent plan-probe [--dir dungeons] [--sql FILE [--plan NAME]] --mmaps DIR [--json] [--out DIR] [--from-step N] [<plan>|<path.json>]
forever-agent plan-probe --forever --sql FILE [--plan NAME] --mmaps DIR --spawns FILE [--json] [--out DIR] [--from-step N]
```

- A plan name reads `<dir>/<name>.json` and rebuilds its rows as `plan-export --mmaps` cuts them; a `.json` path does
  the same for that file.
- `--sql FILE` probes a deployed migration instead — the exact rows the server runs — and `--plan` picks one when the
  file holds several.
- `--out DIR` writes `probe-<plan>.json` and `probe-<plan>.txt`.
- `--from-step N` keeps only the segments that end at run-leader step N or later.

Exit codes: `0` no bad segment, `20` at least one bad segment, `1` the navmesh could not answer, `2` a usage error.
The report is `tb-plan-probe` version 1 with a verdict per segment (`ok`, `warn`, `bad`) and its counts.

# trash-check

Estimates what one walk of a plan wakes besides its own pulls: the packs whose aggro radius, family assist hop,
aggro link or creature group brings them along, and the patrol windows in which a loop crosses the route. A pack the
plan's own pull or boss row clears before the segment is dead and ignored.

```bash
forever-agent trash-check [--dir dungeons] [--sql FILE [--plan NAME]] [--spawns DIR] [--patrols FILE]... \
    [--mmaps DIR] [--level N] [--spread YD] [--aggro-rate R] [--assist-yd 10] [--json] [--out DIR] \
    [--dash-out FILE] [--fail-on danger|patrol|any] [<plan>|<path.json>]
```

| Flag | Default | Meaning |
|---|---|---|
| `--sql` | none | Report on a deployed migration's rows (recommended); `--plan` picks one of several. |
| `--spawns` | `<dir>/../tools/dungeongen/testdata/mapN` | The map's world export. |
| `--patrols` | none | A patrol recording (`patrol-record` output); repeatable. It replaces the DB patrol it matches and is otherwise added. |
| `--mmaps` | none | The mesh: the route is then the server's own path; without it the route is straight lines. |
| `--level` | plan's `level_band[0]` | The party's lowest level. |
| `--spread` | 10 raid / 3 party | How far the party spreads from the route, yd. |
| `--aggro-rate` | `1` | Scales every aggro radius. |
| `--assist-yd` | 10 (20 with `--forever`) | The family assist hop's radius. |
| `--out` | none | Write `trash-<plan>.json` and `trash-<plan>.txt`. |
| `--dash-out` | none | Write the patrols as the dashboard's `patrols.json` data file. |
| `--fail-on` | none | Exit 21 when a finding of this class exists: `danger`, `patrol` or `any`. |

The report is `tb-trash-check` version 1. Every segment carries one of four verdicts, worst first: `danger` (wakes a
pack no row pulls there), `patrol` (a loop crosses the route or a stand), `own` (the plan's own pull here) and
`clear`. The text ends with `<plan>: N steps, M segments: a clear, b own, c patrol, d danger` and the model's own
caveats: positions are static, patrol phase is unknown without a recording, and there is no line of sight, so aggro
through a wall is over-counted.

# stage-check

Answers where a server-side run leader would stage the party for one leg, offline: the leg's first row (or, without
rows, its first plan point), whether the server's path query can walk from that point to the leg's next point, the
dangers a fresh instance still holds, and where a safe point before the leg sits when the first point cannot be
staged. It runs the same `dungeon.StageLeg` checks the driver's staging uses, so a leg that would refuse to stage is
refused here first.

```bash
forever-agent stage-check [--dir dungeons] --mmaps DIR [--cleared-through LEG] [--server-steps N] [--json] <plan> <leg>
forever-agent stage-check --forever --mmaps DIR --sql FILE [--plan NAME] [--cleared-through LEG] [--json] <plan> <leg>
```

| Flag | Default | Meaning |
|---|---|---|
| `--dir` | `dungeons` | The folder with the plan files. |
| `--mmaps` | required | The mesh folder: `<map>.mmap` and tiles, or with `--forever` the 16-format `MMMM.mmap` files. |
| `--forever` | off | Stage on the Forever side's inputs: the run leader's exported migration (`--sql`) for the first row, on v16 tiles. With `--forever`, `--sql` is required. |
| `--sql` | none | With `--forever`: the migration the leg's first row is read from (`plan-export --forever` output). |
| `--plan` | the `<plan>` argument | With `--forever --sql`: the plan in the file when it holds several. |
| `--cleared-through` | none | Legs `0..this one` count as cleared, so their packs are dead when this leg stages. |
| `--server-steps` | `0` | The step count `.bot run list` shows for the plan; it picks the exporter option set the first row comes from. Old-server runs only. |
| `--json` | off | Print the result as JSON with the same fields the RPC's `staged: false, dry_run: true` result carries. |

The leg may be a number (0-based) or a name. Exit codes: `0` the leg stages; `13` it refuses (the reason, the back-off
point and the earlier-leg proposal are printed); `2` a usage error, an unknown plan or leg, or a mesh that cannot
answer. The text form is one line per finding:

```text
<plan> leg 3 <name>: stage at 12.0 -300.5 -44.0 (leg_first; first point from rows)
proposal: before_leg leg 2 <name> at 10.0 -295.0 -44.0 (12 yd back)
```

`first point from rows` means the migration's first move row for that leg was used; `first point from plan` means
the leg is in the migration without any move row, so the leg's first plan point was used. An optional or
`skip_export` leg omitted from `--sql` is refused instead: `the migration's first row cannot stage it; stage from
the plan file instead`, exit 2.

# plan-export

Writes a plan as one idempotent world migration for the bot module's run leader: the rows of `ai_dungeon_plan`,
`ai_dungeon_leg` and `ai_dungeon_step`.

```bash
forever-agent plan-export [--dir dungeons] [--allow-draft] [--drop-companions-loot [--carrier-chests]] \
    [--gm-cross-direct] [--approach-hops] [--floor-aware] [--skip-legs NAME,...] [--with-optional NAME,...] \
    [--with-legs NAME,...] [--mmaps DIR] [--rev SHA] [--stamp YYYYMMDDHHMMSS] [--force] --out FILE|DIR (<plan>... | --all-live)
forever-agent plan-export --forever ... --out DIR|FILE (<plan>... | --all-live)
forever-agent plan-export --forever --report [--dir dungeons] [<plan>...]
```

| Flag | Default | Meaning |
|---|---|---|
| `--out` | required | The migration file, or a folder: the file is then named `<stamp>_world.sql`. |
| `--all-live` | off | Export every plan whose `status` starts with `live-checked`. |
| `--allow-draft` | off | Export a named `draft` plan (refused without). |
| `--mmaps` | none | Cut a hop over 60 yd between two moves into via points along the mesh; without it the long hops are only listed. |
| `--floor-aware` | off | Cut hops the floor-aware way: up to 110 yd stays whole, tight corners and shallow turns get no row, kept corners are pushed 3 yd off walls, and split points take their z from the floor. |
| `--approach-hops` | off | Also cut the hop to a pull, boss or hold that is over 50 yd from the anchor before it into move rows. |
| `--skip-legs` / `--with-optional` / `--with-legs` | none | Leave leg names out / write optional leg names in anyway / write `skip_export` leg names in anyway; each with a warning, an unknown name refused. |
| `--drop-companions-loot` | off | Export a plan with a `companions_loot: false` leg anyway (the rows cannot carry it); `--carrier-chests` adds that leg's chests as carrier rows. |
| `--gm-cross-direct` | off | Export a `gm_cross` waypoint as a straight walk instead of refusing the plan. |
| `--rev` | the plan folder's `git rev-parse` | The forever-agent commit named in `ai_dungeon_plan.source`. |
| `--stamp` | now, UTC | The migration's timestamp. |
| `--force` | off | Replace an existing file; never edit a migration that is applied. |
| `--forever` | off | Write the rows a ForeverCore server module reads (its own object entries from `alt_entries`, one unit row per pack member), and refuse a plan field the format cannot carry. |
| `--report` | off | With `--forever`: print one record per plan file (exported, or refused and why) and write nothing; it cannot be combined with `--out`. |

A file whose base name is not `YYYYMMDDHHMMSS_world.sql` gets a note: the server's updater will not take it. The
written migration is read back and compared with the built rows before it is left on disk.

# plan-scout

Checks a plan's exported rows offline and writes a report plus a fix file of proposed corrections; with `--live` it
adds a live pass on a test server (a GM account on a leased slot) and merges what it saw.

```bash
forever-agent plan-scout <plan> [--dir dungeons] [--sql FILE] [--mmaps DIR] [--spawns DIR] [--from LEG] \
    [--until LEG] [--party-level N] [--out DIR] [--json] [--fix-out FILE] [--apply-fixes DIR] \
    [--fail-on fail|warn] [--live <server> [--slot N] [--holder NAME] [--slots FILE] [--binary PATH] \
    | --forever [--parent MAP=PARENT] [--live --slot DIR]] [--walk-budget 5m] [--max-minutes 20]
```

- `--mmaps` defaults to `~/forever-agent-mmaps`; `--spawns` to the map's export under `tools/dungeongen/testdata`.
- `--out` defaults to a new `./plan-scout-<plan>-<stamp>` folder; it always holds `scout-<plan>.json` and
  `scout-<plan>.txt`. The text lists every non-ok step, then `N steps: a ok, b warn, c fail, d skip (s)`.
- `--fix-out FILE` writes the fix file (`tb-scout-fix` version 1) even when there are no fixes.
- `--apply-fixes DIR` writes a patched plan copy and `<plan>.diff` into a new or empty folder; it refuses a folder
  inside a `dungeons/` folder, and with `--sql` it first proves the migration's rows equal the plan's export.
- `--fail-on fail` (the default) exits 27 on a failed step; `--fail-on warn` also on a warning. The live pass that
  stopped part-way exits 28; a refused or failed live phase exits 18; a mesh that cannot answer exits 1.

The checks are the plan-check lints plus the scout's own rules: `reach` (a hop the server refuses), `hop_len` (a
step the walk never takes, from the plan row and from where a fight leaves the lead), `arrival` (a path that ends
short), `climb_anchor` (too much climb at a gather point), and `joiners` (bodies a pull wakes that it does not own).

On a Forever server the live pass also asks the server's own sight query (`los`, see
[movement](../daemon/movement.md#los)) from each pull row's stand to every unit of its target pack that the live
read saw. The server's answer is recorded beside the navmesh ray under the same unit: the pull row's `live_los`
check carries `server_answered`, `server_in_sight` and `disagree` next to the ray's `answered` and `in_sight`, and
its reason gives the server's verdict and how many units the two disagree about. One report note adds the pass's
totals (`live sight: the server and the navmesh ray disagree on N of M sight answers`). The server decides: one unit
in its sight settles the check even where the ray says otherwise, and its "none in sight" counts only when it answered
for every unit of the pack — a partial answer that saw nothing leaves the ray's verdict standing, with the reason
saying how many the server answered.

The queries are paced at 250 ms, which keeps them inside the server's five-at-once bucket and its 200 ms refill, and
the whole query pass has a one-minute bound. A `rate` refusal is waited out and the same query asked once more. A unit
the server no longer has (`unknown-unit`, `not-in-world`, `different-maps`: it despawned, died or changed map between
the read and the query) is skipped alone, with one note naming how many pack units stayed unanswered; the rest of the
pack is still asked. `no-access` is skipped for that one unit too, with its own note: below GameMaster it is what a
unit that did not resolve answers, and the same code a query without a unit of the caller's own gets, so the note
carries the count without naming a cause. An answer for one unit that is not understood (it is not the module's
record, or not the call's) is recorded for that unit as `answer not understood` in one note with the count, and the
pass goes on with the rest of the pack. A server whose module build has no query, that answers `bad-argument` (the
sides would be read the same way for every unit), or that answers `rate` twice is said once in the notes and the pass
goes on with the ray alone. When the query pass's bound or such a stop leaves stands unasked, one note says how many
were not asked.

# plan-diff

Compares two exports of one plan — two migrations, two plan files, or one of each — and prints what changed, with
an optional picture of the two routes.

```bash
forever-agent plan-diff <old> <new> [--plan NAME] [--json] [--out DIR] [--svg FILE] \
    [--minimaps DIR] [--picture worldmap|minimap|navmesh|none] [--fail-on-change]
```

Both inputs must be on the same map. `--plan` picks the plan when a migration holds several. `--out DIR` writes
`diff-<plan>.txt`, `diff-<plan>.json` and `diff-<plan>.svg` (refused when a file exists); `--svg FILE` also writes
the SVG elsewhere. `--minimaps` defaults to `~/forever-agent-minimaps`, holding `<map>.json`/`<map>.png` (worldmap),
`<map>.minimap.*` and `<map>.navmesh.*`; `--picture` chooses the one under the route (default `worldmap`).
`--fail-on-change` exits 25 when anything other than a comment changed.
