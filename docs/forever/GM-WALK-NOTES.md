# GM-WALK: world data from a GM walk (issue #57)

`forever gm-walk` records a GM character's walk as world data: the walked path as road polylines (thinned,
with the map id), points the GM marks, and danger zones as circles. No server change: the GM marks by
whispering the recording agent (or saying the word nearby), and the agent watches the chat it already reads.

## Mark words

One line is one mark. The keyword is case-insensitive; the name keeps its case.

* `point <name>` — a point of interest at the GM's position now.
* `zone <name> <radius-yd>` — a danger circle at the GM's position now (radius 1–500 yd).
* `split` — cut the road here (a new polyline starts at the next sample).

Names: `[A-Za-z0-9._-]`, at most 64 bytes, no leading `.`/`-`, no `..`. Anything else — a refusal, a line
past 256 bytes, a bad radius, a mark with no position yet — is counted (`unknown`, `ignored`), never stored,
never fatal.

`--marks-from gm` (default) takes marks only from the followed GM; `--marks-from any` takes any sender's
whisper or nearby say. Only whispers and say lines are marks; party lines and system lines are not.

## The walk

`forever-agent forever gm-walk --follow NAME --character AGENT ...` enters the world with the agent, resolves
the GM from the names the model learns (up to 2 min), then follows at `--follow-yd` yards (default 6) with the
client's own chase movement, sampling its own mover position every `--sample` (default 2 s) and feeding every
chat line through the mark tap. It ends on the timeout (`--timeout`, default 60 min), Ctrl-C, or 90 s without
movement (the GM stopped walking).

The agent never commands, never teleports, never marks itself: its only sends are movement and the login
burst. GM mode is not needed (the agent walks, nothing is spawned); `.gm` is never sent.

## Output

`--out FILE` (default `gm-walk-<follow>-<map>.json`) holds the world data:

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
  walked stretch, cut at map changes, `split` marks, and jumps past 200 yd (a teleport, not a walk).
* Coordinates are server units (yards, WoW coordinates), with the map id per road/point/zone.
* No character names anywhere: point and zone names are the GM's labels.

`--module-out FILE` (and `forever gm-convert --in DATA --out COPY --module-out MODULE`, no login) renders the
module's shape: one row per road hop (`mapId fromX fromY toX toY width_yd comment verified`) for the
`handcrafted_road` table the worldserver loads (`HandcraftedRoadStorage`, one straight `from→to` segment per
row, width 8 yd, never `verified` — a walk is raw material, a human marks it verified on the module side),
plus the zone and point proposals (`mapId x y z radius_yd` / `name`) the module has no loader for yet.

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
finite) is checked; a broken record is an error, never a panic, and a refused write leaves no file.

## Tests

`internal/forever/gmwalk` over hand-made data (never captures): RDP thinning (straight collapses, corners
kept, map/jump cuts, NaN/Inf evicted), mark words and refusals, read/write round trip, hostile files (bad
version, unknown fields, bad names, bad radii, oversize, symlink target), and the converter's rows.
`cmd/forever-agent/forever_gmwalk_test.go` replays a hand-made recording through `--dry` (points, zones,
split, unknown count, module file) and pins the argument checks; `internal/forever/world/gm_walk_test.go`
pins the chat tap (GM-only vs any, case-insensitive name, party lines ignored, mark with no position).

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
vm-test ta ~/wt-fa-gmwalk -> RESULT: PASS  kind=ta wt=wt-fa-gmwalk sha=1283b528 dirty=0 files=2346
                             rc=0  vm_duration=1226s  laptop_wall=1238s
```

The RESULT line is quoted in the final report. The full log is
`TortoiseBots-audit/vm-test-logs/wt-fa-gmwalk-20261121-061525-1084831-b17a.log` (every package ok, no FAIL).
The notes commit carries no code change.
