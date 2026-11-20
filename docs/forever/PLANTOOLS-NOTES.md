# Plan tools Forever mode (plan-probe, trash-check, stage-check --forever)

The three offline plan tools read ForeverCore inputs in `--forever` mode:
the run leader's exported migration (plan-export --forever output) plus the
spawn list TSV (`plan <name> spawns` output: guid entry x y z movetype
[kind]), on the v16 tiles (`forever path-check`, internal/navmesh with the
v16 reader). The old mode (world SQL dump, v6 tiles, plan JSON rebuild) is
unchanged. Public repo: plan files and the plan checker only, no live names,
hosts, addresses, captures or private documents.

## What --forever does per tool

- `plan-probe --forever --sql FILE --spawns FILE --mmaps DIR` walks every
  hop of the exported migration on the v16 tiles with the Forever walker
  (PathTo) and matches every pull, boss, `first` and patrol unit row against
  the spawn list by entry plus position (a creature GUID carries no spawn
  id: matched the way plan-check --forever matches). A unit with no spawn
  of its entry within the search radius (15 yd pull, 30 yd boss, +5 yd
  slack) is a MISSING finding and exits 20 like a bad segment. The probe's
  text and `tb-plan-probe` JSON keep their shape; `--out` and `--json`
  write the Forever report (segments plus the per-unit matches). Skipped
  with a note: linked-pack and patrol tables (the spawn list has no groups,
  links or waypoints: each spawn judged alone).
- `trash-check --forever --sql FILE --spawns FILE [--mmaps DIR]` reports
  what one walk of the exported migration wakes. Spawns are matched by
  entry only (the model assigns packs by entry and distance); a unit row
  whose entry the list lacks is named in a note as unplanned there too.
  Routes run on the v16 tiles when `--mmaps` is given (the Forever walker's
  corners), else straight lines. Skipped with a note, never guessed:
  linked packs (no linking rows: every spawn walks alone), patrol windows
  (no waypoint loops in the list: none simulated unless a tb-patrols
  `--patrols` recording gives one; the plan's own patrol paths are
  waypoints without waits or periods and invent no windows), and the
  family assist hop runs on faction alone (the list carries no flags).
- `stage-check --forever --mmaps DIR --sql FILE <plan> <leg>` stages the
  leg on the v16 tiles with the migration's first move row of the leg as
  the first row (the module starts the leg at its first row). The staging
  walk replays the module's own walk query (the Forever walker, up to
  three legs, inside the arrival radius counts as arrived). A leg with no
  move row in the migration falls back to the leg's first plan point with
  a note. The old exporter option-set search (plain, cut, approach,
  avoid-box routes) is the run leader's, not the module's, and is not
  rebuilt here.

## Commands

```sh
# probe a plan's hops and units against the spawn list, on v16 tiles
forever-agent plan-probe --forever --sql plan_ragefire_chasm.sql --spawns rfc-spawns.tsv --mmaps /path/to/mmaps

# what one walk wakes (routes on v16 tiles; without --mmaps: straight lines)
forever-agent trash-check --forever --sql plan_ragefire_chasm.sql --spawns rfc-spawns.tsv --mmaps /path/to/mmaps

# stage a leg with the migration's first row
forever-agent stage-check --forever --mmaps /path/to/mmaps --sql plan_ragefire_chasm.sql ragefire_chasm tunnel
```

- `--forever` needs `--sql FILE` (the exported migration) in all three
  tools, plus `--spawns FILE` (the spawn list TSV) in plan-probe and
  trash-check. stage-check reads no spawn list: it stages points, not
  packs.
- The old-server inputs are refused in `--forever` mode: the plan-JSON
  path (plan-probe rebuilds rows through the v6 cutter; trash-check
  rebuilds through the run-leader exporter), and rows rebuilt against one
  tile set prove nothing about the other.
- A missing v16 tile set, a missing spawn list, or a plan the migration
  does not hold are usage errors naming what to pass.

## Tests

- `cmd/forever-agent/plan_probe_forever_test.go`: a small one-cell plan's
  export matched against a spawn list (clean), one unit's spawn removed
  (MISSING naming the entry, exit 20), the JSON shape (segments present,
  consistent with Counts, decoding as tb-plan-probe), a first add 25 yd
  off the boss matching with the boss radius plus slack, the `--sql` /
  `--spawns` requirements, and the old mode unchanged.
- `cmd/forever-agent/trash_check_forever_test.go`: a pack on the route is
  a danger (exit 0, `--fail-on danger` exits 21), the input requirements,
  and the old mode unchanged.
- `cmd/forever-agent/stage_check_forever_test.go`: leg two stages with
  the migration's first row (`rows` source), the `--sql` requirement, the
  cross-mode rejections (legacy `--sql`/`--plan` need `--forever`;
  `--server-steps` needs the old-server run, where it picks the exporter
  option set, since the migration fixes the exported rows), missing and
  wrong-generation `--mmaps` as usage errors naming the flag, and the old
  usage line unchanged.
- `internal/navmesh/navfixture/navfixture.go` (`tileCoord`): the Detour
  tile index floors (calcTileLoc); truncation toward zero misfiled every
  negative coordinate one tile off, and the engine never found the
  polygon. Found through the probe tests (every real dungeon plan walks
  negative coordinates); the navmesh package tests still pass.
- The run-leader export's own golden tests are unchanged. `plan-check`
  without `--forever` takes the same code path as before.

## Limits

- The fixture tiles never share a corridor across Detour grid cells (no
  portals between fixture tiles): a test walk must fit inside one
  533-yd cell. Cross-cell walks come back partial for want of a portal,
  not for want of mesh.
- The spawn list carries no spawn id on a creature GUID, no template,
  group or link rows and no waypoints: everything matches by entry plus
  position, packs walk alone, and patrols need a recording.
