# Forever navmesh, round A8 notes

Scope: make `internal/navmesh` read the mmap files a ForeverCore server uses, and give the Forever client a
ground-following path between two points. Offline round: no server, no login. The 1.12 path is unchanged; both
generations sit behind one `navmesh.Planner`, chosen by the caller (`NewPlanner` = MaNGOS v6, `NewForeverPlanner` =
Forever v16), never guessed from the folder.

Checked in ForeverCore at 6517af05 and against the unpacked repack mmaps (3160 tiles, 74 maps; the archive is
`forever-mmaps-repack-0.1.37.4-client-70888.tar.gz`, test data only, never committed).

## The two formats side by side

| | 1.12 ancestor (present in this repo) | ForeverCore (added this round) |
|---|---|---|
| map params file | `NNN.mmap`, raw 28-byte `dtNavMeshParams` (`navdetour.cpp:20-21,122-127`) | `MMMM.mmap`, 40-byte `MmapNavMeshHeader`: magic, version, params, off-mesh count (`MMapDefines.h:29-37`) |
| generator version | 6 | 16 (`MMapDefines.h:27`) |
| magic | `0x4d4d4150` (`navdetour.cpp:20`) | `0x4d4d4150` (`MMapDefines.h:26`) |
| tile file | `NNNYYXX.mmtile` (`planner.go` `tileFile`) | `MMMM_XX_YY.mmtile` (`MMapManager.cpp:33`) |
| tile header | 20 bytes: magic, Detour version, generator version, data size, `usesLiquids` + 3 pad | same 20 bytes and layout (`MMapDefines.h:39-46`); the `usesLiquids` byte is 1 |
| Detour payload | Detour 7, 64-bit refs | Detour 7, 64-bit refs |
| tile grid | file `(map, gy, gx)`, `gx` from y, `gy` from x | same (`Map.cpp:298-301`, `GridDefines.h:181-184`) |
| instance tiles | none: every map owns all tiles | child map's `.mmap`; a missing tile is loaded from the parent map's file with the same grid (`MMapManager.cpp:349-357`) |

### The vendored Detour, corrected in round 1

The round-0 notes said the vendored Detour was byte-identical to ForeverCore's; only the headers had been compared,
and that was wrong. Both sides descend from the same recastnavigation snapshot (TC's ~1.5-era copy: same
3670-line `DetourNavMeshQuery.cpp` shape, same `dtNavMeshQuery::queryPolygons` batching API, TC's
`recastnavigation.diff` applied on both: `DT_POLYREF64` at `DetourNavMesh.h:28`, the fork's 12/21/31 salt/tile/poly
split at `:55-57`, `DT_NAVMESH_VERSION 7` at `:85`), but the core carries four later in-tree edits that the client
did not have. Round 1 diffed every file (Include and Source) and applied the correctness/safety differences:

| file, core line | what the core has | applied |
|---|---|---|
| `DetourNavMesh.cpp:673-677` | the null guard in `closestPointOnDetailEdges`: a polygon whose detail mesh has no qualifying edge leaves `pmin`/`pmax` unset; the old code did `dtVlerp(closest, pmin, pmax, tmin)` and died on a null read | yes, plus the fallback clamps to the polygon's own edges (the core keeps the asked position, which can be yards off the mesh) and `getPolyHeight` initializes `closest` (the core's comment assumes the caller did) |
| `DetourNavMeshQuery.cpp:1097-1106` | never update a CLOSED node's parent: with an inadmissible area cost a cheaper route can reach a closed node and a parent cycle (A→B→A) hangs `getPathToNode` forever | yes |
| `DetourNavMeshQuery.cpp:1153-1163` | `getPathToNode` caps the parent chain at `getMaxNodes()+1` and fails instead of looping | yes |
| `DetourNavMeshQuery.cpp` raycast | on a full visited buffer it sets `DT_BUFFER_TOO_SMALL` and keeps casting; the old copy also had a TC comment and returned early | yes |
| `DetourNavMeshQuery.cpp:2303` | the core *removed* `if (fromPoly->vertCount == 0) return ...` from `getPortalPoints` | NOT applied: the client keeps it. Without it `(link->edge+1) % vertCount` is a division by zero on a corrupt off-mesh poly; round 1's tile validator also refuses `vertCount < 3` for ground and `!= 2` for off-mesh. The core should restore it. |

What the client needs and keeps unchanged: `DetourNavMeshBuilder.h`/`.cpp` are generator-only and not vendored;
nothing else differs in `Include/` (byte-identical). The build is still Detour 7 with 64-bit refs
(`detourBuild = 708`, `nd_version()`), so the same tiles load.

The 1.12 path uses the same sources: its full test suite passes, and the real-data results are unchanged
(`TestRealTiles`: complete, 8 points, 128.6 yd — byte-identical point list; the six `TestServerPath*` verdicts
(Windhorn step 641 running out of nodes; the three Hateforge cases; the two Stormwrought ledge cases) are the same
as on the pre-round-1 sources, run side by side by restoring the old Detour and forcing a cgo rebuild). The 1.12 node pool is still
65,535: see "PathTo and the server's path" below.

### What the plan's paragraph had right and wrong

Right: version 16; the 40-byte header (magic, version, params, off-mesh count); `{:04}.mmap` and
`{:04}_{:02}_{:02}.mmtile` names; 20-byte tile headers; Detour 7 with 64-bit refs; the flag bits (ground 1, steep 2,
water 4, magma/slime 8, road 0x10) differ from the 1.12 set (ground 1, magma 2, slime 4, water 8, steep 0x10). All
of those were checked against the code and the 74-map repack, not just repeated.

Wrong or incomplete: the tile header's last field is `char usesLiquids` + 3 padding bytes (as a word it reads 1,
not a `uint32` flag); the params' `maxTiles` is the map's tile count, `maxPolys` is `1<<31` (the fork's 31 poly bits,
not stock Detour's 21), and `orig[1]` in the repack files is a tiny float (`0x00800000`) Detour never reads for tile
location (only `orig[0]`/`orig[2]` enter `calcTileLoc`); instances do not fall back to the parent's `.mmap`, only to
the parent's tile files, loaded into the instance's own mesh and params — and a parent tile the instance's params
cannot hold is a logged failed add, not a failed load (`MMapManager.cpp:349-425`).

The plan's note that the Ragefire basin floor is flagged `NAV_GROUND` does not hold for this repack: a scan of the
four map-389 tiles finds 551 polygons flagged `NAV_MAGMA_SLIME` (0x08) between x and y extents around the basin at
z about -64.3 (the walk filter excludes them; the plan's avoid boxes remain useful for shallower lava).

## Flags and areas

Forever's `NavArea` values and the flag formula `1 << (NAV_AREA_MAX_VALUE - area)` (`MMapDefines.h:58-80`, generator
at `TileBuilder.cpp:626-635`):

| class | Forever area | Forever flag | 1.12 flag |
|---|---|---|---|
| ground | 11 | 0x01 | 0x01 |
| steep | 10 | 0x02 | 0x10 |
| water | 9 | 0x04 | 0x08 |
| magma or slime (same class) | 8 | 0x08 | magma 0x02, slime 0x04 |
| road | 7 | 0x10 | none |

`navmesh.Format.Terrain()` returns these bits and `Format.AreaTable()` the Forever area ids; the planner builds
every filter from that table (`Format.WalkFilter`, `GroundFilter`, `ServerPlayerFilter`), and `LiquidAt` asks the
planner for its water bit (`WaterFlagger`). The 1.12 generator's area numbering is not in this repository's sources,
so `AreaTable` returns nil for it rather than a guess; its filters read the flags only.

### Round 1: the default filters, and why Road and Steep are in them

The generator sets a polygon's flags to `1 << (NAV_AREA_MAX_VALUE - area)` (`TileBuilder.cpp:626-635`, called from
`src/common/mmaps_common/Generator/TileBuilder.cpp` in this tree), so a road polygon (area 7) carries **only**
`NAV_ROAD` 0x10 (`MMapDefines.h:60-80`). The round-0 `WalkFilter` included ground|water (0x05) and excluded the rest:
every road was a hole. Consequences: an open-world route could not cross a road, and a character standing on one got
"start not on the navmesh". The 9/9 Ragefire legs never hit it (no roads in an instance).

The fixed pairs (Forever):

| filter | include | exclude | meaning |
|---|---|---|---|
| `WalkFilter` (PathTo, Plan, Snap, Nearest, Raycast) | ground 0x01, steep 0x02, water 0x04, road 0x10 | magma/slime 0x08 | walkable for a bot: the server's player set minus damaging liquid |
| `GroundFilter` (`PlanNoWater`, `RaycastNoWater`) | ground 0x01, steep 0x02, road 0x10 | magma/slime 0x08 | same, a lake is not walkable |
| `ServerPlayerFilter` | ground, steep, water, magma/slime, road (0x1f) | none | `PathGenerator::CreateFilter`, for comparison |

The MaNGOS pairs are unchanged (`IncludeFlags`/`ExcludeFlags`), so the 1.12 plans do not move.

**Steep, decided deliberately.** `PathGenerator::CreateFilter`'s player branch includes `NAV_GROUND_STEEP` since
2026-05-21 with the reason in the comment: excluding it "only ever blocked START/END resolution, never improved walk
safety" (a bot on a steep river bank, and a ghost at a spirit healer standing on a steep polygon, got NoPath from a
fully connected mesh), and what discourages routing *through* steep ground is the server's own slope cost
(`SetSlopeCoefficient`, `PathGenerator.cpp:1016-1018`), not the filter. This package's Detour is a plain
`dtQueryFilter` with unit costs, so including steep can make the client cross a 55-70° slope
(`TileBuilder.cpp:289-296` splits ground at 55° and clears above 70°) the server would merely price up. The
alternative was worse: excluding it makes the client report a false "not on the navmesh" for a position the server
can path from. The safe filter therefore excludes only the damaging liquids; the tradeoff is named here, and the
dungeon driver's default is this safe filter (the lead's decision), i.e. lava and slime out, everything the server
lets a player stand on in.

### The real tiles' flags (the table item 1 asked for)

`TestForeverRealFlagCounts` scans all 3160 files of `mmaps-70888` with a plain byte parser (no Detour) and counts the
flag word of every polygon. The headline: **no real tile carries 0x02 (steep) or 0x10 (road)**; this repack predates
those tags, so the real-data road route could not be reproduced (see below).

| map | 0x00 empty | 0x01 ground | 0x02 steep | 0x04 water | 0x08 magma/slime | 0x10 road | polygons |
|---|---|---|---|---|---|---|---|
| 0 | 7873 | 1688641 | 0 | 286715 | 4309 | 0 | 1987538 |
| 1 | 11636 | 2164013 | 0 | 351165 | 1937 | 0 | 2528751 |
| 25 | 4 | 4 | 0 | 0 | 0 | 0 | 8 |
| 29 | 4 | 4 | 0 | 0 | 0 | 0 | 8 |
| 30 | 506 | 72624 | 0 | 16 | 0 | 0 | 73146 |
| 33 | 33 | 48295 | 0 | 8786 | 0 | 0 | 57114 |
| 34 | 7 | 898 | 0 | 0 | 0 | 0 | 905 |
| 35 | 1 | 1301 | 0 | 0 | 0 | 0 | 1302 |
| 36 | 444 | 24931 | 0 | 7244 | 0 | 0 | 32619 |
| 37 | 395 | 55014 | 0 | 14883 | 0 | 0 | 70292 |
| 42 | 4 | 4 | 0 | 0 | 0 | 0 | 8 |
| 43 | 240 | 5318 | 0 | 1054 | 0 | 0 | 6612 |
| 44 | 68 | 1313 | 0 | 0 | 0 | 0 | 1381 |
| 47 | 286 | 22357 | 0 | 2311 | 0 | 0 | 24954 |
| 48 | 111 | 4307 | 0 | 1581 | 0 | 0 | 5999 |
| 70 | 185 | 3752 | 0 | 0 | 0 | 0 | 3937 |
| 90 | 989 | 5038 | 0 | 0 | 0 | 0 | 6027 |
| 109 | 269 | 3845 | 0 | 268 | 0 | 0 | 4382 |
| 129 | 210 | 8258 | 0 | 0 | 0 | 0 | 8468 |
| 169 | 2079 | 240586 | 0 | 2111 | 0 | 0 | 244776 |
| 189 | 566 | 4354 | 0 | 0 | 0 | 0 | 4920 |
| 209 | 972 | 42712 | 0 | 3628 | 0 | 0 | 47312 |
| 229 | 217 | 7126 | 0 | 0 | 208 | 0 | 7551 |
| 230 | 497 | 10312 | 0 | 0 | 1992 | 0 | 12801 |
| 249 | 323 | 956 | 0 | 0 | 253 | 0 | 1532 |
| 269 | 586 | 150833 | 0 | 4296 | 0 | 0 | 155715 |
| 289 | 12 | 54156 | 0 | 770 | 0 | 0 | 54938 |
| 309 | 1142 | 120210 | 0 | 1745 | 0 | 0 | 123097 |
| 329 | 566 | 68215 | 0 | 94 | 80 | 0 | 68955 |
| 349 | 591 | 6345 | 0 | 2804 | 0 | 0 | 9740 |
| 369 | 939 | 3485 | 0 | 0 | 0 | 0 | 4424 |
| 389 | 290 | 2760 | 0 | 0 | 551 | 0 | 3601 |
| 409 | 281 | 4496 | 0 | 0 | 2536 | 0 | 7313 |
| 429 | 1696 | 13924 | 0 | 54 | 0 | 0 | 15674 |
| 449 | 35 | 236 | 0 | 0 | 0 | 0 | 271 |
| 450 | 242 | 1676 | 0 | 0 | 0 | 0 | 1918 |
| 469 | 198 | 67942 | 0 | 233 | 459 | 0 | 68832 |
| 489 | 476 | 19450 | 0 | 4874 | 0 | 0 | 24800 |
| 509 | 417 | 118305 | 0 | 2633 | 0 | 0 | 121355 |
| 529 | 156 | 32308 | 0 | 917 | 0 | 0 | 33381 |
| 531 | 1067 | 24917 | 0 | 304 | 129 | 0 | 26417 |
| 533 | 375 | 135660 | 0 | 60 | 5421 | 0 | 141516 |
| 2720 | 85 | 35782 | 0 | 0 | 0 | 0 | 35867 |
| 2784 | 24 | 60826 | 0 | 2206 | 0 | 0 | 63056 |
| 2789 | 45 | 38412 | 0 | 407 | 0 | 0 | 38864 |
| 2791 | 433 | 53167 | 0 | 11522 | 0 | 0 | 65122 |
| 2804 | 242 | 44176 | 0 | 1754 | 0 | 0 | 46172 |
| 2806 | 62 | 49867 | 0 | 1379 | 587 | 0 | 51895 |
| 2807 | 31 | 123763 | 0 | 4528 | 0 | 0 | 128322 |
| 2817 | 31 | 35701 | 0 | 4 | 0 | 0 | 35736 |
| 2832 | 246 | 180648 | 0 | 4569 | 0 | 0 | 185463 |
| 2853 | 28 | 72661 | 0 | 2191 | 0 | 0 | 74880 |
| 2856 | 802 | 182100 | 0 | 1231 | 242 | 0 | 184375 |
| 2868 | 26 | 78008 | 0 | 1749 | 17 | 0 | 79800 |
| 2875 | 28 | 73434 | 0 | 2287 | 0 | 0 | 75749 |
| 2902 | 182 | 118452 | 0 | 223 | 0 | 0 | 118857 |
| 2921 | 375 | 135657 | 0 | 60 | 5421 | 0 | 141513 |
| 2959 | 236 | 185752 | 0 | 11338 | 859 | 0 | 198185 |
| 2970 | 317 | 16555 | 0 | 18036 | 0 | 0 | 34908 |
| 2974 | 258 | 37541 | 0 | 933 | 0 | 0 | 38732 |
| 2980 | 233 | 131259 | 0 | 6283 | 757 | 0 | 138532 |
| 2991 | 2129 | 63876 | 0 | 3492 | 0 | 0 | 69497 |
| 2994 | 20 | 940 | 0 | 0 | 0 | 0 | 960 |
| 2995 | 457 | 134238 | 0 | 1218 | 183 | 0 | 136096 |
| 2996 | 432 | 17152 | 0 | 4720 | 0 | 0 | 22304 |
| 2997 | 902 | 24815 | 0 | 15849 | 0 | 0 | 41566 |
| 2998 | 203 | 89119 | 0 | 4645 | 0 | 0 | 93967 |
| 2999 | 15 | 46433 | 0 | 1800 | 1817 | 0 | 50065 |
| 3001 | 0 | 113085 | 0 | 3036 | 493 | 0 | 116614 |
| 3005 | 492 | 30374 | 0 | 4706 | 0 | 0 | 35572 |
| 3021 | 523 | 153979 | 0 | 3441 | 664 | 0 | 158607 |
| 3039 | 0 | 109434 | 0 | 4332 | 0 | 0 | 113766 |
| 3065 | 956 | 2341 | 0 | 8 | 0 | 0 | 3305 |
| 3104 | 0 | 0 | 0 | 775 | 0 | 0 | 775 |
| **all 74 maps** | 46801 | 7680428 | 0 | 817268 | 28915 | 0 | 8573412 |

`0x00` is an empty (unwalkable/pruned) polygon, `0x08` magma/slime. The scan asserts that no polygon carries a bit
outside `0x1f`. `TestForeverRealRoadRoute` looks for a road polygon in maps 0/1 and, finding none, skips with
"no NAV_ROAD polygon in maps 0/1 of this set (1724 tiles scanned)". The road filter is still covered by
`TestForeverWalkFilterKeepsRoadsAndSteepWalkable` on a Forever fixture: the old ground|water pair answers
`none` ("start ... not on the navmesh") for a corridor whose polygons carry only 0x10 (and, separately, only 0x02),
and the fixed `WalkFilter` plans it complete with the corridor flags naming `road`/`steep`. `Snap` on the same
start succeeds. **Data gap, not a code gap: a real road route needs a repack generated with road tagging.**

## Round 1: corrupt files are errors, not crashes

* **Params (`format.go:97-129`, `validateParams`).** `ParseMmap` now decodes the 28-byte `dtNavMeshParams` and
  refuses, before `nd_create`, a non-finite or out-of-world origin (|y|,|x| > 20,000 yd, |z| > 20,000; the repack
  runs -16,000..+5,867), a tile size that is NaN, ±Inf, ≤0 or > 4,000 yd (real: 533.33), `maxTiles` outside
  1..4096 (the ceiling: `dtNavMesh::init` allocates and zeroes `sizeof(dtMeshTile)*maxTiles`,
  `DetourNavMesh.cpp:237-243`; the largest real map has 988 tiles; the 64-bit ref layout itself allows
  `1<<DT_TILE_BITS` = 2,097,152, `DetourNavMesh.h:55-57`), and a negative `maxPolys`.
* **`maxPolys` cannot be required positive.** All 56 numeric 1.12 `.mmap` files on this machine carry 0 (the two `go*.mmap` files do not parse as navmesh params and are not map files), and
  Forever's generator writes `1<<DT_POLY_BITS` into the signed field (`MapBuilder.cpp:444,476`), i.e. -2147483648
  in the file. This build is `DT_POLYREF64` and never reads the field (`DetourNavMesh.cpp:254-263` is under
  `#ifndef DT_POLYREF64`), so 0 and the generator's 1<<31 are accepted and any other negative is not.
* **Bounded reads (`planner.go:565-654`).** One `readMmapFile` and one `readTileFile` now serve `mesh()`,
  `ensureTile()`, `fetch()` and `load()`: stat first, `.mmap` ≤ 1 MiB and tile ≤ 8 MiB (the largest real tile is
  3,490,372 bytes), then read only the 20-byte tile header and check magic, Detour version 7, generator version and
  the claimed payload size against the file's, then the one bounded read. A sparse 5 GiB tile fails from the stat
  (`ErrFileTooLarge`) without an allocation; a truncated tile fails with its name and version.
* **Tile data (`navdetour.cpp`, `validateTile`, `-5`).** Before `dtNavMesh::addTile` the client checks every index
  Detour will follow: polygon `vertCount` (3..6 ground, 2 off-mesh) and vertex indices below `vertCount`; edge
  neighbours (0, a local index+1 below `polyCount`, or an external link whose direction is 0..7, which
  `connectExtLinks` indexes `dtOppositeTile` with); each ground polygon's detail ranges inside
  `detailVertCount`/`detailTriCount` and triangle indices below `vertCount + detail.vertCount`; the BV tree (a leaf
  below `polyCount`, an escape landing inside `bvNodeCount`); each off-mesh connection's poly inside
  `offMeshBase..offMeshBase+offMeshConCount`; and `maxLinkCount >= 1` (`addTile` writes `links[maxLinkCount-1]`,
  `DetourNavMesh.cpp:1002`). This mirrors MMapManager's own `ValidateTileLinkIntegrity` (`MMapManager.cpp:469-528`)
  for the link chains it walks after a load. Tests: `TestParseMmapRejectsBadParams`,
  `TestOversizedFilesAreRefusedWithoutReading`, `TestCorruptTileIndicesAreRefused` (vertex, internal link, external
  direction, detail triangle/vertex range, empty link array).

## Round 1: the instance (parent map) rule

The server loads a tile for a map from `{mapId}_{x}_{y}.mmtile`; when that file does not exist it takes the parent
map's file with the same grid and adds it to the instance's own mesh (`MMapManager.cpp:349-357`). The parent table is
built once from every `MapEntry`: `World.cpp:1443-1461` puts each child under its `ParentMapID` (or
`CosmeticParentMapID` when that is the one set), and `MMapManager.cpp:211` stores `parentMapData[child] = parent`.
It is one hop, not a chain (the *terrain* map is found transitively in `TerrainMgr.cpp:1033-1045`, but the mmap
fallback is the direct parent).

The client gets the table from the caller: `forever path-check --parent MAP=PARENT[,..]` and
`forever walk-check --parent MAP=PARENT[,..]` (`--parent 389=1`). The pairs are validated (a bare number is
refused: the pair names which map it is for; a map cannot be its own parent; a child may appear once).
`internal/forever/world.Options.Parents` carries them into `walkCheckPath`, which no longer builds the planner with
`nil` parents. The end-to-end test `TestWalkCheckNavmeshFallsBackToTheInstanceParentTile` puts the character on map
389 (the create block's map) with a folder holding only `0389.mmap` and map 0's tile: without the pair the run is
refused ("no forever-v16 tile files for map 389"), with `389=0` the route is complete and the walk runs;
`TestForeverPathCheckInstanceNeedsTheParent` does the same through the command. There is **no committed table**: the
Map DB2 is client data, not in this repository, and the only public per-map data here (`dungeons/*.json`'s
`portal.map`) is the outdoor map where the entrance object stands, not `MapEntry.ParentMapID`, so deriving the table
from it would be a guess. Option (i) only, as the brief allowed.

## Round 1: WalkPath's clock

`Mover.WalkPath` used to restart each segment's clock at the moment a corner packet was actually sent. With a
4-yard floor sample every 0.57 s at 7 yd/s plus 2 heartbeats/s, a densified route sits at the four-packets-a-second
cap, the corner packet is regularly a window late, and that lateness accumulated; the deadline
(length/speed + 10 s) then fired "did not finish" mid-route. Changes:

* One monotonic timeline for the whole polyline (start + walked/runSpeed, `place()` puts the mover there), so a late
  packet never pushes the rest back; the stop's timestamp is still the computed arrival.
* Only real direction changes (`orientationDiff > chaseTurn`) get a `CMSG_MOVE_SET_FACING`; the densified Z samples
  shape the positions the heartbeats carry and get no packet. Corners the cap holds back are faced at the next
  window, from the position the route is at then (never a stale one).
* The cap check now reads the oldest packet's slot (`sendTimes[sendIdx%4]`); it read the second-oldest, waiting
  longer than needed. The final stop waits for its slot at the cap, so the walk never sends five movement packets in
  one second.
* `walkPathTimeout` adds one packet-cap window to the travel time plus the 10 s margin (still capped at 30 min).
  Tests: 200 turning corners (cap held on every rolling second, walk completes), a teleport mid-walk
  (`ErrTeleported`, stop at the server's destination, no packet from the old line), a cancelled context (one stop
  at the interpolated position), and a link death mid-walk through the session (`ErrLinkLost` at the next wait, the
  stop refused by the dead socket).

## Round 1: PathTo and the server's own path — known differences

* **Filter costs.** The server's default is a smooth path built with `findSmoothPath` (4-yd steps, "slop" 0.3-0.4)
  over a corridor whose `dtQueryFilterTC` prices slope (`Pathfinding.SlopeCoefficient`, bot config 1.0 => a 30°
  slope costs 1.30x) and, when enabled, roads (area cost < 1). This client uses plain Detour with unit costs and
  returns the straight-path corners, so a route can prefer a steeper or longer line than the server's smooth one and
  vice versa. Both walk the same corridor polygons.
* **Node pool.** `PathTo`/`nd_route` now uses `ROUTE_NODES = 8192` (`MMapManager.cpp:39`, the server's
  `NAV_MESH_QUERY_MAX_NODES`), so the complete/partial verdict is the server's rather than the 65,535-node one's.
  The 65,535 pool remains only for `nd_find_path`, i.e. the 1.12 `Planner.Plan` (the offline plan/dungeon tooling
  whose behaviour must not change), and `nd_server_path` keeps the 1.12 core's own 2048-node query
  (`MoveMap.cpp:333`). The pool size does not change the returned points, only whether a long search reports
  complete or partial.
* **Starts.** Routes start from the snapped start (`PathGenerator::GetPolyByLocation`'s 3x5x3 then 3x50x3 box and
  `PreferPolyAtHeight`), not from the asked position; the same holds for the goal, with the server's 7-yd
  far-from-poly rule.



## The server's filter and constants (the path this client must be able to walk)

`PathGenerator::CreateFilter` (`PathGenerator.cpp:923-986`), player branch (`:971-975`): include
`NAV_GROUND | NAV_GROUND_STEEP | NAV_WATER | NAV_MAGMA_SLIME`, plus `NAV_ROAD` (`:983`); exclude nothing. Because
the server includes lava, a client that wants a safe route uses the package's walk filter, and the server-filter
route is available through `PathToFilter(mapID, from, to, Format.ServerPlayerFilter())` for comparisons.

Path constants (`PathGenerator.h:30-44`, `:128-137`): a bot player gets 512 polygons and 512 points (74 for other
units); smooth step 4.0 yd, slop 0.3 yd. The client's `PathTo` requests straight-path corners, so it does not
reproduce the smooth walk; the corner path is the same corridor.

Polygon lookup (`PathGenerator::GetPolyByLocation`, `PathGenerator.cpp:329-359`): 3 yd round x 5 yd high x 3 yd
round, then 3 x 50 x 3; `PreferPolyAtHeight` (`:350-370`) picks the stacked floor nearest the unit's height. The
far-from-poly rule is 7 yd (`:419-420`); a far goal is clamped to its polygon and the path is `PATHFIND_INCOMPLETE`.
`navmesh.Planner.Snap` uses the two boxes; `PathTo` uses the same lookup in `nd_route`.

## What is approximated

* **Corner path, not the smooth path.** `PathTo` returns Detour `findStraightPath` corners. The server's
  `FindSmoothPath` (`PathGenerator.cpp:1180-1410`) moves along the surface in 4-yd steps and adds +0.5 yd to each
  point's height. Corners are the same corridor and the same polygons; a walker that interpolates Z between them
  would cut a hill, so `Route.Points` inserts a floor sample every 4 yd along every corner leg.
* **No Z fix-up of the kind `NormalizePath` does** (`PathGenerator.cpp:850-890`): the client has no terrain height
  probe or vmaps, only the mesh.
* **Off-mesh connections** are in the corridor but the walker has no jump/teleport handling for them; the route
  reports their polygons' flags but does not split at them.
* **Detour's `PreferPolyAtHeight`** is ported, but the client has no liquid status query, so the server's
  `UpdateFilter` water-start widening (`PathGenerator.cpp:1041-1060`) has no equivalent.

## Ragefire Chasm plan table

Filled by the env-gated real-data test over `dungeons/ragefire_chasm.json`: the entrance and each leg's `to`, in
order, every adjacent pair.

Run with `FOREVER_MMAPS_DIR` set and `go test -v -run TestForeverRealMap389 ./internal/navmesh`: entrance plus each
leg's `to`, every adjacent pair (9 pairs).

| pair | result | corners | length |
|---|---|---|---|
| entrance -> tunnel | complete | 2 | 0.0 yd |
| tunnel -> tunnel_end | complete | 4 | 105.9 yd |
| tunnel_end -> trogg_cave | complete | 2 | 61.2 yd |
| trogg_cave -> oggleflint | complete | 8 | 230.8 yd |
| oggleflint -> trogg_west | complete | 6 | 98.7 yd |
| trogg_west -> molten_cave | complete | 16 | 240.0 yd |
| molten_cave -> taragaman | complete | 12 | 107.8 yd |
| taragaman -> jergosh | complete | 11 | 149.8 yd |
| jergosh -> bazzalan | complete | 8 | 130.9 yd |

All 9 are complete (re-run in round 1 with the fixed filters and the corrected Detour: same table). The
entrance-to-Oggleflint route the test checks separately is complete, 12 corners, 460.1 yd, all flags ground.

### Pull-group approach points (item 8's extension)

`TestForeverRealMap389ApproachPoints` walks `dungeons/ragefire_chasm.json` group by group: each group's `approach`
entry (its first one), or its first mob `position` when the plan gives no approach, planned from the leg's start and
then from the previous group's point. The leg's start is the previous leg's `to` (the entrance for the first leg).
Safe walk filter, map 389 with `389: 0` as the parent. **51 rows, 51 complete, 0 partial, 0 none.**

| from | to | result | corners | length | straight line |
|---|---|---|---|---|---|
| start(tunnel) | tunnel.g1 | complete | 3 | 46.5 yd | 45.6 yd |
| tunnel.g1 | tunnel.g2 | complete | 2 | 14.3 yd | 14.3 yd |
| tunnel.g2 | tunnel.g3 | complete | 2 | 23.7 yd | 23.7 yd |
| tunnel.g3 | tunnel.g4 | complete | 2 | 16.7 yd | 16.7 yd |
| tunnel.g4 | tunnel.g5 | complete | 2 | 26.5 yd | 26.2 yd |
| start(tunnel_end) | tunnel_end.g1 | complete | 4 | 116.4 yd | 95.4 yd |
| tunnel_end.g1 | tunnel_end.g2 | complete | 2 | 40.6 yd | 40.1 yd |
| start(trogg_cave) | trogg_cave.g1 | complete | 2 | 86.2 yd | 84.8 yd |
| trogg_cave.g1 | trogg_cave.g2 | complete | 2 | 43.3 yd | 43.2 yd |
| trogg_cave.g2 | trogg_cave.g3 | complete | 2 | 17.0 yd | 16.9 yd |
| trogg_cave.g3 | trogg_cave.g4 | complete | 4 | 67.1 yd | 66.0 yd |
| trogg_cave.g4 | trogg_cave.g5 | complete | 5 | 30.4 yd | 16.8 yd |
| trogg_cave.g5 | trogg_cave.g6 | complete | 10 | 104.1 yd | 86.0 yd |
| trogg_cave.g6 | trogg_cave.g7 | complete | 2 | 11.0 yd | 11.0 yd |
| trogg_cave.g7 | trogg_cave.g8 | complete | 3 | 37.7 yd | 35.5 yd |
| start(oggleflint) | oggleflint.g1 | complete | 9 | 244.3 yd | 47.7 yd |
| start(trogg_west) | trogg_west.g1 | complete | 6 | 137.7 yd | 117.9 yd |
| trogg_west.g1 | trogg_west.g2 | complete | 2 | 10.1 yd | 10.1 yd |
| trogg_west.g2 | trogg_west.g3 | complete | 2 | 27.1 yd | 26.9 yd |
| trogg_west.g3 | trogg_west.g4 | complete | 13 | 91.8 yd | 33.5 yd |
| trogg_west.g4 | trogg_west.g5 | complete | 2 | 20.5 yd | 20.5 yd |
| trogg_west.g5 | trogg_west.g6 | complete | 2 | 25.0 yd | 24.8 yd |
| start(molten_cave) | molten_cave.g1 | complete | 20 | 293.9 yd | 177.3 yd |
| molten_cave.g1 | molten_cave.g2 | complete | 2 | 16.9 yd | 16.9 yd |
| molten_cave.g2 | molten_cave.g3 | complete | 2 | 9.0 yd | 9.0 yd |
| molten_cave.g3 | molten_cave.g4 | complete | 2 | 34.2 yd | 34.1 yd |
| molten_cave.g4 | molten_cave.g5 | complete | 2 | 18.0 yd | 18.0 yd |
| molten_cave.g5 | molten_cave.g6 | complete | 2 | 16.8 yd | 16.8 yd |
| molten_cave.g6 | molten_cave.g7 | complete | 2 | 17.5 yd | 17.4 yd |
| molten_cave.g7 | molten_cave.g8 | complete | 2 | 32.0 yd | 32.0 yd |
| start(taragaman) | taragaman.g1 | complete | 13 | 141.4 yd | 101.7 yd |
| taragaman.g1 | taragaman.g2 | complete | 2 | 25.5 yd | 25.4 yd |
| taragaman.g2 | taragaman.g3 | complete | 3 | 23.8 yd | 20.6 yd |
| taragaman.g3 | taragaman.g4 | complete | 2 | 49.0 yd | 48.4 yd |
| taragaman.g4 | taragaman.g5 | complete | 2 | 11.6 yd | 11.6 yd |
| start(jergosh) | jergosh.g1 | complete | 11 | 154.3 yd | 123.4 yd |
| jergosh.g1 | jergosh.g2 | complete | 2 | 21.5 yd | 21.3 yd |
| jergosh.g2 | jergosh.g3 | complete | 2 | 14.3 yd | 14.3 yd |
| jergosh.g3 | jergosh.g4 | complete | 2 | 51.2 yd | 50.0 yd |
| jergosh.g4 | jergosh.g5 | complete | 2 | 20.2 yd | 20.2 yd |
| jergosh.g5 | jergosh.g6 | complete | 2 | 32.8 yd | 32.7 yd |
| jergosh.g6 | jergosh.g7 | complete | 2 | 25.1 yd | 25.0 yd |
| jergosh.g7 | jergosh.g8 | complete | 2 | 11.0 yd | 11.0 yd |
| start(bazzalan) | bazzalan.g1 | complete | 10 | 139.2 yd | 112.4 yd |
| bazzalan.g1 | bazzalan.g2 | complete | 3 | 42.2 yd | 39.6 yd |
| bazzalan.g2 | bazzalan.g3 | complete | 3 | 42.1 yd | 41.7 yd |
| bazzalan.g3 | bazzalan.g4 | complete | 2 | 25.6 yd | 25.5 yd |
| bazzalan.g4 | bazzalan.g5 | complete | 2 | 22.8 yd | 22.8 yd |
| bazzalan.g5 | bazzalan.g6 | complete | 2 | 28.3 yd | 28.3 yd |
| bazzalan.g6 | bazzalan.g7 | complete | 3 | 36.4 yd | 35.9 yd |
| bazzalan.g7 | bazzalan.g8 | complete | 2 | 31.8 yd | 31.8 yd |

The rows whose length is far above the straight line are the plan's own known geometry, not a pathing defect: the
Oggleflint approach is 244.3 yd for 47.7 yd as the crow flies (the terrace packs stand across the cave floor that
the plan connects only through the long way round), trogg_west.g4 is 91.8 yd for 33.5 (the ramp), molten_cave.g1 is
293.9 yd for 177.3 (the entrance-to-molten-cave traverse from the previous `to`), and the four other `start(...)`
rows (tunnel_end 116.4, trogg_cave 86.2, taragaman 141.4, jergosh 154.3, bazzalan 139.2) are whole-leg traverses from
the previous leg's end. The plan's approach points are all ON the mesh and reachable; the driver still has to budget
those longer approaches as real walking time.

**Default filter for the dungeon driver: the safe walk filter (`Format.WalkFilter`), the lead's decision.** It
excludes magma/slime (`NAV_MAGMA_SLIME`, 0x08; `Forever.Terrain().Liquid()`) and nothing else: ground, steep, water
and road are all walkable, so a road or a river bank is never a false "not on the navmesh". A route it returns is
one the server's movement checks accept; it does not route through the flagged lava of the trogg basin or the
tunnel's shelf, which is what the round-3 walk filter was for. `--server-filter` (all 0x1f, lava included) is for
comparison only.

### The other real-data results

Round 1 re-ran all of these with the fixed filters and the corrected Detour: the numbers below are identical
(the data has no road or steep polygon, and the Detour edits change no result on these routes).

* Map 0, human starting area: a 61.1-yd route between two points 60 yd apart is complete, 2 corners, 16 points
  after Z resampling; every point snaps back to the mesh within 0.2 yd, and the points' Z departs from the
  end-to-end interpolation by up to 2.20 yd (the hill the straight line would cut through).
* Map 389, the lava check: with the walk filter the same pair across the trogg cave's basin is complete in 3
  corners and 68.4 yd, corridor flags `ground` only. With `PathToFilter(..., ServerPlayerFilter())` (the server's
  own player filter, lava included) the route is complete in 18 corners and 213.5 yd and its corridor flags are
  `ground|magma`. The safe route is also the shorter one here.
* Map 389 loads its own `0389.mmap` and four tiles (`0389_31_31` .. `0389_32_32`); it does not need the parent
  fallback on this data. The fallback is exercised by a synthetic test (an instance header with no tiles, a parent
  tile loaded into it).

## Client wiring

* `forever path-check --navmesh DIR --map N --from X,Y,Z --to X,Y,Z [--parent MAP=PARENT[,..]] [--server-filter]`
  is offline: the map header, the tiles, the route kind, every point with Z, the length and the corridor's flags.
  `--parent` takes the instance pairs (`--parent 389=1,36=0`) and validates them; there is no default.
* `forever walk-check --navmesh DIR --to-x X --to-y Y --to-z Z [--parent MAP=PARENT[,..]]` plans the same route on
  the map the character is on (the create block's map) and walks `Route.Points` through `Mover.WalkPath`: one start
  forward, a facing packet where the route turns (the 4-yard Z samples get no packet), heartbeats every 500 ms, one
  stop, at most four movement packets a second. The fake world server test shows the sequence for the three-corner
  L route and checks each step against speed x elapsed time; the instance test walks a map-389 route whose only
  tile is map 0's.

## Open questions for the dungeon driver

* A real-data road route is still missing: this repack has no NAV_ROAD polygon (the flag table above), so only the
  fixture proves the fix. A repack generated with road tagging should run `TestForeverRealRoadRoute`.
* The approach rows above are planned with the safe filter and no avoid boxes; the four `start(...)` rows are whole
  leg traverses (up to 293.9 yd), so a driver's pull schedule has to walk them, not assume they are next to the leg.
* The plan's avoid boxes were written for a mesh that left shallow lava under the basin as ground. This repack
  flags the deep basin (551 magma polygons), but a rim under the surface can still be ground, so `SetAvoid` with the
  plan's boxes is still the guard. `PathTo` does not take them by default; the driver must call `SetAvoid` before
  planning.
* The server's player filter includes lava, so a plan that is "complete" under `ServerPlayerFilter` is not
  necessarily a plan a bot should walk. Answered for now: the driver's default is the safe walk filter (the lead's
  decision; see above). The remaining question is whether the client should reproduce the server's slope and road
  area costs, which would change which of two equally walkable routes it takes.
