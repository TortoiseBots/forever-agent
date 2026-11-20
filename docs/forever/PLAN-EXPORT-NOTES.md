# Forever plan export (plan-export --forever)

`plan-export --forever` writes the rows a server module that runs dungeon plans reads: `ai_dungeon_plan`,
`ai_dungeon_leg`, `ai_dungeon_step`, `ai_dungeon_step_unit` and `ai_dungeon_boss`. It is a second mode of the existing
exporter (the run-leader export is unchanged). The plan file stays the single source; the rows are generated.

## Command

```sh
# one migration, named plan_<plan>.sql
forever-agent plan-export --forever --dir dungeons --out forever/sql/world/ ragefire_chasm

# the field table of a plan, or of every plan file in the folder
forever-agent plan-export --forever --report ragefire_chasm
forever-agent plan-export --forever --report --dir dungeons

# navmesh cuts from the server's v16 tiles (no --mmaps: the plan's own points are written)
forever-agent plan-export --forever --mmaps /path/to/mmaps --out forever/sql/world/ ragefire_chasm

# plan-check against the same tiles and a spawn list (guid entry x y z movetype [kind])
forever-agent plan-check --forever --mmaps /path/to/mmaps --spawns rfc-spawns.tsv ragefire_chasm
```

- `--out DIR` writes one file per plan, named `plan_<plan>.sql`. The name carries no date, so the same plan and
  options always write the same name and the same bytes; `--stamp` is the run-leader export's and is refused here.
  `--out FILE` takes exactly one plan.
- `--report` writes nothing. It prints one record per plan:
  `plan=<name> map=<n> status=exported|refused legs= steps= pulls= moves= bosses= units= boss_rows= why=` and one
  `dropped` or `refused` line per field. A plan that does not load, is a draft, or is refused is a record too.
- `--force`, `--rev`, `--skip-legs`, `--with-optional`, `--with-legs`, `--floor-aware` and `--approach-hops` mean
  what they mean in the run-leader export.
- Without `--mmaps` the export still writes, and says so: long hops stay in the plan as written. With `--mmaps` the
  v16 tiles are read; a plan's avoid boxes mark the tiles, and the rows keep the party's own path out of them.

## What the rows say

- A pack is its creature entries and its member positions. No row carries a spawn id.
- `ai_dungeon_step_unit` has one row per pull-group member (`role=pack`, `idx` 0, 1, ...), one per boss
  (`role=boss`, `idx` 0), one per `boss.first` add (`role=first`) and one per patrol entry (`role=patrol`, at the
  patrol's first path point). Its key is `(plan, leg, seq, idx)`; a patrol's `idx` follows the pack rows of the same
  step.
- `role=first` marks a `boss.first` add: the plan gives it no position of its own, so its row stands at the boss's
  point and the reader must search wider there (the add's spawn may be several yards off). `role=patrol` marks a
  patrol unit whose row stands at the first path point: the reader searches along the patrol's path, not an exact
  point.
- A creature entry with no spawn on the map is exported like any other member. The rows do not mark it optional;
  the reader decides whether a unit it cannot find is optional.
- `ai_dungeon_boss` gets one row per boss (map, entry). The plan does not name a boss, so `name` is empty; map and
  entry are the row. The table is keyed by `(map, entry)` and is shared by every plan on the map and by the server
  module's own rows, so the export never deletes from it.
- Idempotent and safe on a shrinking plan: one `START TRANSACTION;` ... `COMMIT;` per plan deletes the plan's
  `ai_dungeon_step_unit`, `ai_dungeon_step`, `ai_dungeon_leg` and `ai_dungeon_plan` rows child first, then inserts
  the plan row, its legs, steps and units parent first. Every delete names the plan (``WHERE `plan` = '<plan>'``), so
  it cannot reach another plan's rows.
- The boss rows are an upsert on the table's own key:

  ```sql
  INSERT INTO `ai_dungeon_boss` (`map`, `entry`, `name`) VALUES (...)
  ON DUPLICATE KEY UPDATE `name` = IF(VALUES(`name`) <> '', VALUES(`name`), `name`);
  ```

  A missing `(map, entry)` is inserted; an existing row keeps its display name unless the plan supplies a non-empty
  one. Nothing else in the file touches `ai_dungeon_boss`, so two plans on the same map keep both plans' boss rows
  in either order, and a shrinking plan removes none of them.
- Deterministic: the same plan and options give the same bytes. Float columns use fixed point with three decimals,
  no exponent form and no negative zero (`-0.000` is written `0.000`); no locale separator. Strings are SQL
  literals with backslash, quote, newline, carriage return, tab and NUL escaped, so they read back unchanged.

## Ragefire Chasm

`dungeons/ragefire_chasm.json`, map 389, without `--mmaps`:

| Item | Count |
| --- | --- |
| Legs | 9 |
| Steps | 74 (19 move, 51 pull, 4 boss) |
| Pull groups | 51 |
| Pack unit rows | 93 |
| Boss unit rows | 4 |
| `boss.first` unit rows | 0 |
| Patrol unit rows | 6 |
| Unit rows | 103 |
| Boss rows | 4 |

The golden file `internal/planexport/testdata/forever/ragefire_chasm.sql` is this export. With `--mmaps` the ten hops
over 60 yd gain move rows, so the step count is higher.

### Field table

| Field | What happens |
| --- | --- |
| `version`, `name`, `map`, `kind` | carried |
| `status` | carried in the plan row's comment |
| `cc_priority`, `cc_always`, `kill_first` | carried in the plan row; the first run makes no crowd-control orders from them |
| `legs[].name`, `legs[].to` | carried |
| `legs[].via[].direct`, `exact` | carried (move / move_direct rows) |
| `legs[].pull_groups[].entries`, `positions` | carried; the members are the unit rows |
| `legs[].pull_groups[].radius`, `approach`, `team`, `ambush`, `stand_pull`, `clear_yd`, `max_bodies` | carried (row fields and flags) |
| `legs[].pull_groups[].patrol` | carried as `role=patrol` unit rows at the first path point |
| `legs[].boss.entry`, `position`, `first` | carried (unit rows for the boss and its adds; an add is `role=first` at the boss's point) |
| `legs[].boss.approach`, `team`, `radius`, `wait_s`, `stand_pull`, `skip_unreachable` | carried (move rows, flags, row fields) |
| `legs[].leave` | carried in the leg row's comment; the module does not read it |
| `entrance`, `entrance_trigger`, `entrance_tele`, `outside_tele`, `portal` | dropped: the module starts the party inside the instance |
| `level_band`, `tank_min_hp`, `hire_composition` | dropped: the party is the module's own |
| `fleers` | dropped: the bots' own combat AI handles a fleeing mob |
| `notes`, `legs[].notes` | dropped: for the reader only |
| `avoid` | dropped from the rows, one report line per box naming the plan points it holds; with `--mmaps` it shapes them |
| `legs[].skip` | dropped: no skip column; the report line names the leg and its entries, and the module finds units by entry and position |
| `legs[].pull_groups[].patrol.path`, `period_s`, `source` | dropped: only the first path point is a unit row |
| `legs[].boss.tactics` | dropped: no tactic rows; the module's own boss handling applies |
| `legs[].needs_walk`, `pull_groups[].needs_walk`, `boss.needs_walk` | dropped: the point is written as given |
| `legs[].notes`, `legs[].rest`, `pull_groups[].equiv`, `no_cc`, `summoned_kill_first`, `pull_ceiling`, `watchdog` | dropped with a note |
| steps, holds, loot, items, gossip, area triggers, phase entries, GM crosses, leftovers | refused by field name (see below) |
| `legs[].steps[].use_object.alt_entries` (and `loot_object`, `wait_for.gameobject`) | carried by `plan-check --forever`: the object lint reads the checked server's entry; the rows carry no objects, so the export ignores it |

Refused fields: `kind` (a raid plan), `raid`, `legs[].steps[].use_object`, `wait_for`, `loot_object`, `use_item`,
`ritual`, `gossip`, `area_trigger`, the same under `after_boss`, `legs[].pull_groups[].hold`, `legs[].boss.hold`,
`legs[].boss.phase_entries`, `legs[].boss.loot_item`, `legs[].boss.summon_open_step`, `legs[].leftover_entries` and
`legs[].via[].gm_cross`. A refused plan writes no file; every refused field is named on stderr (or in the report).

## Tests

- `internal/planexport/forever_test.go`: the RFC golden file, determinism over repeated builds, the unit rows rebuilt
  from the plan, each refusal, string escaping (quote, backslash, newline, non-ASCII), fixed three-decimal numbers
  with no exponent and no negative zero, a large export followed by a smaller one of the same plan (the second
  removes the first's plan rows and no boss row), two plans on one map applied in both orders (both plans' boss rows
  survive), an empty boss name leaving an existing display name alone, and a round trip through the package's SQL
  reader that understands the upsert.
- `cmd/forever-agent/plan_export_forever_test.go`: the command's export, its report, its refusal, the file name, and
  the `--force` rule.
- `internal/worldspawn/foreverspawns_test.go` and `foreverspawns_objects_test.go`: the spawn-list TSV `plan-check --forever --spawns` reads (six columns by default; a seventh `kind` column marks game-object rows: `creature`, or `object` for a door, lever or chest).
- `internal/planlint/objects_test.go`: the `objects` lint. Every `open`, `use_on`, `open_proof`, `loot` and `wait_object` row must name an object spawn of its entry (the plan's `alt_entries` value for the checked server) within the step's search radius: a miss is an error, like a missing creature. No objects in the list fails the rows instead of skipping: a creature-only spawn file must not approve a plan with doors unchecked. A nil world (no list at all) still skips.
- `cmd/forever-agent/plan_check_forever_test.go`: the wiring on the real plans. Deadmines passes on the facts' eight object spawns (16400 at Sneed's door, 16399 at Gilnid's) and fails naming those entries when their rows are missing; Wailing Caverns (no object step) passes. Deadmines against a creature-only spawn file fails every object-backed step at the objects-lint level (the CheckObjects call lintRows makes); Wailing Caverns passes the same file.
- `internal/dungeon/plan_test.go` (`TestParseAltEntries`): an object ref's `alt_entries` (server name to entry) is carried, round-trips, and must differ from the base entry.
- The run-leader export's own golden tests are unchanged and byte-identical. `plan-check` without `--forever` takes
  the same code path as before (the v16 planner and the TSV list are behind the flag); its outputs are unchanged.

## Limits

- The format has no rows for doors, objects, waits, loot, items, gossip, area triggers, holds, tactics, or a raid
  library. Those fields are refused by name rather than silently lost.
- `skip` and `avoid` have no representation of their own; the report says they were dropped, names the leg and
  entries of a `skip`, and the plan points an `avoid` box holds. The rows therefore leave a skipped friendly alone
  only because the module searches by entry and position, not because they carry the list.
- Patrol paths beyond the first point and a boss's first-add positions are not in the plan's units; the module finds
  them by entry and position.
