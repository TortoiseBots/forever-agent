#!/usr/bin/env bash
# Export one map's spawns, formations, waypoint paths, game objects and entrances from a ForeverCore world database to the
# TSV files dungeongen and internal/worldspawn read:   export-forever.sh <map> <outdir>
# With --all it exports every map that has a normal spawn, one folder each:   export-forever.sh --all <rootdir>
# writes <rootdir>/map<N> for every map N (the layout the plan tools take as --spawns: a world root holding map<N>
# folders, or one map's folder). A failing map stops the run and names it; the maps before it stay exported.
# It writes <outdir>/creatures.tsv, groups.tsv, movement.tsv, movement_template.tsv, gameobjects.tsv, entrances.tsv and
# portals.tsv, with the column sets tools/dungeongen/export.sh writes except that the formations column is group_ai (the
# core's raw creature_formations.groupAI) rather than the reader's group_flags; worldspawn.LoadForeverDB reads them.
#
# The tables are the ForeverCore schema of server revision 25fc7ca (sql/base/dev/world_database.sql): creature.guid is the
# spawn id, creature.wander_distance and creature.MovementType the movement, creature_template.Classification the rank.
# The level is the one the core applies (Creature::ApplyLevelScaling, Creature.cpp:3244-3265): a
# creature_classic_level_spawn row for the guid first, then creature_classic_level for the entry, then the
# creature_template_difficulty.ContentTuningID row of hotfixes.content_tuning, and an empty cell when none exists.
# creature_formations gives the groups (groupAI is the core's GroupAIFlags, CreatureGroups.h:28-31, not worldspawn's
# option set: LoadForeverDB maps it), creature_addon.PathId -> waypoint_path_node the waypoint loops, pool_members
# (type 0) the pool, and areatrigger_teleport.PortLocID -> world_safe_locs the entrance position.
#
# Only normal-difficulty rows are exported: a spawn whose creature.spawnDifficulties does not list 0 is a heroic/other
# mode copy of the same spawn. Phases are not filtered: every PhaseId/PhaseGroup row of the map is exported as a
# normal spawn, and the loader notes that a spawn behind another phase may not be up at the same time as a plan assumes.
#
# ForeverCore has no creature_linking, creature_movement, script_waypoint or areatrigger position columns: links.tsv and
# link_templates.tsv are not written (LoadForeverDB notes their absence) and the trigger's own position is in the
# client's AreaTrigger.db2, so portals.tsv carries only its header.
#
# The world database is the one in the docker stack; SELECTs only: the session is opened with
# SET SESSION TRANSACTION READ ONLY and every statement below is a SELECT. No credential is read, printed or stored here:
# the password is expanded inside the container from the container's own environment (MARIADB_ROOT_PASSWORD).
# The files are written into a sibling temporary directory and moved into <outdir> only after every query finished and
# every file exists, so a failed run leaves the previous export untouched.
#
# Environment:  DUNGEONGEN_HOST           ssh host that runs docker (default local: run docker here)
#               DUNGEONGEN_CONTAINER      database container (default forever-db)
#               DUNGEONGEN_DB             world database (default forever_world)
#               DUNGEONGEN_HOTFIXES_DB    hotfixes database (default forever_hotfixes)
set -euo pipefail
[ $# -eq 2 ] || { sed -n '2,7p' "$0"; exit 2; }
all=0 map=
if [ "$1" = --all ]; then
  all=1
else
  map=$1
  [[ $map =~ ^[0-9]+$ ]] || { echo "map must be a number" >&2; exit 2; }
fi
out=$2
host=${DUNGEONGEN_HOST:-local} ctr=${DUNGEONGEN_CONTAINER:-forever-db} db=${DUNGEONGEN_DB:-forever_world}
hotdb=${DUNGEONGEN_HOTFIXES_DB:-forever_hotfixes}

# Every value below is interpolated into a shell command line and a SQL string inside the container. Refuse anything
# outside the name-safe set: a value with ';', a quote, a space or '$' would run a second command or end the SQL.
valid_env() {
  [[ $2 =~ ^[A-Za-z0-9_.@:-]+$ ]] || { echo "$1 must match [A-Za-z0-9_.@:-]+" >&2; exit 2; }
}
valid_env DUNGEONGEN_HOST "$host"
valid_env DUNGEONGEN_CONTAINER "$ctr"
valid_env DUNGEONGEN_DB "$db"
valid_env DUNGEONGEN_HOTFIXES_DB "$hotdb"

query() { # reads one SELECT on stdin, prints tab-separated rows without a header
  local remote="docker exec -i $ctr sh -c 'MYSQL_PWD=\"\$MARIADB_ROOT_PASSWORD\" exec mariadb -uroot -N -B --init-command=\"SET SESSION TRANSACTION READ ONLY\" $db'"
  if [ "$host" = local ]; then bash -c "$remote"; else ssh -o LogLevel=ERROR "$host" "$remote"; fi
}

# --all: one export per map that has a normal spawn (the same normal-difficulty filter the per-map export uses). Each map
# runs this script again for its own folder, so a map is staged and published whole, exactly as a single export is.
if [ "$all" = 1 ]; then
  maps=$(query <<SQL
SELECT DISTINCT c.map FROM creature c WHERE FIND_IN_SET('0', REPLACE(c.spawnDifficulties, ' ', '')) > 0 ORDER BY c.map;
SQL
)
  [ -n "$maps" ] || { echo "export found no map with spawns" >&2; exit 1; }
  mkdir -p "$out"
  for m in $maps; do
    [[ $m =~ ^[0-9]+$ ]] || { echo "the database returned a map id that is not a number: $m" >&2; exit 1; }
    bash "$0" "$m" "$out/map$m" || { echo "export of map $m failed: the maps before it are exported, map $m and the rest are not" >&2; exit 1; }
  done
  exit 0
fi

tmp="$out.tmp.$$"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp"

# normal is the row filter for the normal difficulty: spawnDifficulties is a comma list of difficulty ids and 0 is the
# one a normal group sees (ObjectMgr::ParseSpawnDifficulties, ObjectMgr.cpp:2464-2482). REPLACE drops spaces so the
# list matches FIND_IN_SET.
normal="FIND_IN_SET('0', REPLACE(c.spawnDifficulties, ' ', '')) > 0"

# creatures.tsv: one row per normal spawn of the map. level_min and level_max are the core's effective range (classic
# spawn row, then classic entry row, then content tuning) and empty when the core has no row for the spawn:
# LoadForeverDB then assumes level 60 and says so. A spawn whose creature_template row is missing is still exported with
# header defaults and counted at the end. ForeverCore has no template detection_range (Creature::GetAttackDistance is
# level based, Creature.cpp:2257), so that column is empty too and the reader defaults it to 20 yd. flags_extra keeps
# only the not-aggro bit (CREATURE_FLAG_EXTRA_CIVILIAN 0x2, CreatureData.h:345): the other bits are not the 1.12
# NO_AGGRO/NO_ASSIST set worldspawn reads. group_ai is the raw creature_formations.groupAI (GroupAIFlags), which
# LoadForeverDB maps; it is not worldspawn's option bits. patrol is a creature_addon.PathId, pool is the
# pool_members.poolSpawnId the core loads (ObjectMgr.cpp:2492).
{
  printf 'guid\tentry\tname\tlevel_min\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\tpatrol\tauras\tdetection_range\tflags_extra\tspeed_walk\tgroup_ai\tpool\n'
  query <<SQL
SELECT c.guid, c.id, COALESCE(t.name, ''),
       CASE WHEN sp.level_min IS NOT NULL THEN sp.level_min
            WHEN cl.level_min IS NOT NULL THEN cl.level_min
            WHEN ct.ID IS NOT NULL THEN ct.MinLevel ELSE '' END,
       CASE WHEN sp.level_max IS NOT NULL THEN sp.level_max
            WHEN cl.level_max IS NOT NULL THEN cl.level_max
            WHEN ct.ID IS NOT NULL THEN ct.MaxLevel ELSE '' END,
       COALESCE(t.Classification, 0), COALESCE(t.faction, 0), COALESCE(c.npcflag, t.npcflag, 0),
       c.position_x, c.position_y, c.position_z, c.wander_distance, c.MovementType,
       COALESCE((SELECT f.leaderGUID FROM creature_formations f WHERE f.memberGUID = c.guid LIMIT 1), 0),
       IF(EXISTS(SELECT 1 FROM creature_addon a WHERE a.guid = c.guid AND a.PathId <> 0), 1, 0),
       COALESCE(CONCAT_WS(' ', NULLIF(a.auras, ''), NULLIF(ta.auras, '')), ''),
       '', IF((COALESCE(t.flags_extra, 0) & 2) <> 0, 2, 0), COALESCE(t.speed_walk, 1),
       COALESCE((SELECT g.groupAI FROM creature_formations g WHERE g.memberGUID = c.guid LIMIT 1), 0),
       COALESCE((SELECT p.poolSpawnId FROM pool_members p WHERE p.type = 0 AND p.spawnId = c.guid LIMIT 1), 0)
FROM creature c
LEFT JOIN creature_template t ON t.entry = c.id
LEFT JOIN creature_classic_level_spawn sp ON sp.guid = c.guid
LEFT JOIN creature_classic_level cl ON cl.entry = c.id
LEFT JOIN creature_addon a ON a.guid = c.guid
LEFT JOIN creature_template_addon ta ON ta.entry = c.id
LEFT JOIN creature_template_difficulty d ON d.Entry = c.id AND d.DifficultyID = 0
LEFT JOIN (SELECT ID, MAX(VerifiedBuild) AS VerifiedBuild FROM $hotdb.content_tuning GROUP BY ID) ctb
       ON ctb.ID = d.ContentTuningID AND d.ContentTuningID <> 0
LEFT JOIN $hotdb.content_tuning ct ON ct.ID = ctb.ID AND ct.VerifiedBuild = ctb.VerifiedBuild
WHERE c.map = $map AND $normal ORDER BY c.guid;
SQL
} > "$tmp/creatures.tsv"

# Spawns whose creature_template row is missing: the core does not load them, so the summary names the count. They are
# exported with defaults so the row count matches creature's and the loader sees every guid of the map.
orphans=$(query <<SQL
SELECT COUNT(*) FROM creature c LEFT JOIN creature_template t ON t.entry = c.id
WHERE c.map = $map AND $normal AND t.entry IS NULL;
SQL
)

# Creature formations: the map's members with the raw groupAI the core keeps per member row (CreatureGroups.h:28-31).
# The leader has a row of its own (leaderGUID = memberGUID) with no meaningful dist/angle; LoadForeverDB reads that row
# as the marker that the spawn leads and maps the bits.
{
  printf 'leader_guid\tmember_guid\tdist\tangle\tgroup_ai\n'
  query <<SQL
SELECT f.leaderGUID, f.memberGUID, f.dist, f.angle, f.groupAI
FROM creature_formations f JOIN creature c ON c.guid = f.memberGUID
WHERE c.map = $map AND $normal ORDER BY f.leaderGUID, f.memberGUID;
SQL
} > "$tmp/groups.tsv"

# The waypoint loops of the map's patrolling spawns (MovementType 2): creature_addon.PathId names the waypoint_path row.
# Delay is milliseconds (WaypointManager.cpp:142), which is what the reader's waittime column is.
{
  printf 'guid\tpoint\tx\ty\tz\twaittime\n'
  query <<SQL
SELECT c.guid, n.NodeId, n.PositionX, n.PositionY, n.PositionZ, n.Delay
FROM creature c
JOIN creature_addon a ON a.guid = c.guid AND a.PathId <> 0
JOIN waypoint_path_node n ON n.PathId = a.PathId
WHERE c.map = $map AND $normal AND c.MovementType = 2 ORDER BY c.guid, n.NodeId;
SQL
} > "$tmp/movement.tsv"

# Template paths for the entries whose spawns on this map patrol: creature_template_addon.PathId names one path for the
# whole entry (the creature_movement_template rows of the 1.12 export).
{
  printf 'entry\tpoint\tx\ty\tz\twaittime\n'
  query <<SQL
SELECT DISTINCT c.id, n.NodeId, n.PositionX, n.PositionY, n.PositionZ, n.Delay
FROM creature c
JOIN creature_template_addon ta ON ta.entry = c.id AND ta.PathId <> 0
JOIN waypoint_path_node n ON n.PathId = ta.PathId
WHERE c.map = $map AND $normal AND c.MovementType = 2 ORDER BY c.id, n.NodeId;
SQL
} > "$tmp/movement_template.tsv"

# Game objects of the map. ForeverCore's gameobject table has no flags column (the 1.12 one did): the column is 0 and the
# state is gameobject.state.
{
  printf 'guid\tentry\tname\ttype\tflags\tx\ty\tz\tstate\n'
  query <<SQL
SELECT g.guid, g.id, t.name, t.type, 0, g.position_x, g.position_y, g.position_z, g.state
FROM gameobject g JOIN gameobject_template t ON t.entry = g.id
WHERE g.map = $map AND FIND_IN_SET('0', REPLACE(g.spawnDifficulties, ' ', '')) > 0 ORDER BY g.guid;
SQL
} > "$tmp/gameobjects.tsv"

# Areatriggers whose destination is this map (the entrance position): areatrigger_teleport.PortLocID names the destination
# row in world_safe_locs (AreaTriggerTeleportDialog.h).
{
  printf 'id\tname\ttarget_map\tx\ty\tz\to\n'
  query <<SQL
SELECT at.ID, COALESCE(at.Name, ''), wsl.MapID, wsl.LocX, wsl.LocY, wsl.LocZ, wsl.Facing
FROM areatrigger_teleport at JOIN world_safe_locs wsl ON wsl.ID = at.PortLocID
WHERE wsl.MapID = $map ORDER BY at.ID;
SQL
} > "$tmp/entrances.tsv"

# The trigger's own position on the outer map is AreaTrigger.db2 (client data), not a server table of this revision: the
# header is written so a spec plan that names a trigger reads a file, and the plan gets its entrance from entrances.tsv.
{
  printf 'target_map\tid\tname\trequired_level\touter_map\tx\ty\tz\tradius\tbox_x\tbox_y\tbox_z\ttarget_x\ttarget_y\ttarget_z\n'
} > "$tmp/portals.tsv"

# Every query finished: publish the generation. Each file replaces its old copy with one rename, so a run that stopped
# at any point above leaves <outdir> as it was.
mkdir -p "$out"
for f in creatures.tsv groups.tsv movement.tsv movement_template.tsv gameobjects.tsv entrances.tsv portals.tsv; do
  [ -s "$tmp/$f" ] || { echo "export wrote no $f" >&2; exit 1; }
  mv -f "$tmp/$f" "$out/$f"
done
wc -l "$out"/*.tsv
echo "spawns with no creature_template row: $orphans (exported with header defaults; the core does not load them)"
