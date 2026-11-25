# ForeverCore world DB export and loader (round L3)

`tools/dungeongen/export-forever.sh` writes one map's spawn export from a ForeverCore world database, and
`worldspawn.LoadForeverDB` reads it into the same `worldspawn.World` the 1.12 export (`tools/dungeongen/export.sh`)
produces. The reader feeds the offline plan tools and plan-scout; nothing here touches a server.

## Command

```sh
# on a machine that can reach the database container: one map, one folder
tools/dungeongen/export-forever.sh 36 forever/map36

# every map with a spawn, each at its own difficulty (an instance's lowest nonzero spawn id, 0 for open world): forever/world/map<N>
tools/dungeongen/export-forever.sh --all forever/world

# read it back (map id; the folder's files carry no map)
forever-agent plan-check --forever --spawns forever/world ...
forever-agent plan-probe --forever --spawns forever/world ...
forever-agent trash-check --forever --spawns forever/world ...
```

`--spawns` of the three plan tools is a world root (`map<N>/creatures.tsv`, the plan's map picks the folder), one map's
folder (`creatures.tsv` in it; taken as the plan's map, the files carry no map id) or the thin TSV spawn list a server
module prints (guid entry x y z movetype [kind]; no groups, levels or waypoint loops). The export is Blizzard game data:
it is never committed here; tests use small hand-made folders. `trash-check --forever` takes the family assist ring from
the port (`kAggroFamilyAssistMinYd`, 20 yd, `PullAggroEstimatePolicy.h`) unless `--assist-yd` is given; its 3 yd party /
10 yd raid spread and `aggroest`'s ring, hop and floor numbers already match the port.

The script is SELECT-only: the session is opened with `SET SESSION TRANSACTION READ ONLY` (in the client command line
that `query()` runs) and every statement is a SELECT. It reads no credential: the database password is expanded inside
the container from its own environment (`MYSQL_ROOT_PASSWORD` of a mysql image, `MARIADB_ROOT_PASSWORD` of a mariadb
one), as `export.sh` does, and the client is whichever of `mysql` or `mariadb` the image has. `DUNGEONGEN_HOST`,
`DUNGEONGEN_CONTAINER`, `DUNGEONGEN_DB` and `DUNGEONGEN_HOTFIXES_DB` name the docker host, the container, the world
database and the hotfixes database, and `DUNGEONGEN_DIFFICULTY` the id to export (default 1, `DIFFICULTY_NORMAL`).
Without it `--all` gives each map its own; a single raid map is exported with 9 (classic 40-man) or 148
(Zul'Gurub/AQ20) given explicitly. Each name value must match `[A-Za-z0-9_.@:-]+` before it is interpolated into the
remote shell command and the SQL string; anything else (`;`, a quote, `$`, a space) is refused with exit code 2, and
the difficulty must be a number.

The files are written into a sibling staging directory (`<out>.tmp.<pid>`), and each one is moved into `<out>` only
after every query finished and every file exists. A failing query, ssh connection or container leaves the previous
export untouched instead of a mixed generation. The staging directory is removed on exit.

## Shared plan files

A plan file serves both stacks. A Forever check never deletes a Turtle spawn, leg or step: where ForeverCore lacks
content a plan uses (Deadmines' Defias Companions; Shadowfang Keep's Sever/Prelate Ironmane/Mistblade Rippers;
Blackfathom Deeps' Velthelaxx wing; Razorfen Kraul's Bramblehide Rootshaper, Gnarled Bramblehide and Rotthorn;
Scarlet Graveyard's Scarlet Jailer pack, Duke Dreadmoore and Scorn; Scarlet Library's Brother Wystan; Scarlet
Armory's Quartermaster Daghelm; Scarlet Cathedral's Mograine hold, which the module has no hold row for; Razorfen
Downs' Death's Head Worshipper, Servitor and Battleguard packs and Death Prophet Rakameg; Zul'Farrak's Zel'jeb the
Ancient wing, the Farraki Arena, Gahz'rilla, Sezz'ziz and Nekrum Gutchewer; Blackrock Depths' Shan-xi Watershaper;
Scholomance's Lord Blackwood), the plan
keeps it, `forever_status` says `refused` with the missing content named,
and `alt_entries` is the only per-server field (object entries at one position). The plans that can be cleared for
Forever say `exported`; a plan whose Forever data does not match says `refused` with the field or spawns named
(Uldaman's item, ritual and hold rows, Sunken Temple's unit wait and hold, Maraudon Purple's spawn positions and one
segment, and the batch-4 spawn positions, holds, unit waits and gossip are the examples).

## What it writes

The tables are the ForeverCore schema at server revision `25fc7ca` (`sql/base/dev/world_database.sql`,
`sql/base/dev/hotfixes_database.sql`, plus the `sql/custom/world` classic tables).

Only **the map's own difficulty** rows are exported: `creature.spawnDifficulties` is a comma list of difficulty ids,
a map loads a spawn only when its list holds the map's difficulty (`Map.cpp:2433`), and 1 is `DIFFICULTY_NORMAL`
(`DBCEnums.h:938`; 0 is `DIFFICULTY_NONE`, which an open-world map carries), so every creature query carries
`FIND_IN_SET('$diff', ...) > 0` with `DUNGEONGEN_DIFFICULTY` defaulting to 1. Without it, `--all` resolves each map
from `instance_template`: an open-world map (no row) is 0 even with a stray nonzero spawn row, and an instance uses its
lowest nonzero spawn difficulty (1 for a normal dungeon, 9 for a classic 40-man raid, 148 for Zul'Gurub/AQ20). An
instance whose rows are all 0 stops the run before the root is swapped. So a root refresh keeps open-world and raid
maps instead of omitting them. Copies for another mode are not exported.

**Phases are not filtered.** Every `PhaseId`/`PhaseGroup` row of the map is exported as a normal spawn. A spawn behind
another phase may therefore not be up at the same time as a plan assumes; `LoadForeverDB` notes it, and a live
`plan-scout` read remains the check.

### creatures.tsv (the 21 columns of the 1.12 export, with `group_ai` in place of `group_flags`)

| TSV column | ForeverCore source | Note |
|---|---|---|
| `guid` | `creature.guid` | the spawn id (`world_database.sql:594`) |
| `entry` | `creature.id` | |
| `name` | `creature_template.name` | `:961`; empty when the template row is missing |
| `level_min`, `level_max` | `creature_classic_level_spawn` (guid), else `creature_classic_level` (entry), else `hotfixes.content_tuning.MinLevel/MaxLevel` of the map difficulty's `creature_template_difficulty` row (`DUNGEONGEN_DIFFICULTY`, default 1; the difficulty-0 row is the fallback only when the id is 1) | the order `Creature::ApplyLevelScaling` applies (`Creature.cpp:3244-3265`); empty when the core has no level for the spawn. `LoadForeverDB` then assumes 60 for such a spawn and adds one note counting them. A literal 0 or negative level is an export error (`worldspawn.ErrLevelZero`), not "unknown" |
| `rank` | `creature_template.Classification` | the same 0 normal / 1 elite / 2 rare elite / 3 boss / 4 rare order as the 1.12 rank column |
| `faction` | `creature_template.faction` | a modern faction template id; the reader only compares it (35 = friendly) and carries it as a number |
| `npc_flags` | `COALESCE(creature.npcflag, creature_template.npcflag, 0)` | the spawn's flag when set, else the template's (`ObjectMgr.cpp:1985`) |
| `x`, `y`, `z` | `creature.position_x/y/z` | |
| `wander` | `creature.wander_distance` | |
| `movement_type` | `creature.MovementType` | 2 = waypoint |
| `group_leader` | `creature_formations.leaderGUID` of the spawn's member row, else 0 | `:705` |
| `patrol` | `creature_addon.PathId <> 0` | `:636` |
| `auras` | `creature_addon.auras` then `creature_template_addon.auras`, space separated | |
| `detection_range` | empty | ForeverCore has no template detection range: `Creature::GetAttackDistance` is level based (`Creature.cpp:2257`). The reader's 20 yd default stands |
| `flags_extra` | only bit `0x2` (`CREATURE_FLAG_EXTRA_CIVILIAN`, `CreatureData.h:345`) | the modern bits are not the 1.12 `NO_AGGRO`/`NO_ASSIST` pair `worldspawn` reads (`0x2` keeps its not-aggro meaning; `0x010000` is ignore-feign-death now, not `NO_ASSIST`), so everything else is written 0 |
| `speed_walk` | `creature_template.speed_walk` | |
| `group_ai` | `creature_formations.groupAI` of the member row | the core's raw `GroupAIFlags` (`CreatureGroups.h:28-31`), mapped by `LoadForeverDB`; see below |
| `pool` | `pool_members.poolSpawnId` (`type = 0`, `spawnId = creature.guid`) | the pool id the core loads (`ObjectMgr.cpp:2492`, `:3008`) |

`creature_template` is a **LEFT JOIN**: a spawn whose template row is missing is still exported, with header defaults
(name empty, rank 0, faction 0, flags 0, speed 1), because the core does not load it. The export summary ends with
`spawns with no creature_template row: N`. The loader rejects two rows with one guid
(`worldspawn.ErrDuplicateGUID`): `hotfixes.content_tuning` is keyed `(ID, VerifiedBuild)`, so the export joins it
through one row per ID (the newest `VerifiedBuild`), and a duplicate row means a fan-out or a concatenated export.

### The other files

| File | ForeverCore source |
|---|---|
| `groups.tsv` | `creature_formations` joined to the map's `creature` rows: `leaderGUID, memberGUID, dist, angle, groupAI` (`CreatureGroups.cpp:103`). The column is `group_ai`: the core's bits, not the reader's |
| `movement.tsv` | the map's `MovementType = 2` spawns: `creature_addon.PathId -> waypoint_path_node` (`NodeId, PositionX/Y/Z, Delay`) (`WaypointManager.cpp:44`, `:110`, `:142`; Delay is milliseconds) |
| `movement_template.tsv` | the same nodes through `creature_template_addon.PathId`, one set per entry (`SELECT DISTINCT`, because several spawns share the entry) |
| `gameobjects.tsv` | `gameobject` joined to `gameobject_template`: guid, entry, name, type, position, state (`:1672`, `:1812`), the map's difficulty only. `LoadForeverDB` reads it into the object lint's list (guid, entry, position). ForeverCore's `gameobject` has no `flags` column, so that cell is 0 |
| `entrances.tsv` | `areatrigger_teleport.PortLocID -> world_safe_locs`: the destination map and position (`:286`, `:4973`; `ObjectMgr.cpp:7559`, `src/tools/world_editor/app/AreaTriggerTeleportDialog.h`) |
| `portals.tsv` | header only: the trigger's own position is `AreaTrigger.db2` (client data), not a table of this revision (`areatrigger_template` carries no geometry, `:302`) |

Not written, and why: `links.tsv`/`link_templates.tsv` (ForeverCore has no `creature_linking` or
`creature_linking_template` tables), `script_waypoint.tsv` (no `script_waypoint` table), `movement.tsv` rows for
non-waypoint movers (the core only applies a `PathId` to those).

## LoadForeverDB

`LoadForeverDB(dir, mapID)` is `Load` plus these ForeverCore facts:

- **Levels.** The export writes the range the core applies: `creature_classic_level_spawn` for the guid, else
  `creature_classic_level` for the entry, else the map difficulty's content tuning row (`DUNGEONGEN_DIFFICULTY`, default 1; the difficulty-0 row is the fallback only when the id is 1). An empty `level_max` cell is
  the only "no level" encoding: the loader sets such a spawn to level 60 and adds one note counting them (`N of M
  spawns have no level in the core ... level 60 assumed`). A numeric cell that is not positive is refused with
  `worldspawn.ErrLevelZero`, and a `creatures.tsv` with no data rows (a wrong database name or map id writes the header
  only) with `worldspawn.ErrNoSpawns`. A numeric level is kept.
- **Detection.** A spawn with no detection range gets the model's 20 yd default (`defaultDetectionYd`).
- **Formations.** The group pass runs inside `load`, **before** the patrols are derived, so a follower of a patrolling
  leader is in the leader's loop. `creature_formations` stores the leader on a row of its own (`leaderGUID =
  memberGUID`, dist and angle ignored by the core, `CreatureGroups.cpp:143-147`); that row marks the leader, and its
  `groupAI` counts with the members' — the core reads the *engaging* member's own row, so the self row's `0x1` is what
  makes the members assist the leader. A formation whose leader has no row of its own is dropped, as the core drops it
  (`CreatureGroups.cpp:156-171`), and one note counts them.
- **groupAI mapping.** The core's `GroupAIFlags` (`CreatureGroups.h:28-31`) are not worldspawn's option bits:
  `0x1` = members assist the leader, `0x2` = the leader assists a member, `0x3` = both, `0x200` = the members idle in
  formation. The reader's `AggroTogether` (the group wakes as one) is set for either assist bit, and its
  `FormationMove` (the member follows the leader) for `0x200`. The group's flags are that mapping of the OR of every
  row, the self row included. The raw number is never copied: a `0x1` pack is one pull, a `0x3` pack is not a moving
  formation, and a `0x200` group is not aggro together.
- **Game objects.** `gameobjects.tsv` is read into `World.Objects` (guid, entry, position), the list the plan tools'
  object-presence lint (`planlint.CheckObjects`) checks a plan's door, lever and chest steps against, so
  `plan-check --forever --spawns DIR` checks Deadmines' object rows from the export alone. An export without the file
  (an older script) leaves the list empty and adds a note naming the file; the lint then says it cannot check.
- Load's two "an export before the raid columns" link notes are replaced by one that names the Forever reason (no
  `creature_linking` tables). Everything else — formation offsets, packs, aggro radii, patrol periods — is `Load`'s.

## Tests

- `TestForeverGroupAIMapping`: the bit mapping, alone and combined (`0x1`, `0x2`, `0x3`, `0x200`, `0x201`, an unknown
  bit).
- `TestForeverDBObjects`: the fixture's `gameobjects.tsv` loads both objects with their entries and positions; without
  the file the list is empty and one note names it.
- `TestForeverDBFormationSelfRow`: fixture `testdata/map700-forever`; the leader's own row with `0x1` alone (the member
  row is 0) makes the group `AggroTogether` and keeps `[100 101]` one pull, the `0x200` group is `FormationMove` and
  not a pack, and the group note counts both.
- `TestForeverDBFormationWithoutSelfRow`: a member row whose leader has no self row drops the formation and adds a
  note, as the core does.
- `TestForeverDBFollowerJoinsPatrol`: a `0x200` follower joins its patrolling leader's loop (the group pass runs before
  `loadPatrols`).
- `TestForeverDBRejectsDuplicateGUID`, `TestForeverDBRejectsEmptyMap`, `TestForeverDBRejectsLevelZero`: the typed
  errors `ErrDuplicateGUID`, `ErrNoSpawns`, `ErrLevelZero`.
- `TestForeverDBWaypointsViaAddonPath`: the waypoint rows through the addon path become one patrol in node order with
  the `Delay` cells as waits (0, 2500, 0 ms).
- `TestForeverDBLevelNote`: empty level cells become 60 with one note, a numeric cell is kept, detection is 20 yd, the
  not-aggro bit survives, and a 1.12 `Load` still refuses an empty level cell.
- `TestExportScriptSelectsOnly`: the read-only session is in the client command line that `query()` runs (mysql or
  mariadb, whichever the image has, with the image's own root password variable), no
  write verb appears in the script, every heredoc statement starts with SELECT, and every file is staged and published.
- `TestExportScriptOneTuningRowPerID`: the `content_tuning` join goes through `MAX(VerifiedBuild) GROUP BY ID`.
- `TestExportScriptNormalSpawnsAndMissingTemplates`: the normal-difficulty filter, the `LEFT JOIN creature_template`,
  the orphan count in the summary, and the phase note.
- `TestExportScriptRejectsUnsafeEnv`: a `DUNGEONGEN_DB` with `;` exits 2 naming the variable, before docker runs and
  before the output directory exists (stub `docker` on `PATH`).
- `TestExportScriptRejectsABadDifficulty`: a `DUNGEONGEN_DIFFICULTY` that is not a number exits 2 before docker runs.
- `TestExportScriptUsesTheMapDifficultyForTemplateRows`: the level join and the spawn filter use the same difficulty id; the difficulty-0 row is the fallback only for the normal (1) map.
- `TestExportScriptAllWritesEveryMap`, `TestExportScriptAllStopsOnAnInstanceWithoutADifficulty`: `--all` exports an open-world map at 0 even with a stray 1 row and a classic raid at 9, and an instance with no nonzero row stops before the root is swapped.
- `TestExportScriptKeepsPreviousExportOnFailure`: a failing query leaves the previous export's files byte-identical and
  no staging directory (stub `docker` that fails).

## Gates

Run on the clean committed tree `15683f1b` (this section's own commit adds only these lines):

- `make fmt-check`, `make vet`, `make build`, `make nocgo`, `make cross`: pass.
- `staticcheck ./...`: pass.
- `go test -race -count=1 -timeout 20m ./...`: pass.
- `vm-test ta <local worktree>`:
  `RESULT: PASS  kind=ta sha=15683f1b dirty=0 files=2313` (rc=0, vm_duration=2246s).
