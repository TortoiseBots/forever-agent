# GM-WALK: world data from a GM walk (issue #57)

`forever gm-walk` records a GM character's walk as world data: the walked path as road polylines (thinned,
with the map id), points the GM marks, and danger zones as circles. No server change: the GM marks by
whispering the recording agent (or saying the word nearby), and the agent watches the chat it already reads.

## Mark words

One line is one mark. The keyword is case-insensitive; the name keeps its case.

* `point <name>` — a point of interest at the GM's position now.
* `zone <name> <radius-yd>` — a danger circle at the GM's position now (radius 1–500 yd).
* `split` — cut the road here (a new polyline starts at the next sample).

Names: `[A-Za-z0-9._-]`, at most 64 bytes, no leading `.`/`-`, no `..`, no duplicates (one name names one
mark: names become SQL comments and file stems). Anything else — a refusal, a line past 256 bytes, a bad
radius, a mark with no GM position yet — is counted (`unknown`, `ignored`), never stored, never fatal.

`--marks-from gm` (default) takes marks only from the followed GM, matched by the chat line's sender GUID
against the resolved GM guid: a stranger using the same words — even with the GM's name on the line — and a
line with a faked sender name are unknown chatter, counted, never stored. Names are never consulted.
`--marks-from any` takes any sender's whisper or nearby say (the mark still lands at the followed GM's object
position). Only whispers and say lines are marks; party lines and system lines are not.

## The walk

`forever-agent forever gm-walk --follow NAME --character AGENT ...` enters the world with the agent, resolves
the GM from the names the model learns (up to 2 min), then follows at `--follow-yd` yards (default 6) with
the client's own chase movement, sampling the GM's own object position from the world model every `--sample`
(default 2 s) and feeding every chat line through the mark tap. The road is the walk the GM walked, not the
agent's chase path: marks land at the GM's object position when the line arrived, with no fallback to the
agent's position (a GM out of the model means the mark is ignored, never stamped elsewhere). The map id and
the coordinates of a sample come from the same update: while a transfer is open the client stands between
maps, so the sample is skipped, never guessed — no stray road crosses a transfer. It ends on the timeout
(`--timeout`, default 60 min), Ctrl-C, or 90 s without movement (the GM stopped walking). Past `MaxRoads`
(256) roads the run ends with the error: the walk no longer fits, and a silent truncation would lose its tail.

The agent never commands, never teleports, never marks itself: its only sends are movement and the login
burst. GM mode is not needed (the agent walks, nothing is spawned); `.gm` is never sent.

## Output

`--out FILE` (default `gm-walk-<map>-<utc-timestamp>.json`, e.g. `gm-walk-0-20261121T120405Z.json`) holds the
world data:

```json
{
  "version": 1,
  "roads": [{"map": 0, "points": [[x, y, z], ...]}],
  "points": [{"name": "gate", "map": 0, "x": 1, "y": 2, "z": 3}],
  "zones": [{"name": "lava", "map": 0, "x": 1, "y": 2, "z": 3, "radius_yd": 25}]
}
```

* `version` is 1; anything else is refused on read.
* Roads are the walk thinned by Ramer-Douglas-Peucker at `--tolerance` yards (default 1.0): one polyline per
  walked stretch, cut at map changes, `split` marks, jumps past 200 yd (a teleport, not a walk), and thinned
  runs past 4096 points (a new road starts where the last ended, sharing the joint point and keeping the
  original final endpoint — split, never dropped).
* Coordinates are server units (yards, WoW coordinates), with the map id per road/point/zone.
* No character names anywhere: point and zone names are the GM's labels; file names carry the map id and a
  UTC timestamp; the run report carries the map id, never who walked. The result line reads
  `gm-walk: walked map M for ...`, with no name.
* Every writer refuses to overwrite an existing file unless `--force`: a second walk never destroys earlier
  world data by accident. `--out` (and `--module-out`) must name a file in a folder that already exists: the
  writer creates no directories.

`--module-out FILE` (and `forever gm-convert --in DATA --out COPY --module-out MODULE`, no login) renders the
module's shape: one row per road hop (`mapId fromX fromY toX toY width_yd comment verified`) for the
`handcrafted_road` table the worldserver loads (`HandcraftedRoadStorage`, one straight `from→to` segment per
row, width 8 yd, never `verified` — a walk is raw material, a human marks it verified on the module side),
plus the zone and point proposals (`mapId x y z radius_yd` / `name`) the module has no loader for yet. A
one-point road renders no row (the loader's segments need two ends): the converter reports the dropped hop
count on stdout, never fakes it. The module writer validates its rows as its comment says: finite in-bound
coordinates, mark-rule names, radii in range, no duplicate names, never verified.

## Format spec (for the module side to adopt)

The table the converter targets exists: `handcrafted_road(id, mapId, fromX, fromY, toX, toY, width,
comment, verified)` in the shared Playerbot database, loaded once at world start and applied as
`NAV_AREA_ROAD` tags on the detour mesh. The converter emits exactly one row per road hop, with `width_yd`
8.0, `verified` false, and `comment` `gm-walk road <i> hop <j>`. The table carries no Z: ground height comes
from the mesh at load, so the converter drops Z from hops.

Danger zones and points have no module loader yet. The proposed shape the converter emits (and the module
should adopt) is one JSON object per zone — `name mapId x y z radius_yd` — and per point — `name mapId x y
z` — with the same name rules as the marks (one `[A-Za-z0-9._-]` component, ≤ 64 bytes) so they stay usable
as SQL comments and file stems.

## Bounds and breakage

File reads refuse anything past 8 MiB, non-regular files, unknown JSON fields, and trailing values; every
count (100k samples, 256 roads, 4096 points per road, 1024 marks) and every coordinate (±100000,
finite) is checked, including the dry recording's samples at read; a broken record is an error, never a
panic, and a refused write leaves no file.

## Tests

`internal/forever/gmwalk` over hand-made data (never captures): RDP thinning (straight collapses, corners
kept, shallow 2° vs 30° angles at 2 yd, hairpin, map/jump cuts, NaN/Inf evicted, road split past 4096 points
keeping the final endpoint, MaxRoads refusal), mark words and refusals (GUID gate, fake sender names, GM
object positions, duplicates), read/write round trip, hostile files (bad version, unknown fields, bad names,
bad radii, oversize, symlink target), overwrite refusal without `--force`, and the converter's rows
(one-point roads reported, verified/duplicate/bad-radius module rows refused).
`cmd/forever-agent/forever_gmwalk_test.go` replays a hand-made recording through `--dry` (points, zones,
split, unknown count, module file), pins the nameless default names, the `--force` and missing-folder
refusals, and the argument checks; `internal/forever/world/gm_walk_test.go` pins the chat tap (GUID-only vs
names, fake sender names ignored, marks at the GM's object position, any-sender still at the GM, mark with no
position) and the yards (not squared) distance.

## Known limits

* No live run yet: the follow loop, the chase movement and the whisper tap are wired but unwalked on a
  server. The live check, in order: (1) `.gm` GM walks a straight road on a flat map while the agent follows
  — the road polyline matches within tolerance; (2) the GM whispers `point`/`zone` mid-walk — the point and
  circle land at the GM's position; (3) a `split` mid-walk — two roads; (4) a map change mid-walk — the road
  cuts at the right sample.
* The 90 s idle bound ends a run where the GM stands still to mark several points; restart the walk or raise
  the follow tick by keeping the GM moving.
* Say marks only work near the agent (say range); far marks need a whisper.
* Frame thinning bounds the drawn points but not the samples held while walking (100k cap, oldest evicted).

## Gates (final commit)

On the last code commit with a clean worktree, each in the foreground and waited for:

```
make fmt-check            -> clean (no file listed)
make cross                -> CROSS-OK (windows/amd64 and darwin/arm64 builds, then their vet)
staticcheck ./...         -> rc=0 (no finding)
vm-test ta ~/wt-fa-gmwalk -> RESULT: PENDING
```

The RESULT line is quoted in the final report. The notes commit carries no code change.
